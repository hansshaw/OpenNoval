package desktopui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/hansshaw/AInoval-web-ui/internal/host"
	"github.com/hansshaw/AInoval-web-ui/internal/store"
)

var errCopyUnsafeFile = errors.New("项目含链接或非普通文件，无法安全复制")

// copyWorkspace creates a new, independent project without switching the active
// workspace. The server mutex excludes all workbench edits for the snapshot;
// other projects additionally need the same OS lease used by Host.
func (s *Server) copyWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory string `json:"directory"`
		Name      string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name != "" && !validProjectName(req.Name) {
		fail(w, http.StatusBadRequest, "请输入 1–80 字的副本名称")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.stopping || s.busy || s.styleBusy || (s.rt != nil && s.rt.Snapshot().IsRunning) {
		fail(w, http.StatusConflict, "请先暂停并等待当前操作结束后再复制项目")
		return
	}
	if strings.TrimSpace(req.Directory) == "" {
		fail(w, http.StatusBadRequest, "请选择要复制的项目")
		return
	}
	source, err := s.validWorkspaceLocked(req.Directory)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if !existingProject(source) {
		fail(w, http.StatusNotFound, "项目不存在，请刷新项目列表")
		return
	}
	// A legacy project lives directly in booksDir. Only reject ordinary
	// projects that contain the project root and would recursively capture
	// every sibling project.
	if source != s.booksDir {
		if rel, err := filepath.Rel(source, s.booksDir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			fail(w, http.StatusBadRequest, "源项目不能包含工作台项目目录，请使用独立项目目录")
			return
		}
	}
	if s.backgroundProjectBusyLocked(source) {
		fail(w, 409, "此项目已有后台任务，请先停止后再复制")
		return
	}
	if err := host.CheckProjectDeleting(source); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		fail(w, http.StatusBadRequest, "源项目目录无法读取")
		return
	}
	defer root.Close()
	activeHost := source == s.dir && s.rt != nil
	// Never pass a symlink to flock, including one hidden in an excluded path.
	// The active Host keeps its lease open. On Windows, os.Root cannot always
	// inspect that open lock, so its consistency comes from WithProjectSnapshot
	// below instead of opening or reading the live lock file.
	if !activeHost {
		if info, err := root.Lstat(".ainovel.lock"); err == nil {
			if !info.Mode().IsRegular() {
				fail(w, http.StatusBadRequest, errCopyUnsafeFile.Error())
				return
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			fail(w, http.StatusBadRequest, "源项目锁无法读取")
			return
		}
		lease := flock.New(filepath.Join(source, ".ainovel.lock"), flock.SetPermissions(0600))
		defer lease.Close()
		locked, err := lease.TryLock()
		if err != nil {
			fail(w, http.StatusConflict, "无法取得源项目锁，请关闭其他工作台后重试")
			return
		}
		if !locked {
			fail(w, http.StatusConflict, "源项目正被其他实例使用，请先停止并关闭该项目")
			return
		}
		// Detect a concurrently replaced lock or a renamed source directory.
		lockedInfo, statErr := lease.Stat()
		rootInfo, rootErr := root.Lstat(".ainovel.lock")
		if statErr != nil || rootErr != nil || !rootInfo.Mode().IsRegular() || !os.SameFile(lockedInfo, rootInfo) {
			fail(w, http.StatusConflict, "源项目目录已变化，请刷新后重试")
			return
		}
	}
	if err := host.CheckProjectDeleting(source); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	var project projectInfo
	projectJSON, err := readProjectCopyFile(root, source, "meta/web-project.json")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		fail(w, http.StatusInternalServerError, "项目资料读取失败")
		return
	}
	if err == nil && json.Unmarshal(projectJSON, &project) != nil {
		fail(w, http.StatusInternalServerError, "项目资料读取失败")
		return
	}
	if project.StyleReferences == nil {
		if library, openErr := s.sharedStyleLibraryLocked(); openErr == nil {
			if ids, refErr := selectedStyleIDs(library, source, project); refErr == nil {
				project.StyleReferences = ids
			}
		}
	}
	if req.Name == "" {
		name := project.Name
		if strings.TrimSpace(name) == "" {
			name = filepath.Base(source)
		}
		runes := []rune(name)
		if len(runes) > 75 {
			runes = runes[:75]
		}
		req.Name = string(runes) + "（副本）"
	}
	project.CopiedFrom = project.ID
	if project.CopiedFrom == "" {
		project.CopiedFrom = source
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		fail(w, http.StatusInternalServerError, "无法生成副本标识")
		return
	}
	project.ID = hex.EncodeToString(id[:])
	project.Name = req.Name
	project.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	project.UpdatedAt = project.CreatedAt
	project.Archived = false

	if err := os.MkdirAll(s.booksDir, 0700); err != nil {
		fail(w, http.StatusInternalServerError, "无法创建副本目录")
		return
	}
	booksInfo, err := os.Lstat(s.booksDir)
	if err != nil || !booksInfo.IsDir() || booksInfo.Mode()&os.ModeSymlink != 0 {
		fail(w, http.StatusBadRequest, "工作台项目目录不存在或是链接")
		return
	}
	// Build beside booksDir and publish with a single rename. Failed and partial
	// copies never appear in the project list, even to another server.
	staging, err := os.MkdirTemp(filepath.Dir(s.booksDir), ".ainovel-copy-*")
	if err != nil {
		fail(w, http.StatusInternalServerError, "无法创建副本目录")
		return
	}
	defer os.RemoveAll(staging)
	copySnapshot := func() error {
		return copyProjectFiles(r.Context(), root, staging, source == s.booksDir, activeHost)
	}
	if activeHost {
		err = s.rt.WithProjectSnapshot(copySnapshot)
	} else {
		err = copySnapshot()
	}
	if err != nil {
		if errors.Is(err, host.ErrProjectSnapshotBusy) {
			fail(w, http.StatusConflict, err.Error())
		} else if errors.Is(err, errCopyUnsafeFile) {
			fail(w, http.StatusBadRequest, errCopyUnsafeFile.Error())
		} else {
			slog.Error("项目复制快照失败", "module", "web", "source", s.redactLocked(source), "error", s.redactLocked(err.Error()))
			fail(w, http.StatusInternalServerError, "项目复制失败，请检查目录权限和磁盘空间")
		}
		return
	}
	if err := resetCopiedRun(staging, project.CreatedAt); err != nil {
		fail(w, http.StatusInternalServerError, "副本运行状态初始化失败")
		return
	}
	if err := saveProject(staging, project); err != nil {
		fail(w, http.StatusInternalServerError, "副本资料保存失败")
		return
	}
	target := filepath.Join(s.booksDir, "copy-"+project.ID)
	if err := os.Rename(staging, target); err != nil {
		fail(w, http.StatusInternalServerError, "副本保存失败")
		return
	}
	s.broadcastLocked("workspace", nil)
	reply(w, http.StatusCreated, map[string]any{"ok": true, "directory": target, "project": project})
}

