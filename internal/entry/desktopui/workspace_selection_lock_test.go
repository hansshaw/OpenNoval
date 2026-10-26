package desktopui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkspaceSwitchWaitsForCurrentReadResponse(t *testing.T) {
	s := newTestServer(t, false)
	target := filepath.Join(s.booksDir, "next")
	if err := saveProject(target, projectInfo{Name: "下一本"}); err != nil {
		t.Fatal(err)
	}
	// A completed GET still owns selectionMu and mu while writing its response,
	// matching the overlap seen with Windows chapter/version polling.
	writer := &gatedCoCreateResponse{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(writer.release) }) }
	t.Cleanup(unblock)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		s.Handler().ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/planning", nil))
	}()
	select {
	case <-writer.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("read request did not reach its response")
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	body := mustJSON(t, map[string]string{"path": target})
	go func() { result <- request(s, http.MethodPost, "/api/workspaces", body) }()
	select {
	case w := <-result:
		t.Fatalf("switch rejected a short read instead of waiting: %d %s", w.Code, w.Body.String())
	case <-time.After(60 * time.Millisecond):
	}
	unblock()
	select {
	case w := <-result:
		if w.Code != http.StatusOK {
			t.Fatalf("switch did not proceed when the read finished: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("switch remained blocked after the short read finished")
	}
	<-readDone
	if s.dir != target {
		t.Fatalf("selected %q, want %q", s.dir, target)
	}
}

func TestSelectionWaitAllowsPauseAndRequestCancellation(t *testing.T) {
	s := newTestServer(t, true)
	s.selectionMu.RLock()
	defer s.selectionMu.RUnlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/workspaces", strings.NewReader(`{"deselect":true}`)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		result <- w
	}()
	select {
	case w := <-result:
		t.Fatalf("selection did not wait for the active request: %d %s", w.Code, w.Body.String())
	case <-time.After(60 * time.Millisecond):
	}
	paused := make(chan *httptest.ResponseRecorder, 1)
	go func() { paused <- request(s, http.MethodPost, "/api/action", `{"action":"pause"}`) }()
	select {
	case w := <-paused:
		if w.Code != http.StatusAccepted {
			t.Fatalf("pause failed during selection wait: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("a waiting selection blocked the pause request")
	}
	cancel()
	select {
	case w := <-result:
		// Pause can mark the server stopping before cancellation is observed.
		if w.Code != http.StatusRequestTimeout && w.Code != http.StatusConflict {
			t.Fatalf("canceled selection unexpectedly proceeded: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("canceled selection kept waiting")
	}
}

func TestSelectionWaitRemainsBoundedWhileServerMutexIsHeld(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "deadline"
		if canceled {
			name = "cancellation"
		}
		t.Run(name, func(t *testing.T) {
			s := newTestServer(t, false)
			s.selectionMu.RLock()
			defer s.selectionMu.RUnlock()
			s.mu.Lock()
			defer s.mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := 75 * time.Millisecond
			if canceled {
				timeout = time.Hour
			}
			result := make(chan bool, 1)
			go func() { result <- s.lockSelection(ctx, timeout) }()
			if canceled {
				cancel()
			}
			select {
			case acquired := <-result:
				if acquired {
					s.selectionMu.Unlock()
					t.Fatal("selection passed an active read")
				}
			case <-time.After(time.Second):
				t.Fatal("the state mutex prevented a bounded selection wait")
			}
		})
	}
}

func TestSelectionWaitRejectsBusyStoppingOrClosedServerPromptly(t *testing.T) {
	for _, state := range []string{"busy", "stopping", "closed"} {
		t.Run(state, func(t *testing.T) {
			s := newTestServer(t, false)
			s.selectionMu.RLock()
			defer s.selectionMu.RUnlock()
			s.mu.Lock()
			s.busy, s.stopping, s.closed = state == "busy", state == "stopping", state == "closed"
			s.mu.Unlock()
			defer func() {
				s.mu.Lock()
				s.busy, s.stopping, s.closed = false, false, false
				s.mu.Unlock()
			}()
			result := make(chan bool, 1)
			go func() { result <- s.lockSelection(context.Background(), selectionWaitTimeout) }()
			select {
			case acquired := <-result:
				if acquired {
					s.selectionMu.Unlock()
					t.Fatal("busy selection was acquired")
				}
			case <-time.After(time.Second):
				t.Fatal("long operation did not reject selection promptly")
			}
		})
	}
}
