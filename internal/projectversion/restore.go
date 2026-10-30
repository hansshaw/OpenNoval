package projectversion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"time"

	"github.com/hansshaw/OpenNovel/internal/domain"
)

// Restore verifies every snapshot file and the preview token before writing.
// It creates a durable pre-restore backup, assembles the new tree separately,
// and rolls directory renames back if any replacement fails. The lock inode,
// logs and version history remain in place. Previous runtime queues are removed
// on success and retained when a replacement is rolled back.
func Restore(ctx context.Context, projectDir, id, expectedFingerprint string) (RestoreResult, error) {
	root, err := openProject(projectDir)
	if err != nil {
		return RestoreResult{}, err
	}
	defer root.Close()
	if err := requireRecovered(root); err != nil {
		return RestoreResult{}, err
	}
	return restore(ctx, root, id, expectedFingerprint, root.Rename)
}

// rename is injected by tests to exercise failures after partial publication.
func restore(ctx context.Context, root *os.Root, id, expectedFingerprint string, rename func(string, string) error) (RestoreResult, error) {
	m, err := verifiedSnapshot(ctx, root, id)
	if err != nil {
		return RestoreResult{}, err
	}
	current, err := collect(ctx, root)
	if err != nil {
		return RestoreResult{}, err
	}
	before, err := previewFingerprint(ctx, root, current)
	if err != nil {
		return RestoreResult{}, err
	}
	if expectedFingerprint == "" || before != expectedFingerprint {
		return RestoreResult{}, ErrChanged
	}
	stageID, err := newID()
	if err != nil {
		return RestoreResult{}, err
	}
	stage := path.Join(versionsDir, ".restore-"+stageID)
	if err := root.MkdirAll(path.Join(stage, "new"), 0700); err != nil {
		return RestoreResult{}, err
	}
	shouldCleanup := true
	defer func() {
		if shouldCleanup {
			removeStage(root, stage)
		}
	}()
	for _, file := range m.Files {
		if err := copyFile(ctx, root, path.Join(versionsDir, id, "files", file.Path), root, path.Join(stage, "new", file.Path), file); err != nil {
			return RestoreResult{}, err
		}
	}
	if err := preserveIdentity(root, path.Join(stage, "new")); err != nil {
		return RestoreResult{}, err
	}
	if err := resetRun(root, path.Join(stage, "new")); err != nil {
		return RestoreResult{}, err
	}
	// The backup is published before any durable project files are replaced.
	backupName := []rune("恢复前备份：" + m.Name)
	if len(backupName) > 80 {
		backupName = backupName[:80]
	}
	backup, err := create(ctx, root, string(backupName))
	if err != nil {
		return RestoreResult{}, err
	}
	backupToken, err := previewFingerprint(ctx, root, backup.Files)
	if err != nil {
		return RestoreResult{}, err
	}
	if backupToken != expectedFingerprint {
		return RestoreResult{}, ErrChanged
	}
	oldRoots, err := managedRoots(root)
	if err != nil {
		return RestoreResult{}, err
	}
	if _, err := safeInfo(root, "meta/runtime"); err == nil {
		oldRoots = append(oldRoots, "meta/runtime")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return RestoreResult{}, err
	}
	newRoot, err := root.OpenRoot(path.Join(stage, "new"))
	if err != nil {
		return RestoreResult{}, err
	}
	newRoots, err := managedRoots(newRoot)
	newRoot.Close()
	if err != nil {
		return RestoreResult{}, err
	}
	all := append(slices.Clone(oldRoots), newRoots...)
	slices.Sort(all)
	all = slices.Compact(all)
	oldSet, newSet := map[string]bool{}, map[string]bool{}
	for _, rel := range oldRoots {
		oldSet[rel] = true
	}
	for _, rel := range newRoots {
		newSet[rel] = true
	}
	journal := restoreJournal{Version: 1, BackupID: backup.ID, BeforeFingerprint: backup.Fingerprint}
	for _, rel := range all {
		journal.Paths = append(journal.Paths, restorePath{Path: rel, HadOld: oldSet[rel], HasNew: newSet[rel]})
	}
	if err := syncTree(root, path.Join(stage, "new")); err != nil {
		return RestoreResult{}, err
	}
	if err := publishJournal(root, stage, journal); err != nil {
		return RestoreResult{}, err
	}
	// From this point the stage must survive a panic or incomplete rollback.
	// Startup recovery uses the journal and actual rename locations.
	shouldCleanup = false
	type movedPath struct {
		rel       string
		oldMoved  bool
		newPlaced bool
	}
	var moved []movedPath
	rollback := func(cause error) error {
		var failures []error
		for i := len(moved) - 1; i >= 0; i-- {
			item := moved[i]
			if item.newPlaced {
				if err := durableRename(root, item.rel, path.Join(stage, "new", item.rel)); err != nil {
					failures = append(failures, err)
					continue
				}
			}
			if item.oldMoved {
				if err := durableRename(root, path.Join(stage, "old", item.rel), item.rel); err != nil {
					failures = append(failures, err)
				}
			}
		}
		if len(failures) > 0 {
			// Retain rollback material for manual recovery if the filesystem itself
			// refuses rollback. The independently published backup also survives.
			return errors.Join(cause, fmt.Errorf("恢复回滚失败，原文件保留在 %s: %w", stage, errors.Join(failures...)))
		}
		shouldCleanup = true
		return cause
	}
	for _, rel := range all {
		if err := root.MkdirAll(path.Join(stage, "old", path.Dir(rel)), 0700); err != nil {
			return RestoreResult{}, rollback(err)
		}
	}
	if err := syncTree(root, stage); err != nil {
		return RestoreResult{}, rollback(err)
	}
	for _, rel := range all {
		if err := ctx.Err(); err != nil {
			return RestoreResult{}, rollback(err)
		}
		if err := root.MkdirAll(path.Join(stage, "old", path.Dir(rel)), 0700); err != nil {
			return RestoreResult{}, rollback(err)
		}
		moved = append(moved, movedPath{rel: rel})
		item := &moved[len(moved)-1]
		if oldSet[rel] {
			if err := rename(rel, path.Join(stage, "old", rel)); err != nil {
				return RestoreResult{}, rollback(err)
			}
			item.oldMoved = true
			if err := syncRenameParents(root, rel, path.Join(stage, "old", rel)); err != nil {
				return RestoreResult{}, rollback(err)
			}
		}
		if newSet[rel] {
			if err := root.MkdirAll(path.Dir(rel), 0700); err != nil {
				return RestoreResult{}, rollback(err)
			}
			if err := rename(path.Join(stage, "new", rel), rel); err != nil {
				return RestoreResult{}, rollback(err)
			}
			item.newPlaced = true
			if err := syncRenameParents(root, path.Join(stage, "new", rel), rel); err != nil {
				return RestoreResult{}, rollback(err)
			}
		}
	}
	if err := publishMarker(root, stage, "committed", []byte("committed\n")); err != nil {
		// Publication of the marker may have succeeded before directory Sync
		// failed. Keep the complete journal so startup decides by the marker.
		return RestoreResult{}, err
	}
	shouldCleanup = true
	return RestoreResult{Backup: backup, Restored: m}, nil
}

