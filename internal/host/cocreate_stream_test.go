package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/store"
)

type coCreateEventsModel struct {
	events        []agentcore.StreamEvent
	streamCalls   int
	generateCalls int
}

func (m *coCreateEventsModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.generateCalls++
	return nil, fmt.Errorf("unexpected direct generation")
}

func (m *coCreateEventsModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.streamCalls++
	ch := make(chan agentcore.StreamEvent, len(m.events))
	for _, ev := range m.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func (*coCreateEventsModel) SupportsTools() bool { return false }

func coCreateTestModels(m *coCreateEventsModel) *bootstrap.ModelSet {
	return &bootstrap.ModelSet{Default: bootstrap.NewSwappableModel("test", "cocreate", m, nil)}
}

func coCreateTestResponse(reply, draft string, ready bool) string {
	return fmt.Sprintf("<reply>%s</reply><draft>%s</draft><ready>%t</ready><suggestions></suggestions>", reply, draft, ready)
}

func TestCoCreateStreamUsesAuthoritativeDone(t *testing.T) {
	stale := coCreateTestResponse("旧回复", "旧草稿", true)
	final := coCreateTestResponse("新回复", "新草稿", false)
	tests := []struct {
		name          string
		deltas        []agentcore.StreamEvent
		finalText     string
		finalThinking string
	}{
		{
			name: "recovered response replaces stale preview",
			deltas: []agentcore.StreamEvent{
				{Type: agentcore.StreamEventTextDelta, Delta: stale},
				{Type: agentcore.StreamEventThinkingDelta, Delta: "旧思考"},
			},
			finalText: final, finalThinking: "重取后的思考",
		},
		{
			name:      "done only response",
			finalText: final, finalThinking: "非流式思考",
		},
		{
			name:      "matching streamed text is not duplicated",
			deltas:    []agentcore.StreamEvent{{Type: agentcore.StreamEventTextDelta, Delta: final}},
			finalText: final,
		},
		{
			name:      "empty final thinking clears stale thinking",
			deltas:    []agentcore.StreamEvent{{Type: agentcore.StreamEventThinkingDelta, Delta: "旧思考"}},
			finalText: final,
		},
		{
			name:          "final thinking answer replaces stale text",
			deltas:        []agentcore.StreamEvent{{Type: agentcore.StreamEventTextDelta, Delta: stale}},
			finalThinking: final,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := store.NewStore(t.TempDir())
			if err := st.Init(); err != nil {
				t.Fatal(err)
			}
			model := &coCreateEventsModel{events: append(tt.deltas, agentcore.StreamEvent{
				Type: agentcore.StreamEventDone,
				Message: agentcore.Message{Role: agentcore.RoleAssistant, StopReason: agentcore.StopReasonStop, Content: []agentcore.ContentBlock{
					agentcore.TextBlock(tt.finalText), agentcore.ThinkingBlock(tt.finalThinking),
				}},
			})}
			progress := make(map[string]string)
			got, err := coCreateStream(context.Background(), coCreateTestModels(model), st.Sessions, coCreateSystemPrompt,
				[]CoCreateMessage{{Role: "user", Content: "继续共创"}}, func(kind, text string) { progress[kind] = text })
			if err != nil {
				t.Fatal(err)
			}
			if got.Message != "新回复" || got.Prompt != "新草稿" || got.Ready || got.Raw != final {
				t.Fatalf("must use only the complete final response: %+v", got)
			}
			if progress[CoCreateProgressReply] != "新回复" {
				t.Fatalf("stale reply preview survived final response: %q", progress[CoCreateProgressReply])
			}
			if progress[CoCreateProgressThinking] != tt.finalThinking {
				t.Fatalf("stale thinking survived final response: %q", progress[CoCreateProgressThinking])
			}
			data, err := os.ReadFile(filepath.Join(st.Dir(), "meta", "sessions", "cocreate.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var logged coCreateLogEntry
			if err := json.Unmarshal(data, &logged); err != nil {
				t.Fatal(err)
			}
			if logged.RawResponse != tt.finalText || logged.Thinking != tt.finalThinking || logged.ParsedReady {
				t.Fatalf("session must record authoritative response only: %+v", logged)
			}
		})
	}
}

func TestCoCreateStreamRejectsIncompleteResponse(t *testing.T) {
	raw := coCreateTestResponse("可以开始", "不能采用的草稿", true)
	tests := []struct {
		name    string
		events  []agentcore.StreamEvent
		wantErr string
		partial bool
	}{
		{
			name:    "missing done with apparently ready content",
			events:  []agentcore.StreamEvent{{Type: agentcore.StreamEventTextDelta, Delta: raw}},
			partial: true,
		},
		{
			name: "length in final message",
			events: []agentcore.StreamEvent{{Type: agentcore.StreamEventDone, Message: agentcore.Message{
				Content: []agentcore.ContentBlock{agentcore.TextBlock(raw)}, StopReason: agentcore.StopReasonLength,
			}}},
			wantErr: "stop_reason=length",
		},
		{
			name: "length on done event",
			events: []agentcore.StreamEvent{{Type: agentcore.StreamEventDone, StopReason: agentcore.StopReasonLength,
				Message: agentcore.Message{Content: []agentcore.ContentBlock{agentcore.TextBlock(raw)}}}},
			wantErr: "stop_reason=length",
		},
		{
			name:    "empty authoritative final must not reuse stale ready response",
			events:  []agentcore.StreamEvent{{Type: agentcore.StreamEventTextDelta, Delta: raw}, {Type: agentcore.StreamEventDone}},
			wantErr: "empty response",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := &coCreateEventsModel{events: tt.events}
			got, err := coCreateStream(context.Background(), coCreateTestModels(model), nil, coCreateSystemPrompt,
				[]CoCreateMessage{{Role: "user", Content: "继续共创"}}, nil)
			if err == nil || tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("incomplete response accepted or wrong error: reply=%+v, err=%v", got, err)
			}
			if tt.partial && !errors.Is(err, agentcore.ErrStreamPartial) {
				t.Fatalf("missing Done must preserve stream-partial identity: %v", err)
			}
			if got.Ready || got.Prompt != "" || got.Message != "" {
				t.Fatalf("incomplete response must not update draft or readiness: %+v", got)
			}
		})
	}
}

func TestCoCreateStreamCancellationDoesNotPublishDraftOrRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := coCreateTestResponse("可以开始", "不能采用的草稿", true)
	model := &coCreateEventsModel{events: []agentcore.StreamEvent{
		{Type: agentcore.StreamEventTextDelta, Delta: raw},
		{Type: agentcore.StreamEventDone, Message: testTextMsg(raw)},
	}}
	got, err := coCreateStream(ctx, coCreateTestModels(model), nil, coCreateSystemPrompt,
		[]CoCreateMessage{{Role: "user", Content: "继续共创"}}, func(_, _ string) { cancel() })
	if !errors.Is(err, context.Canceled) || got.Ready || got.Prompt != "" {
		t.Fatalf("cancellation must preserve the previous draft: reply=%+v, err=%v", got, err)
	}
	if model.streamCalls != 1 || model.generateCalls != 0 {
		t.Fatalf("cancellation must not retry: stream=%d, generate=%d", model.streamCalls, model.generateCalls)
	}
}
