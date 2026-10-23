package novelreader

import (
	"io"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// HTMLText uses a bounded streaming tokenizer instead of retaining an untrusted
// DOM. It returns inert text; no URLs, scripts, or assets are fetched or executed.
func HTMLText(source string) (string, string, error) {
	if len(source) > MaxChapterBytes {
		return "", "", invalid("HTML 章节超过 8 MiB")
	}
	type frame struct {
		tag                               string
		ignored, title, heading, excluded bool
	}
	stack := []frame{}
	var out, title, heading strings.Builder
	z := html.NewTokenizer(strings.NewReader(source))
	z.SetMaxBuf(MaxChapterBytes)
	writeText := func(b *strings.Builder, text string) {
		space := false
		for _, r := range text {
			if unicode.IsSpace(r) {
				if !space {
					b.WriteByte(' ')
				}
				space = true
			} else {
				b.WriteRune(r)
				space = false
			}
		}
	}
	for {
		tokenType := z.Next()
		if tokenType == html.ErrorToken {
			if z.Err() != io.EOF {
				return "", "", invalid("HTML 内容损坏或超过限制")
			}
			break
		}
		current := frame{}
		if len(stack) > 0 {
			current = stack[len(stack)-1]
		}
		switch tokenType {
		case html.StartTagToken, html.SelfClosingTagToken:
			token := z.Token()
			// HTML permits omitted paragraph and head end tags. Avoid treating
			// an ordinary exported document as thousands of nested paragraphs.
			for i := len(stack) - 1; i >= 0; i-- {
				closeHead := stack[i].tag == "head" && !headElement(token.Data)
				closeParagraph := stack[i].tag == "p" && blockElement(token.Data) && token.Data != "br"
				if closeHead || closeParagraph {
					stack = stack[:i]
					break
				}
			}
			current = frame{}
			if len(stack) > 0 {
				current = stack[len(stack)-1]
			}
			next := frame{tag: token.Data, ignored: current.ignored, title: current.title, heading: current.heading, excluded: current.excluded}
			if token.Data == "title" && title.Len() == 0 {
				next.title = true
			}
			if token.Data == "h1" && heading.Len() == 0 && !current.ignored {
				next.heading = true
			}
			switch token.Data {
			case "script", "style", "noscript", "template", "svg", "math", "iframe", "object", "embed":
				next.ignored = true
				next.excluded = true
			case "head", "title":
				next.ignored = true
			}
			for _, attr := range token.Attr {
				if attr.Key == "hidden" || attr.Key == "aria-hidden" && attr.Val == "true" {
					next.ignored = true
					next.excluded = true
				}
			}
			if blockElement(token.Data) && !next.ignored {
				out.WriteByte('\n')
			}
			if tokenType != html.SelfClosingTagToken && !voidElement(token.Data) {
				if len(stack) >= 256 {
					return "", "", invalid("HTML 嵌套过深")
				}
				stack = append(stack, next)
			}
		case html.EndTagToken:
			tag, _ := z.TagName()
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].tag == string(tag) {
					if blockElement(string(tag)) && !stack[i].ignored {
						out.WriteByte('\n')
					}
					stack = stack[:i]
					break
				}
			}
		case html.TextToken:
			text := string(z.Text())
			if !current.excluded {
				if current.title {
					writeText(&title, text)
				}
				if current.heading {
					writeText(&heading, text)
				}
			}
			if !current.ignored {
				writeText(&out, text)
			}
		}
		if out.Len() > MaxChapterBytes || title.Len() > MaxChapterBytes || heading.Len() > MaxChapterBytes {
			return "", "", invalid("HTML 正文超过 8 MiB")
		}
	}
	lines := strings.Split(out.String(), "\n")
	clean := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			clean = append(clean, line)
		}
	}
	bookTitle := strings.TrimSpace(title.String())
	if heading.Len() > 0 {
		bookTitle = strings.TrimSpace(heading.String())
	}
	return strings.Join(clean, "\n\n"), bookTitle, nil
}

func headElement(tag string) bool {
	switch tag {
	case "base", "basefont", "bgsound", "link", "meta", "title", "noscript", "noframes", "style", "script", "template":
		return true
	}
	return false
}

func blockElement(tag string) bool {
	switch tag {
	case "p", "div", "br", "hr", "li", "ul", "ol", "h1", "h2", "h3", "h4", "h5", "h6", "section", "article", "blockquote", "pre", "tr", "table":
		return true
	}
	return false
}

func voidElement(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	}
	return false
}
