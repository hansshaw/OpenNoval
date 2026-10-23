package novelreader

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func makeArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, content := range files {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func epubFiles() map[string]string {
	return map[string]string{
		"mimetype":               "application/epub+zip",
		"META-INF/container.xml": `<container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>`,
		"OEBPS/content.opf":      `<package><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>夜航</dc:title><dc:creator>作者</dc:creator></metadata><manifest><item id="first" href="one.xhtml" media-type="application/xhtml+xml"/><item id="second" href="two.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="second"/><itemref idref="first"/></spine></package>`,
		"OEBPS/one.xhtml":        `<html><body><h1>先见</h1><p>甲章的正文。</p></body></html>`,
		"OEBPS/two.xhtml":        `<html><head><title>浏览器标题</title></head><body><h1>归来</h1><p>海风吹来。</p><script>fetch('https://evil.test')</script><style>.secret{}</style><p hidden>隐藏文字</p></body></html>`,
	}
}

func TestNovelReaderTextEncodingsAndChapterSplit(t *testing.T) {
	text := "第一章 风起\n夜色沉静。\n\n第二章 归来\n又见故人。"
	gb, _, err := transform.Bytes(simplifiedchinese.GB18030.NewEncoder(), []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	utf16data := []byte{0xff, 0xfe}
	for _, r := range utf16.Encode([]rune(text)) {
		utf16data = binary.LittleEndian.AppendUint16(utf16data, r)
	}
	for name, data := range map[string][]byte{"UTF8": []byte(text), "BOM": append([]byte{0xef, 0xbb, 0xbf}, []byte(text)...), "GB18030": gb, "UTF16": utf16data} {
		t.Run(name, func(t *testing.T) {
			book, err := Parse("星河.txt", data, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(book.Chapters) != 2 || book.Chapters[0].Title != "第一章 风起" || book.Chapters[1].Content != "又见故人。" {
				t.Fatalf("unexpected chapters: %+v", book.Chapters)
			}
		})
	}
	if _, err := Parse("binary.txt", []byte{0, 1, 2, 3}, nil); err == nil {
		t.Fatal("binary input accepted")
	}
}

func TestNovelReaderEPUBSpineAndInertHTML(t *testing.T) {
	book, err := Parse("novel.epub", makeArchive(t, epubFiles()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if book.Title != "夜航" || book.Author != "作者" || len(book.Chapters) != 2 || book.Chapters[0].Title != "归来" {
		t.Fatalf("metadata/spine lost: %+v", book)
	}
	if !strings.Contains(book.Chapters[0].Content, "海风") || strings.Contains(book.Chapters[0].Content, "evil.test") || strings.Contains(book.Chapters[0].Content, "隐藏文字") {
		t.Fatalf("unsafe HTML extraction: %q", book.Chapters[0].Content)
	}
	for _, unsafe := range []string{"../outside.txt", "/absolute.txt", `C:\escape.txt`, "OEBPS/../duplicate.txt"} {
		files := epubFiles()
		files[unsafe] = "bad"
		if _, err := Parse("unsafe.epub", makeArchive(t, files), nil); err == nil {
			t.Errorf("unsafe ZIP path accepted: %s", unsafe)
		}
	}
	files := epubFiles()
	files["OEBPS/content.opf"] = strings.ReplaceAll(files["OEBPS/content.opf"], "two.xhtml", "https://example.com/remote.xhtml")
	if _, err := Parse("external.epub", makeArchive(t, files), nil); err == nil {
		t.Fatal("external spine accepted")
	}
	files = epubFiles()
	files["META-INF/encryption.xml"] = `<encryption><EncryptedData><EncryptionMethod Algorithm="urn:drm:private"/></EncryptedData></encryption>`
	if _, err := Parse("protected.epub", makeArchive(t, files), nil); err == nil {
		t.Fatal("DRM EPUB accepted")
	}
}

func TestNovelReaderArchiveRejectsExpansionAndLinks(t *testing.T) {
	files := epubFiles()
	files["huge.bin"] = strings.Repeat("0", (16<<20)+1)
	if _, err := Parse("bomb.epub", makeArchive(t, files), nil); err == nil {
		t.Fatal("expansion limit ignored")
	}
	var buffer bytes.Buffer
	z := zip.NewWriter(&buffer)
	f, err := z.CreateHeader(&zip.FileHeader{Name: "link", ExternalAttrs: 0120777 << 16, CreatorVersion: 3 << 8})
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("/etc/passwd"))
	z.Close()
	if _, err := openArchive(buffer.Bytes()); err == nil {
		t.Fatal("ZIP symlink accepted")
	}
}

func TestNovelReaderDOCXAndPDFScans(t *testing.T) {
	data := makeArchive(t, map[string]string{"word/document.xml": `<w:document xmlns:w="word"><w:body><w:p><w:r><w:t>第一章 起点</w:t></w:r></w:p><w:p><w:r><w:t>正文段落</w:t></w:r><w:del><w:r><w:t>已删除</w:t></w:r></w:del></w:p></w:body></w:document>`})
	book, err := Parse("manuscript.docx", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(book.Chapters) != 1 || book.Chapters[0].Content != "正文段落" {
		t.Fatalf("DOCX body: %+v", book)
	}
	book, err = Parse("scan.pdf", []byte("%PDF-1.7\n..."), []PDFPage{{Page: 1}, {Page: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(book.Chapters) != 2 || book.Chapters[1].PDFPage != 2 || book.Chapters[1].Content != "" {
		t.Fatalf("scan page lost: %+v", book)
	}
	for _, pages := range [][]PDFPage{nil, {{Page: 2}}, {{Page: 1}, {Page: 1}}} {
		if _, err := Parse("invalid.pdf", []byte("%PDF-1.7"), pages); err == nil {
			t.Fatal("invalid PDF page numbering accepted")
		}
	}
	if _, err := Parse("bad.pdf", []byte("this is not a PDF"), []PDFPage{{Page: 1}}); err == nil {
		t.Fatal("invalid PDF signature accepted")
	}
}

func TestNovelReaderHTMLBoundsAndOptionalEndTags(t *testing.T) {
	text, title, err := HTMLText("<head><title>书名</title><body>" + strings.Repeat("<p>可读段落", 300))
	if err != nil || title != "书名" || strings.Count(text, "可读段落") != 300 || strings.Contains(text, "书名") {
		t.Fatalf("ordinary HTML optional closing tags failed: %q %v", title, err)
	}
	if _, _, err := HTMLText(strings.Repeat("<div>", 257) + "正文"); err == nil {
		t.Fatal("excessive HTML nesting accepted")
	}
}
