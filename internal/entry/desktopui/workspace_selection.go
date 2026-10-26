package desktopui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/hansshaw/AInoval-web-ui/internal/bootstrap"
)

// Legacy novels predate web-project.json. Only durable story artifacts count;
// a lock, an empty directory or a freshly generated format marker does not.
func existingProject(directory string) bool {
	if directory == "" {
		return false
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	for _, name := range []string{"meta/web-project.json", "meta/book.json", "meta/run.json", "meta/progress.json", "meta/web-cocreate.json", "premise.md", "outline.json", "layered_outline.json"} {
		if info, err := os.Lstat(filepath.Join(directory, filepath.FromSlash(name))); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	entries, _ := os.ReadDir(filepath.Join(directory, "chapters"))
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".md") {
			return true
		}
	}
	return false
}

func requiresSelectedProject(pattern string) bool {
	if !strings.Contains(pattern, " /api/") {
		return false
	}
	for _, prefix := range []string{"/api/config", "/api/media-models", "/api/jobs", "/api/reader/", "/api/desktop/"} {
		if strings.HasPrefix(strings.SplitN(pattern, " ", 2)[1], prefix) {
			return false
		}
	}
	switch pattern {
	case "GET /api/state", "GET /api/events", "POST /api/export", "GET /api/workspaces", "POST /api/workspaces", "POST /api/workspaces/copy", "POST /api/workspaces/archive", "POST /api/workspaces/delete", "POST /api/manual/projects",
		"GET /api/style", "POST /api/style/entries", "POST /api/style/upload", "POST /api/style/run", "POST /api/style/delete":
		return false
	}
	return true
}

func (s *Server) clearSelectedProjectLocked() {
	if s.rt != nil {
		s.rt.Close()
	}
	s.rt, s.dir, s.hostError, s.stream, s.events = nil, "", "", "", nil
	s.cfg = bootstrap.CloneConfig(s.baseCfg)
	s.cfg.OutputDir = s.homeDir
	s.broadcastLocked("history", map[string]any{"events": []any{}, "stream": "", "directory": ""})
	s.broadcastLocked("workspace", nil)
}
