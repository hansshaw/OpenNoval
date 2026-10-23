//go:build !windows

package fileio

func retryRename(rename func() error) error { return rename() }
