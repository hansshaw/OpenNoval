package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

func manualPlanGuardStore(t *testing.T) *store.Store {
	t.Helper()
	st := workbenchGuardStore(t)
	if err := st.Outline.SavePremise("第一阶段原则：不改人物身份"); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "已保存一", CoreEvent: "得到信", Hook: "启程"},
		{Chapter: 2, Title: "已保存二", CoreEvent: "离开城镇", Hook: "有来客"},
		{Chapter: 3, Title: "已保存三", CoreEvent: "见到故人", Hook: "真相"},
	}); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := st.PlanningFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	title := "用户亲自改好的章名"
	if _, err := st.UpdateWorkbenchChapterPlan(2, store.WorkbenchChapterPlanEdit{Title: &title}, fingerprint); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestManualPlanAuditToolsCannotOverwriteUserSavedArtifacts(t *testing.T) {
	type executable interface {
		Execute(context.Context, json.RawMessage) (json.RawMessage, error)
	}
	tests := []struct {
		name string
		tool func(*store.Store) executable
		args string
	}{
		{"replace_book", func(st *store.Store) executable { return NewSaveBookTool(st) }, `{"title":"模型改书名","synopsis":"模型改设定"}`},
		{"replace_flat_and_scale", func(st *store.Store) executable { return NewSaveFoundationTool(st) }, `{"type":"outline","scale":"long","content":[{"chapter":1,"title":"模型覆盖","core_event":"新事件","hook":"新钩子"}]}`},
		{"replace_layered", func(st *store.Store) executable { return NewSaveFoundationTool(st) }, `{"type":"layered_outline","content":[{"title":"模型卷","arcs":[{"title":"模型弧","goal":"目标","chapters":[{"title":"模型章","core_event":"新事件","hook":"新钩子"}]}]}]}`},
		{"replace_premise", func(st *store.Store) executable { return NewSaveFoundationTool(st) }, `{"type":"premise","content":"模型替换的第一阶段原则"}`},
		{"append_volume", func(st *store.Store) executable { return NewSaveFoundationTool(st) }, `{"type":"append_volume","reason":"模型自行添加","content":{"title":"新卷","arcs":[{"title":"新弧","goal":"目标","chapters":[{"title":"新章","core_event":"事件","hook":"钩子"}]}]}}`},
		{"update_compass", func(st *store.Store) executable { return NewSaveFoundationTool(st) }, `{"type":"update_compass","content":{"ending_direction":"模型更换结局","estimated_scale":"十卷"}}`},
		{"complete_book", func(st *store.Store) executable { return NewSaveFoundationTool(st) }, `{"type":"complete_book","reason":"模型擅自完结","content":"无需更多内容"}`},
		{"revise_tail", func(st *store.Store) executable { return NewReviseOutlineTool(st) }, `{"from_chapter":2,"reason":"模型擅自替换","replacement":[{"title":"旧会话中的目录","core_event":"旧事件","hook":"旧钩子"}]}`},
		{"expand_arc", func(st *store.Store) executable { return NewExpandNextArcTool(st) }, `{"title":"模型弧","goal":"目标","chapters":[{"title":"模型章","core_event":"事件","hook":"钩子"}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := manualPlanGuardStore(t)
			before := manualPlanGuardFiles(t, st.Dir())
			_, err := tc.tool(st).Execute(context.Background(), json.RawMessage(tc.args))
			if !errors.Is(err, errs.ErrToolPrecondition) || !strings.Contains(err.Error(), "audit_foundation") {
				t.Fatalf("manual edit was not protected by an actionable audit gate: %v", err)
			}
			if got := manualPlanGuardFiles(t, st.Dir()); !reflect.DeepEqual(before, got) {
				t.Fatal("blocked tool wrote a planning artifact, progress, scale or checkpoint")
			}
		})
	}
}

func TestManualPlanProtectionDoesNotBlockExplicitModelRevision(t *testing.T) {
	for _, state := range []struct {
		name    string
		pending bool
		manual  bool
	}{{"normal_confirmation_stage", false, false}, {"user_requests_model_revision", true, false}, {"audit_already_finished", false, true}} {
		t.Run(state.name, func(t *testing.T) {
			st := manualPlanGuardStore(t)
			if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
				w.PlanningRevisionPending, w.PlanningManualEdit = state.pending, state.manual
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			_, err := NewSaveFoundationTool(st).Execute(context.Background(), json.RawMessage(`{"type":"outline","content":[{"chapter":1,"title":"用户重新要求模型修订","core_event":"重新调查","hook":"新的线索"}]}`))
			if err != nil {
				t.Fatalf("normal model planning was blocked: %v", err)
			}
			outline, err := st.Outline.LoadOutlineView()
			if err != nil || len(outline) != 1 || outline[0].Title != "用户重新要求模型修订" {
				t.Fatalf("explicit model edit was not saved: %+v %v", outline, err)
			}
		})
	}
}

func manualPlanGuardFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}
