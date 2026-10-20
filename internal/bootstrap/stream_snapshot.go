package bootstrap

import (
	"context"
	"encoding/json"

	"github.com/voocel/agentcore"
)

// snapshotModelStream shields callers from agentcore v1.8.3's adapter, which
// reuses a mutable Content slice in intermediate StreamEvent.Message values.
// Copying those values on receipt is itself a race with the adapter's next
// write. Build live snapshots from immutable deltas instead; only Done carries
// a complete message that the producer has finished modifying.
func snapshotModelStream(ctx context.Context, source <-chan agentcore.StreamEvent) <-chan agentcore.StreamEvent {
	out := make(chan agentcore.StreamEvent)
	go func() {
		defer close(out)
		partial := agentcore.Message{Role: agentcore.RoleAssistant}
		toolIndices := make(map[string]int)
		lastToolIndex := -1
		block := func(index int, kind agentcore.ContentType) *agentcore.ContentBlock {
			for len(partial.Content) <= index {
				partial.Content = append(partial.Content, agentcore.ContentBlock{})
			}
			partial.Content[index].Type = kind
			return &partial.Content[index]
		}
		toolIndex := func(id string) int {
			if index, ok := toolIndices[id]; id != "" && ok {
				return index
			}
			if lastToolIndex < 0 {
				lastToolIndex = len(partial.Content)
			}
			if id != "" {
				toolIndices[id] = lastToolIndex
			}
			return lastToolIndex
		}
		for event := range source {
			switch event.Type {
			case agentcore.StreamEventDone:
				event.Message = cloneStreamMessage(event.Message)
			case agentcore.StreamEventTextStart:
				block(event.ContentIndex, agentcore.ContentText)
			case agentcore.StreamEventTextDelta:
				block(event.ContentIndex, agentcore.ContentText).Text += event.Delta
			case agentcore.StreamEventThinkingStart:
				block(event.ContentIndex, agentcore.ContentThinking)
			case agentcore.StreamEventThinkingDelta:
				block(event.ContentIndex, agentcore.ContentThinking).Thinking += event.Delta
			case agentcore.StreamEventToolCallStart:
				lastToolIndex = len(partial.Content)
				if event.ToolID != "" {
					toolIndices[event.ToolID] = lastToolIndex
				}
				block(lastToolIndex, agentcore.ContentToolCall).ToolCall = &agentcore.ToolCall{ID: event.ToolID}
				event.ContentIndex = lastToolIndex
			case agentcore.StreamEventToolCallDelta:
				index := toolIndex(event.ToolID)
				content := block(index, agentcore.ContentToolCall)
				if content.ToolCall == nil {
					content.ToolCall = &agentcore.ToolCall{ID: event.ToolID}
				}
				content.ToolCall.Args = append(content.ToolCall.Args, event.Delta...)
				event.ContentIndex = index
			case agentcore.StreamEventToolCallEnd:
				if event.CompletedToolCall != nil {
					completed := cloneStreamToolCall(*event.CompletedToolCall)
					index := event.ContentIndex
					if mapped, ok := toolIndices[completed.ID]; ok {
						index = mapped
					}
					block(index, agentcore.ContentToolCall).ToolCall = &completed
					event.CompletedToolCall = &completed
				}
			}
			if event.Type != agentcore.StreamEventDone {
				event.Message = cloneStreamMessage(partial)
			}
			select {
			case out <- event:
			case <-ctx.Done():
				// Keep draining the adapter so its buffered producer can observe
				// cancellation and close, rather than becoming stuck on a send.
				for range source {
				}
				return
			}
		}
	}()
	return out
}

func cloneStreamToolCall(call agentcore.ToolCall) agentcore.ToolCall {
	call.Args = append(json.RawMessage(nil), call.Args...)
	return call
}

func cloneStreamMessage(message agentcore.Message) agentcore.Message {
	message.Content = append([]agentcore.ContentBlock(nil), message.Content...)
	for index := range message.Content {
		content := &message.Content[index]
		if content.ToolCall != nil {
			call := cloneStreamToolCall(*content.ToolCall)
			content.ToolCall = &call
		}
		if content.Image != nil {
			image := *content.Image
			content.Image = &image
		}
	}
	if message.Usage != nil {
		usage := *message.Usage
		if usage.Cost != nil {
			cost := *usage.Cost
			usage.Cost = &cost
		}
		message.Usage = &usage
	}
	if message.Metadata != nil {
		message.Metadata = cloneStreamValue(message.Metadata).(map[string]any)
	}
	return message
}

func cloneStreamValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		copied := make(map[string]any, len(value))
		for key, item := range value {
			copied[key] = cloneStreamValue(item)
		}
		return copied
	case []any:
		copied := make([]any, len(value))
		for index, item := range value {
			copied[index] = cloneStreamValue(item)
		}
		return copied
	case json.RawMessage:
		return append(json.RawMessage(nil), value...)
	case []byte:
		return append([]byte(nil), value...)
	case []string:
		return append([]string(nil), value...)
	default:
		return value
	}
}
