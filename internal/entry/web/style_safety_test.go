package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStyleMutationsRejectStaleProject(t *testing.T) {
	s := newTestServer(t, false)
	root := styleRoot(s.dir)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "sample.md")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"upload", "delete", "run"} {
		body := map[string]string{"directory": filepath.Join(t.TempDir(), "old-project")}
		if endpoint != "run" {
			body["name"] = "sample.md"
		}
		if endpoint == "upload" {
			body["content"] = "replacement"
		}
		data, _ := json.Marshal(body)
		w := request(s, "POST", "/api/style/"+endpoint, string(data))
		if w.Code != 409 {
			t.Fatalf("%s: %d %s", endpoint, w.Code, w.Body.String())
		}
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "original" {
		t.Fatalf("current project's corpus changed: %q %v", data, err)
	}
}

func TestWriteStyleSourceReplacesFileAndRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "sample.md")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeStyleSource(target, []byte("new")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "new" {
		t.Fatalf("replacement: %q %v", data, err)
	}
	dir := filepath.Join(root, "directory.md")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeStyleSource(dir, []byte("new")); err == nil {
		t.Fatal("directory replaced by corpus file")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("directory was changed: %v", err)
	}
}
