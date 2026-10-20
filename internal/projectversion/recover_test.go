package projectversion

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// simulate a process exit immediately after a real rename. No application
// rollback runs; only the durable journal and files on disk remain.
func crashRestore(t *testing.T, dir string, m Manifest, token string, after int) string {
	t.Helper()
	root, err := openProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	count := 0
	crashed := false
	func() {
		defer func() {
			if recover() != nil {
				crashed = true
			}
		}()
		_, err := restore(context.Background(), root, m.ID, token, func(old, new string) error {
			if after == 0 {
				panic("simulated process exit before first rename")
			}
			if err := root.Rename(old, new); err != nil {
				return err
			}
			count++
			if count == after {
				panic("simulated process exit after rename")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("restore failed before simulated exit: %v", err)
		}
	}()
	if !crashed {
		t.Fatalf("did not reach crash point %d (got %d renames)", after, count)
	}
	stages, err := restoreStages(root)
	if err != nil || len(stages) != 1 {
		t.Fatalf("missing durable stage: %v, %v", stages, err)
	}
	return stages[0]
}

func crashFixture(t *testing.T) (string, Manifest, Comparison) {
	t.Helper()
	dir := t.TempDir()
	put(t, dir, "chapters/01.md", "snapshot")
	put(t, dir, "meta/progress.json", `{"completed_chapters":[1]}`)
	put(t, dir, "notes/one.md", "snapshot note")
	m := snapshot(t, dir, "before")
	put(t, dir, "chapters/01.md", "current")
	put(t, dir, "meta/progress.json", `{"completed_chapters":[1,2]}`)
	put(t, dir, "current.txt", "current only")
	put(t, dir, "meta/runtime/queue.jsonl", "current queue")
	if err := os.RemoveAll(filepath.Join(dir, "notes")); err != nil {
		t.Fatal(err)
	}
	return dir, m, comparison(t, dir, m.ID)
}

func TestRecoverProcessExitAfterEveryRename(t *testing.T) {
	for crashAt := 0; crashAt <= 7; crashAt++ {
		t.Run(string(rune('0'+crashAt)), func(t *testing.T) {
			dir, m, preview := crashFixture(t)
			crashRestore(t, dir, m, preview.Fingerprint, crashAt)
			if pending, err := HasPendingRecovery(dir); err != nil || !pending {
				t.Fatalf("pending=%v err=%v", pending, err)
			}
			if _, err := Create(context.Background(), dir, "while recovering"); !errors.Is(err, ErrRecoveryPending) {
				t.Fatalf("create accepted an incomplete transaction: %v", err)
			}
			if _, err := Compare(context.Background(), dir, m.ID); !errors.Is(err, ErrRecoveryPending) {
				t.Fatalf("compare accepted an incomplete transaction: %v", err)
			}
			if _, err := Restore(context.Background(), dir, m.ID, preview.Fingerprint); !errors.Is(err, ErrRecoveryPending) {
				t.Fatalf("restore accepted an incomplete transaction: %v", err)
			}
			if recovered, err := Recover(context.Background(), dir); err != nil || !recovered {
				t.Fatalf("recovered=%v err=%v", recovered, err)
			}
			after := comparison(t, dir, m.ID)
			if after.Fingerprint != preview.Fingerprint || get(t, dir, "meta/runtime/queue.jsonl") != "current queue" {
				t.Fatal("startup recovery did not restore original project and runtime")
			}
			if pending, err := HasPendingRecovery(dir); err != nil || pending {
				t.Fatalf("recovery left pending=%v err=%v", pending, err)
			}
			if recovered, err := Recover(context.Background(), dir); err != nil || recovered {
				t.Fatalf("second recovery was not a no-op: %v %v", recovered, err)
			}
		})
	}
}

func TestRecoverRetainsCommittedRestore(t *testing.T) {
	dir, m, preview := crashFixture(t)
	stage := crashRestore(t, dir, m, preview.Fingerprint, 7)
	root, err := openProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishMarker(root, stage, "committed", []byte("committed\n")); err != nil {
		t.Fatal(err)
	}
	root.Close()
	if _, err := Recover(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if get(t, dir, "chapters/01.md") != "snapshot" || get(t, dir, "notes/one.md") != "snapshot note" {
		t.Fatal("committed restoration was rolled back")
	}
	if _, err := os.Stat(filepath.Join(dir, "current.txt")); !os.IsNotExist(err) {
		t.Fatal("current-only file survived committed restore")
	}
}

func TestRecoverAfterInterruptedRollback(t *testing.T) {
	dir, m, preview := crashFixture(t)
	stage := crashRestore(t, dir, m, preview.Fingerprint, 7)
	root, err := openProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Roll back the last path and stop, as if recovery itself was interrupted.
	if err := durableRename(root, "notes", path.Join(stage, "new/notes")); err != nil {
		t.Fatal(err)
	}
	root.Close()
	if _, err := Recover(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if after := comparison(t, dir, m.ID); after.Fingerprint != preview.Fingerprint {
		t.Fatal("retry after interrupted rollback did not recover original")
	}
}

func TestRecoverPreparationAndCleanupStages(t *testing.T) {
	for _, prefix := range []string{".restore-", ".cleanup-"} {
		t.Run(prefix, func(t *testing.T) {
			dir := t.TempDir()
			put(t, dir, "story.md", "live")
			stage := "meta/versions/" + prefix + strings.Repeat("a", 32)
			put(t, dir, stage+"/new/story.md", "unused assembly")
			if _, err := Recover(context.Background(), dir); err != nil {
				t.Fatal(err)
			}
			if get(t, dir, "story.md") != "live" {
				t.Fatal("cleanup changed live story")
			}
			if _, err := os.Stat(filepath.Join(dir, stage)); !os.IsNotExist(err) {
				t.Fatal("stage remained after cleanup")
			}
		})
	}
}

func TestRecoveryAllowsFirstProjectStartup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created-yet")
	if pending, err := HasPendingRecovery(dir); err != nil || pending {
		t.Fatalf("new project pending=%v err=%v", pending, err)
	}
	if recovered, err := Recover(context.Background(), dir); err != nil || recovered {
		t.Fatalf("new project recovered=%v err=%v", recovered, err)
	}
}

func TestRecoverRejectsCorruptJournalWithoutMutating(t *testing.T) {
	dir, m, preview := crashFixture(t)
	stage := crashRestore(t, dir, m, preview.Fingerprint, 1)
	put(t, dir, stage+"/journal.json", `{"version":1,"backupId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","paths":[{"path":"../outside","hadOld":true}]}`)
	if _, err := Recover(context.Background(), dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("unsafe journal accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, stage)); err != nil {
		t.Fatal("corrupt recovery discarded original files")
	}
}
