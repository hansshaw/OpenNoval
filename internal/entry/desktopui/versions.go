package desktopui

import (
	"errors"
	"net/http"
	"strings"

	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/projectversion"
)

func (s *Server) versionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, projectversion.ErrRecoveryPending):
		fail(w, http.StatusConflict, "项目有未完成的恢复，请重新打开项目后重试")
	case errors.Is(err, host.ErrProjectSnapshotBusy), errors.Is(err, projectversion.ErrChanged):
		fail(w, http.StatusConflict, "项目仍在运行或预览后已变化，请等待停止并重新预览")
	case errors.Is(err, projectversion.ErrNotFound):
		fail(w, http.StatusNotFound, "版本不存在")
	case errors.Is(err, projectversion.ErrUnsafeFile), errors.Is(err, projectversion.ErrInvalidID), errors.Is(err, projectversion.ErrCorrupt):
		fail(w, http.StatusBadRequest, "版本文件不安全或校验失败，未执行恢复")
	default:
		fail(w, http.StatusInternalServerError, "版本操作失败："+s.redactLocked(err.Error()))
	}
}

func (s *Server) versionDirectoryLocked(w http.ResponseWriter, directory string) bool {
	if s.closed || s.stopping || s.busy || (s.styleBusy && projectKey(s.styleDirectory) == projectKey(directory)) || s.backgroundProjectBusyLocked(directory) {
		fail(w, http.StatusConflict, "请等待当前操作结束")
		return false
	}
	if directory == "" || directory != s.dir {
		fail(w, http.StatusConflict, "项目已切换，请刷新后重试")
		return false
	}
	return true
}

func (s *Server) withVersionSnapshotLocked(fn func() error) error {
	if s.rt != nil {
		return s.rt.WithProjectSnapshot(fn)
	}
	// A disconnected project may still belong to another process. Use the same
	// directory lease as the engine before reading its versioned content.
	return withWorkspaceLease(s.dir, fn)
}

func (s *Server) listVersions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.versionDirectoryLocked(w, r.URL.Query().Get("directory")) {
		return
	}
	versions, err := projectversion.List(s.dir)
	if err != nil {
		s.versionError(w, err)
		return
	}
	reply(w, http.StatusOK, map[string]any{"directory": s.dir, "versions": versions})
}

func (s *Server) createVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory string `json:"directory"`
		Name      string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !validProjectName(strings.TrimSpace(req.Name)) {
		fail(w, 400, "请输入 1–80 字的版本名称")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.versionDirectoryLocked(w, req.Directory) {
		return
	}
	var version projectversion.Manifest
	err := s.withVersionSnapshotLocked(func() error {
		var err error
		version, err = projectversion.Create(r.Context(), s.dir, req.Name)
		return err
	})
	if err != nil {
		s.versionError(w, err)
		return
	}
	reply(w, http.StatusCreated, version)
}

func (s *Server) compareVersion(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.versionDirectoryLocked(w, r.URL.Query().Get("directory")) {
		return
	}
	var comparison projectversion.Comparison
	err := s.withVersionSnapshotLocked(func() error {
		var err error
		comparison, err = projectversion.Compare(r.Context(), s.dir, r.PathValue("version"))
		return err
	})
	if err != nil {
		s.versionError(w, err)
		return
	}
	reply(w, http.StatusOK, comparison)
}

func (s *Server) restoreVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory   string `json:"directory"`
		ID          string `json:"id"`
		Fingerprint string `json:"fingerprint"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Fingerprint == "" {
		fail(w, 400, "请先预览恢复差异")
		return
	}
	s.mu.Lock()
	if !s.versionDirectoryLocked(w, req.Directory) {
		s.mu.Unlock()
		return
	}
	if err := s.withVersionSnapshotLocked(func() error { return nil }); err != nil {
		s.versionError(w, err)
		s.mu.Unlock()
		return
	}
	// Close cached stores and auto-save before touching story files. Busy keeps
	// all project mutation and switching endpoints out until the Host is rebuilt.
	rt := s.rt
	s.rt = nil
	s.busy, s.job = true, "restore-version"
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	if rt != nil {
		rt.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy, s.job = false, ""
	if s.closed {
		fail(w, 409, "工作台已关闭")
		return
	}
	var result projectversion.RestoreResult
	err := withWorkspaceLease(s.dir, func() error {
		var err error
		result, err = projectversion.Restore(r.Context(), s.dir, req.ID, req.Fingerprint)
		return err
	})
	s.events, s.stream = nil, ""
	s.initLocked()
	s.broadcastLocked("workspace", nil)
	if err != nil {
		s.versionError(w, err)
		return
	}
	reply(w, http.StatusOK, map[string]any{"ok": true, "result": result, "error": s.hostError})
}
