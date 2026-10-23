//go:build !windows

package fileio

func retryRename(rename func() error, _ func() bool) error { return rename() }
