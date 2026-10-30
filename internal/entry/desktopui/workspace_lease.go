package desktopui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
	"github.com/hansshaw/OpenNovel/internal/host"
)

func withWorkspaceLease(directory string, fn func() error) error {
	return withWorkspaceLeaseMode(directory, false, fn)
}

// Only the delete operation may reopen a marked directory, so a user can retry
// deletion after a process exit between marker publication and relocation.
func withWorkspaceLeaseMode(directory string, allowDeleting bool, fn func() error) error {
	lease, err := acquireWorkspaceLease(directory, allowDeleting)
	if err != nil {
		return err
	}
	defer lease.Close()
	return fn()
}

// acquireWorkspaceLease also supports metadata handlers that must release the
// lease before rebuilding a Host, which acquires its own lease for the project.
func acquireWorkspaceLease(directory string, allowDeleting bool) (*flock.Flock, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("作品目录无效")
	}
	if !allowDeleting {
		if err := host.CheckProjectDeleting(directory); err != nil {
			return nil, err
		}
	}
	lockPath := filepath.Join(directory, ".ainovel.lock")
	if info, err := os.Lstat(lockPath); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("作品锁文件不安全")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lease := flock.New(lockPath, flock.SetPermissions(0600))
	owned := false
	defer func() {
		if !owned {
			_ = lease.Close()
		}
	}()
	locked, err := lease.TryLock()
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, host.ErrProjectSnapshotBusy
	}
	lockedInfo, err := lease.Stat()
	if err != nil {
		return nil, err
	}
	pathInfo, err := os.Lstat(lockPath)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(lockedInfo, pathInfo) {
		return nil, fmt.Errorf("作品锁已变化")
	}
	if !allowDeleting {
		if err := host.CheckProjectDeleting(directory); err != nil {
			return nil, err
		}
	}
	owned = true
	return lease, nil
}
