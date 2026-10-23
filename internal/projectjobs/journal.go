package projectjobs

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/hansshaw/AInoval-web-ui/internal/fileio"
)

const journalVersion = 1

type journal struct {
	Version      int     `json:"version"`
	NextSequence uint64  `json:"next_sequence"`
	Jobs         []Job   `json:"jobs"`
	Events       []Event `json:"events"`
	Limits       *Limits `json:"limits,omitempty"`
}

func loadJournal(path string) (journal, error) {
	j := journal{Version: journalVersion, NextSequence: 1}
	if path == "" {
		return j, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return j, nil
	}
	if err != nil {
		return j, err
	}
	if err := json.Unmarshal(data, &j); err != nil {
		return j, fmt.Errorf("decode project jobs: %w", err)
	}
	if j.Version != journalVersion || j.NextSequence == 0 {
		return j, fmt.Errorf("unsupported project jobs journal version/sequence: %d/%d", j.Version, j.NextSequence)
	}
	if j.Limits != nil {
		limits, err := normalizeLimits(*j.Limits)
		if err != nil {
			return j, fmt.Errorf("invalid persisted job limits: %w", err)
		}
		j.Limits = &limits
	}
	seen := make(map[string]Job, len(j.Jobs))
	for _, job := range j.Jobs {
		if job.ID == "" {
			return j, fmt.Errorf("project jobs journal contains an empty job ID")
		}
		if _, ok := seen[job.ID]; ok {
			return j, fmt.Errorf("project jobs journal contains duplicate job %q", job.ID)
		}
		if err := validateRequest(job.Request); err != nil {
			return j, fmt.Errorf("invalid persisted job %q: %w", job.ID, err)
		}
		if !validMoney(job.CostUSD) || job.CreatedAt.IsZero() {
			return j, fmt.Errorf("invalid persisted job cost or creation time: %q", job.ID)
		}
		switch job.Status {
		case Queued, Running, Canceling, Succeeded, Failed, Canceled, Aborted:
		default:
			return j, fmt.Errorf("unknown persisted job status %q", job.Status)
		}
		seen[job.ID] = job
	}
	var previous uint64
	for _, ev := range j.Events {
		job, ok := seen[ev.JobID]
		if !ok || ev.ProjectID != job.ProjectID || ev.ProviderID != job.ProviderID {
			return j, fmt.Errorf("persisted event has invalid ownership: %q", ev.JobID)
		}
		if ev.Sequence <= previous || ev.Sequence >= j.NextSequence {
			return j, fmt.Errorf("invalid persisted event sequence: %d", ev.Sequence)
		}
		previous = ev.Sequence
	}
	return j, nil
}

// Atomic replacement matches internal/store's write convention: a private
// temporary file, fsync, close, then rename in the destination directory.
func saveJournal(path string, j journal) error {
	if path == "" {
		return nil
	}
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".project-jobs-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return fileio.Rename(f.Name(), path)
}

func validMoney(v float64) bool { return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }

func validateRequest(r Request) error {
	if strings.TrimSpace(r.ProjectID) == "" || strings.TrimSpace(r.ProviderID) == "" || strings.TrimSpace(r.Kind) == "" {
		return fmt.Errorf("project_id, provider_id and kind are required")
	}
	if !validMoney(r.BudgetUSD) || !validMoney(r.EstimatedCostUSD) {
		return fmt.Errorf("job budget and estimated cost must be finite and nonnegative")
	}
	if len(r.Payload) != 0 && !json.Valid(r.Payload) {
		return fmt.Errorf("job payload must be valid JSON")
	}
	return nil
}
