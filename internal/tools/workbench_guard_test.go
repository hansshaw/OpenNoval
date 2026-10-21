package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

func workbenchGuardStore(t *testing.T) *store.Store {
	t.Helper()
	st := store.NewStore(t.TempDir())
	for _, err := range []error{st.Init(), st.Progress.Init(3), st.RunMeta.Init("default", "test", "test"), st.Progress.UpdatePhase(domain.PhaseWriting)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error { return nil }); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestWorkbenchPlanningCanReplaceBeforeProseButProtectsStartedChapter(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "review_stage", true: "writing_started"}[started], func(t *testing.T) {
			st := workbenchGuardStore(t)
			if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "旧目录"}}); err != nil {
				t.Fatal(err)
			}
			if started {
				if err := st.Progress.StartChapter(1); err != nil {
					t.Fatal(err)
				}
			}
			_, err := NewSaveFoundationTool(st).Execute(context.Background(), json.RawMessage(`{"type":"outline","content":[{"chapter":1,"title":"新目录","core_event":"调查","hook":"发现线索"}]}`))
			outline, _ := st.Outline.LoadOutline()
			if started {
				if !errors.Is(err, errs.ErrToolPrecondition) || outline[0].Title != "旧目录" {
					t.Fatalf("started prose must remain protected: outline=%+v err=%v", outline, err)
				}
				return
			}
			if err != nil || outline[0].Title != "新目录" {
				t.Fatalf("review stage must allow replacement despite writing phase: outline=%+v err=%v", outline, err)
			}
			meta, _ := st.RunMeta.Load()
			if !meta.Workbench.PlanningRevisionPending || meta.Workbench.ApprovedFingerprint != "" {
				t.Fatalf("replacement must require a new audit and consent: %+v", meta.Workbench)
			}
		})
	}
}

func TestWorkbenchCompletedProseToolsRequireSelectedChapter(t *testing.T) {
	st := workbenchGuardStore(t)
	if err := st.Progress.MarkChapterComplete(1, 20, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Progress.ApplyReviewOutcome(domain.FlowRewriting, []int{1}, "模型自行建议返工"); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		run  func() error
	}{
		{"draft", func() error {
			_, err := NewDraftChapterTool(st).Execute(context.Background(), json.RawMessage(`{"chapter":1,"content":"不得覆盖","mode":"write"}`))
			return err
		}},
		{"edit", func() error {
			_, err := NewEditChapterTool(st).Execute(context.Background(), json.RawMessage(`{"chapter":1,"old_string":"旧","new_string":"新"}`))
			return err
		}},
		{"commit", func() error {
			_, err := NewCommitChapterTool(st, NewStyleStatsIndex(st)).Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); !errors.Is(err, errs.ErrToolPrecondition) || !strings.Contains(err.Error(), "用户明确指令") {
				t.Fatalf("expected explicit user consent guard, got %v", err)
			}
		})
	}
	if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error { w.RewriteChapter = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := checks[0].run(); err != nil {
		t.Fatalf("selected single chapter must be writable: %v", err)
	}
}

func TestWorkbenchEditorSuggestionsDoNotQueueAutomaticRewrites(t *testing.T) {
	st := workbenchGuardStore(t)
	if err := st.Progress.MarkChapterComplete(1, 20, "", ""); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"chapter":1,"scope":"chapter","dimensions":[{"dimension":"continuity","score":60,"comment":"存在需要用户确认的问题"}],"issues":[{"type":"continuity","severity":"error","description":"人物行动需调整","evidence":"本章直接离开","chapters":[1],"requires_change":true}],"verdict":"rewrite","summary":"建议修改，等待用户选择章节"}`)
	if _, err := NewSaveReviewTool(st).Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	p, _ := st.Progress.Load()
	r, _ := st.World.LoadReview(1)
	if len(p.PendingRewrites) != 0 || p.Flow != domain.FlowWriting || r == nil || r.Verdict != "rewrite" || len(r.AffectedChapters) != 1 {
		t.Fatalf("findings must remain reviewable without changing completed prose: progress=%+v review=%+v", p, r)
	}
}
