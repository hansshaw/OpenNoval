package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hansshaw/AInoval-web-ui/internal/modelrequest"
	"github.com/hansshaw/AInoval-web-ui/internal/projectjobs"
	"github.com/voocel/agentcore"
)

func modelPermitScheduler(t *testing.T, total int) *projectjobs.Scheduler {
	t.Helper()
	s, err := projectjobs.New(projectjobs.Config{MaxProjects: 3, MaxConcurrent: total, DefaultProviderLimit: 1}, projectjobs.RunnerFunc(func(context.Context, projectjobs.Job, projectjobs.Reporter) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

func writeQuotaStream(w http.ResponseWriter, truncated bool) {
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"response\"}}]}\n\n"
	w.Header().Set("Content-Type", "text/event-stream")
	if truncated {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)+100))
	} else {
		body += "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	}
	_, _ = io.WriteString(w, body)
}

func TestModelRequestPermitReleasedAfterRealResponseOrCancellation(t *testing.T) {
	for _, mode := range []string{"stream_done", "stream_error", "stream_cancel", "generate_done", "generate_error", "generate_cancel"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			arrived := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				arrived <- struct{}{}
				switch mode {
				case "stream_done", "stream_error":
					writeQuotaStream(w, mode == "stream_error")
				case "stream_cancel":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				case "generate_error":
					http.Error(w, `{"error":{"message":"unauthorized","type":"authentication_error"}}`, http.StatusUnauthorized)
				case "generate_cancel":
					<-r.Context().Done()
				default:
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
				}
			}))
			defer server.Close()
			quota := modelPermitScheduler(t, 1)
			set, err := NewModelSet(Config{Provider: "quota", ModelName: mode, OutputDir: t.TempDir(), AcquireModelRequest: quota.AcquireModel,
				Providers: map[string]ProviderConfig{"quota": {Type: "openai", APIKey: "test", BaseURL: server.URL}}})
			if err != nil {
				t.Fatal(err)
			}
			// Cancellation releases the permit before the asynchronous terminal
			// log necessarily finishes. Join its owner before TempDir cleanup.
			t.Cleanup(set.CloseRequests)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode[:6] == "stream" {
				stream, err := set.Default.GenerateStream(ctx, []agentcore.Message{agentcore.UserMsg("test")}, nil)
				if err != nil {
					t.Fatal(err)
				}
				var terminal agentcore.StreamEventType
				for event := range stream {
					if mode == "stream_cancel" {
						cancel()
					}
					if event.Type == agentcore.StreamEventDone || event.Type == agentcore.StreamEventError {
						terminal = event.Type
					}
				}
				if mode == "stream_done" && terminal != agentcore.StreamEventDone || mode == "stream_error" && terminal != agentcore.StreamEventError {
					t.Fatalf("wrong stream terminal event: %v", terminal)
				}
			} else {
				if mode == "generate_cancel" {
					returned := make(chan error, 1)
					go func() {
						_, err := set.Default.Generate(ctx, []agentcore.Message{agentcore.UserMsg("test")}, nil)
						returned <- err
					}()
					select {
					case <-arrived:
						cancel()
					case <-time.After(time.Second):
						t.Fatal("request did not reach server")
					}
					select {
					case err := <-returned:
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("Generate ignored cancellation: %v", err)
						}
					case <-time.After(time.Second):
						t.Fatal("Generate cancellation did not return")
					}
				} else {
					_, err := set.Default.Generate(ctx, []agentcore.Message{agentcore.UserMsg("test")}, nil)
					if mode == "generate_done" && err != nil || mode != "generate_done" && err == nil {
						t.Fatalf("wrong Generate result: %v", err)
					}
				}
			}

			checkCtx, stop := context.WithTimeout(t.Context(), time.Second)
			defer stop()
			release, err := quota.AcquireModel(checkCtx, "quota")
			if err != nil {
				t.Fatalf("completed/canceled request leaked its permit: %v", err)
			}
			release()
			if calls.Load() != 1 {
				t.Fatalf("hidden duplicate request: %d", calls.Load())
			}
		})
	}
}

