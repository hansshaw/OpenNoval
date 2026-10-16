package host

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestWorkbenchGateRequiresPlanApprovalChapterAcceptanceAndBatch(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.Init("default", "test", "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(3); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SavePremise("premise v1"); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "一"}}); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := st.PlanningFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.BatchSize, w.BatchEnd = 1, 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	paused := []string{}
	gate := NewChapterAdvanceGate(st, func(reason string) { paused = append(paused, reason) }, nil)
	writer := &flow.Instruction{Agent: "writer", Chapter: 1, Task: "写第一章"}

	allowed, err := gate.Allow(writer)
	if err != nil || allowed || len(paused) != 1 || !strings.Contains(paused[0], "确认") {
		t.Fatalf("未确认规划时应阻止 writer: allowed=%v paused=%v err=%v", allowed, paused, err)
	}
	if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.ApprovedFingerprint = fingerprint
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	allowed, err = gate.Allow(writer)
	if err != nil || !allowed {
		t.Fatalf("确认规划且批次有效时应放行第一章: allowed=%v err=%v", allowed, err)
	}
	if err := st.Progress.MarkChapterComplete(1, 100, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Checkpoints.Append(domain.ChapterScope(1), "commit", "", ""); err != nil {
		t.Fatal(err)
	}
	allowed, err = gate.Allow(&flow.Instruction{Agent: "writer", Chapter: 2, Task: "写第二章"})
	if err != nil || allowed || len(paused) != 2 || !strings.Contains(paused[1], "确认本章") {
		t.Fatalf("完成章节未验收时应暂停: allowed=%v paused=%v err=%v", allowed, paused, err)
	}
	if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.AcceptedChapter = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	allowed, err = gate.Allow(&flow.Instruction{Agent: "writer", Chapter: 2, Task: "写第二章"})
	if err != nil || allowed || len(paused) != 3 || !strings.Contains(paused[2], "批次") {
		t.Fatalf("超出批次的章节应暂停: allowed=%v paused=%v err=%v", allowed, paused, err)
	}
	if err := st.Outline.SavePremise("premise v2"); err != nil {
		t.Fatal(err)
	}
	allowed, err = gate.Allow(&flow.Instruction{Agent: "writer", Chapter: 2, Task: "写第二章"})
	if err != nil || allowed || len(paused) != 4 || !strings.Contains(paused[3], "大纲") {
		t.Fatalf("规划变化后旧确认应失效: allowed=%v paused=%v err=%v", allowed, paused, err)
	}
}