func copyProjectFiles(ctx context.Context, source *os.Root, destination string, excludeChildProjects ...bool) error {
	copier := projectFileCopier{
		ctx:                  ctx,
		source:               source,
		sourceDirectory:      source.Name(),
		destination:          destination,
		excludeChildProjects: len(excludeChildProjects) > 0 && excludeChildProjects[0],
		skipActiveHostLock:   len(excludeChildProjects) > 1 && excludeChildProjects[1],
	}
	if runtime.GOOS == "windows" && copier.skipActiveHostLock {
		return copier.walkWindowsProject()
	}
	return fs.WalkDir(source.FS(), ".", copier.walk)
}

type projectFileCopier struct {
	ctx                  context.Context
	source               *os.Root
	sourceDirectory      string
	destination          string
	excludeChildProjects bool
	skipActiveHostLock   bool
}

// walkWindowsProject avoids os.Root for the active project. Go 1.25 can return
// ERROR_CANT_ACCESS_FILE from os.Root when it walks ordinary descendants on
// Windows. The absolute-path walker still proves containment, rejects links,
// and verifies that each opened file is the file inspected before copying.
func (c projectFileCopier) walkWindowsProject() error {
	source, err := filepath.Abs(c.sourceDirectory)
	if err != nil {
		return err
	}
	source = filepath.Clean(source)
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := c.ctx.Err(); err != nil {
			return err
		}
		if path == source {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || !projectCopyPathContained(rel) {
			return fmt.Errorf("%w: %s", errCopyUnsafeFile, path)
		}
		return c.walkWindowsPath(path, filepath.ToSlash(rel), entry)
	})
}

func (c projectFileCopier) walkWindowsPath(path, rel string, entry fs.DirEntry) error {
	if c.skipActiveHostLock && rel == ".ainovel.lock" {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && temporaryProjectCopyPath(rel) && entry.Type().IsRegular() {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return fmt.Errorf("%w: %s", errCopyUnsafeFile, rel)
	}
	if c.excludeChildProjects && info.IsDir() && !strings.Contains(rel, "/") && projectDirectory(path) {
		return fs.SkipDir
	}
	if skipProjectCopyPath(rel) {
		if info.IsDir() {
			return fs.SkipDir
		}
		return nil
	}
	target := filepath.Join(c.destination, filepath.FromSlash(rel))
	if info.IsDir() {
		return os.Mkdir(target, 0700)
	}
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	openedInfo, err := in.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return fmt.Errorf("%w: %s", errCopyUnsafeFile, rel)
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, copyContextReader{ctx: c.ctx, reader: in})
	closeErr := out.Close()
	return errors.Join(copyErr, closeErr)
}

