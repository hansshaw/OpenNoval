package desktopui

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hansshaw/OpenNovel/internal/chaptermedia"
	"github.com/hansshaw/OpenNovel/internal/domain"
	"github.com/hansshaw/OpenNovel/internal/host"
	"github.com/hansshaw/OpenNovel/internal/novelreader"
)

var readerImports = make(chan struct{}, 2)

func (s *Server) readerError(w http.ResponseWriter, err error) {
	if errors.Is(err, host.ErrProjectSnapshotBusy) || errors.Is(err, host.ErrProjectDeleting) {
		fail(w, 409, "项目正在运行、生成快照或由其他实例使用，请稍后保存阅读进度")
		return
	}
	status := http.StatusInternalServerError
	if errors.Is(err, novelreader.ErrInvalid) || errors.Is(err, errReaderUnsafe) {
		status = http.StatusBadRequest
	}
	if errors.Is(err, os.ErrNotExist) {
		status = http.StatusNotFound
	}
	fail(w, status, "阅读器操作失败："+s.redactLocked(err.Error()))
}

func (s *Server) readerRequestedBook(w http.ResponseWriter, r *http.Request) (readerBookInfo, bool) {
	directory, err := s.readerDirectoryLocked(r.URL.Query().Get("directory"))
	if err != nil {
		fail(w, 400, err.Error())
		return readerBookInfo{}, false
	}
	book, err := readReaderBook(directory)
	if err != nil {
		s.readerError(w, err)
		return book, false
	}
	return book, true
}

func (s *Server) readerBooks(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	books := []map[string]any{}
	for _, directory := range s.projectDirectoriesLocked() {
		book, err := readReaderBook(directory)
		if err != nil {
			continue
		}
		if book.Archived {
			continue
		}
		if len(book.Chapters) == 0 && book.Title == filepath.Base(directory) {
			continue
		}
		books = append(books, map[string]any{"directory": directory, "title": book.Title, "author": book.Author, "format": book.Format, "chapters": len(book.Chapters), "words": book.Words, "updatedAt": book.UpdatedAt,
			"lastReadAt": book.Progress.LastReadAt, "progress": map[string]any{"chapter": book.Progress.Chapter, "offset": book.Progress.Offset, "ratio": readerBookRatio(book)}})
	}
	sort.Slice(books, func(i, j int) bool { return books[i]["directory"].(string) < books[j]["directory"].(string) })
	reply(w, 200, map[string]any{"books": books})
}

func (s *Server) readerBook(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if book, ok := s.readerRequestedBook(w, r); ok {
		reply(w, 200, book)
	}
}

func (s *Server) readerChapter(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	book, ok := s.readerRequestedBook(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("chapter"))
	if err != nil || n < 1 || !readerHasChapter(book, n) {
		fail(w, 404, "章节不存在")
		return
	}
	var info readerChapterInfo
	for _, c := range book.Chapters {
		if c.Number == n {
			info = c
			break
		}
	}
	content, err := readReaderChapter(book.Directory, n)
	if err != nil {
		s.readerError(w, err)
		return
	}
	images := []chapterMediaItemView{}
	items, err := chaptermedia.List(book.Directory, n)
	if err != nil {
		s.readerError(w, err)
		return
	}
	for _, item := range items {
		images = append(images, chapterMediaItemView{Item: item, URL: fmt.Sprintf("/api/chapters/%d/media/%s/content?directory=%s", n, item.ID, url.QueryEscape(book.Directory))})
	}
	reply(w, 200, map[string]any{"number": n, "title": info.Title, "content": content, "contentHash": readerContentHash(content), "words": domain.WordCount(content), "pdfPage": info.PDFPage, "hasText": strings.TrimSpace(content) != "", "images": images})
}

func readerContentHash(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}

