package web

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/store"
)

func (s *Server) workspaces(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dirs := s.projectDirectoriesLocked()
	list := []map[string]any{}
	for _, dir := range dirs {
		title := filepath.Base(dir)
		st := store.NewStore(dir)
		if b, err := st.Book.Load(); err == nil && b != nil && b.Title != "" {
			title = b.Title
		}
		project, _ := readProject(dir)
		if project.Name != "" {
			title = project.Name
		}
		completed, words := 0, 0
		if p, e := st.Progress.Load(); e == nil && p != nil {
			completed = len(p.CompletedChapters)
			words = p.TotalWordCount
		}
		status := "paused"
		if s.backgroundProjectBusyLocked(dir) {
			status = "background"
		}
		if s.projectRunningLocked(dir) {
			status = "running"
		}
		workflow, _ := workflowView(st)
		list = append(list, map[string]any{
			"path": dir, "title": title, "completed": completed, "words": words,
			"status": status, "stage": workflow["stage"], "createdAt": project.CreatedAt,
			"archived": project.Archived, "tags": project.Tags, "genre": project.Genre,
			"tone": project.Tone, "targetWords": project.TargetWords,
			"targetChapters": project.TargetChapters, "stylePreset": project.StylePreset,
			"agentPreset": project.AgentPreset, "backgroundBusy": s.backgroundProjectBusyLocked(dir),
		})
	}
	sort.Slice(list, func(i, j int) bool { return list[i]["path"].(string) < list[j]["path"].(string) })
	reply(w, 200, list)
}

// projectDirectoriesLocked returns the physical project directories exposed by
// Project Center. A legacy story may live directly in booksDir; in that layout
// only its auxiliary directories are children, so scanning every child would
// expose "meta", "chapters" and similar folders as fake projects.
func (s *Server) projectDirectoriesLocked() []string {
	dirs := map[string]bool{s.dir: true, s.homeDir: true}
	legacy, _ := filepath.Abs(filepath.Join("output", "novel"))
	if st, e := os.Stat(legacy); e == nil && st.IsDir() {
		dirs[legacy] = true
	}

	// A legacy project may itself be the configured books root. Copies and
	// independently created projects remain direct children. Scan the active
	// project root as well because a manually launched sidecar can have a
	// different working directory from the Desktop process.
	roots := map[string]bool{s.booksDir: true, s.dir: true, s.homeDir: true}
	for root := range roots {
		if projectDirectory(root) {
			dirs[root] = true
		}
		entries, _ := os.ReadDir(root)
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			dir := filepath.Join(root, entry.Name())
			if projectDirectory(dir) {
				dirs[dir] = true
			}
		}
	}

	list := make([]string, 0, len(dirs))
	for dir := range dirs {
		list = append(list, dir)
	}
	sort.Strings(list)
	return list
}

func projectDirectory(dir string) bool {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	meta, err := os.Lstat(filepath.Join(dir, "meta", "web-project.json"))
	return err == nil && meta.Mode().IsRegular()
}

func (s *Server) switchWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string
		Name   string
		Create bool
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Create && req.Name != "" && !validProjectName(req.Name) {
		fail(w, 400, "请输入 1–80 字的项目名称")
		return
	}
	s.mu.Lock()
	if s.busy || s.stopping || s.closed {
		s.mu.Unlock()
		fail(w, 409, "请等待当前操作结束后再切换项目")
		return
	}
	target := req.Path
	if req.Create {
		if err := os.MkdirAll(s.booksDir, 0700); err != nil {
			s.mu.Unlock()
			fail(w, 500, "无法创建项目目录")
			return
		}
		var err error
		target, err = os.MkdirTemp(s.booksDir, time.Now().Format("20060102-150405")+"-*")
		if err != nil {
			s.mu.Unlock()
			fail(w, 500, "无法创建项目目录")
			return
		}
	} else {
		validated, err := s.validWorkspaceLocked(target)
		if err != nil {
			s.mu.Unlock()
			fail(w, 400, err.Error())
			return
		}
		target = validated
		st, err := os.Lstat(target)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			s.mu.Unlock()
			fail(w, 400, "作品目录不存在或是链接")
			return
		}
	}
	if req.Create {
		if req.Name == "" {
			req.Name = "未命名项目 " + time.Now().Format("01-02 15:04:05")
		}
		if err := saveProject(target, projectInfo{Name: req.Name, CreatedAt: time.Now().Format(time.RFC3339)}); err != nil {
			s.mu.Unlock()
			fail(w, 500, "无法创建作品目录")
			return
		}
	}
	if s.backgroundProjectBusyLocked(target) {
		s.mu.Unlock()
		fail(w, 409, "此项目正在后台排队或创作，请先停止任务再打开")
		return
	}
	project, err := readProject(target)
	if err != nil {
		s.mu.Unlock()
		fail(w, 500, "项目资料读取失败")
		return
	}
	if project.Archived {
		s.mu.Unlock()
		fail(w, 409, "项目已归档，请先取消归档")
		return
	}
	if target == s.dir {
		if s.rt == nil {
			s.initLocked()
		}
		result := map[string]any{"ok": true, "directory": s.dir, "error": s.hostError}
		s.broadcastLocked("workspace", nil)
		s.mu.Unlock()
		reply(w, 200, result)
		return
	}
	// Build the next view before changing ownership. Active engines retain their
	// independent Host and continue generating while another project is selected.
	key := projectKey(target)
	cached := s.projectRuntimes[key]
	var next projectRuntime
	if cached != nil && cached.rt != nil {
		next = *cached
	} else {
		cfg, candidate, hostErr, buildErr := s.buildHostLocked(target, project)
		if buildErr != nil {
			message := s.redactLocked(buildErr.Error())
			s.mu.Unlock()
			fail(w, 409, message)
			return
		}
		next = projectRuntime{rt: candidate, cfg: cfg, hostError: hostErr}
		if candidate != nil {
			s.wg.Add(1)
			go s.collect(candidate)
		}
	}
	old, oldDirectory := s.rt, s.dir
	if s.projectRuntimes == nil {
		s.projectRuntimes = make(map[string]*projectRuntime)
	}
	delete(s.projectRuntimes, key)
	keepOld := old != nil && (old.Snapshot().IsRunning || s.runSlots[old] != nil)
	if keepOld {
		s.projectRuntimes[projectKey(oldDirectory)] = &projectRuntime{rt: old, cfg: s.cfg, hostError: s.hostError, events: s.events, stream: s.stream}
	}
	s.dir, s.cfg, s.rt, s.hostError = target, next.cfg, next.rt, next.hostError
	s.events, s.stream = next.events, next.stream
	s.broadcastLocked("history", map[string]any{"events": s.events, "stream": s.stream, "directory": s.dir})
	s.broadcastLocked("workspace", nil)
	s.busy, s.job = false, ""
	result := map[string]any{"ok": true, "directory": s.dir, "error": s.hostError}
	s.mu.Unlock()
	if old != nil && !keepOld {
		old.Close()
	}
	reply(w, 200, result)
}
