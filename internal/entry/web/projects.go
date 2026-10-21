package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofrs/flock"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/store"
)

type projectInfo struct {
	ID             string                        `json:"id,omitempty"`
	Name           string                        `json:"name"`
	CreatedAt      string                        `json:"createdAt"`
	UpdatedAt      string                        `json:"updatedAt,omitempty"`
	CopiedFrom     string                        `json:"copiedFrom,omitempty"`
	Genre          string                        `json:"genre,omitempty"`
	TargetAudience string                        `json:"targetAudience,omitempty"`
	POV            string                        `json:"pov,omitempty"`
	Tone           string                        `json:"tone,omitempty"`
	TargetWords    int                           `json:"targetWords,omitempty"`
	TargetChapters int                           `json:"targetChapters,omitempty"`
	WritingRules   string                        `json:"writingRules,omitempty"`
	StylePreset    string                        `json:"stylePreset,omitempty"`
	AgentPreset    string                        `json:"agentPreset,omitempty"`
	RoleModels     map[string]bootstrap.ModelRef `json:"roleModels,omitempty"`
	RoleDirectives map[string]string             `json:"roleDirectives,omitempty"`
	Tags           []string                      `json:"tags,omitempty"`
	Archived       bool                          `json:"archived,omitempty"`
}

var projectRoles = map[string]bool{
	"architect": true, "writer": true, "editor": true,
	"import_segment": true, "import_analyze": true, "import_synthesize": true,
}

func readProject(dir string) (projectInfo, error) {
	var p projectInfo
	b, err := os.ReadFile(filepath.Join(dir, "meta", "web-project.json"))
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(b, &p)
	return p, err
}

