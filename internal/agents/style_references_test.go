package agents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hansshaw/OpenNovel/assets"
	"github.com/hansshaw/OpenNovel/internal/bootstrap"
	"github.com/hansshaw/OpenNovel/internal/domain"
	"github.com/hansshaw/OpenNovel/internal/store"
	"github.com/hansshaw/OpenNovel/internal/tools"
	"github.com/voocel/agentcore"
)

func TestEveryCreationWorkerUsesSelectedStyleReferenceCallback(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(1); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "first", CoreEvent: "event"}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	cfg := bootstrap.Config{Provider: "local", ModelName: "test-model", Style: "default", Providers: map[string]bootstrap.ProviderConfig{"local": {Type: "openai", APIKey: "test", BaseURL: "http://127.0.0.1:1/v1"}},
		StyleReferenceProfiles: func() ([]domain.NamedStyleReference, error) {
			calls++
			return []domain.NamedStyleReference{{ID: "chosen", Name: "Chosen shared reference", Profile: &domain.SimulationCompactProfile{Version: domain.SimulationProfileVersion, Style: domain.SimulationStyle{NarrativeVoice: []string{"selected technique"}}}}}, nil
		},
	}
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatal(err)
	}
	runner, _, _ := BuildWorkers(cfg, st, tools.NewStyleStatsIndex(st), models, assets.Load("default", assets.LoadOptions{}), nil, nil)
	for _, worker := range []string{"architect_short", "architect_long", "writer", "editor"} {
		agent, ok := runner.AgentConfig(worker)
		if !ok {
			t.Fatalf("missing worker %s", worker)
		}
		var contextTool agentcore.Tool
		for _, tool := range agent.Tools {
			if tool.Name() == "novel_context" {
				contextTool = tool
			}
		}
		if contextTool == nil {
			t.Fatalf("%s lacks the selected-reference context tool", worker)
		}
		args := json.RawMessage(`{}`)
		if worker == "writer" || worker == "editor" {
			args = json.RawMessage(`{"chapter":1}`)
		}
		raw, err := contextTool.Execute(context.Background(), args)
		if err != nil || !strings.Contains(string(raw), "Chosen shared reference") || !strings.Contains(string(raw), "selected technique") {
			t.Fatalf("%s did not load its selected references: %s %v", worker, raw, err)
		}
	}
	if calls != 4 {
		t.Fatalf("callbacks were not resolved at each context read: %d", calls)
	}
}
