// Package novelreader imports untrusted books into plain-text chapters. It never
// executes document scripts or fetches resources referenced by a document.
package novelreader

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

const (
	MaxFileBytes    = 64 << 20
	MaxTextBytes    = 32 << 20
	MaxChapterBytes = 8 << 20
	MaxChapters     = 10000
	MaxPDFPages     = 2000
)

var ErrInvalid = errors.New("invalid book")

type Chapter struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	PDFPage int    `json:"pdfPage,omitempty"`
}

type Book struct {
	Title    string    `json:"title"`
	Author   string    `json:"author,omitempty"`
	Format   string    `json:"format"`
	Chapters []Chapter `json:"chapters"`
}

type PDFPage struct {
	Page  int    `json:"page"`
	Title string `json:"title,omitempty"`
	Text  string `json:"text"`
}

func invalid(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }

func Parse(filename string, data []byte, pages []PDFPage) (Book, error) {
	var book Book
	if len(data) == 0 || len(data) > MaxFileBytes {
		return book, invalid("文件不能为空且不能超过 64 MiB")
	}
	format := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	book.Title = strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	book.Format = format
	var err error
	switch format {
	case "txt", "md", "markdown":
		var text string
		text, err = DecodeText(data)
		if err == nil {
			book.Chapters = SplitText(text, book.Title)
		}
	case "html", "htm":
		var text string
		text, err = DecodeText(data)
		if err == nil {
			var title string
			text, title, err = HTMLText(text)
			if title != "" {
				book.Title = title
			}
			if err == nil {
				book.Chapters = SplitText(text, book.Title)
			}
		}
	case "epub":
		book, err = parseEPUB(data, book.Title)
	case "docx":
		book.Chapters, err = parseDOCX(data, book.Title)
	case "pdf":
		if !bytes.HasPrefix(bytes.TrimSpace(data[:min(len(data), 1024)]), []byte("%PDF-")) {
			return book, invalid("PDF 文件头无效")
		}
		if len(pages) == 0 || len(pages) > MaxPDFPages {
			return book, invalid("请先用阅读器解析 PDF，页数须为 1–2000")
		}
		for i, page := range pages {
			if page.Page != i+1 {
				return book, invalid("PDF 页码必须从 1 开始连续排列")
			}
			title := strings.TrimSpace(page.Title)
			if title == "" {
				title = fmt.Sprintf("第 %d 页", i+1)
			}
			book.Chapters = append(book.Chapters, Chapter{Title: title, Content: cleanText(page.Text), PDFPage: i + 1})
		}
	default:
		return book, invalid("支持 TXT、Markdown、HTML、EPUB、DOCX 和 PDF 文件")
	}
	if err != nil {
		return Book{}, err
	}
	if err := Validate(book); err != nil {
		return Book{}, err
	}
	return book, nil
}

func Validate(book Book) error {
	if len(book.Chapters) == 0 || len(book.Chapters) > MaxChapters {
		return invalid("未找到可读章节，或章节数超过 10000")
	}
	if !utf8.ValidString(book.Title) || len([]rune(book.Title)) > 500 || !utf8.ValidString(book.Author) || len([]rune(book.Author)) > 500 {
		return invalid("书名或作者信息无效")
	}
	total, nonempty := 0, false
	for _, chapter := range book.Chapters {
		if !utf8.ValidString(chapter.Title) || len([]rune(chapter.Title)) > 500 || !utf8.ValidString(chapter.Content) || len(chapter.Content) > MaxChapterBytes {
			return invalid("章节标题或正文无效，单章正文上限为 8 MiB")
		}
		total += len(chapter.Content)
		nonempty = nonempty || strings.TrimSpace(chapter.Content) != ""
		if total > MaxTextBytes {
			return invalid("提取正文总量超过 32 MiB")
		}
	}
	if !nonempty && book.Format != "pdf" {
		return invalid("文档没有可提取的正文")
	}
	return nil
}

func DecodeText(data []byte) (string, error) {
	if len(data) > MaxTextBytes {
		return "", invalid("文本文件超过 32 MiB")
	}
	var decoded []byte
	var err error
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}):
		decoded, _, err = transform.Bytes(unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder(), data)
	case bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		decoded, _, err = transform.Bytes(unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM).NewDecoder(), data)
	case bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}):
		decoded = data[3:]
	case utf8.Valid(data):
		decoded = data
	default:
		decoded, _, err = transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), data)
	}
	if len(decoded) > MaxTextBytes {
		return "", invalid("解码后的正文超过 32 MiB")
	}
	if err != nil || !utf8.Valid(decoded) || bytes.IndexByte(decoded, 0) >= 0 || bytes.Contains(decoded, []byte("\ufffd")) {
		return "", invalid("文件编码无效；请使用 UTF-8、UTF-16 或 GB18030 文本")
	}
	return cleanText(string(decoded)), nil
}

func cleanText(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	return strings.TrimSpace(strings.TrimPrefix(text, "\ufeff"))
}

var chapterHeading = regexp.MustCompile(`(?i)^(?:#{1,3}\s+.+|第[零〇一二三四五六七八九十百千万两0-9０-９]+[章回节卷部集篇](?:\s*.*)?|(?:chapter|part|book)\s+(?:[0-9ivxlcdm]+|one|two|three|four|five|six|seven|eight|nine|ten)(?:\b.*)?|序章|序言|楔子|尾声|后记|番外(?:\s*.*)?)$`)

// SplitText recognizes common Chinese/English headings. Files without headings
// remain readable; very long sections are split at paragraph boundaries.
func SplitText(text, fallback string) []Chapter {
	text = cleanText(text)
	if text == "" {
		return nil
	}
	var chapters []Chapter
	title := fallback
	var lines []string
	flush := func() {
		body := strings.TrimSpace(strings.Join(lines, "\n"))
		if body != "" {
			chapters = append(chapters, Chapter{Title: title, Content: body})
		}
		lines = nil
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if utf8.RuneCountInString(trimmed) <= 100 && chapterHeading.MatchString(trimmed) {
			flush()
			title = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			continue
		}
		lines = append(lines, line)
	}
	flush()
	if len(chapters) == 0 {
		return []Chapter{{Title: fallback, Content: text}}
	}
	var bounded []Chapter
	for _, ch := range chapters {
		// A modest text chunk keeps chapter navigation and speech usable even if
		// the source has no headings. This is independent of rendering pagination.
		remaining := ch.Content
		for part := 1; len(remaining) > 180000; part++ {
			cut := strings.LastIndex(remaining[:180000], "\n")
			if cut < 90000 {
				cut = 180000
				for cut > 0 && !utf8.RuneStart(remaining[cut]) {
					cut--
				}
			}
			bounded = append(bounded, Chapter{Title: fmt.Sprintf("%s（%d）", ch.Title, part), Content: strings.TrimSpace(remaining[:cut])})
			remaining = strings.TrimSpace(remaining[cut:])
		}
		if remaining != "" {
			bounded = append(bounded, Chapter{Title: ch.Title, Content: remaining})
		}
	}
	return bounded
}