func (s *Server) readerText(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	book, ok := s.readerRequestedBook(w, r)
	if !ok {
		return
	}
	selected := 0
	if raw := r.URL.Query().Get("chapter"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || !readerHasChapter(book, n) {
			fail(w, 404, "章节不存在")
			return
		}
		selected = n
	}
	var text strings.Builder
	for _, chapter := range book.Chapters {
		if selected != 0 && selected != chapter.Number {
			continue
		}
		content, err := readReaderChapter(book.Directory, chapter.Number)
		if err != nil {
			s.readerError(w, err)
			return
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		if text.Len()+len(content)+len(chapter.Title)+4 > novelreader.MaxTextBytes {
			fail(w, 413, "全文超过 32 MiB，请选择单章进行风格蒸馏")
			return
		}
		if text.Len() > 0 {
			text.WriteString("\n\n")
		}
		text.WriteString(chapter.Title)
		text.WriteString("\n\n")
		text.WriteString(content)
	}
	if text.Len() == 0 {
		fail(w, 422, "当前文档没有可提取的文字；扫描 PDF 可使用原页阅读，暂不支持正文朗读、配图或风格蒸馏")
		return
	}
	reply(w, 200, map[string]any{"title": book.Title, "content": text.String()})
}

func (s *Server) readerSearch(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	book, ok := s.readerRequestedBook(w, r)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || !utf8.ValidString(q) || utf8.RuneCountInString(q) > 100 {
		fail(w, 400, "请输入 1–100 字的搜索内容")
		return
	}
	matches := []map[string]any{}
	for _, c := range book.Chapters {
		if err := r.Context().Err(); err != nil {
			return
		}
		content, err := readReaderChapter(book.Directory, c.Number)
		if err != nil {
			s.readerError(w, err)
			return
		}
		// Case folding can change byte widths. Search bytes efficiently, then
		// translate only matching offsets back to a rune window for the excerpt.
		runes := []rune(content)
		folded, query := strings.ToLower(content), strings.ToLower(q)
		length := utf8.RuneCountInString(query)
		contentHash := readerContentHash(content)
		for offset := 0; offset < len(folded); {
			hit := strings.Index(folded[offset:], query)
			if hit < 0 {
				break
			}
			hit += offset
			pos := utf8.RuneCountInString(folded[:hit])
			start, end := max(0, pos-45), min(len(runes), pos+length+85)
			excerpt := string(runes[start:end])
			if start > 0 {
				excerpt = "…" + excerpt
			}
			if end < len(runes) {
				excerpt += "…"
			}
			matches = append(matches, map[string]any{"chapter": c.Number, "title": c.Title, "excerpt": excerpt, "start": pos, "length": length, "contentHash": contentHash})
			offset = hit + len(query)
			if len(matches) >= 100 {
				reply(w, 200, map[string]any{"matches": matches, "truncated": true})
				return
			}
		}
	}
	reply(w, 200, map[string]any{"matches": matches, "truncated": false})
}

func (s *Server) readerProgress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory string `json:"directory"`
		readerProgressState
		// Old mobile/desktop clients omit these fields. Merge only omitted
		// preferences while holding the project lease, preserving explicit 0
		// paragraph spacing and false sidebar choices from current clients.
		FontFamily       *string  `json:"fontFamily"`
		PageWidth        *string  `json:"pageWidth"`
		ParagraphSpacing *float64 `json:"paragraphSpacing"`
		SidebarCollapsed *bool    `json:"sidebarCollapsed"`
	}
	if !readJSONLimit(w, r, &req, 1<<20) {
		return
	}
	if strings.TrimSpace(req.Directory) == "" {
		fail(w, 400, "保存阅读进度必须指定作品目录")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	directory, err := s.readerDirectoryLocked(req.Directory)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if s.closed {
		fail(w, 409, "工作台已关闭")
		return
	}
	err = s.withReaderProgressWriteLocked(directory, func() error {
		// A different instance may have restored or edited the book before
		// this lease became available. Validate against that locked version.
		current, err := readReaderBook(directory)
		if err != nil {
			return err
		}
		next := &req.readerProgressState
		next.FontFamily, next.PageWidth = current.Progress.FontFamily, current.Progress.PageWidth
		next.ParagraphSpacing, next.SidebarCollapsed = current.Progress.ParagraphSpacing, current.Progress.SidebarCollapsed
		if req.FontFamily != nil {
			next.FontFamily = *req.FontFamily
		}
		if req.PageWidth != nil {
			next.PageWidth = *req.PageWidth
		}
		if req.ParagraphSpacing != nil {
			next.ParagraphSpacing = *req.ParagraphSpacing
		}
		if req.SidebarCollapsed != nil {
			next.SidebarCollapsed = *req.SidebarCollapsed
		}
		// Reading recency is a server observation, never a client-supplied
		// timestamp (which could otherwise force a book atop the shelf).
		next.LastReadAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := validateReaderProgress(req.readerProgressState, current); err != nil {
			return fmt.Errorf("%w: %v", novelreader.ErrInvalid, err)
		}
		data, err := json.MarshalIndent(req.readerProgressState, "", "  ")
		if err != nil {
			return err
		}
		root, err := os.OpenRoot(directory)
		if err != nil {
			return err
		}
		defer root.Close()
		return readerWrite(root, filepath.Join("meta", "reader-progress.json"), data)
	})
	if err != nil {
		s.readerError(w, err)
		return
	}
	reply(w, 200, req.readerProgressState)
}

func (s *Server) withReaderProgressWriteLocked(directory string, write func() error) error {
	if s.dir == directory && s.rt != nil {
		return s.rt.WithProjectSnapshot(write)
	}
	if view := s.projectRuntimes[projectKey(directory)]; view != nil && view.rt != nil {
		return view.rt.WithProjectSnapshot(write)
	}
	return withWorkspaceLease(directory, write)
}

