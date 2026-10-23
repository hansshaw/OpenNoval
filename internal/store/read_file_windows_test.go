package store

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsSnapshotReaderAllowsAtomicReplacement(t *testing.T) {
	directory := t.TempDir()
	storeIO := newIO(directory)
	const relative = "meta/run.json"
	old := []byte(`{"target_chapters":1}`)
	newer := []byte(`{"target_chapters":2}`)
	if err := storeIO.WriteFileUnlocked(relative, old); err != nil {
		t.Fatal(err)
	}
	// This is the previous implementation's exact Windows failure condition:
	// /api/state holds an ordinary reader while startup replaces run.json.
	ordinary, err := os.Open(filepath.Join(directory, relative))
	if err != nil {
		t.Fatal(err)
	}
	err = storeIO.WriteFileUnlocked(relative, newer)
	ordinary.Close()
	if err == nil {
		t.Fatal("ordinary Windows reader unexpectedly allowed replacement; revisit reproduction")
	}
	reader, err := openFileSnapshot(filepath.Join(directory, relative))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := storeIO.WriteFileUnlocked(relative, newer); err != nil {
		t.Fatalf("read-only snapshot blocked atomic publication: %v", err)
	}
	held, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(held, old) {
		t.Fatalf("held reader lost its consistent old snapshot: %s %v", held, err)
	}
	actual, err := newIO(directory).ReadFile(relative)
	if err != nil || !bytes.Equal(actual, newer) {
		t.Fatalf("independent workbench reader missed new snapshot: %s %v", actual, err)
	}
}
func TestWindowsSnapshotReaderSupportsLongUnicodePathsAndReadErrors(t *testing.T) {
	directory := filepath.Join(t.TempDir(), strings.Repeat("long-directory-", 7), strings.Repeat("more-directory-", 7), "中文作品")
	storeIO := newIO(directory)
	const relative = "meta/run.json"
	if err := storeIO.WriteFileUnlocked(relative, []byte("内容")); err != nil {
		t.Fatal(err)
	}
	data, err := storeIO.ReadFile(relative)
	if err != nil || string(data) != "内容" {
		t.Fatalf("long unicode read: %q %v", data, err)
	}
	if _, err := storeIO.ReadFile("missing.json"); !os.IsNotExist(err) {
		t.Fatalf("missing file semantics changed: %v", err)
	}
}
