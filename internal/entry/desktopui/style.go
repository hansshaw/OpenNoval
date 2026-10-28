package desktopui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hansshaw/AInoval-web-ui/assets"
	"github.com/hansshaw/AInoval-web-ui/internal/bootstrap"
	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	"github.com/hansshaw/AInoval-web-ui/internal/fileio"
	"github.com/hansshaw/AInoval-web-ui/internal/host"
	"github.com/hansshaw/AInoval-web-ui/internal/host/sim"
	"github.com/hansshaw/AInoval-web-ui/internal/store"
	"github.com/hansshaw/AInoval-web-ui/internal/stylelibrary"
)

const maxStyleSourceBytes = 8 << 20

type styleSourceView struct {
	Name       string `json:"name"`
	SizeBytes  int64  `json:"sizeBytes"`
	ModifiedAt string `json:"modifiedAt,omitempty"`
}

func styleRoot(directory string) string { return filepath.Join(directory, "simulate") }

// Shared library analysis never belongs to a novel directory.
func (s *Server) styleRunningForDirectoryLocked(directory string) bool {
	return s.styleBusy && projectKey(s.styleDirectory) == projectKey(directory)
}

func (s *Server) styleLibraryErrorLocked(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, stylelibrary.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, stylelibrary.ErrInvalidID), errors.Is(err, stylelibrary.ErrInvalidName), errors.Is(err, stylelibrary.ErrUnsafePath):
		status = http.StatusBadRequest
	}
	fail(w, status, "风格库操作失败："+s.redactLocked(err.Error()))
}

func (s *Server) style(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	library, err := s.sharedStyleLibraryLocked()
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	warnings := s.migrateLegacyStyleLibrariesLocked(library)
	entries, err := library.List()
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	id := r.URL.Query().Get("entryId")
	if id == "" && len(entries) > 0 {
		id = entries[0].ID
	}
	var entry *stylelibrary.Entry
	var profile *domain.SimulationProfile
	sources := []styleSourceView{}
	if id != "" {
		value, err := library.Get(id)
		if err != nil {
			s.styleLibraryErrorLocked(w, err)
			return
		}
		entry = &value
		directory, err := library.Directory(id)
		if err != nil {
			s.styleLibraryErrorLocked(w, err)
			return
		}
		sources, err = listStyleSources(directory)
		if err != nil {
			s.styleLibraryErrorLocked(w, err)
			return
		}
		profile, err = store.NewStore(directory).Simulation.Load()
		if err != nil {
			warnings = append(warnings, "画像暂不可用，可重新蒸馏修复："+s.redactLocked(err.Error()))
			profile = nil
		}
	}
	reply(w, http.StatusOK, map[string]any{"libraryRoot": s.styleLibraryRootLocked(), "entries": entries, "entryId": id, "entry": entry, "sources": sources, "profile": domain.CompactSimulationProfile(profile), "busy": s.styleBusy && s.styleEntryID == id, "analysisRunning": s.styleBusy, "analysisEntryId": s.styleEntryID, "warnings": warnings})
}

func (s *Server) createStyleEntry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		fail(w, http.StatusConflict, "工作台正在关闭")
		return
	}
	library, err := s.sharedStyleLibraryLocked()
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	entry, err := library.Create(req.Name)
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	s.broadcastLocked("style-updated", map[string]any{"entryId": entry.ID})
	reply(w, http.StatusCreated, entry)
}