func readObject(root *os.Root, rel string) (map[string]json.RawMessage, bool, error) {
	f, err := openRegular(root, rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	var object map[string]json.RawMessage
	if err := json.NewDecoder(f).Decode(&object); err != nil {
		return nil, false, err
	}
	if object == nil {
		return nil, false, errors.New("项目元数据必须是 JSON 对象")
	}
	return object, true, nil
}

func writeObject(root *os.Root, rel string, object map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(root, rel, data)
}

func preserveIdentity(root *os.Root, stage string) error {
	current, exists, err := readObject(root, "meta/web-project.json")
	if err != nil || !exists {
		return err
	}
	rel := path.Join(stage, "meta/web-project.json")
	target, _, err := readObject(root, rel)
	if err != nil {
		return err
	}
	if target == nil {
		target = map[string]json.RawMessage{}
	}
	for _, field := range []string{"id", "name", "copiedFrom", "createdAt"} {
		if value, exists := current[field]; exists {
			target[field] = value
		} else {
			delete(target, field)
		}
	}
	target["updatedAt"], _ = json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
	return writeObject(root, rel, target)
}

// resetRun retains durable approvals and story state while withdrawing commands
// whose one-time authorization belonged to an earlier execution.
func resetRun(root *os.Root, stage string) error {
	rel := path.Join(stage, "meta/run.json")
	meta, exists, err := readObject(root, rel)
	if err != nil || !exists {
		return err
	}
	delete(meta, "pending_steer")
	delete(meta, "advance_permit_chapter")
	delete(meta, "advance_hold")
	meta["started_at"], _ = json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
	if value := meta["workbench"]; len(value) > 0 && string(value) != "null" {
		var workbench map[string]json.RawMessage
		if err := json.Unmarshal(value, &workbench); err != nil || workbench == nil {
			return fmt.Errorf("工作台运行信息无效: %w", err)
		}
		latest := 0
		progressFile, err := openRegular(root, path.Join(stage, "meta/progress.json"))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err == nil {
			var progress domain.Progress
			decodeErr := json.NewDecoder(progressFile).Decode(&progress)
			progressFile.Close()
			if decodeErr != nil {
				return decodeErr
			}
			latest = progress.LatestCompleted()
		}
		workbench["batchEnd"], _ = json.Marshal(latest)
		meta["workbench"], _ = json.Marshal(workbench)
	}
	return writeObject(root, rel, meta)
}
