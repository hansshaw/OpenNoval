package desktopui

import (
	"context"
	"testing"
	"time"

	"github.com/hansshaw/OpenNovel/internal/host"
	"github.com/hansshaw/OpenNovel/internal/projectjobs"
)

func TestMobileActivityIncludesIndependentWorkAndIgnoresHistory(t *testing.T) {
	for _, test := range []struct {
		name   string
		server *Server
	}{
		{"foreground", &Server{busy: true}},
		{"stopping", &Server{stopping: true}},
		{"style", &Server{styleBusy: true}},
		{"detached project cleanup", &Server{runSlots: map[*host.Host]func(){new(host.Host): func() {}}}},
		{"media stream", &Server{mediaGenerations: map[string]*chapterGeneration{"live": {cancel: func() {}}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !test.server.HasActiveWork() {
				t.Fatal("independent model work was not protected")
			}
			test.server.closed = true
			if test.server.HasActiveWork() {
				t.Fatal("closed runtime retained protection")
			}
		})
	}
	idle := &Server{mediaGenerations: map[string]*chapterGeneration{"completed": {}}}
	if idle.HasActiveWork() || idle.background != nil {
		t.Fatal("reading retained media history must not acquire protection or initialize a scheduler")
	}
}

func TestMobileActivityFollowsBackgroundQueueLifecycle(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	queue, err := projectjobs.New(projectjobs.Config{MaxConcurrent: 1}, projectjobs.RunnerFunc(func(ctx context.Context, _ projectjobs.Job, _ projectjobs.Reporter) error {
		close(started)
		select {
		case <-finish:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := queue.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	s := &Server{background: queue}
	if s.HasActiveWork() {
		t.Fatal("empty queue held protection")
	}
	if _, err := queue.Submit(t.Context(), projectjobs.Request{ProjectID: t.TempDir(), ProviderID: "test", Kind: "batch"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("background job did not start")
	}
	if !s.HasActiveWork() {
		t.Fatal("background job with no selected project was not protected")
	}
	close(finish)
	deadline := time.Now().Add(3 * time.Second)
	for s.HasActiveWork() {
		if time.Now().After(deadline) {
			t.Fatal("finished background job retained protection")
		}
		time.Sleep(time.Millisecond)
	}
}