func saveProject(dir string, p projectInfo) error {
	meta := filepath.Join(dir, "meta")
	if err := os.MkdirAll(meta, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(meta, "web-project-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(meta, "web-project.json"))
}

func normalizeProject(p *projectInfo) {
	p.Name = strings.TrimSpace(p.Name)
	p.Genre = strings.TrimSpace(p.Genre)
	p.TargetAudience = strings.TrimSpace(p.TargetAudience)
	p.POV = strings.TrimSpace(p.POV)
	p.Tone = strings.TrimSpace(p.Tone)
	p.WritingRules = strings.TrimSpace(p.WritingRules)
	p.StylePreset = strings.TrimSpace(p.StylePreset)
	p.AgentPreset = strings.TrimSpace(p.AgentPreset)
	seen := map[string]bool{}
	tags := make([]string, 0, len(p.Tags))
	for _, tag := range p.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] || utf8.RuneCountInString(tag) > 24 {
			continue
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	p.Tags = tags
	if p.RoleModels == nil {
		p.RoleModels = map[string]bootstrap.ModelRef{}
	}
	for role, ref := range p.RoleModels {
		ref.Provider = strings.TrimSpace(ref.Provider)
		ref.Model = strings.TrimSpace(ref.Model)
		if ref.Provider == "" && ref.Model == "" {
			delete(p.RoleModels, role)
			continue
		}
		p.RoleModels[role] = ref
	}
	if p.RoleDirectives == nil {
		p.RoleDirectives = map[string]string{}
	}
	for role, directive := range p.RoleDirectives {
		directive = strings.TrimSpace(directive)
		if directive == "" {
			delete(p.RoleDirectives, role)
			continue
		}
		p.RoleDirectives[role] = directive
	}
}

func validateProject(p projectInfo, cfg bootstrap.Config) error {
	if !validProjectName(p.Name) {
		return fmt.Errorf("请输入 1–80 字的项目名称")
	}
	if utf8.RuneCountInString(p.Genre) > 120 || utf8.RuneCountInString(p.TargetAudience) > 120 || utf8.RuneCountInString(p.POV) > 120 || utf8.RuneCountInString(p.Tone) > 120 {
		return fmt.Errorf("题材、受众、叙事视角和语气均不能超过 120 字符")
	}
	if utf8.RuneCountInString(p.WritingRules) > 12000 || utf8.RuneCountInString(p.StylePreset) > 120 || utf8.RuneCountInString(p.AgentPreset) > 120 {
		return fmt.Errorf("写作规则、风格预设或 Agent 预设过长")
	}
	if p.TargetWords < 0 || p.TargetWords > 100000000 || p.TargetChapters < 0 || p.TargetChapters > 2000 {
		return fmt.Errorf("目标字数或章节数超出允许范围")
	}
	if len(p.RoleModels) > 16 || len(p.RoleDirectives) > 16 || len(p.Tags) > 20 {
		return fmt.Errorf("项目角色或标签数量过多")
	}
	if p.StylePreset != "" {
		styles := assets.Load(p.StylePreset, assets.DefaultLoadOptions("")).Styles
		if _, ok := styles[p.StylePreset]; !ok {
			return fmt.Errorf("风格预设不存在：%s", p.StylePreset)
		}
	}
	for role, ref := range p.RoleModels {
		if !projectRoles[role] {
			return fmt.Errorf("不支持的角色：%s", role)
		}
		ref.Provider, ref.Model = strings.TrimSpace(ref.Provider), strings.TrimSpace(ref.Model)
		if ref.Provider == "" || ref.Model == "" {
			return fmt.Errorf("角色 %s 的 Provider 和模型不能为空", role)
		}
		pc, ok := cfg.Providers[ref.Provider]
		if !ok {
			return fmt.Errorf("角色 %s 使用了未配置的 Provider：%s", role, ref.Provider)
		}
		found := false
		for _, model := range pc.Models {
			if model.Name == ref.Model {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("角色 %s 使用了 Provider %s 下未配置的模型：%s", role, ref.Provider, ref.Model)
		}
		p.RoleModels[role] = ref
	}
	for role, directive := range p.RoleDirectives {
		if !projectRoles[role] {
			return fmt.Errorf("不支持的角色：%s", role)
		}
		if utf8.RuneCountInString(directive) > 4000 {
			return fmt.Errorf("角色 %s 的创作指令不能超过 4000 字", role)
		}
	}
	return nil
}

func applyProjectRoleConfigs(cfg *bootstrap.Config, models map[string]bootstrap.ModelRef) {
	if cfg.Roles == nil {
		cfg.Roles = make(map[string]bootstrap.RoleConfig)
	}
	for role, ref := range models {
		rc := cfg.Roles[role]
		rc.Provider = ref.Provider
		rc.Model = ref.Model
		cfg.Roles[role] = rc
	}
}

func projectBrief(text string, p projectInfo) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(text))
	write := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			fmt.Fprintf(&b, "\n\n%s：%s", label, strings.TrimSpace(value))
		}
	}
	write("目标题材", p.Genre)
	write("目标读者", p.TargetAudience)
	write("叙事视角", p.POV)
	write("整体语气", p.Tone)
	if p.TargetWords > 0 {
		fmt.Fprintf(&b, "\n\n目标总字数：约 %d 字", p.TargetWords)
	}
	if p.TargetChapters > 0 {
		fmt.Fprintf(&b, "\n\n目标章节数量：约 %d 章", p.TargetChapters)
	}
	write("风格预设", p.StylePreset)
	write("Agent 编排预设", p.AgentPreset)
	write("写作规则", p.WritingRules)
	roles := []string{"architect", "writer", "editor"}
	for _, role := range roles {
		write("Agent "+role+" 指令", p.RoleDirectives[role])
	}
	if len(p.Tags) > 0 {
		write("项目标签", strings.Join(p.Tags, "、"))
	}
	return strings.TrimSpace(b.String())
}

