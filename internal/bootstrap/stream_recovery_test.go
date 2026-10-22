package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/litellm"
)

type streamRecoveryModel struct {
	generate func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error)
	stream   func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error)
}

func (m *streamRecoveryModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return m.generate(ctx, messages, tools, opts...)
}

func (m *streamRecoveryModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return m.stream(ctx, messages, tools, opts...)
}

func (m *streamRecoveryModel) SupportsTools() bool { return true }

func recoveryResponse(text string) *agentcore.LLMResponse {
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role: agentcore.RoleAssistant, StopReason: agentcore.StopReasonStop,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(text)},
	}}
}

func recoveryEvents(events ...agentcore.StreamEvent) <-chan agentcore.StreamEvent {
	out := make(chan agentcore.StreamEvent, len(events))
	for _, event := range events {
		out <- event
	}
	close(out)
	return out
}

func collectRecoveryEvents(t *testing.T, ctx context.Context, model agentcore.ChatModel, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) []agentcore.StreamEvent {
	t.Helper()
	stream, err := model.GenerateStream(ctx, messages, tools, opts...)
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

func TestStreamRecoveryRepairsTruncatedOpenAITransport(t *testing.T) {
	for _, truncatedHTTP := range []bool{true, false} {
		name := map[bool]string{true: "unexpected_eof", false: "missing_done"}[truncatedHTTP]
		t.Run(name, func(t *testing.T) {
			requests := make(chan map[string]any, 4)
			partialChunk, err := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{
					"tool_calls": []any{map[string]any{
						"index": 0, "id": "abandoned_call", "type": "function",
						"function": map[string]any{"name": "draft_chapter", "arguments": `{"chapter":7,"content":"半截旧正文`},
					}},
				}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				requests <- payload
				if payload["stream"] == true {
					body := "data: " + string(partialChunk) + "\n\n"
					w.Header().Set("Content-Type", "text/event-stream")
					if truncatedHTTP {
						// Advertising more bytes than are sent makes net/http surface
						// io.ErrUnexpectedEOF rather than a clean body EOF.
						w.Header().Set("Content-Length", strconv.Itoa(len(body)+100))
					}
					_, _ = io.WriteString(w, body)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"recovered","object":"chat.completion","model":"gpt-4.1","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"final_call","type":"function","function":{"name":"draft_chapter","arguments":"{\"chapter\":7,\"content\":\"完整新正文\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`)
			}))
			defer server.Close()
			model, err := createModelFromConfig("stub", "gpt-4.1", ProviderConfig{
				Type: "openai", APIKey: "test-key", BaseURL: server.URL,
			}, 4096, make(map[string]agentcore.ChatModel))
			if err != nil {
				t.Fatal(err)
			}
			messages := []agentcore.Message{agentcore.UserMsg("生成第七章")}
			schema := map[string]any{"type": "object", "properties": map[string]any{
				"chapter": map[string]any{"type": "integer"}, "content": map[string]any{"type": "string"},
			}, "required": []string{"chapter", "content"}, "additionalProperties": false}
			tools := []agentcore.ToolSpec{{Name: "draft_chapter", Description: "保存章节", Parameters: schema}}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			var progress []agentcore.ProgressPayload
			ctx = agentcore.WithToolProgress(ctx, func(payload agentcore.ProgressPayload) { progress = append(progress, payload) })
			events := collectRecoveryEvents(t, ctx, model, messages, tools,
				agentcore.WithMaxTokens(777), agentcore.WithToolChoice("required"),
				agentcore.WithJSONSchema("chapter", "章节结果", schema, true))
			var done []agentcore.StreamEvent
			for _, event := range events {
				if event.Type == agentcore.StreamEventError {
					t.Fatalf("recoverable transport error escaped: %v", event.Err)
				}
				if event.Type == agentcore.StreamEventDone {
					done = append(done, event)
				}
			}
			if len(done) != 1 || done[0].StopReason != agentcore.StopReasonToolUse {
				t.Fatalf("expected one authoritative tool response, got %+v", done)
			}
			calls := done[0].Message.ToolCalls()
			if len(calls) != 1 || calls[0].ID != "final_call" || calls[0].Name != "draft_chapter" || string(calls[0].Args) != `{"chapter":7,"content":"完整新正文"}` {
				t.Fatalf("partial tool call contaminated recovery: %+v", calls)
			}
			if len(progress) != 1 || progress[0].Kind != agentcore.ProgressRetry {
				t.Fatalf("recovery should report exactly one retry: %+v", progress)
			}
			if len(requests) != 2 {
				t.Fatalf("request count = %d, want one stream and one complete response", len(requests))
			}
			streamRequest, completeRequest := <-requests, <-requests
			if streamRequest["stream"] != true || completeRequest["stream"] == true {
				t.Fatalf("request modes = %v / %v", streamRequest["stream"], completeRequest["stream"])
			}
			for _, field := range []string{"model", "messages", "tools", "max_tokens", "tool_choice", "response_format"} {
				if streamRequest[field] == nil || !reflect.DeepEqual(streamRequest[field], completeRequest[field]) {
					t.Errorf("recovery changed request field %q: %#v / %#v", field, streamRequest[field], completeRequest[field])
				}
			}
			if completeRequest["max_tokens"] != float64(777) {
				t.Fatalf("explicit output budget changed: %v", completeRequest["max_tokens"])
			}
		})
	}
}