func TestWaitingModelPermitDoesNotStartJournalOrConsumeStreamIdleTimeout(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeQuotaStream(w, false)
	}))
	defer server.Close()
	quota := modelPermitScheduler(t, 1)
	hold, err := quota.AcquireModel(t.Context(), "quota")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	directory := t.TempDir()
	set, err := NewModelSet(Config{Provider: "quota", ModelName: "model", OutputDir: directory, AcquireModelRequest: quota.AcquireModel,
		Providers: map[string]ProviderConfig{"quota": {Type: "openai", APIKey: "test", BaseURL: server.URL, StreamIdleTimeout: "100ms"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(set.CloseRequests)
	journal, err := modelrequest.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	waitingCtx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	if _, err := set.Default.GenerateStream(waitingCtx, []agentcore.Message{agentcore.UserMsg("waiting")}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting permit did not cancel: %v", err)
	}
	requestLogRecords(t, journal, 0)
	if calls.Load() != 0 {
		t.Fatal("waiting permit started an HTTP request")
	}
	finished := make(chan error, 1)
	go func() {
		stream, err := set.Default.GenerateStream(t.Context(), []agentcore.Message{agentcore.UserMsg("waiting")}, nil)
		if err == nil {
			var done bool
			for event := range stream {
				if event.Type == agentcore.StreamEventError {
					err = event.Err
				}
				done = done || event.Type == agentcore.StreamEventDone
			}
			if err == nil && !done {
				err = errors.New("missing complete response")
			}
		}
		finished <- err
	}()
	// Wait longer than the configured transport idle timeout while still queued.
	<-time.After(150 * time.Millisecond)
	requestLogRecords(t, journal, 0)
	hold()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("quota waiting consumed the stream idle budget: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("releasing request permit did not start the queued stream")
	}
	requestLogRecords(t, journal, 1)
}

func TestRoleAndFallbackModelsAcquireTheirRealProviderPermit(t *testing.T) {
	var primaryCalls, roleCalls atomic.Int32
	primarySeen := make(chan struct{}, 4)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		primarySeen <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limit","type":"rate_limit_error"}}`)
	}))
	defer primary.Close()
	role := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		roleCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer role.Close()
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			primaryBefore, roleBefore := primaryCalls.Load(), roleCalls.Load()
			quota := modelPermitScheduler(t, 2)
			hold, err := quota.AcquireModel(t.Context(), "role")
			if err != nil {
				t.Fatal(err)
			}
			defer hold()
			rc := RoleConfig{Provider: "role", Model: "writer-model"}
			if fallback {
				rc = RoleConfig{Provider: "primary", Model: "limited", Fallbacks: []ModelRef{{Provider: "role", Model: "backup"}}}
			}
			directory := t.TempDir()
			set, err := NewModelSet(Config{Provider: "primary", ModelName: "default", OutputDir: directory, AcquireModelRequest: quota.AcquireModel,
				Providers: map[string]ProviderConfig{"primary": {Type: "openai", APIKey: "test", BaseURL: primary.URL}, "role": {Type: "openai", APIKey: "test", BaseURL: role.URL}}, Roles: map[string]RoleConfig{"writer": rc}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(set.CloseRequests)
			journal, err := modelrequest.Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() {
				_, err := set.ForRoleWithFailover("writer", nil).Generate(t.Context(), []agentcore.Message{agentcore.UserMsg("test")}, nil)
				finished <- err
			}()
			if fallback {
				select {
				case <-primarySeen:
				case <-time.After(time.Second):
					t.Fatal("primary request did not reach provider")
				}
			} else {
				<-time.After(30 * time.Millisecond)
			}
			if roleCalls.Load() != roleBefore {
				t.Fatal("role or backup bypassed its own provider permit")
			}
			count := 0
			if fallback {
				count = 1
				if primaryCalls.Load() != primaryBefore+1 {
					t.Fatal("primary request was incorrectly blocked by the backup provider quota")
				}
			}
			requestLogRecords(t, journal, count)
			hold()
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("role/fallback request did not resume after permit release")
			}
			requestLogRecords(t, journal, count+1)
			if roleCalls.Load() != roleBefore+1 {
				t.Fatal("wrong backup request count")
			}
		})
	}
}
