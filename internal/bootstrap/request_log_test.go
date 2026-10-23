package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hansshaw/AInoval-web-ui/internal/modelrequest"
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
)

func requestLogRecords(t *testing.T, journal *modelrequest.Journal, count int) []modelrequest.Record {
	t.Helper()
	rows, err := journal.List(100, "")
	if err != nil || len(rows) != count {
		t.Fatalf("request count = %d, want %d: %v", len(rows), count, err)
	}
	for index := range rows {
		rows[index], err = journal.Get(rows[index].ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	return rows
}

func TestModelSetLogsRealStreamSuccessAndEOFWithoutHiddenRequest(t *testing.T) {
	for _, truncate := range []bool{false, true} {
		t.Run(fmt.Sprint(truncate), func(t *testing.T) {
			const apiKey, headerSecret, otherKey = "sk-request-secret", "private-header-value", "other-provider-secret"
			const preview = "章节预览"
			var calls, buffered atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer "+apiKey || r.Header.Get("X-Private-Header") != headerSecret {
					t.Error("provider headers did not reach the real request")
				}
				var payload struct {
					Stream bool   `json:"stream"`
					Model  string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				if !payload.Stream {
					buffered.Add(1)
					http.Error(w, "must remain streaming", http.StatusBadRequest)
					return
				}
				if payload.Model != "logged-model" {
					t.Errorf("wrong model %q", payload.Model)
				}
				body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + preview + "\"}}]}\n\n"
				w.Header().Set("Content-Type", "text/event-stream")
				if truncate {
					w.Header().Set("Content-Length", strconv.Itoa(len(body)+100))
				} else {
					body += "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":7,\"total_tokens\":19}}\n\ndata: [DONE]\n\n"
				}
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			directory := t.TempDir()
			set, err := NewModelSet(Config{
				Provider: "logged", ModelName: "logged-model", OutputDir: directory,
				Providers: map[string]ProviderConfig{
					"logged": {Type: "openai", APIKey: apiKey, BaseURL: server.URL, StreamIdleTimeout: "20m", Models: []ModelConfig{{Name: "logged-model", MaxOutputTokens: 512}}, Extra: map[string]any{"headers": map[string]any{"X-Private-Header": headerSecret}}},
					"unused": {Type: "openai", APIKey: otherKey},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx := WithModelRequestAgent(t.Context(), "architect_long")
			messages := []agentcore.Message{agentcore.UserMsg(apiKey + " " + headerSecret + " " + otherKey + " https://example.test/path?token=query-secret#fragment")}
			stream, err := set.ForRole("architect").GenerateStream(ctx, messages, []agentcore.ToolSpec{{Name: "save", Description: "save", Parameters: map[string]any{"type": "object"}}}, agentcore.WithMaxTokens(256))
			if err != nil {
				t.Fatal(err)
			}
			var done int
			var streamErr error
			for event := range stream {
				if event.Type == agentcore.StreamEventDone {
					done++
				}
				if event.Type == agentcore.StreamEventError {
					streamErr = event.Err
				}
			}
			journal, err := modelrequest.Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			record := requestLogRecords(t, journal, 1)[0]
			if !record.Stream || record.Provider != "logged" || record.Model != "logged-model" || record.Agent != "architect_long" || record.MaxOutputTokens != 256 || record.IdleTimeoutMs != (20*time.Minute).Milliseconds() || record.MessageCount != 1 || record.ToolCount != 1 {
				t.Fatalf("request metadata missing: %+v", record)
			}
			var request struct {
				Options  agentcore.CallConfig `json:"options"`
				Messages []agentcore.Message  `json:"messages"`
			}
			if err := json.Unmarshal(record.Request, &request); err != nil || request.Options.MaxTokens != 256 || len(request.Messages) != 1 {
				t.Fatalf("request snapshot lost actual options: %s %v", record.Request, err)
			}
			if record.FirstChunkAt.IsZero() || record.LastChunkAt.Before(record.FirstChunkAt) || record.FinishedAt.Before(record.FirstChunkAt) || record.Chunks < 1 || record.OutputBytes < len(preview) || !strings.Contains(string(record.Response), preview) {
				t.Fatalf("response timing/preview missing: %+v", record)
			}
			if truncate {
				if record.Status != "error" || record.Error == "" || streamErr == nil || done != 0 {
					t.Fatalf("truncated stream was recorded as success: %+v", record)
				}
				// The delay gate is above logging: a canceled queued call is not a
				// second API request and must not create another journal record.
				waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
				if _, err := set.ForRole("architect").GenerateStream(waitCtx, messages, nil); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("retry gate did not remain cancelable: %v", err)
				}
				requestLogRecords(t, journal, 1)
			} else if record.Status != "success" || record.StopReason != "stop" || record.InputTokens != 12 || record.OutputTokens != 7 || done != 1 || streamErr != nil {
				t.Fatalf("successful stream metadata missing: %+v error=%v", record, streamErr)
			}
			if calls.Load() != 1 || buffered.Load() != 0 {
				t.Fatalf("hidden replay: calls=%d buffered=%d", calls.Load(), buffered.Load())
			}
			persisted, err := os.ReadFile(filepath.Join(directory, "logs", "model-requests.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{apiKey, headerSecret, otherKey, "query-secret", "#fragment", "Bearer "} {
				if strings.Contains(string(persisted), secret) {
					t.Errorf("persisted credentials or URL query: %q", secret)
				}
			}
		})
	}
}

func TestModelRequestJournalSurvivesSwapAndPreparedConfiguration(t *testing.T) {
	cfg, requests := modelBindingFixture(t)
	cfg.OutputDir = t.TempDir()
	set, err := NewModelSet(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writer := set.ForRole("writer")
	generate := func(want string) {
		t.Helper()
		_, err := writer.Generate(WithModelRequestAgent(t.Context(), "writer"), []agentcore.Message{agentcore.UserMsg("hi")}, nil)
		if err != nil || <-requests != want {
			t.Fatalf("model call after configuration update: %v", err)
		}
	}
	generate("default-v1")
	if err := set.Swap("writer", "stub", "writer-v1"); err != nil {
		t.Fatal(err)
	}
	generate("writer-v1")
	cfg.Roles = map[string]RoleConfig{"writer": {Provider: "stub", Model: "writer-v2"}}
	prepared, err := NewModelSet(cfg)
	if err != nil {
		t.Fatal(err)
	}
	set.ApplyPrepared(prepared)
	generate("writer-v2")
	journal, err := modelrequest.Open(cfg.OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, record := range requestLogRecords(t, journal, 3) {
		seen[record.Model] = true
		if record.Status != "success" || record.Agent != "writer" || record.Stream || record.InputTokens != 1 || record.OutputTokens != 1 || record.MaxOutputTokens != DefaultMaxOutputTokens {
			t.Fatalf("reconfigured model lost project journal: %+v", record)
		}
	}
	if !seen["default-v1"] || !seen["writer-v1"] || !seen["writer-v2"] || set.ForRole("writer") != writer {
		t.Fatalf("bound role or journal changed across configuration: %v", seen)
	}
}

type requestMetadataModel struct{ *streamPolicyModel }

func (*requestMetadataModel) Info() llm.ModelInfo {
	return llm.ModelInfo{Name: "metadata-model", Provider: "metadata-provider", ContextSize: 64000}
}
func (*requestMetadataModel) Capabilities() llm.Capabilities {
	return llm.Capabilities{Provider: "metadata-provider", Model: "metadata-model", Structured: llm.StructuredCapabilities{JSONSchema: llm.SupportYes}, Streaming: llm.StreamingCapabilities{Supported: llm.SupportYes}}
}
func (*requestMetadataModel) JSONSchemaOverride() *bool {
	yes := true
	return &yes
}

func TestRequestLoggerPreservesCapabilitiesAndCanceledPartialResponse(t *testing.T) {
	journal, err := modelrequest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	inner := &requestMetadataModel{streamPolicyModel: &streamPolicyModel{
		stream: func(ctx context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
			calls.Add(1)
			if agentcore.ResolveCallConfig(opts).MaxTokens != 0 {
				t.Error("logging facade unexpectedly applied a second output policy")
			}
			source := make(chan agentcore.StreamEvent)
			go func() {
				defer close(source)
				select {
				case source <- agentcore.StreamEvent{Type: agentcore.StreamEventToolCallDelta, ToolID: "pending", Delta: `{"content":"partial`}:
				case <-ctx.Done():
				}
				<-ctx.Done()
			}()
			return source, nil
		},
	}}
	logged := newRequestLoggedModel(inner, journal, "metadata-provider", "metadata-model", "https://user:pass@example.test/v1?token=query-secret#fragment", 512, 15*time.Minute).(*requestLoggedModel)
	facts := logged.StructuredOutputFacts()
	if !logged.SupportsTools() || !reflect.DeepEqual(logged.Info(), inner.Info()) || !reflect.DeepEqual(logged.Capabilities(), inner.Capabilities()) || logged.JSONSchemaOverride() == nil || !*logged.JSONSchemaOverride() || facts.Info.ContextSize != 64000 {
		t.Fatalf("optional model interfaces were lost: %+v", facts)
	}
	stream, err := logged.GenerateStream(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if event := <-stream; event.Type != agentcore.StreamEventToolCallDelta {
		t.Fatalf("stream delta not forwarded: %+v", event)
	}
	cancel()
	for range stream {
	}
	record := requestLogRecords(t, journal, 1)[0]
	if record.Status != "canceled" || record.FinishedAt.IsZero() || record.Error == "" || calls.Load() != 1 || !json.Valid(record.Response) || !strings.Contains(string(record.Response), "partial") {
		t.Fatalf("canceled partial response lost: %+v", record)
	}
	if record.Endpoint != "https://example.test/v1" {
		t.Fatalf("endpoint credentials leaked: %q", record.Endpoint)
	}
}

func TestRequestLoggerCancellationWinsOverLateStreamCompletion(t *testing.T) {
	for _, ending := range []string{"closed", "done", "initialization_error"} {
		t.Run(ending, func(t *testing.T) {
			journal, err := modelrequest.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			// Cancellation and provider completion may be ready together. Every
			// possible select ordering must retain the canceled terminal state.
			for attempt := 0; attempt < 12; attempt++ {
				ctx, cancel := context.WithCancel(t.Context())
				inner := &streamPolicyModel{stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
					cancel()
					switch ending {
					case "initialization_error":
						return nil, errors.New("provider stopped after cancellation")
					case "done":
						return policyEvents(agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: policyResponse("late response").Message}), nil
					default:
						return policyEvents(), nil
					}
				}}
				logged := newRequestLoggedModel(inner, journal, "test", "model", "", 256, time.Minute)
				stream, _ := logged.GenerateStream(ctx, nil, nil)
				if stream != nil {
					for range stream {
					}
				}
				cancel()
			}
			for _, record := range requestLogRecords(t, journal, 12) {
				if record.Status != "canceled" {
					t.Errorf("late %s overwrote cancellation: status=%q error=%q", ending, record.Status, record.Error)
				}
			}
		})
	}
}
