package host

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBookLeaseExclusiveAndReusable(t *testing.T) {
	dir := t.TempDir()

	first, err := acquireBookLease(dir)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	second, err := acquireBookLease(dir)
	if second != nil {
		_ = second.Close()
		t.Fatal("second lease unexpectedly acquired")
	}
	if !errors.Is(err, ErrBookInUse) {
		t.Fatalf("second lease error = %v, want ErrBookInUse", err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close first lease: %v", err)
	}
	third, err := acquireBookLease(dir)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	t.Cleanup(func() {
		if err := third.Close(); err != nil {
			t.Errorf("close third lease: %v", err)
		}
	})
}

func TestBookLeaseRefusesDeletionMarkerBeforeOpeningLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ProjectDeletingFile), []byte("deleting\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lease, err := acquireBookLease(dir)
	if lease != nil {
		lease.Close()
		t.Fatal("deleting project received a lease")
	}
	if !errors.Is(err, ErrProjectDeleting) {
		t.Fatalf("deleting project error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, bookLockFile)); !os.IsNotExist(err) {
		t.Fatal("acquire modified a deleting project before refusing")
	}
	if err := os.Remove(filepath.Join(dir, ProjectDeletingFile)); err != nil {
		t.Fatal(err)
	}
	lease, err = acquireBookLease(dir)
	if err != nil {
		t.Fatalf("withdrawn deletion marker still blocked reopening: %v", err)
	}
	lease.Close()
}

func TestBookLeaseAllowsDifferentDirectories(t *testing.T) {
	first, err := acquireBookLease(t.TempDir())
	if err != nil {
		t.Fatalf("acquire first directory: %v", err)
	}
	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Errorf("close first lease: %v", err)
		}
	})

	second, err := acquireBookLease(t.TempDir())
	if err != nil {
		t.Fatalf("acquire second directory: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("close second lease: %v", err)
		}
	})
}

func TestHostCloseReleasesBookLease(t *testing.T) {
	dir := t.TempDir()
	lease, err := acquireBookLease(dir)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	h := &Host{
		bookLease: lease,
		observer:  &observer{},
		engine:    &engine{},
		events:    make(chan Event, 1),
		streamCh:  make(chan string, 1),
		done:      make(chan struct{}, 1),
	}
	h.Close()

	next, err := acquireBookLease(dir)
	if err != nil {
		t.Fatalf("acquire after Host.Close: %v", err)
	}
	t.Cleanup(func() {
		if err := next.Close(); err != nil {
			t.Errorf("close next lease: %v", err)
		}
	})
}
