package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

func TestStreamSnapshotsDoNotReadAdapterMutablePartials(t *testing.T) {
	source := make(chan agentcore.StreamEvent, 100)
	go func() {
		defer close(source)
		// Match the adapter's shallow snapshots and concurrent in-place writes.
		shared := agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("")}}
		source <- agentcore.StreamEvent{Type: agentcore.StreamEventTextStart, Message: shared}
		for index := 0; index < 200; index++ {
			shared.Content[0].Text += "字"
			source <- agentcore.StreamEvent{Type: agentcore.StreamEventTextDelta, Delta: "字", Message: shared}
		}
		source <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: shared}
	}()
	var first agentcore.Message
	count := 0
	for event := range snapshotModelStream(t.Context(), source) {
		if event.Type == agentcore.StreamEventTextDelta {
			count++
			if event.Message.TextContent() != strings.Repeat("字", count) {
				t.Fatalf("partial snapshot did not match its own delta: %q", event.Message.TextContent())
			}
			if count == 1 {
				first = event.Message
			}
		}
	}
	if count != 200 || first.TextContent() != "字" {
		t.Fatalf("earlier snapshot changed after publication: count=%d first=%q", count, first.TextContent())
	}
}

func TestStreamSnapshotsPreserveThinkingParallelToolsAndFinalMetadata(t *testing.T) {
	first := agentcore.ToolCall{ID: "first", Name: "novel_context", Args: json.RawMessage(`{"chapter":1}`)}
	second := agentcore.ToolCall{ID: "second", Name: "read_chapter", Args: json.RawMessage(`{"chapter":2}`)}
	final := agentcore.Message{
		Role:       agentcore.RoleAssistant,
		Content:    []agentcore.ContentBlock{agentcore.TextBlock("text"), agentcore.ThinkingBlock("think"), agentcore.ToolCallBlock(first), agentcore.ToolCallBlock(second)},
		Usage:      &agentcore.Usage{Input: 7, Output: 11, Cost: &agentcore.Cost{Total: 0.25}},
		Metadata:   map[string]any{"nested": map[string]any{"key": "original"}},
		StopReason: agentcore.StopReasonToolUse,
	}
	events := []agentcore.StreamEvent{
		{Type: agentcore.StreamEventTextStart, ContentIndex: 0},
		{Type: agentcore.StreamEventTextDelta, ContentIndex: 0, Delta: "text"},
		{Type: agentcore.StreamEventThinkingStart, ContentIndex: 1},
		{Type: agentcore.StreamEventThinkingDelta, ContentIndex: 1, Delta: "think"},
		{Type: agentcore.StreamEventToolCallStart, ToolID: first.ID},
		{Type: agentcore.StreamEventToolCallDelta, ToolID: first.ID, Delta: `{"chapter":`},
		{Type: agentcore.StreamEventToolCallStart, ToolID: second.ID},
		{Type: agentcore.StreamEventToolCallDelta, ToolID: second.ID, Delta: string(second.Args)},
		{Type: agentcore.StreamEventToolCallDelta, ToolID: first.ID, Delta: `1}`},
		{Type: agentcore.StreamEventToolCallEnd, ToolID: first.ID, ContentIndex: 2, CompletedToolCall: &first},
		{Type: agentcore.StreamEventToolCallEnd, ToolID: second.ID, ContentIndex: 3, CompletedToolCall: &second},
		{Type: agentcore.StreamEventDone, Message: final, StopReason: agentcore.StopReasonToolUse},
	}
	source := make(chan agentcore.StreamEvent, len(events))
	for _, event := range events {
		source <- event
	}
	close(source)
	var snapshots []agentcore.StreamEvent
	for event := range snapshotModelStream(t.Context(), source) {
		snapshots = append(snapshots, event)
	}
	end := snapshots[len(snapshots)-2]
	if end.Message.TextContent() != "text" || end.Message.ThinkingContent() != "think" {
		t.Fatalf("live text/thinking lost: %+v", end.Message)
	}
	calls := end.Message.ToolCalls()
	if len(calls) != 2 || calls[0].Name != first.Name || string(calls[0].Args) != string(first.Args) || calls[1].Name != second.Name {
		t.Fatalf("parallel tool calls lost their index or arguments: %+v", calls)
	}
	// Consumers may retain/mutate a delivered value without corrupting the
	// adapter, another event, completed-tool payload, usage, or metadata.
	end.Message.Content[2].ToolCall.Args[0] = '['
	if string(snapshots[len(snapshots)-3].Message.ToolCalls()[0].Args) != string(first.Args) {
		t.Fatal("successive partial snapshots share mutable tool arguments")
	}
	done := snapshots[len(snapshots)-1]
	done.Message.Usage.Cost.Total = 1
	done.Message.Metadata["nested"].(map[string]any)["key"] = "changed"
	done.Message.Content[2].ToolCall.Args[0] = '['
	if final.Usage.Cost.Total != 0.25 || final.Metadata["nested"].(map[string]any)["key"] != "original" || string(first.Args) != `{"chapter":1}` {
		t.Fatal("final snapshots share mutable provider state")
	}
	if done.StopReason != agentcore.StopReasonToolUse || done.Message.Usage.Input != 7 {
		t.Fatal("final stop reason or token usage changed")
	}
}

func TestStreamSnapshotsDrainOnCancellationAndPreserveErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	source := make(chan agentcore.StreamEvent, 2)
	producerDone := make(chan struct{})
	go func() {
		defer close(source)
		defer close(producerDone)
		for index := 0; index < 200; index++ {
			source <- agentcore.StreamEvent{Type: agentcore.StreamEventTextDelta, Delta: "x"}
		}
	}()
	stream := snapshotModelStream(ctx, source)
	cancel()
	for range stream {
	}
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("cancelled bridge left the adapter producer blocked")
	}
	expected := errors.New("upstream interrupted")
	failed := make(chan agentcore.StreamEvent, 1)
	failed <- agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: expected}
	close(failed)
	event := <-snapshotModelStream(t.Context(), failed)
	if !errors.Is(event.Err, expected) || event.Type != agentcore.StreamEventError {
		t.Fatalf("provider error changed: %+v", event)
	}
}
