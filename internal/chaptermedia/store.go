// Package chaptermedia stores illustration attachments independently from
// accepted chapter prose. Callers own the project lease and serialize mutations.
package chaptermedia

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hansshaw/OpenNovel/internal/fileio"
	_ "golang.org/x/image/webp"
)

const MaxImageBytes = 20 << 20
const MaxImagePixels = 40_000_000
const MaxItems = 200
const maxManifestBytes = 1 << 20

var ErrInvalid = errors.New("配图数据无效")
var ErrUnsafe = errors.New("配图目录包含链接或不安全文件")
var ErrNotFound = errors.New("配图不存在")
var ErrCorrupt = errors.New("配图文件损坏或已变化")

// Item never contains an absolute path or an arbitrary resource URL.
type Item struct {
	ID        string `json:"id"`
	Chapter   int    `json:"chapter"`
	Name      string `json:"name"`
	MIME      string `json:"mime"`
	Bytes     int    `json:"bytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Caption   string `json:"caption"`
	Placement string `json:"placement"`
	Order     int    `json:"order"`
	CreatedAt string `json:"createdAt"`
	SHA256    string `json:"sha256"`
}

type manifest struct {
	Version int    `json:"version"`
	Items   []Item `json:"items"`
}

// Upload is produced by Prepare before callers acquire lifecycle/project locks.
// Its data and dimensions cannot be forged by API consumers.
type Upload struct {
	data          []byte
	name, mime    string
	width, height int
}

func Prepare(data []byte, name string) (Upload, error) {
	if len(data) == 0 || len(data) > MaxImageBytes {
		return Upload{}, fmt.Errorf("%w：图片须在 20 MiB 以内", ErrInvalid)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	mime := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp"}[format]
	if err != nil || mime == "" {
		return Upload{}, fmt.Errorf("%w：仅支持有效的 PNG、JPEG、WebP 或 GIF 图片", ErrInvalid)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > 16384 || config.Height > 16384 || int64(config.Width)*int64(config.Height) > MaxImagePixels {
		return Upload{}, fmt.Errorf("%w：图片尺寸不能超过 16384 像素或 4000 万像素", ErrInvalid)
	}
	// Decode the image as well as its header: truncated or fabricated headers must
	// not become durable chapter attachments.
	if format == "gif" {
		// DecodeAll retains every frame. First inspect the bounded input without
		// allocating frame buffers so an animation cannot exhaust memory before
		// its complete payload is validated.
		if err := validateGIFFrameBudget(data); err != nil {
			return Upload{}, err
		}
		if _, err := gif.DecodeAll(bytes.NewReader(data)); err != nil {
			return Upload{}, fmt.Errorf("%w：GIF 动画内容无法完整解码", ErrInvalid)
		}
	} else if _, decodedFormat, err := image.Decode(bytes.NewReader(data)); err != nil || decodedFormat != format {
		return Upload{}, fmt.Errorf("%w：图片内容无法完整解码", ErrInvalid)
	}
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if !utf8.ValidString(name) || name == "." || name == "/" || name == "" {
		name = "章节配图"
	}
	if len([]rune(name)) > 120 {
		name = string([]rune(name)[:120])
	}
	return Upload{data: append([]byte(nil), data...), name: name, mime: mime, width: config.Width, height: config.Height}, nil
}

func validID(id string) bool {
	raw, err := hex.DecodeString(id)
	return err == nil && len(raw) == 16 && strings.ToLower(id) == id
}
func extension(mime string) string {
	return map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[mime]
}
func validOptions(caption, placement string, order int) bool {
	return utf8.ValidString(caption) && len([]rune(caption)) <= 1000 && !strings.ContainsRune(caption, '\x00') && (placement == "chapter-start" || placement == "chapter-end") && order >= 0 && order <= 10000
}
func chapterDir(chapter int) string { return fmt.Sprintf("media/chapters/%06d", chapter) }
func itemPath(item Item) string {
	return path.Join(chapterDir(item.Chapter), item.ID+extension(item.MIME))
}

func openProject(directory string) (*os.Root, error) {
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnsafe
	}
	return os.OpenRoot(directory)
}

func safeInfo(root *os.Root, rel string) (os.FileInfo, error) {
	var parent string
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		parent = path.Join(parent, part)
		info, err := root.Lstat(parent)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrUnsafe
		}
	}
	info, err := root.Lstat(rel)
	if err == nil && !info.IsDir() && !info.Mode().IsRegular() {
		return nil, ErrUnsafe
	}
	return info, err
}

func readRegular(root *os.Root, rel string, max int64) ([]byte, error) {
	info, err := safeInfo(root, rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrUnsafe
	}
	if info.Size() > max {
		return nil, ErrCorrupt
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, ErrUnsafe
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if len(data) > int(max) {
		return nil, ErrCorrupt
	}
	return data, err
}

func ensureDir(root *os.Root, rel string) error {
	var parent string
	for _, part := range strings.Split(rel, "/") {
		parent = path.Join(parent, part)
		info, err := root.Lstat(parent)
		if os.IsNotExist(err) {
			if err := root.Mkdir(parent, 0700); err != nil {
				return err
			}
			info, err = root.Lstat(parent)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafe
		}
	}
	return nil
}

func load(root *os.Root, chapter int) (manifest, error) {
	m := manifest{Version: 1, Items: []Item{}}
	if chapter < 1 || chapter > 1_000_000 {
		return m, ErrInvalid
	}
	data, err := readRegular(root, path.Join(chapterDir(chapter), "manifest.json"), maxManifestBytes)
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if json.Unmarshal(data, &m) != nil || m.Version != 1 || len(m.Items) > MaxItems {
		return m, ErrCorrupt
	}
	ids := map[string]bool{}
	for _, item := range m.Items {
		if !validID(item.ID) || ids[item.ID] || item.Chapter != chapter || extension(item.MIME) == "" || item.Bytes < 1 || item.Bytes > MaxImageBytes || item.Width < 1 || item.Height < 1 || item.Width > 16384 || item.Height > 16384 || int64(item.Width)*int64(item.Height) > MaxImagePixels || len(item.SHA256) != 64 || !validOptions(item.Caption, item.Placement, item.Order) {
			return m, ErrCorrupt
		}
		ids[item.ID] = true
	}
	if m.Items == nil {
		m.Items = []Item{}
	}
	sort.SliceStable(m.Items, func(i, j int) bool {
		if m.Items[i].Order != m.Items[j].Order {
			return m.Items[i].Order < m.Items[j].Order
		}
		return m.Items[i].CreatedAt < m.Items[j].CreatedAt
	})
	return m, nil
}

func writeNew(root *os.Root, rel string, data []byte) error {
	if _, err := safeInfo(root, path.Dir(rel)); err != nil {
		return err
	}
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			_ = root.Remove(rel)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func save(root *os.Root, chapter int, m manifest) error {
	rel := path.Join(chapterDir(chapter), "manifest.json")
	if info, err := safeInfo(root, rel); err == nil && !info.Mode().IsRegular() {
		return ErrUnsafe
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// JSON escaping can multiply otherwise-valid captions and names. Keep the
	// published manifest inside the reader's bound; Add removes its unpublished
	// image on this error and edits retain the previous readable manifest.
	if len(data) > maxManifestBytes {
		return fmt.Errorf("%w：本章配图资料超过容量限制，请缩短图注或减少配图", ErrInvalid)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmp := rel + ".tmp-" + hex.EncodeToString(random[:])
	if err := writeNew(root, tmp, data); err != nil {
		return err
	}
	defer root.Remove(tmp)
	return fileio.RootRename(root, tmp, rel)
}

func List(directory string, chapter int) ([]Item, error) {
	root, err := openProject(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	m, err := load(root, chapter)
	return m.Items, err
}

func Add(directory string, chapter int, upload Upload, caption, placement string) (Item, error) {
	if !validOptions(caption, placement, 0) || len(upload.data) == 0 || len(upload.data) > MaxImageBytes || extension(upload.mime) == "" {
		return Item{}, ErrInvalid
	}
	root, err := openProject(directory)
	if err != nil {
		return Item{}, err
	}
	defer root.Close()
	m, err := load(root, chapter)
	if err != nil {
		return Item{}, err
	}
	if len(m.Items) >= MaxItems {
		return Item{}, fmt.Errorf("%w：每章最多保存 200 张配图", ErrInvalid)
	}
	if err := ensureDir(root, chapterDir(chapter)); err != nil {
		return Item{}, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Item{}, err
	}
	digest := sha256.Sum256(upload.data)
	item := Item{ID: hex.EncodeToString(random[:]), Chapter: chapter, Name: upload.name, MIME: upload.mime, Bytes: len(upload.data), Width: upload.width, Height: upload.height, Caption: caption, Placement: placement, Order: len(m.Items), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), SHA256: hex.EncodeToString(digest[:])}
	if err := writeNew(root, itemPath(item), upload.data); err != nil {
		return Item{}, err
	}
	m.Items = append(m.Items, item)
	if err := save(root, chapter, m); err != nil {
		_ = root.Remove(itemPath(item))
		return Item{}, err
	}
	return item, nil
}

type Update struct {
	Caption   *string `json:"caption"`
	Placement *string `json:"placement"`
	Order     *int    `json:"order"`
}

func Edit(directory string, chapter int, id string, edit Update) error {
	if !validID(id) {
		return ErrInvalid
	}
	root, err := openProject(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	m, err := load(root, chapter)
	if err != nil {
		return err
	}
	for i := range m.Items {
		item := &m.Items[i]
		if item.ID != id {
			continue
		}
		if edit.Caption != nil {
			item.Caption = *edit.Caption
		}
		if edit.Placement != nil {
			item.Placement = *edit.Placement
		}
		if edit.Order != nil {
			item.Order = *edit.Order
		}
		if !validOptions(item.Caption, item.Placement, item.Order) {
			return ErrInvalid
		}
		return save(root, chapter, m)
	}
	return ErrNotFound
}

func Remove(directory string, chapter int, id string) error {
	if !validID(id) {
		return ErrInvalid
	}
	root, err := openProject(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	m, err := load(root, chapter)
	if err != nil {
		return err
	}
	for i, item := range m.Items {
		if item.ID != id {
			continue
		}
		if _, err := safeInfo(root, itemPath(item)); err != nil && !os.IsNotExist(err) {
			return err
		}
		// Unpublish the attachment before removing its file. An interrupted cleanup
		// may leave an unreachable image, but never a visible broken attachment.
		m.Items = append(m.Items[:i], m.Items[i+1:]...)
		if err := save(root, chapter, m); err != nil {
			return err
		}
		_ = root.Remove(itemPath(item))
		return nil
	}
	return ErrNotFound
}

func Read(directory string, chapter int, id string) (Item, []byte, error) {
	if !validID(id) {
		return Item{}, nil, ErrInvalid
	}
	root, err := openProject(directory)
	if err != nil {
		return Item{}, nil, err
	}
	defer root.Close()
	m, err := load(root, chapter)
	if err != nil {
		return Item{}, nil, err
	}
	for _, item := range m.Items {
		if item.ID != id {
			continue
		}
		data, err := readRegular(root, itemPath(item), MaxImageBytes)
		if err != nil {
			return Item{}, nil, err
		}
		digest := sha256.Sum256(data)
		if len(data) != item.Bytes || hex.EncodeToString(digest[:]) != item.SHA256 {
			return Item{}, nil, ErrCorrupt
		}
		return item, data, nil
	}
	return Item{}, nil, ErrNotFound
}