func TestStreamRecoveryEligibility(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"unexpected_eof", fmt.Errorf("stream read: %w", io.ErrUnexpectedEOF), true},
		{"eof", io.EOF, true},
		{"network", agentcore.ErrProviderNetwork, true},
		{"idle", agentcore.ErrProviderStreamIdle, true},
		{"timeout", agentcore.ErrProviderTimeout, true},
		{"partial", &agentcore.PartialStreamError{}, true},
		{"missing_done", litellm.NewProviderError("openai", litellm.ErrorTypeProvider, "openai: stream ended before [DONE]"), true},
		{"canceled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"auth", litellm.NewHTTPError("openai", 401, "invalid API key"), false},
		{"rate_limit", litellm.NewHTTPError("openai", 429, "rate limit"), false},
		{"validation", litellm.NewError(litellm.ErrorTypeValidation, "invalid schema"), false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := recoverableStreamError(tc.err); got != tc.want {
				t.Fatalf("recoverable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestStreamRecoveryDoesNotReplayTerminalRejections(t *testing.T) {
	for _, terminal := range []error{
		litellm.NewHTTPError("openai", 401, "invalid API key"),
		litellm.NewHTTPError("openai", 429, "rate limit"),
		litellm.NewError(litellm.ErrorTypeValidation, "invalid schema"),
	} {
		for _, initialized := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream_started=%t", terminal.Error(), initialized), func(t *testing.T) {
				var generated atomic.Int64
				stub := &streamRecoveryModel{
					generate: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
						generated.Add(1)
						return recoveryResponse("must not generate"), nil
					},
					stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
						if initialized {
							return recoveryEvents(agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: terminal}), nil
						}
						return nil, terminal
					},
				}
				limited := newOutputLimitedModel(stub, "stub", "model", 256).(*outputLimitedModel)
				stream, err := limited.GenerateStream(t.Context(), nil, nil)
				if initialized {
					if err != nil {
						t.Fatal(err)
					}
					event := <-stream
					err = event.Err
					if event.Type != agentcore.StreamEventError {
						t.Fatalf("terminal stream event = %s", event.Type)
					}
				}
				if !errors.Is(err, terminal) || generated.Load() != 0 || limited.streamFallbackUntil.Load() != 0 {
					t.Fatalf("terminal failure was retried: err=%v generate=%d cooldown=%d", err, generated.Load(), limited.streamFallbackUntil.Load())
				}
			})
		}
	}
}

