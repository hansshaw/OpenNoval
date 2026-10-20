package projectversion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func put(t *testing.T, dir, rel, text string) {
	t.Helper()
	file := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func snapshot(t *testing.T, dir, name string) Manifest {
	t.Helper()
	m, err := Create(context.Background(), dir, name)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func comparison(t *testing.T, dir, id string) Comparison {
	t.Helper()
	c, err := Compare(context.Background(), dir, id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCreateIndependentVerifiedFilesAndExclusions(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "chapters/01.md", "正文\n")
	put(t, dir, "meta/review.json", `{"approved":true}`)
	for _, rel := range []string{".ainovel.lock", "logs/old.log", "meta/runtime/queue.jsonl", "chapters/draft.tmp", "meta/file.tmp-123"} {
		put(t, dir, rel, "excluded")
	}
	m := snapshot(t, dir, "初稿")
	want := []string{"chapters/01.md", "meta/review.json"}
	var got []string
	for _, file := range m.Files {
		got = append(got, file.Path)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("files %v, want %v", got, want)
	}
	original, _ := os.Stat(filepath.Join(dir, "chapters/01.md"))
	copy, _ := os.Stat(filepath.Join(dir, "meta/versions", m.ID, "files/chapters/01.md"))
	if os.SameFile(original, copy) {
		t.Fatal("snapshot reused original inode")
	}
	put(t, dir, "chapters/01.md", "新版")
	if text := get(t, dir, "meta/versions/"+m.ID+"/files/chapters/01.md"); text != "正文\n" {
		t.Fatalf("snapshot changed with original: %q", text)
	}
	second := snapshot(t, dir, "第二稿")
	if len(second.Files) != len(m.Files) {
		t.Fatal("version history recursively included in snapshot")
	}
	listed, err := List(dir)
	if err != nil || len(listed) != 2 || listed[0].ID != second.ID {
		t.Fatalf("list: %+v, %v", listed, err)
	}
}

func TestCompareRestoreDirectionAndTextDiff(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "changed.md", "开头\n旧内容\n结尾\n")
	put(t, dir, "removed.md", "恢复时新增")
	m := snapshot(t, dir, "before")
	put(t, dir, "changed.md", "开头\n新内容\n结尾\n")
	put(t, dir, "added.md", "恢复时删除")
	if err := os.Remove(filepath.Join(dir, "removed.md")); err != nil {
		t.Fatal(err)
	}
	c := comparison(t, dir, m.ID)
	if len(c.Changes) != 3 || c.Changes[0].Kind != "deleted" || c.Changes[1].Kind != "modified" || c.Changes[2].Kind != "added" {
		t.Fatalf("changes: %+v", c.Changes)
	}
	if diff := c.Changes[1].Diff; !strings.Contains(diff, "-新内容") || !strings.Contains(diff, "+旧内容") {
		t.Fatalf("wrong diff direction: %s", diff)
	}
}

func TestRestorePreservesIdentityLeaseApprovalsAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "chapters/01.md", "初稿")
	put(t, dir, "meta/web-project.json", `{"id":"old-id","name":"旧名称","copiedFrom":"old-source","createdAt":"old-date","genre":"奇幻","custom":42}`)
	put(t, dir, "meta/progress.json", `{"completed_chapters":[1,3,2]}`)
	put(t, dir, "meta/run.json", `{"pending_steer":"旧指令","advance_permit_chapter":4,"advance_hold":{"chapter":4},"workbench":{"batchEnd":20,"approvedFingerprint":"reviewed","acceptedChapter":3},"custom":42}`)
	m := snapshot(t, dir, "初稿")
	put(t, dir, "chapters/01.md", "当前版本")
	put(t, dir, "meta/web-project.json", `{"id":"current-id","name":"当前名称","copiedFrom":"current-source","createdAt":"current-date","genre":"科幻"}`)
	put(t, dir, "meta/runtime/queue.jsonl", "old queued command")
	put(t, dir, "logs/current.log", "keep log")
	put(t, dir, ".ainovel.lock", "keep inode")
	lockBefore, _ := os.Stat(filepath.Join(dir, ".ainovel.lock"))
	c := comparison(t, dir, m.ID)
	result, err := Restore(context.Background(), dir, m.ID, c.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if get(t, dir, "chapters/01.md") != "初稿" || get(t, dir, "meta/versions/"+result.Backup.ID+"/files/chapters/01.md") != "当前版本" {
		t.Fatal("restore or pre-restore backup has incorrect content")
	}
	var project map[string]any
	if err := json.Unmarshal([]byte(get(t, dir, "meta/web-project.json")), &project); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]any{"id": "current-id", "name": "当前名称", "copiedFrom": "current-source", "createdAt": "current-date", "genre": "奇幻", "custom": float64(42)} {
		if project[field] != want {
			t.Fatalf("project %s = %v, want %v", field, project[field], want)
		}
	}
	var run map[string]any
	if err := json.Unmarshal([]byte(get(t, dir, "meta/run.json")), &run); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"pending_steer", "advance_permit_chapter", "advance_hold"} {
		if _, ok := run[field]; ok {
			t.Fatalf("one-time command survived restore: %s", field)
		}
	}
	workbench := run["workbench"].(map[string]any)
	if workbench["batchEnd"] != float64(3) || workbench["approvedFingerprint"] != "reviewed" || run["custom"] != float64(42) {
		t.Fatalf("run state: %+v", run)
	}
	if _, err := os.Stat(filepath.Join(dir, "meta/runtime")); !os.IsNotExist(err) {
		t.Fatalf("old runtime queue survived: %v", err)
	}
	lockAfter, _ := os.Stat(filepath.Join(dir, ".ainovel.lock"))
	if !os.SameFile(lockBefore, lockAfter) || get(t, dir, "logs/current.log") != "keep log" {
		t.Fatal("lease inode or log changed")
	}
	if listed, err := List(dir); err != nil || len(listed) != 2 {
		t.Fatalf("saved backup not listed: %v, %v", listed, err)
	}
}

func TestRestoreRejectsStalePreviewWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "story.md", "snapshot")
	m := snapshot(t, dir, "snapshot")
	c := comparison(t, dir, m.ID)
	put(t, dir, "story.md", "user edited after preview")
	if _, err := Restore(context.Background(), dir, m.ID, c.Fingerprint); !errors.Is(err, ErrChanged) {
		t.Fatalf("expected ErrChanged, got %v", err)
	}
	if get(t, dir, "story.md") != "user edited after preview" {
		t.Fatal("stale restore changed story")
	}
	if listed, err := List(dir); err != nil || len(listed) != 1 {
		t.Fatal("stale restore published a backup")
	}
}

func TestUsageFlushTimestampDoesNotInvalidatePreviewButCountsDo(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "meta/usage.json", `{"schema":2,"updated_at":"before","overall":{"input":1,"cost_usd":0.1}}`)
	m := snapshot(t, dir, "usage")
	preview := comparison(t, dir, m.ID)
	put(t, dir, "meta/usage.json", `{"schema":2,"updated_at":"after","overall":{"input":1,"cost_usd":0.1}}`)
	if after := comparison(t, dir, m.ID); preview.Fingerprint != after.Fingerprint {
		t.Fatal("flush-only timestamp invalidated preview")
	} else if len(after.Changes) != 0 {
		t.Fatalf("flush-only timestamp appeared as a story change: %+v", after.Changes)
	}
	if _, err := Restore(context.Background(), dir, m.ID, preview.Fingerprint); err != nil {
		t.Fatalf("timestamp-only flush rejected restore: %v", err)
	}
	put(t, dir, "meta/usage.json", `{"schema":2,"updated_at":"later","overall":{"input":2,"cost_usd":0.1}}`)
	if _, err := Restore(context.Background(), dir, m.ID, preview.Fingerprint); !errors.Is(err, ErrChanged) {
		t.Fatalf("usage counter change accepted: %v", err)
	}
}

func TestSnapshotPayloadTamperingRejected(t *testing.T) {
	for _, mutation := range []string{"changed", "missing", "unlisted", "manifest-traversal"} {
		t.Run(mutation, func(t *testing.T) {
			dir := t.TempDir()
			put(t, dir, "story.md", "original")
			m := snapshot(t, dir, "safe")
			preview := comparison(t, dir, m.ID)
			base := "meta/versions/" + m.ID
			switch mutation {
			case "changed":
				put(t, dir, base+"/files/story.md", "tampered")
			case "missing":
				if err := os.Remove(filepath.Join(dir, base, "files/story.md")); err != nil {
					t.Fatal(err)
				}
			case "unlisted":
				put(t, dir, base+"/files/unlisted.md", "extra")
			case "manifest-traversal":
				m.Files[0].Path = "../outside.md"
				m.Fingerprint = fingerprint(m.Files)
				data, _ := json.Marshal(m)
				put(t, dir, base+"/manifest.json", string(data))
			}
			if _, err := Compare(context.Background(), dir, m.ID); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("tampered compare: %v", err)
			}
			if _, err := Restore(context.Background(), dir, m.ID, preview.Fingerprint); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("tampered restore: %v", err)
			}
			if get(t, dir, "story.md") != "original" {
				t.Fatal("tampered snapshot changed original")
			}
		})
	}
}