func (s *Server) uploadStyleSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EntryID   string `json:"entryId"`
		Name      string `json:"name"`
		Content   string `json:"content"`
		Directory string `json:"directory"`
	}
	if !readJSONLimit(w, r, &req, maxStyleSourceBytes+(64<<10)) {
		return
	}
	name, err := validateStyleSourceName(req.Name)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err = validateStyleSourceContent(req.Content); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || (s.styleBusy && req.EntryID == s.styleEntryID) {
		fail(w, http.StatusConflict, "请等待此风格条目的分析结束")
		return
	}
	library, err := s.sharedStyleLibraryLocked()
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	directory, err := library.Directory(req.EntryID)
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	root, err := ensureStyleRoot(directory)
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	if err = writeStyleSource(filepath.Join(root, name), []byte(req.Content)); err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	if err = store.NewStore(directory).Simulation.DeleteProfile(); err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	s.broadcastLocked("style-updated", map[string]any{"entryId": req.EntryID})
	reply(w, http.StatusOK, map[string]any{"ok": true, "entryId": req.EntryID, "name": name, "sizeBytes": len([]byte(req.Content))})
}

func (s *Server) runStyleAnalysis(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EntryID   string `json:"entryId"`
		Directory string `json:"directory"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	if s.closed || s.styleBusy {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "已有风格分析正在运行，请等待分析结束")
		return
	}
	library, err := s.sharedStyleLibraryLocked()
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		s.mu.Unlock()
		return
	}
	directory, err := library.Directory(req.EntryID)
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		s.mu.Unlock()
		return
	}
	cfg := bootstrap.CloneConfig(s.baseCfg)
	cfg.OutputDir = directory
	cfg.AcquireModelRequest = nil
	cfg.StyleReferenceProfiles = nil
	if err = cfg.ValidateBase(); err != nil {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "请先在模型与连接设置中配置分析使用的默认模型")
		return
	}
	sources, err := listStyleSources(directory)
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		s.mu.Unlock()
		return
	}
	if len(sources) == 0 {
		s.mu.Unlock()
		fail(w, http.StatusBadRequest, "请先为此风格条目添加语料")
		return
	}
	entry, err := library.Get(req.EntryID)
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		s.mu.Unlock()
		return
	}
	if entry.ProfileError != "" {
		if err := store.NewStore(directory).Simulation.DeleteProfile(); err != nil {
			s.styleLibraryErrorLocked(w, err)
			s.mu.Unlock()
			return
		}
	}
	bundle := assets.Load(cfg.Style, s.assetLoadOptions(""))
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.styleCancel, s.styleDone, s.styleDirectory, s.styleBusy, s.styleEntryID = cancel, done, directory, true, req.EntryID
	events, err := host.RunStyleAnalysis(ctx, cfg, bundle)
	if err != nil {
		cancel()
		s.styleCancel, s.styleDone, s.styleDirectory, s.styleBusy, s.styleEntryID = nil, nil, "", false, ""
		close(done)
		s.styleLibraryErrorLocked(w, err)
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer close(done)
		defer cancel()
		finished := false
		for ev := range events {
			event := map[string]any{"entryId": req.EntryID, "stage": ev.Stage, "current": ev.Current, "total": ev.Total, "message": ev.Message, "time": ev.Time}
			s.mu.Lock()
			if ev.Err != nil {
				event["error"] = s.redactLocked(ev.Err.Error())
			}
			kind := "style-progress"
			switch ev.Stage {
			case sim.StageDone:
				kind, finished = "style-done", true
				if profile, loadErr := store.NewStore(directory).Simulation.Load(); loadErr == nil {
					event["profile"] = domain.CompactSimulationProfile(profile)
				} else {
					event["warning"] = s.redactLocked(loadErr.Error())
				}
			case sim.StageError:
				kind, finished = "style-error", true
			}
			s.broadcastLocked(kind, event)
			s.mu.Unlock()
		}
		s.mu.Lock()
		if s.styleDone == done {
			if !finished && ctx.Err() == nil {
				s.broadcastLocked("style-error", map[string]any{"entryId": req.EntryID, "error": "分析未收到完成事件"})
			}
			s.styleCancel, s.styleDone, s.styleDirectory, s.styleBusy, s.styleEntryID = nil, nil, "", false, ""
			s.broadcastLocked("style-updated", map[string]any{"entryId": req.EntryID})
		}
		s.mu.Unlock()
	}()
	reply(w, http.StatusAccepted, map[string]any{"ok": true, "entryId": req.EntryID})
}

func (s *Server) deleteStyleSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EntryID   string `json:"entryId"`
		Name      string `json:"name"`
		Directory string `json:"directory"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name, err := validateStyleSourceName(req.Name)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || (s.styleBusy && s.styleEntryID == req.EntryID) {
		fail(w, http.StatusConflict, "请等待此风格条目的分析结束")
		return
	}
	library, err := s.sharedStyleLibraryLocked()
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	directory, err := library.Directory(req.EntryID)
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	root, err := ensureStyleRoot(directory)
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	target := filepath.Join(root, name)
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		fail(w, http.StatusNotFound, "语料文件不存在")
		return
	}
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		fail(w, http.StatusBadRequest, "只能删除普通语料文件")
		return
	}
	if err = os.Remove(target); err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	if err = store.NewStore(directory).Simulation.DeleteProfile(); err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	s.broadcastLocked("style-updated", map[string]any{"entryId": req.EntryID})
	reply(w, http.StatusOK, map[string]bool{"ok": true})
}

