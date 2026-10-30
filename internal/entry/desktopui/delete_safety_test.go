package desktopui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
	"github.com/hansshaw/OpenNovel/assets"
	"github.com/hansshaw/OpenNovel/internal/host"
)

func TestDeleteWorkspaceRefusesForeignLeaseWithoutTouchingFiles(t *testing.T) {
	s := newTestServer(t, false)
	target := filepath.Join(s.booksDir, "leased-delete")
	if err := saveProject(target, projectInfo{Name: "已归档但仍被其他实例打开", Archived: true}); err != nil {
		t.Fatal(err)
	}
	story := filepath.Join(target, "story.md")
	if err := os.WriteFile(story, []byte("未完成的正文"), 0600); err != nil {
		t.Fatal(err)
	}
	lease := flock.New(filepath.Join(target, ".ainovel.lock"))
	locked, err := lease.TryLock()
	if err != nil || !locked {
		t.Fatalf("foreign lease: %v, %v", locked, err)
	}
	defer lease.Close()
	before, err := lease.Stat()
	if err != nil {
		t.Fatal(err)
	}
	body := mustJSON(t, map[string]any{"directory": target, "confirmed": true})
	w := request(s, "POST", "/api/workspaces/delete", body)
	if w.Code != 409 {
		t.Fatalf("deleted a leased project: %d %s", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(story)
	if err != nil || string(data) != "未完成的正文" {
		t.Fatalf("leased story was changed: %q, %v", data, err)
	}
	after, err := os.Stat(filepath.Join(target, ".ainovel.lock"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("lease inode was replaced: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	w = request(s, "POST", "/api/workspaces/delete", body)
	if w.Code != 200 {
		t.Fatalf("unleased archived project could not be deleted: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("deleted directory still exists: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(s.booksDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".ainovel-delete-") {
			t.Fatalf("successful cleanup leaked a tombstone: %s", entry.Name())
		}
	}
}

func TestDeleteRelocationClosesLeaseButMarkerRejectsNewWriters(t *testing.T) {
	s := newTestServer(t, true)
	target := filepath.Join(s.booksDir, "marker-delete")
	if err := saveProject(target, projectInfo{Name: "待删除", Archived: true}); err != nil {
		t.Fatal(err)
	}
	tombstone, err := stageWorkspaceDeletion(target, s.booksDir, func(old, new string) error {
		// Raw flock succeeds because the delete operation has closed its handle.
		// This is required for Windows to rename the containing directory.
		probe := flock.New(filepath.Join(old, ".ainovel.lock"))
		locked, err := probe.TryLock()
		if err != nil || !locked {
			t.Fatalf("deletion retained its lease handle: %v %v", locked, err)
		}
		probe.Close()
		cfg := s.baseCfg
		cfg.OutputDir = old
		if rt, err := host.New(cfg, assets.Bundle{}); !errors.Is(err, host.ErrProjectDeleting) {
			if rt != nil {
				rt.Close()
			}
			t.Fatalf("new Host bypassed deletion marker: %v", err)
		}
		if err := withWorkspaceLease(old, func() error { t.Fatal("workspace lease bypassed deletion marker"); return nil }); !errors.Is(err, host.ErrProjectDeleting) {
			t.Fatalf("workspace lease accepted marked project: %v", err)
		}
		return os.Rename(old, new)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tombstone)
	if filepath.Dir(tombstone) != filepath.Dir(s.booksDir) || filepath.Dir(tombstone) == s.booksDir {
		t.Fatal("tombstone was placed in discoverable project directory")
	}
}

func TestDeleteFailedRelocationWithdrawsMarkerUnderLease(t *testing.T) {
	s := newTestServer(t, false)
	target := filepath.Join(s.booksDir, "failed-relocation")
	if err := saveProject(target, projectInfo{Name: "待删除", Archived: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "story.md"), []byte("keep original"), 0600); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected rename failure")
	if _, err := stageWorkspaceDeletion(target, s.booksDir, func(_, _ string) error { return injected }); !errors.Is(err, injected) {
		t.Fatalf("wrong relocation error: %v", err)
	}
	if err := host.CheckProjectDeleting(target); err != nil {
		t.Fatalf("failed relocation left a blocking marker: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(target, "story.md")); err != nil || string(data) != "keep original" {
		t.Fatal("failed relocation changed original story")
	}
	if err := withWorkspaceLease(target, func() error { return nil }); err != nil {
		t.Fatalf("failed relocation did not release original project: %v", err)
	}
}

func TestDeleteCanRetryAfterInterruptedMarkerPublication(t *testing.T) {
	s := newTestServer(t, false)
	target := filepath.Join(s.booksDir, "interrupted-delete")
	if err := saveProject(target, projectInfo{Name: "重试删除", Archived: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, host.ProjectDeletingFile), []byte("stale deletion marker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w := request(s, "POST", "/api/workspaces/delete", mustJSON(t, map[string]any{"directory": target, "confirmed": true}))
	if w.Code != 200 {
		t.Fatalf("interrupted deletion could not be retried: %d %s", w.Code, w.Body.String())
	}
}

func TestDeleteCoordinatesRetryAcrossInstances(t *testing.T) {
	s := newTestServer(t, false)
	target := filepath.Join(s.booksDir, "concurrent-delete")
	if err := saveProject(target, projectInfo{Name: "正在删除", Archived: true}); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, host.ProjectDeletingFile)
	if err := os.WriteFile(marker, []byte("other instance marker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	coordinator := flock.New(filepath.Join(filepath.Dir(s.booksDir), ".ainovel-delete.lock"))
	locked, err := coordinator.TryLock()
	if err != nil || !locked {
		t.Fatalf("foreign deletion coordinator: %v, %v", locked, err)
	}
	defer coordinator.Close()
	if _, err := stageWorkspaceDeletion(target, s.booksDir, os.Rename); !errors.Is(err, host.ErrProjectSnapshotBusy) {
		t.Fatalf("second instance bypassed deletion coordinator: %v", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "other instance marker\n" {
		t.Fatal("second deleter overwrote an active marker")
	}
}

func TestDeleteWorkspaceRejectsUnsafeLeaseFile(t *testing.T) {
	s := newTestServer(t, false)
	target := filepath.Join(s.booksDir, "unsafe-delete")
	if err := saveProject(target, projectInfo{Name: "不安全的锁", Archived: true}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(target, ".ainovel.lock")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	w := request(s, "POST", "/api/workspaces/delete", mustJSON(t, map[string]any{"directory": target, "confirmed": true}))
	if w.Code != 409 {
		t.Fatalf("unsafe lock accepted: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("rejected deletion moved original project")
	}
	if value, err := os.ReadFile(outside); err != nil || string(value) != "keep outside" {
		t.Fatal("unsafe lease touched outside file")
	}
}
