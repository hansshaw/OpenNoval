package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
)

type streamPolicyModel struct {
	generate func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error)
	stream   func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error)
}

func (m *streamPolicyModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return m.generate(ctx, messages, tools, opts...)
}
func (m *streamPolicyModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return m.stream(ctx, messages, tools, opts...)
}
func (*streamPolicyModel) SupportsTools() bool { return true }
func policyResponse(text string) *agentcore.LLMResponse {
	return &agentcore.LLMResponse{Message: agentcore.Message{Role: agentcore.RoleAssistant, StopReason: agentcore.StopReasonStop, Content: []agentcore.ContentBlock{agentcore.TextBlock(text)}}}
}
func policyEvents(events ...agentcore.StreamEvent) <-chan agentcore.StreamEvent {
	out := make(chan agentcore.StreamEvent, len(events))
	for _, event := range events {
		out <- event
	}
	close(out)
	return out
}
func collectPolicyEvents(t *testing.T, ctx context.Context, model agentcore.ChatModel) []agentcore.StreamEvent {
	t.Helper()
	stream, err := model.GenerateStream(ctx, []agentcore.Message{{Role: agentcore.RoleUser, Content: []agentcore.ContentBlock{agentcore.TextBlock("test")}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var events []agentcore.StreamEvent
	for {
		select {
		case event, ok := <-stream:
			if !ok {
				return events
			}
			events = append(events, event)
		case <-ctx.Done():
			t.Fatalf("stream did not finish: %v", ctx.Err())
		}
	}
}

func TestStreamPolicyTruncatedTransportNeverChangesToBuffered(t *testing.T) {
	for _, truncatedHTTP := range []bool{true, false} {
		t.Run(fmt.Sprint(truncatedHTTP), func(t *testing.T) {
			var streamed, buffered atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Stream bool `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				if !payload.Stream {
					buffered.Add(1)
					http.Error(w, "must stay streaming", 500)
					return
				}
				streamed.Add(1)
				body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"半截预览\"}}]}\n\n"
				w.Header().Set("Content-Type", "text/event-stream")
				if truncatedHTTP {
					w.Header().Set("Content-Length", strconv.Itoa(len(body)+100))
				}
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			model, err := createModelFromConfig("test", "test-model", ProviderConfig{Type: "openai", APIKey: "test", BaseURL: server.URL}, 256, make(map[string]agentcore.ChatModel))
			if err != nil {
				t.Fatal(err)
			}
			events := collectPolicyEvents(t, t.Context(), model)
			var done int
			var finalErr error
			for _, event := range events {
				if event.Type == agentcore.StreamEventDone {
					done++
				}
				if event.Type == agentcore.StreamEventError {
					finalErr = event.Err
				}
			}
			var hint agentcore.RetryHinter
			var retryable agentcore.RetryableError
			if done != 0 || finalErr == nil || !errors.As(finalErr, &hint) || hint.RetryAfter() != 15*time.Second || !errors.As(finalErr, &retryable) || !retryable.Retryable() {
				t.Fatalf("incomplete response must fail with delayed retry hint: done=%d err=%v", done, finalErr)
			}
			if streamed.Load() != 1 || buffered.Load() != 0 {
				t.Fatalf("hidden replay: streamed=%d buffered=%d", streamed.Load(), buffered.Load())
			}
		})
	}
}

func TestStreamPolicyTerminalErrorsRemainTerminal(t *testing.T) {
	for _, terminal := range []error{context.Canceled, context.DeadlineExceeded, agentcore.ErrProviderAuth, agentcore.ErrProviderQuota, agentcore.ErrProviderContentFilter, errors.New("invalid schema")} {
		for _, atStart := range []bool{true, false} {
			t.Run(fmt.Sprintf("%v/start=%v", terminal, atStart), func(t *testing.T) {
				stub := &streamPolicyModel{
					generate: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
						t.Error("unexpected buffered request")
						return nil, nil
					},
					stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
						if atStart {
							return nil, terminal
						}
						return policyEvents(agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: terminal}), nil
					},
				}
				limited := newOutputLimitedModel(stub, "stub", "model", 256).(*outputLimitedModel)
				stream, err := limited.GenerateStream(t.Context(), nil, nil)
				if err == nil {
					for event := range stream {
						if event.Type == agentcore.StreamEventError {
							err = event.Err
						}
					}
				}
				if !errors.Is(err, terminal) || limited.streamRetryNotBefore.Load() != 0 {
					t.Fatalf("terminal error changed: %v", err)
				}
			})
		}
	}
}

func TestStreamPolicyNormalCompletionStaysStreaming(t *testing.T) {
	var streamed atomic.Int32
	stub := &streamPolicyModel{
		generate: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
			t.Error("unexpected buffered request")
			return nil, nil
		},
		stream: func(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
			streamed.Add(1)
			if agentcore.ResolveCallConfig(opts).MaxTokens != 256 {
				t.Error("output budget lost")
			}
			return policyEvents(agentcore.StreamEvent{Type: agentcore.StreamEventTextDelta, Delta: "完整正文"}, agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: policyResponse("完整正文").Message}), nil
		},
	}
	limited := newOutputLimitedModel(stub, "stub", "model", 256).(*outputLimitedModel)
	for i := 0; i < 2; i++ {
		events := collectPolicyEvents(t, t.Context(), limited)
		if len(events) != 2 || events[1].Type != agentcore.StreamEventDone || events[1].Message.TextContent() != "完整正文" {
			t.Fatalf("bad response: %+v", events)
		}
	}
	if streamed.Load() != 2 || limited.streamRetryNotBefore.Load() != 0 {
		t.Fatal("successful streaming changed policy")
	}
}

func TestStreamPolicyMissingDoneReportsTruncation(t *testing.T) {
	stub := &streamPolicyModel{stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
		return policyEvents(agentcore.StreamEvent{Type: agentcore.StreamEventTextDelta, Delta: "partial"}), nil
	}}
	events := collectPolicyEvents(t, t.Context(), newOutputLimitedModel(stub, "stub", "model", 256))
	if len(events) != 2 || events[1].Type != agentcore.StreamEventError || !errors.Is(events[1].Err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncation was accepted: %+v", events)
	}
}

type policyHintError struct{ delay time.Duration }

func (e policyHintError) Error() string             { return "provider rate limit" }
func (e policyHintError) Is(target error) bool      { return target == agentcore.ErrProviderRateLimit }
func (e policyHintError) Retryable() bool           { return true }
func (e policyHintError) RetryAfter() time.Duration { return e.delay }

func TestStreamPolicyRetryHintsAndGatePreserveLongServerDelay(t *testing.T) {
	for _, serverDelay := range []time.Duration{0, time.Second, 5 * time.Minute} {
		limited := newOutputLimitedModel(&streamPolicyModel{}, "stub", "model", 256).(*outputLimitedModel)
		now := time.Now()
		cause := policyHintError{delay: serverDelay}
		err := limited.streamError(t.Context(), cause)
		var hint agentcore.RetryHinter
		if !errors.As(err, &hint) {
			t.Fatal("retry hint missing")
		}
		want := max(15*time.Second, serverDelay)
		if hint.RetryAfter() != want || time.Unix(0, limited.streamRetryNotBefore.Load()).Before(now.Add(want)) || !errors.Is(err, cause) {
			t.Fatalf("delay/cause lost: hint=%v err=%v", hint.RetryAfter(), err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := limited.waitForStreamRetry(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation blocked by gate: %v", err)
		}
	}
}

func TestStreamPolicyGateBlocksNewRequestAndThenKeepsStreaming(t *testing.T) {
	var calls atomic.Int32
	stub := &streamPolicyModel{stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
		calls.Add(1)
		return policyEvents(agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: policyResponse("done").Message}), nil
	}}
	limited := newOutputLimitedModel(stub, "stub", "model", 256).(*outputLimitedModel)
	limited.streamError(t.Context(), policyHintError{delay: 5 * time.Minute})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := limited.GenerateStream(ctx, nil, nil); !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 0 {
		t.Fatalf("retry gate bypassed: calls=%d err=%v", calls.Load(), err)
	}
	// Advance only the gate; no test sleeps through the production 15-second delay.
	limited.streamRetryNotBefore.Store(time.Now().Add(-time.Second).UnixNano())
	events := collectPolicyEvents(t, t.Context(), limited)
	if calls.Load() != 1 || len(events) != 1 || events[0].Type != agentcore.StreamEventDone {
		t.Fatalf("gate expiry did not resume streaming: %+v", events)
	}
}

func TestStreamPolicyCancellationDoesNotStartModelOrRetry(t *testing.T) {
	var calls atomic.Int32
	stub := &streamPolicyModel{stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
		calls.Add(1)
		return nil, io.ErrUnexpectedEOF
	}}
	limited := newOutputLimitedModel(stub, "stub", "model", 256).(*outputLimitedModel)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := limited.GenerateStream(ctx, nil, nil); !errors.Is(err, context.Canceled) || calls.Load() != 0 || limited.streamRetryNotBefore.Load() != 0 {
		t.Fatalf("canceled request invoked model: calls=%d err=%v", calls.Load(), err)
	}
}

func TestStreamPolicyRunnerDoesNotExecutePartialToolsOrRetryWithoutBudget(t *testing.T) {
	var requests, executed atomic.Int32
	call := agentcore.ToolCall{ID: "partial", Name: "finish", Args: json.RawMessage(`{"content":"partial"}`)}
	stub := &streamPolicyModel{stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
		requests.Add(1)
		return policyEvents(agentcore.StreamEvent{Type: agentcore.StreamEventToolCallEnd, CompletedToolCall: &call}, agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: io.ErrUnexpectedEOF}), nil
	}}
	tool := agentcore.NewFuncTool("finish", "finish", map[string]any{"type": "object"}, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		executed.Add(1)
		return json.RawMessage(`{}`), nil
	})
	runner := subagent.NewRunner(subagent.Config{Name: "test", Model: newOutputLimitedModel(stub, "stub", "model", 256), Tools: []agentcore.Tool{tool}, MaxTurns: 2, MaxRetries: 0})
	_, err := runner.Run(t.Context(), "test", "test")
	if err == nil || requests.Load() != 1 || executed.Load() != 0 {
		t.Fatalf("incomplete tool/request committed: err=%v requests=%d tools=%d", err, requests.Load(), executed.Load())
	}
}

func TestStreamPolicyWorkerRetryIsDelayedAndCancelable(t *testing.T) {
	var requests atomic.Int32
	stub := &streamPolicyModel{stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
		requests.Add(1)
		return nil, io.ErrUnexpectedEOF
	}}
	runner := subagent.NewRunner(subagent.Config{Name: "test", Model: newOutputLimitedModel(stub, "stub", "model", 256), MaxTurns: 2, MaxRetries: 2})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var delay int64
	ctx = agentcore.WithToolProgress(ctx, func(p agentcore.ProgressPayload) {
		if p.Kind == agentcore.ProgressRetry {
			var meta struct {
				Delay int64 `json:"retry_delay_ms"`
			}
			if err := json.Unmarshal(p.Meta, &meta); err != nil {
				t.Error(err)
			}
			delay = meta.Delay
			cancel()
		}
	})
	_, err := runner.Run(ctx, "test", "test")
	if !errors.Is(err, context.Canceled) || requests.Load() != 1 || delay < 15000 {
		t.Fatalf("retry was too eager or ignored cancel: calls=%d delay=%d err=%v", requests.Load(), delay, err)
	}
}

func TestStreamFailoverWaitsBeforeUsingBackup(t *testing.T) {
	for _, atStart := range []bool{true, false} {
		t.Run(fmt.Sprint(atStart), func(t *testing.T) {
			var backups atomic.Int32
			primary := &streamPolicyModel{stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
				if atStart {
					return nil, io.ErrUnexpectedEOF
				}
				return policyEvents(agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: io.ErrUnexpectedEOF}), nil
			}}
			backup := &streamPolicyModel{stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
				backups.Add(1)
				return policyEvents(), nil
			}}
			model := &failoverModel{role: "writer", primary: NewSwappableModel("primary", "model", primary, nil), set: &ModelSet{fallbacks: map[string][]modelTarget{"writer": {{provider: "backup", name: "model", model: backup}}}}}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			stream, err := model.GenerateStream(ctx, []agentcore.Message{{Role: agentcore.RoleUser, Content: []agentcore.ContentBlock{agentcore.TextBlock("test")}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			for range stream {
			}
			if backups.Load() != 0 || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatal("fallback was immediate or ignored cancellation")
			}
		})
	}
}

func TestFailoverTargetKeepsInFlightModelAfterSwap(t *testing.T) {
	fixed := func(name string) *streamPolicyModel {
		return &streamPolicyModel{generate: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
			return policyResponse(name), nil
		}}
	}
	oldModel, nextModel := fixed("old"), fixed("next")
	yes, no := true, false
	primary := NewSwappableModel("old-provider", "old-model", oldModel, &yes)
	failover := &failoverModel{primary: primary}
	inFlight := failover.currentTarget()
	primary.Swap("next-provider", "next-model", nextModel, &no)
	resp, err := inFlight.model.Generate(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inFlight.provider != "old-provider" || inFlight.name != "old-model" || resp.Message.TextContent() != "old" || inFlight.jsonSchema == nil || !*inFlight.jsonSchema {
		t.Fatalf("active request changed model or capability facts after swap: %+v response=%q", inFlight, resp.Message.TextContent())
	}
	next := failover.currentTarget()
	resp, err = next.model.Generate(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if next.provider != "next-provider" || next.name != "next-model" || resp.Message.TextContent() != "next" || next.jsonSchema == nil || *next.jsonSchema {
		t.Fatalf("next request did not observe new model: %+v response=%q", next, resp.Message.TextContent())
	}
}

func TestFailoverStartAttemptDoesNotBufferTerminalErrors(t *testing.T) {
	for _, terminal := range []error{context.Canceled, agentcore.ErrProviderAuth, agentcore.ErrProviderRateLimit, io.ErrUnexpectedEOF, errors.New("invalid schema")} {
		t.Run(terminal.Error(), func(t *testing.T) {
			var generated atomic.Int64
			stub := &streamPolicyModel{
				generate: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
					generated.Add(1)
					return policyResponse("must not generate"), nil
				},
				stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
					return nil, terminal
				},
			}
			failover := &failoverModel{}
			_, resp, err := failover.startAttempt(t.Context(), modelTarget{model: stub}, nil, nil)
			if !errors.Is(err, terminal) || resp != nil || generated.Load() != 0 {
				t.Fatalf("terminal stream initialization failure was replayed: err=%v generated=%d", err, generated.Load())
			}
		})
	}
}