func TestStreamRecoveryPreservesFinalFailureAndStops(t *testing.T) {
	for _, finalErr := range []error{agentcore.ErrProviderAuth, agentcore.ErrProviderRateLimit, errors.New("invalid response schema")} {
		t.Run(finalErr.Error(), func(t *testing.T) {
			var generated atomic.Int64
			stub := &streamRecoveryModel{
				generate: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
					generated.Add(1)
					return nil, finalErr
				},
				stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
					return recoveryEvents(agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: io.ErrUnexpectedEOF}), nil
				},
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			events := collectRecoveryEvents(t, ctx, newOutputLimitedModel(stub, "stub", "model", 256), nil, nil)
			if generated.Load() != 1 || len(events) != 1 || events[0].Type != agentcore.StreamEventError || !errors.Is(events[0].Err, finalErr) {
				t.Fatalf("recovery did not surface final failure once: generated=%d events=%+v", generated.Load(), events)
			}
			if strings.Contains(strings.ToLower(events[0].Err.Error()), "eof") || errors.Is(events[0].Err, io.ErrUnexpectedEOF) {
				t.Fatalf("earlier network failure masked final rejection: %v", events[0].Err)
			}
		})
	}
}

func TestStreamRecoveryCooldownIsScopedAndProbesAfterExpiry(t *testing.T) {
	var streamed, generated atomic.Int64
	stub := &streamRecoveryModel{
		generate: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
			generated.Add(1)
			return recoveryResponse("complete"), nil
		},
		stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
			if streamed.Add(1) == 1 {
				return nil, io.ErrUnexpectedEOF
			}
			return recoveryEvents(agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: recoveryResponse("streamed").Message}), nil
		},
	}
	limited := newOutputLimitedModel(stub, "stub", "model", 256).(*outputLimitedModel)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for range 2 {
		events := collectRecoveryEvents(t, ctx, limited, nil, nil)
		if len(events) != 1 || events[0].Message.TextContent() != "complete" {
			t.Fatalf("expected buffered recovery/cooldown response: %+v", events)
		}
	}
	if streamed.Load() != 1 || generated.Load() != 2 || time.Unix(0, limited.streamFallbackUntil.Load()).Before(time.Now().Add(4*time.Minute)) {
		t.Fatalf("cooldown did not avoid the broken stream: streamed=%d generated=%d", streamed.Load(), generated.Load())
	}
	// Even matching provider/model names belong to an independent instance after
	// reconfiguration; one endpoint's cooldown must not affect another instance.
	independent := newOutputLimitedModel(stub, "stub", "model", 256)
	events := collectRecoveryEvents(t, ctx, independent, nil, nil)
	if events[0].Message.TextContent() != "streamed" || generated.Load() != 2 {
		t.Fatal("cooldown leaked into a different model instance")
	}
	limited.streamFallbackUntil.Store(time.Now().Add(-time.Second).UnixNano())
	events = collectRecoveryEvents(t, ctx, limited, nil, nil)
	if events[0].Message.TextContent() != "streamed" || streamed.Load() != 3 || generated.Load() != 2 {
		t.Fatalf("expired cooldown did not probe streaming: streamed=%d generated=%d", streamed.Load(), generated.Load())
	}
}

func TestStreamRecoveryNormalCompletionDoesNotGenerate(t *testing.T) {
	var generated atomic.Int64
	stub := &streamRecoveryModel{
		generate: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
			generated.Add(1)
			return nil, errors.New("unexpected Generate")
		},
		stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
			return recoveryEvents(
				agentcore.StreamEvent{Type: agentcore.StreamEventTextDelta, Delta: "normal"},
				agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: recoveryResponse("normal").Message},
			), nil
		},
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	limited := newOutputLimitedModel(stub, "stub", "model", 256).(*outputLimitedModel)
	events := collectRecoveryEvents(t, ctx, limited, nil, nil)
	if len(events) != 2 || events[0].Delta != "normal" || events[1].Message.TextContent() != "normal" || generated.Load() != 0 || limited.streamFallbackUntil.Load() != 0 {
		t.Fatalf("healthy stream was changed: generated=%d events=%+v", generated.Load(), events)
	}
}

