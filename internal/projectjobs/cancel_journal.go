package projectjobs

import "time"

// CancelPendingJournal cancels unfinished saved jobs without starting a
// scheduler. The caller must exclusively own the journal: no live Scheduler
// may be using path. For a live scheduler, use StopAll instead.
// Missing journals and already-terminal history are left untouched.
func CancelPendingJournal(path string) error {
	j, err := loadJournal(path)
	if err != nil {
		return err
	}
	changed := false
	for i := range j.Jobs {
		job := &j.Jobs[i]
		if job.Status.Terminal() {
			continue
		}
		changed = true
		job.Status, job.Error, job.FinishedAt = Canceled, ErrCanceled.Error(), time.Now().UTC()
		j.Events = append(j.Events, Event{
			Sequence: j.NextSequence, JobID: job.ID, ProjectID: job.ProjectID,
			ProviderID: job.ProviderID, Time: job.FinishedAt, Kind: "job.status", Message: string(Canceled),
		})
		j.NextSequence++
	}
	if !changed {
		return nil
	}
	// Match the scheduler's default retained event budget.
	if len(j.Events) > 2000 {
		j.Events = j.Events[len(j.Events)-2000:]
	}
	return saveJournal(path, j)
}
