package web

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
	"github.com/voocel/ainovel-cli/internal/host"
)

func withWorkspaceLease(directory string, fn func() error) error {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("作品目录无效")
	}
	lockPath := filepath.Join(directory, ".ainovel.lock")
	if info, err := os.Lstat(lockPath); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("作品锁文件不安全")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lease := flock.New(lockPath, flock.SetPermissions(0600))
	defer lease.Close()
	locked, err := lease.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return host.ErrProjectSnapshotBusy
	}
	lockedInfo, err := lease.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := os.Lstat(lockPath)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(lockedInfo, pathInfo) {
		return fmt.Errorf("作品锁已变化")
	}
	return fn()
}
