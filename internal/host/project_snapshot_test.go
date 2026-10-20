package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
