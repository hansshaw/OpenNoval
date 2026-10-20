package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/store"
)

func TestProjectSnapshotRejectsBusyHost(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(*Host)
	}{
		{"running", func(h *Host) { h.lifecycle = lifecycleRunning }},
		{"paused-engine-running", func(h *Host) { h.lifecycle = lifecyclePaused; h.engine = &engine{running: true} }},
		{"paused-engine-draining", func(h *Host) { h.lifecycle = lifecyclePaused; h.engine = &engine{draining: true} }},
		{"closing", func(h *Host) { h.closing = true }},
		{"cocreating", func(h *Host) { h.cocreating = true }},
		{"exclusive", func(h *Host) { h.exclusive = "导入" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newModelConfigTestHost(t)
			tc.prepare(h)
			called := false
			err := h.WithProjectSnapshot(func() error { called = true; return nil })
			if !errors.Is(err, ErrProjectSnapshotBusy) || called {
				t.Fatalf("unsafe snapshot was accepted: error=%v callback=%v", err, called)
			}
		})
	}
	h, _ := newModelConfigTestHost(t)
	h.interMu.Lock()
	err := h.WithProjectSnapshot(func() error { t.Error("snapshot ran during an intervention"); return nil })
	h.interMu.Unlock()
	if !errors.Is(err, ErrProjectSnapshotBusy) {
		t.Fatalf("in-flight intervention was accepted: %v", err)
	}
}

func TestProjectSnapshotFlushesUsageAndLocksLifecycle(t *testing.T) {
	h, _ := newModelConfigTestHost(t)
	h.lifecycle = lifecyclePaused
	h.engine = &engine{}
	h.store = store.NewStore(t.TempDir())
	h.usage = NewUsageTracker(h.models, h.store)
	h.usage.overall.Input, h.usage.overall.Output = 17, 9
	want := errors.New("snapshot callback error")
	err := h.WithProjectSnapshot(func() error {
		usage, err := h.store.Usage.Load()
		if err != nil || usage == nil || usage.Overall.Input != 17 || usage.Overall.Output != 9 {
			t.Errorf("usage was not flushed before snapshot: %+v %v", usage, err)
		}
		if h.mu.TryLock() {
			h.mu.Unlock()
			t.Error("lifecycle was not locked during snapshot")
		}
		if h.interMu.TryLock() {
			h.interMu.Unlock()
			t.Error("interventions were not locked during snapshot")
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("callback error was not preserved: %v", err)
	}
	if err := h.WithProjectSnapshot(func() error { return nil }); err != nil {
		t.Fatalf("failed snapshot retained a lock: %v", err)
	}
}

func TestProjectSnapshotRefusesUsageFlushFailure(t *testing.T) {
	h, _ := newModelConfigTestHost(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "meta"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	h.usage = NewUsageTracker(h.models, store.NewStore(dir))
	called := false
	err := h.WithProjectSnapshot(func() error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("snapshot ran without durable usage: error=%v callback=%v", err, called)
	}
	if !h.usage.persistMu.TryLock() {
		t.Fatal("failed usage flush retained the persistence lock")
	}
	h.usage.persistMu.Unlock()
	if err := os.Remove(filepath.Join(dir, "meta")); err != nil {
		t.Fatal(err)
	}
	if err := h.WithProjectSnapshot(func() error { called = true; return nil }); err != nil || !called {
		t.Fatalf("snapshot did not recover after flush failure: callback=%v error=%v", called, err)
	}
}

func TestProjectSnapshotFreezesUsagePersistence(t *testing.T) {
	for _, failCallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "callback-error"}[failCallback], func(t *testing.T) {
			h, _ := newModelConfigTestHost(t)
			h.store = store.NewStore(t.TempDir())
			h.usage = NewUsageTracker(h.models, h.store)
			h.usage.overall.Input = 17
			started, saved := make(chan struct{}), make(chan error, 1)
			want := errors.New("snapshot callback failed")
			err := h.WithProjectSnapshot(func() error {
				if h.usage.persistMu.TryLock() {
					h.usage.persistMu.Unlock()
					t.Error("usage persistence was not locked during snapshot")
				}
				// This later usage must remain in memory until the directory
				// reader completes; a queued autosave uses this same SaveNow path.
				h.usage.mu.Lock()
				h.usage.overall.Input = 42
				h.usage.mu.Unlock()
				go func() {
					close(started)
					saved <- h.usage.SaveNow()
				}()
				<-started
				select {
				case err := <-saved:
					t.Errorf("SaveNow completed during snapshot: %v", err)
					// Preserve the result so the final join never hangs.
					saved <- err
				case <-time.After(30 * time.Millisecond):
				}
				usage, err := h.store.Usage.Load()
				if err != nil || usage == nil || usage.Overall.Input != 17 {
					t.Errorf("snapshot usage was replaced: %+v error=%v", usage, err)
				}
				if failCallback {
					return want
				}
				return nil
			})
			if (failCallback && !errors.Is(err, want)) || (!failCallback && err != nil) {
				t.Errorf("snapshot result = %v", err)
			}
			select {
			case err := <-saved:
				if err != nil {
					t.Fatalf("queued SaveNow failed after snapshot: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("snapshot retained usage persistence lock")
			}
			usage, err := h.store.Usage.Load()
			if err != nil || usage == nil || usage.Overall.Input != 42 {
				t.Fatalf("queued save did not persist latest usage: %+v error=%v", usage, err)
			}
		})
	}
}

func TestProjectSnapshotWithoutUsageStillInvokesCallback(t *testing.T) {
	h, _ := newModelConfigTestHost(t)
	h.usage = nil
	called := false
	if err := h.WithProjectSnapshot(func() error { called = true; return nil }); err != nil || !called {
		t.Fatalf("nil usage suppressed snapshot: callback=%v error=%v", called, err)
	}
}

func TestEngineRemainsBusyUntilExitCleanupCompletes(t *testing.T) {
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	e := &engine{running: true, onDone: func() { close(entered); <-release }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	go func() { defer close(finished); e.run(ctx) }()
	<-entered
	if !e.isRunning() {
		t.Error("engine reported idle before exit cleanup completed")
	}
	if e.start(nil) {
		t.Error("engine restarted before exit cleanup completed")
	}
	h, _ := newModelConfigTestHost(t)
	h.lifecycle, h.engine = lifecyclePaused, e
	if err := h.WithProjectSnapshot(nil); !errors.Is(err, ErrProjectSnapshotBusy) {
		t.Errorf("snapshot accepted an engine still exiting: %v", err)
	}
	close(release)
	<-finished
	if e.isRunning() {
		t.Fatal("engine remained busy after exit cleanup")
	}
}
