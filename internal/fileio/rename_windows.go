package fileio

import (
	"errors"
	"syscall"
	"time"
)

const (
	renameRetryTimeout                  = 2 * time.Second
	errorSharingViolation syscall.Errno = 32
)

// Like Go's cmd/internal/robustio, retain MoveFileEx (os.Rename) semantics and
// wait for transient readers to close. FILE_SHARE_DELETE alone does not make
// replacement of an open target succeed; ReplaceFile has different failure
// and metadata semantics. Do not retry missing paths or unrelated errors.
func retryRename(rename func() error, targetIsDirectory func() bool) error {
	deadline := time.Now().Add(renameRetryTimeout)
	delay := time.Millisecond
	for {
		err := rename()
		if err == nil || !isRenameSharingError(err) {
			return err
		}
		// Windows also reports ACCESS_DENIED when a file's destination is a
		// directory. That is a permanent type conflict, not an open reader.
		// Retrying every failed scheduler write would block its actor queue.
		if targetIsDirectory() {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return err
		}
		time.Sleep(min(delay, remaining))
		delay = min(2*delay, 50*time.Millisecond)
	}
}

func isRenameSharingError(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) &&
		(errno == syscall.ERROR_ACCESS_DENIED || errno == errorSharingViolation)
}
