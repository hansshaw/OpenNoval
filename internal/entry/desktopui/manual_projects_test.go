package desktopui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	"github.com/hansshaw/AInoval-web-ui/internal/host"
	"github.com/hansshaw/AInoval-web-ui/internal/store"
)

func openManualProject(t *testing.T, s *Server, chapters ...manualChapterInput) string {
	t.Helper()
	s.mu.Lock()
	directory, err := s.createManualProjectLocked("手写作品", chapters)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	w := request(s, http.MethodPost, "/api/workspaces", mustJSON(t, map[string]string{"path": directory}))
	if w.Code != http.StatusOK {
		t.Fatalf("switch manual: %d %s", w.Code, w.Body.String())
	}
	return directory
}

func TestManualProjectAndEmptyChapterCreationWithoutModel(t *testing.T) {
	s := newTestServer(t, false)
	oldDirectory := s.dir
	w := request(s, http.MethodPost, "/api/manual/projects", `{"name":"我的手写小说"}`)
	var created struct{ Directory string }
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.Directory == "" {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if s.dir != oldDirectory {
		t.Fatal("creating a project changed the active workspace")
	}
	w = request(s, http.MethodPost, "/api/workspaces", mustJSON(t, map[string]string{"path": created.Directory}))
	if w.Code != http.StatusOK || s.rt != nil || s.hostError != "" {
		t.Fatalf("manual workbench must be usable without a model: %d %s %s", w.Code, w.Body.String(), s.hostError)
	}
	w = request(s, http.MethodPost, "/api/manual/chapters", mustJSON(t, map[string]string{"directory": s.dir}))
	var chapter chapterEditView
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &chapter) != nil || chapter.Chapter != 1 || !chapter.Manual || chapter.HasContent || !chapter.HasPlan || chapter.Title != "第 1 章" {
		t.Fatalf("empty chapter: %d %s", w.Code, w.Body.String())
	}
	w = request(s, http.MethodGet, "/api/chapters/1?directory="+url.QueryEscape(s.dir), "")
	var empty struct {
		Content string
		Manual  bool
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &empty) != nil || !empty.Manual || empty.Content != "" {
		t.Fatalf("empty chapter display: %d %s", w.Code, w.Body.String())
	}
	code, body := saveChapterEditor(s, "1", map[string]any{"directory": s.dir, "expectedRevision": chapter.Revision, "saveContent": true, "content": "我亲手写下的第一段。"})
	if code != http.StatusOK {
		t.Fatalf("write empty chapter: %d %s", code, body)
	}
	current := getChapterEditor(t, s, "1")
	code, body = saveChapterEditor(s, "1", map[string]any{"directory": s.dir, "expectedRevision": current.Revision, "savePlan": true, "title": "春日来信", "volumeTitle": "正文"})
	if code != http.StatusOK {
		t.Fatalf("rename manual chapter: %d %s", code, body)
	}
	current = getChapterEditor(t, s, "1")
	if current.Title != "春日来信" || current.Content != "我亲手写下的第一段。" || current.SyncRequired || current.Workflow["stage"] != "manual" {
		t.Fatalf("manual source: %+v", current)
	}
	st := store.NewStore(s.dir)
	progress, err := st.Progress.Load()
	if err != nil || len(progress.CompletedChapters) != 1 || progress.TotalChapters != 1 || progress.TotalWordCount != domain.WordCount(current.Content) {
		t.Fatalf("progress: %+v %v", progress, err)
	}
	if record, err := st.ChapterRecords.Load(1); err != nil || record != nil {
		t.Fatalf("manual edits invented AI facts: %+v %v", record, err)
	}
	if meta, err := st.RunMeta.Load(); err != nil || meta != nil {
		t.Fatalf("manual editing started AI planning: %+v %v", meta, err)
	}
	if text, err := generationSource(s.dir, 1, "final"); err != nil || text != current.Content {
		t.Fatalf("media/speech source: %q %v", text, err)
	}
	w = request(s, http.MethodPost, "/api/export", mustJSON(t, map[string]string{"directory": s.dir, "format": "txt"}))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), current.Content) || !strings.Contains(w.Body.String(), current.Title) {
		t.Fatalf("manual export: %d %s", w.Code, w.Body.String())
	}
}

