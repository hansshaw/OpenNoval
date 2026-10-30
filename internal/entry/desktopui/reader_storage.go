package desktopui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hansshaw/OpenNovel/internal/domain"
	"github.com/hansshaw/OpenNovel/internal/fileio"
	"github.com/hansshaw/OpenNovel/internal/novelreader"
)

var errReaderUnsafe = errors.New("阅读器路径包含链接、特殊文件或越界引用")

type readerSourceInfo struct {
	Format   string      `json:"format"`
	Filename string      `json:"filename"`
	URL      string      `json:"url,omitempty"`
	Author   string      `json:"author,omitempty"`
	Pages    int         `json:"pages,omitempty"`
	HasText  bool        `json:"hasText"`
	PDFPages map[int]int `json:"pdfPages,omitempty"`
}

type readerBookmark struct {
	Chapter int     `json:"chapter"`
	Offset  float64 `json:"offset"`
	Label   string  `json:"label"`
}

type readerProgressState struct {
	Chapter          int              `json:"chapter"`
	Offset           float64          `json:"offset"`
	Bookmarks        []readerBookmark `json:"bookmarks"`
	FontSize         int              `json:"fontSize"`
	LineHeight       float64          `json:"lineHeight"`
	Theme            string           `json:"theme"`
	Mode             string           `json:"mode"`
	FontFamily       string           `json:"fontFamily,omitempty"`
	PageWidth        string           `json:"pageWidth,omitempty"`
	ParagraphSpacing float64          `json:"paragraphSpacing"`
	SidebarCollapsed bool             `json:"sidebarCollapsed"`
	LastReadAt       string           `json:"lastReadAt,omitempty"`
}

func defaultReaderProgress() readerProgressState {
	return readerProgressState{Chapter: 1, Bookmarks: []readerBookmark{}, FontSize: 20, LineHeight: 1.8, Theme: "auto", Mode: "text", FontFamily: "serif", PageWidth: "comfortable", ParagraphSpacing: 0.5}
}

// Book-level progress follows chapter order and remains useful for scanned or
// empty chapters whose word count is unavailable. No saved progress means an
// unread book, even if an outline happens to start at a non-one chapter number.
func readerBookRatio(book readerBookInfo) float64 {
	if len(book.Chapters) == 0 {
		return 0
	}
	var total, completed float64
	found := false
	for _, chapter := range book.Chapters {
		weight := float64(max(1, chapter.Words))
		total += weight
		if chapter.Number == book.Progress.Chapter {
			completed += weight * book.Progress.Offset
			found = true
		} else if !found {
			completed += weight
		}
	}
	if !found || total == 0 {
		return 0
	}
	return min(1, max(0, completed/total))
}

type readerChapterInfo struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Words   int    `json:"words"`
	PDFPage int    `json:"pdfPage,omitempty"`
	HasText bool   `json:"hasText"`
}

type readerBookInfo struct {
	Archived  bool                `json:"-"`
	Directory string              `json:"directory"`
	Title     string              `json:"title"`
	Author    string              `json:"author,omitempty"`
	Format    string              `json:"format"`
	Words     int                 `json:"words"`
	UpdatedAt string              `json:"updatedAt"`
	Chapters  []readerChapterInfo `json:"chapters"`
	Source    *readerSourceInfo   `json:"source,omitempty"`
	Progress  readerProgressState `json:"progress"`
}

// os.Root provides race-resistant confinement even if files change between the
// explicit symlink checks and opening. User-supplied paths never reach this API.
func readerSafePath(root *os.Root, name string, allowMissing bool) error {
	if !filepath.IsLocal(name) || filepath.Clean(name) != name {
		return errReaderUnsafe
	}
	parts := strings.Split(name, string(filepath.Separator))
	for i := range parts {
		info, err := root.Lstat(filepath.Join(parts[:i+1]...))
		if errors.Is(err, os.ErrNotExist) && allowMissing && i == len(parts)-1 {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return errReaderUnsafe
		}
	}
	return nil
}

func readerRead(root *os.Root, name string, limit int64) ([]byte, error) {
	if err := readerSafePath(root, name, false); err != nil {
		return nil, err
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > limit {
		return nil, fmt.Errorf("阅读器文件超过限制")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("阅读器文件超过限制")
	}
	return b, nil
}

func readerReadJSON(root *os.Root, name string, limit int64, v any) error {
	b, err := readerRead(root, name, limit)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("阅读器资料损坏：%s", filepath.Base(name))
	}
	return nil
}

func readerWrite(root *os.Root, name string, data []byte) error {
	if err := readerSafePath(root, name, true); err != nil {
		return err
	}
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(name), ".reader-"+hex.EncodeToString(id[:])+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return fileio.RootRename(root, tmp, name)
}

func (s *Server) readerDirectoryLocked(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = s.dir
	}
	directory, err := s.validWorkspaceLocked(raw)
	if err != nil {
		return "", err
	}
	for _, known := range s.projectDirectoriesLocked() {
		if known == directory {
			return directory, nil
		}
	}
	return "", errors.New("作品不在当前书架中")
}

