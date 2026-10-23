package desktopui

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
	"github.com/hansshaw/AInoval-web-ui/internal/chaptermedia"
	"github.com/hansshaw/AInoval-web-ui/internal/mediaworkspace"
	"github.com/hansshaw/AInoval-web-ui/internal/store"
)

func mediaExportRequest(s *Server, chapter, directory string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/chapters/"+chapter+"/media/export?directory="+url.QueryEscape(directory), nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func mediaArchiveFiles(t *testing.T, content []byte) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, entry := range reader.File {
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name] = data
	}
	return files
}

func TestChapterMediaExportPreservesSavedContentAndEmbeddedImages(t *testing.T) {
	s := newTestServer(t, false)
	st := seedChapterEditor(t, s)
	// The current manual body differs from the accepted facts baseline. Export
	// must use exactly what the chapter workbench currently presents.
	const body = "用户手动保存的最新正文。\n\n雨停了，邮局的门打开了。"
	if err := st.Drafts.SaveFinalChapter(1, body); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := st.PlanningFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	title, volume, event := "新的章名", "新的卷名", "找到手改规划里的邮局"
	if _, err := st.UpdateWorkbenchChapterPlan(1, store.WorkbenchChapterPlanEdit{Title: &title, VolumeTitle: &volume, CoreEvent: &event}, fingerprint); err != nil {
		t.Fatal(err)
	}
	data := chapterMediaPNG(t)
	upload, err := chaptermedia.Prepare(data, "邮局.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chaptermedia.Add(s.dir, 1, upload, "邮局外景", "chapter-end"); err != nil {
		t.Fatal(err)
	}
	w := mediaExportRequest(s, "1", s.dir)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Disposition"), "chapter-000001-media.zip") {
		t.Fatalf("export: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	files := mediaArchiveFiles(t, w.Body.Bytes())
	var manifest mediaworkspace.Manifest
	if err := json.Unmarshal(files["chapter.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Format != mediaworkspace.Format || manifest.SchemaVersion != 1 || manifest.Chapter.Number != 1 || len(manifest.Images) != 1 || len(files) != 2 {
		t.Fatalf("portable chapter manifest: %+v", manifest)
	}
	chapter := manifest.Chapter
	if chapter.Title != title || chapter.VolumeTitle != volume || chapter.Body == nil || *chapter.Body != body || chapter.Plan == nil || chapter.Plan.CoreEvent != event {
		t.Fatalf("saved content was not exported: %+v", chapter)
	}
	image := manifest.Images[0]
	if image.Caption != "邮局外景" || image.Placement != "chapter-end" || image.Order != 0 || image.Width != 8 || image.Height != 6 || image.MIME != "image/png" || image.Bytes != len(data) || !bytes.Equal(files[image.File], data) {
		t.Fatalf("image metadata or bytes changed: %+v", image)
	}
	if strings.Contains(string(files["chapter.json"]), s.dir) || strings.Contains(string(files["chapter.json"]), "apiKey") {
		t.Fatal("media export must not expose local paths or model credentials")
	}
	current, _ := st.Drafts.LoadChapterText(1)
	record, _ := st.ChapterRecords.Load(1)
	if current != body || record.Content == body {
		t.Fatal("export mutated prose or accepted facts")
	}
}

func TestChapterMediaExportPlannedChapterAndRequestBoundaries(t *testing.T) {
	s := newTestServer(t, false)
	seedChapterEditor(t, s)
	w := mediaExportRequest(s, "2", s.dir)
	if w.Code != 200 {
		t.Fatalf("planned chapter export: %d %s", w.Code, w.Body.String())
	}
	var manifest mediaworkspace.Manifest
	if err := json.Unmarshal(mediaArchiveFiles(t, w.Body.Bytes())["chapter.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Chapter.Plan == nil || manifest.Chapter.Body != nil || manifest.Chapter.ContentSHA256 != "" {
		t.Fatalf("planned chapter invented body: %+v", manifest.Chapter)
	}
	for _, tc := range []struct {
		chapter, directory string
		status             int
	}{{"1", s.dir + "-other", 409}, {"1", "", 409}, {"-1", s.dir, 400}, {"1000001", s.dir, 400}, {"999", s.dir, 404}} {
		if got := mediaExportRequest(s, tc.chapter, tc.directory); got.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, got.Code, got.Body.String())
		}
	}
	s.mu.Lock()
	s.busy = true
	s.mu.Unlock()
	w = mediaExportRequest(s, "1", s.dir)
	s.mu.Lock()
	s.busy = false
	s.mu.Unlock()
	if w.Code != 409 {
		t.Fatalf("busy snapshot: %d", w.Code)
	}
	lock := flock.New(filepath.Join(s.dir, ".ainovel.lock"))
	if ok, err := lock.TryLock(); err != nil || !ok {
		t.Fatalf("external lease fixture: %v %v", ok, err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if w := mediaExportRequest(s, "1", s.dir); w.Code == 200 {
		t.Fatal("export read another process's mutable project without a lease")
	}
}

func TestChapterMediaExportRejectsCorruptImageWithoutPublishingArchive(t *testing.T) {
	s := newTestServer(t, false)
	seedChapterEditor(t, s)
	upload, err := chaptermedia.Prepare(chapterMediaPNG(t), "image.png")
	if err != nil {
		t.Fatal(err)
	}
	item, err := chaptermedia.Add(s.dir, 1, upload, "", "chapter-start")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "media", "chapters", "000001", item.ID+".png"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	w := mediaExportRequest(s, "1", s.dir)
	if w.Code != 409 || w.Header().Get("Content-Type") == "application/zip" {
		t.Fatalf("partial/corrupt archive was returned: %d %s", w.Code, w.Body.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	view := chapterEditView{Chapter: 2, HasPlan: true, Title: "尚未生成"}
	if file, err := createChapterMediaArchive(ctx, s.dir, view); file != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled export: %v %v", file, err)
	}
}
