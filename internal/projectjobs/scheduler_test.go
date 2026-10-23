package projectjobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type runCall struct {
	job     Job
	ctx     context.Context
	report  Reporter
	release chan error
}

func controlledRunner(calls chan<- runCall, cooperate bool) Runner {
	return RunnerFunc(func(ctx context.Context, job Job, report Reporter) error {
		call := runCall{job: job, ctx: ctx, report: report, release: make(chan error, 1)}
		calls <- call
		if cooperate {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case err := <-call.release:
				return err
			}
		}
		return <-call.release
	})
}

func newScheduler(t *testing.T, cfg Config, runner Runner) *Scheduler {
	t.Helper()
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = 2
	}
	if cfg.MaxProjects == 0 {
		cfg.MaxProjects = cfg.MaxConcurrent
	}
	s, err := New(cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Burst tests leave 64 active runners whose terminal states are persisted
		// individually. Allow their fsyncs and renames to drain on slow Windows
		// filesystems; shutdown deadline behavior has its own focused assertions.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}

func submitJob(t *testing.T, s *Scheduler, project, provider string) Job {
	t.Helper()
	job, err := s.Submit(context.Background(), Request{ProjectID: project, ProviderID: provider, Kind: "batch"})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func nextRun(t *testing.T, calls <-chan runCall) runCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not start")
		return runCall{}
	}
}