func readReaderBook(directory string) (readerBookInfo, error) {
	book := readerBookInfo{Directory: directory, Title: filepath.Base(directory), Format: "project", Chapters: []readerChapterInfo{}, Progress: defaultReaderProgress()}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return book, err
	}
	defer root.Close()
	var project projectInfo
	var meta domain.BookMetadata
	var outline []domain.OutlineEntry
	var layered []domain.VolumeOutline
	var progress domain.Progress
	for _, item := range []struct {
		name  string
		value any
	}{
		{filepath.Join("meta", "web-project.json"), &project}, {filepath.Join("meta", "book.json"), &meta},
		{"outline.json", &outline}, {"layered_outline.json", &layered}, {filepath.Join("meta", "progress.json"), &progress},
	} {
		if err := readerReadJSON(root, item.name, 32<<20, item.value); err != nil {
			return book, err
		}
	}
	if meta.Title != "" {
		book.Title = meta.Title
	}
	if project.Name != "" {
		book.Title = project.Name
	}
	if project.Mode == "manual" {
		book.Format = "manual"
	}
	book.Archived = project.Archived
	book.UpdatedAt = project.UpdatedAt
	if book.UpdatedAt == "" {
		book.UpdatedAt = project.CreatedAt
	}
	var source readerSourceInfo
	if err := readerReadJSON(root, filepath.Join("meta", "reader-source.json"), 1<<20, &source); err != nil {
		return book, err
	}
	if source.Format != "" {
		book.Format, book.Author = source.Format, source.Author
		if source.Format == "pdf" {
			source.URL = "/api/reader/source?directory=" + url.QueryEscape(directory)
		}
		book.Source = &source
	}
	if len(layered) > 0 {
		outline = domain.FlattenOutline(layered)
	}
	chapters := map[int]readerChapterInfo{}
	for _, e := range outline {
		if e.Chapter > 0 && e.Chapter <= 1000000 {
			chapters[e.Chapter] = readerChapterInfo{Number: e.Chapter, Title: manualChapterTitle(e.Title, e.Chapter)}
		}
	}
	for _, n := range progress.CompletedChapters {
		if n > 0 && n <= 1000000 {
			if _, ok := chapters[n]; !ok {
				chapters[n] = readerChapterInfo{Number: n, Title: fmt.Sprintf("第 %d 章", n)}
			}
		}
	}
	if len(chapters) > novelreader.MaxChapters {
		return book, errors.New("阅读器最多支持 10000 章")
	}
	for n, c := range chapters {
		c.Words = progress.ChapterWordCounts[n]
		c.PDFPage = source.PDFPages[n]
		name := filepath.Join("chapters", fmt.Sprintf("%02d.md", n))
		if err := readerSafePath(root, name, false); err == nil {
			if st, err := root.Stat(name); err == nil {
				c.HasText = st.Size() > 0
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return book, err
		}
		book.Chapters = append(book.Chapters, c)
	}
	if book.Source != nil {
		book.Source.HasText = false
		for _, c := range book.Chapters {
			book.Source.HasText = book.Source.HasText || c.HasText
		}
	}
	sort.Slice(book.Chapters, func(i, j int) bool { return book.Chapters[i].Number < book.Chapters[j].Number })
	book.Words = progress.TotalWordCount
	if len(book.Chapters) > 0 {
		book.Progress.Chapter = book.Chapters[0].Number
	}
	if err := readerReadJSON(root, filepath.Join("meta", "reader-progress.json"), 1<<20, &book.Progress); err != nil {
		return book, err
	}
	if err := validateReaderProgress(book.Progress, book); err != nil {
		book.Progress = defaultReaderProgress()
		if len(book.Chapters) > 0 {
			book.Progress.Chapter = book.Chapters[0].Number
		}
	}
	if book.Progress.Bookmarks == nil {
		book.Progress.Bookmarks = []readerBookmark{}
	}
	return book, nil
}

func readReaderChapter(directory string, chapter int) (string, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", err
	}
	defer root.Close()
	b, err := readerRead(root, filepath.Join("chapters", fmt.Sprintf("%02d.md", chapter)), novelreader.MaxChapterBytes)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", errors.New("章节正文编码无效")
	}
	return string(b), nil
}

func readerHasChapter(book readerBookInfo, n int) bool {
	for _, c := range book.Chapters {
		if c.Number == n {
			return true
		}
	}
	return len(book.Chapters) == 0 && n == 1
}

func validateReaderProgress(p readerProgressState, book readerBookInfo) error {
	validRatio := func(v float64) bool { return v >= 0 && v <= 1 }
	if !readerHasChapter(book, p.Chapter) || !validRatio(p.Offset) || p.FontSize < 16 || p.FontSize > 30 || !(p.LineHeight >= 1.4 && p.LineHeight <= 2.2) || len(p.Bookmarks) > 1000 {
		return errors.New("阅读进度或排版设置超出范围")
	}
	if p.Theme != "auto" && p.Theme != "paper" && p.Theme != "sepia" && p.Theme != "dark" {
		return errors.New("阅读主题无效")
	}
	if p.Mode != "text" && p.Mode != "original" {
		return errors.New("阅读模式无效")
	}
	if p.Mode == "original" && (book.Source == nil || book.Source.Format != "pdf") {
		return errors.New("只有 PDF 支持原页阅读")
	}
	if p.FontFamily != "serif" && p.FontFamily != "sans" && p.FontFamily != "system" {
		return errors.New("阅读字体无效")
	}
	if p.PageWidth != "compact" && p.PageWidth != "comfortable" && p.PageWidth != "wide" {
		return errors.New("阅读版心宽度无效")
	}
	if p.ParagraphSpacing != 0 && p.ParagraphSpacing != 0.5 && p.ParagraphSpacing != 1 {
		return errors.New("阅读段距无效")
	}
	if p.LastReadAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, p.LastReadAt); err != nil {
			return errors.New("阅读时间无效")
		}
	}
	for _, mark := range p.Bookmarks {
		if !readerHasChapter(book, mark.Chapter) || !validRatio(mark.Offset) || !utf8.ValidString(mark.Label) || utf8.RuneCountInString(mark.Label) > 80 || strings.ContainsRune(mark.Label, '\x00') {
			return errors.New("书签信息无效")
		}
	}
	return nil
}