func readerSingleLine(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.ReplaceAll(value, "\x00", "")), " ")
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit])
	}
	return value
}

func (s *Server) readerImport(w http.ResponseWriter, r *http.Request) {
	select {
	case readerImports <- struct{}{}:
		defer func() { <-readerImports }()
	default:
		fail(w, 429, "已有书籍正在导入，请稍后重试")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, novelreader.MaxFileBytes+novelreader.MaxTextBytes+2<<20)
	if err := r.ParseMultipartForm(novelreader.MaxTextBytes); err != nil {
		if r.MultipartForm != nil {
			r.MultipartForm.RemoveAll()
		}
		fail(w, 400, "导入文件无效或超过大小限制")
		return
	}
	defer r.MultipartForm.RemoveAll()
	if len(r.MultipartForm.File["file"]) != 1 {
		fail(w, 400, "请选择一个小说文件")
		return
	}
	for key, files := range r.MultipartForm.File {
		if key != "file" || len(files) != 1 {
			fail(w, 400, "每次仅能导入一个小说文件")
			return
		}
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "未找到小说文件")
		return
	}
	defer f.Close()
	if header.Size > novelreader.MaxFileBytes {
		fail(w, 413, "导入文件不能超过 64 MiB")
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, novelreader.MaxFileBytes+1))
	if err != nil || len(data) > novelreader.MaxFileBytes {
		fail(w, 413, "导入文件不能超过 64 MiB")
		return
	}
	filename := path.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	var pages []novelreader.PDFPage
	if strings.EqualFold(filepath.Ext(filename), ".pdf") {
		raw := r.FormValue("pdfPages")
		if len(raw) > novelreader.MaxTextBytes || json.Unmarshal([]byte(raw), &pages) != nil {
			fail(w, 400, "PDF 解析结果无效或超过 32 MiB")
			return
		}
	}
	book, err := novelreader.Parse(filename, data, pages)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	name := readerSingleLine(book.Title, 80)
	if override := strings.TrimSpace(r.FormValue("title")); override != "" {
		if !utf8.ValidString(override) || utf8.RuneCountInString(override) > 80 {
			fail(w, 400, "书名不能超过 80 字")
			return
		}
		name = readerSingleLine(override, 80)
	}
	if name == "" {
		name = "导入的小说"
	}
	chapters := make([]manualChapterInput, len(book.Chapters))
	source := readerSourceInfo{Format: book.Format, Filename: readerSingleLine(filename, 255), Author: book.Author, PDFPages: map[int]int{}}
	for i, c := range book.Chapters {
		chapters[i] = manualChapterInput{Title: readerSingleLine(c.Title, 200), Content: c.Content}
		if c.PDFPage > 0 {
			source.PDFPages[i+1] = c.PDFPage
			source.Pages++
		}
		source.HasText = source.HasText || strings.TrimSpace(c.Content) != ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		fail(w, 409, "工作台已关闭")
		return
	}
	directory, err := s.createManualProjectLocked(name, chapters, func(directory string) error {
		root, err := os.OpenRoot(directory)
		if err != nil {
			return err
		}
		defer root.Close()
		if book.Format == "pdf" {
			if err = root.Mkdir("imports", 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			if err = readerWrite(root, filepath.Join("imports", "reader-original.pdf"), data); err != nil {
				return err
			}
		}
		metadata, err := json.MarshalIndent(source, "", "  ")
		if err != nil {
			return err
		}
		return readerWrite(root, filepath.Join("meta", "reader-source.json"), metadata)
	})
	if err != nil {
		s.readerError(w, err)
		return
	}
	view, err := readReaderBook(directory)
	if err != nil {
		s.readerError(w, err)
		return
	}
	reply(w, 201, map[string]any{"directory": directory, "book": view})
}

func (s *Server) readerSource(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	book, ok := s.readerRequestedBook(w, r)
	if !ok {
		s.mu.Unlock()
		return
	}
	if book.Source == nil || book.Source.Format != "pdf" {
		s.mu.Unlock()
		fail(w, 404, "此书没有 PDF 原页")
		return
	}
	root, err := os.OpenRoot(book.Directory)
	if err != nil {
		s.readerError(w, err)
		s.mu.Unlock()
		return
	}
	name := filepath.Join("imports", "reader-original.pdf")
	if err = readerSafePath(root, name, false); err != nil {
		root.Close()
		s.readerError(w, err)
		s.mu.Unlock()
		return
	}
	f, err := root.Open(name)
	root.Close()
	if err != nil {
		s.readerError(w, err)
		s.mu.Unlock()
		return
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > novelreader.MaxFileBytes {
		f.Close()
		fail(w, 400, "PDF 原文件无效或超过大小限制")
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	defer f.Close()
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="book.pdf"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "book.pdf", st.ModTime(), f)
}
