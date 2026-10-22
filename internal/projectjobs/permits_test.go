package projectjobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultAndLegacyLimitsAllowThreeProjectsWithOneModelRequest(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "legacy"}[legacy], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jobs.json")
			if legacy {
				if err := os.WriteFile(path, []byte(`{"version":1,"next_sequence":1,"limits":{"maxConcurrent":2,"providerLimit":1,"providerLimits":{}}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			calls := make(chan runCall, 4)
			// Use New directly: the old tests' helper explicitly sizes project
			// capacity to their individual scenarios rather than testing defaults.
			s, err := New(Config{MaxConcurrent: 1, DefaultProviderLimit: 1, PersistencePath: path}, controlledRunner(calls, true))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close(context.Background()) })
			limits, err := s.Limits(context.Background())
			if err != nil || limits.MaxProjects != 3 || limits.ProviderLimit != 1 {
				t.Fatalf("project default or legacy migration lost: %+v %v", limits, err)
			}
			for _, project := range []string{"A", "B", "C"} {
				submitJob(t, s, project, "same-connection")
				nextRun(t, calls)
			}
			fourth := submitJob(t, s, "D", "same-connection")
			awaitStatus(t, s, fourth.ID, Queued)
			if err := s.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			j, err := loadJournal(path)
			if err != nil || j.Limits == nil || j.Limits.MaxProjects != 3 {
				t.Fatalf("three-project migration not durable: %+v %v", j.Limits, err)
			}
		})
	}
}

func TestForegroundReservationSharesSlotsButNotBackgroundBusy(t *testing.T) {
	calls := make(chan runCall, 8)
	s := newScheduler(t, Config{MaxProjects: 3, MaxConcurrent: 1}, controlledRunner(calls, true))
	ctx, cancel := context.WithCancel(context.Background())
	release, err := s.ReserveProject(ctx, "foreground")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if busy, _ := s.Busy(context.Background(), ""); !busy {
		t.Fatal("foreground reservation missing from global busy")
	}
	if busy, _ := s.JobsBusy(context.Background(), "foreground"); busy {
		t.Fatal("foreground reservation was reported as a background job")
	}
	if _, err := s.ReserveProject(context.Background(), "foreground"); !errors.Is(err, ErrProjectBusy) {
		t.Fatalf("duplicate project reservation accepted: %v", err)
	}
	ownQueued := submitJob(t, s, "foreground", "one")
	for _, project := range []string{"B", "C"} {
		submitJob(t, s, project, "one")
		nextRun(t, calls)
	}
	fourth := submitJob(t, s, "D", "one")
	awaitStatus(t, s, ownQueued.ID, Queued)
	awaitStatus(t, s, fourth.ID, Queued)
	if _, err := s.ReserveProject(context.Background(), "E"); !errors.Is(err, ErrProjectLimit) {
		t.Fatalf("foreground reservation bypassed total project limit: %v", err)
	}
	cancel() // Host cleanup has not returned; the reservation must remain held.
	awaitStatus(t, s, fourth.ID, Queued)
	release()
	release() // Idempotent release must not create another free slot.
	if nextRun(t, calls).job.ID != ownQueued.ID {
		t.Fatal("foreground release did not dispatch its queued successor")
	}
	awaitStatus(t, s, fourth.ID, Queued)
	if _, err := s.ReserveProject(context.Background(), "D"); !errors.Is(err, ErrProjectBusy) {
		t.Fatalf("foreground overtook queued work for its own project: %v", err)
	}
}

func TestModelPermitsHaveIndependentLimitsAndFollowRuntimeChanges(t *testing.T) {
	s := newScheduler(t, Config{MaxProjects: 3, MaxConcurrent: 1, DefaultProviderLimit: 1}, RunnerFunc(func(context.Context, Job, Reporter) error { return nil }))
	first, err := s.AcquireModel(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	waiting := make(chan func(), 1)
	failure := make(chan error, 1)
	go func() {
		release, err := s.AcquireModel(context.Background(), "one")
		if err != nil {
			failure <- err
			return
		}
		waiting <- release
	}()
	if _, err := s.SetLimits(context.Background(), Limits{MaxProjects: 3, MaxConcurrent: 3, ProviderLimit: 2}); err != nil {
		t.Fatal(err)
	}
	var second func()
	select {
	case second = <-waiting:
	case err := <-failure:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("raising model limits did not wake a waiting real request")
	}
	defer second()
	other, err := s.AcquireModel(context.Background(), "two")
	if err != nil {
		t.Fatal(err)
	}
	defer other()
	// There are now three model requests, but no project execution slot is used.
	if busy, _ := s.Busy(context.Background(), ""); busy {
		t.Fatal("model permits incorrectly consumed project execution slots")
	}
	if _, err := s.SetLimits(context.Background(), Limits{MaxProjects: 1, MaxConcurrent: 1, ProviderLimit: 1}); err != nil {
		t.Fatal(err)
	}
	second()
	other()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.AcquireModel(ctx, "two"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lowering global model limit released in-flight requests early: %v", err)
	}
	first()
	first()
	next, err := s.AcquireModel(context.Background(), "two")
	if err != nil {
		t.Fatal(err)
	}
	next()
}

func TestClosingSchedulerWakesQueuedModelRequests(t *testing.T) {
	s := newScheduler(t, Config{MaxConcurrent: 1}, RunnerFunc(func(context.Context, Job, Reporter) error { return nil }))
	release, err := s.AcquireModel(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	waiting := make(chan error, 1)
	go func() {
		_, err := s.AcquireModel(context.Background(), "one")
		waiting <- err
	}()
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waiting:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("closed scheduler returned wrong model permit error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("closing did not wake model request waiters")
	}
	if _, err := s.ReserveProject(context.Background(), "other"); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed scheduler accepted project reservation: %v", err)
	}
}
