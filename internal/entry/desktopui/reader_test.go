package desktopui

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hansshaw/OpenNovel/internal/novelreader"
	"github.com/hansshaw/OpenNovel/internal/projectversion"
	"github.com/hansshaw/OpenNovel/internal/store"
)

func importReaderFixture(t *testing.T, s *Server, name, content string, pages []novelreader.PDFPage) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	f, err := form.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte(content))
	if pages != nil {
		data, _ := json.Marshal(pages)
		form.WriteField("pdfPages", string(data))
	}
	form.Close()
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/reader/import", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func readerImportedBook(t *testing.T, w *httptest.ResponseRecorder) readerBookInfo {
	t.Helper()
	var result struct {
		Directory string         `json:"directory"`
		Book      readerBookInfo `json:"book"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Directory == "" || result.Directory != result.Book.Directory {
		t.Fatalf("import failed: %d %s", w.Code, w.Body.String())
	}
	return result.Book
}

func readerGET(t *testing.T, s *Server, endpoint, directory string) *httptest.ResponseRecorder {
	t.Helper()
	separator := "?"
	if strings.Contains(endpoint, "?") {
		separator = "&"
	}
	return request(s, "GET", endpoint+separator+"directory="+url.QueryEscape(directory), "")
}

func TestReaderImportSharedChaptersSearchAndProjectIsolation(t *testing.T) {
	s := newTestServer(t, false)
	active := s.dir
	book := readerImportedBook(t, importReaderFixture(t, s, "夜航.txt", "第一章 雨夜\n海风穿过灯塔。\n\n第二章 归来\n她记得那阵海风。", nil))
	if s.dir != active || book.Title != "夜航" || book.Format != "txt" || len(book.Chapters) != 2 || book.Words == 0 {
		t.Fatalf("unexpected imported book: %+v", book)
	}
	project, err := readProject(book.Directory)
	if err != nil || project.Mode != "manual" {
		t.Fatalf("import not integrated with manual project: %+v %v", project, err)
	}
	st := store.NewStore(book.Directory)
	p, err := st.Progress.Load()
	if err != nil || len(p.CompletedChapters) != 2 {
		t.Fatalf("import not available to media/export: %+v %v", p, err)
	}
	if text, err := st.Drafts.LoadChapterText(2); err != nil || !strings.Contains(text, "海风") {
		t.Fatalf("chapter content unavailable: %q %v", text, err)
	}
	chapter := readerGET(t, s, "/api/reader/chapter/2", book.Directory)
	if chapter.Code != 200 || !strings.Contains(chapter.Body.String(), "她记得") {
		t.Fatalf("chapter read: %d %s", chapter.Code, chapter.Body.String())
	}
	all := readerGET(t, s, "/api/reader/text", book.Directory)
	if all.Code != 200 || !strings.Contains(all.Body.String(), "雨夜") || !strings.Contains(all.Body.String(), "归来") {
		t.Fatalf("full book read: %d %s", all.Code, all.Body.String())
	}
	search := readerGET(t, s, "/api/reader/search?q="+url.QueryEscape("海风"), book.Directory)
	var result struct {
		Matches []struct {
			Chapter int `json:"chapter"`
		} `json:"matches"`
	}
	if search.Code != 200 || json.Unmarshal(search.Body.Bytes(), &result) != nil || len(result.Matches) != 2 || result.Matches[1].Chapter != 2 {
		t.Fatalf("full book search: %d %s", search.Code, search.Body.String())
	}
	other := readerImportedBook(t, importReaderFixture(t, s, "另一部.txt", "只属于另一部的内容。", nil))
	if w := readerGET(t, s, "/api/reader/text", other.Directory); w.Code != 200 || strings.Contains(w.Body.String(), "海风") {
		t.Fatalf("cross-project body leak: %s", w.Body.String())
	}
	if w := readerGET(t, s, "/api/reader/book", t.TempDir()); w.Code != 400 {
		t.Fatalf("outside directory accepted: %d %s", w.Code, w.Body.String())
	}
	if w := readerGET(t, s, "/api/reader/book", filepath.Join(book.Directory, "chapters")); w.Code != 400 {
		t.Fatalf("child directory accepted: %d", w.Code)
	}
}

func TestReaderProgressPersistsAndRejectsInvalidWrites(t *testing.T) {
	s := newTestServer(t, false)
	book := readerImportedBook(t, importReaderFixture(t, s, "进度.txt", "第一章 初见\n第一段。\n第二章 重逢\n第二段。", nil))
	state := readerProgressState{Chapter: 2, Offset: 0.63, FontSize: 24, LineHeight: 1.9, Theme: "sepia", Mode: "text", Bookmarks: []readerBookmark{{Chapter: 1, Offset: 0.2, Label: "初见"}}}
	save := func(directory string, p readerProgressState) *httptest.ResponseRecorder {
		encoded, _ := json.Marshal(struct {
			Directory string `json:"directory"`
			readerProgressState
		}{directory, p})
		return request(s, "POST", "/api/reader/progress", string(encoded))
	}
	if w := save(book.Directory, state); w.Code != 200 {
		t.Fatalf("save progress: %d %s", w.Code, w.Body.String())
	}
	// No Server cache is involved: a separate disk read, like a restart, restores it.
	restored, err := readReaderBook(book.Directory)
	if err != nil || restored.Progress.Chapter != 2 || restored.Progress.Offset != 0.63 || len(restored.Progress.Bookmarks) != 1 || restored.Progress.FontSize != 24 {
		t.Fatalf("progress not restored: %+v %v", restored.Progress, err)
	}
	for _, mutate := range []func(*readerProgressState){func(p *readerProgressState) { p.Chapter = 99 }, func(p *readerProgressState) { p.Offset = 1.1 }, func(p *readerProgressState) { p.FontSize = 100 }, func(p *readerProgressState) { p.Theme = "url(evil)" }, func(p *readerProgressState) { p.Mode = "original" }, func(p *readerProgressState) { p.Bookmarks = []readerBookmark{{Chapter: 99}} }} {
		bad := state
		mutate(&bad)
		if w := save(book.Directory, bad); w.Code != 400 {
			t.Fatalf("bad progress accepted: %d %s", w.Code, w.Body.String())
		}
	}
	if w := save("", state); w.Code != 400 {
		t.Fatal("missing directory can update unrelated project")
	}
	restored, _ = readReaderBook(book.Directory)
	if restored.Progress.Offset != 0.63 {
		t.Fatal("invalid write replaced saved progress")
	}
	other := readerImportedBook(t, importReaderFixture(t, s, "独立.txt", "独立的阅读进度。", nil))
	if other.Progress.Offset != 0 || len(other.Progress.Bookmarks) != 0 {
		t.Fatal("reading progress shared across books")
	}
}

func TestReaderPDFOriginalRangeScansAndEditedText(t *testing.T) {
	s := newTestServer(t, false)
	pdf := "%PDF-1.7\noriginal-page-stream\n%%EOF"
	book := readerImportedBook(t, importReaderFixture(t, s, "扫描.pdf", pdf, []novelreader.PDFPage{{Page: 1}, {Page: 2}}))
	if book.Source == nil || book.Source.HasText || book.Source.Pages != 2 || book.Chapters[1].PDFPage != 2 {
		t.Fatalf("scanned PDF metadata: %+v", book)
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:8080"+book.Source.URL, nil)
	r.Header.Set("Range", "bytes=0-7")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusPartialContent || w.Body.String() != pdf[:8] || w.Header().Get("Content-Type") != "application/pdf" || w.Header().Get("Content-Range") == "" {
		t.Fatalf("PDF range serving failed: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	if w := readerGET(t, s, "/api/reader/text", book.Directory); w.Code != 422 {
		t.Fatalf("scan pretends to have text: %d %s", w.Code, w.Body.String())
	}
	if err := store.NewStore(book.Directory).Drafts.SaveFinalChapter(2, "人工补充的正文"); err != nil {
		t.Fatal(err)
	}
	updated, err := readReaderBook(book.Directory)
	if err != nil || !updated.Source.HasText || !updated.Chapters[1].HasText {
		t.Fatalf("new text not reflected: %+v %v", updated, err)
	}
	if w := readerGET(t, s, "/api/reader/text", book.Directory); w.Code != 200 || !strings.Contains(w.Body.String(), "人工补充") {
		t.Fatal(w.Body.String())
	}
	if err := store.NewStore(book.Directory).Drafts.SaveFinalChapter(2, ""); err != nil {
		t.Fatal(err)
	}
	updated, err = readReaderBook(book.Directory)
	if err != nil || updated.Source.HasText {
		t.Fatalf("cleared text remains available: %+v %v", updated, err)
	}
}

func TestReaderRejectsSymlinkSourceMetadataAndChapters(t *testing.T) {
	for _, kind := range []string{"source", "metadata", "chapter", "progress"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestServer(t, false)
			book := readerImportedBook(t, importReaderFixture(t, s, "安全.pdf", "%PDF-1.7\nscan", []novelreader.PDFPage{{Page: 1, Text: "安全的正文"}}))
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("PRIVATE OUTSIDE DATA"), 0600); err != nil {
				t.Fatal(err)
			}
			rel := map[string]string{"source": "imports/reader-original.pdf", "metadata": "meta/reader-source.json", "chapter": "chapters/01.md", "progress": "meta/reader-progress.json"}[kind]
			target := filepath.Join(book.Directory, filepath.FromSlash(rel))
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			endpoint := "/api/reader/book"
			if kind == "source" {
				endpoint = "/api/reader/source"
			}
			if kind == "chapter" {
				endpoint = "/api/reader/chapter/1"
			}
			w := readerGET(t, s, endpoint, book.Directory)
			if w.Code != 400 || strings.Contains(w.Body.String(), "PRIVATE OUTSIDE DATA") {
				t.Fatalf("symlink followed: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestReaderFailedImportDoesNotPublishProject(t *testing.T) {
	s := newTestServer(t, false)
	for _, name := range []string{"malformed.epub", "binary.txt", "invalid.pdf"} {
		w := importReaderFixture(t, s, name, "not a document\x00", nil)
		if w.Code < 400 {
			t.Fatalf("invalid import accepted: %s %d", name, w.Code)
		}
	}
	entries, err := os.ReadDir(s.booksDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed imports left projects: %+v", entries)
	}
}

func TestReaderConcurrentProgressWritesKeepWholeSnapshots(t *testing.T) {
	s := newTestServer(t, false)
	book := readerImportedBook(t, importReaderFixture(t, s, "并发.txt", "第一章 并发\n完整的正文。", nil))
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			p := readerProgressState{Chapter: 1, Offset: float64(n) / 12, FontSize: 18 + n, LineHeight: 1.8, Theme: "auto", Mode: "text", Bookmarks: []readerBookmark{{Chapter: 1, Offset: float64(n) / 12, Label: "书签"}}}
			data, _ := json.Marshal(struct {
				Directory string `json:"directory"`
				readerProgressState
			}{book.Directory, p})
			w := request(s, "POST", "/api/reader/progress", string(data))
			if w.Code != 200 {
				t.Errorf("concurrent write: %d %s", w.Code, w.Body.String())
			}
		}(i)
	}
	wg.Wait()
	restored, err := readReaderBook(book.Directory)
	if err != nil || len(restored.Progress.Bookmarks) != 1 || restored.Progress.Offset != restored.Progress.Bookmarks[0].Offset || restored.Progress.FontSize != 18+int(restored.Progress.Offset*12) {
		t.Fatalf("torn reading state: %+v %v", restored.Progress, err)
	}
}

func TestReaderProgressRespectsLeaseAndBooksHideArchived(t *testing.T) {
	s := newTestServer(t, false)
	book := readerImportedBook(t, importReaderFixture(t, s, "租约.txt", "第一章 起点\n安全的阅读进度。", nil))
	p := book.Progress
	p.Offset = 0.5
	data, _ := json.Marshal(struct {
		Directory string `json:"directory"`
		readerProgressState
	}{book.Directory, p})
	lease, err := acquireWorkspaceLease(book.Directory, false)
	if err != nil {
		t.Fatal(err)
	}
	w := request(s, "POST", "/api/reader/progress", string(data))
	lease.Close()
	if w.Code != 409 {
		t.Fatalf("owned external lease ignored: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(book.Directory, "meta", "reader-progress.json")); !os.IsNotExist(err) {
		t.Fatalf("locked progress was modified: %v", err)
	}
	if w := request(s, "POST", "/api/reader/progress", string(data)); w.Code != 200 {
		t.Fatalf("unlocked progress rejected: %d %s", w.Code, w.Body.String())
	}
	project, err := readProject(book.Directory)
	if err != nil {
		t.Fatal(err)
	}
	project.Archived = true
	if err := saveProject(book.Directory, project); err != nil {
		t.Fatal(err)
	}
	list := request(s, "GET", "/api/reader/books", "")
	if list.Code != 200 || strings.Contains(list.Body.String(), book.Directory) {
		t.Fatalf("archived book remains an unopenable shelf item: %d %s", list.Code, list.Body.String())
	}
	project.Archived = false
	if err := saveProject(book.Directory, project); err != nil {
		t.Fatal(err)
	}
	list = request(s, "GET", "/api/reader/books", "")
	if list.Code != 200 || !strings.Contains(list.Body.String(), "租约") {
		t.Fatalf("restored book missing: %d %s", list.Code, list.Body.String())
	}
}

func TestReaderSourceAndProgressSurviveCopyAndVersionRestore(t *testing.T) {
	s := newTestServer(t, false)
	pdf := "%PDF-1.7\noriginal-book\n%%EOF"
	book := readerImportedBook(t, importReaderFixture(t, s, "备份.pdf", pdf, []novelreader.PDFPage{{Page: 1, Text: "应被复制和恢复的正文"}}))
	p := book.Progress
	p.Offset = 0.72
	data, _ := json.Marshal(struct {
		Directory string `json:"directory"`
		readerProgressState
	}{book.Directory, p})
	if w := request(s, "POST", "/api/reader/progress", string(data)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	copyResponse := request(s, "POST", "/api/workspaces/copy", mustJSON(t, map[string]any{"directory": book.Directory, "name": "阅读副本"}))
	var copied struct {
		Directory string `json:"directory"`
	}
	if copyResponse.Code != 201 || json.Unmarshal(copyResponse.Body.Bytes(), &copied) != nil {
		t.Fatalf("copy reader book: %d %s", copyResponse.Code, copyResponse.Body.String())
	}
	copyBook, err := readReaderBook(copied.Directory)
	if err != nil || copyBook.Progress.Offset != 0.72 || copyBook.Source == nil || copyBook.Source.Format != "pdf" {
		t.Fatalf("copy lost reader data: %+v %v", copyBook, err)
	}
	if original, err := os.ReadFile(filepath.Join(copied.Directory, "imports", "reader-original.pdf")); err != nil || string(original) != pdf {
		t.Fatalf("copy lost source: %q %v", original, err)
	}
	var version projectversion.Manifest
	err = withWorkspaceLease(book.Directory, func() error {
		var err error
		version, err = projectversion.Create(context.Background(), book.Directory, "阅读版本")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"meta/reader-source.json", "meta/reader-progress.json", "imports/reader-original.pdf"} {
		found := false
		for _, f := range version.Files {
			found = found || f.Path == wanted
		}
		if !found {
			t.Fatalf("reader file excluded from snapshot: %s", wanted)
		}
	}
	if err := os.WriteFile(filepath.Join(book.Directory, "imports", "reader-original.pdf"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	err = withWorkspaceLease(book.Directory, func() error {
		comparison, err := projectversion.Compare(context.Background(), book.Directory, version.ID)
		if err != nil {
			return err
		}
		_, err = projectversion.Restore(context.Background(), book.Directory, version.ID, comparison.Fingerprint)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if original, err := os.ReadFile(filepath.Join(book.Directory, "imports", "reader-original.pdf")); err != nil || string(original) != pdf {
		t.Fatalf("restore lost original PDF: %q %v", original, err)
	}
}
