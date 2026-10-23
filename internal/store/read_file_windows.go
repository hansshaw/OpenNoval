package store

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// A workbench read and an engine write may use different Store instances.
// Ordinary Windows os.Open denies deletion while its handle is alive, which
// makes the writer's atomic Rename fail even though both operations are valid.
// Share deletion so an open reader retains the old file snapshot while Rename
// publishes the replacement for subsequent readers, as it does on Unix.
func openFileSnapshot(name string) (*os.File, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	// Extended paths retain support for long project directories and UNC shares.
	if !strings.HasPrefix(absolute, `\\?\`) && !strings.HasPrefix(absolute, `\\.\`) {
		if strings.HasPrefix(absolute, `\\`) {
			absolute = `\\?\UNC\` + strings.TrimPrefix(absolute, `\\`)
		} else {
			absolute = `\\?\` + absolute
		}
	}
	ptr, err := syscall.UTF16PtrFromString(absolute)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	handle, err := syscall.CreateFile(ptr, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	return os.NewFile(uintptr(handle), name), nil
}
func readFileSnapshot(name string) ([]byte, error) {
	f, err := openFileSnapshot(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
