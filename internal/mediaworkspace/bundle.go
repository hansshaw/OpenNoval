// Package mediaworkspace defines the portable chapter-media format. It has no
// dependency on the desktop transport, project store, or a rendering engine, so
// a future editor can use the same chapter and image data through an adapter.
package mediaworkspace

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

const Format = "ainovel-chapter-media"
const SchemaVersion = 1
const MaxImageBytes = 256 << 20

var ErrTooLarge = errors.New("本章配图总量超过 256 MiB，请减少配图后再导出")
var ErrInvalid = errors.New("章节配图包数据无效")
var ErrCorrupt = errors.New("配图文件损坏或已变化")

type Plan struct {
	CoreEvent   string   `json:"coreEvent"`
	Hook        string   `json:"hook"`
	Scenes      []string `json:"scenes"`
	Fingerprint string   `json:"fingerprint"`
}

type Chapter struct {
	Number        int     `json:"number"`
	Title         string  `json:"title"`
	VolumeTitle   string  `json:"volumeTitle"`
	Body          *string `json:"body,omitempty"`
	ContentSHA256 string  `json:"contentSHA256,omitempty"`
	Plan          *Plan   `json:"plan,omitempty"`
}

// Image contains portable metadata only. File is assigned by CreateArchive;
// neither a project path nor a remote URL is accepted as an archive location.
type Image struct {
	ID        string `json:"id"`
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
	File      string `json:"file"`
}

type Manifest struct {
	Format        string  `json:"format"`
	SchemaVersion int     `json:"schemaVersion"`
	ExportedAt    string  `json:"exportedAt"`
	Chapter       Chapter `json:"chapter"`
	Images        []Image `json:"images"`
}

// ReadImage adapts an image store to the portable exporter. Implementations
// should check that current metadata still matches the supplied snapshot.
type ReadImage func(context.Context, Image) ([]byte, error)

// CreateArchive returns a complete, owner-only temporary ZIP at position zero.
// The caller must close and remove it. Failed or cancelled exports remove their
// temporary file and never return a partially written artifact.
func CreateArchive(ctx context.Context, chapter Chapter, images []Image, read ReadImage) (_ *os.File, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if chapter.Number < 1 || chapter.Number > 1_000_000 || (chapter.Body == nil && chapter.Plan == nil) || (len(images) > 0 && read == nil) {
		return nil, ErrInvalid
	}
	manifest := Manifest{Format: Format, SchemaVersion: SchemaVersion, ExportedAt: time.Now().UTC().Format(time.RFC3339Nano), Chapter: chapter, Images: append([]Image{}, images...)}
	if chapter.Body != nil {
		digest := sha256.Sum256([]byte(*chapter.Body))
		manifest.Chapter.ContentSHA256 = hex.EncodeToString(digest[:])
	} else {
		manifest.Chapter.ContentSHA256 = ""
	}
	var total int64
	seen := make(map[string]bool, len(images))
	for i := range manifest.Images {
		item := &manifest.Images[i]
		ext := imageExtension(item.MIME)
		if !validHex(item.ID, 32) || !validHex(item.SHA256, 64) || seen[item.ID] || ext == "" || item.Bytes < 1 || item.Width < 1 || item.Height < 1 || (item.Placement != "chapter-start" && item.Placement != "chapter-end") || item.Order < 0 {
			return nil, ErrInvalid
		}
		seen[item.ID] = true
		total += int64(item.Bytes)
		if total > MaxImageBytes {
			return nil, ErrTooLarge
		}
		item.File = "images/" + item.ID + ext
	}
	archive, err := os.CreateTemp("", "ainovel-chapter-media-*.zip")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = archive.Close()
			_ = os.Remove(archive.Name())
		}
	}()
	writer := zip.NewWriter(archive)
	defer writer.Close()
	for _, item := range manifest.Images {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := read(ctx, item)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(data)
		if len(data) != item.Bytes || hex.EncodeToString(digest[:]) != item.SHA256 {
			return nil, ErrCorrupt
		}
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: item.File, Method: zip.Store})
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(data); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry, err := writer.CreateHeader(&zip.FileHeader{Name: "chapter.json", Method: zip.Deflate})
	if err != nil {
		return nil, err
	}
	if err := json.NewEncoder(entry).Encode(manifest); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return archive, nil
}

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func imageExtension(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ""
	}
}
