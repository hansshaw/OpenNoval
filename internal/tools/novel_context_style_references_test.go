package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

func styleReferenceContextStore(t *testing.T) *store.Store {
	t.Helper()
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(1); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "project chapter", CoreEvent: "project fact"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Simulation.Save(domain.SimulationProfile{Version: domain.SimulationProfileVersion, Synthesis: domain.SimulationSynthesis{Style: domain.SimulationStyle{NarrativeVoice: []string{"UNSELECTED-LOCAL-VOICE"}}}, SourceReports: []domain.SimulationSourceReport{{Summary: "LOCAL-RAW-REPORT"}}}); err != nil {
		t.Fatal(err)
	}
	return st
}

func selectedReference(id, voice string) domain.NamedStyleReference {
	return domain.NamedStyleReference{ID: id, Name: "Reference " + id, Profile: &domain.SimulationCompactProfile{
		Version: domain.SimulationProfileVersion, SourceCount: 3,
		SourceFiles:  []string{"PRIVATE-REFERENCE-SOURCE-PATH"},
		Style:        domain.SimulationStyle{NarrativeVoice: []string{voice}},
		RoleGuidance: domain.SimulationRoleGuidance{Architect: []string{"structure guidance"}, Writer: []string{"voice guidance"}, Editor: []string{"review guidance"}},
	}}
}

func styleReferenceSection(t *testing.T, tool *ContextTool, chapter bool) (map[string]any, map[string]any, string) {
	t.Helper()
	args, section := json.RawMessage(`{}`), "planning_memory"
	if chapter {
		args, section = json.RawMessage(`{"chapter":1}`), "working_memory"
	}
	raw, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	memory, ok := payload[section].(map[string]any)
	if !ok {
		t.Fatalf("missing %s: %s", section, raw)
	}
	return memory, payload, string(raw)
}

func TestContextLoadsOnlySelectedSharedStylesAndRefreshesSelection(t *testing.T) {
	st := styleReferenceContextStore(t)
	selection := []domain.NamedStyleReference{selectedReference("one", "selected voice one")}
	calls := 0
	tool := NewContextTool(st, References{}, "default", NewStyleStatsIndex(st), func() ([]domain.NamedStyleReference, error) {
		calls++
		return selection, nil
	})
	for _, chapter := range []bool{false, true} {
		memory, _, raw := styleReferenceSection(t, tool, chapter)
		refs, ok := memory["style_references"].([]any)
		if !ok || len(refs) != 1 || refs[0].(map[string]any)["id"] != "one" {
			t.Fatalf("selected named style missing: %s", raw)
		}
		if _, exists := memory["simulation_profile"]; exists {
			t.Fatal("shared references unexpectedly fell back to local profile")
		}
		for _, forbidden := range []string{"UNSELECTED-LOCAL-VOICE", "LOCAL-RAW-REPORT", "source_reports", "PRIVATE-REFERENCE-SOURCE-PATH", "source_files"} {
			if strings.Contains(raw, forbidden) {
				t.Errorf("reference context leaked unselected or raw source data: %s", forbidden)
			}
		}
		if !strings.Contains(raw, "仿写参考:1项") || !strings.Contains(memory["style_reference_policy"].(string), "用户当前明确指令") || !strings.Contains(raw, "project fact") {
			t.Fatalf("reference policy/summary or project facts missing: %s", raw)
		}
	}
	selection = []domain.NamedStyleReference{selectedReference("two", "newly selected voice")}
	_, _, raw := styleReferenceSection(t, tool, true)
	if calls != 3 || strings.Contains(raw, "selected voice one") || !strings.Contains(raw, "newly selected voice") {
		t.Fatalf("context retained an old selection: calls=%d payload=%s", calls, raw)
	}
}

func TestSharedReferenceEmptyOrUnavailableNeverUsesLocalProfile(t *testing.T) {
	for _, failure := range []error{nil, os.ErrNotExist} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			st := styleReferenceContextStore(t)
			tool := NewContextTool(st, References{}, "default", NewStyleStatsIndex(st), func() ([]domain.NamedStyleReference, error) { return nil, failure })
			for _, chapter := range []bool{false, true} {
				memory, payload, raw := styleReferenceSection(t, tool, chapter)
				refs, ok := memory["style_references"].([]any)
				if !ok || len(refs) != 0 || strings.Contains(raw, "UNSELECTED-LOCAL-VOICE") || strings.Contains(raw, "simulation_profile") {
					t.Fatalf("empty/unavailable shared selection used local profile: %s", raw)
				}
				if failure != nil {
					warnings, ok := payload["_warnings"].([]any)
					if !ok || len(warnings) == 0 || !strings.Contains(warnings[0].(string), "style_references") {
						t.Fatalf("missing selected-entry warning: %s", raw)
					}
				}
			}
		})
	}
}

func TestSharedReferencePartialAvailabilityKeepsValidSelection(t *testing.T) {
	st := styleReferenceContextStore(t)
	tool := NewContextTool(st, References{}, "default", NewStyleStatsIndex(st), func() ([]domain.NamedStyleReference, error) {
		return []domain.NamedStyleReference{selectedReference("ready", "available voice"), {ID: "invalidated", Name: "waiting for reanalysis"}}, fmt.Errorf("selected reference missing: %w", os.ErrNotExist)
	})
	memory, payload, raw := styleReferenceSection(t, tool, false)
	refs := memory["style_references"].([]any)
	if len(refs) != 1 || refs[0].(map[string]any)["id"] != "ready" || !strings.Contains(raw, "available voice") || strings.Contains(raw, "UNSELECTED-LOCAL-VOICE") {
		t.Fatalf("partial callback failure discarded valid selected references: %s", raw)
	}
	if warnings, ok := payload["_warnings"].([]any); !ok || len(warnings) == 0 {
		t.Fatal("invalidated selected references were silently ignored")
	}
}

func TestSharedReferenceContextBoundsProfilesWithoutMutatingLibrary(t *testing.T) {
	st := styleReferenceContextStore(t)
	profile := selectedReference("one", "voice").Profile
	profile.Style.NarrativeVoice = make([]string, 30)
	for index := range profile.Style.NarrativeVoice {
		profile.Style.NarrativeVoice[index] = fmt.Sprint(index)
	}
	selected := make([]domain.NamedStyleReference, 25)
	for index := range selected {
		selected[index] = domain.NamedStyleReference{ID: fmt.Sprint(index), Name: fmt.Sprint(index), Profile: profile}
	}
	tool := NewContextTool(st, References{}, "default", NewStyleStatsIndex(st), func() ([]domain.NamedStyleReference, error) { return selected, nil })
	memory, payload, raw := styleReferenceSection(t, tool, false)
	refs := memory["style_references"].([]any)
	if len(refs) != 20 {
		t.Fatalf("unbounded selected reference count: %d", len(refs))
	}
	compact := refs[0].(map[string]any)["profile"].(map[string]any)
	if len(compact["style"].(map[string]any)["narrative_voice"].([]any)) != 12 || compact["source_count"] != float64(3) {
		t.Fatalf("compact synthesis bounds or source count lost: %s", raw)
	}
	if len(profile.Style.NarrativeVoice) != 30 || len(profile.SourceFiles) != 1 {
		t.Fatal("context compaction mutated the library profile")
	}
	if warnings, ok := payload["_warnings"].([]any); !ok || len(warnings) == 0 {
		t.Fatal("reference count cap was not reported")
	}
}
