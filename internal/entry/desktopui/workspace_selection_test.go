package desktopui

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hansshaw/AInoval-web-ui/internal/bootstrap"
	"github.com/hansshaw/AInoval-web-ui/internal/host"
)

func freshProjectServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	disabled := false
	cfg := bootstrap.Config{OutputDir: filepath.Join(root, "novel")}
	cfg.Notify.Enabled = &disabled
	s, err := New(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	s.booksDir = filepath.Join(root, "web-books")
	s.configPath = filepath.Join(root, "config.json")
	return s
}

func assertNoSelectedProject(t *testing.T, s *Server) {
	t.Helper()
	w := request(s, "GET", "/api/state", "")
	var state map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || state["directory"] != "" || state["project"] != nil || state["hasProject"] != false || state["ready"] != false {
		t.Fatalf("unexpected empty state: %s", w.Body.String())
	}
	if s.rt != nil || s.dir != "" {
		t.Fatalf("empty state retained a Host or project: %q", s.dir)
	}
}

func TestFreshDesktopHasNoSyntheticProjectAndGlobalSetupStillWorks(t *testing.T) {
	s := freshProjectServer(t)
	assertNoSelectedProject(t, s)
	if w := request(s, "GET", "/api/workspaces", ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Stat(s.homeDir); !os.IsNotExist(err) {
		t.Fatalf("fresh home was materialized: %v", err)
	}
	body := `{"name":"local","model":"test-model","type":"openai","api":"chat","base_url":"http://127.0.0.1:1/v1","api_key":"test-key","context_window":32000,"max_output_tokens":8000}`
	if w := request(s, "POST", "/api/config", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertNoSelectedProject(t, s)
	if _, err := os.Stat(s.homeDir); !os.IsNotExist(err) {
		t.Fatalf("global config created an implicit project: %v", err)
	}
	for _, endpoint := range []string{"/api/config", "/api/media-models", "/api/jobs", "/api/reader/books", "/api/style"} {
		if w := request(s, "GET", endpoint, ""); w.Code != 200 {
			t.Fatalf("global %s unavailable: %d %s", endpoint, w.Code, w.Body.String())
		}
	}
}

func TestEmptySelectionRejectsProjectAPIsAndCWDPaths(t *testing.T) {
	s := freshProjectServer(t)
	for _, endpoint := range []string{"/api/planning", "/api/audit", "/api/cocreate", "/api/model-requests", "/api/chapters/1", "/api/chapters/1/versions", "/api/versions", "/api/style/references"} {
		if w := request(s, "GET", endpoint, ""); w.Code != 409 || !strings.Contains(w.Body.String(), "请先创建或选择项目") {
			t.Fatalf("empty project API %s: %d %s", endpoint, w.Code, w.Body.String())
		}
	}
	for _, endpoint := range []string{"/api/project", "/api/action", "/api/cocreate", "/api/manual/chapters"} {
		if w := request(s, "POST", endpoint, `{}`); w.Code != 409 {
			t.Fatalf("empty project mutation %s: %d %s", endpoint, w.Code, w.Body.String())
		}
	}
	if w := request(s, "POST", "/api/export", `{"format":"txt"}`); w.Code != 409 {
		t.Fatal("empty export not rejected", w.Code, w.Body.String())
	}
	if _, err := s.validWorkspaceLocked(""); err == nil {
		t.Fatal("empty directory resolved to cwd")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.validWorkspaceLocked(filepath.Join(cwd, "static")); err == nil {
		t.Fatal("empty current project admitted arbitrary cwd children")
	}
	if w := request(s, "POST", "/api/workspaces", mustJSON(t, map[string]string{"path": s.homeDir})); w.Code == 200 {
		t.Fatal("hidden home was opened as a project")
	}
}

func TestDeleteOnlySelectedProjectLeavesEmptyAndCanCreateAgain(t *testing.T) {
	s := freshProjectServer(t)
	if w := request(s, "POST", "/api/workspaces", `{"create":true,"name":"唯一项目"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	selected := s.dir
	if selected == "" {
		t.Fatal("created project not selected")
	}
	if w := request(s, "POST", "/api/workspaces/archive", mustJSON(t, map[string]any{"directory": selected, "archived": true})); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/workspaces/delete", mustJSON(t, map[string]any{"directory": selected, "confirmed": true})); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertNoSelectedProject(t, s)
	if w := request(s, "GET", "/api/workspaces", ""); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Body.String())
	}
	if w := request(s, "POST", "/api/reader/progress", mustJSON(t, map[string]any{"directory": selected, "progress": map[string]any{}})); w.Code == 200 {
		t.Fatal("late progress revived deleted project")
	}
	if _, err := os.Stat(selected); !os.IsNotExist(err) {
		t.Fatalf("deleted project revived: %v", err)
	}
	if w := request(s, "POST", "/api/workspaces", `{"create":true,"name":"新项目"}`); w.Code != 200 || s.dir == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/workspaces", `{"deselect":true}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertNoSelectedProject(t, s)
}

func TestDeleteLegacyCatalogueStoryPreservesChildrenAndRootIdentity(t *testing.T) {
	s := freshProjectServer(t)
	root := s.booksDir
	s.homeDir, s.dir = root, root
	if err := saveProject(root, projectInfo{Name: "旧默认作品", Archived: true}); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "independent-project")
	if err := saveProject(child, projectInfo{Name: "独立作品", Mode: "manual"}); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"chapters/01.md": "根项目正文", "media/illustration.png": "root image", "independent-project/chapters/01.md": "独立正文", "pending-import/chapters/01.md": "未完成导入", "notes/keep.txt": "个人资料"} {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if w := request(s, "POST", "/api/workspaces/delete", mustJSON(t, map[string]any{"directory": root, "confirmed": true})); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	assertNoSelectedProject(t, s)
	for _, name := range []string{"independent-project/chapters/01.md", "pending-import/chapters/01.md", "notes/keep.txt"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatalf("lost %s: %v", name, err)
		}
	}
	for _, name := range []string{"meta", "chapters", "media"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("root story data remained: %s %v", name, err)
		}
	}
	if !errors.Is(host.CheckProjectDeleting(root), host.ErrProjectDeleting) {
		t.Fatal("stale instances can revive deleted root")
	}
	if dirs := s.projectDirectoriesLocked(); len(dirs) != 1 || dirs[0] != child {
		t.Fatalf("wrong surviving catalogue: %+v", dirs)
	}
	cfg := s.baseCfg
	cfg.OutputDir = root
	restarted, err := New(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restarted.booksDir = root
	assertNoSelectedProject(t, restarted)
	if w := request(restarted, "POST", "/api/workspaces", mustJSON(t, map[string]string{"path": child})); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(restarted, "POST", "/api/manual/projects", `{"name":"后续作品"}`); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRootProjectDeletionRollsBackWithoutTouchingChild(t *testing.T) {
	s := freshProjectServer(t)
	root := s.booksDir
	if err := saveProject(root, projectInfo{Name: "失败后保留", Archived: true}); err != nil {
		t.Fatal(err)
	}
	if err := saveProject(filepath.Join(root, "child"), projectInfo{Name: "独立子项目"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "story.md"), []byte("根正文"), 0600); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected move failure")
	_, err := stageRootProjectDeletion(root, root, func(from, to string) error {
		if from == filepath.Join(root, "meta") {
			return injected
		}
		return os.Rename(from, to)
	})
	if !errors.Is(err, injected) {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "story.md")); err != nil || string(content) != "根正文" {
		t.Fatal("rollback lost story", err)
	}
	if !projectDirectory(filepath.Join(root, "child")) || !projectDirectory(root) {
		t.Fatal("rollback lost project metadata")
	}
	if err := host.CheckProjectDeleting(root); err != nil {
		t.Fatal("rollback left stale marker", err)
	}
}

func TestPendingProjectRequestDoesNotQueueSelectionOrBlockPause(t *testing.T) {
	s := newTestServer(t, false)
	s.selectionMu.RLock()
	defer s.selectionMu.RUnlock()
	// A live request owns a read lock. Selection must reject immediately rather
	// than queue a writer and block a subsequent pause behind that writer.
	if w := request(s, "POST", "/api/workspaces", `{"deselect":true}`); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !s.selectionMu.TryRLock() {
		t.Fatal("failed selection left a pending writer blocking pause")
	}
	s.selectionMu.RUnlock()
}

func TestEmptySelectionCanExportAnExplicitProject(t *testing.T) {
	s := freshProjectServer(t)
	directory, err := s.createManualProjectLocked("可导出作品", []manualChapterInput{{Title: "开篇", Content: "无当前项目也能从项目卡导出。"}})
	if err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/api/export", mustJSON(t, map[string]string{"format": "txt", "directory": directory})); w.Code != 200 || !strings.Contains(w.Body.String(), "无当前项目也能从项目卡导出") {
		t.Fatal(w.Code, w.Body.String())
	}
	assertNoSelectedProject(t, s)
}

func TestDeleteHomeContainingEntireCatalogueProtectsGlobalData(t *testing.T) {
	s := freshProjectServer(t)
	root := filepath.Dir(s.booksDir)
	s.homeDir, s.dir = root, root
	if err := saveProject(root, projectInfo{Name: "异常旧布局", Archived: true}); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(s.booksDir, "child")
	if err := saveProject(child, projectInfo{Name: "独立项目"}); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(root, "workbench-jobs.json")
	if err := os.WriteFile(journal, []byte(`{"version":1,"jobs":[],"events":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/api/workspaces/delete", mustJSON(t, map[string]any{"directory": root, "confirmed": true})); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if data, err := os.ReadFile(journal); err != nil || string(data) != `{"version":1,"jobs":[],"events":[]}` {
		t.Fatal("global queue changed", err)
	}
	if !projectDirectory(child) || !projectDirectory(root) {
		t.Fatal("unsafe ancestor deletion changed project data")
	}
}
