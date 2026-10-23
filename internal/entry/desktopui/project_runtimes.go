package desktopui

import (
	"context"
	"errors"

	"github.com/hansshaw/AInoval-web-ui/internal/projectjobs"
	"path/filepath"

	"github.com/hansshaw/AInoval-web-ui/internal/bootstrap"
	"github.com/hansshaw/AInoval-web-ui/internal/host"
)

// A selected workbench is only a view. Engines left running when the user opens
// another project retain their own Host, output buffers, model set and lease.
type projectRuntime struct {
	rt        *host.Host
	cfg       bootstrap.Config
	hostError string
	events    []host.Event
	stream    string
}

func projectKey(directory string) string {
	if resolved, err := filepath.EvalSymlinks(directory); err == nil {
		return resolved
	}
	if absolute, err := filepath.Abs(directory); err == nil {
		return absolute
	}
	return filepath.Clean(directory)
}

func (s *Server) projectRunningLocked(directory string) bool {
	if projectKey(directory) == projectKey(s.dir) {
		return s.busy || s.stopping || (s.rt != nil && (s.rt.Snapshot().IsRunning || s.runSlots[s.rt] != nil))
	}
	view := s.projectRuntimes[projectKey(directory)]
	return view != nil && view.rt != nil && (view.rt.Snapshot().IsRunning || s.runSlots[view.rt] != nil)
}

func (s *Server) reserveProjectRunLocked(rt *host.Host) error {
	if s.runSlots[rt] != nil {
		return nil
	}
	if err := s.ensureBackgroundLocked(); err != nil {
		return err
	}
	release, err := s.background.ReserveProject(s.ctx, projectKey(s.dir))
	if err != nil {
		return err
	}
	if s.runSlots == nil {
		s.runSlots = make(map[*host.Host]func())
	}
	s.runSlots[rt] = release
	return nil
}

func (s *Server) releaseProjectRunLocked(rt *host.Host) {
	if release := s.runSlots[rt]; release != nil {
		delete(s.runSlots, rt)
		release()
	}
}

func (s *Server) releaseIdleProjectRunLocked(rt *host.Host) {
	if rt != nil && !rt.Snapshot().IsRunning && !(s.rt == rt && s.busy) {
		s.releaseProjectRunLocked(rt)
	}
}

// Idle detached Hosts are closed before a background job or destructive edit
// takes their directory. Running ones are never implicitly canceled.
func (s *Server) closeIdleProjectLocked(directory string) bool {
	key := projectKey(directory)
	view := s.projectRuntimes[key]
	if view == nil {
		return true
	}
	if view.rt != nil {
		if view.rt.Snapshot().IsRunning || s.runSlots[view.rt] != nil {
			return false
		}
		view.rt.Close()
	}
	delete(s.projectRuntimes, key)
	return true
}

// The queue is lazy because the desktop and tests establish booksDir after New.
// Constructing a Host only captures this callback; first execution initializes
// quotas in the correct workspace without holding the server lock while waiting.
func (s *Server) acquireCreationModel(ctx context.Context, provider string) (func(), error) {
	s.mu.Lock()
	err := s.ensureBackgroundLocked()
	queue := s.background
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return queue.AcquireModel(ctx, provider)
}

func (s *Server) projectRunErrorLocked(err error) string {
	switch {
	case errors.Is(err, projectjobs.ErrProjectLimit):
		return "同时创作项目数已达上限，请暂停一个项目，或在后台任务中调整项目并发数"
	case errors.Is(err, projectjobs.ErrProjectBusy):
		return "该项目已有排队或运行中的任务，请先查看项目进度"
	default:
		return "无法启动项目：" + s.redactLocked(err.Error())
	}
}