func TestRejectsInvalidIDs(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"../other", "/tmp/other", "", strings.Repeat("A", 32), strings.Repeat("g", 32)} {
		if _, err := Compare(context.Background(), dir, id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("accepted id %q: %v", id, err)
		}
	}
}

func TestRejectsSymlinksIncludingExcludedTrees(t *testing.T) {
	for _, rel := range []string{"story.md", "logs/hidden", "meta/runtime/hidden", "meta/versions/hidden"} {
		t.Run(rel, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(t.TempDir(), "outside.md")
			if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if _, err := Create(context.Background(), dir, "unsafe"); !errors.Is(err, ErrUnsafeFile) {
				t.Fatalf("symlink accepted: %v", err)
			}
		})
	}
}

func TestRejectsSpecialFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets are tested on Unix")
	}
	dir := t.TempDir()
	listener, err := net.Listen("unix", filepath.Join(dir, "socket"))
	if err != nil {
		t.Skipf("Unix socket unavailable: %v", err)
	}
	defer listener.Close()
	if _, err := Create(context.Background(), dir, "unsafe"); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("special file accepted: %v", err)
	}
}

func TestRejectsDirectoryAtLeasePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".ainovel.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), dir, "unsafe lease"); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("directory at lease path accepted: %v", err)
	}
}

func TestRestoreRollsBackEveryPartialRename(t *testing.T) {
	for failureAt := 1; failureAt <= 7; failureAt++ {
		t.Run(fmt.Sprint(failureAt), func(t *testing.T) {
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
			preview := comparison(t, dir, m.ID)
			root, err := openProject(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			calls := 0
			injected := errors.New("injected rename error")
			_, err = restore(context.Background(), root, m.ID, preview.Fingerprint, func(old, new string) error {
				calls++
				if calls == failureAt {
					return injected
				}
				return root.Rename(old, new)
			})
			if !errors.Is(err, injected) {
				t.Fatalf("failure %d did not reach rollback (%d renames): %v", failureAt, calls, err)
			}
			after := comparison(t, dir, m.ID)
			if preview.Fingerprint != after.Fingerprint || get(t, dir, "meta/runtime/queue.jsonl") != "current queue" {
				t.Fatal("rollback did not restore exact current story and runtime")
			}
			versions, err := List(dir)
			if err != nil || len(versions) != 2 {
				t.Fatalf("backup lost after rollback: %v, %v", versions, err)
			}
			entries, err := os.ReadDir(filepath.Join(dir, "meta/versions"))
			if err != nil || len(entries) != 2 {
				t.Fatalf("restore staging leaked: %v, %v", entries, err)
			}
		})
	}
}

func TestCanceledCreationDoesNotPublish(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "story.md", "text")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Create(ctx, dir, "cancel"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled create: %v", err)
	}
	if versions, err := List(dir); err != nil || len(versions) != 0 {
		t.Fatalf("canceled create published: %v, %v", versions, err)
	}
}

func TestCompareBoundsDiffAndChangedFileCount(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "story.md", strings.Repeat("old line\n", 10000))
	m := snapshot(t, dir, "large")
	put(t, dir, "story.md", strings.Repeat("new line\n", 10000))
	for i := 0; i < maxChanges+10; i++ {
		put(t, dir, path.Join("new", fmt.Sprintf("%04d.md", i)), "new")
	}
	c := comparison(t, dir, m.ID)
	if len(c.Changes) != maxChanges || !c.Truncated {
		t.Fatalf("unbounded changes: count=%d truncated=%v", len(c.Changes), c.Truncated)
	}
	total := 0
	for _, change := range c.Changes {
		if len(change.Diff) > maxFileDiff {
			t.Fatal("individual diff exceeded bound")
		}
		total += len(change.Diff)
	}
	if total > maxDiffBytes {
		t.Fatalf("total diffs exceeded bound: %d", total)
	}
}
