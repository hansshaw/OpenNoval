package desktopui

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hansshaw/AInoval-web-ui/internal/projectjobs"
)

func TestMobileShutdownCancelsQueuedWorkBeforeRestart(t *testing.T) {
	for _, loaded := range []bool{true, false} {
		name := "active-queue"
		if !loaded {
			name = "unloaded-saved-queue"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			cfg := projectjobs.Config{MaxProjects: 1, MaxConcurrent: 1, PersistencePath: filepath.Join(root, "workbench-jobs.json")}
			started := make(chan struct{}, 2)
			cancelObserved := make(chan struct{})
			releaseCancellation := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseCancellation) }) }
			var calls atomic.Int32
			queue, err := projectjobs.New(cfg, projectjobs.RunnerFunc(func(ctx context.Context, _ projectjobs.Job, _ projectjobs.Reporter) error {
				calls.Add(1)
				started <- struct{}{}
				<-ctx.Done()
				close(cancelObserved)
				<-releaseCancellation
				return ctx.Err()
			}))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				release()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = queue.Close(ctx)
			})
			first, err := queue.Submit(t.Context(), projectjobs.Request{ProjectID: "running", ProviderID: "model", Kind: "batch"})
			if err != nil {
				t.Fatal(err)
			}
			awaitMobileStream(t, started, "first queued model request")
			second, err := queue.Submit(t.Context(), projectjobs.Request{ProjectID: "queued", ProviderID: "model", Kind: "batch"})
			if err != nil || second.Status != projectjobs.Queued {
				t.Fatalf("second task must wait: %+v %v", second, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			s := &Server{ctx: ctx, cancel: cancel, booksDir: filepath.Join(root, "books")}
			if loaded {
				s.background = queue
			} else {
				// Desktop-style close/crash recovery leaves the second job queued.
				// The mobile service has not lazily opened that saved queue yet.
				release()
				if err := queue.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			closed := make(chan struct{})
			go func() { s.CloseStoppingQueued(); close(closed) }()
			if loaded {
				awaitMobileStream(t, cancelObserved, "mobile shutdown to cancel active request")
				// Cancellation is still flushing, but new submissions must already
				// be forbidden. Otherwise StopAll could miss a later paid request.
				w := request(s, "POST", "/api/jobs", `{"kind":"batch","count":1}`)
				if w.Code != 409 {
					t.Errorf("shutdown admitted another job: %d %s", w.Code, w.Body.String())
				}
				release()
			}
			awaitMobileStream(t, closed, "mobile shutdown to flush cancellations")
			s.CloseStoppingQueued() // idempotent, with no second journal owner
			if s.HasActiveWork() {
				t.Fatal("closed workbench still reported active work")
			}
			reopened, err := projectjobs.New(cfg, projectjobs.RunnerFunc(func(context.Context, projectjobs.Job, projectjobs.Reporter) error {
				calls.Add(1)
				return nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close(context.Background()) })
			for _, id := range []string{first.ID, second.ID} {
				job, err := reopened.Get(t.Context(), id)
				if err != nil || !job.Status.Terminal() {
					t.Fatalf("reopened job is not terminal: %+v %v", job, err)
				}
				if (loaded || id == second.ID) && job.Status != projectjobs.Canceled {
					t.Fatalf("cleared queue was replayed: %+v", job)
				}
			}
			if busy, err := reopened.Busy(t.Context(), ""); err != nil || busy || calls.Load() != 1 {
				t.Fatalf("reopening started paid work: busy=%v calls=%d err=%v", busy, calls.Load(), err)
			}
		})
	}
}
