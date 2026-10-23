package desktopui

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	"github.com/hansshaw/AInoval-web-ui/internal/store"
)

func prepareExportProject(t *testing.T, directory string) {
	t.Helper()
	if err := saveProject(directory, projectInfo{Name: "一致快照"}); err != nil {
		t.Fatal(err)
	}
	st := store.NewStore(directory)
	for _, err := range []error{
		st.Book.Save(domain.BookMetadata{Title: "一致快照", Synopsis: "导出时保持资料与正文一致。"}),
		st.Drafts.SaveFinalChapter(1, "同一版本的正文"),
		st.Progress.Save(&domain.Progress{Phase: domain.PhaseWriting, CurrentChapter: 1, CompletedChapters: []int{1}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestExportRequiresProjectLeaseForStableSnapshot(t *testing.T) {
	for _, current := range []bool{true, false} {
		t.Run(map[bool]string{true: "current", false: "other"}[current], func(t *testing.T) {
			s := newTestServer(t, false)
			target := s.dir
			if !current {
				target = filepath.Join(s.booksDir, "other")
			}
			prepareExportProject(t, target)
			lease := flock.New(filepath.Join(target, ".ainovel.lock"))
			locked, err := lease.TryLock()
			if err != nil || !locked {
				t.Fatalf("foreign lease: %v %v", locked, err)
			}
			t.Cleanup(func() { _ = lease.Close() })
			for _, format := range []string{"txt", "epub"} {
				response := request(s, http.MethodPost, "/api/export", mustJSON(t, map[string]string{"directory": target, "format": format}))
				if response.Code != http.StatusConflict || !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
					t.Fatalf("read changing files without a lease: %d %s", response.Code, response.Body.String())
				}
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			response := request(s, http.MethodPost, "/api/export", mustJSON(t, map[string]string{"directory": target, "format": "txt"}))
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "同一版本的正文") {
				t.Fatalf("export after lease released: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestExportCannotInterleaveCurrentRestoreOrChapterEdit(t *testing.T) {
	s := newTestServer(t, false)
	prepareExportProject(t, s.dir)
	// Both restore and chapter editing temporarily close the Host while retaining
	// the busy flag; exporting then would read a partially replaced file set.
	s.busy, s.job = true, "restore-version"
	response := request(s, http.MethodPost, "/api/export", `{"format":"txt"}`)
	s.busy, s.job = false, ""
	if response.Code != http.StatusConflict {
		t.Fatalf("exported during restore: %d %s", response.Code, response.Body.String())
	}
}

func TestExportUsesIdleDetachedHostSnapshot(t *testing.T) {
	s := newTestServer(t, true)
	prepareExportProject(t, s.dir)
	source, rt := s.dir, s.rt
	// An idle Host retained after a run still owns its OS lease. Its own snapshot
	// must be used instead of trying to acquire the lease a second time.
	s.mu.Lock()
	s.projectRuntimes = map[string]*projectRuntime{projectKey(source): {rt: rt, cfg: s.cfg}}
	s.dir, s.rt = filepath.Join(s.booksDir, "new-current"), nil
	s.mu.Unlock()
	response := request(s, http.MethodPost, "/api/export", mustJSON(t, map[string]string{"directory": source, "format": "txt"}))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "同一版本的正文") {
		t.Fatalf("idle detached export: %d %s", response.Code, response.Body.String())
	}
}