func TestStreamRecoveryClosedWithoutDoneUsesCompleteResponse(t *testing.T) {
	for _, hasDelta := range []bool{false, true} {
		t.Run(fmt.Sprintf("has_delta=%t", hasDelta), func(t *testing.T) {
			var generated atomic.Int64
			stub := &streamRecoveryModel{
				generate: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
					generated.Add(1)
					return recoveryResponse("complete"), nil
				},
				stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
					if hasDelta {
						return recoveryEvents(agentcore.StreamEvent{Type: agentcore.StreamEventTextDelta, Delta: "partial"}), nil
					}
					return recoveryEvents(), nil
				},
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			events := collectRecoveryEvents(t, ctx, newOutputLimitedModel(stub, "stub", "model", 256), nil, nil)
			last := events[len(events)-1]
			if generated.Load() != 1 || last.Type != agentcore.StreamEventDone || last.Message.TextContent() != "complete" {
				t.Fatalf("closed stream accepted partial output: generated=%d events=%+v", generated.Load(), events)
			}
		})
	}
}

type recoveryCountingTool struct {
	calls atomic.Int64
	args  chan string
}

func (*recoveryCountingTool) Name() string        { return "save_chapter" }
func (*recoveryCountingTool) Description() string { return "保存章节" }
func (*recoveryCountingTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"content": map[string]any{"type": "string"}}, "required": []string{"content"}}
}
func (tool *recoveryCountingTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	tool.calls.Add(1)
	tool.args <- string(args)
	return json.RawMessage(`"saved"`), nil
}

func TestStreamRecoveryRunnerCommitsOnlyFinalToolCallOnce(t *testing.T) {
	oldCall := agentcore.ToolCall{ID: "abandoned", Name: "save_chapter", Args: json.RawMessage(`{"content":"旧内容"}`)}
	finalCall := agentcore.ToolCall{ID: "authoritative", Name: "save_chapter", Args: json.RawMessage(`{"content":"完整新内容"}`)}
	var generated atomic.Int64
	stub := &streamRecoveryModel{
		generate: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
			generated.Add(1)
			return &agentcore.LLMResponse{Message: agentcore.Message{
				Role: agentcore.RoleAssistant, StopReason: agentcore.StopReasonToolUse,
				Content: []agentcore.ContentBlock{agentcore.ToolCallBlock(finalCall)},
			}}, nil
		},
		stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
			// Even a locally complete tool call is provisional until the entire
			// assistant response reaches Done; it must never execute on EOF.
			return recoveryEvents(
				agentcore.StreamEvent{Type: agentcore.StreamEventToolCallStart, ToolID: oldCall.ID},
				agentcore.StreamEvent{Type: agentcore.StreamEventToolCallDelta, ToolID: oldCall.ID, Delta: string(oldCall.Args)},
				agentcore.StreamEvent{Type: agentcore.StreamEventToolCallEnd, ToolID: oldCall.ID, CompletedToolCall: &oldCall},
				agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: io.ErrUnexpectedEOF},
			), nil
		},
	}
	tool := &recoveryCountingTool{args: make(chan string, 2)}
	var committedCalls []agentcore.ToolCall
	runner := subagent.NewRunner(subagent.Config{
		Name: "writer", Model: newOutputLimitedModel(stub, "stub", "model", 256),
		Tools: []agentcore.Tool{tool}, MaxTurns: 2, MaxRetries: 0,
		StopAfterTools: []string{tool.Name()},
		OnMessage: func(_, _ string, message agentcore.AgentMessage) {
			if msg, ok := message.(agentcore.Message); ok && msg.Role == agentcore.RoleAssistant {
				committedCalls = append(committedCalls, msg.ToolCalls()...)
			}
		},
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := runner.Run(ctx, "writer", "保存章节")
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls.Load() != 1 || len(tool.args) != 1 || <-tool.args != string(finalCall.Args) || generated.Load() != 1 {
		t.Fatalf("recovery replayed or mixed tool side effects: tools=%d generates=%d", tool.calls.Load(), generated.Load())
	}
	if len(committedCalls) != 1 || committedCalls[0].ID != finalCall.ID || string(result.TerminalResult) != `"saved"` {
		t.Fatalf("runner committed provisional tool output: calls=%+v result=%s", committedCalls, result.TerminalResult)
	}
}

func TestStreamRecoveryCancellationDoesNotStartAnotherRequest(t *testing.T) {
	for _, preCanceled := range []bool{true, false} {
		t.Run(fmt.Sprintf("pre_canceled=%t", preCanceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var generated, streamed atomic.Int64
			producerDone := make(chan struct{})
			stub := &streamRecoveryModel{
				generate: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
					generated.Add(1)
					return recoveryResponse("must not generate"), nil
				},
				stream: func(streamCtx context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
					streamed.Add(1)
					out := make(chan agentcore.StreamEvent, 1)
					out <- agentcore.StreamEvent{Type: agentcore.StreamEventTextDelta, Delta: "partial"}
					go func() {
						defer close(out)
						defer close(producerDone)
						<-streamCtx.Done()
					}()
					return out, nil
				},
			}
			limited := newOutputLimitedModel(stub, "stub", "model", 256).(*outputLimitedModel)
			if preCanceled {
				cancel()
				_, err := limited.GenerateStream(ctx, nil, nil)
				if !errors.Is(err, context.Canceled) || streamed.Load() != 0 {
					t.Fatalf("pre-canceled request was sent: err=%v streams=%d", err, streamed.Load())
				}
			} else {
				stream, err := limited.GenerateStream(ctx, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				<-stream
				cancel()
				select {
				case <-producerDone:
				case <-time.After(time.Second):
					t.Fatal("cancellation did not release the stream producer")
				}
				for event := range stream {
					if event.Type == agentcore.StreamEventDone {
						t.Fatal("canceled partial was promoted to completion")
					}
				}
			}
			if generated.Load() != 0 || limited.streamFallbackUntil.Load() != 0 {
				t.Fatal("cancellation started buffered recovery or changed cooldown")
			}
		})
	}
}

func TestStreamRecoveryCancellationDuringBufferedRequestDiscardsSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, returned := make(chan struct{}), make(chan struct{})
	stub := &streamRecoveryModel{
		generate: func(ctx context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
			close(started)
			<-ctx.Done()
			defer close(returned)
			// Simulate a provider returning a complete message concurrently with
			// cancellation; it still must not become a committed assistant turn.
			return recoveryResponse("late success"), nil
		},
		stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
			return nil, io.ErrUnexpectedEOF
		},
	}
	stream, err := newOutputLimitedModel(stub, "stub", "model", 256).GenerateStream(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("buffered request did not start")
	}
	cancel()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach the buffered request")
	}
	for event := range stream {
		if event.Type == agentcore.StreamEventDone {
			t.Fatal("canceled buffered request committed a late success")
		}
	}
}