func TestManualImportPreservesOrderEmptyChaptersAndNoHostWithConfiguredModel(t *testing.T) {
	s := newTestServer(t, true)
	openManualProject(t, s,
		manualChapterInput{Title: "序章", Content: "外部小说原文"},
		manualChapterInput{Title: "扫描页"},
		manualChapterInput{Content: "第三章内容"},
	)
	if s.rt != nil || s.hostError != "" {
		t.Fatalf("manual import instantiated an AI engine: %v %s", s.rt != nil, s.hostError)
	}
	st := store.NewStore(s.dir)
	outline, err := st.Outline.LoadOutlineView()
	if err != nil || len(outline) != 3 || outline[2].Title != "第 3 章" {
		t.Fatalf("outline: %+v %v", outline, err)
	}
	progress, err := st.Progress.Load()
	if err != nil || progress.TotalChapters != 3 || len(progress.CompletedChapters) != 2 || progress.CompletedChapters[0] != 1 || progress.CompletedChapters[1] != 3 || progress.CurrentChapter != 2 {
		t.Fatalf("empty imported page counted as complete: %+v %v", progress, err)
	}
	w := request(s, http.MethodPost, "/api/project", mustJSON(t, map[string]any{"directory": s.dir, "name": "改名作品", "targetChapters": 8}))
	if w.Code != http.StatusOK || s.rt != nil {
		t.Fatalf("manual metadata invoked AI: %d %s", w.Code, w.Body.String())
	}
	if meta, err := st.RunMeta.Load(); err != nil || meta != nil {
		t.Fatalf("manual target created planning state: %+v %v", meta, err)
	}
}

func TestManualEditsRejectConcurrentStaleRevisionAndChangedWorkspace(t *testing.T) {
	s := newTestServer(t, false)
	directory := openManualProject(t, s, manualChapterInput{Title: "一", Content: "原文"})
	before := getChapterEditor(t, s, "1")
	start := make(chan struct{})
	codes := make(chan int, 2)
	var workers sync.WaitGroup
	for _, body := range []string{"来自窗口一", "来自窗口二"} {
		workers.Add(1)
		go func(content string) {
			defer workers.Done()
			<-start
			code, _ := saveChapterEditor(s, "1", map[string]any{"directory": directory, "expectedRevision": before.Revision, "saveContent": true, "content": content})
			codes <- code
		}(body)
	}
	close(start)
	workers.Wait()
	one, two := <-codes, <-codes
	if !((one == http.StatusOK && two == http.StatusConflict) || (two == http.StatusOK && one == http.StatusConflict)) {
		t.Fatalf("concurrent writers overwrote each other: %d %d", one, two)
	}
	current := getChapterEditor(t, s, "1")
	openManualProject(t, s)
	code, _ := saveChapterEditor(s, "1", map[string]any{"directory": directory, "expectedRevision": current.Revision, "saveContent": true, "content": "切换后迟到的正文"})
	if code != http.StatusConflict {
		t.Fatalf("old project edit accepted: %d", code)
	}
	w := request(s, http.MethodPost, "/api/manual/chapters", mustJSON(t, map[string]string{"directory": directory, "title": "旧项目的新章"}))
	if w.Code != http.StatusConflict {
		t.Fatalf("old project append accepted: %d %s", w.Code, w.Body.String())
	}
	if content, _ := store.NewStore(directory).Drafts.LoadChapterText(1); content != current.Content {
		t.Fatal("inactive project was changed")
	}
}

func TestManualClearPreservesVersionsAndCanRestoreOffline(t *testing.T) {
	s := newTestServer(t, false)
	openManualProject(t, s, manualChapterInput{Title: "原章", Content: "可恢复的原文"})
	before := getChapterEditor(t, s, "1")
	code, body := saveChapterEditor(s, "1", map[string]any{"directory": s.dir, "expectedRevision": before.Revision, "saveContent": true, "content": ""})
	if code != http.StatusOK {
		t.Fatalf("clear: %d %s", code, body)
	}
	st := store.NewStore(s.dir)
	progress, _ := st.Progress.Load()
	if len(progress.CompletedChapters) != 0 || progress.TotalWordCount != 0 || progress.TotalChapters != 1 {
		t.Fatalf("cleared chapter progress: %+v", progress)
	}
	w := request(s, http.MethodGet, "/api/chapters/1/versions?directory="+url.QueryEscape(s.dir), "")
	var versions struct {
		CurrentContentHash string
		SyncRequired       bool
		Versions           []domain.ChapterVersion
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &versions) != nil || len(versions.Versions) != 1 || versions.SyncRequired || versions.Versions[0].Active {
		t.Fatalf("versions of cleared manuscript: %d %s", w.Code, w.Body.String())
	}
	w = request(s, http.MethodPost, "/api/chapters/1/versions/"+versions.Versions[0].ID+"/apply", mustJSON(t, map[string]string{"directory": s.dir, "expectedContentHash": versions.CurrentContentHash}))
	if w.Code != http.StatusAccepted {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	s.mu.Lock()
	done := s.jobDone
	s.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("manual version restore did not finish")
		}
	}
	after := getChapterEditor(t, s, "1")
	if after.Content != before.Content || after.SyncRequired {
		t.Fatalf("manual restore: %+v", after)
	}
	progress, _ = st.Progress.Load()
	if len(progress.CompletedChapters) != 1 || progress.TotalWordCount != domain.WordCount(before.Content) {
		t.Fatalf("restore progress: %+v", progress)
	}
}

