package projectjobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Scheduler owns all mutable state in one command loop. Runner calls, context
// waits and shutdown waits never hold a mutex. Filesystem writes are serialized
// by that loop. Once a command is accepted it completes before its API returns,
// avoiding an ambiguous successful Submit after the caller's context expires.
type Scheduler struct {
	commands chan func(*state)
	done     chan struct{}
	closeErr error // published by closing done; only read after done is closed
}

type execution struct {
	cancel context.CancelCauseFunc
	ctx    context.Context
}

type waiter struct {
	ids  map[string]bool
	done chan struct{}
}

type state struct {
	journal
	cfg            Config
	runner         Runner
	owner          *Scheduler
	active         map[string]execution
	waiters        []waiter
	closing        bool
	persistenceErr error
}

// New replays queued jobs automatically. Interrupted running/canceling jobs are
// durably marked aborted, never silently rerun: a Host operation may already
// have changed chapter files before a crash. Resubmission requires the caller to
// inspect project checkpoints and explicitly submit a new task.
func New(cfg Config, runner Runner) (*Scheduler, error) {
	if runner == nil || cfg.MaxConcurrent < 1 {
		return nil, fmt.Errorf("runner and a positive max concurrency are required")
	}
	if cfg.MaxEvents == 0 {
		cfg.MaxEvents = 2000
	}
	if cfg.MaxEvents < 1 {
		return nil, fmt.Errorf("max events must be positive")
	}
	if cfg.DefaultProviderLimit == 0 {
		cfg.DefaultProviderLimit = cfg.MaxConcurrent
	}
	if cfg.DefaultProviderLimit < 1 {
		return nil, fmt.Errorf("default provider limit must be positive")
	}
	providers := make(map[string]int, len(cfg.ProviderLimits))
	for id, limit := range cfg.ProviderLimits {
		if strings.TrimSpace(id) == "" || limit < 1 {
			return nil, fmt.Errorf("provider quotas require an ID and a positive limit")
		}
		providers[id] = limit
	}
	budgets := make(map[string]float64, len(cfg.ProjectBudgetsUSD))
	for id, limit := range cfg.ProjectBudgetsUSD {
		if strings.TrimSpace(id) == "" || !validMoney(limit) || limit == 0 {
			return nil, fmt.Errorf("project budgets require an ID and a finite positive limit")
		}
		budgets[id] = limit
	}
	cfg.ProviderLimits, cfg.ProjectBudgetsUSD = providers, budgets
	j, err := loadJournal(cfg.PersistencePath)
	if err != nil {
		return nil, err
	}
	s := &Scheduler{commands: make(chan func(*state)), done: make(chan struct{})}
	st := &state{journal: j, cfg: cfg, runner: runner, owner: s, active: make(map[string]execution)}
	recovered := false
	for i := range st.Jobs {
		job := &st.Jobs[i]
		if job.Status == Running || job.Status == Canceling {
			job.Status, job.Error, job.FinishedAt = Aborted, ErrInterrupted.Error(), time.Now().UTC()
			st.addEvent(*job, "job.recovered", job.Error, nil)
			recovered = true
		}
	}
	if recovered {
		if err := st.persist(); err != nil {
			return nil, err
		}
	}
	go s.loop(st)
	return s, nil
}

func (s *Scheduler) loop(st *state) {
	for {
		st.startReady()
		st.notifyWaiters()
		if st.closing && len(st.active) == 0 {
			s.closeErr = st.persist()
			close(s.done)
			return
		}
		command := <-s.commands
		command(st)
	}
}

