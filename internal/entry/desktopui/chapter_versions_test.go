package desktopui

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestChapterVersionsArePerChapterAndCanApplyOfflineWithOriginalPreserved(t *testing.T) {
	s := newTestServer(t, false)
	st := seedChapterEditor(t, s)
	original := "原始正文。"
	version, err := st.ChapterVersions.Append(1, "旧信", "候选版本正文", "只调整第一章", "rewrite")
	if err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "/api/chapters/1/versions?directory="+url.QueryEscape(s.dir), "")
	var result struct {
		Versions           []domain.ChapterVersion
		CurrentContentHash string
		SyncRequired       bool
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Versions) != 2 || result.SyncRequired {
		t.Fatal(w.Code, w.Body.String())
	}
	// A caller may have created a candidate before opening the reader. The
	// first candidate does not become the active original merely by number.
	if result.CurrentContentHash != domain.ChapterContentSHA256(original) {
		t.Fatal(result)
	}
	w = request(s, "POST", "/api/chapters/1/versions/"+version.ID+"/apply", mustJSON(t, map[string]any{"directory": s.dir, "expectedContentHash": result.CurrentContentHash}))
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		busy := s.busy
		s.mu.Unlock()
		if !busy {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	text, err := st.Drafts.LoadChapterText(1)
	if err != nil || text != version.Content {
		t.Fatal(text, err)
	}
	record, _ := st.ChapterRecords.Load(1)
	if record.Content != original {
		t.Fatal("offline apply fabricated accepted facts")
	}
	w = request(s, "GET", "/api/chapters/1/versions?directory="+url.QueryEscape(s.dir), "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.SyncRequired {
		t.Fatal(w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), original) || !strings.Contains(w.Body.String(), version.Content) {
		t.Fatal("lost manuscripts", w.Body.String())
	}
}

func TestChapterVersionsGetNeverArchivesWithoutTheProjectLease(t *testing.T) {
	s := newTestServer(t, false)
	seedChapterEditor(t, s)
	lease := flock.New(filepath.Join(s.dir, ".ainovel.lock"))
	locked, err := lease.TryLock()
	if err != nil || !locked {
		t.Fatal(locked, err)
	}
	defer lease.Close()
	w := request(s, "GET", "/api/chapters/1/versions?directory="+url.QueryEscape(s.dir), "")
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.dir, "meta", "chapter_versions", "000001.json")); !os.IsNotExist(err) {
		t.Fatal("GET archived into an externally occupied project", err)
	}
}

func TestChapterVersionsBusyReadIsReadOnlyAndMutationsRejectStaleProjects(t *testing.T) {
	s := newTestServer(t, false)
	seedChapterEditor(t, s)
	s.mu.Lock()
	s.busy = true
	s.mu.Unlock()
	w := request(s, "GET", "/api/chapters/1/versions?directory="+url.QueryEscape(s.dir), "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.dir, "meta", "chapter_versions", "000001.json")); !os.IsNotExist(err) {
		t.Fatal("busy read wrote metadata", err)
	}
	w = request(s, "POST", "/api/chapter-rewrites", mustJSON(t, map[string]any{"directory": s.dir, "chapters": []int{1}, "instruction": "明确重写"}))
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.mu.Lock()
	s.busy = false
	s.mu.Unlock()
	for _, endpoint := range []string{"/api/chapter-rewrites", "/api/chapters/1/versions/invalid/apply"} {
		body := map[string]any{"directory": "stale", "expectedContentHash": "hash"}
		if endpoint == "/api/chapter-rewrites" {
			body = map[string]any{"directory": "stale", "chapters": []int{1}, "instruction": "明确重写"}
		}
		w = request(s, "POST", endpoint, mustJSON(t, body))
		if w.Code != 409 {
			t.Fatal(endpoint, w.Code, w.Body.String())
		}
	}
}