func projectCopyPathContained(rel string) bool {
	clean := filepath.Clean(rel)
	return !filepath.IsAbs(clean) &&
		clean != "." &&
		clean != ".." &&
		!strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func (c projectFileCopier) walk(rel string, entry fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	if c.skipActiveHostLock && rel == ".ainovel.lock" {
		return nil
	}
	info, err := lstatProjectCopyPath(c.source, c.sourceDirectory, rel)
	if err != nil {
		// Atomic writers may rename a listed temporary file before Lstat.
		// Only disposable, ordinary temp files may disappear; lock/control
		// files, directories and links must retain the normal safety checks.
		if errors.Is(err, fs.ErrNotExist) && temporaryProjectCopyPath(rel) && entry.Type().IsRegular() {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return fmt.Errorf("%w: %s", errCopyUnsafeFile, rel)
	}
	if c.excludeChildProjects && info.IsDir() && !strings.ContainsAny(rel, `/\`) {
		if projectDirectory(filepath.Join(c.source.Name(), filepath.FromSlash(rel))) {
			return fs.SkipDir
		}
	}
	if skipProjectCopyPath(rel) {
		if info.IsDir() {
			return fs.SkipDir
		}
		return nil
	}
	target := filepath.Join(c.destination, filepath.FromSlash(rel))
	if info.IsDir() {
		return os.Mkdir(target, 0700)
	}
	in, err := openProjectCopyFile(c.source, c.sourceDirectory, rel)
	if err != nil {
		return err
	}
	defer in.Close()
	openedInfo, err := in.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return fmt.Errorf("%w: %s", errCopyUnsafeFile, rel)
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, copyContextReader{ctx: c.ctx, reader: in})
	closeErr := out.Close()
	return errors.Join(copyErr, closeErr)
}

func readProjectCopyFile(root *os.Root, sourceDirectory, rel string) ([]byte, error) {
	f, err := openProjectCopyFile(root, sourceDirectory, rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// os.Root on Windows can reject opening ordinary descendants of a directory
// that has itself been marked as a reparse point by Windows. The active
// workbench has already been validated and held open by Host, so use the
// canonical path after proving it stays below the opened source directory.
func resolveWindowsRootPath(root *os.Root, sourceDirectory, rel string) (string, error) {
	if runtime.GOOS != "windows" {
		return "", fs.ErrInvalid
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fs.ErrPermission
	}
	target := filepath.Join(sourceDirectory, clean)
	relative, err := filepath.Rel(sourceDirectory, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fs.ErrPermission
	}
	if _, err := root.Lstat(rel); err == nil {
		return target, nil
	}
	if info, err := os.Lstat(target); err == nil && info.IsDir() {
		return target, nil
	}
	return target, nil
}

func lstatProjectCopyPath(root *os.Root, sourceDirectory, rel string) (fs.FileInfo, error) {
	info, err := root.Lstat(rel)
	if err == nil || runtime.GOOS != "windows" {
		return info, err
	}
	// Go 1.25 os.Root may report ERROR_CANT_ACCESS_FILE for Windows
	// directories propagated with a reparse attribute. Verify the ordinary
	// filesystem path stays within the canonical source before retrying.
	target, pathErr := resolveWindowsRootPath(root, sourceDirectory, rel)
	if pathErr != nil {
		return nil, pathErr
	}
	return os.Lstat(target)
}

func openProjectCopyFile(root *os.Root, sourceDirectory, rel string) (*os.File, error) {
	f, err := root.Open(rel)
	if err == nil || runtime.GOOS != "windows" {
		return f, err
	}
	target, pathErr := resolveWindowsRootPath(root, sourceDirectory, rel)
	if pathErr != nil {
		return nil, pathErr
	}
	return os.Open(target)
}

func skipProjectCopyPath(rel string) bool {
	if rel == ".ainovel.lock" || rel == host.ProjectDeletingFile || rel == "logs" || rel == "meta/runtime" || rel == "meta/versions" {
		return true
	}
	return temporaryProjectCopyPath(rel)
}

func temporaryProjectCopyPath(rel string) bool {
	name := filepath.Base(rel)
	return strings.HasSuffix(name, ".tmp") || strings.Contains(name, ".tmp-")
}

type copyContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r copyContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Retain durable story state and review decisions, while requiring a fresh
// command before the copied project runs another batch or replays an intervention.
func resetCopiedRun(dir, createdAt string) error {
	st := store.NewStore(dir)
	meta, err := st.RunMeta.Load()
	if err != nil || meta == nil {
		return err
	}
	progress, err := st.Progress.Load()
	if err != nil {
		return err
	}
	meta.StartedAt = createdAt
	meta.PendingSteer = ""
	meta.AdvancePermitChapter = 0
	meta.AdvanceHold = nil
	if meta.Workbench != nil {
		meta.Workbench.BatchEnd = 0
		if progress != nil {
			meta.Workbench.BatchEnd = progress.LatestCompleted()
		}
	}
	return st.RunMeta.Save(*meta)
}
