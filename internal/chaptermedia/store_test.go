package chaptermedia

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func picture(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 3))
	img.Set(1, 1, color.NRGBA{R: 90, G: 180, B: 80, A: 255})
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestAttachmentsRoundtripAndCorruptionDetection(t *testing.T) {
	dir := t.TempDir()
	data := picture(t)
	upload, err := Prepare(data, "../../手稿.png")
	if err != nil {
		t.Fatal(err)
	}
	item, err := Add(dir, 2, upload, "雨夜旧城", "chapter-start")
	if err != nil {
		t.Fatal(err)
	}
	if item.Name != "手稿.png" || item.Width != 4 || item.Height != 3 || item.Chapter != 2 || !validID(item.ID) {
		t.Fatalf("metadata: %+v", item)
	}
	got, content, err := Read(dir, 2, item.ID)
	if err != nil || got.ID != item.ID || !bytes.Equal(content, data) {
		t.Fatalf("read: %+v %v", got, err)
	}
	empty, err := List(dir, 1)
	if err != nil || len(empty) != 0 {
		t.Fatalf("cross chapter: %+v %v", empty, err)
	}
	caption, placement, order := "全新的图注", "chapter-end", 3
	if err := Edit(dir, 2, item.ID, Update{Caption: &caption, Placement: &placement, Order: &order}); err != nil {
		t.Fatal(err)
	}
	listed, err := List(dir, 2)
	if err != nil || len(listed) != 1 || listed[0].Caption != caption || listed[0].Order != order || listed[0].Placement != placement {
		t.Fatalf("updated: %+v %v", listed, err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(itemPath(item))), []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Read(dir, 2, item.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected digest mismatch: %v", err)
	}
	if err := Remove(dir, 2, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Read(dir, 2, item.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted still visible: %v", err)
	}
}

func TestPrepareRejectsActiveContentTruncationAndOversize(t *testing.T) {
	for _, data := range [][]byte{[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), []byte("<html>bad</html>"), picture(t)[:32], make([]byte, MaxImageBytes+1)} {
		if _, err := Prepare(data, "valid.png"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid image: %v", err)
		}
	}
}

func TestAttachmentsRejectLinksAndForgedPaths(t *testing.T) {
	upload, err := Prepare(picture(t), "test.png")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("linked ancestor even with missing chapter", func(t *testing.T) {
		dir, outside := t.TempDir(), t.TempDir()
		if err := os.Symlink(outside, filepath.Join(dir, "media")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := List(dir, 1); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("link accepted on list: %v", err)
		}
		if _, err := Add(dir, 1, upload, "", "chapter-end"); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("link accepted on add: %v", err)
		}
		entries, _ := os.ReadDir(outside)
		if len(entries) != 0 {
			t.Fatal("outside tree was changed")
		}
	})
	t.Run("linked content", func(t *testing.T) {
		dir := t.TempDir()
		item, err := Add(dir, 1, upload, "", "chapter-end")
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, filepath.FromSlash(itemPath(item)))
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(t.TempDir(), "secret"), file); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, _, err := Read(dir, 1, item.ID); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("link served: %v", err)
		}
		if err := Remove(dir, 1, item.ID); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("unsafe remove: %v", err)
		}
	})
	for _, id := range []string{"../secret", strings.Repeat("A", 32), "", strings.Repeat("a", 64)} {
		if _, _, err := Read(t.TempDir(), 1, id); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid id accepted: %q %v", id, err)
		}
	}
}

func TestManifestCapacityFailurePreservesReadableImagesAndRollsBackUpload(t *testing.T) {
	dir := t.TempDir()
	upload, err := Prepare(picture(t), strings.Repeat("<", 120))
	if err != nil {
		t.Fatal(err)
	}
	caption := strings.Repeat("<", 1000)
	manifestPath := filepath.Join(dir, "media", "chapters", "000001", "manifest.json")
	var previous []byte
	successful := 0
	rejected := false
	for i := 0; i < MaxItems; i++ {
		_, err := Add(dir, 1, upload, caption, "chapter-end")
		if err == nil {
			successful++
			previous, err = os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(previous) > maxManifestBytes {
				t.Fatal("published manifest exceeds reader limit")
			}
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("capacity must be a recoverable input error: %v", err)
		}
		rejected = true
		current, readErr := os.ReadFile(manifestPath)
		if readErr != nil || !bytes.Equal(current, previous) {
			t.Fatalf("capacity failure changed previous manifest: %v", readErr)
		}
		items, listErr := List(dir, 1)
		if listErr != nil || len(items) != successful {
			t.Fatalf("capacity failure made saved images unreadable: count=%d err=%v", len(items), listErr)
		}
		files, readErr := os.ReadDir(filepath.Dir(manifestPath))
		if readErr != nil || len(files) != successful+1 {
			t.Fatalf("failed Add leaked unpublished image/temp: files=%d expected=%d err=%v", len(files), successful+1, readErr)
		}
		if successful == 0 {
			t.Fatal("fixture did not save any images before reaching capacity")
		}
		if _, _, err := Read(dir, 1, items[0].ID); err != nil {
			t.Fatalf("original image was lost: %v", err)
		}
		break
	}
	if !rejected {
		t.Fatal("escaped names/captions did not exercise the manifest byte limit")
	}
}