func validateStyleSourceName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || !utf8.ValidString(name) {
		return "", fmt.Errorf("请填写有效的语料文件名")
	}
	if len([]rune(name)) > 180 || filepath.IsAbs(name) || filepath.VolumeName(name) != "" ||
		name == "." || name == ".." || filepath.Base(name) != name ||
		strings.ContainsAny(name, `/\<>:"|?*`) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("语料文件名不能包含路径或特殊字符")
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md", ".markdown":
		return name, nil
	default:
		return "", fmt.Errorf("仅支持 .txt、.md、.markdown 语料")
	}
}

func validateStyleSourceContent(content string) error {
	if !utf8.ValidString(content) {
		return fmt.Errorf("语料必须是有效的 UTF-8 文本")
	}
	if len([]byte(content)) > maxStyleSourceBytes {
		return fmt.Errorf("单篇语料不能超过 8 MiB")
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("语料内容不能为空")
	}
	for _, r := range content {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return fmt.Errorf("语料包含不允许的控制字符")
		}
	}
	return nil
}

func ensureStyleRoot(projectDir string) (string, error) {
	root := styleRoot(projectDir)
	if info, err := os.Lstat(root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("simulate 语料目录无效或为符号链接")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	} else if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	projectAbs, err := filepath.Abs(projectDir)
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	realProject, err := filepath.EvalSymlinks(projectAbs)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realProject, realRoot)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("语料目录超出风格条目范围")
	}
	return rootAbs, nil
}

func writeStyleSource(target string, content []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".style-upload-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("只能覆盖普通语料文件")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return fileio.Rename(tmpName, target)
}

func listStyleSources(projectDir string) ([]styleSourceView, error) {
	root := styleRoot(projectDir)
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return []styleSourceView{}, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("simulate 语料目录无效或为符号链接")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, err
	}
	var sources []styleSourceView
	err = filepath.WalkDir(rootAbs, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".txt", ".md", ".markdown":
		default:
			return nil
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		relReal, err := filepath.Rel(rootReal, real)
		if err != nil || relReal == ".." || strings.HasPrefix(relReal, ".."+string(filepath.Separator)) {
			return fmt.Errorf("语料路径超出风格条目目录")
		}
		rel, err := filepath.Rel(rootAbs, path)
		if err != nil || !utf8.ValidString(rel) || strings.IndexFunc(rel, unicode.IsControl) >= 0 {
			return fmt.Errorf("语料路径包含无效字符")
		}
		sources = append(sources, styleSourceView{
			Name: filepath.ToSlash(rel), SizeBytes: info.Size(),
			ModifiedAt: info.ModTime().Format(time.RFC3339),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	return sources, nil
}
