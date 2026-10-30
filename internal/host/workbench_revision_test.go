package host

import (
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hansshaw/OpenNovel/internal/domain"
	"github.com/hansshaw/OpenNovel/internal/flow"
	"github.com/hansshaw/OpenNovel/internal/store"
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
)

func workbenchRevisionHost(t *testing.T) *Host {
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
	return &Host{store: st, engine: &engine{}, events: make(chan Event, 8)}
}

func TestWorkbenchPlanningRecoveryPreservesInitialIntentAndPlanner(t *testing.T) {
	h := workbenchRevisionHost(t)
	meta, _ := h.store.RunMeta.Load()
	meta.StartPrompt = "第一阶段原则：不得改变主角身份，保持推理逻辑"
	meta.PlanStart = &domain.PlanStartRecord{Planner: "architect_long", PlannerTask: "分卷安排三部长期调查"}
	if err := h.store.RunMeta.Save(*meta); err != nil {
		t.Fatal(err)
	}
	if err := h.RecordWorkbenchFeedback("planning", 0, "重新安排第二卷目录"); err != nil {
		t.Fatal(err)
	}
	inst, err := workbenchPlanningInstruction(h.store)
	if err != nil || inst == nil || inst.Agent != "architect_long" {
		t.Fatalf("interrupted long-form planning must retain its planner: %+v %v", inst, err)
	}
	for _, want := range []string{
		meta.StartPrompt,
		meta.PlanStart.PlannerTask,
		"重新安排第二卷目录",
		"audit_foundation",
		`save_foundation(type="layered_outline")`,
		`save_foundation(type="outline")`,
		"revise_outline",
		"不得用目录调整隐式改写已完成正文",
	} {
		if !strings.Contains(inst.Task, want) {
			t.Fatalf("recovery task lost %q: %s", want, inst.Task)
		}
	}
	after, _ := h.store.RunMeta.Load()
	if after.StartPrompt != meta.StartPrompt || after.PlanStart.PlannerTask != meta.PlanStart.PlannerTask || !after.Workbench.PlanningRevisionPending {
		t.Fatalf("initial records must remain intact: %+v", after)
	}
	if _, err := currentFoundationFingerprint(h.store); err == nil || !strings.Contains(err.Error(), "尚未完成语义审查") {
		t.Fatalf("legacy project must not bypass revision audit: %v", err)
	}
}

