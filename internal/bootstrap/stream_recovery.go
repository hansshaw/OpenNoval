package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/voocel/agentcore"
)

// A broken SSE connection often reflects a gateway's streaming implementation,
// rather than invalid model output. Recover once with the same request in
// buffered mode, then avoid that model instance's stream for a short cooldown.
// No partial text or tool arguments are promoted to a completed response.
const streamFallbackCooldown = 5 * time.Minute

func recoverableStreamError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) ||
		errors.Is(err, agentcore.ErrProviderNetwork) ||
		errors.Is(err, agentcore.ErrProviderStreamIdle) ||
		errors.Is(err, agentcore.ErrProviderTimeout) ||
		errors.Is(err, agentcore.ErrStreamPartial) {
		return true
	}
	// The OpenAI and compatible adapters classify this exact missing-terminal
	// marker as a provider error, even when the underlying HTTP body ends cleanly.
	return strings.Contains(err.Error(), "stream ended before [DONE]")
}

func (m *outputLimitedModel) streamWithRecovery(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if time.Now().UnixNano() < m.streamFallbackUntil.Load() {
		return m.bufferedModelStream(ctx, messages, tools, opts...), nil
	}
	streamCtx, cancel := context.WithCancel(ctx)
	source, err := m.ChatModel.GenerateStream(streamCtx, messages, tools, opts...)
	if err != nil {
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !recoverableStreamError(err) {
			return nil, err
		}
		m.reportStreamRecovery(ctx, err)
		return m.bufferedModelStream(ctx, messages, tools, opts...), nil
	}
	if source == nil {
		cancel()
		return nil, errors.New("model returned a nil stream")
	}
	stable := snapshotModelStream(streamCtx, source)
	out := make(chan agentcore.StreamEvent)
	go func() {
		defer close(out)
		defer cancel()
		var streamErr error
		for event := range stable {
			if event.Type == agentcore.StreamEventError {
				streamErr = event.Err
				if streamErr == nil {
					streamErr = errors.New("model stream failed without an error")
				}
				break
			}
			if !sendModelEvent(ctx, out, event) || event.Type == agentcore.StreamEventDone {
				return
			}
		}
		cancel() // Release the abandoned stream before starting another request.
		if ctx.Err() != nil {
			return
		}
		if streamErr == nil {
			streamErr = fmt.Errorf("model stream closed without completion: %w", io.ErrUnexpectedEOF)
		}
		if !recoverableStreamError(streamErr) {
			sendModelEvent(ctx, out, agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: streamErr})
			return
		}
		m.reportStreamRecovery(ctx, streamErr)
		event := m.bufferedModelResponse(ctx, messages, tools, opts...)
		if event.Err != nil {
			// Classify the final failure (e.g. auth/429) using its own typed cause,
			// rather than letting the earlier EOF mask a terminal rejection.
			event.Err = fmt.Errorf("non-stream recovery failed: %w", event.Err)
		}
		sendModelEvent(ctx, out, event)
	}()
	return out, nil
}

func (m *outputLimitedModel) reportStreamRecovery(ctx context.Context, err error) {
	m.streamFallbackUntil.Store(time.Now().Add(streamFallbackCooldown).UnixNano())
	slog.Warn("流式传输中断，改用非流式重新获取完整响应", "module", "stream_recovery", "provider", m.provider, "model", m.model, "cooldown", streamFallbackCooldown, "err", err)
	agentcore.ReportToolProgress(ctx, agentcore.ProgressPayload{
		Kind: agentcore.ProgressRetry, Attempt: 1, MaxRetries: 1,
		Meta:    json.RawMessage(`{"stream_recovery":true}`),
		Message: fmt.Sprintf("%s / %s 流连接中断，正在重新获取完整响应；此连接暂时改为完整响应模式。", m.provider, m.model),
	})
}

func (m *outputLimitedModel) bufferedModelStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) <-chan agentcore.StreamEvent {
	out := make(chan agentcore.StreamEvent)
	go func() {
		defer close(out)
		sendModelEvent(ctx, out, m.bufferedModelResponse(ctx, messages, tools, opts...))
	}()
	return out
}

func (m *outputLimitedModel) bufferedModelResponse(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) agentcore.StreamEvent {
	if err := ctx.Err(); err != nil {
		return agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: err}
	}
	resp, err := m.ChatModel.Generate(ctx, messages, tools, opts...)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: err}
	}
	if resp == nil {
		return agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: errors.New("non-stream recovery returned no response")}
	}
	return agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: cloneStreamMessage(resp.Message), StopReason: resp.Message.StopReason}
}

func sendModelEvent(ctx context.Context, out chan<- agentcore.StreamEvent, event agentcore.StreamEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- event:
		return true
	}
}
