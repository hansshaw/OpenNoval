package chaptermedia

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func generationFixture(data []byte) Generation {
	h := sha256.Sum256(data)
	return Generation{ID: strings.Repeat("a", 32), Chapter: 1, Kind: "audio", Source: "final", SourceSHA256: strings.Repeat("b", 64), Model: "mock", Status: "completed", Progress: 100, CreatedAt: "2026-10-23T00:00:00Z", MIME: "audio/mpeg", Bytes: len(data), SHA256: hex.EncodeToString(h[:])}
}
func TestGeneratedMediaPersistsAndDetectsCorruptOrCrossChapterContent(t *testing.T) {
	dir := t.TempDir()
	data := []byte("audio fixture bytes")
	g := generationFixture(data)
	if err := SaveGeneration(dir, g, data); err != nil {
		t.Fatal(err)
	}
	items, err := ListGenerations(dir, 1)
	if err != nil || len(items) != 1 || items[0] != g {
		t.Fatalf("generation metadata: %v %+v", err, items)
	}
	if _, read, err := ReadGeneration(dir, 1, g.ID); err != nil || !bytes.Equal(read, data) {
		t.Fatalf("durable read: %v", err)
	}
	if _, _, err := ReadGeneration(dir, 2, g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross chapter: %v", err)
	}
	if _, _, err := ReadGeneration(dir, 1, "../../escape"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe ID: %v", err)
	}
	file := filepath.Join(dir, "media", "chapters", "000001", "generated", g.ID+".mp3")
	if err := os.WriteFile(file, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadGeneration(dir, 1, g.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered artifact: %v", err)
	}
	if err := RemoveGeneration(dir, 1, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("artifact left after removal")
	}
}
func TestGeneratedMediaRejectsLinkedStorageAndInvalidMetadata(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "media")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	g := generationFixture([]byte("audio"))
	if err := SaveGeneration(dir, g, []byte("audio")); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("linked output accepted: %v", err)
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("wrote outside project")
	}
	g.ID = "../outside"
	if err := SaveGeneration(t.TempDir(), g, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid ID accepted")
	}
}

func TestGeneratedArtifactIsRemovedIfMetadataCannotPublish(t *testing.T) {
	dir := t.TempDir()
	data := []byte("audio fixture")
	g := generationFixture(data)
	base := filepath.Join(dir, "media", "chapters", "000001", "generated")
	if err := os.MkdirAll(filepath.Join(base, g.ID+".json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := SaveGeneration(dir, g, data); err == nil {
		t.Fatal("metadata directory replaced")
	}
	if _, err := os.Stat(filepath.Join(base, g.ID+".mp3")); !os.IsNotExist(err) {
		t.Fatal("unpublished output was left behind")
	}
}
