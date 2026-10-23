package tools

import (
	"fmt"
	"slices"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// A direct user edit is already the intended plan. Its next architect task is
// an audit, never permission for model tools to replace those source files. A
// later explicit model revision clears PlanningManualEdit in the Host.
func ensureWorkbenchPlanningMutationAllowed(st *store.Store) error {
	meta, err := st.RunMeta.Load()
	if err != nil {
		return fmt.Errorf("load workbench: %w: %w", errs.ErrStoreRead, err)
	}
	if meta != nil && meta.Workbench != nil && meta.Workbench.PlanningRevisionPending && meta.Workbench.PlanningManualEdit {
		return fmt.Errorf("用户已手动保存规划，本轮只能审查，不能覆盖规划或设定；请重新调用 novel_context 后使用 audit_foundation，通过或列出冲突供用户处理。如需模型调整，请由用户重新提交规划修改指令: %w", errs.ErrToolPrecondition)
	}
	return nil
}

// A planning mutation invalidates both the semantic audit and user approval.
// This also applies to legacy browser projects that have no audit artifact yet.
func invalidateWorkbenchPlanning(st *store.Store) error {
	meta, err := st.RunMeta.Load()
	if err != nil {
		return fmt.Errorf("load workbench: %w", err)
	}
	if meta == nil || meta.Workbench == nil {
		return nil
	}
	return st.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.PlanningRevisionPending = true
		w.ApprovedFingerprint = ""
		w.ApprovedFoundationFingerprint = ""
		w.BatchEnd = 0
		return nil
	})
}

// EnsureWorkbenchRewriteAllowed binds changes to completed prose to a single,
// explicitly selected chapter. An editor's suggestion is not user consent.
// CLI projects retain their existing automatic editorial workflow.
func EnsureWorkbenchRewriteAllowed(st *store.Store, chapter int) error {
	meta, err := st.RunMeta.Load()
	if err != nil {
		return fmt.Errorf("load workbench: %w", err)
	}
	if meta == nil || meta.Workbench == nil {
		return nil
	}
	p, err := st.Progress.Load()
	if err != nil {
		return fmt.Errorf("load progress: %w", err)
	}
	if p == nil || !slices.Contains(p.CompletedChapters, chapter) {
		return nil
	}
	if meta.Workbench.RewriteChapter != chapter || len(p.PendingRewrites) > 1 {
		return fmt.Errorf("已完成正文只能按用户明确指令单章重写；请在章节工作台选择第 %d 章并提交修改意见: %w", chapter, errs.ErrToolPrecondition)
	}
	return nil
}