func validProjectName(name string) bool {
	return strings.TrimSpace(name) != "" && utf8.RuneCountInString(name) <= 80
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name           *string                        `json:"name"`
		Directory      string                         `json:"directory"`
		Genre          *string                        `json:"genre"`
		TargetAudience *string                        `json:"targetAudience"`
		POV            *string                        `json:"pov"`
		Tone           *string                        `json:"tone"`
		TargetWords    *int                           `json:"targetWords"`
		TargetChapters *int                           `json:"targetChapters"`
		WritingRules   *string                        `json:"writingRules"`
		StylePreset    *string                        `json:"stylePreset"`
		AgentPreset    *string                        `json:"agentPreset"`
		RoleModels     *map[string]bootstrap.ModelRef `json:"roleModels"`
		RoleDirectives *map[string]string             `json:"roleDirectives"`
		Tags           *[]string                      `json:"tags"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.stopping || s.busy || (s.rt != nil && s.rt.Snapshot().IsRunning) {
		fail(w, 409, "请先暂停并等待当前操作结束")
		return
	}
	if req.Directory != "" && req.Directory != s.dir {
		fail(w, 409, "项目已切换或正在停止")
		return
	}
	p, err := readProject(s.dir)
	if err != nil {
		fail(w, 500, "项目资料读取失败")
		return
	}
	oldPlanning := p
	normalizeProject(&oldPlanning)
	if req.Name != nil {
		p.Name = *req.Name
	}
	if req.Genre != nil {
		p.Genre = *req.Genre
	}
	if req.TargetAudience != nil {
		p.TargetAudience = *req.TargetAudience
	}
	if req.POV != nil {
		p.POV = *req.POV
	}
	if req.Tone != nil {
		p.Tone = *req.Tone
	}
	if req.TargetWords != nil {
		p.TargetWords = *req.TargetWords
	}
	if req.TargetChapters != nil {
		p.TargetChapters = *req.TargetChapters
	}
	if req.WritingRules != nil {
		p.WritingRules = *req.WritingRules
	}
	if req.StylePreset != nil {
		p.StylePreset = *req.StylePreset
	}
	if req.AgentPreset != nil {
		p.AgentPreset = *req.AgentPreset
	}
	if req.RoleModels != nil {
		p.RoleModels = *req.RoleModels
	}
	if req.RoleDirectives != nil {
		p.RoleDirectives = *req.RoleDirectives
	}
	if req.Tags != nil {
		p.Tags = *req.Tags
	}
	if p.CreatedAt == "" {
		p.CreatedAt = time.Now().Format(time.RFC3339)
	}
	normalizeProject(&p)
	if err := validateProject(p, s.baseCfg); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := saveProject(s.dir, p); err != nil {
		fail(w, 500, "项目资料保存失败")
		return
	}
	planningChanged := oldPlanning.Genre != p.Genre ||
		oldPlanning.TargetAudience != p.TargetAudience ||
		oldPlanning.POV != p.POV ||
		oldPlanning.Tone != p.Tone ||
		oldPlanning.TargetWords != p.TargetWords ||
		oldPlanning.TargetChapters != p.TargetChapters ||
		oldPlanning.WritingRules != p.WritingRules ||
		oldPlanning.StylePreset != p.StylePreset ||
		oldPlanning.AgentPreset != p.AgentPreset ||
		!reflect.DeepEqual(oldPlanning.RoleModels, p.RoleModels) ||
		!reflect.DeepEqual(oldPlanning.RoleDirectives, p.RoleDirectives)
	if planningChanged {
		if meta, e := store.NewStore(s.dir).RunMeta.Load(); e == nil && meta != nil {
			if err := store.NewStore(s.dir).RunMeta.UpdateWorkbench(func(wc *domain.WorkbenchControl) error {
				wc.ApprovedFingerprint = ""
				wc.ApprovedFoundationFingerprint = ""
				wc.BatchEnd = 0
				wc.Feedback = nil
				return nil
			}); err != nil {
				fail(w, 500, "规划确认状态更新失败")
				return
			}
		}
	}
	if s.rt == nil && s.hostError == "项目已归档，请先取消归档后再打开。" {
		s.hostError = ""
	}
	if s.baseCfg.ValidateBase() == nil {
		cfg, cfgErr := s.projectConfigLocked()
		if cfgErr != nil {
			s.hostError = s.redactLocked(cfgErr.Error())
		} else {
			s.replaceHostLocked(cfg)
		}
	}
	s.broadcastLocked("workspace", nil)
	reply(w, 200, p)
}

func (s *Server) archiveWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory string `json:"directory"`
		Archived  bool   `json:"archived"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	if s.closed || s.stopping || s.busy || (s.rt != nil && s.rt.Snapshot().IsRunning) {
		s.mu.Unlock()
		fail(w, 409, "请先暂停并等待当前操作结束")
		return
	}
	target, err := s.validWorkspaceLocked(req.Directory)
	if err != nil {
		s.mu.Unlock()
		fail(w, 400, err.Error())
		return
	}
	if s.backgroundProjectBusyLocked(target) {
		fail(w, 409, "此项目已有后台任务，请先停止任务")
		s.mu.Unlock()
		return
	}

	if s.styleBusy {
		if target != s.dir {
			s.mu.Unlock()
			fail(w, 409, "当前项目的风格分析尚未结束")
			return
		}
		s.busy = true
		s.job = "archive-style-cancel"
		cancel, done := s.styleCancel, s.styleDone
		if cancel != nil {
			cancel()
		}
		s.mu.Unlock()
		if done != nil {
			<-done
		}
		s.mu.Lock()
		if s.closed || s.stopping {
			s.busy, s.job = false, ""
			s.mu.Unlock()
			fail(w, 409, "工作台已关闭")
			return
		}
		s.busy, s.job = false, ""
	}
	defer s.mu.Unlock()
	p, err := readProject(target)
	if err != nil {
		fail(w, 500, "项目资料读取失败")
		return
	}
	p.Archived = req.Archived
	if p.Name == "" {
		p.Name = filepath.Base(target)
	}
	normalizeProject(&p)
	if err := saveProject(target, p); err != nil {
		fail(w, 500, "归档状态保存失败")
		return
	}
	if target == s.dir {
		if req.Archived && s.rt != nil {
			s.rt.Close()
			s.rt = nil
			s.hostError = "项目已归档，请选择其他项目或取消归档。"
		} else if !req.Archived {
			cfg, err := s.projectConfigLocked()
			if err != nil {
				s.hostError = err.Error()
			} else {
				s.replaceHostLocked(cfg)
			}
		}
	}
	s.broadcastLocked("workspace", nil)
	reply(w, 200, p)
}

