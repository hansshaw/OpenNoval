package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/hansshaw/AInoval-web-ui/internal/modelrequest"
	"github.com/voocel/agentcore"
)

type requestAgentKey struct{}

// WithModelRequestAgent labels diagnostic records without changing API inputs.
func WithModelRequestAgent(ctx context.Context, agent string) context.Context {
	return context.WithValue(ctx, requestAgentKey{}, agent)
}

func withDefaultModelRequestAgent(ctx context.Context, agent string) context.Context {
	if current, _ := ctx.Value(requestAgentKey{}).(string); current != "" {
		return ctx
	}
	return WithModelRequestAgent(ctx, agent)
}

// This wrapper sits below the stream retry gate so only an actual adapter
// invocation creates a record. The embedded facade preserves optional model
// capabilities; calls themselves go directly to its raw ChatModel.
type requestLoggedModel struct {
	*outputLimitedModel
	journal   requestLogSink
	endpoint  string
	idle      time.Duration
	lifecycle *modelRequestLifecycle
}

type requestLogSink interface {
	Save(modelrequest.Record) error
	Redact(string) string
}

func newRequestLoggedModel(inner agentcore.ChatModel, journal requestLogSink, provider, model, endpoint string, limit int, idle time.Duration, lifecycles ...*modelRequestLifecycle) agentcore.ChatModel {
	var lifecycle *modelRequestLifecycle
	if len(lifecycles) > 0 {
		lifecycle = lifecycles[0]
	}
	return &requestLoggedModel{
		outputLimitedModel: &outputLimitedModel{ChatModel: inner, provider: provider, model: model, limit: limit},
		journal:            journal, endpoint: endpoint, idle: idle,
		lifecycle: lifecycle,
	}
}

func (*requestLoggedModel) streamSnapshotsStable() {}

func (m *requestLoggedModel) start(ctx context.Context, streaming bool, messages []agentcore.Message, tools []agentcore.ToolSpec, opts []agentcore.CallOption) modelrequest.Record {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		// A diagnostic ID must not stop an otherwise valid generation request.
		copy(id[:], time.Now().Format("150405.000000000"))
	}
	agent, _ := ctx.Value(requestAgentKey{}).(string)
	if agent == "" {
		agent = "default"
	}
	options := agentcore.ResolveCallConfig(opts)
	record := modelrequest.Record{
		ID: hex.EncodeToString(id[:]), Provider: m.provider, Model: m.model, Agent: agent,
		Endpoint: m.endpoint, Stream: streaming, StartedAt: time.Now(), Status: "running",
		MaxOutputTokens: options.MaxTokens, IdleTimeoutMs: m.idle.Milliseconds(),
		MessageCount: len(messages), ToolCount: len(tools),
		Request: requestLogJSON(map[string]any{"messages": messages, "tools": tools, "options": options}),
	}
	m.save(record)
	return record
}

func (m *requestLoggedModel) save(record modelrequest.Record) {
	if err := m.journal.Save(record); err != nil {
		slog.Warn("模型请求记录写入失败", "module", "model_request", "id", record.ID, "err", err)
	}
}

func (m *requestLoggedModel) finish(record *modelrequest.Record, message agentcore.Message, err error) {
	record.FinishedAt = time.Now()
	record.DurationMs = record.FinishedAt.Sub(record.StartedAt).Milliseconds()
	record.Status = "success"
	if err != nil {
		record.Status = "error"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			record.Status = "canceled"
		}
		record.Error = m.journal.Redact(err.Error())
	}
	record.StopReason = string(message.StopReason)
	if record.OutputBytes == 0 {
		record.OutputBytes = len(message.TextContent()) + len(message.ThinkingContent())
		for _, call := range message.ToolCalls() {
			record.OutputBytes += len(call.Args)
		}
	}
	if message.Usage != nil {
		record.InputTokens = message.Usage.Input
		record.OutputTokens = message.Usage.Output
	}
	record.Response = responseLogJSON(message)
	m.save(*record)
}

