package web

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
	"github.com/voocel/ainovel-cli/internal/chaptermedia"
	"github.com/voocel/ainovel-cli/internal/projectversion"
)

func chapterMediaPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 6))
	img.Set(2, 2, color.NRGBA{R: 40, G: 180, B: 110, A: 255})
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func uploadChapterMediaTest(t *testing.T, s *Server, chapter, directory string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("directory", directory); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("caption", "配图说明"); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("placement", "chapter-start"); err != nil {
		t.Fatal(err)
	}
	file, err := form.CreateFormFile("file", "handmade.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/chapters/"+chapter+"/media", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func mediaRequestTest(s *Server, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
	if method == "PATCH" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestChapterMediaImportEditAndReadPreserveAcceptedProse(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "offline", true: "connected"}[configured], func(t *testing.T) {
			s := newTestServer(t, configured)
			st := seedChapterEditor(t, s)
			original, _ := st.Drafts.LoadChapterText(1)
			recordBefore, _ := st.ChapterRecords.Load(1)
			fingerprintBefore, _ := st.PlanningFingerprint()
			data := chapterMediaPNG(t)
			w := uploadChapterMediaTest(t, s, "1", s.dir, data)
			var view chapterMediaListView
			if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &view) != nil || len(view.Items) != 1 {
				t.Fatalf("upload: %d %s", w.Code, w.Body.String())
			}
			item := view.Items[0]
			w = mediaRequestTest(s, "GET", item.URL, "")
			if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("image response: %d %v", w.Code, w.Header())
			}
			body, _ := json.Marshal(map[string]any{"directory": s.dir, "caption": "新的说明", "placement": "chapter-end", "order": 7})
			resource := "/api/chapters/1/media/" + item.ID
			w = mediaRequestTest(s, "PATCH", resource, string(body))
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Items[0].Caption != "新的说明" || view.Items[0].Placement != "chapter-end" || view.Items[0].Order != 7 {
				t.Fatalf("edit: %d %s", w.Code, w.Body.String())
			}
			current, _ := st.Drafts.LoadChapterText(1)
			recordAfter, _ := st.ChapterRecords.Load(1)
			fingerprintAfter, _ := st.PlanningFingerprint()
			if current != original || recordBefore.ContentSHA256 != recordAfter.ContentSHA256 || fingerprintBefore != fingerprintAfter {
				t.Fatal("illustration edit altered prose or its approved planning/facts")
			}
			w = mediaRequestTest(s, "DELETE", resource+"?directory="+url.QueryEscape(s.dir), "")
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || len(view.Items) != 0 {
				t.Fatalf("delete: %d %s", w.Code, w.Body.String())
			}
			if w = mediaRequestTest(s, "GET", item.URL, ""); w.Code != 404 {
				t.Fatalf("removed image still served: %d", w.Code)
			}
		})
	}
}

func TestChapterMediaValidatesProjectChapterFileAndExternalLease(t *testing.T) {
	s := newTestServer(t, false)
	seedChapterEditor(t, s)
	data := chapterMediaPNG(t)
	for _, test := range []struct {
		chapter, directory string
		data               []byte
		status             int
	}{
		{"1", s.dir + "-other", data, 409}, {"999", s.dir, data, 404}, {"1", s.dir, []byte(`<svg onload="alert(1)"></svg>`), 400},
	} {
		if w := uploadChapterMediaTest(t, s, test.chapter, test.directory, test.data); w.Code != test.status {
			t.Fatalf("expected %d got %d: %s", test.status, w.Code, w.Body.String())
		}
	}
	lease := flock.New(filepath.Join(s.dir, ".ainovel.lock"))
	locked, err := lease.TryLock()
	if err != nil || !locked {
		t.Fatalf("lock: %v %v", locked, err)
	}
	w := uploadChapterMediaTest(t, s, "1", s.dir, data)
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if w.Code != 409 {
		t.Fatalf("external project lease bypassed: %d %s", w.Code, w.Body.String())
	}
	w = uploadChapterMediaTest(t, s, "2", s.dir, data)
	if w.Code != 201 {
		t.Fatalf("planned chapter should accept images: %d %s", w.Code, w.Body.String())
	}
	var view chapterMediaListView
	json.Unmarshal(w.Body.Bytes(), &view)
	wrongProject := strings.Replace(view.Items[0].URL, url.QueryEscape(s.dir), url.QueryEscape(s.dir+"-other"), 1)
	if w := mediaRequestTest(s, "GET", wrongProject, ""); w.Code != 409 {
		t.Fatalf("cross project image read: %d", w.Code)
	}
	if w := mediaRequestTest(s, "GET", strings.Replace(view.Items[0].URL, "/chapters/2/", "/chapters/1/", 1), ""); w.Code != 404 {
		t.Fatalf("cross chapter image read: %d", w.Code)
	}
}

func TestChapterMediaRefusesLinkedStorageWithoutOutsideWrites(t *testing.T) {
	s := newTestServer(t, false)
	seedChapterEditor(t, s)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(s.dir, "media")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if w := uploadChapterMediaTest(t, s, "1", s.dir, chapterMediaPNG(t)); w.Code != 400 {
		t.Fatalf("linked media upload: %d %s", w.Code, w.Body.String())
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("upload wrote outside project")
	}
}

func TestChapterMediaTravelsWithProjectCopiesAndSnapshots(t *testing.T) {
	s := newTestServer(t, true)
	seedChapterEditor(t, s)
	data := chapterMediaPNG(t)
	response := uploadChapterMediaTest(t, s, "1", s.dir, data)
	var media chapterMediaListView
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &media) != nil || len(media.Items) != 1 {
		t.Fatalf("upload: %d %s", response.Code, response.Body.String())
	}
	item := media.Items[0]
	response = request(s, "POST", "/api/workspaces/copy", mustJSON(t, map[string]any{"directory": s.dir, "name": "配图副本"}))
	var copied struct{ Directory string }
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &copied) != nil || copied.Directory == "" {
		t.Fatalf("copy: %d %s", response.Code, response.Body.String())
	}
	if _, actual, err := chaptermedia.Read(copied.Directory, 1, item.ID); err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("copy omitted image: %v", err)
	}
	response = request(s, "POST", "/api/versions", mustJSON(t, map[string]any{"directory": s.dir, "name": "有配图的版本"}))
	var version projectversion.Manifest
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &version) != nil {
		t.Fatalf("snapshot: %d %s", response.Code, response.Body.String())
	}
	imageFound, manifestFound := false, false
	for _, file := range version.Files {
		if strings.HasPrefix(file.Path, "media/chapters/000001/") && strings.Contains(file.Path, item.ID) {
			imageFound = true
		}
		if file.Path == "media/chapters/000001/manifest.json" {
			manifestFound = true
		}
	}
	if !imageFound || !manifestFound {
		t.Fatalf("snapshot omitted durable attachments: %+v", version.Files)
	}
	if _, actual, err := chaptermedia.Read(filepath.Join(s.dir, "meta", "versions", version.ID, "files"), 1, item.ID); err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("snapshot image mismatch: %v", err)
	}
}
