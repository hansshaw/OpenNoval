package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/voocel/agentcore"
)

// Only the Worker retry loop may issue another request. The model wrapper
// preserves streaming, never fills incomplete tool arguments, and supplies a
// conservative delay instead of issuing a hidden buffered recovery request.
const minimumStreamRetryDelay = 15 * time.Second

type streamRetryError struct {
	cause error
	delay time.Duration
}

func (e *streamRetryError) Error() string             { return e.cause.Error() }
func (e *streamRetryError) Unwrap() error             { return e.cause }
func (e *streamRetryError) Retryable() bool           { return true }
func (e *streamRetryError) RetryAfter() time.Duration { return e.delay }

func streamRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, agentcore.ErrProviderAuth) || errors.Is(err, agentcore.ErrProviderQuota) ||
		errors.Is(err, agentcore.ErrProviderContentFilter) || errors.Is(err, agentcore.ErrContextOverflow) {
		return false
	}
	var retryable agentcore.RetryableError
	if errors.As(err, &retryable) && retryable.Retryable() {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) ||
		errors.Is(err, agentcore.ErrProviderNetwork) || errors.Is(err, agentcore.ErrProviderTimeout) ||
		errors.Is(err, agentcore.ErrProviderStreamIdle) || errors.Is(err, agentcore.ErrStreamPartial) ||
		errors.Is(err, agentcore.ErrProviderRateLimit) || errors.Is(err, agentcore.ErrProviderOverloaded) ||
		strings.Contains(err.Error(), "stream ended before [DONE]")
}

func streamRetryDelay(err error) time.Duration {
	delay := minimumStreamRetryDelay
	var hint agentcore.RetryHinter
	if errors.As(err, &hint) && hint.RetryAfter() > delay {
		delay = hint.RetryAfter()
	}
	return delay
}

func (m *outputLimitedModel) streamError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !streamRetryable(err) {
		return err
	}
	delay := streamRetryDelay(err)
	until := time.Now().Add(delay).UnixNano()
	for previous := m.streamRetryNotBefore.Load(); previous < until; previous = m.streamRetryNotBefore.Load() {
		if m.streamRetryNotBefore.CompareAndSwap(previous, until) {
			break
		}
	}
	return &streamRetryError{cause: err, delay: delay}
}

// agentcore caps its Retry-After sleep at 60 seconds. Keep an endpoint gate so
// a longer provider hint remains effective, without delaying the current error
// or creating an extra request. Each subsequent caller can cancel the wait.
func (m *outputLimitedModel) waitForStreamRetry(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		delay := time.Until(time.Unix(0, m.streamRetryNotBefore.Load()))
		if delay <= 0 {
			return nil
		}
		if err := waitForStreamDelay(ctx, delay); err != nil {
			return err
		}
	}
}

func waitForStreamDelay(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (m *outputLimitedModel) streamWithPolicy(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	if err := m.waitForStreamRetry(ctx); err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	source, err := m.ChatModel.GenerateStream(streamCtx, messages, tools, opts...)
	if err != nil {
		cancel()
		return nil, m.streamError(ctx, err)
	}
	if source == nil {
		cancel()
		return nil, errors.New("model returned a nil stream")
	}
	stable := source
	if _, alreadyStable := m.ChatModel.(interface{ streamSnapshotsStable() }); !alreadyStable {
		stable = snapshotModelStream(streamCtx, source)
	}
	out := make(chan agentcore.StreamEvent)
	go func() {
		defer close(out)
		defer cancel()
		for event := range stable {
			if ctx.Err() != nil {
				return
			}
			if event.Type == agentcore.StreamEventError {
				if event.Err == nil {
					event.Err = errors.New("model stream failed without an error")
				}
				event.Err = m.streamError(ctx, event.Err)
				sendModelEvent(ctx, out, event)
				return
			}
			if !sendModelEvent(ctx, out, event) || event.Type == agentcore.StreamEventDone {
				return
			}
		}
		if ctx.Err() == nil {
			err := fmt.Errorf("model stream closed without completion: %w", io.ErrUnexpectedEOF)
			sendModelEvent(ctx, out, agentcore.StreamEvent{Type: agentcore.StreamEventError, Err: m.streamError(ctx, err)})
		}
	}()
	return out, nil
}

func sendModelEvent(ctx context.Context, out chan<- agentcore.StreamEvent, event agentcore.StreamEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- event:
		return true
	}
}
