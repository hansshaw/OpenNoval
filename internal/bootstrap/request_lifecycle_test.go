package bootstrap

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hansshaw/AInoval-web-ui/internal/modelrequest"
	"github.com/voocel/agentcore"
)

type gatedTerminalRequestSink struct {
	*modelrequest.Journal
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (sink *gatedTerminalRequestSink) Save(record modelrequest.Record) error {
	if record.Status != "running" {
		sink.once.Do(func() { close(sink.entered); <-sink.release })
	}
	return sink.Journal.Save(record)
}

func lifecycleModelConfig(directory string) Config {
	return Config{Provider: "test", ModelName: "primary", OutputDir: directory,
		Providers: map[string]ProviderConfig{"test": {Type: "openai", APIKey: "test-key", BaseURL: "http://127.0.0.1:1/v1", Models: []ModelConfig{{Name: "primary"}, {Name: "replacement"}, {Name: "writer"}, {Name: "fallback"}}}},
		Roles:     map[string]RoleConfig{"writer": {Provider: "test", Model: "writer", Fallbacks: []ModelRef{{Provider: "test", Model: "fallback"}}}},
	}
}

func unwrapRequestLogger(t *testing.T, model agentcore.ChatModel) *requestLoggedModel {
	t.Helper()
	if swappable, ok := model.(*SwappableModel); ok {
		model = swappable.SwappableModel.Current()
	}
	limited, ok := model.(*outputLimitedModel)
	if !ok {
		t.Fatalf("missing policy wrapper: %T", model)
	}
	logged, ok := limited.ChatModel.(*requestLoggedModel)
	if !ok {
		t.Fatalf("missing request logger: %T", limited.ChatModel)
	}
	return logged
}

func gateModelTerminalLog(t *testing.T, set *ModelSet, model agentcore.ChatModel) (*gatedTerminalRequestSink, func()) {
	t.Helper()
	logged := unwrapRequestLogger(t, model)
	if logged.lifecycle == nil {
		t.Fatal("runtime client has no owned lifecycle")
	}
	gate := &gatedTerminalRequestSink{Journal: set.requestJournal, entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	logged.journal = gate
	logged.ChatModel = &streamPolicyModel{
		generate: func(ctx context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		stream: func(ctx context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
			out := make(chan agentcore.StreamEvent)
			go func() { <-ctx.Done(); close(out) }()
			return out, nil
		},
	}
	return gate, release
}

func waitLifecycleSignal(t *testing.T, label string, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func TestModelSetCloseWaitsForCanceledTerminalLogBeforeReturning(t *testing.T) {
	set, err := NewModelSet(lifecycleModelConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(set.CloseRequests)
	gate, release := gateModelTerminalLog(t, set, set.Default)
	stream, err := set.Default.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { set.CloseRequests(); close(closed) }()
	waitLifecycleSignal(t, "terminal log entered", gate.entered)
	select {
	case <-closed:
		t.Fatal("shutdown returned before terminal log persisted")
	default:
	}
	release()
	waitLifecycleSignal(t, "request producer joined", closed)
	for range stream {
	}
	rows := requestLogRecords(t, set.requestJournal, 1)
	if rows[0].Status != "canceled" || rows[0].FinishedAt.IsZero() {
		t.Fatal(rows)
	}
	// Check the diagnostic boundary directly; the outer transport retry gate
	// may retain its backoff from cancellation independently of file ownership.
	if _, err := unwrapRequestLogger(t, set.Default).GenerateStream(context.Background(), nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("closed client started a new logged request", err)
	}
	if len(requestLogRecords(t, set.requestJournal, 1)) != 1 {
		t.Fatal("closed set wrote another record")
	}
}

func TestModelSetCloseWaitsForSynchronousCanceledRequestLogging(t *testing.T) {
	set, err := NewModelSet(lifecycleModelConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(set.CloseRequests)
	gate, release := gateModelTerminalLog(t, set, set.Default)
	// The provider latch proves the synchronous request is registered before
	// shutdown cancels it; the second latch then holds its final disk write.
	providerEntered := make(chan struct{})
	logged := unwrapRequestLogger(t, set.Default)
	logged.ChatModel = &streamPolicyModel{generate: func(ctx context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
		close(providerEntered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	returned := make(chan error, 1)
	go func() { _, err := set.Default.Generate(context.Background(), nil, nil); returned <- err }()
	waitLifecycleSignal(t, "synchronous provider", providerEntered)
	closed := make(chan struct{})
	go func() { set.CloseRequests(); close(closed) }()
	waitLifecycleSignal(t, "synchronous terminal log", gate.entered)
	select {
	case <-closed:
		t.Fatal("shutdown returned before synchronous log save")
	default:
	}
	release()
	waitLifecycleSignal(t, "synchronous producer joined", closed)
	if err := <-returned; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestModelSetOwnsRetiredClientsReconfiguredRolesFallbacksAndSwaps(t *testing.T) {
	config := lifecycleModelConfig(t.TempDir())
	set, err := NewModelSet(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(set.CloseRequests)
	oldGate, releaseOld := gateModelTerminalLog(t, set, set.Default)
	oldStream, err := set.Default.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := NewModelSet(config)
	if err != nil {
		t.Fatal(err)
	}
	set.ApplyPrepared(candidate)
	role := set.ForRole("writer")
	roleGate, releaseRole := gateModelTerminalLog(t, set, role)
	roleStream, err := role.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fallback := set.fallbackTargets("writer")[0].model
	fallbackGate, releaseFallback := gateModelTerminalLog(t, set, fallback)
	fallbackStream, err := fallback.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Swap("default", "test", "replacement"); err != nil {
		t.Fatal(err)
	}
	swapGate, releaseSwap := gateModelTerminalLog(t, set, set.Default)
	swapStream, err := set.Default.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { set.CloseRequests(); close(closed) }()
	for label, gate := range map[string]*gatedTerminalRequestSink{"old client": oldGate, "reconfigured role": roleGate, "fallback": fallbackGate, "hot swap": swapGate} {
		waitLifecycleSignal(t, label, gate.entered)
	}
	releaseRole()
	releaseFallback()
	releaseSwap()
	select {
	case <-closed:
		t.Fatal("retired client's terminal log was not retained")
	default:
	}
	releaseOld()
	waitLifecycleSignal(t, "all owned clients", closed)
	for _, stream := range []<-chan agentcore.StreamEvent{oldStream, roleStream, fallbackStream, swapStream} {
		for range stream {
		}
	}
	for _, record := range requestLogRecords(t, set.requestJournal, 4) {
		if record.Status != "canceled" {
			t.Fatal(record)
		}
	}
}

func TestClosingOneModelSetDoesNotCancelOrWaitForAnotherProject(t *testing.T) {
	set, err := NewModelSet(lifecycleModelConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(set.CloseRequests)
	other, err := NewModelSet(lifecycleModelConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.CloseRequests)
	gate, release := gateModelTerminalLog(t, other, other.Default)
	stream, err := other.Default.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { set.CloseRequests(); close(closed) }()
	waitLifecycleSignal(t, "unrelated idle project", closed)
	select {
	case <-gate.entered:
		t.Fatal("unrelated project's stream was canceled")
	default:
	}
	otherClosed := make(chan struct{})
	go func() { other.CloseRequests(); close(otherClosed) }()
	waitLifecycleSignal(t, "other project terminal log", gate.entered)
	release()
	waitLifecycleSignal(t, "other project joined", otherClosed)
	for range stream {
	}
}