func TestCompletedManualPlanEditCanResumeOnlyItsAudit(t *testing.T) {
	h := workbenchRevisionHost(t)
	if err := h.store.Progress.MarkComplete(); err != nil {
		t.Fatal(err)
	}
	if err := h.store.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.PlanningRevisionPending = true
		w.PlanningManualEdit = true
		w.PlanningRevisionRequest = "保留手工规划并审查"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	label, err := resumeLabel(h.store)
	if err != nil || !strings.Contains(label, "规划修改") {
		t.Fatalf("manual audit resume: %q %v", label, err)
	}
	inst, err := workbenchPlanningInstruction(h.store)
	if err != nil || inst == nil || !strings.Contains(inst.Task, "不能修改、覆盖、还原这些规划") || strings.Contains(inst.Task, "正文尚未开始时可用 save_foundation 覆盖") {
		t.Fatalf("manual review must not authorize model changes: %+v %v", inst, err)
	}
	e := &engine{store: h.store}
	if replacement, err := e.precheck(&flow.Instruction{Agent: "architect_short"}); err != nil || replacement != nil {
		t.Fatalf("audit should be allowed: %+v %v", replacement, err)
	}
	if replacement, err := e.precheck(&flow.Instruction{Agent: "writer", Chapter: 1}); err != nil || replacement == nil || replacement.Agent != "" {
		t.Fatalf("completed prose must remain protected: %+v %v", replacement, err)
	}
	if err := h.store.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error { w.PlanningRevisionPending = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if replacement, err := e.precheck(&flow.Instruction{Agent: "architect_short"}); err != nil || replacement == nil || replacement.Agent != "" {
		t.Fatalf("unrequested planning must remain blocked: %+v %v", replacement, err)
	}
}

func TestWorkbenchSingleChapterFeedbackWorksWithoutActiveBatch(t *testing.T) {
	h := workbenchRevisionHost(t)
	for ch := 1; ch <= 2; ch++ {
		text := "原始正文"
		if err := h.store.Drafts.SaveFinalChapter(ch, text); err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.ChapterRecords.Accept(ch, domain.ChapterOriginGenerated, text, domain.ChapterFacts{}, domain.StyleDelta{}); err != nil {
			t.Fatal(err)
		}
		if err := h.store.Progress.MarkChapterComplete(ch, 4, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.RecordWorkbenchFeedback("chapter", 1, "只把第一章的线索解释更清楚"); err != nil {
		t.Fatal(err)
	}
	p, _ := h.store.Progress.Load()
	meta, _ := h.store.RunMeta.Load()
	if !slices.Equal(p.PendingRewrites, []int{1}) || meta.Workbench.RewriteChapter != 1 {
		t.Fatalf("only explicitly selected chapter may enter queue: progress=%+v workbench=%+v", p, meta.Workbench)
	}
	if err := h.RecordWorkbenchFeedback("chapter", 2, "同时重写第二章"); err == nil {
		t.Fatal("must reject adding a second chapter to an unfinished rewrite")
	}
	gate := NewChapterAdvanceGate(h.store, nil, nil)
	if allowed, err := gate.Allow(&flow.Instruction{Agent: "writer", Chapter: 1}); !allowed || err != nil {
		t.Fatalf("explicit single-chapter rewrite does not require a new forward batch: %v %v", allowed, err)
	}
	if allowed, err := gate.Allow(&flow.Instruction{Agent: "writer", Chapter: 2}); allowed || err != nil {
		t.Fatalf("another completed chapter must remain protected: %v %v", allowed, err)
	}
	if err := h.store.Progress.CompleteRewrite(1); err != nil {
		t.Fatal(err)
	}
	if !gate.HandleBoundary() {
		t.Fatal("single-chapter rewrite must stop for user review")
	}
	meta, _ = h.store.RunMeta.Load()
	if meta.Workbench.RewriteChapter != 0 {
		t.Fatal("single-chapter permission must be consumed at completion")
	}
}

func TestWorkbenchPlanningRevisionRejectsCompleteBookWithoutMutation(t *testing.T) {
	h := workbenchRevisionHost(t)
	if err := h.store.Progress.MarkComplete(); err != nil {
		t.Fatal(err)
	}
	if err := h.ReviseWorkbenchPlanning("修改已完结的目录"); err == nil || !strings.Contains(err.Error(), "作品已完结") {
		t.Fatalf("expected completed-project explanation, got %v", err)
	}
	meta, _ := h.store.RunMeta.Load()
	if meta.Workbench.PlanningRevisionPending || len(meta.Workbench.Feedback) != 0 {
		t.Fatalf("rejected request must not leave pending revision: %+v", meta.Workbench)
	}
}

func TestWorkbenchPendingAuditRejectsDeadlockRerouteToWriter(t *testing.T) {
	h := workbenchRevisionHost(t)
	if err := h.RecordWorkbenchFeedback("planning", 0, "调整未来目录并重新审查"); err != nil {
		t.Fatal(err)
	}
	var writerCalls, arbiterCalls atomic.Int32
	architect := subagent.Config{Name: "architect_short", Description: "idle planner", SystemPrompt: "test", MaxTurns: 3,
		Model: &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message { return testTextMsg("尚未保存审查") }}}
	writer := subagent.Config{Name: "writer", Description: "must not run", SystemPrompt: "test", MaxTurns: 3,
		Model: &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
			writerCalls.Add(1)
			return testTextMsg("unauthorized writing")
		}}}
	arbiter := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		arbiterCalls.Add(1)
		return testTextMsg(`{"action":"reroute","dispatch":{"agent":"writer","task":"先写第一章"},"reason":"规划停滞，尝试正文"}`)
	}}
	e, _, done := newTestEngine(t, h.store, subagent.NewRunner(architect, writer), arbiter)
	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)
	if arbiterCalls.Load() != 1 || writerCalls.Load() != 0 {
		t.Fatalf("final rerouted dispatch must pass the pending-audit gate: arbiter=%d writer=%d", arbiterCalls.Load(), writerCalls.Load())
	}
}

func TestWorkbenchGateAllowsFrozenCommitRecoveryWithoutNewRewritePermit(t *testing.T) {
	h := workbenchRevisionHost(t)
	if err := h.store.Drafts.SaveFinalChapter(1, "已冻结正文"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.ChapterRecords.Accept(1, domain.ChapterOriginGenerated, "已冻结正文", domain.ChapterFacts{}, domain.StyleDelta{}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Progress.MarkChapterComplete(1, 4, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Signals.SavePendingCommit(domain.PendingCommit{Chapter: 1, Stage: domain.CommitStageProgressMarked}); err != nil {
		t.Fatal(err)
	}
	gate := NewChapterAdvanceGate(h.store, nil, nil)
	if allowed, err := gate.Allow(&flow.Instruction{Agent: "writer", Chapter: 1}); !allowed || err != nil {
		t.Fatalf("finishing a frozen commit is not a new rewrite: %v %v", allowed, err)
	}
	// A fresh request must not replace state while this frozen payload still
	// needs its original commit to finish.
	if err := h.RecordWorkbenchFeedback("planning", 0, "调整目录"); err == nil || !strings.Contains(err.Error(), "提交尚未完成") {
		t.Fatalf("new planning requests must wait until the frozen commit is recovered: %v", err)
	}
	if err := h.store.Signals.ClearPendingCommit(); err != nil {
		t.Fatal(err)
	}
	if allowed, err := gate.Allow(&flow.Instruction{Agent: "writer", Chapter: 1}); allowed || err != nil {
		t.Fatalf("after recovery, completed prose must still require consent: %v %v", allowed, err)
	}
}
