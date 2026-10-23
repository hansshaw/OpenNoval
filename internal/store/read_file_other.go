//go:build !windows

package store

import "os"

func readFileSnapshot(name string) ([]byte, error) { return os.ReadFile(name) }