func (s *Server) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory string `json:"directory"`
		Confirmed bool   `json:"confirmed"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !req.Confirmed {
		fail(w, 400, "永久删除需要明确确认")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.stopping || s.busy || (s.rt != nil && s.rt.Snapshot().IsRunning) {
		fail(w, 409, "请先暂停并等待当前操作结束")
		return
	}
	target, err := s.validWorkspaceLocked(req.Directory)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if s.backgroundProjectBusyLocked(target) {
		fail(w, 409, "此项目已有后台任务，请先停止任务")
		return
	}

	booksRoot, err := filepath.Abs(s.booksDir)
	if err != nil {
		fail(w, 500, "工作台目录无效")
		return
	}
	target, err = filepath.Abs(target)
	if err != nil {
		fail(w, 400, "无效作品目录")
		return
	}
	booksRoot = filepath.Clean(booksRoot)
	target = filepath.Clean(target)
	legacy, _ := filepath.Abs(filepath.Join("output", "novel"))
	if target == booksRoot ||
		target == filepath.Clean(s.dir) ||
		target == filepath.Clean(s.homeDir) ||
		target == filepath.Clean(legacy) ||
		filepath.Dir(target) != booksRoot {
		fail(w, 400, "只能永久删除工作台项目目录中的非当前项目")
		return
	}
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		fail(w, 400, "作品目录不存在或是链接")
		return
	}
	project, err := readProject(target)
	if err != nil {
		fail(w, 500, "项目资料读取失败")
		return
	}
	if !project.Archived {
		fail(w, 409, "只能永久删除已归档项目")
		return
	}
	tombstone, err := stageWorkspaceDeletion(target, booksRoot, os.Rename)
	if err != nil {
		if errors.Is(err, host.ErrProjectSnapshotBusy) {
			fail(w, 409, "项目正被其他实例使用，请先停止并关闭该项目")
		} else {
			fail(w, 409, "无法安全删除项目，原目录已保留："+s.redactLocked(err.Error()))
		}
		return
	}
	s.broadcastLocked("workspace", nil)
	// The lease handle is closed before removing the relocated lock file. A
	// failed cleanup leaves the remaining files outside the project list.
	if err := os.RemoveAll(tombstone); err != nil {
		fail(w, 500, "项目已移出列表，部分文件清理失败；剩余数据保留在 "+s.redactLocked(tombstone)+"："+s.redactLocked(err.Error()))
		return
	}
	reply(w, 200, map[string]any{"ok": true, "directory": target})
}

// Publish a durable marker under the lease, then close every handle before
// renaming. Windows opens the flock file without FILE_SHARE_DELETE, so holding
// its handle during a directory rename can otherwise prevent every deletion.
func stageWorkspaceDeletion(target, booksRoot string, rename func(string, string) error) (string, error) {
	// This lease stays outside project directories. It prevents two deleters
	// from overwriting each other's retry marker while the book lease is closed.
	coordinatorPath := filepath.Join(filepath.Dir(booksRoot), ".ainovel-delete.lock")
	if info, err := os.Lstat(coordinatorPath); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("删除协调锁文件不安全")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	coordinator := flock.New(coordinatorPath, flock.SetPermissions(0600))
	defer coordinator.Close()
	locked, err := coordinator.TryLock()
	if err != nil {
		return "", err
	}
	if !locked {
		return "", host.ErrProjectSnapshotBusy
	}
	lockedInfo, err := coordinator.Stat()
	if err != nil {
		return "", err
	}
	pathInfo, err := os.Lstat(coordinatorPath)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(lockedInfo, pathInfo) {
		return "", fmt.Errorf("删除协调锁已变化")
	}
	var container, token string
	err = withWorkspaceLeaseMode(target, true, func() error {
		project, err := readProject(target)
		if err != nil {
			return err
		}
		if !project.Archived {
			return fmt.Errorf("项目已取消归档，请刷新后重试")
		}
		container, err = os.MkdirTemp(filepath.Dir(booksRoot), ".ainovel-delete-*")
		if err != nil {
			return err
		}
		token = filepath.Base(container) + "\n"
		if err := writeDeletionMarker(target, token); err != nil {
			return errors.Join(err, clearDeletionMarker(target, token))
		}
		return nil
	})
	if err != nil {
		if container != "" {
			_ = os.Remove(container)
		}
		return "", err
	}
	if err := rename(target, filepath.Join(container, "project")); err != nil {
		// The marker keeps new writers out while the lease handle is closed.
		// On a failed move, withdraw our marker only after reacquiring that lease.
		rollbackErr := withWorkspaceLeaseMode(target, true, func() error {
			return clearDeletionMarker(target, token)
		})
		_ = os.Remove(container)
		return "", errors.Join(err, rollbackErr)
	}
	return container, nil
}

func writeDeletionMarker(target, token string) error {
	marker := filepath.Join(target, host.ProjectDeletingFile)
	if info, err := os.Lstat(marker); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("删除标记文件不安全")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(target, ".ainovel-deleting.tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(token); err != nil {
		f.Close()
		return err
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), marker); err != nil {
		return err
	}
	return syncDeletionDirectory(target)
}

func clearDeletionMarker(target, token string) error {
	marker := filepath.Join(target, host.ProjectDeletingFile)
	info, err := os.Lstat(marker)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("删除标记文件不安全")
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		return err
	}
	if string(data) != token {
		return fmt.Errorf("删除标记已变化，请重试永久删除")
	}
	if err := os.Remove(marker); err != nil {
		return err
	}
	return syncDeletionDirectory(target)
}

func syncDeletionDirectory(target string) error {
	if runtime.GOOS == "windows" {
		return nil // os.File.Sync does not expose directory fsync on Windows.
	}
	f, err := os.Open(target)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (s *Server) validWorkspaceLocked(raw string) (string, error) {
	target, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("无效作品目录")
	}
	st, err := os.Lstat(target)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("作品目录不存在或是链接")
	}
	physicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("作品目录无法解析")
	}
	// Windows EvalSymlinks expands 8.3 names and normalizes case. Comparing a
	// canonical job path against a raw TEMP/books root rejects the same physical
	// project. Resolve both sides, then return the workbench's existing spelling
	// so current-project guards and browser directory identities stay consistent.
	physicalRoot := func(directory string) string {
		absolute, err := filepath.Abs(directory)
		if err != nil {
			return filepath.Clean(directory)
		}
		if physical, err := filepath.EvalSymlinks(absolute); err == nil {
			return physical
		}
		return filepath.Clean(absolute)
	}
	legacy, _ := filepath.Abs(filepath.Join("output", "novel"))
	for _, known := range []string{s.dir, s.homeDir, legacy} {
		if known != "" && physicalTarget == physicalRoot(known) {
			return filepath.Clean(known), nil
		}
	}

	// Desktop may open a legacy story directly at output/web-books while the
	// sidecar's default books root is output/web-books. A copied story remains
	// a direct child of the active legacy root. Accept direct children of every
	// configured project root, while still rejecting grandchildren and paths
	// outside the roots.
	seen := make(map[string]bool)
	for _, root := range []string{s.booksDir, s.dir, s.homeDir} {
		absoluteRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		absoluteRoot = filepath.Clean(absoluteRoot)
		if seen[absoluteRoot] {
			continue
		}
		seen[absoluteRoot] = true
		relative, err := filepath.Rel(physicalRoot(absoluteRoot), physicalTarget)
		if err == nil && filepath.Dir(relative) == "." && relative != "." &&
			relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
			!filepath.IsAbs(relative) {
			return filepath.Join(absoluteRoot, relative), nil
		}
	}
	return "", fmt.Errorf("只能打开工作台作品目录")
}

func workflowView(st *store.Store) (map[string]any, error) {
	v := map[string]any{
		"stage": "new", "started": false, "recoverable": false,
		"canRepair": false, "canRevise": false, "canApprove": false,
		"approved": false, "auditState": "missing", "batchSize": 1,
		"latestChapter": 0, "missing": []string{},
	}
	m, err := st.RunMeta.Load()
	if err != nil {
		return v, err
	}
	if m == nil {
		return v, nil
	}
	p, err := st.Progress.Load()
	if err != nil {
		return v, err
	}
	premise, err := st.Outline.LoadPremise()
	if err != nil {
		return v, err
	}
	outline, err := st.Outline.LoadOutline()
	if err != nil {
		return v, err
	}
	layered, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return v, err
	}
	started := m.StartPrompt != "" || m.PlanningTier != "" || m.PlanStart != nil || p != nil ||
		strings.TrimSpace(premise) != "" || len(outline) > 0 || len(layered) > 0
	v["started"] = started
	w := m.Workbench
	if w == nil {
		w = &domain.WorkbenchControl{BatchSize: 1}
	}
	v["batchSize"], v["batchEnd"], v["acceptedChapter"], v["feedback"] = w.BatchSize, w.BatchEnd, w.AcceptedChapter, w.Feedback
	if p != nil {
		v["latestChapter"] = p.LatestCompleted()
	}
	if !started {
		return v, nil
	}
	v["stage"] = "planning"
	missing, err := st.FoundationMissing()
	if err != nil {
		return v, err
	}
	v["missing"] = missing
	onlyAuditMissing := len(missing) == 0 ||
		(len(missing) == 1 && missing[0] == "foundation_audit")
	if !onlyAuditMissing {
		return v, nil
	}
	fp, err := st.PlanningFingerprint()
	if err != nil {
		return v, err
	}
	foundationFP, err := st.FoundationFingerprint()
	if err != nil {
		return v, err
	}
	audit, err := st.Outline.LoadFoundationAudit()
	if err != nil {
		return v, err
	}
	auditState := "missing"
	if audit != nil {
		switch {
		case !audit.Ready:
			auditState = "failed"
		case audit.Fingerprint != foundationFP:
			auditState = "stale"
		default:
			auditState = "ready"
		}
	}
	legacyWritable := audit == nil && p != nil &&
		(p.Phase == domain.PhaseWriting || p.Phase == domain.PhaseComplete)
	approved := w.ApprovedFingerprint != "" && fp == w.ApprovedFingerprint &&
		w.ApprovedFoundationFingerprint != "" &&
		w.ApprovedFoundationFingerprint == foundationFP
	canApprove := !approved && (auditState == "ready" || legacyWritable)
	canRepair := !approved && !legacyWritable && auditState != "ready"
	// 审计缺失、失败或过期时仍应允许用户修改规划。重新审计和人工修改是两条
	// 并行入口；否则恢复项目一旦审计发现问题，就会把唯一的修复路径锁死。
	canRevise := !legacyWritable && (p == nil || p.LatestCompleted() == 0)
	v["fingerprint"] = fp
	v["foundationFingerprint"] = foundationFP
	v["auditState"] = auditState
	v["approved"] = approved
	v["canApprove"] = canApprove
	v["canRepair"] = canRepair
	v["canRevise"] = canRevise
	v["recoverable"] = canRepair
	switch {
	case canRepair:
		v["stage"] = "foundation-audit"
	case !approved:
		v["stage"] = "plan-review"
	default:
		v["stage"] = "ready"
	}
	if p != nil {
		latest := p.LatestCompleted()
		if approved {
			if latest > w.AcceptedChapter {
				if p.Phase == domain.PhaseComplete || latest >= w.BatchEnd {
					v["stage"] = "chapter-review"
				} else {
					v["stage"] = "writing"
				}
			} else if p.Phase == domain.PhaseComplete {
				v["stage"] = "complete"
			} else if p.NextChapter() <= w.BatchEnd {
				v["stage"] = "writing"
			}
		}
	}
	return v, nil
}

func (s *Server) planning(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := r.URL.Query().Get("directory"); d != "" && d != s.dir {
		fail(w, 409, "项目已切换")
		return
	}
	st := store.NewStore(s.dir)
	warnings := []string{}
	premise, e1 := st.Outline.LoadPremise()
	outline, e2 := st.Outline.LoadOutline()
	layered, e3 := st.Outline.LoadLayeredOutline()
	book, e4 := st.Book.Load()
	workflow, e5 := workflowView(st)
	compass, e6 := st.Outline.LoadCompass()
	audit, e7 := st.Outline.LoadFoundationAudit()
	characters, e8 := st.Characters.Load()
	worldRules, e9 := st.World.LoadWorldRules()
	for name, err := range map[string]error{
		"premise": e1, "outline": e2, "layered": e3, "book": e4,
		"workflow": e5, "compass": e6, "foundationAudit": e7,
		"characters": e8, "worldRules": e9,
	} {
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s 读取失败：%s", name, s.redactLocked(err.Error())))
		}
	}
	sort.Strings(warnings)
	reply(w, 200, map[string]any{
		"directory": s.dir, "premise": premise, "outline": outline,
		"layered": layered, "book": book, "workflow": workflow,
		"compass": compass, "foundationAudit": audit, "characters": characters,
		"worldRules": worldRules, "warnings": warnings,
	})
}
