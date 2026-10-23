package novelreader

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"net/url"
	"path"
	"strings"
)

type bookArchive map[string]*zip.File

func openArchive(data []byte) (bookArchive, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, invalid("文档 ZIP 容器损坏")
	}
	if len(z.File) > 10000 {
		return nil, invalid("文档包含过多文件")
	}
	files := bookArchive{}
	var size uint64
	for _, f := range z.File {
		name := strings.TrimSuffix(f.Name, "/")
		if name == "" || strings.ContainsAny(name, "\\\x00:") || strings.HasPrefix(name, "/") || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return nil, invalid("文档包含不安全的文件路径")
		}
		if _, exists := files[name]; exists {
			return nil, invalid("文档包含重复文件路径")
		}
		if !f.Mode().IsRegular() && !f.FileInfo().IsDir() {
			return nil, invalid("文档包含特殊文件或链接")
		}
		if f.Flags&1 != 0 {
			return nil, invalid("不支持加密文档")
		}
		if f.UncompressedSize64 > 16<<20 {
			return nil, invalid("文档单个资源超过 16 MiB")
		}
		size += f.UncompressedSize64
		if size > 128<<20 {
			return nil, invalid("文档解压总量超过 128 MiB")
		}
		files[name] = f
	}
	return files, nil
}

func (files bookArchive) read(name string, limit int64) ([]byte, error) {
	f := files[name]
	if f == nil || f.FileInfo().IsDir() {
		return nil, invalid("文档缺少文件：" + name)
	}
	if f.UncompressedSize64 > uint64(limit) {
		return nil, invalid("文档内容超过限制")
	}
	r, err := f.Open()
	if err != nil {
		return nil, invalid("无法读取文档资源")
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, invalid("文档资源损坏或超过限制")
	}
	return data, nil
}

func archiveReference(base, ref string) (string, error) {
	u, err := url.Parse(ref)
	if err != nil || u.Scheme != "" || u.Host != "" || u.RawQuery != "" || strings.ContainsAny(u.Path, "\\\x00:") || strings.HasPrefix(u.Path, "/") {
		return "", invalid("文档正文不能引用外部地址")
	}
	name := path.Clean(path.Join(path.Dir(base), u.Path))
	if name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return "", invalid("文档引用路径越界")
	}
	return name, nil
}

func parseEPUB(data []byte, fallback string) (Book, error) {
	book := Book{Title: fallback, Format: "epub"}
	files, err := openArchive(data)
	if err != nil {
		return book, err
	}
	if mime, err := files.read("mimetype", 100); err != nil || strings.TrimSpace(string(mime)) != "application/epub+zip" {
		return book, invalid("不是有效的 EPUB 文件")
	}
	if files["META-INF/encryption.xml"] != nil {
		b, err := files.read("META-INF/encryption.xml", 1<<20)
		if err != nil {
			return book, err
		}
		decoder := xml.NewDecoder(bytes.NewReader(b))
		for {
			tok, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return book, invalid("EPUB 加密信息损坏")
			}
			if e, ok := tok.(xml.StartElement); ok && e.Name.Local == "EncryptionMethod" {
				for _, a := range e.Attr {
					if a.Name.Local == "Algorithm" && a.Value != "http://www.idpf.org/2008/embedding" && a.Value != "http://ns.adobe.com/pdf/enc#RC" {
						return book, invalid("不支持受 DRM 保护的 EPUB")
					}
				}
			}
		}
	}
	container, err := files.read("META-INF/container.xml", 1<<20)
	if err != nil {
		return book, err
	}
	var c struct {
		Roots []struct {
			Path string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if xml.Unmarshal(container, &c) != nil || len(c.Roots) == 0 {
		return book, invalid("EPUB 缺少内容索引")
	}
	opfPath, err := archiveReference("root", c.Roots[0].Path)
	if err != nil {
		return book, err
	}
	opf, err := files.read(opfPath, 4<<20)
	if err != nil {
		return book, err
	}
	var pkg struct {
		Metadata struct {
			Title   string `xml:"title"`
			Creator string `xml:"creator"`
		} `xml:"metadata"`
		Manifest []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			Media      string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"manifest>item"`
		Spine []struct {
			ID     string `xml:"idref,attr"`
			Linear string `xml:"linear,attr"`
		} `xml:"spine>itemref"`
	}
	if xml.Unmarshal(opf, &pkg) != nil || len(pkg.Spine) == 0 || len(pkg.Spine) > MaxChapters {
		return book, invalid("EPUB 阅读顺序无效")
	}
	if v := strings.TrimSpace(pkg.Metadata.Title); v != "" {
		book.Title = v
	}
	book.Author = strings.TrimSpace(pkg.Metadata.Creator)
	manifest := make(map[string]int)
	for i, item := range pkg.Manifest {
		if item.ID == "" {
			continue
		}
		if _, exists := manifest[item.ID]; exists {
			return book, invalid("EPUB 索引重复")
		}
		manifest[item.ID] = i
	}
	total := 0
	for _, entry := range pkg.Spine {
		if entry.Linear == "no" {
			continue
		}
		i, ok := manifest[entry.ID]
		if !ok {
			return book, invalid("EPUB 章节索引缺失")
		}
		item := pkg.Manifest[i]
		if item.Media != "application/xhtml+xml" && item.Media != "text/html" {
			return book, invalid("EPUB 章节格式不支持")
		}
		name, err := archiveReference(opfPath, item.Href)
		if err != nil {
			return book, err
		}
		b, err := files.read(name, MaxChapterBytes)
		if err != nil {
			return book, err
		}
		text, title, err := HTMLText(string(b))
		if err != nil {
			return book, err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		if title == "" {
			title = strings.TrimSuffix(path.Base(name), path.Ext(name))
		}
		total += len(text)
		if total > MaxTextBytes {
			return book, invalid("提取正文总量超过 32 MiB")
		}
		book.Chapters = append(book.Chapters, Chapter{Title: title, Content: text})
	}
	return book, nil
}

func parseDOCX(data []byte, fallback string) ([]Chapter, error) {
	files, err := openArchive(data)
	if err != nil {
		return nil, err
	}
	document, err := files.read("word/document.xml", 16<<20)
	if err != nil {
		return nil, err
	}
	decoder := xml.NewDecoder(bytes.NewReader(document))
	var text strings.Builder
	inText, ignoredDepth := false, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, invalid("DOCX 正文损坏")
		}
		switch t := token.(type) {
		case xml.StartElement:
			if ignoredDepth > 0 {
				ignoredDepth++
				continue
			}
			if t.Name.Local == "del" || t.Name.Local == "instrText" {
				ignoredDepth = 1
				continue
			}
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				text.WriteByte('\t')
			case "br", "cr":
				text.WriteByte('\n')
			}
		case xml.CharData:
			if inText && ignoredDepth == 0 {
				text.Write(t)
			}
		case xml.EndElement:
			if ignoredDepth > 0 {
				ignoredDepth--
				continue
			}
			if t.Name.Local == "t" {
				inText = false
			}
			if t.Name.Local == "p" {
				text.WriteString("\n\n")
			}
		}
		if text.Len() > MaxTextBytes {
			return nil, invalid("DOCX 正文超过 32 MiB")
		}
	}
	return SplitText(text.String(), fallback), nil
}