func TestManualAppendRespectsBusyLeaseArchiveAndAIProjectProtection(t *testing.T) {
	s := newTestServer(t, false)
	seedChapterEditor(t, s)
	w := request(s, http.MethodPost, "/api/manual/chapters", mustJSON(t, map[string]string{"directory": s.dir, "title": "绕过规划"}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("manual append bypassed AI planning: %d %s", w.Code, w.Body.String())
	}
	openManualProject(t, s)
	s.busy = true
	w = request(s, http.MethodPost, "/api/manual/chapters", mustJSON(t, map[string]string{"directory": s.dir}))
	s.busy = false
	if w.Code != http.StatusConflict {
		t.Fatalf("append while busy: %d %s", w.Code, w.Body.String())
	}
	lease, err := acquireWorkspaceLease(s.dir, false)
	if err != nil {
		t.Fatal(err)
	}
	w = request(s, http.MethodPost, "/api/manual/chapters", mustJSON(t, map[string]string{"directory": s.dir}))
	lease.Close()
	if w.Code != http.StatusConflict {
		t.Fatalf("append without lease: %d %s", w.Code, w.Body.String())
	}
	project, _ := readProject(s.dir)
	project.Archived = true
	if err := saveProject(s.dir, project); err != nil {
		t.Fatal(err)
	}
	w = request(s, http.MethodPost, "/api/manual/chapters", mustJSON(t, map[string]string{"directory": s.dir}))
	if w.Code != http.StatusConflict {
		t.Fatalf("append into archived project: %d %s", w.Code, w.Body.String())
	}
}

func TestManualCreateRejectsInvalidInputBeforePublishingProject(t *testing.T) {
	s := newTestServer(t, false)
	for _, chapters := range [][]manualChapterInput{
		{{Title: "非法\n章节"}},
		{{Content: strings.Repeat("x", maxWorkbenchBodyBytes+1)}},
		{{Content: string([]byte{0xff})}},
	} {
		s.mu.Lock()
		_, err := s.createManualProjectLocked("非法导入", chapters)
		s.mu.Unlock()
		if !errors.Is(err, store.ErrWorkbenchPlanInvalid) {
			t.Fatalf("invalid import accepted: %v", err)
		}
	}
	entries, err := os.ReadDir(s.booksDir)
	if !os.IsNotExist(err) && (err != nil || len(entries) != 0) {
		t.Fatalf("failed import leaked a project: %+v %v", entries, err)
	}
}

func TestManualImportFinalizerRunsUnderLeaseBeforePublicationAndRollsBackFailure(t *testing.T) {
	for _, shouldFail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[shouldFail], func(t *testing.T) {
			s := newTestServer(t, false)
			var staged string
			failure := errors.New("source file could not be saved")
			s.mu.Lock()
			directory, err := s.createManualProjectLocked("导入事务", []manualChapterInput{{Content: "正文"}}, func(target string) error {
				staged = target
				if projectDirectory(target) {
					t.Error("project became visible before imported source files were finalized")
				}
				lease, lockErr := acquireWorkspaceLease(target, false)
				if lease != nil {
					lease.Close()
				}
				if !errors.Is(lockErr, host.ErrProjectSnapshotBusy) {
					t.Errorf("finalizer lost the project lease: %v", lockErr)
				}
				if err := os.WriteFile(filepath.Join(target, "source.txt"), []byte("imported source"), 0600); err != nil {
					return err
				}
				if shouldFail {
					return failure
				}
				return nil
			})
			s.mu.Unlock()
			if shouldFail {
				if !errors.Is(err, failure) || directory != "" || staged == "" {
					t.Fatalf("failed finalizer result: %q %q %v", directory, staged, err)
				}
				if _, err := os.Stat(staged); !os.IsNotExist(err) {
					t.Fatalf("failed import retained partially written files: %v", err)
				}
			} else {
				if err != nil || !projectDirectory(directory) {
					t.Fatalf("finished import not published: %q %v", directory, err)
				}
				if source, err := os.ReadFile(filepath.Join(directory, "source.txt")); err != nil || string(source) != "imported source" {
					t.Fatalf("published project missing its source: %q %v", source, err)
				}
			}
		})
	}
}

func TestManualSaveRejectsSymlinkAndRollsBackFailedSourceWrites(t *testing.T) {
	s := newTestServer(t, false)
	openManualProject(t, s, manualChapterInput{Content: "原文"})
	path := filepath.Join(s.dir, "meta", "web-project.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = manualFilesTransaction(s.dir, []string{"meta/web-project.json", "chapters/02.md"}, func() error {
		if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(s.dir, "chapters", "02.md"), []byte("partial"), 0600); err != nil {
			return err
		}
		return errors.New("simulated write failure")
	})
	if err == nil {
		t.Fatal("simulated failure lost")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Fatal("metadata was not restored")
	}
	if _, err := os.Stat(filepath.Join(s.dir, "chapters", "02.md")); !os.IsNotExist(err) {
		t.Fatal("partial chapter survived rollback")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.dir, "outline.md"+".link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	called := false
	err = manualFilesTransaction(s.dir, []string{"outline.md.link"}, func() error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("symlink source accepted: %v, called=%v", err, called)
	}
}
