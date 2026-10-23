// Package fileio provides filesystem operations used to publish saved state.
package fileio

import "os"

// Rename publishes a closed temporary file using os.Rename. On Windows, a
// reader or virus scanner can temporarily prevent replacement; only sharing
// violations and access-denied errors are retried, for at most two seconds.
// The destination is never removed before replacement. Callers retain ownership
// of the temporary file and must clean it up if publication fails.
func Rename(oldpath, newpath string) error {
	return retryRename(
		func() error { return os.Rename(oldpath, newpath) },
		func() bool { info, err := os.Lstat(newpath); return err == nil && info.IsDir() },
	)
}

// RootRename has Rename's retry behavior while preserving os.Root confinement.
func RootRename(root *os.Root, oldpath, newpath string) error {
	return retryRename(
		func() error { return root.Rename(oldpath, newpath) },
		func() bool { info, err := root.Lstat(newpath); return err == nil && info.IsDir() },
	)
}
