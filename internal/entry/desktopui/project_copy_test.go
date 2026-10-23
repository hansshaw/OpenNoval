package desktopui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	"github.com/hansshaw/AInoval-web-ui/internal/host"
	"github.com/hansshaw/AInoval-web-ui/internal/store"
)

func TestCopyWorkspacePreservesStoryAndIsolatesFiles(t *testing.T) {
	s := newTestServer(t, true)
	source := s.dir
	project := projectInfo{ID: "original-project", Name: "原项目", CreatedAt: "2020-01-01T00:00:00Z", Archived: true, Genre: "悬疑", Tags: []string{"改写"}}
	if err := saveProject(source, project); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"chapters/chapter-001.md":                "原版正文",
		"drafts/chapter-002.md":                  "下一章草稿",
		"outline.json":                           `[]`,
		"meta/checkpoints.jsonl":                 `{"seq":1,"scope":{"kind":"chapter","chapter":1},"step":"commit"}` + "\n",
		"meta/sessions/agents/writer-ch01.jsonl": `{"role":"assistant","content":[]}` + "\n",
		"meta/decisions.jsonl":                   `{}` + "\n",
		".ainovel/rules/custom.md":               "用户自己的项目规则",
		"logs/web.log":                           "runtime log",
		"meta/runtime/queue.jsonl":               "running task",
		"meta/runtime/tasks/run.log":             "running log",
		"meta/web-project-old.tmp":               "partial metadata",
		"outline.json.tmp-123":                   "partial outline",
	}
	for rel, data := range files {
		writeCopyFixture(t, source, rel, data)
	}
	st := store.NewStore(source)
	meta, err := st.RunMeta.Load()
	if err != nil || meta == nil {
		t.Fatal(meta, err)
	}
	meta.StartPrompt = "继续创作的原始意图"
	meta.PendingSteer = "旧实例的待处理指令"
	meta.AdvanceMode = domain.ChapterAdvanceReview
	meta.AdvancePermitChapter = 2
	meta.Workbench = &domain.WorkbenchControl{
		ApprovedFingerprint:           "approved-plan",
		ApprovedFoundationFingerprint: "approved-foundation",
		BatchSize:                     3,
		BatchEnd:                      4,
		AcceptedChapter:               1,
	}
	if err := st.RunMeta.Save(*meta); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, CurrentChapter: 2, CompletedChapters: []int{1}, TotalWordCount: 4}); err != nil {
		t.Fatal(err)
	}
	w := request(s, http.MethodPost, "/api/workspaces/copy", mustJSON(t, map[string]any{"directory": source, "name": "分支版本"}))
	if w.Code != http.StatusCreated {
		t.Fatalf("copy: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Directory string      `json:"directory"`
		Project   projectInfo `json:"project"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Directory == source || filepath.Dir(result.Directory) != s.booksDir || s.dir != source {
		t.Fatalf("copy path or selection is not isolated: %+v, current=%s", result, s.dir)
	}
	if result.Project.ID == "" || result.Project.ID == project.ID || result.Project.CopiedFrom != project.ID || result.Project.Archived || result.Project.Name != "分支版本" || result.Project.CreatedAt == project.CreatedAt || result.Project.UpdatedAt != result.Project.CreatedAt {
		t.Fatalf("copy metadata = %+v", result.Project)
	}
	for rel, want := range files {
		if skipProjectCopyPath(rel) || strings.HasPrefix(rel, "logs/") || strings.HasPrefix(rel, "meta/runtime/") {
			if _, err := os.Lstat(filepath.Join(result.Directory, rel)); !os.IsNotExist(err) {
				t.Fatalf("runtime artifact copied: %s, err=%v", rel, err)
			}
			continue
		}
		got, err := os.ReadFile(filepath.Join(result.Directory, rel))
		if err != nil || string(got) != want {
			t.Fatalf("story content %s = %q, err=%v", rel, got, err)
		}
		originalInfo, err := os.Stat(filepath.Join(source, rel))
		if err != nil {
			t.Fatal(err)
		}
		copiedInfo, err := os.Stat(filepath.Join(result.Directory, rel))
		if err != nil || os.SameFile(originalInfo, copiedInfo) {
			t.Fatalf("copy shares an inode: %s, err=%v", rel, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(result.Directory, ".ainovel.lock")); !os.IsNotExist(err) {
		t.Fatalf("source lease copied: %v", err)
	}
	originalUsage, err := os.ReadFile(filepath.Join(source, "meta", "usage.json"))
	if err != nil {
		t.Fatalf("source usage was not flushed: %v", err)
	}
	copiedUsage, err := os.ReadFile(filepath.Join(result.Directory, "meta", "usage.json"))
	if err != nil || string(copiedUsage) != string(originalUsage) {
		t.Fatalf("copied usage does not match source snapshot: %v", err)
	}
	copiedMeta, err := store.NewStore(result.Directory).RunMeta.Load()
	if err != nil || copiedMeta == nil {
		t.Fatal(copiedMeta, err)
	}
	if copiedMeta.StartPrompt != meta.StartPrompt || copiedMeta.PendingSteer != "" || copiedMeta.AdvancePermitChapter != 0 || copiedMeta.AdvanceHold != nil || copiedMeta.Workbench.BatchEnd != 1 || copiedMeta.Workbench.ApprovedFingerprint != "approved-plan" || copiedMeta.Workbench.ApprovedFoundationFingerprint != "approved-foundation" || copiedMeta.Workbench.AcceptedChapter != 1 {
		t.Fatalf("copied recovery state = %+v", copiedMeta)
	}
	if err := os.WriteFile(filepath.Join(result.Directory, "chapters/chapter-001.md"), []byte("副本独立改写"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(source, "chapters/chapter-001.md")); err != nil || string(got) != "原版正文" {
		t.Fatalf("editing copy changed original: %q %v", got, err)
	}
	originalMeta, err := st.RunMeta.Load()
	if err != nil || originalMeta.PendingSteer != meta.PendingSteer || originalMeta.Workbench.BatchEnd != 4 || originalMeta.Workbench.ApprovedFoundationFingerprint != "approved-foundation" {
		t.Fatalf("copy changed source state: %+v %v", originalMeta, err)
	}
	// A copy can open its own Host while the original still owns its book lease.
	w = request(s, http.MethodPost, "/api/workspaces", mustJSON(t, map[string]any{"path": result.Directory}))
	if w.Code != http.StatusOK || s.rt == nil || s.dir != result.Directory {
		t.Fatalf("open independent copy: %d %s", w.Code, w.Body.String())
	}
}

func TestCopyLegacyRootProjectCopiesOnlyItsOwnStory(t *testing.T) {
	s := newTestServer(t, false)
	s.dir, s.homeDir = s.booksDir, s.booksDir
	source := s.booksDir
	project := projectInfo{
		ID: "legacy-id", Name: "知微的破碎",
		CreatedAt: "2026-10-19T21:22:58+08:00", Archived: true,
	}
	if err := saveProject(source, project); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"premise.md":             "知微的故事前提",
		"outline.json":           `[{"chapter":1,"title":"规矩","core_event":"测试"}]`,
		"layered_outline.json":   `[]`,
		"characters.json":        `[{"name":"林知微","role":"主角"}]`,
		"world_rules.json":       `[]`,
		"meta/run.json":          `{"started_at":"2026-10-19T21:26:10+08:00","workbench":{"batchSize":1,"batchEnd":0,"acceptedChapter":0}}`,
		"chapters/chapter-01.md": "第一章正文",
	}
	for rel, data := range files {
		writeCopyFixture(t, source, rel, data)
	}
	child := filepath.Join(source, "other-project")
	if err := saveProject(child, projectInfo{Name: "不应复制的嵌套项目"}); err != nil {
		t.Fatal(err)
	}
	writeCopyFixture(t, child, "outline.json", `[{"chapter":1,"title":"嵌套"}]`)
	writeCopyFixture(t, source, "logs/web.log", "runtime only")

	w := request(s, http.MethodPost, "/api/workspaces/copy", mustJSON(t, map[string]any{"directory": source}))
	if w.Code != http.StatusCreated {
		t.Fatalf("copy legacy root: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Directory string      `json:"directory"`
		Project   projectInfo `json:"project"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Directory == source || filepath.Dir(result.Directory) != s.booksDir {
		t.Fatalf("copy published outside books root: %+v", result)
	}
	if result.Project.ID == "" || result.Project.ID == project.ID ||
		result.Project.CopiedFrom != project.ID || result.Project.Archived ||
		result.Project.Name != "知微的破碎（副本）" ||
		result.Project.CreatedAt == project.CreatedAt {
		t.Fatalf("copy metadata = %+v", result.Project)
	}
	for rel, want := range files {
		if rel == "meta/run.json" {
			continue
		}
		got, err := os.ReadFile(filepath.Join(result.Directory, rel))
		if err != nil || string(got) != want {
			t.Fatalf("copied story file %s = %q, err=%v", rel, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(result.Directory, "meta", "web-project.json")); err != nil {
		t.Fatalf("copy metadata missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.Directory, "other-project")); !os.IsNotExist(err) {
		t.Fatalf("nested independent project was copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.Directory, "logs")); !os.IsNotExist(err) {
		t.Fatalf("runtime log was copied: %v", err)
	}
	if err := os.WriteFile(filepath.Join(result.Directory, "chapters/chapter-01.md"), []byte("副本改写"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(source, "chapters/chapter-01.md")); err != nil || string(got) != "第一章正文" {
		t.Fatalf("editing legacy copy changed source: %q %v", got, err)
	}
}

func TestCopyWorkspaceRejectsHostCoCreation(t *testing.T) {
	s := newTestServer(t, true)
	if !s.rt.PauseForCoCreate() {
		t.Fatal("could not enter paused co-creation")
	}
	defer s.rt.CancelCoCreate()
	w := request(s, http.MethodPost, "/api/workspaces/copy", mustJSON(t, map[string]any{"directory": s.dir}))
	if w.Code != http.StatusConflict {
		t.Fatalf("copy accepted an active Host operation: %d %s", w.Code, w.Body.String())
	}
	entries, err := os.ReadDir(s.booksDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("busy Host published a project: %v %v", entries, err)
	}
}

func TestCopyWorkspaceRejectsBusyAndForeignLease(t *testing.T) {
	s := newTestServer(t, true)
	for _, kind := range []string{"busy", "stopping", "style"} {
		s.mu.Lock()
		s.busy, s.stopping, s.styleBusy = kind == "busy", kind == "stopping", kind == "style"
		s.mu.Unlock()
		w := request(s, http.MethodPost, "/api/workspaces/copy", mustJSON(t, map[string]any{"directory": s.dir}))
		if w.Code != http.StatusConflict {
			t.Fatalf("%s: %d %s", kind, w.Code, w.Body.String())
		}
	}
	s.mu.Lock()
	s.busy, s.stopping, s.styleBusy = false, false, false
	s.mu.Unlock()
	other := newTestServer(t, true)
	// Treat a separately running server as an otherwise allowed home workspace.
	s.homeDir = other.dir
	w := request(s, http.MethodPost, "/api/workspaces/copy", mustJSON(t, map[string]any{"directory": other.dir}))
	if w.Code != http.StatusConflict {
		t.Fatalf("foreign lease accepted: %d %s", w.Code, w.Body.String())
	}
	other.Close()
	w = request(s, http.MethodPost, "/api/workspaces/copy", mustJSON(t, map[string]any{"directory": other.dir}))
	if w.Code != http.StatusCreated {
		t.Fatalf("released project could not be copied: %d %s", w.Code, w.Body.String())
	}
}

func TestCopyWorkspaceRejectsPendingDeletionWithoutPublishing(t *testing.T) {
	s := newTestServer(t, false)
	if err := saveProject(s.dir, projectInfo{Name: "正在删除的源项目"}); err != nil {
		t.Fatal(err)
	}
	writeCopyFixture(t, s.dir, host.ProjectDeletingFile, "pending deletion")
	w := request(s, http.MethodPost, "/api/workspaces/copy", mustJSON(t, map[string]any{"directory": s.dir}))
	if w.Code != http.StatusConflict {
		t.Fatalf("pending deletion copied: %d %s", w.Code, w.Body.String())
	}
	entries, err := os.ReadDir(s.booksDir)
	if (err != nil && !os.IsNotExist(err)) || len(entries) != 0 {
		t.Fatalf("copy of deleting project was published: %v %v", entries, err)
	}
}

func TestCopyWorkspaceRejectsUnsafeSourceAndCleansPartialCopy(t *testing.T) {
	s := newTestServer(t, false)
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []map[string]any{
		{"directory": ""},
		{"directory": filepath.Dir(s.dir)},
		{"directory": s.dir, "name": strings.Repeat("字", 81)},
	} {
		w := request(s, http.MethodPost, "/api/workspaces/copy", mustJSON(t, body))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("unsafe request accepted: %d %s", w.Code, w.Body.String())
		}
	}
	writeCopyFixture(t, s.dir, "a-story.md", "copied before unsafe entry")
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.dir, "z-unsafe.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	w := request(s, http.MethodPost, "/api/workspaces/copy", mustJSON(t, map[string]any{"directory": s.dir}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("symlink accepted: %d %s", w.Code, w.Body.String())
	}
	entries, err := os.ReadDir(s.booksDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial project published: %v %v", entries, err)
	}
	staging, err := filepath.Glob(filepath.Join(filepath.Dir(s.booksDir), ".ainovel-copy-*"))
	if err != nil || len(staging) != 0 {
		t.Fatalf("staging not cleaned: %v %v", staging, err)
	}
}

func TestCopyWorkspaceDefaultNameAndCanceledSnapshot(t *testing.T) {
	s := newTestServer(t, false)
	if err := saveProject(s.dir, projectInfo{Name: strings.Repeat("字", 80)}); err != nil {
		t.Fatal(err)
	}
	w := request(s, http.MethodPost, "/api/workspaces/copy", mustJSON(t, map[string]any{"directory": s.dir}))
	if w.Code != http.StatusCreated {
		t.Fatalf("default name: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Project projectInfo `json:"project"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !validProjectName(result.Project.Name) || !strings.HasSuffix(result.Project.Name, "（副本）") {
		t.Fatalf("default metadata = %+v %v", result, err)
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyProjectFiles(ctx, root, t.TempDir(), false, true); err != context.Canceled {
		t.Fatalf("canceled snapshot = %v", err)
	}
}

func TestCopyProjectFilesOnlyIgnoresDisappearedTemporaryFiles(t *testing.T) {
	for _, name := range []string{"usage.json.tmp-123", "old.tmp", ".ainovel.lock", host.ProjectDeletingFile, "story.md", "directory.tmp"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			if name == "directory.tmp" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				writeCopyFixture(t, dir, name, "listed before atomic rename")
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			// Reproduce the exact WalkDir -> Lstat window without relying
			// on timing or retries: the directory entry outlives its file.
			entry := fs.FileInfoToDirEntry(info)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			copier := projectFileCopier{ctx: context.Background(), source: root, destination: t.TempDir()}
			err = copier.walk(name, entry, nil)
			if name == "usage.json.tmp-123" || name == "old.tmp" {
				if err != nil {
					t.Fatalf("disposable temp entry failed snapshot: %v", err)
				}
			} else if !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("lost non-temporary file was ignored: %v", err)
			}
		})
	}
}

func TestCopyProjectFilesRejectsExcludedSymlinks(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "usage.json.tmp-123")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := copyProjectFiles(context.Background(), root, t.TempDir(), false, true); !errors.Is(err, errCopyUnsafeFile) {
		t.Fatalf("excluded temporary symlink was accepted: %v", err)
	}
}

func TestCopyWorkspaceLogsSnapshotFailureWithoutExposingDetails(t *testing.T) {
	s := newTestServer(t, true)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/workspaces/copy", strings.NewReader(mustJSON(t, map[string]string{"directory": s.dir}))).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "context canceled") || strings.Contains(w.Body.String(), s.dir) {
		t.Fatalf("copy exposed internal failure: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(logs.String(), "context canceled") || !strings.Contains(logs.String(), "项目复制快照失败") {
		t.Fatalf("copy failure lacked server diagnostics: %s", logs.String())
	}
}

func writeCopyFixture(t *testing.T, dir, rel, data string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