func awaitStatus(t *testing.T, s *Scheduler, id string, want Status) Job {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		job, err := s.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == want {
			return job
		}
		select {
		case <-deadline:
			t.Fatalf("job %s status = %s, want %s", id, job.Status, want)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestProjectSlotsIgnoreModelQuotasAndKeepProjectFIFO(t *testing.T) {
	calls := make(chan runCall, 16)
	s := newScheduler(t, Config{MaxConcurrent: 3, DefaultProviderLimit: 1, ProviderLimits: map[string]int{"one": 1}}, controlledRunner(calls, true))
	a := submitJob(t, s, "A", "one")
	ra := nextRun(t, calls)
	b := submitJob(t, s, "B", "one")
	rb := nextRun(t, calls)
	c := submitJob(t, s, "C", "one")
	rc := nextRun(t, calls)
	a2 := submitJob(t, s, "A", "two")
	d := submitJob(t, s, "D", "two")
	e := submitJob(t, s, "E", "three")
	for _, job := range []Job{a2, d, e} {
		awaitStatus(t, s, job.ID, Queued)
	}
	if ra.job.ID != a.ID || rb.job.ID != b.ID || rc.job.ID != c.ID {
		t.Fatal("model quotas incorrectly blocked project dispatch")
	}
	rb.release <- nil
	if nextRun(t, calls).job.ID != d.ID {
		t.Fatal("active project blocked independent queued project")
	}
	awaitStatus(t, s, a2.ID, Queued)
	ra.release <- nil
	if nextRun(t, calls).job.ID != a2.ID {
		t.Fatal("same-project successor lost FIFO order")
	}
	awaitStatus(t, s, e.ID, Queued)
	if err := s.StopAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, s, e.ID, Canceled)
}

func TestQueuedProjectCannotOvertakeItsEarlierJob(t *testing.T) {
	calls := make(chan runCall, 16)
	s := newScheduler(t, Config{MaxConcurrent: 1}, controlledRunner(calls, true))
	submitJob(t, s, "occupied", "one")
	running := nextRun(t, calls)
	first := submitJob(t, s, "waiting", "one")
	second := submitJob(t, s, "waiting", "two")
	submitJob(t, s, "independent", "two")
	awaitStatus(t, s, second.ID, Queued)
	running.release <- nil
	next := nextRun(t, calls)
	if next.job.ID != first.ID {
		t.Fatal("later job overtook earlier job")
	}
	awaitStatus(t, s, second.ID, Queued)
	next.release <- nil
	if nextRun(t, calls).job.ID != second.ID {
		t.Fatal("same-project FIFO did not preserve submission order")
	}
}

func TestCancelQueuedAndRunningRetainsSlotUntilRunnerReturns(t *testing.T) {
	calls := make(chan runCall, 16)
	s := newScheduler(t, Config{MaxConcurrent: 1}, controlledRunner(calls, false))
	a := submitJob(t, s, "A", "one")
	running := nextRun(t, calls)
	b := submitJob(t, s, "B", "two")
	if err := s.Cancel(context.Background(), b.ID); err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, s, b.ID, Canceled)
	c := submitJob(t, s, "A", "one")
	if err := s.Cancel(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, s, a.ID, Canceling)
	awaitStatus(t, s, c.ID, Queued)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.StopAll(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StopAll did not wait for runner: %v", err)
	}
	// Reads and cancellation still work while another goroutine waits for exit.
	awaitStatus(t, s, c.ID, Canceled)
	if context.Cause(running.ctx) != ErrCanceled {
		t.Fatal("runner did not receive cancellation")
	}
	running.release <- nil
	awaitStatus(t, s, a.ID, Canceled)
	if err := s.Cancel(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing cancel = %v", err)
	}
	if err := s.Cancel(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetsRecordFinalChargesAndRejectFurtherWork(t *testing.T) {
	calls := make(chan runCall, 16)
	s := newScheduler(t, Config{ProjectBudgetsUSD: map[string]float64{"A": 1}}, controlledRunner(calls, false))
	_, err := s.Submit(context.Background(), Request{ProjectID: "A", ProviderID: "one", Kind: "batch", EstimatedCostUSD: 2})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("over-budget estimate = %v", err)
	}
	a := submitJob(t, s, "A", "one")
	running := nextRun(t, calls)
	queued := submitJob(t, s, "A", "one")
	if err := running.report.RecordCost(0.4); err != nil {
		t.Fatal(err)
	}
	if err := running.report.RecordCost(0.7); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("budget crossing = %v", err)
	}
	awaitStatus(t, s, queued.ID, Canceled)
	awaitStatus(t, s, a.ID, Canceling)
	if err := running.report.RecordCost(0.2); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal("late actual charge was rejected instead of recorded")
	}
	running.release <- nil
	final := awaitStatus(t, s, a.ID, Canceled)
	if math.Abs(final.CostUSD-1.3) > 1e-9 || final.Error != ErrBudgetExceeded.Error() {
		t.Fatalf("wrong cost/cause: %+v", final)
	}
	_, err = s.Submit(context.Background(), Request{ProjectID: "A", ProviderID: "one", Kind: "batch"})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("exhausted project accepted another job: %v", err)
	}
	if err := running.report.RecordCost(1); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("terminal reporter accepted cost: %v", err)
	}
}

func TestPerTaskBudgetDoesNotCancelOtherProjects(t *testing.T) {
	calls := make(chan runCall, 16)
	s := newScheduler(t, Config{}, controlledRunner(calls, true))
	a, err := s.Submit(context.Background(), Request{ProjectID: "A", ProviderID: "one", Kind: "batch", BudgetUSD: 0.1})
	if err != nil {
		t.Fatal(err)
	}
	ra := nextRun(t, calls)
	b := submitJob(t, s, "B", "one")
	rb := nextRun(t, calls)
	if err := ra.report.RecordCost(0.1); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal(err)
	}
	awaitStatus(t, s, a.ID, Canceled)
	awaitStatus(t, s, b.ID, Running)
	rb.release <- nil
	awaitStatus(t, s, b.ID, Succeeded)
}

