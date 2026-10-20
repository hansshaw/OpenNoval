package projectjobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func requireLimits(t *testing.T, s *Scheduler, want Limits) {
	t.Helper()
	got, err := s.Limits(context.Background())
	if err != nil || got.MaxConcurrent != want.MaxConcurrent || got.ProviderLimit != want.ProviderLimit || !maps.Equal(got.ProviderLimits, want.ProviderLimits) {
		t.Fatalf("limits = %+v, want %+v, err=%v", got, want, err)
	}
}

func changeLimits(t *testing.T, s *Scheduler, max, provider int, overrides map[string]int) Limits {
	t.Helper()
	got, err := s.SetLimits(context.Background(), Limits{MaxConcurrent: max, ProviderLimit: provider, ProviderLimits: overrides})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestLimitsValidationCopiesAndClosedScheduler(t *testing.T) {
	initial := map[string]int{"one": 2}
	s := newScheduler(t, Config{MaxConcurrent: 4, DefaultProviderLimit: 1, ProviderLimits: initial}, RunnerFunc(func(context.Context, Job, Reporter) error { return nil }))
	initial["one"] = 99
	requireLimits(t, s, Limits{MaxConcurrent: 4, ProviderLimit: 1, ProviderLimits: map[string]int{"one": 2}})
	returned, _ := s.Limits(context.Background())
	returned.ProviderLimits["one"] = 100
	input := map[string]int{"one": 8}
	updated := changeLimits(t, s, 64, 4, input)
	input["one"], updated.ProviderLimits["one"] = 10, 12
	want := Limits{MaxConcurrent: 64, ProviderLimit: 4, ProviderLimits: map[string]int{"one": 8}}
	requireLimits(t, s, want)
	for _, invalid := range []Limits{
		{}, {MaxConcurrent: 0, ProviderLimit: 1}, {MaxConcurrent: 1, ProviderLimit: -1},
		{MaxConcurrent: MaxConcurrency + 1, ProviderLimit: 1}, {MaxConcurrent: 1, ProviderLimit: MaxConcurrency + 1},
		{MaxConcurrent: 2, ProviderLimit: 1, ProviderLimits: map[string]int{"": 1}},
		{MaxConcurrent: 2, ProviderLimit: 1, ProviderLimits: map[string]int{"one": 0}},
		{MaxConcurrent: 2, ProviderLimit: 1, ProviderLimits: map[string]int{"one": MaxConcurrency + 1}},
	} {
		if _, err := s.SetLimits(context.Background(), invalid); !errors.Is(err, ErrInvalidLimits) {
			t.Fatalf("invalid limits accepted or unclassified: %+v / %v", invalid, err)
		}
		requireLimits(t, s, want)
	}
	upper := changeLimits(t, s, MaxConcurrency, MaxConcurrency, map[string]int{"one": MaxConcurrency})
	encoded, _ := json.Marshal(upper)
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &shape); err != nil || len(shape) != 3 || shape["maxConcurrent"] == nil || shape["providerLimit"] == nil || shape["providerLimits"] == nil {
		t.Fatalf("limits JSON contract changed: %s / %v", encoded, err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetLimits(context.Background(), Limits{MaxConcurrent: 1, ProviderLimit: 1}); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed scheduler accepted limits: %v", err)
	}
}

func TestRaisingLimitsStartsQueuedJobsWithProviderAndProjectIsolation(t *testing.T) {
	calls := make(chan runCall, 16)
	s := newScheduler(t, Config{MaxConcurrent: 1, DefaultProviderLimit: 1}, controlledRunner(calls, true))
	a := submitJob(t, s, "A", "one")
	ra := nextRun(t, calls)
	b := submitJob(t, s, "B", "one")
	c := submitJob(t, s, "C", "two")
	a2 := submitJob(t, s, "A", "two")
	changeLimits(t, s, 4, 1, nil)
	rc := nextRun(t, calls)
	if rc.job.ID != c.ID {
		t.Fatal("raising global concurrency ignored provider quota")
	}
	awaitStatus(t, s, b.ID, Queued)
	awaitStatus(t, s, a2.ID, Queued)
	changeLimits(t, s, 4, 1, map[string]int{"one": 2})
	if nextRun(t, calls).job.ID != b.ID {
		t.Fatal("raising provider override did not dispatch queued work")
	}
	awaitStatus(t, s, a2.ID, Queued)
	ra.release <- nil
	awaitStatus(t, s, a.ID, Succeeded)
	awaitStatus(t, s, a2.ID, Queued) // provider two still belongs to C
	rc.release <- nil
	if nextRun(t, calls).job.ID != a2.ID {
		t.Fatal("same-project successor did not wait for project and provider slots")
	}
}

func TestLoweringLimitsDoesNotCancelWorkAndCancelingStillCounts(t *testing.T) {
	calls := make(chan runCall, 16)
	cleanup := make(chan struct{})
	runner := RunnerFunc(func(ctx context.Context, job Job, report Reporter) error {
		call := runCall{job: job, ctx: ctx, report: report, release: make(chan error, 1)}
		calls <- call
		select {
		case err := <-call.release:
			return err
		case <-cleanup:
			return nil
		}
	})
	s := newScheduler(t, Config{MaxConcurrent: 4, DefaultProviderLimit: 4}, runner)
	t.Cleanup(func() { close(cleanup) })
	var running []runCall
	for i := 0; i < 4; i++ {
		provider := "one"
		if i == 3 {
			provider = "two"
		}
		submitJob(t, s, fmt.Sprint(i), provider)
		running = append(running, nextRun(t, calls))
	}
	e := submitJob(t, s, "E", "one")
	f := submitJob(t, s, "F", "two")
	if err := s.Cancel(context.Background(), running[0].job.ID); err != nil {
		t.Fatal(err)
	}
	changeLimits(t, s, 2, 1, nil)
	awaitStatus(t, s, running[0].job.ID, Canceling)
	for _, call := range running[1:] {
		if call.ctx.Err() != nil {
			t.Fatal("lowering limits canceled existing work")
		}
	}
	running[1].release <- nil
	running[2].release <- nil
	awaitStatus(t, s, running[2].job.ID, Succeeded)
	awaitStatus(t, s, e.ID, Queued)
	awaitStatus(t, s, f.ID, Queued)
	running[3].release <- nil
	if nextRun(t, calls).job.ID != f.ID {
		t.Fatal("unaffected provider could not use an available global slot")
	}
	awaitStatus(t, s, e.ID, Queued)
	running[0].release <- nil
	if nextRun(t, calls).job.ID != e.ID {
		t.Fatal("canceling job released its provider slot before actually returning")
	}
}

func TestLimitsPersistAndApplyBeforeRestartedQueueDispatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	want := Limits{MaxConcurrent: 64, ProviderLimit: 32, ProviderLimits: map[string]int{"other": 16}}
	s := newScheduler(t, Config{MaxConcurrent: 1, PersistencePath: path}, RunnerFunc(func(context.Context, Job, Reporter) error { return nil }))
	changeLimits(t, s, want.MaxConcurrent, want.ProviderLimit, want.ProviderLimits)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	j, err := loadJournal(path)
	if err != nil || j.Limits == nil {
		t.Fatalf("updated limits were not durable: %+v / %v", j.Limits, err)
	}
	for i := 0; i < 64; i++ {
		j.Jobs = append(j.Jobs, Job{ID: fmt.Sprint(i), Request: Request{ProjectID: fmt.Sprint(i), ProviderID: "one", Kind: "batch"}, Status: Queued, CreatedAt: time.Now().UTC()})
	}
	if err := saveJournal(path, j); err != nil {
		t.Fatal(err)
	}
	calls := make(chan runCall, 64)
	restarted := newScheduler(t, Config{MaxConcurrent: 1, DefaultProviderLimit: 1, PersistencePath: path}, controlledRunner(calls, true))
	requireLimits(t, restarted, want)
	for i := 0; i < 32; i++ {
		nextRun(t, calls)
	}
	jobs, _ := restarted.List(context.Background(), "")
	running := 0
	for _, job := range jobs {
		if job.Status == Running {
			running++
		}
	}
	if running != 32 {
		t.Fatalf("restart used config defaults or ignored stored provider limit: running=%d", running)
	}
}

