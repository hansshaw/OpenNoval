package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

var outlineMutationFiles = []string{
	"outline.json",
	"outline.md",
	"layered_outline.json",
	"layered_outline.md",
	"meta/progress.json",
}

type outlineMutationSnapshot struct {
	rel    string
	data   []byte
	mode   fs.FileMode
	exists bool
}

// withOutlineMutationRollback gives outline writes a file-level transaction
// boundary. Callers must hold the Outline, Progress, and RunMeta write locks.
func (s *Store) withOutlineMutationRollback(fn func() error) error {
	snapshots, err := s.snapshotOutlineMutationFiles()
	if err != nil {
		return fmt.Errorf("snapshot outline mutation: %w", err)
	}
	if err := fn(); err != nil {
		if rollbackErr := s.restoreOutlineMutationFiles(snapshots); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("rollback outline mutation: %w", rollbackErr))
		}
		return err
	}
	return nil
}

func (s *Store) snapshotOutlineMutationFiles() ([]outlineMutationSnapshot, error) {
	snapshots := make([]outlineMutationSnapshot, 0, len(outlineMutationFiles))
	for _, rel := range outlineMutationFiles {
		path := filepath.Join(s.dir, filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			snapshots = append(snapshots, outlineMutationSnapshot{rel: rel})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rel, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", rel, err)
		}
		snapshots = append(snapshots, outlineMutationSnapshot{
			rel: rel, data: data, mode: info.Mode().Perm(), exists: true,
		})
	}
	return snapshots, nil
}

func (s *Store) restoreOutlineMutationFiles(snapshots []outlineMutationSnapshot) error {
	var restoreErrs []error
	for _, snapshot := range snapshots {
		if snapshot.exists {
			if err := s.Outline.io.writeFileUnlockedRaw(snapshot.rel, snapshot.data, snapshot.mode); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restore %s: %w", snapshot.rel, err))
			}
			continue
		}
		if err := s.Outline.io.removeFileUnlockedRaw(snapshot.rel); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("remove restored-absent %s: %w", snapshot.rel, err))
		}
	}
	return errors.Join(restoreErrs...)
}
