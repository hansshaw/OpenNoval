package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

func modelBindingFixture(t *testing.T) (Config, <-chan string) {
	t.Helper()
	requests := make(chan string, 32)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- payload.Model
		w.Header().Set("Content-Type", "application/json")
		if payload.Model == "rate-limited" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limit","type":"rate_limit_error","code":"rate_limit_exceeded"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"stub","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(upstream.Close)
	return Config{
		Provider: "stub", ModelName: "default-v1",
		Providers: map[string]ProviderConfig{"stub": {Type: "openai", APIKey: "test-key", BaseURL: upstream.URL}},
	}, requests
}

func TestBoundRoleUsesNewOverridesAndContinuesInheritingDefault(t *testing.T) {
	cfg, requests := modelBindingFixture(t)
	set, err := NewModelSet(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// These are the same long-lived references held by existing workers.
	writer := set.ForRole("writer")
	writerWithFailover := set.ForRoleWithFailover("writer", nil)
	editor := set.ForRole("editor")
	generate := func(model agentcore.ChatModel, want string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := model.Generate(ctx, []agentcore.Message{agentcore.UserMsg("hi")}, nil); err != nil {
			t.Fatal(err)
		}
		if got := <-requests; got != want {
			t.Fatalf("upstream received model %q, want %q", got, want)
		}
	}
	generate(writer, "default-v1")
	if err := set.Swap("writer", "stub", "writer-v1"); err != nil {
		t.Fatal(err)
	}
	generate(writer, "writer-v1")
	generate(writerWithFailover, "writer-v1")
	if err := set.Swap("default", "stub", "default-v2"); err != nil {
		t.Fatal(err)
	}
	generate(editor, "default-v2")
	generate(writer, "writer-v1")

	// Removing an override must update the worker that previously used it.
	cfg.ModelName = "default-v2"
	prepared, err := NewModelSet(cfg)
	if err != nil {
		t.Fatal(err)
	}
	set.ApplyPrepared(prepared)
	generate(writer, "default-v2")
	generate(writerWithFailover, "default-v2")
	if _, _, explicit := set.CurrentSelection("writer"); explicit {
		t.Fatal("removed override still reports an explicit selection")
	}

	// Adding a new override through the full configuration uses that same pointer.
	cfg.Roles = map[string]RoleConfig{"writer": {Provider: "stub", Model: "writer-v2"}}
	prepared, err = NewModelSet(cfg)
	if err != nil {
		t.Fatal(err)
	}
	set.ApplyPrepared(prepared)
	if set.ForRole("writer") != writer || set.ForRole("editor") != editor {
		t.Fatal("reconfiguration replaced a worker's bound model wrapper")
	}
	generate(writer, "writer-v2")
	generate(writerWithFailover, "writer-v2")
	generate(editor, "default-v2")
}

func TestBoundRolePicksUpAddedAndRemovedFallbacks(t *testing.T) {
	cfg, requests := modelBindingFixture(t)
	set, err := NewModelSet(cfg)
	if err != nil {
		t.Fatal(err)
	}
	worker := set.ForRoleWithFailover("writer", nil)
	cfg.Roles = map[string]RoleConfig{
		"writer": {Provider: "stub", Model: "rate-limited", Fallbacks: []ModelRef{{Provider: "stub", Model: "backup"}}},
	}
	prepared, err := NewModelSet(cfg)
	if err != nil {
		t.Fatal(err)
	}
	set.ApplyPrepared(prepared)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := worker.Generate(ctx, []agentcore.Message{agentcore.UserMsg("hi")}, nil); err != nil {
		t.Fatalf("new fallback was not used: %v", err)
	}
	for _, want := range []string{"rate-limited", "backup"} {
		if got := <-requests; got != want {
			t.Fatalf("upstream received model %q, want %q", got, want)
		}
	}
	rc := cfg.Roles["writer"]
	rc.Fallbacks = nil
	cfg.Roles["writer"] = rc
	prepared, err = NewModelSet(cfg)
	if err != nil {
		t.Fatal(err)
	}
	set.ApplyPrepared(prepared)
	if _, err := worker.Generate(ctx, []agentcore.Message{agentcore.UserMsg("hi")}, nil); err == nil {
		t.Fatal("removed fallback still used")
	}
	if got := <-requests; got != "rate-limited" || len(requests) != 0 {
		t.Fatalf("unexpected requests after removing fallback: first=%q remaining=%d", got, len(requests))
	}
}
