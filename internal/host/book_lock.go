package host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

const bookLockFile = ".ainovel.lock"

// ProjectDeletingFile prevents a new writer from opening a project while the
// deleter releases its lock handle and relocates the directory on Windows.
const ProjectDeletingFile = ".ainovel-deleting"

// ErrBookInUse 表示同一小说目录已被另一个进程占用。
var ErrBookInUse = errors.New("小说目录已被另一个 ainovel-cli 实例占用")

var ErrProjectDeleting = errors.New("项目正在删除；若上次删除中断，请在项目中心重试永久删除")

// CheckProjectDeleting must be checked both before and after acquiring a lease.
// Any marker, including a link, blocks opening; its contents are never executed.
func CheckProjectDeleting(dir string) error {
	if _, err := os.Lstat(filepath.Join(dir, ProjectDeletingFile)); err == nil {
		return ErrProjectDeleting
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// bookLease 在 Host 的完整生命周期内持有小说目录的跨进程独占权。
// 锁文件会保留在目录中；真正的占用状态由操作系统管理，进程异常退出也会自动释放。
type bookLease struct {
	lock *flock.Flock
}

func acquireBookLease(dir string) (*bookLease, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("解析小说目录: %w", err)
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建小说目录: %w", err)
	}
	if err := CheckProjectDeleting(absDir); err != nil {
		return nil, err
	}
	fileLock := flock.New(filepath.Join(absDir, bookLockFile), flock.SetPermissions(0o600))
	locked, err := fileLock.TryLock()
	if err != nil {
		return nil, closeBookLockAfterFailure(fileLock, fmt.Errorf("占用小说目录 %q: %w", absDir, err))
	}
	if !locked {
		return nil, closeBookLockAfterFailure(fileLock, fmt.Errorf(
			"%w：%s；请关闭正在操作此目录的另一个终端，或使用不同的小说目录",
			ErrBookInUse,
			absDir,
		))
	}
	if err := CheckProjectDeleting(absDir); err != nil {
		return nil, closeBookLockAfterFailure(fileLock, err)
	}
	return &bookLease{lock: fileLock}, nil
}

func closeBookLockAfterFailure(fileLock *flock.Flock, cause error) error {
	if err := fileLock.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("关闭小说目录锁: %w", err))
	}
	return cause
}

func (l *bookLease) Close() error {
	if l == nil || l.lock == nil {
		return nil
	}
	err := l.lock.Close()
	l.lock = nil
	return err
}
