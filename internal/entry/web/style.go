package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/sim"
	"github.com/voocel/ainovel-cli/internal/store"
)

const maxStyleSourceBytes = 8 << 20

type styleSourceView struct {
	Name       string `json:"name"`
	SizeBytes  int64  `json:"sizeBytes"`
	ModifiedAt string `json:"modifiedAt,omitempty"`
}

func styleRoot(projectDir string) string {
	return filepath.Join(projectDir, "simulate")
}

func (s *Server) style(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := r.URL.Query().Get("directory"); d != "" && d != s.dir {
		fail(w, http.StatusConflict, "项目已切换")
		return
	}
	sources, err := listStyleSources(s.dir)
	if err != nil {
		fail(w, http.StatusInternalServerError, "风格语料目录读取失败："+s.redactLocked(err.Error()))
		return
	}
	profile, err := store.NewStore(s.dir).Simulation.Load()
	if err != nil {
		fail(w, http.StatusInternalServerError, "风格画像读取失败："+s.redactLocked(err.Error()))
		return
	}
	reply(w, http.StatusOK, map[string]any{
		"directory": s.dir,
		"sources":   sources,
		"profile":   domain.CompactSimulationProfile(profile),
		"busy":      s.styleBusy,
	})
}

func (s *Server) uploadStyleSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
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
	if err := validateStyleSourceContent(req.Content); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	if req.Directory != "" && req.Directory != s.dir {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "项目已切换，请刷新后重试")
		return
	}
	if s.closed || s.stopping || s.busy || s.styleBusy || (s.rt != nil && s.rt.Snapshot().IsRunning) {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "请先暂停创作并等待当前操作结束")
		return
	}
	projectDir := s.dir
	root, err := ensureStyleRoot(projectDir)
	if err != nil {
		fail(w, http.StatusBadRequest, s.redactLocked(err.Error()))
		s.mu.Unlock()
		return
	}
	target := filepath.Join(root, name)
	if filepath.Dir(target) != root {
		s.mu.Unlock()
		fail(w, http.StatusBadRequest, "语料文件名无效")
		return
	}
	if info, statErr := os.Lstat(target); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		s.mu.Unlock()
		fail(w, http.StatusBadRequest, "不能覆盖符号链接")
		return
	}
	if err := writeStyleSource(target, []byte(req.Content)); err != nil {
		fail(w, http.StatusInternalServerError, "风格语料保存失败："+s.redactLocked(err.Error()))
		s.mu.Unlock()
		return
	}
	if err := store.NewStore(projectDir).Simulation.DeleteProfile(); err != nil {
		fail(w, http.StatusInternalServerError, "风格画像失效失败："+s.redactLocked(err.Error()))
		s.mu.Unlock()
		return
	}
	if s.dir != projectDir {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "项目已切换，请刷新后重试")
		return
	}
	s.broadcastLocked("style-updated", map[string]any{"directory": projectDir})
	s.mu.Unlock()
	reply(w, http.StatusOK, map[string]any{"ok": true, "name": name, "sizeBytes": len([]byte(req.Content))})
}

func (s *Server) runStyleAnalysis(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory string `json:"directory"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	if req.Directory != "" && req.Directory != s.dir {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "项目已切换，请刷新后重试")
		return
	}
	if s.closed || s.stopping || s.busy || s.styleBusy || s.backgroundAnyBusyLocked() {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "请等待当前操作结束")
		return
	}
	if s.rt == nil {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "请先配置模型并连接当前项目")
		return
	}
	sources, err := listStyleSources(s.dir)
	if err != nil {
		fail(w, http.StatusInternalServerError, s.redactLocked(err.Error()))
		s.mu.Unlock()
		return
	}
	if len(sources) == 0 {
		s.mu.Unlock()
		fail(w, http.StatusBadRequest, "请先上传或粘贴至少一篇 .txt / .md 风格语料")
		return
	}
	projectDir := s.dir
	rt := s.rt
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.styleCancel = cancel
	s.styleDone = done
	s.styleDirectory = projectDir
	s.styleBusy = true
	events, err := rt.Simulate(ctx)
	if err != nil {
		cancel()
		s.styleCancel, s.styleDone, s.styleDirectory, s.styleBusy = nil, nil, "", false
		close(done)
		fail(w, http.StatusConflict, s.redactLocked(err.Error()))
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer close(done)
		finished := false
		for ev := range events {
			event := map[string]any{
				"stage": ev.Stage, "current": ev.Current, "total": ev.Total,
				"message": ev.Message, "time": ev.Time,
			}
			s.mu.Lock()
			if ev.Err != nil {
				event["error"] = s.redactLocked(ev.Err.Error())
			}
			if s.dir == projectDir {
				kind := "style-progress"
				switch ev.Stage {
				case sim.StageDone:
					kind = "style-done"
					finished = true
					if profile, loadErr := store.NewStore(projectDir).Simulation.Load(); loadErr == nil {
						event["profile"] = domain.CompactSimulationProfile(profile)
					} else {
						event["warning"] = s.redactLocked(loadErr.Error())
					}
				case sim.StageError:
					kind = "style-error"
					finished = true
				}
				s.broadcastLocked(kind, event)
			}
			s.mu.Unlock()
		}
		s.mu.Lock()
		if s.styleDirectory == projectDir {
			if !finished && ctx.Err() == nil {
				s.broadcastLocked("style-error", map[string]any{"message": "风格分析提前结束", "error": "未收到完成事件"})
			}
			s.styleCancel, s.styleDone, s.styleDirectory, s.styleBusy = nil, nil, "", false
		}
		s.mu.Unlock()
		cancel()
	}()
	reply(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) deleteStyleSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
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
	if req.Directory != "" && req.Directory != s.dir {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "项目已切换，请刷新后重试")
		return
	}
	if s.closed || s.stopping || s.busy || s.styleBusy || (s.rt != nil && s.rt.Snapshot().IsRunning) {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "请先暂停创作并等待当前操作结束")
		return
	}
	projectDir := s.dir
	root, err := ensureStyleRoot(projectDir)
	if err != nil {
		fail(w, http.StatusBadRequest, s.redactLocked(err.Error()))
		s.mu.Unlock()
		return
	}
	target := filepath.Join(root, name)
	info, err := os.Lstat(target)
	if err != nil {
		s.mu.Unlock()
		if os.IsNotExist(err) {
			fail(w, http.StatusNotFound, "语料文件不存在")
		} else {
			fail(w, http.StatusInternalServerError, "语料文件读取失败")
		}
		return
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		s.mu.Unlock()
		fail(w, http.StatusBadRequest, "只能删除普通语料文件")
		return
	}
	if err := os.Remove(target); err != nil {
		s.mu.Unlock()
		fail(w, http.StatusInternalServerError, "语料文件删除失败")
		return
	}
	if err := store.NewStore(projectDir).Simulation.DeleteProfile(); err != nil {
		fail(w, http.StatusInternalServerError, "风格画像失效失败："+s.redactLocked(err.Error()))
		s.mu.Unlock()
		return
	}
	if s.dir != projectDir {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "项目已切换，请刷新后重试")
		return
	}
	s.broadcastLocked("style-updated", map[string]any{"directory": projectDir})
	s.mu.Unlock()
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
		return "", fmt.Errorf("语料目录超出项目范围")
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
	return os.Rename(tmpName, target)
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
			return fmt.Errorf("语料路径超出项目目录")
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
