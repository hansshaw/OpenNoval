package tools

import (
	"fmt"
	"slices"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

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
