package projectversion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"runtime"
	"slices"
	"strings"
)

type restorePath struct {
	Path   string `json:"path"`
	HadOld bool   `json:"hadOld"`
	HasNew bool   `json:"hasNew"`
}

type restoreJournal struct {
	Version           int           `json:"version"`
	BackupID          string        `json:"backupId"`
	BeforeFingerprint string        `json:"beforeFingerprint"`
	Paths             []restorePath `json:"paths"`
}

// HasPendingRecovery is read-only. It lets callers avoid taking another lease
// for the usual startup path. A true result requires Recover before stores open.
func HasPendingRecovery(projectDir string) (bool, error) {
	root, err := openProject(projectDir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer root.Close()
	stages, err := restoreStages(root)
	return len(stages) > 0, err
}

func requireRecovered(root *os.Root) error {
	stages, err := restoreStages(root)
	if err != nil {
		return err
	}
	if len(stages) > 0 {
		return ErrRecoveryPending
	}
	return nil
}

// Recover must run under the project lease, before opening a Host. It rolls an
// uncommitted restore back using its durable plan and actual file locations.
// A committed transaction only needs its staging data removed. It is safe to
// retry recovery after another interruption, including during the rollback.
func Recover(ctx context.Context, projectDir string) (bool, error) {
	root, err := openProject(projectDir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer root.Close()
	stages, err := restoreStages(root)
	if err != nil || len(stages) == 0 {
		return false, err
	}
	// Validate every tree before any mutation, including excluded staging data.
	if _, err := collect(ctx, root); err != nil {
		return false, err
	}
	for _, stage := range stages {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := recoverStage(ctx, root, stage); err != nil {
			return false, err
		}
	}
	return true, nil
}

func restoreStages(root *os.Root) ([]string, error) {
	if _, err := safeInfo(root, versionsDir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	f, err := root.Open(versionsDir)
	if err != nil {
		return nil, err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return nil, err
	}
	var stages []string
	for _, entry := range entries {
		prefix := ".restore-"
		if strings.HasPrefix(entry.Name(), ".cleanup-") {
			prefix = ".cleanup-"
		} else if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if !validID(strings.TrimPrefix(entry.Name(), prefix)) {
			return nil, ErrCorrupt
		}
		rel := path.Join(versionsDir, entry.Name())
		info, err := safeInfo(root, rel)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, ErrCorrupt
		}
		stages = append(stages, rel)
	}
	slices.Sort(stages)
	return stages, nil
}

func readJournal(root *os.Root, stage string) (restoreJournal, error) {
	f, err := openRegular(root, path.Join(stage, "journal.json"))
	if err != nil {
		return restoreJournal{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil || len(data) > maxManifestBytes {
		return restoreJournal{}, ErrCorrupt
	}
	var j restoreJournal
	if json.Unmarshal(data, &j) != nil || j.Version != 1 || !validID(j.BackupID) || len(j.Paths) > maxFiles {
		return restoreJournal{}, ErrCorrupt
	}
	previous := ""
	for _, item := range j.Paths {
		parts := strings.Split(item.Path, "/")
		valid := validPath(item.Path) || item.Path == "meta/runtime"
		if !valid || item.Path <= previous || item.Path == "meta" || len(parts) > 2 || (len(parts) == 2 && parts[0] != "meta") || (!item.HadOld && !item.HasNew) {
			return restoreJournal{}, ErrCorrupt
		}
		previous = item.Path
	}
	return j, nil
}

func recoverStage(ctx context.Context, root *os.Root, stage string) error {
	if strings.HasPrefix(path.Base(stage), ".cleanup-") {
		if err := root.RemoveAll(stage); err != nil {
			return err
		}
		return syncDir(root, versionsDir)
	}
	j, err := readJournal(root, stage)
	if errors.Is(err, fs.ErrNotExist) {
		// The plan is published before the first project rename. A stage without
		// a plan can only contain assembly data, never displaced original files.
		if exists, err := pathExists(root, path.Join(stage, "old")); err != nil {
			return err
		} else if exists {
			return ErrCorrupt
		}
		return removeStage(root, stage)
	}
	if err != nil {
		return err
	}
	if committed, err := pathExists(root, path.Join(stage, "committed")); err != nil {
		return err
	} else if committed {
		marker, err := openRegular(root, path.Join(stage, "committed"))
		if err != nil {
			return err
		}
		value, err := io.ReadAll(io.LimitReader(marker, 16))
		marker.Close()
		if err != nil || string(value) != "committed\n" {
			return ErrCorrupt
		}
		return removeStage(root, stage)
	}
	backup, err := verifiedSnapshot(ctx, root, j.BackupID)
	if err != nil {
		return err
	}
	if backup.Fingerprint != j.BeforeFingerprint {
		return ErrCorrupt
	}
	for i := len(j.Paths) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := j.Paths[i]
		old := path.Join(stage, "old", item.Path)
		new := path.Join(stage, "new", item.Path)
		oldExists, err := pathExists(root, old)
		if err != nil {
			return err
		}
		newExists, err := pathExists(root, new)
		if err != nil {
			return err
		}
		liveExists, err := pathExists(root, item.Path)
		if err != nil {
			return err
		}
		if item.HadOld && oldExists {
			if liveExists {
				if !item.HasNew || newExists {
					return fmt.Errorf("%w: %s", ErrChanged, item.Path)
				}
				if err := root.MkdirAll(path.Dir(new), 0700); err != nil {
					return err
				}
				if err := durableRename(root, item.Path, new); err != nil {
					return err
				}
			}
			if err := root.MkdirAll(path.Dir(item.Path), 0700); err != nil {
				return err
			}
			if err := durableRename(root, old, item.Path); err != nil {
				return err
			}
		} else if item.HadOld {
			if !liveExists || (item.HasNew && !newExists) {
				return fmt.Errorf("%w: 恢复日志缺少原文件 %s", ErrCorrupt, item.Path)
			}
		} else if liveExists {
			if newExists {
				return fmt.Errorf("%w: %s", ErrChanged, item.Path)
			}
			if err := root.MkdirAll(path.Dir(new), 0700); err != nil {
				return err
			}
			if err := durableRename(root, item.Path, new); err != nil {
				return err
			}
		}
	}
	current, err := currentFingerprint(ctx, root)
	if err != nil {
		return err
	}
	if current != j.BeforeFingerprint {
		return fmt.Errorf("%w: 恢复后的文件与恢复前备份不一致", ErrCorrupt)
	}
	return removeStage(root, stage)
}

func pathExists(root *os.Root, rel string) (bool, error) {
	_, err := safeInfo(root, rel)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func publishJournal(root *os.Root, stage string, j restoreJournal) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return publishMarker(root, stage, "journal.json", data)
}

func publishMarker(root *os.Root, stage, name string, data []byte) error {
	tmp := path.Join(stage, name+".tmp")
	if err := writeFile(root, tmp, data); err != nil {
		return err
	}
	return durableRename(root, tmp, path.Join(stage, name))
}

func durableRename(root *os.Root, old, new string) error {
	if err := root.Rename(old, new); err != nil {
		return err
	}
	return syncRenameParents(root, old, new)
}

func syncRenameParents(root *os.Root, old, new string) error {
	if err := syncDir(root, path.Dir(old)); err != nil {
		return err
	}
	if path.Dir(old) != path.Dir(new) {
		return syncDir(root, path.Dir(new))
	}
	return nil
}

func syncDir(root *os.Root, rel string) error {
	// Windows does not expose directory fsync through os.File. Regular data
	// files and journal markers are still flushed before their rename.
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := root.Open(rel)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func syncTree(root *os.Root, base string) error {
	var dirs []string
	if err := fs.WalkDir(root.FS(), base, func(rel string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			dirs = append(dirs, rel)
		}
		return nil
	}); err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := syncDir(root, dirs[i]); err != nil {
			return err
		}
	}
	return syncDir(root, path.Dir(base))
}

func removeStage(root *os.Root, stage string) error {
	cleanup := path.Join(versionsDir, strings.Replace(path.Base(stage), ".restore-", ".cleanup-", 1))
	if err := durableRename(root, stage, cleanup); err != nil {
		return err
	}
	if err := root.RemoveAll(cleanup); err != nil {
		return err
	}
	return syncDir(root, versionsDir)
}
