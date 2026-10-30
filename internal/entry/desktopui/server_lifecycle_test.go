package desktopui

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hansshaw/OpenNovel/internal/projectjobs"
)

const testDesktopShutdownToken = "1a4ab1f94d027ad7168dcc762c08135544c4658c64c4433962c53927a0ee2e41"

func TestDesktopShutdownRequiresPrivateCapabilityAndTrustedOrigin(t *testing.T) {
	s := newTestServer(t, false)
	s.desktopShutdownToken = testDesktopShutdownToken
	for _, test := range []struct {
		name, token, host, origin, site, body string
		status                                int
	}{
		{"renderer has no capability", "", "127.0.0.1:8080", "", "", `{}`, 403},
		{"wrong capability", "incorrect", "127.0.0.1:8080", "", "", `{}`, 403},
		{"foreign host", testDesktopShutdownToken, "external.example", "", "", `{}`, 403},
		{"foreign origin", testDesktopShutdownToken, "127.0.0.1:8080", "https://external.example", "", `{}`, 403},
		{"cross site", testDesktopShutdownToken, "127.0.0.1:8080", "", "cross-site", `{}`, 403},
		{"unexpected body", testDesktopShutdownToken, "127.0.0.1:8080", "", "", `{"unknown":true}`, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/desktop/shutdown", strings.NewReader(test.body))
			r.Host = test.host
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set(desktopShutdownHeader, test.token)
			r.Header.Set("Origin", test.origin)
			r.Header.Set("Sec-Fetch-Site", test.site)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			select {
			case <-s.desktopShutdownRequested:
				t.Fatal("unauthorized request initiated shutdown")
			default:
			}
		})
	}
	for _, route := range []string{"/api/state", "/api/config"} {
		if w := request(s, "GET", route, ""); strings.Contains(w.Body.String(), testDesktopShutdownToken) {
			t.Fatal("shutdown capability leaked through a public API")
		}
	}
	for index := 0; index < 2; index++ {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/desktop/shutdown", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(desktopShutdownHeader, testDesktopShutdownToken)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusAccepted {
			t.Fatalf("authorized shutdown: %d %s", w.Code, w.Body.String())
		}
	}
	select {
	case <-s.desktopShutdownRequested:
	default:
		t.Fatal("authorized shutdown was not delivered")
	}
	webOnly := newTestServer(t, false)
	if w := request(webOnly, "POST", "/api/desktop/shutdown", `{}`); w.Code != http.StatusNotFound {
		t.Fatalf("ordinary Web server exposed desktop shutdown: %d", w.Code)
	}
}

func TestDesktopShutdownWaitsForBackgroundChargesAndConcurrentClose(t *testing.T) {
	s := newTestServer(t, false)
	s.desktopShutdownToken = testDesktopShutdownToken
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	journal := filepath.Join(t.TempDir(), "shutdown-jobs.json")
	queue, err := projectjobs.New(projectjobs.Config{MaxConcurrent: 1, PersistencePath: journal}, projectjobs.RunnerFunc(func(ctx context.Context, job projectjobs.Job, reporter projectjobs.Reporter) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		if err := reporter.RecordCost(0.25); err != nil {
			return err
		}
		return ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	s.background = queue
	job, err := queue.Submit(t.Context(), projectjobs.Request{ProjectID: "shutdown-project", ProviderID: "local", Kind: "batch"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- serveWorkbench(parent, s, &http.Server{Handler: s.Handler()}, listener) }()
	r, _ := http.NewRequest("POST", "http://"+listener.Addr().String()+"/api/desktop/shutdown", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(desktopShutdownHeader, testDesktopShutdownToken)
	client := &http.Client{Timeout: time.Second}
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("shutdown status=%d", response.StatusCode)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel its active background job")
	}
	secondClose := make(chan struct{})
	go func() { s.Close(); close(secondClose) }()
	select {
	case <-secondClose:
		t.Fatal("concurrent Close returned before the active job flushed")
	case <-served:
		t.Fatal("sidecar returned before its active job flushed")
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sidecar did not exit after its active job flushed")
	}
	<-secondClose
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	var persisted struct{ Jobs []projectjobs.Job }
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Jobs) != 1 || persisted.Jobs[0].ID != job.ID || persisted.Jobs[0].Status != projectjobs.Aborted || persisted.Jobs[0].CostUSD != 0.25 {
		t.Fatalf("shutdown returned before final job charges were durable: %+v", persisted.Jobs)
	}
	if response, err := client.Get("http://" + listener.Addr().String() + "/api/state"); err == nil {
		response.Body.Close()
		t.Fatal("HTTP listener survived graceful desktop shutdown")
	}
}
