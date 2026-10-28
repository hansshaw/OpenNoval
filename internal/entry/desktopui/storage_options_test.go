package desktopui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hansshaw/AInoval-web-ui/internal/bootstrap"
)

func TestEmbeddedStorageIgnoresDesktopDirectories(t *testing.T) {
	desktopDir, appDir, cacheDir := t.TempDir(), t.TempDir(), t.TempDir()
	t.Chdir(desktopDir)
	legacy := filepath.Join(desktopDir, "output", "novel")
	if err := saveProject(legacy, projectInfo{Name: "desktop-only"}); err != nil {
		t.Fatal(err)
	}
	// An unrelated project's invalid local config must not prevent the embedded
	// runtime from initializing, and must never become its save target.
	if err := os.MkdirAll(".ainovel", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(".ainovel", "config.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(bootstrap.Config{}, Options{DataDir: appDir, TempDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.homeDir != filepath.Join(appDir, "books") || s.booksDir != s.homeDir || s.configPath != filepath.Join(appDir, "config.json") || s.tempDir != cacheDir {
		t.Fatalf("embedded paths escaped app storage: home=%s books=%s config=%s temp=%s", s.homeDir, s.booksDir, s.configPath, s.tempDir)
	}
	if projects := s.projectDirectoriesLocked(); len(projects) != 0 {
		t.Fatalf("desktop projects leaked into mobile catalogue: %v", projects)
	}
	if _, err := s.validWorkspaceLocked(legacy); err == nil {
		t.Fatal("mobile accepted a desktop legacy workspace")
	}
	projectDir := filepath.Join(appDir, "books", "novel")
	assets := s.assetLoadOptions(projectDir)
	if assets.HomeStyleDir != filepath.Join(appDir, "style") || assets.BookStyleDir != filepath.Join(projectDir, "style") {
		t.Fatalf("style overrides escaped app storage: %+v", assets)
	}
}