func TestEventsOwnershipRetentionAndImmutableCopies(t *testing.T) {
	calls := make(chan runCall, 16)
	s := newScheduler(t, Config{MaxEvents: 8}, controlledRunner(calls, true))
	payload := json.RawMessage(`{"value":1}`)
	job, err := s.Submit(context.Background(), Request{ProjectID: "A", ProviderID: "one", Kind: "batch", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	run := nextRun(t, calls)
	payload[9] = '2'
	job.Payload[9] = '3'
	run.job.Payload[9] = '4'
	loaded, _ := s.Get(context.Background(), job.ID)
	if string(loaded.Payload) != `{"value":1}` {
		t.Fatal("request/runner/caller shared mutable payload")
	}
	for i := 0; i < 12; i++ {
		if err := run.report.Emit("chapter", fmt.Sprint(i), payload); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.Events(context.Background(), 0)
	if err != nil || len(page.Events) != 8 || !page.Truncated {
		t.Fatalf("invalid retention: %+v / %v", page, err)
	}
	for _, event := range page.Events {
		if event.JobID != job.ID || event.ProjectID != "A" || event.ProviderID != "one" {
			t.Fatalf("event ownership lost: %+v", event)
		}
	}
	page.Events[0].Payload[9] = '9'
	page2, _ := s.Events(context.Background(), 0)
	if page2.Events[0].Payload[9] == '9' {
		t.Fatal("events shared mutable payload")
	}
	empty, _ := s.Events(context.Background(), page.AfterSequence)
	if len(empty.Events) != 0 || empty.Truncated {
		t.Fatal("cursor replay duplicated events")
	}
	if err := run.report.Emit("job.status", "fake", nil); err == nil {
		t.Fatal("runner could impersonate scheduler events")
	}
}

func TestRestartKeepsQueuedAndAbortsInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	now := time.Now().UTC()
	j := journal{Version: 1, NextSequence: 1}
	for _, status := range []Status{Running, Canceling, Queued, Succeeded} {
		j.Jobs = append(j.Jobs, Job{ID: string(status), Request: Request{ProjectID: string(status), ProviderID: "one", Kind: "batch"}, Status: status, CreatedAt: now, CostUSD: 0.25})
	}
	if err := saveJournal(path, j); err != nil {
		t.Fatal(err)
	}
	calls := make(chan runCall, 16)
	s := newScheduler(t, Config{PersistencePath: path}, controlledRunner(calls, true))
	if nextRun(t, calls).job.ID != "queued" {
		t.Fatal("restart reran an interrupted job")
	}
	for _, id := range []string{"running", "canceling"} {
		job := awaitStatus(t, s, id, Aborted)
		if job.Error != ErrInterrupted.Error() || job.CostUSD != 0.25 {
			t.Fatal("restart lost cost or interruption reason")
		}
	}
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	persisted, err := loadJournal(path)
	if err != nil || persisted.Jobs[0].Status != Aborted {
		t.Fatalf("recovery was not durable: %+v / %v", persisted, err)
	}
}

func TestGracefulClosePreservesQueuedAndRestartDoesNotRerunActiveJob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	calls := make(chan runCall, 4)
	s := newScheduler(t, Config{MaxConcurrent: 1, PersistencePath: path}, controlledRunner(calls, true))
	a := submitJob(t, s, "A", "one")
	running := nextRun(t, calls)
	if err := running.report.RecordCost(0.25); err != nil {
		t.Fatal(err)
	}
	b := submitJob(t, s, "B", "one")
	awaitStatus(t, s, b.ID, Queued)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	persisted, err := loadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Jobs) != 2 || persisted.Jobs[0].Status != Aborted || persisted.Jobs[0].CostUSD != 0.25 || persisted.Jobs[1].Status != Queued {
		t.Fatalf("graceful shutdown lost queued work or active-job cost: %+v", persisted.Jobs)
	}
	restartedCalls := make(chan runCall, 4)
	restarted := newScheduler(t, Config{MaxConcurrent: 1, PersistencePath: path}, controlledRunner(restartedCalls, true))
	if nextRun(t, restartedCalls).job.ID != b.ID {
		t.Fatal("restart reran active work instead of continuing the preserved queue")
	}
	awaitStatus(t, restarted, a.ID, Aborted)
	awaitStatus(t, restarted, b.ID, Running)
}