func (s *Scheduler) call(ctx context.Context, fn func(*state) error) error {
	if ctx == nil {
		return fmt.Errorf("nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	reply := make(chan error, 1)
	select {
	case <-s.done:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	case s.commands <- func(st *state) { reply <- fn(st) }:
	}
	return <-reply
}

func (s *Scheduler) Submit(ctx context.Context, request Request) (Job, error) {
	if err := validateRequest(request); err != nil {
		return Job{}, err
	}
	request.Payload = cloneJSON(request.Payload)
	var result Job
	err := s.call(ctx, func(st *state) error {
		if st.closing {
			return ErrClosed
		}
		if err := st.checkBudget(request, 0); err != nil {
			return err
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return err
		}
		before := cloneJournal(st.journal)
		job := Job{ID: hex.EncodeToString(id[:]), Request: request, Status: Queued, CreatedAt: time.Now().UTC()}
		st.Jobs = append(st.Jobs, job)
		st.addEvent(job, "job.status", string(Queued), nil)
		if err := st.persist(); err != nil {
			// Never execute a job whose Submit failed and whose ID was not returned.
			st.journal = before
			return err
		}
		result = cloneJob(job)
		return nil
	})
	return result, err
}

func (s *Scheduler) Get(ctx context.Context, id string) (Job, error) {
	var result Job
	err := s.call(ctx, func(st *state) error {
		job := st.find(id)
		if job == nil {
			return ErrNotFound
		}
		result = cloneJob(*job)
		return nil
	})
	return result, err
}

// List returns creation order; projectID == "" lists every project.
func (s *Scheduler) List(ctx context.Context, projectID string) ([]Job, error) {
	var result []Job
	err := s.call(ctx, func(st *state) error {
		for _, job := range st.Jobs {
			if projectID == "" || job.ProjectID == projectID {
				result = append(result, cloneJob(job))
			}
		}
		return nil
	})
	return result, err
}

func (s *Scheduler) Events(ctx context.Context, after uint64) (EventPage, error) {
	var result EventPage
	err := s.call(ctx, func(st *state) error {
		result.AfterSequence = st.NextSequence - 1
		if len(st.journal.Events) > 0 {
			result.OldestSequence = st.journal.Events[0].Sequence
			result.Truncated = after < result.OldestSequence-1
		}
		for _, ev := range st.journal.Events {
			if ev.Sequence > after {
				ev.Payload = cloneJSON(ev.Payload)
				result.Events = append(result.Events, ev)
			}
		}
		return nil
	})
	return result, err
}

// Cancel is idempotent for terminal jobs. Running jobs remain canceling and keep
// their project/provider slots until the Runner has actually returned.
func (s *Scheduler) Cancel(ctx context.Context, id string) error {
	return s.call(ctx, func(st *state) error {
		job := st.find(id)
		if job == nil {
			return ErrNotFound
		}
		if job.Status.Terminal() || job.Status == Canceling {
			return nil
		}
		st.cancel(job, ErrCanceled)
		return st.persist()
	})
}

// StopAll cancels the jobs present when the command is accepted and waits for
// their Runners outside the state loop. Later submissions are allowed.
func (s *Scheduler) StopAll(ctx context.Context) error {
	wait := make(chan struct{})
	registered := false
	err := s.call(ctx, func(st *state) error {
		ids := make(map[string]bool, len(st.active))
		for i := range st.Jobs {
			job := &st.Jobs[i]
			if !job.Status.Terminal() {
				st.cancel(job, ErrCanceled)
				if _, ok := st.active[job.ID]; ok {
					ids[job.ID] = true
				}
			}
		}
		st.waiters = append(st.waiters, waiter{ids: ids, done: wait})
		registered = true
		st.notifyWaiters()
		return st.persist()
	})
	if err != nil && !registered {
		return err
	}
	select {
	case <-wait:
		return err
	case <-ctx.Done():
		return errors.Join(ctx.Err(), err)
	}
}

// Close stops admission and aborts active work, preserving queued jobs for the
// next New. It waits for all Runners. If ctx expires shutdown continues; a later
// Close can finish waiting. StopAll explicitly cancels queued jobs as well. A
// Runner ignoring cancellation cannot be force-killed safely by the scheduler.
func (s *Scheduler) Close(ctx context.Context) error {
	begun := false
	err := s.call(ctx, func(st *state) error {
		begun = true
		st.closing = true
		for id := range st.active {
			st.cancel(st.find(id), ErrInterrupted)
		}
		return st.persist()
	})
	if err != nil && !errors.Is(err, ErrClosed) && !begun {
		return err
	}
	select {
	case <-s.done:
		return s.closeErr
	case <-ctx.Done():
		return errors.Join(ctx.Err(), err)
	}
}

// Sync retries a failed journal write. Dispatch is suspended while persistence
// is unhealthy; a write failure also cancels active work to avoid unlogged cost.
func (s *Scheduler) Sync(ctx context.Context) error {
	return s.call(ctx, func(st *state) error { return st.persist() })
}

func (s *Scheduler) Health(ctx context.Context) error {
	return s.call(ctx, func(st *state) error { return st.persistenceErr })
}

func (st *state) persist() error {
	err := saveJournal(st.cfg.PersistencePath, st.journal)
	if err != nil {
		st.persistenceErr = fmt.Errorf("persist project jobs: %w", err)
		for _, active := range st.active {
			active.cancel(st.persistenceErr)
		}
		return st.persistenceErr
	}
	st.persistenceErr = nil
	return nil
}

func (st *state) find(id string) *Job {
	for i := range st.Jobs {
		if st.Jobs[i].ID == id {
			return &st.Jobs[i]
		}
	}
	return nil
}

func (st *state) addEvent(job Job, kind, message string, payload json.RawMessage) {
	st.journal.Events = append(st.journal.Events, Event{
		Sequence: st.NextSequence, JobID: job.ID, ProjectID: job.ProjectID,
		ProviderID: job.ProviderID, Time: time.Now().UTC(), Kind: kind,
		Message: message, Payload: cloneJSON(payload),
	})
	st.NextSequence++
	if len(st.journal.Events) > st.cfg.MaxEvents {
		st.journal.Events = append([]Event(nil), st.journal.Events[len(st.journal.Events)-st.cfg.MaxEvents:]...)
	}
}

func (st *state) cancel(job *Job, cause error) {
	if job.Status.Terminal() || job.Status == Canceling {
		return
	}
	job.Error = cause.Error()
	if active, ok := st.active[job.ID]; ok {
		job.Status = Canceling
		active.cancel(cause)
	} else {
		job.Status, job.FinishedAt = Canceled, time.Now().UTC()
		if errors.Is(cause, ErrInterrupted) {
			job.Status = Aborted
		}
	}
	st.addEvent(*job, "job.status", string(job.Status), nil)
}

func (st *state) projectCost(projectID string) float64 {
	var cost float64
	for _, job := range st.Jobs {
		if job.ProjectID == projectID {
			cost += job.CostUSD
		}
	}
	return cost
}

func (st *state) checkBudget(request Request, cost float64) error {
	if request.BudgetUSD > 0 && (cost >= request.BudgetUSD || request.EstimatedCostUSD > request.BudgetUSD-cost) {
		return ErrBudgetExceeded
	}
	if limit := st.cfg.ProjectBudgetsUSD[request.ProjectID]; limit > 0 {
		spent := st.projectCost(request.ProjectID)
		if spent >= limit || request.EstimatedCostUSD > limit-spent {
			return ErrBudgetExceeded
		}
	}
	return nil
}

func (st *state) startReady() {
	for !st.closing && st.persistenceErr == nil && len(st.active) < st.cfg.MaxConcurrent {
		projects, providers := make(map[string]bool), make(map[string]int)
		for id := range st.active {
			job := st.find(id)
			projects[job.ProjectID] = true
			providers[job.ProviderID]++
		}
		var candidate *Job
		for i := range st.Jobs {
			job := &st.Jobs[i]
			if job.Status != Queued {
				continue
			}
			if projects[job.ProjectID] {
				continue
			}
			// Mark even quota-blocked projects to preserve per-project FIFO.
			projects[job.ProjectID] = true
			limit := st.cfg.ProviderLimits[job.ProviderID]
			if limit == 0 {
				limit = st.cfg.DefaultProviderLimit
			}
			if providers[job.ProviderID] < limit {
				candidate = job
				break
			}
		}
		if candidate == nil {
			return
		}
		if err := st.checkBudget(candidate.Request, candidate.CostUSD); err != nil {
			st.cancel(candidate, err)
			if st.persist() != nil {
				return
			}
			continue
		}
		before := cloneJournal(st.journal)
		candidate.Status, candidate.StartedAt = Running, time.Now().UTC()
		st.addEvent(*candidate, "job.status", string(Running), nil)
		job := cloneJob(*candidate)
		if st.persist() != nil {
			st.journal = before
			return
		}
		ctx, cancel := context.WithCancelCause(context.Background())
		st.active[job.ID] = execution{ctx: ctx, cancel: cancel}
		go st.owner.execute(st.runner, ctx, job)
	}
}

func (s *Scheduler) execute(runner Runner, ctx context.Context, job Job) {
	var runErr error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				runErr = fmt.Errorf("job runner panicked: %v", recovered)
			}
		}()
		runErr = runner.Run(ctx, job, reporter{scheduler: s, id: job.ID})
	}()
	_ = s.call(context.Background(), func(st *state) error {
		active := st.active[job.ID]
		current := st.find(job.ID)
		current.FinishedAt = time.Now().UTC()
		cause := context.Cause(active.ctx)
		if cause != nil {
			current.Status, current.Error = Canceled, cause.Error()
			if errors.Is(cause, ErrInterrupted) {
				current.Status = Aborted
			}
		} else if runErr != nil {
			current.Status, current.Error = Failed, runErr.Error()
		} else {
			current.Status, current.Error = Succeeded, ""
		}
		active.cancel(nil)
		delete(st.active, job.ID)
		st.addEvent(*current, "job.status", string(current.Status), nil)
		return st.persist()
	})
}

