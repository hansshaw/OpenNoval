package desktopui

import (
	"encoding/json"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func legacyReaderProgress() map[string]any {
	return map[string]any{"chapter": 2, "offset": 0.63, "fontSize": 24, "lineHeight": 1.8, "theme": "sepia", "mode": "text", "bookmarks": []readerBookmark{{Chapter: 1, Offset: 0.2, Label: "雨夜"}}}
}

func TestReaderDesktopPreferencesMigrateAndOldClientsPreserveThem(t *testing.T) {
	s := newTestServer(t, false)
	book := readerImportedBook(t, importReaderFixture(t, s, "排版.txt", "第一章 雨夜\n海风穿过灯塔。\n第二章 归来\n她记得那阵海风。", nil))
	legacy := legacyReaderProgress()
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(book.Directory, "meta", "reader-progress.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := readReaderBook(book.Directory)
	if err != nil {
		t.Fatal(err)
	}
	p := restored.Progress
	if p.Chapter != 2 || p.Offset != 0.63 || len(p.Bookmarks) != 1 || p.FontSize != 24 || p.FontFamily != "serif" || p.PageWidth != "comfortable" || p.ParagraphSpacing != 0.5 || p.SidebarCollapsed || p.LastReadAt != "" {
		t.Fatalf("legacy progress lost or defaults not applied: %+v", p)
	}
	save := func(payload map[string]any) readerProgressState {
		t.Helper()
		payload["directory"] = book.Directory
		encoded, _ := json.Marshal(payload)
		w := request(s, "POST", "/api/reader/progress", string(encoded))
		var result readerProgressState
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatalf("save failed: %d %s", w.Code, w.Body.String())
		}
		return result
	}
	modern := legacyReaderProgress()
	modern["fontFamily"], modern["pageWidth"], modern["paragraphSpacing"], modern["sidebarCollapsed"] = "sans", "wide", 1, true
	modern["lastReadAt"] = "2999-01-01T00:00:00Z"
	before := time.Now().UTC()
	modernSaved := save(modern)
	lastRead, err := time.Parse(time.RFC3339Nano, modernSaved.LastReadAt)
	if err != nil || lastRead.Before(before) || lastRead.After(time.Now().UTC()) {
		t.Fatalf("client controlled reading timestamp: %q %v", modernSaved.LastReadAt, err)
	}
	legacy["chapter"], legacy["offset"], legacy["lastReadAt"] = 1, 0.21, "invalid client timestamp"
	oldSaved := save(legacy)
	if oldSaved.FontFamily != "sans" || oldSaved.PageWidth != "wide" || oldSaved.ParagraphSpacing != 1 || !oldSaved.SidebarCollapsed || oldSaved.Chapter != 1 || oldSaved.Offset != 0.21 || len(oldSaved.Bookmarks) != 1 {
		t.Fatalf("legacy client reset desktop preferences: %+v", oldSaved)
	}
	newRead, err := time.Parse(time.RFC3339Nano, oldSaved.LastReadAt)
	if err != nil || newRead.Before(lastRead) {
		t.Fatalf("legacy save lost server reading timestamp: %+v %v", oldSaved, err)
	}
	legacy["paragraphSpacing"], legacy["sidebarCollapsed"] = 0, false
	explicit := save(legacy)
	if explicit.ParagraphSpacing != 0 || explicit.SidebarCollapsed || explicit.FontFamily != "sans" || explicit.PageWidth != "wide" {
		t.Fatalf("explicit zero/false confused with omitted preferences: %+v", explicit)
	}
	restored, err = readReaderBook(book.Directory)
	if err != nil || restored.Progress.LastReadAt != explicit.LastReadAt || restored.Progress.ParagraphSpacing != 0 || restored.Progress.FontFamily != "sans" {
		t.Fatalf("desktop preferences did not survive disk reload: %+v %v", restored.Progress, err)
	}
	other := readerImportedBook(t, importReaderFixture(t, s, "未读.txt", "独立的阅读进度。", nil))
	if other.Progress.LastReadAt != "" || other.Progress.FontFamily != "serif" || other.Progress.PageWidth != "comfortable" || other.Progress.ParagraphSpacing != 0.5 {
		t.Fatalf("desktop preferences leaked across books: %+v", other.Progress)
	}
}

func TestReaderDesktopPreferencesRejectInvalidValuesWithoutChangingProgress(t *testing.T) {
	s := newTestServer(t, false)
	book := readerImportedBook(t, importReaderFixture(t, s, "白名单.txt", "第一章 雨夜\n海风穿过灯塔。\n第二章 归来\n她记得那阵海风。", nil))
	initial := legacyReaderProgress()
	initial["directory"] = book.Directory
	data, _ := json.Marshal(initial)
	if w := request(s, "POST", "/api/reader/progress", string(data)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	path := filepath.Join(book.Directory, "meta", "reader-progress.json")
	before, _ := os.ReadFile(path)
	for _, invalid := range []struct {
		field string
		value any
	}{
		{"fontFamily", "url(https://invalid.example/font)"}, {"fontFamily", ""},
		{"pageWidth", "calc(100vw)"}, {"pageWidth", ""},
		{"paragraphSpacing", -1}, {"paragraphSpacing", 0.2}, {"paragraphSpacing", 1000},
		{"paragraphSpacing", "0.5"}, {"sidebarCollapsed", "false"},
	} {
		t.Run(invalid.field+"/"+strings.ReplaceAll(string(mustReaderJSON(t, invalid.value)), "/", "_"), func(t *testing.T) {
			payload := legacyReaderProgress()
			payload["directory"], payload[invalid.field] = book.Directory, invalid.value
			w := request(s, "POST", "/api/reader/progress", string(mustReaderJSON(t, payload)))
			if w.Code != 400 {
				t.Fatalf("invalid preference accepted: %d %s", w.Code, w.Body.String())
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Fatal("invalid preference replaced saved progress or reading time")
			}
		})
	}
}

func mustReaderJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestReaderShelfSummarizesSavedProgressAndReadingTime(t *testing.T) {
	s := newTestServer(t, false)
	book := readerImportedBook(t, importReaderFixture(t, s, "续读.txt", "第一章 风\n海风。\n第二章 灯塔\n她看见海岸线尽头的灯塔，忽然想起一封没有署名的信。", nil))
	unread := readerImportedBook(t, importReaderFixture(t, s, "未读.txt", "第一章 雨\n海风。\n第二章 灯\n灯光。", nil))
	payload := legacyReaderProgress()
	payload["directory"], payload["offset"] = book.Directory, 0.5
	if w := request(s, "POST", "/api/reader/progress", string(mustReaderJSON(t, payload))); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var result struct {
		Books []struct {
			Directory, LastReadAt string
			Progress              struct {
				Chapter       int
				Offset, Ratio float64
			}
		} `json:"books"`
	}
	w := request(s, "GET", "/api/reader/books", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal(w.Body.String())
	}
	seen := 0
	for _, summary := range result.Books {
		switch summary.Directory {
		case book.Directory:
			seen++
			want := (float64(book.Chapters[0].Words) + float64(book.Chapters[1].Words)*0.5) / float64(book.Chapters[0].Words+book.Chapters[1].Words)
			if summary.LastReadAt == "" || summary.Progress.Chapter != 2 || summary.Progress.Offset != 0.5 || math.Abs(summary.Progress.Ratio-want) > 1e-9 {
				t.Fatalf("incorrect shelf reading summary: %+v want ratio %v", summary, want)
			}
		case unread.Directory:
			seen++
			if summary.LastReadAt != "" || summary.Progress.Ratio != 0 {
				t.Fatalf("unread book acquired progress: %+v", summary)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("book summaries missing: %+v", result)
	}
	for _, sample := range []struct {
		chapters []readerChapterInfo
		progress readerProgressState
		want     float64
	}{
		{nil, readerProgressState{Chapter: 1}, 0},
		{[]readerChapterInfo{{Number: 4}, {Number: 9}}, readerProgressState{Chapter: 4}, 0},
		{[]readerChapterInfo{{Number: 4}, {Number: 9}}, readerProgressState{Chapter: 9, Offset: 0.5}, 0.75},
		{[]readerChapterInfo{{Number: 4}, {Number: 9}}, readerProgressState{Chapter: 9, Offset: 1}, 1},
		{[]readerChapterInfo{{Number: 4}, {Number: 9}}, readerProgressState{Chapter: 99, Offset: 1}, 0},
	} {
		if got := readerBookRatio(readerBookInfo{Chapters: sample.chapters, Progress: sample.progress}); got != sample.want {
			t.Fatalf("unknown-word/chapter weighting: got %v want %v", got, sample.want)
		}
	}
}

func TestReaderSearchUsesUnicodeCodepointRangesAndContentIdentity(t *testing.T) {
	s := newTestServer(t, false)
	book := readerImportedBook(t, importReaderFixture(t, s, "定位.txt", "第一章 雨夜\n🌙甲İ乙I海风，𠀀海风。", nil))
	var chapter struct{ Content, ContentHash string }
	read := readerGET(t, s, "/api/reader/chapter/1", book.Directory)
	if read.Code != 200 || json.Unmarshal(read.Body.Bytes(), &chapter) != nil || chapter.ContentHash != readerContentHash(chapter.Content) {
		t.Fatal(read.Body.String())
	}
	for _, term := range []string{"i", "海风", "𠀀"} {
		var result struct {
			Matches []struct {
				Start, Length int
				ContentHash   string
			}
		}
		w := readerGET(t, s, "/api/reader/search?q="+url.QueryEscape(term), book.Directory)
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Matches) == 0 {
			t.Fatal(w.Body.String())
		}
		runes := []rune(chapter.Content)
		for _, match := range result.Matches {
			if match.Start < 0 || match.Length < 1 || match.Start+match.Length > len(runes) || strings.ToLower(string(runes[match.Start:match.Start+match.Length])) != term || match.ContentHash != chapter.ContentHash {
				t.Fatalf("incorrect Unicode search locator: %+v for %q", match, term)
			}
		}
	}
	// A saved edit after search changes the identity; the renderer can refuse
	// stale ranges instead of highlighting unrelated text at the old position.
	if err := os.WriteFile(filepath.Join(book.Directory, "chapters", "01.md"), []byte("新的段落。\n"+chapter.Content), 0600); err != nil {
		t.Fatal(err)
	}
	var edited struct{ ContentHash string }
	read = readerGET(t, s, "/api/reader/chapter/1", book.Directory)
	if read.Code != 200 || json.Unmarshal(read.Body.Bytes(), &edited) != nil || edited.ContentHash == chapter.ContentHash {
		t.Fatal("a stale search range cannot be identified after editing")
	}
}
