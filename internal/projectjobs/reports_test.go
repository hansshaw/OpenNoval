package projectjobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func holdActor(t *testing.T, s *Scheduler) func() {
	t.Helper()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		_ = s.call(context.Background(), func(*state) error { close(entered); <-release; return nil })
	}()
	unblock := func() { once.Do(func() { close(release) }); <-done }
	t.Cleanup(unblock)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("actor did not reach test barrier")
	}
	return unblock
}

func awaitQueuedReports(t *testing.T, s *Scheduler, count int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for len(s.reports) < count {
		select {
		case <-deadline:
			t.Fatalf("only %d of %d reports reached the bounded queue", len(s.reports), count)
		default:
			runtime.Gosched()
		}
	}
}

func TestConcurrentReportsAreDurableBeforeEachReply(t *testing.T) {
	const count = maxReportBatch
	path := filepath.Join(t.TempDir(), "jobs.json")
	startCosts, startEvents := make(chan struct{}), make(chan struct{})
	ready := make(chan struct{}, count)
	costResults, eventResults := make(chan error, count), make(chan error, count)
	runner := RunnerFunc(func(ctx context.Context, job Job, report Reporter) error {
		ready <- struct{}{}
		select {
		case <-startCosts:
		case <-ctx.Done():
			return ctx.Err()
		}
		costResults <- func() error {
			if err := report.RecordCost(0.125); err != nil {
				return err
			}
			persisted, err := loadJournal(path)
			if err != nil {
				return err
			}
			found := false
			for _, stored := range persisted.Jobs {
				if stored.ID == job.ID && stored.CostUSD == 0.125 {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("job %s received a successful cost reply before durable accounting", job.ID)
			}
			return nil
		}()
		select {
		case <-startEvents:
		case <-ctx.Done():
			return ctx.Err()
		}
		eventResults <- func() error {
			if err := report.Emit("progress", job.ID, nil); err != nil {
				return err
			}
			persisted, err := loadJournal(path)
			if err != nil {
				return err
			}
			for _, event := range persisted.Events {
				if event.JobID == job.ID && event.Kind == "progress" && event.Message == job.ID && event.ProjectID == job.ProjectID {
					return nil
				}
			}
			return fmt.Errorf("job %s received a successful event reply before durable event storage", job.ID)
		}()
		<-ctx.Done()
		return ctx.Err()
	})
	s := newScheduler(t, Config{MaxProjects: count, MaxConcurrent: count, PersistencePath: path}, runner)
	for i := 0; i < count; i++ {
		submitJob(t, s, fmt.Sprint(i), "one")
	}
	for i := 0; i < count; i++ {
		select {
		case <-ready:
		case <-time.After(10 * time.Second):
			t.Fatal("durable burst did not start every Runner")
		}
	}
	// Put a complete batch in the actor's queue before each phase. Every
	// successful reply is still followed immediately by an independent disk
	// read, and no later persist can hide an early reply. Keeping Runners active
	// and waiting for every reader before the next phase also avoids Windows'
	// ordinary os.Open handles conflicting with a subsequent atomic Rename.
	for _, phase := range []struct {
		start   chan struct{}
		results chan error
	}{
		{startCosts, costResults},
		{startEvents, eventResults},
	} {
		unblock := holdActor(t, s)
		close(phase.start)
		awaitQueuedReports(t, s, count)
		unblock()
		for i := 0; i < count; i++ {
			select {
			case err := <-phase.results:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("durable burst did not acknowledge every report")
			}
		}
		if t.Failed() {
			return
		}
	}
}

func TestFailedReportBatchCancelsActiveAndRetainsEveryActualCharge(t *testing.T) {
	const count = 64
	path := filepath.Join(t.TempDir(), "jobs.json")
	start := make(chan struct{})
	ready := make(chan struct{}, count)
	results := make(chan error, count)
	s := newScheduler(t, Config{MaxProjects: count, MaxConcurrent: count, PersistencePath: path}, RunnerFunc(func(ctx context.Context, job Job, report Reporter) error {
		ready <- struct{}{}
		select {
		case <-start:
		case <-ctx.Done():
			return ctx.Err()
		}
		err := report.RecordCost(0.25)
		results <- err
		<-ctx.Done()
		return ctx.Err()
	}))
	t.Cleanup(func() {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			_ = os.Remove(path)
		}
	})
	for i := 0; i < count; i++ {
		submitJob(t, s, fmt.Sprint(i), "one")
	}
	for i := 0; i < count; i++ {
		<-ready
	}
	unblock := holdActor(t, s)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	close(start)
	awaitQueuedReports(t, s, count)
	unblock()
	for i := 0; i < count; i++ {
		select {
		case err := <-results:
			if err == nil {
				t.Fatal("failed batch returned a durable success")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("failed batch stranded a reporter")
		}
	}
	// No actor write may race the fault repair. The next successful Sync must
	// contain the actual charges retained in memory even though fsync failed.
	unlockRepair := holdActor(t, s)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	unlockRepair()
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	persisted, err := loadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range persisted.Jobs {
		if job.CostUSD != 0.25 || job.Status != Canceled {
			t.Fatalf("failed batch lost an actual charge or failed to cancel its Runner: %+v", job)
		}
	}
}

func TestReportBatchPreservesPerReportBudgetErrorsAndLateCharges(t *testing.T) {
	const count = 64
	path := filepath.Join(t.TempDir(), "jobs.json")
	calls := make(chan runCall, 1)
	cleanup := make(chan struct{})
	s := newScheduler(t, Config{MaxConcurrent: 1, PersistencePath: path}, RunnerFunc(func(ctx context.Context, job Job, report Reporter) error {
		call := runCall{job: job, ctx: ctx, report: report, release: make(chan error, 1)}
		calls <- call
		select {
		case err := <-call.release:
			return err
		case <-cleanup:
			return nil
		}
	}))
	t.Cleanup(func() { close(cleanup) })
	job, err := s.Submit(context.Background(), Request{ProjectID: "A", ProviderID: "one", Kind: "batch", BudgetUSD: 8})
	if err != nil {
		t.Fatal(err)
	}
	call := nextRun(t, calls)
	unblock := holdActor(t, s)
	results := make(chan error, count)
	for i := 0; i < count; i++ {
		go func() { results <- call.report.RecordCost(1) }()
	}
	missing := make(chan error, 1)
	go func() { missing <- (reporter{scheduler: s, id: "missing"}).Emit("progress", "rejected", nil) }()
	awaitQueuedReports(t, s, count+1)
	unblock()
	budgetErrors := 0
	for i := 0; i < count; i++ {
		select {
		case err := <-results:
			if errors.Is(err, ErrBudgetExceeded) {
				budgetErrors++
			} else if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("budget batch stranded a reporter")
		}
	}
	if err := <-missing; !errors.Is(err, ErrNotRunning) {
		t.Fatalf("another report's validation error was lost: %v", err)
	}
	if budgetErrors != count-7 {
		t.Fatalf("budget limit returned the wrong per-report results: %d", budgetErrors)
	}
	current := awaitStatus(t, s, job.ID, Canceling)
	if current.CostUSD != count || context.Cause(call.ctx) != ErrBudgetExceeded {
		t.Fatalf("batch discarded already incurred charges: %+v / %v", current, context.Cause(call.ctx))
	}
	persisted, err := loadJournal(path)
	if err != nil || persisted.Jobs[0].CostUSD != count {
		t.Fatal("final late charges were not durable", err)
	}
	call.release <- nil
	awaitStatus(t, s, job.ID, Canceled)
}

func TestClosingSchedulerDoesNotStrandBufferedOrBlockedReporters(t *testing.T) {
	calls := make(chan runCall, 1)
	s := newScheduler(t, Config{MaxConcurrent: 1}, controlledRunner(calls, true))
	submitJob(t, s, "A", "one")
	call := nextRun(t, calls)
	unblock := holdActor(t, s)
	const count = 512
	results := make(chan error, count)
	for i := 0; i < count; i++ {
		go func() { results <- call.report.Emit("progress", "late", nil) }()
	}
	awaitQueuedReports(t, s, cap(s.reports))
	closed := make(chan error, 1)
	go func() { closed <- s.Close(context.Background()) }()
	unblock()
	for i := 0; i < count; i++ {
		select {
		case err := <-results:
			if err != nil && !errors.Is(err, ErrClosed) && !errors.Is(err, ErrNotRunning) {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("close stranded a buffered or backpressured reporter")
		}
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("report burst starved shutdown")
	}
}
