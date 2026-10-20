package projectjobs

import (
	"encoding/json"
	"errors"
	"fmt"
)

const maxReportBatch = 64

type reportCommand struct {
	id, kind, message string
	payload           json.RawMessage
	cost              bool
	delta             float64
	reply             chan error
}

func (s *Scheduler) report(report reportCommand) error {
	report.reply = make(chan error, 1)
	select {
	case <-s.done:
		return ErrClosed
	default:
	}
	select {
	case <-s.done:
		return ErrClosed
	case s.reports <- report:
	}
	select {
	case err := <-report.reply:
		return err
	case <-s.done:
		// A retained Reporter may arrive after its Runner exited. Closing the
		// scheduler must not strand it behind a buffered, unprocessed request.
		select {
		case err := <-report.reply:
			return err
		default:
			return ErrClosed
		}
	}
}

// flushReports groups only already-ready reports, without a timer or deferred
// durability. Replies are sent after the complete batch has been fsynced. A
// bounded batch and a command check prevent reporting from starving controls.
func (st *state) flushReports(first reportCommand) {
	batch := make([]reportCommand, 1, maxReportBatch)
	batch[0] = first
	var command func(*state)
collect:
	for len(batch) < maxReportBatch {
		select {
		case command = <-st.owner.commands:
			break collect
		default:
		}
		select {
		case report := <-st.owner.reports:
			batch = append(batch, report)
		default:
			break collect
		}
	}
	results := make([]error, len(batch))
	dirty := false
	for i, report := range batch {
		var changed bool
		results[i], changed = st.applyReport(report)
		dirty = dirty || changed
	}
	var persistErr error
	if dirty {
		persistErr = st.persist()
	}
	for i, report := range batch {
		report.reply <- errors.Join(results[i], persistErr)
	}
	if command != nil {
		command(st)
	}
}

func (st *state) applyReport(report reportCommand) (error, bool) {
	job := st.find(report.id)
	if job == nil || (job.Status != Running && job.Status != Canceling) {
		return ErrNotRunning, false
	}
	if !report.cost {
		st.addEvent(*job, report.kind, report.message, report.payload)
		return nil, true
	}
	projectCost := st.projectCost(job.ProjectID)
	if !validMoney(job.CostUSD+report.delta) || !validMoney(projectCost+report.delta) {
		return fmt.Errorf("cost overflow"), false
	}
	job.CostUSD += report.delta
	payload, _ := json.Marshal(map[string]float64{"delta_usd": report.delta, "total_usd": job.CostUSD})
	st.addEvent(*job, "job.cost", "", payload)
	// Estimates affect admission only; every actual charge, including charges
	// arriving after a previous report crossed the limit, remains in the journal.
	exhausted := job.BudgetUSD > 0 && job.CostUSD >= job.BudgetUSD
	if limit := st.cfg.ProjectBudgetsUSD[job.ProjectID]; limit > 0 && projectCost+report.delta >= limit {
		exhausted = true
		for i := range st.Jobs {
			if st.Jobs[i].ProjectID == job.ProjectID && st.Jobs[i].Status == Queued {
				st.cancel(&st.Jobs[i], ErrBudgetExceeded)
			}
		}
	}
	if exhausted {
		st.cancel(job, ErrBudgetExceeded)
		return ErrBudgetExceeded, true
	}
	return nil, true
}
