package mobilebridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hansshaw/AInoval-web-ui/internal/projectjobs"
)

type workObserverFunc func()

func (f workObserverFunc) OnWorkStarted() { f() }

func TestWorkObserverProtectsOnlyAuthenticatedNetworkRequests(t *testing.T) {
	const address = "127.0.0.1:5555"
	token := strings.Repeat("a", 64)
	var observed atomic.Int64
	running := &runtime{}
	running.observer = workObserverFunc(func() {
		if running.requests.Load() == 0 {
			t.Error("callback must see pending work before the handler starts")
		}
		observed.Add(1)
	})
	var entered bool
	handler := sessionHandler(address, token, running.trackWork(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered = true
		if observed.Load() != 1 || running.requests.Load() != 1 {
			t.Error("network handler ran without synchronous protection")
		}
		w.WriteHeader(http.StatusAccepted)
	})))
	for _, authenticated := range []bool{false, true} {
		r := httptest.NewRequest(http.MethodPost, "http://"+address+"/api/cocreate", nil)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: "ainovel_mobile_" + token[:12], Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if entered != authenticated {
			t.Fatalf("authenticated=%v: handler entered=%v", authenticated, entered)
		}
		if !authenticated && observed.Load() != 0 {
			t.Fatal("unauthenticated request acquired power protection")
		}
		if running.requests.Load() != 0 {
			t.Fatal("request completion leaked active request count")
		}
	}
}

func TestWorkTrackingKeepsOverlappingRequestsActiveUntilBothFinish(t *testing.T) {
	running := &runtime{}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	finished := make(chan struct{}, 2)
	handler := running.trackWork(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		entered <- struct{}{}
		<-release
	}))
	for range 2 {
		go func() {
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/config/models", nil))
			finished <- struct{}{}
		}()
	}
	<-entered
	<-entered
	if running.requests.Load() != 2 {
		t.Error("overlapping requests lost their independent leases")
	}
	release <- struct{}{}
	<-finished
	if running.requests.Load() != 1 {
		t.Error("finishing one request hid another active request")
	}
	release <- struct{}{}
	<-finished
	if running.requests.Load() != 0 {
		t.Error("all requests finished but lease remained active")
	}
}

func TestNetworkWorkRoutes(t *testing.T) {
	for _, path := range []string{"/api/action", "/api/cocreate", "/api/cocreate/confirm", "/api/chapter-rewrites", "/api/style/run", "/api/jobs", "/api/config", "/api/config/select", "/api/config/test", "/api/config/models", "/api/media-models/discover", "/api/chapters/1/media/generate"} {
		if !startsNetworkWork(httptest.NewRequest(http.MethodPost, path, nil)) {
			t.Errorf("missing protection for %s", path)
		}
	}
	for _, path := range []string{"/api/events", "/api/state", "/api/manual/chapters", "/api/chapters/1", "/api/reader/progress"} {
		if startsNetworkWork(httptest.NewRequest(http.MethodPost, path, nil)) {
			t.Errorf("idle/local editing request acquired protection: %s", path)
		}
	}
	if startsNetworkWork(httptest.NewRequest(http.MethodGet, "/api/cocreate", nil)) {
		t.Error("reading saved generation state acquired protection")
	}
}

func TestMobileStopCancelsSavedQueueBeforeNextStart(t *testing.T) {
	dataDir, cacheDir := t.TempDir(), t.TempDir()
	started := make(chan struct{}, 1)
	queue, err := projectjobs.New(projectjobs.Config{
		MaxProjects: 1, MaxConcurrent: 1, PersistencePath: filepath.Join(dataDir, "workbench-jobs.json"),
	}, projectjobs.RunnerFunc(func(ctx context.Context, _ projectjobs.Job, _ projectjobs.Reporter) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Close(context.Background()) })
	if _, err := queue.Submit(t.Context(), projectjobs.Request{ProjectID: "running", ProviderID: "model", Kind: "batch"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("fixture job did not start")
	}
	queued, err := queue.Submit(t.Context(), projectjobs.Request{ProjectID: "waiting", ProviderID: "model", Kind: "batch"})
	if err != nil || queued.Status != projectjobs.Queued {
		t.Fatalf("fixture must preserve a waiting job: %+v %v", queued, err)
	}
	if err := queue.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(dataDir, cacheDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Stop)
	// Clearing the app before its first /api/state or /api/jobs response must
	// cancel even a saved queue that this runtime has never opened.
	Stop()
	if HasActiveWork() {
		t.Fatal("stopped mobile runtime still has active work")
	}
	entry, err := Start(dataDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	data := requestJSON(t, authenticatedClient(t, entry), http.MethodGet, originOf(t, entry)+"/api/jobs", "", http.StatusOK)
	var listing struct{ Jobs []projectjobs.Job }
	if err := json.Unmarshal(data, &listing); err != nil {
		t.Fatal(err)
	}
	for _, job := range listing.Jobs {
		if job.ID == queued.ID {
			if job.Status != projectjobs.Canceled {
				t.Fatalf("mobile Stop allowed queued work to replay: %+v", job)
			}
			return
		}
	}
	t.Fatal("cancellation lost queued task history")
}