func TestStreamRecoveryConcurrentCallsShareCooldownSafely(t *testing.T) {
	const calls = 24
	var generated, streamed atomic.Int64
	stub := &streamRecoveryModel{
		generate: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
			generated.Add(1)
			return recoveryResponse("complete"), nil
		},
		stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
			streamed.Add(1)
			return recoveryEvents(agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: io.ErrUnexpectedEOF}), nil
		},
	}
	limited := newOutputLimitedModel(stub, "stub", "model", 256)
	var workers sync.WaitGroup
	errorsSeen := make(chan error, calls)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for range calls {
		workers.Go(func() {
			stream, err := limited.GenerateStream(ctx, nil, nil)
			if err != nil {
				errorsSeen <- err
				return
			}
			done := 0
			for event := range stream {
				if event.Err != nil {
					errorsSeen <- event.Err
					return
				}
				if event.Type == agentcore.StreamEventDone && event.Message.TextContent() == "complete" {
					done++
				}
			}
			if done != 1 {
				errorsSeen <- fmt.Errorf("completion count = %d", done)
			}
		})
	}
	workers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
	if generated.Load() != calls || streamed.Load() < 1 || streamed.Load() > calls {
		t.Fatalf("unexpected concurrent request counts: generated=%d streamed=%d", generated.Load(), streamed.Load())
	}
}

func TestFailoverTargetKeepsInFlightModelAfterSwap(t *testing.T) {
	fixed := func(name string) *streamRecoveryModel {
		return &streamRecoveryModel{generate: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
			return recoveryResponse(name), nil
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
	for _, terminal := range []error{context.Canceled, agentcore.ErrProviderAuth, agentcore.ErrProviderRateLimit, errors.New("invalid schema")} {
		t.Run(terminal.Error(), func(t *testing.T) {
			var generated atomic.Int64
			stub := &streamRecoveryModel{
				generate: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
					generated.Add(1)
					return recoveryResponse("must not generate"), nil
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