func TestLegacyJournalUsesConfiguredLimitsAndInvalidSavedLimitsFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	j := journal{Version: 1, NextSequence: 1}
	if err := saveJournal(path, j); err != nil {
		t.Fatal(err)
	}
	s := newScheduler(t, Config{MaxConcurrent: 64, DefaultProviderLimit: 8, PersistencePath: path}, RunnerFunc(func(context.Context, Job, Reporter) error { return nil }))
	requireLimits(t, s, Limits{MaxConcurrent: 64, ProviderLimit: 8})
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	j.Limits = &Limits{MaxConcurrent: 1, ProviderLimit: MaxConcurrency + 1}
	if err := saveJournal(path, j); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := New(Config{MaxConcurrent: 1, PersistencePath: path}, RunnerFunc(func(context.Context, Job, Reporter) error { t.Error("invalid saved limits started work"); return nil })); !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("invalid persisted limits fell back silently: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("invalid saved limits were overwritten")
	}
}

func TestFailedLimitsWriteRollsBackWithoutCancelingActiveJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	calls := make(chan runCall, 4)
	s := newScheduler(t, Config{MaxConcurrent: 1, DefaultProviderLimit: 1, PersistencePath: path}, controlledRunner(calls, true))
	submitJob(t, s, "A", "one")
	a := nextRun(t, calls)
	b := submitJob(t, s, "B", "one")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetLimits(context.Background(), Limits{MaxConcurrent: 64, ProviderLimit: 64}); err == nil {
		t.Fatal("failed persistence was reported as a successful settings update")
	}
	requireLimits(t, s, Limits{MaxConcurrent: 1, ProviderLimit: 1})
	if a.ctx.Err() != nil || s.Health(context.Background()) != nil {
		t.Fatal("a failed settings write canceled healthy work or poisoned scheduler health")
	}
	awaitStatus(t, s, b.ID, Queued)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	changeLimits(t, s, 64, 64, nil)
	if nextRun(t, calls).job.ID != b.ID {
		t.Fatal("successful retry did not activate the persisted limits")
	}
}