func (st *state) notifyWaiters() {
	remaining := st.waiters[:0]
	for _, w := range st.waiters {
		pending := false
		for id := range w.ids {
			if _, ok := st.active[id]; ok {
				pending = true
				break
			}
		}
		if pending {
			remaining = append(remaining, w)
		} else {
			close(w.done)
		}
	}
	st.waiters = remaining
}

type reporter struct {
	scheduler *Scheduler
	id        string
}

func (r reporter) Emit(kind, message string, payload json.RawMessage) error {
	if strings.TrimSpace(kind) == "" || strings.HasPrefix(kind, "job.") {
		return fmt.Errorf("event kind is required and cannot use the reserved job. prefix")
	}
	if len(payload) != 0 && !json.Valid(payload) {
		return fmt.Errorf("event payload must be valid JSON")
	}
	payload = cloneJSON(payload)
	return r.scheduler.call(context.Background(), func(st *state) error {
		job := st.find(r.id)
		if job == nil || (job.Status != Running && job.Status != Canceling) {
			return ErrNotRunning
		}
		st.addEvent(*job, kind, message, payload)
		return st.persist()
	})
}

func (r reporter) RecordCost(delta float64) error {
	if !validMoney(delta) {
		return fmt.Errorf("cost must be finite and nonnegative")
	}
	return r.scheduler.call(context.Background(), func(st *state) error {
		job := st.find(r.id)
		if job == nil || (job.Status != Running && job.Status != Canceling) {
			return ErrNotRunning
		}
		if !validMoney(job.CostUSD+delta) || !validMoney(st.projectCost(job.ProjectID)+delta) {
			return fmt.Errorf("cost overflow")
		}
		job.CostUSD += delta
		payload, _ := json.Marshal(map[string]float64{"delta_usd": delta, "total_usd": job.CostUSD})
		st.addEvent(*job, "job.cost", "", payload)
		// Estimates are for admission only; do not add them to actual spending.
		exhausted := (job.BudgetUSD > 0 && job.CostUSD >= job.BudgetUSD)
		if limit := st.cfg.ProjectBudgetsUSD[job.ProjectID]; limit > 0 && st.projectCost(job.ProjectID) >= limit {
			exhausted = true
			for i := range st.Jobs {
				if st.Jobs[i].ProjectID == job.ProjectID && st.Jobs[i].Status == Queued {
					st.cancel(&st.Jobs[i], ErrBudgetExceeded)
				}
			}
		}
		if exhausted {
			st.cancel(job, ErrBudgetExceeded)
		}
		if err := st.persist(); err != nil {
			return err
		}
		if exhausted {
			return ErrBudgetExceeded
		}
		return nil
	})
}

func cloneJSON(v json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), v...) }

func cloneJob(job Job) Job {
	job.Payload = cloneJSON(job.Payload)
	return job
}

func cloneJournal(j journal) journal {
	j.Jobs = append([]Job(nil), j.Jobs...)
	for i := range j.Jobs {
		j.Jobs[i] = cloneJob(j.Jobs[i])
	}
	j.Events = append([]Event(nil), j.Events...)
	for i := range j.Events {
		j.Events[i].Payload = cloneJSON(j.Events[i].Payload)
	}
	return j
}
