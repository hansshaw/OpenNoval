package mediaworkspace

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func imageFixture(data []byte) Image {
	digest := sha256.Sum256(data)
	return Image{ID: "0123456789abcdef0123456789abcdef", Name: "晚霞.png", MIME: "image/png", Bytes: len(data), Width: 800, Height: 600, Caption: "邮局外的晚霞", Placement: "chapter-end", Order: 7, CreatedAt: "2026-10-23T01:00:00Z", SHA256: hex.EncodeToString(digest[:])}
}

func TestPortableArchivePreservesChapterAndImageMetadata(t *testing.T) {
	body := "手动编辑后的正文。\n\n下一段。"
	chapter := Chapter{Number: 3, Title: "邮局", VolumeTitle: "初遇", Body: &body, Plan: &Plan{CoreEvent: "送信", Hook: "信来自何人", Scenes: []string{"街道", "邮局"}, Fingerprint: "saved-planning-revision"}}
	data := []byte("validated source image bytes")
	image := imageFixture(data)
	image.File = "ignored/absolute-or-provider-path"
	f, err := CreateArchive(t.Context(), chapter, []Image{image}, func(_ context.Context, _ Image) ([]byte, error) { return data, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	info, err := f.Stat()
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("archive is not owner-only: %v, %v", info, err)
	}
	reader, err := zip.NewReader(f, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string][]byte)
	for _, entry := range reader.File {
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		contents, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name] = contents
	}
	var manifest Manifest
	if err := json.Unmarshal(files["chapter.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || manifest.Format != Format || manifest.SchemaVersion != 1 || manifest.ExportedAt == "" || manifest.Chapter.Body == nil || *manifest.Chapter.Body != body || manifest.Chapter.Title != chapter.Title || manifest.Chapter.VolumeTitle != chapter.VolumeTitle || manifest.Chapter.Plan == nil || manifest.Chapter.Plan.Fingerprint != chapter.Plan.Fingerprint || len(manifest.Chapter.Plan.Scenes) != 2 {
		t.Fatalf("chapter snapshot changed: %+v", manifest)
	}
	digest := sha256.Sum256([]byte(body))
	if manifest.Chapter.ContentSHA256 != hex.EncodeToString(digest[:]) || len(manifest.Images) != 1 {
		t.Fatalf("body digest or images missing: %+v", manifest)
	}
	image.File = "images/" + image.ID + ".png"
	if manifest.Images[0] != image || !bytes.Equal(files[image.File], data) {
		t.Fatalf("image cannot be reconstructed from manifest: %+v", manifest.Images)
	}
}

func TestPortableArchiveRejectsOversizedAndUnsafeSourcesBeforeReading(t *testing.T) {
	chapter := Chapter{Number: 1, Plan: &Plan{}}
	image := imageFixture([]byte("image"))
	for _, tc := range []struct {
		name   string
		change func(*Image)
		want   error
	}{
		{"size", func(i *Image) { i.Bytes = MaxImageBytes + 1 }, ErrTooLarge},
		{"traversal", func(i *Image) { i.ID = strings.Repeat("../", 10) + ".." }, ErrInvalid},
		{"mime", func(i *Image) { i.MIME = "text/html" }, ErrInvalid},
		{"checksum", func(i *Image) { i.SHA256 = "missing" }, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := image
			tc.change(&item)
			f, err := CreateArchive(t.Context(), chapter, []Image{item}, func(context.Context, Image) ([]byte, error) {
				t.Fatal("rejected metadata must not read image bytes")
				return nil, nil
			})
			if f != nil || !errors.Is(err, tc.want) {
				t.Fatalf("invalid image accepted: %v, %v", f, err)
			}
		})
	}
	image.Bytes = MaxImageBytes / 2
	second, third := image, image
	second.ID, third.ID = strings.Repeat("b", 32), strings.Repeat("c", 32)
	if f, err := CreateArchive(t.Context(), chapter, []Image{image, second, third}, func(context.Context, Image) ([]byte, error) {
		t.Fatal("aggregate limit must precede reads")
		return nil, nil
	}); f != nil || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("aggregate budget was not enforced: %v, %v", f, err)
	}
}

func TestPortableArchiveFailuresDoNotLeavePartialFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, dir)
	}
	data := []byte("unchanged image")
	item := imageFixture(data)
	chapter := Chapter{Number: 1, Plan: &Plan{}}
	ctx, cancel := context.WithCancel(t.Context())
	for _, tc := range []struct {
		name string
		read ReadImage
		want error
	}{
		{"checksum", func(context.Context, Image) ([]byte, error) { return []byte("different image"), nil }, ErrCorrupt},
		{"truncated", func(context.Context, Image) ([]byte, error) { return data[:2], nil }, ErrCorrupt},
		{"cancel", func(context.Context, Image) ([]byte, error) { cancel(); return data, nil }, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := CreateArchive(ctx, chapter, []Image{item}, tc.read)
			if f != nil || !errors.Is(err, tc.want) {
				t.Fatalf("failed export published a file: %v, %v", f, err)
			}
			files, err := filepath.Glob(filepath.Join(dir, "ainovel-chapter-media-*.zip"))
			if err != nil || len(files) != 0 {
				t.Fatalf("partial export left behind: %v, %v", files, err)
			}
		})
	}
}
