package desktopui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hansshaw/OpenNovel/internal/bootstrap"
)

const selectionWaitTimeout = 1500 * time.Millisecond

// Short chapter/version reads may still be writing their response when the
// user switches projects. Let them finish without queuing an RWMutex writer:
// writer preference would otherwise block a new pause request behind a long
// co-create request. The caller must unlock selectionMu after a successful wait.
func (s *Server) lockSelection(ctx context.Context, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	retry := time.NewTicker(15 * time.Millisecond)
	defer retry.Stop()
	for {
		if ctx.Err() != nil {
			return false
		}
		if s.selectionMu.TryLock() {
			return true
		}
		// A response can still own both locks. Do not block on mu or the
		// request's cancellation/deadline would stop bounding this wait.
		if s.mu.TryLock() {
			blocked := s.busy || s.stopping || s.closed
			s.mu.Unlock()
			if blocked {
				return false
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-retry.C:
		}
	}
}

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
