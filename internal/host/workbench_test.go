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
	if err := st.Book.Save(domain.BookMetadata{Title: "规划审批测试", Synopsis: "验证规划确认与章节批次闸门"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SavePremise("premise v1"); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "一"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Characters.Save([]domain.Character{{Name: "主角", Role: "主角", Description: "测试角色"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.World.SaveWorldRules([]domain.WorldRule{{Category: "society", Rule: "规则", Boundary: "边界"}}); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := st.PlanningFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	foundationFingerprint, err := st.FoundationFingerprint()
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
		w.ApprovedFoundationFingerprint = foundationFingerprint
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
	if err != nil || allowed || len(paused) != 2 || !strings.Contains(paused[1], "确认") {
		t.Fatalf("批次完成未验收时应暂停: allowed=%v paused=%v err=%v", allowed, paused, err)
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

func TestWorkbenchGateRunsWholeBatchBeforeReview(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.Init("default", "test", "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(4); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatal(err)
	}
	if err := st.Book.Save(domain.BookMetadata{Title: "章节批次测试", Synopsis: "验证批次内连续生成"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SavePremise("premise"); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveOutline([]domain.OutlineEntry{
		{Chapter: 1, Title: "一"},
		{Chapter: 2, Title: "二"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Characters.Save([]domain.Character{{Name: "主角", Role: "主角", Description: "测试角色"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.World.SaveWorldRules([]domain.WorldRule{{Category: "society", Rule: "规则", Boundary: "边界"}}); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := st.PlanningFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	foundationFingerprint, err := st.FoundationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.ApprovedFingerprint = fingerprint
		w.ApprovedFoundationFingerprint = foundationFingerprint
		w.BatchSize, w.BatchEnd = 2, 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	paused := []string{}
	gate := NewChapterAdvanceGate(st, func(reason string) {
		paused = append(paused, reason)
	}, nil)
	for chapter := 1; chapter <= 2; chapter++ {
		allowed, err := gate.Allow(&flow.Instruction{
			Agent:   "writer",
			Chapter: chapter,
			Task:    "写章节",
		})
		if err != nil || !allowed {
			t.Fatalf("批次内第 %d 章应直接放行: allowed=%v err=%v", chapter, allowed, err)
		}
		if err := st.Progress.MarkChapterComplete(chapter, 1000, "", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Checkpoints.Append(domain.ChapterScope(chapter), "commit", "", ""); err != nil {
			t.Fatal(err)
		}
		stopped := gate.HandleBoundary()
		if chapter == 1 && stopped {
			t.Fatal("批次尚未全部完成时不能暂停等待审核")
		}
		if chapter == 2 && !stopped {
			t.Fatal("批次全部完成后必须暂停等待统一审核")
		}
	}
	if len(paused) != 1 || !strings.Contains(paused[0], "整批") {
		t.Fatalf("批次完成后的审核提示不正确: %v", paused)
	}
}