func TestPersistenceFailureNeverExecutesFailedSubmission(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	calls := make(chan runCall, 1)
	s := newScheduler(t, Config{PersistencePath: filepath.Join(blocker, "jobs.json")}, controlledRunner(calls, true))
	if err := os.WriteFile(blocker, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	job, err := s.Submit(context.Background(), Request{ProjectID: "A", ProviderID: "one", Kind: "batch"})
	if err == nil || job.ID != "" || s.Health(context.Background()) == nil {
		t.Fatal("persistence error was not surfaced")
	}
	jobs, _ := s.List(context.Background(), "")
	if len(jobs) != 0 {
		t.Fatal("failed submission remained eligible to run")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	submitJob(t, s, "B", "one")
	if nextRun(t, calls).job.ProjectID != "B" {
		t.Fatal("failed submission executed after persistence recovered")
	}
}

func TestCloseWaitsForRunnerEvenWhenJournalWriteFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	path := filepath.Join(dir, "jobs.json")
	calls := make(chan runCall, 1)
	s := newScheduler(t, Config{PersistencePath: path}, controlledRunner(calls, false))
	submitJob(t, s, "A", "one")
	running := nextRun(t, calls)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("blocks journal writes"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close returned before runner exited after persistence error: %v", err)
	}
	if context.Cause(running.ctx) == nil {
		t.Fatal("failed persistence did not stop active execution")
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	running.release <- nil
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	persisted, err := loadJournal(path)
	if err != nil || persisted.Jobs[0].Status != Aborted {
		t.Fatalf("close did not save the final interrupted state: %+v / %v", persisted, err)
	}
}

func TestDefaultProviderQuotaAppliesToRealRequestsAddedLater(t *testing.T) {
	s := newScheduler(t, Config{MaxConcurrent: 3, DefaultProviderLimit: 1}, RunnerFunc(func(context.Context, Job, Reporter) error { return nil }))
	release, err := s.AcquireModel(context.Background(), "new-provider")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.AcquireModel(ctx, "new-provider"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("new provider bypassed its default request quota: %v", err)
	}
	other, err := s.AcquireModel(context.Background(), "other-provider")
	if err != nil {
		t.Fatal(err)
	}
	defer other()
	release()
	next, err := s.AcquireModel(context.Background(), "new-provider")
	if err != nil {
		t.Fatal(err)
	}
	next()
}

func TestErrorsPanicAndConcurrentReporting(t *testing.T) {
	s := newScheduler(t, Config{MaxConcurrent: 4}, RunnerFunc(func(ctx context.Context, job Job, report Reporter) error {
		if job.ProjectID == "panic" {
			panic("broken runner")
		}
		if job.ProjectID == "fail" {
			return errors.New("generation failed")
		}
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := report.RecordCost(0.01); err != nil {
					t.Error(err)
				}
				if err := report.Emit("progress", job.ProjectID, nil); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		return nil
	}))
	for _, project := range []string{"panic", "fail", "A", "B", "C"} {
		job := submitJob(t, s, project, "one")
		want := Succeeded
		if project == "panic" || project == "fail" {
			want = Failed
		}
		final := awaitStatus(t, s, job.ID, want)
		if want == Succeeded && math.Abs(final.CostUSD-0.08) > 1e-9 {
			t.Fatal("concurrent charges were lost")
		}
	}
	page, _ := s.Events(context.Background(), 0)
	jobs, _ := s.List(context.Background(), "")
	owner := map[string]string{}
	for _, job := range jobs {
		owner[job.ID] = job.ProjectID
	}
	for _, event := range page.Events {
		if event.ProjectID != owner[event.JobID] {
			t.Fatal("concurrent events crossed project ownership")
		}
	}
}

func TestInputValidationAndClose(t *testing.T) {
	calls := make(chan runCall, 1)
	s := newScheduler(t, Config{}, controlledRunner(calls, true))
	for _, req := range []Request{
		{}, {ProjectID: "A", ProviderID: "one", Kind: "batch", BudgetUSD: math.NaN()},
		{ProjectID: "A", ProviderID: "one", Kind: "batch", Payload: json.RawMessage(`bad`)},
	} {
		if _, err := s.Submit(context.Background(), req); err == nil {
			t.Fatalf("invalid request accepted: %+v", req)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Submit(ctx, Request{ProjectID: "A", ProviderID: "one", Kind: "batch"}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled API context was ignored")
	}
	submitJob(t, s, "A", "one")
	run := nextRun(t, calls)
	if err := run.report.RecordCost(-1); err == nil {
		t.Fatal("negative cost accepted")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(context.Background(), ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed scheduler accepted command: %v", err)
	}
}

func TestCorruptJournalRejectedWithoutOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	for _, content := range []string{`{`, `{"version":99,"next_sequence":1}`, `{"version":1,"next_sequence":0}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(Config{MaxConcurrent: 1, PersistencePath: path}, RunnerFunc(func(context.Context, Job, Reporter) error { return nil })); err == nil {
			t.Fatal("corrupt journal was accepted")
		}
		got, _ := os.ReadFile(path)
		if string(got) != content {
			t.Fatal("corrupt journal was overwritten")
		}
	}
}

func TestJournalRejectsInvalidStateAndCrossProjectEventOwnership(t *testing.T) {
	base := Job{ID: "job", Request: Request{ProjectID: "A", ProviderID: "one", Kind: "batch"}, Status: Queued, CreatedAt: time.Now().UTC()}
	for name, corrupt := range map[string]func(*journal){
		"duplicate ID":       func(j *journal) { j.Jobs = append(j.Jobs, base) },
		"unknown status":     func(j *journal) { j.Jobs[0].Status = "unknown" },
		"negative cost":      func(j *journal) { j.Jobs[0].CostUSD = -1 },
		"missing owner":      func(j *journal) { j.Events[0].JobID = "missing" },
		"different project":  func(j *journal) { j.Events[0].ProjectID = "B" },
		"different provider": func(j *journal) { j.Events[0].ProviderID = "two" },
		"invalid sequence":   func(j *journal) { j.Events[0].Sequence = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			j := journal{Version: 1, NextSequence: 2, Jobs: []Job{base}, Events: []Event{{Sequence: 1, JobID: "job", ProjectID: "A", ProviderID: "one"}}}
			corrupt(&j)
			path := filepath.Join(t.TempDir(), "jobs.json")
			if err := saveJournal(path, j); err != nil {
				t.Fatal(err)
			}
			if _, err := New(Config{MaxConcurrent: 1, PersistencePath: path}, RunnerFunc(func(context.Context, Job, Reporter) error { return nil })); err == nil {
				t.Fatal("corrupt state was accepted and could rerun or misattribute work")
			}
		})
	}
}

func TestConfigurationRejectsInvalidConcurrencyAndBudgets(t *testing.T) {
	runner := RunnerFunc(func(context.Context, Job, Reporter) error { return nil })
	for _, cfg := range []Config{
		{}, {MaxConcurrent: 1, MaxEvents: -1}, {MaxConcurrent: 1, DefaultProviderLimit: -1},
		{MaxConcurrent: 1, ProviderLimits: map[string]int{"one": 0}},
		{MaxConcurrent: 1, ProjectBudgetsUSD: map[string]float64{"A": 0}},
		{MaxConcurrent: 1, ProjectBudgetsUSD: map[string]float64{"A": math.NaN()}},
	} {
		if s, err := New(cfg, runner); err == nil {
			_ = s.Close(context.Background())
			t.Fatalf("invalid configuration accepted: %+v", cfg)
		}
	}
	if _, err := New(Config{MaxConcurrent: 1}, nil); err == nil {
		t.Fatal("nil runner accepted")
	}
}
