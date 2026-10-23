package host

import (
	"errors"
	"testing"
)

func attachmentHost(t *testing.T) (*Host, string) {
	t.Helper()
	directory := t.TempDir()
	lease, err := acquireBookLease(directory)
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{bookLease: lease, engine: &engine{}, events: make(chan Event, 1), streamCh: make(chan string, 1), done: make(chan struct{}, 1)}
	t.Cleanup(h.Close)
	return h, directory
}

func TestAttachmentWriteKeepsLiveLeaseWhileGenerationRuns(t *testing.T) {
	h, directory := attachmentHost(t)
	h.lifecycle = lifecycleRunning
	called := false
	failure := errors.New("attachment save failed")
	err := h.WithProjectAttachmentWrite(func() error {
		called = true
		if h.mu.TryLock() {
			h.mu.Unlock()
			t.Fatal("attachment write did not hold the lifecycle lock")
		}
		if lease, err := acquireBookLease(directory); !errors.Is(err, ErrBookInUse) {
			if lease != nil {
				lease.Close()
			}
			t.Fatalf("project lease was not retained: %v", err)
		}
		return failure
	})
	if !called || !errors.Is(err, failure) {
		t.Fatalf("running generation blocked independent media: called=%v err=%v", called, err)
	}
	if err := h.WithProjectAttachmentWrite(func() error { return nil }); err != nil {
		t.Fatalf("callback failure retained the lock: %v", err)
	}
	h.Close()
	if err := h.WithProjectAttachmentWrite(func() error { t.Error("write after Close"); return nil }); !errors.Is(err, ErrProjectSnapshotBusy) {
		t.Fatalf("closed host permitted write: %v", err)
	}
}

func TestAttachmentWriteFinishesBeforeCloseReleasesProject(t *testing.T) {
	h, directory := attachmentHost(t)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- h.WithProjectAttachmentWrite(func() error { close(entered); <-release; return nil })
	}()
	<-entered
	closing, closed := make(chan struct{}), make(chan struct{})
	go func() { close(closing); h.Close(); close(closed) }()
	<-closing
	if lease, err := acquireBookLease(directory); !errors.Is(err, ErrBookInUse) {
		if lease != nil {
			lease.Close()
		}
		t.Errorf("lease released during attachment write: %v", err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	<-closed
	lease, err := acquireBookLease(directory)
	if err != nil {
		t.Fatalf("Close did not release lease: %v", err)
	}
	lease.Close()
}

func TestAttachmentWriteRequiresARealProjectLease(t *testing.T) {
	h := &Host{}
	if err := h.WithProjectAttachmentWrite(func() error { t.Error("unleased write"); return nil }); !errors.Is(err, ErrProjectSnapshotBusy) {
		t.Fatalf("missing lease accepted: %v", err)
	}
}