func TestSuccessfulLimitsWriteRecoversExistingJournalFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	calls := make(chan runCall, 1)
	s := newScheduler(t, Config{MaxConcurrent: 1, PersistencePath: path}, controlledRunner(calls, true))
	t.Cleanup(func() {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			_ = os.Remove(path)
		}
	})
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(context.Background(), Request{ProjectID: "failed", ProviderID: "one", Kind: "batch"}); err == nil {
		t.Fatal("failed journal accepted a job")
	}
	if s.Health(context.Background()) == nil {
		t.Fatal("failed job persistence did not suspend dispatch")
	}
	if _, err := s.SetLimits(context.Background(), Limits{MaxConcurrent: 64, ProviderLimit: 64}); err == nil || s.Health(context.Background()) == nil {
		t.Fatal("failed settings write incorrectly cleared an existing persistence fault")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	changeLimits(t, s, 64, 64, nil)
	if err := s.Health(context.Background()); err != nil {
		t.Fatal("successful complete journal write did not restore scheduler health:", err)
	}
	submitJob(t, s, "healthy", "one")
	if nextRun(t, calls).job.ProjectID != "healthy" {
		t.Fatal("recovered settings write revived a failed submission")
	}
}

func TestHighConcurrencyKeepsProjectSerialAndEventCostOwnership(t *testing.T) {
	for _, concurrency := range []int{64, 128} {
		t.Run(fmt.Sprint(concurrency), func(t *testing.T) {
			gate := make(chan struct{})
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(gate) }) })
			started := make(chan Job, concurrency*2)
			var current, peak atomic.Int32
			var mu sync.Mutex
			activeProjects := make(map[string]int)
			runner := RunnerFunc(func(ctx context.Context, job Job, report Reporter) error {
				now := current.Add(1)
				for old := peak.Load(); now > old && !peak.CompareAndSwap(old, now); old = peak.Load() {
				}
				mu.Lock()
				activeProjects[job.ProjectID]++
				if activeProjects[job.ProjectID] != 1 {
					t.Error("same-project jobs overlapped under high concurrency")
				}
				mu.Unlock()
				defer func() { mu.Lock(); activeProjects[job.ProjectID]--; mu.Unlock(); current.Add(-1) }()
				started <- job
				select {
				case <-gate:
				case <-ctx.Done():
					return ctx.Err()
				}
				if err := report.RecordCost(0.01); err != nil {
					return err
				}
				return report.Emit("progress", job.ProjectID, nil)
			})
			s := newScheduler(t, Config{MaxConcurrent: concurrency, DefaultProviderLimit: concurrency}, runner)
			// Register after the scheduler cleanup so failure also releases gates
			// before waiting for Runners that have not reached their context select.
			t.Cleanup(func() { release.Do(func() { close(gate) }) })
			for i := 0; i < concurrency; i++ {
				submitJob(t, s, fmt.Sprint(i), "one")
			}
			for i := 0; i < concurrency; i++ {
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("requested concurrency never became active")
				}
			}
			for i := 0; i < concurrency; i++ {
				submitJob(t, s, fmt.Sprint(i), "one")
			}
			jobs, _ := s.List(context.Background(), "")
			queued := 0
			for _, job := range jobs {
				if job.Status == Queued {
					queued++
				}
			}
			if queued != concurrency || int(peak.Load()) != concurrency {
				t.Fatalf("wrong active/queued capacity: peak=%d queued=%d", peak.Load(), queued)
			}
			release.Do(func() { close(gate) })
			deadline := time.After(5 * time.Second)
			for {
				busy, err := s.Busy(context.Background(), "")
				if err != nil {
					t.Fatal(err)
				}
				if !busy {
					break
				}
				select {
				case <-deadline:
					t.Fatal("high-concurrency jobs did not complete")
				case <-time.After(time.Millisecond):
				}
			}
			jobs, _ = s.List(context.Background(), "")
			owner := make(map[string]string)
			for _, job := range jobs {
				if job.Status != Succeeded || job.CostUSD != 0.01 {
					t.Fatalf("high-concurrency accounting failed: %+v", job)
				}
				owner[job.ID] = job.ProjectID
			}
			page, _ := s.Events(context.Background(), 0)
			for _, event := range page.Events {
				if owner[event.JobID] != event.ProjectID || event.ProviderID != "one" {
					t.Fatal("high-concurrency event attribution crossed projects")
				}
			}
		})
	}
}
