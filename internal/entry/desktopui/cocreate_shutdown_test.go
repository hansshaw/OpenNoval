package desktopui

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hansshaw/AInoval-web-ui/internal/host"
)

// Hold the final HTTP response after the handler has persisted the canceled
// session. The handler still owns its cleanup defers and must retain its lease
// until the response and registered job have actually finished.
type gatedCoCreateResponse struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *gatedCoCreateResponse) WriteHeader(status int) {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	w.ResponseRecorder.WriteHeader(status)
}

func TestCoCreateShutdownWaitsForHandlerCleanupAndRetainsLease(t *testing.T) {
	for _, stop := range []string{"close", "pause"} {
		t.Run(stop, func(t *testing.T) {
			entered, canceled := make(chan struct{}), make(chan struct{})
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial preview\"}}]}\n\n")
				w.(http.Flusher).Flush()
				close(entered)
				<-r.Context().Done()
				close(canceled)
			}))
			t.Cleanup(model.Close)
			s := newTestServer(t, true)
			s.mu.Lock()
			connection := s.baseCfg.Providers["local"]
			connection.BaseURL = model.URL + "/v1"
			s.baseCfg.Providers["local"] = connection
			s.rt.Close()
			s.rt = nil
			s.initLocked()
			directory := s.dir
			ready, hostError := s.rt != nil, s.hostError
			s.mu.Unlock()
			if !ready {
				t.Fatalf("model Host unavailable: %s", hostError)
			}
			writer := &gatedCoCreateResponse{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(writer.release) }) }
			t.Cleanup(unblock)
			handlerDone := make(chan struct{})
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/cocreate", strings.NewReader(`{"text":"请帮我规划一篇关于月球图书馆的短篇。"}`))
			r.Header.Set("Content-Type", "application/json")
			go func() {
				defer close(handlerDone)
				s.Handler().ServeHTTP(writer, r)
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("coCreate did not open its real streaming request")
			}
			s.mu.Lock()
			jobDone := s.jobDone
			registered := s.busy && s.job == "cocreate" && jobDone != nil
			s.mu.Unlock()
			if !registered {
				t.Fatal("coCreate was not registered for shutdown and pause waiting")
			}
			closeDone := make(chan struct{})
			if stop == "close" {
				go func() { s.Close(); close(closeDone) }()
			} else if response := request(s, http.MethodPost, "/api/action", `{"action":"pause"}`); response.Code != http.StatusAccepted {
				t.Fatalf("pause = %d: %s", response.Code, response.Body.String())
			}
			select {
			case <-canceled:
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown did not cancel the real model HTTP stream")
			}
			select {
			case <-writer.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("canceled coCreate did not finish saving its session")
			}
			saved, err := readWebCoCreate(directory)
			if err != nil || saved.LastError == "" || len(saved.Transcript) != 1 || saved.Transcript[0].Role != "user" {
				t.Fatalf("canceled coCreate session was not durably finalized: %+v %v", saved, err)
			}
			select {
			case <-jobDone:
				t.Fatal("coCreate job finished before its handler cleanup")
			default:
			}
			if stop == "close" {
				select {
				case <-closeDone:
					t.Fatal("Server.Close returned before coCreate handler cleanup")
				case <-time.After(20 * time.Millisecond):
				}
			}
			if err := withWorkspaceLease(directory, func() error { return nil }); !errors.Is(err, host.ErrProjectSnapshotBusy) {
				t.Fatalf("project lease released before coCreate handler returned: %v", err)
			}
			unblock()
			for name, done := range map[string]<-chan struct{}{"handler": handlerDone, "job cleanup": jobDone} {
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatalf("%s did not finish after releasing final response", name)
				}
			}
			if stop == "close" {
				select {
				case <-closeDone:
				case <-time.After(3 * time.Second):
					t.Fatal("Server.Close did not finish after coCreate cleanup")
				}
			} else {
				deadline := time.Now().Add(3 * time.Second)
				for {
					s.mu.Lock()
					stopped := !s.stopping && s.rt == nil
					s.mu.Unlock()
					if stopped {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("pause did not finish closing the coCreate Host")
					}
					time.Sleep(time.Millisecond)
				}
			}
			s.mu.Lock()
			clean := !s.busy && s.cancelJob == nil && s.jobDone == nil && len(s.runSlots) == 0
			s.mu.Unlock()
			if !clean || writer.Code != http.StatusBadGateway {
				t.Fatalf("shutdown left coCreate state or returned a successful partial response: clean=%v status=%d", clean, writer.Code)
			}
			if err := withWorkspaceLease(directory, func() error { return nil }); err != nil {
				t.Fatalf("project lease not released after complete cleanup: %v", err)
			}
		})
	}
}
