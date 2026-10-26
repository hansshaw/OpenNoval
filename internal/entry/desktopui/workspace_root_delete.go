package desktopui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hansshaw/AInoval-web-ui/internal/host"
)

func hasProjectChildren(directory string) bool {
	entries, _ := os.ReadDir(directory)
	for _, entry := range entries {
		if entry.IsDir() && existingProject(filepath.Join(directory, entry.Name())) {
			return true
		}
	}
	return false
}

// Old desktop versions stored a novel in the catalogue itself. Remove only its
// files, retaining the catalogue, independent projects and unpublished imports.
// The root lease stays open throughout; Windows can rename its data directories
// because the lock file is deliberately kept outside the relocated data.
func stageRootProjectDeletion(target, booksRoot string, rename func(string, string) error) (container string, err error) {
	err = withWorkspaceLeaseMode(target, true, func() error {
		project, err := readProject(target)
		if err != nil {
			return err
		}
		if !project.Archived {
			return fmt.Errorf("项目已取消归档，请刷新后重试")
		}
		entries, err := os.ReadDir(target)
		if err != nil {
			return err
		}
		ownedDirectories := map[string]bool{"meta": true, "chapters": true, "drafts": true, "summaries": true, "reviews": true, "media": true, "simulate": true, "imports": true, "logs": true}
		var names []string
		for _, entry := range entries {
			name := entry.Name()
			if name == ".ainovel.lock" || name == host.ProjectDeletingFile {
				continue
			}
			if entry.IsDir() && (existingProject(filepath.Join(target, name)) || !ownedDirectories[name]) {
				continue
			}
			names = append(names, name)
		}
		// Metadata is the final commit: an interrupted operation can still be
		// retried as an archived project, while its durable marker blocks writers.
		sort.Slice(names, func(i, j int) bool {
			if names[i] == "meta" || names[j] == "meta" {
				return names[j] == "meta"
			}
			return names[i] < names[j]
		})
		parent := filepath.Dir(booksRoot)
		if directoryContains(target, parent) {
			parent = filepath.Dir(target)
		}
		container, err = os.MkdirTemp(parent, ".ainovel-delete-*")
		if err != nil {
			return err
		}
		token := filepath.Base(container) + "\n"
		if err := writeDeletionMarker(target, token); err != nil {
			_ = os.Remove(container)
			return err
		}
		moved := []string{}
		for _, name := range names {
			if err := rename(filepath.Join(target, name), filepath.Join(container, name)); err != nil {
				var rollback error
				for i := len(moved) - 1; i >= 0; i-- {
					rollback = errors.Join(rollback, rename(filepath.Join(container, moved[i]), filepath.Join(target, moved[i])))
				}
				if rollback == nil {
					rollback = clearDeletionMarker(target, token)
					_ = os.Remove(container)
				}
				return errors.Join(err, rollback)
			}
			moved = append(moved, name)
		}
		// Retain the marker in the now-empty home. Stale instances cannot revive
		// this deleted novel; independent child projects use their own leases.
		return syncDeletionDirectory(target)
	})
	return container, err
}

func directoryContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
