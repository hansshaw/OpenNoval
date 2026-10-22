// Package projectjobs provides a durable, project-isolated background job queue.
// It owns execution slots and model request permits without creating Hosts.
package projectjobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrClosed         = errors.New("project job scheduler is closed")
	ErrNotFound       = errors.New("project job not found")
	ErrNotRunning     = errors.New("project job is not running")
	ErrBudgetExceeded = errors.New("project job budget exhausted")
	ErrCanceled       = errors.New("project job canceled")
	ErrInterrupted    = errors.New("project job interrupted by process shutdown")
	ErrInvalidLimits  = errors.New("invalid project job concurrency limits")
	ErrProjectBusy    = errors.New("project already has running or queued work")
	ErrProjectLimit   = errors.New("maximum active project limit reached")
)

// MaxConcurrency is a validation ceiling, not a promise about machine capacity
// or upstream API quotas. Users should size limits for their actual resources.
const MaxConcurrency = 4096
const DefaultMaxProjects = 3

// Limits separates project execution slots from real model request permits.
// MaxConcurrent and ProviderLimit apply only while a model request is in flight.
// Canceling projects retain their slots until their Runner actually exits.
type Limits struct {
	MaxProjects    int            `json:"maxProjects"`
	MaxConcurrent  int            `json:"maxConcurrent"`
	ProviderLimit  int            `json:"providerLimit"`
	ProviderLimits map[string]int `json:"providerLimits"`
}

type Status string

const (
	Queued    Status = "queued"
	Running   Status = "running"
	Canceling Status = "canceling"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Canceled  Status = "canceled"
	Aborted   Status = "aborted"
)

func (s Status) Terminal() bool {
	return s == Succeeded || s == Failed || s == Canceled || s == Aborted
}

// Config separates active project slots from concurrent model requests.
// MaxProjects defaults to three. Canceling jobs retain their project slot.
// An unconfigured provider uses DefaultProviderLimit (or MaxConcurrent when zero).
// Persisted Limits take priority over these initial values on restart.
// One scheduler owns PersistencePath;
// the desktop's single-instance guard must prevent a second process sharing it.
type Config struct {
	MaxProjects          int
	MaxConcurrent        int
	DefaultProviderLimit int
	ProviderLimits       map[string]int
	ProjectBudgetsUSD    map[string]float64
	PersistencePath      string
	MaxEvents            int
}

// Request contains immutable execution input, without credentials. ProjectID must
// be a stable canonical directory or desktop project ID, never a display name.
// ProviderID identifies the connection subject to the quota, not its model name.
type Request struct {
	ProjectID        string          `json:"project_id"`
	ProviderID       string          `json:"provider_id"`
	Kind             string          `json:"kind"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	BudgetUSD        float64         `json:"budget_usd,omitempty"`
	EstimatedCostUSD float64         `json:"estimated_cost_usd,omitempty"`
}

type Job struct {
	ID string `json:"id"`
	Request
	Status     Status    `json:"status"`
	CostUSD    float64   `json:"cost_usd"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// Event ownership is set by the scheduler. A runner cannot choose JobID,
// ProjectID, ProviderID, Sequence or Time. Payload is copied at the API boundary.
type Event struct {
	Sequence   uint64          `json:"sequence"`
	JobID      string          `json:"job_id"`
	ProjectID  string          `json:"project_id"`
	ProviderID string          `json:"provider_id"`
	Time       time.Time       `json:"time"`
	Kind       string          `json:"kind"`
	Message    string          `json:"message,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// EventPage exposes retention gaps so a UI can refresh List instead of silently
// losing events. AfterSequence is the sequence to use for the next poll.
type EventPage struct {
	Events         []Event `json:"events"`
	AfterSequence  uint64  `json:"after_sequence"`
	OldestSequence uint64  `json:"oldest_sequence"`
	Truncated      bool    `json:"truncated"`
}

// Runner must stop when ctx is canceled and return only after its child work has
// stopped. The scheduler retains concurrency slots until Run returns. Each Run
// needs its own Host/config/store/event collectors; reusing a global Host is unsafe.
// Each actual model call must acquire its real provider's request permit with
// AcquireModel. A queued or thinking Host alone does not consume a model permit.
type Runner interface {
	Run(ctx context.Context, job Job, report Reporter) error
}

type RunnerFunc func(context.Context, Job, Reporter) error

func (f RunnerFunc) Run(ctx context.Context, job Job, report Reporter) error {
	return f(ctx, job, report)
}

// Reporter methods are synchronous and durable. RecordCost takes an incremental
// actual charge, including a final charge after cancellation. At a budget limit
// it records that charge, cancels the job, and returns ErrBudgetExceeded. Context
// cancellation cannot undo provider charges already in flight.
type Reporter interface {
	Emit(kind, message string, payload json.RawMessage) error
	RecordCost(deltaUSD float64) error
}