func (m *requestLoggedModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, finished, err := m.lifecycle.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finished()
	record := m.start(ctx, false, messages, tools, opts)
	response, err := m.ChatModel.Generate(ctx, messages, tools, opts...)
	var message agentcore.Message
	if response != nil {
		message = response.Message
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if response == nil && err == nil {
		err = errors.New("model returned no response")
	}
	m.finish(&record, message, err)
	return response, err
}

func (m *requestLoggedModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, finished, err := m.lifecycle.begin(ctx)
	if err != nil {
		return nil, err
	}
	record := m.start(ctx, true, messages, tools, opts)
	source, err := m.ChatModel.GenerateStream(ctx, messages, tools, opts...)
	if err != nil || source == nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == nil {
			err = errors.New("model returned a nil stream")
		}
		m.finish(&record, agentcore.Message{}, err)
		finished()
		return nil, err
	}
	stable := snapshotModelStream(ctx, source)
	out := make(chan agentcore.StreamEvent)
	go func() {
		defer finished()
		defer close(out)
		var partial agentcore.Message
		lastSaved := record.StartedAt
		for {
			var event agentcore.StreamEvent
			var ok bool
			select {
			case <-ctx.Done():
				m.finish(&record, partial, ctx.Err())
				return
			case event, ok = <-stable:
				if !ok {
					err := ctx.Err()
					if err == nil {
						err = &agentcore.PartialStreamError{Partial: partial}
					}
					m.finish(&record, partial, err)
					return
				}
			}
			if err := ctx.Err(); err != nil {
				m.finish(&record, partial, err)
				return
			}
			if event.Type == agentcore.StreamEventDone {
				message := event.Message
				if message.StopReason == "" {
					message.StopReason = event.StopReason
				}
				m.finish(&record, message, nil)
				sendModelEvent(ctx, out, event)
				return
			}
			if event.Type == agentcore.StreamEventError {
				if event.Err == nil {
					event.Err = errors.New("model stream failed")
				}
				if ctx.Err() != nil {
					event.Err = ctx.Err()
				}
				m.finish(&record, partial, event.Err)
				sendModelEvent(ctx, out, event)
				return
			}
			partial = event.Message
			now := time.Now()
			record.Chunks++
			record.OutputBytes += len(event.Delta)
			record.LastChunkAt = now
			first := record.FirstChunkAt.IsZero()
			if first {
				record.FirstChunkAt = now
				record.FirstChunkMs = now.Sub(record.StartedAt).Milliseconds()
			}
			if first || now.Sub(lastSaved) >= 2*time.Second {
				record.DurationMs = now.Sub(record.StartedAt).Milliseconds()
				record.Response = responseLogJSON(partial)
				m.save(record)
				lastSaved = now
			}
			if !sendModelEvent(ctx, out, event) {
				m.finish(&record, partial, ctx.Err())
				return
			}
		}
	}()
	return out, nil
}

func requestLogJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		data, _ = json.Marshal(map[string]string{"serializationError": err.Error()})
	}
	return data
}

// Partial tool arguments need not be valid JSON. Encode them as a string rather
// than corrupting the journal or pretending missing fields have been repaired.
func responseLogJSON(message agentcore.Message) json.RawMessage {
	tools := make([]map[string]any, 0)
	for _, call := range message.ToolCalls() {
		var args any = string(call.Args)
		if json.Valid(call.Args) {
			_ = json.Unmarshal(call.Args, &args)
		}
		entry := map[string]any{"id": call.ID, "name": call.Name, "arguments": args}
		if call.ArgsParseError != "" {
			entry["rawArguments"] = call.ArgsRawText
			entry["argumentsError"] = call.ArgsParseError
		}
		tools = append(tools, entry)
	}
	return requestLogJSON(map[string]any{
		"text": message.TextContent(), "thinking": message.ThinkingContent(),
		"toolCalls": tools, "stopReason": message.StopReason, "usage": message.Usage,
	})
}
