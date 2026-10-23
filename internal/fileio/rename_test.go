package fileio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenamePublishesAndPreservesFailure(t *testing.T) {
	for _, confined := range []bool{false, true} {
		name := "absolute"
		if confined {
			name = "root"
		}
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), strings.Repeat("long-directory-", 7), strings.Repeat("more-directory-", 7), "中文作品")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			rename := func(from, to string) error { return Rename(filepath.Join(dir, from), filepath.Join(dir, to)) }
			if confined {
				rename = func(from, to string) error { return RootRename(root, from, to) }
			}
			writeTestFile(t, dir, "state.json", "old")
			writeTestFile(t, dir, "state.tmp", "new")
			if err := rename("state.tmp", "state.json"); err != nil {
				t.Fatal(err)
			}
			assertTestFile(t, dir, "state.json", "new")
			if _, err := os.Stat(filepath.Join(dir, "state.tmp")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("published source still exists: %v", err)
			}
			if err := rename("missing.tmp", "state.json"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing source error lost: %v", err)
			}
			assertTestFile(t, dir, "state.json", "new")
			writeTestFile(t, dir, "initial.tmp", "initial")
			if err := rename("initial.tmp", "initial.json"); err != nil {
				t.Fatal(err)
			}
			assertTestFile(t, dir, "initial.json", "initial")
		})
	}
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, dir, name, want string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(b) != want {
		t.Fatalf("%s = %q, %v; want %q", name, b, err, want)
	}
}
