package desktopui

import (
	"context"
	"log/slog"

	"github.com/hansshaw/AInoval-web-ui/internal/projectjobs"
)

// CloseStoppingQueued is Android's explicit service-stop policy. The ordinary
// desktop Close preserves queued jobs for recovery; clearing the mobile app
// must also cancel them so reopening cannot start new paid model requests.
func (s *Server) CloseStoppingQueued() {
	s.closeOnce.Do(func() { s.closeResourcesWithQueuePolicy(true) })
}

func cancelMobileQueue(queue *projectjobs.Scheduler, journalPath string) {
	var err error
	if queue != nil {
		err = queue.StopAll(context.Background())
	} else {
		// The queue is lazy. Do not instantiate it merely to cancel saved work:
		// New would immediately dispatch those jobs before StopAll could run.
		err = projectjobs.CancelPendingJournal(journalPath)
	}
	if err != nil {
		slog.Error("取消移动端排队任务失败", "error", err)
	}
}

// HasActiveWork reports work that needs CPU/network access independently of the
// currently displayed project or its SSE subscription. Android uses this only
// for temporary power protection, never to start or resume a model request.
func (s *Server) HasActiveWork() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if s.busy || s.stopping || s.styleBusy || len(s.runSlots) != 0 {
		return true
	}
	// Every foreground run reserves a runSlot, including runtimes retained
	// after switching projects. Avoid constructing full UI snapshots on each
	// power-policy poll: those load manuscript details from disk.
	for _, generation := range s.mediaGenerations {
		if generation.cancel != nil {
			return true
		}
	}
	if s.background != nil {
		busy, err := s.background.Busy(context.Background(), "")
		return busy || err != nil
	}
	return false
}
