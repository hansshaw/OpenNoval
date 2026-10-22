package host

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/flow"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestWorkerStreamRecoverySerializesPreviewAndKeepsAgentScope(t *testing.T) {
	var streamed, buffered atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if request.Stream {
			streamed.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			for index := 0; index < 200; index++ {
				_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"旧预览\"}}]}\n\n")
				w.(http.Flusher).Flush()
			}
			return // HTTP EOF without the SSE completion marker.
		}
		buffered.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"recovered","type":"function","function":{"name":"finish","arguments":"{\"content\":\"完整结果\"}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	defer upstream.Close()
	models, err := bootstrap.NewModelSet(bootstrap.Config{
		Provider: "test", ModelName: "recovery-model",
		Providers: map[string]bootstrap.ProviderConfig{"test": {Type: "openai", APIKey: "test", BaseURL: upstream.URL}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var executed atomic.Int32
	tool := agentcore.NewFuncTool("finish", "record complete result", map[string]any{
		"type": "object", "properties": map[string]any{"content": map[string]any{"type": "string"}}, "required": []string{"content"},
	}, func(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
		var result struct{ Content string }
		if err := json.Unmarshal(args, &result); err != nil || result.Content != "完整结果" {
			return nil, fmt.Errorf("incomplete tool arguments: %s", args)
		}
		executed.Add(1)
		return json.RawMessage(`{"ok":true}`), nil
	})
	workers := subagent.NewRunner(subagent.Config{
		Name: "architect_long", Model: models.ForRoleWithFailover("architect", nil),
		Tools: []agentcore.Tool{tool}, MaxTurns: 2, MaxRetries: 1, StopAfterTools: []string{"finish"},
	})
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	e, _, _ := newTestEngine(t, st, workers, nil)
	var recoveryStarted, staleAfterRecovery bool
	var recoveryAgent string
	previousEmit := e.observer.emitEv
	e.observer.emitEv = func(event Event) {
		if strings.Contains(event.Detail, "完整响应模式") {
			recoveryStarted = true
			recoveryAgent = event.Agent
		}
		previousEmit(event)
	}
	e.observer.emitD = func(delta string) {
		if recoveryStarted && strings.Contains(delta, "旧预览") {
			staleAfterRecovery = true
		}
	}
	if err := e.runWorker(t.Context(), &flow.Instruction{Agent: "architect_long", Task: "save complete result"}); err != nil {
		t.Fatal(err)
	}
	if !recoveryStarted || recoveryAgent != "architect_long" || staleAfterRecovery {
		t.Fatalf("recovery preview/scope invalid: started=%v agent=%q stale=%v", recoveryStarted, recoveryAgent, staleAfterRecovery)
	}
	if streamed.Load() != 1 || buffered.Load() != 1 || executed.Load() != 1 {
		t.Fatalf("expected one stream, one recovery, one tool: %d/%d/%d", streamed.Load(), buffered.Load(), executed.Load())
	}
}
