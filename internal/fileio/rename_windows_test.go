package fileio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Use a real ordinary Windows reader, not an injected sharing-error sentinel:
// /api/state and antivirus readers can keep the destination open concurrently
// with a different Store instance publishing the next revision.
func TestWindowsRenameRetriesUntilReaderCloses(t *testing.T) {
	for _, confined := range []bool{false, true} {
		t.Run(fmt.Sprintf("root=%t", confined), func(t *testing.T) {
			dir := t.TempDir()
			writeTestFile(t, dir, "state.json", "old")
			writeTestFile(t, dir, "state.tmp", "new")
			reader, err := os.Open(filepath.Join(dir, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			attempt := func() error { return os.Rename(filepath.Join(dir, "state.tmp"), filepath.Join(dir, "state.json")) }
			if confined {
				attempt = func() error { return root.Rename("state.tmp", "state.json") }
			}
			attempts := 0
			var firstErr error
			err = retryRename(func() error {
				attempts++
				err := attempt()
				if attempts == 1 {
					firstErr = err
					// Synchronize release with the actual failing syscall, so
					// the regression does not depend on scheduler timing.
					// os.Root may use POSIX rename semantics on Windows and
					// succeed immediately; the held reader must still see old.
					want := "old"
					if err == nil {
						want = "new"
					}
					assertTestFile(t, dir, "state.json", want)
					held, readErr := io.ReadAll(reader)
					if readErr != nil || string(held) != "old" {
						t.Fatalf("held reader changed: %q %v", held, readErr)
					}
					if closeErr := reader.Close(); closeErr != nil {
						t.Fatal(closeErr)
					}
				}
				return err
			}, func() bool { return false })
			if err != nil || (firstErr != nil && (!isRenameSharingError(firstErr) || attempts < 2)) || (!confined && firstErr == nil) {
				t.Fatalf("transient conflict: first=%v attempts=%d final=%v", firstErr, attempts, err)
			}
			assertTestFile(t, dir, "state.json", "new")
		})
	}
}

func TestWindowsRenamePersistentReaderFailsWithoutChangingFiles(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "state.json", "old")
	writeTestFile(t, dir, "state.tmp", "new")
	reader, err := os.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	start := time.Now()
	err = Rename(filepath.Join(dir, "state.tmp"), filepath.Join(dir, "state.json"))
	if !isRenameSharingError(err) {
		t.Fatalf("persistent conflict should return the real filesystem error: %v", err)
	}
	if elapsed := time.Since(start); elapsed < renameRetryTimeout || elapsed > renameRetryTimeout+5*time.Second {
		t.Fatalf("retry was not bounded around %s: %s", renameRetryTimeout, elapsed)
	}
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) {
		t.Fatalf("original syscall context was lost: %T %v", err, err)
	}
	assertTestFile(t, dir, "state.json", "old")
	assertTestFile(t, dir, "state.tmp", "new")
}

func TestWindowsRenameDoesNotRetryUnrelatedErrors(t *testing.T) {
	for _, cause := range []error{syscall.ERROR_FILE_NOT_FOUND, syscall.ERROR_PATH_NOT_FOUND, syscall.Errno(87) /* ERROR_INVALID_PARAMETER */, errors.New("caller failure")} {
		t.Run(cause.Error(), func(t *testing.T) {
			want := &os.LinkError{Op: "rename", Old: "old", New: "new", Err: cause}
			attempts := 0
			err := retryRename(func() error { attempts++; return want }, func() bool { return false })
			if err != want || attempts != 1 {
				t.Fatalf("unrelated error retried or replaced: attempts=%d err=%v", attempts, err)
			}
		})
	}
}

func TestWindowsRenameDirectoryDestinationDoesNotRetry(t *testing.T) {
	for _, confined := range []bool{false, true} {
		t.Run(fmt.Sprintf("root=%t", confined), func(t *testing.T) {
			dir := t.TempDir()
			writeTestFile(t, dir, "state.tmp", "new")
			if err := os.Mkdir(filepath.Join(dir, "state.json"), 0700); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			attempt := func() error { return os.Rename(filepath.Join(dir, "state.tmp"), filepath.Join(dir, "state.json")) }
			stat := func() (os.FileInfo, error) { return os.Lstat(filepath.Join(dir, "state.json")) }
			if confined {
				attempt = func() error { return root.Rename("state.tmp", "state.json") }
				stat = func() (os.FileInfo, error) { return root.Lstat("state.json") }
			}
			attempts := 0
			var firstErr error
			err = retryRename(func() error {
				attempts++
				firstErr = attempt()
				return firstErr
			}, func() bool { info, statErr := stat(); return statErr == nil && info.IsDir() })
			if err == nil || err != firstErr || attempts != 1 {
				t.Fatalf("permanent type conflict retried or error replaced: attempts=%d err=%v first=%v", attempts, err, firstErr)
			}
			assertTestFile(t, dir, "state.tmp", "new")
			if info, err := stat(); err != nil || !info.IsDir() {
				t.Fatalf("failed publication changed destination: %v %v", info, err)
			}
		})
	}
}
