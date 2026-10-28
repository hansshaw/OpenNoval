package desktopui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A paused WebView may lose both its command response and SSE subscription.
// Neither transport owns the long-lived model request: only explicit service
// shutdown/pause should cancel it. Exercise real HTTP streams, not a fake model.
func TestMobileStreamOutlivesDisconnectedWebView(t *testing.T) {
	for _, stopService := range []bool{false, true} {
		name := "background-completes"
		if stopService {
			name = "service-stop-cancels"
		}
		t.Run(name, func(t *testing.T) {
			started, canceled, complete := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var startedOnce, canceledOnce, completeOnce sync.Once
			finish := func() { completeOnce.Do(func() { close(complete) }) }
			var requests atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = io.WriteString(w, `{"data":[{"id":"test-model"}]}`)
					return
				}
				requests.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(content string) {
					chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": content}}}})
					_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
					w.(http.Flusher).Flush()
				}
				send("<reply>后台")
				startedOnce.Do(func() { close(started) })
				select {
				case <-r.Context().Done():
					canceledOnce.Do(func() { close(canceled) })
					return
				case <-complete:
				}
				send("创作已完成。</reply><draft>雾港来信：林舟在灯塔找到父亲的信。</draft><ready>true</ready><suggestions></suggestions>")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				w.(http.Flusher).Flush()
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
			directory, ready, hostError := s.dir, s.rt != nil, s.hostError
			s.mu.Unlock()
			if !ready {
				t.Fatalf("model Host unavailable: %s", hostError)
			}
			api := httptest.NewServer(s.Handler())
			t.Cleanup(api.Close)
			// Registered last, so failing assertions still cancel a blocked HTTP
			// handler before httptest.Server waits for it during cleanup.
			t.Cleanup(s.Close)
			t.Cleanup(finish)
			client := &http.Client{Timeout: 5 * time.Second}
			events, err := client.Get(api.URL + "/api/events")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = events.Body.Close() })
			if events.StatusCode != http.StatusOK {
				t.Fatalf("SSE status = %d", events.StatusCode)
			}

			ctx, cancelClient := context.WithCancel(t.Context())
			t.Cleanup(cancelClient)
			r, _ := http.NewRequestWithContext(ctx, http.MethodPost, api.URL+"/api/cocreate", strings.NewReader(`{"text":"规划一个关于雾港来信的短篇。"}`))
			r.Header.Set("Content-Type", "application/json")
			clientDone := make(chan error, 1)
			go func() {
				response, err := client.Do(r)
				if response != nil {
					_ = response.Body.Close()
				}
				clientDone <- err
			}()
			awaitMobileStream(t, started, "model request to start")
			s.mu.Lock()
			jobDone := s.jobDone
			busy := s.busy && s.job == "cocreate"
			s.mu.Unlock()
			if !busy || jobDone == nil {
				t.Fatal("model request is not registered as an active service job")
			}
			if !s.HasActiveWork() {
				t.Fatal("Android power policy cannot see the active model stream")
			}
			cancelClient()
			_ = events.Body.Close()
			select {
			case err := <-clientDone:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("command client did not disconnect: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("command client did not finish cancellation")
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				s.mu.Lock()
				clients := len(s.clients)
				s.mu.Unlock()
				if clients == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("disconnected SSE client remained registered")
				}
				time.Sleep(time.Millisecond)
			}
			select {
			case <-canceled:
				t.Fatal("WebView disconnect canceled the independent upstream model stream")
			case <-jobDone:
				t.Fatal("model job ended before stream completion or service shutdown")
			case <-time.After(30 * time.Millisecond):
			}

			if stopService {
				closed := make(chan struct{})
				go func() { s.Close(); close(closed) }()
				awaitMobileStream(t, canceled, "service shutdown to cancel upstream HTTP")
				awaitMobileStream(t, closed, "service shutdown to finish")
			} else {
				finish()
			}
			awaitMobileStream(t, jobDone, "model job cleanup")
			if s.HasActiveWork() {
				t.Fatal("finished/canceled stream left Android power protection active")
			}
			session, err := readWebCoCreate(directory)
			if err != nil {
				t.Fatal(err)
			}
			if stopService {
				if session.Ready || session.DraftPrompt != "" || session.LastError == "" {
					t.Fatalf("canceled partial output became a valid draft: %+v", session)
				}
			} else {
				if !session.Ready || session.LastError != "" || !strings.Contains(session.DraftPrompt, "林舟在灯塔找到父亲的信") || len(session.Transcript) != 2 {
					t.Fatalf("background response was not durably saved: %+v", session)
				}
				response, err := client.Get(api.URL + "/api/cocreate")
				if err != nil {
					t.Fatal(err)
				}
				data, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if readErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(data), "后台创作已完成") {
					t.Fatalf("returning WebView could not recover finished output: %s (%v)", data, readErr)
				}
			}
			if requests.Load() != 1 {
				t.Fatalf("disconnect triggered an extra/retried model request: %d", requests.Load())
			}
		})
	}
}

func awaitMobileStream(t *testing.T, done <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}
