package host

import (
	"fmt"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
	"strings"
	"time"
)

func (h *Host) rulesLoadOptions() rules.LoadOptions {
	return rules.LoadOptions{
		HomeRulesDir:    rules.DefaultHomeRulesDir(),
		ProjectRulesDir: rules.DefaultProjectRulesDir(h.store.Dir()),
	}
}

// EnableWorkbench opts a project into mandatory browser review without changing CLI projects.
func (h *Host) EnableWorkbench() error {
	h.interMu.Lock()
	defer h.interMu.Unlock()
	return h.store.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error { return nil })
}

func (h *Host) workbenchIdle() error {
	h.mu.Lock()
	blocked := h.lifecycle == lifecycleRunning || h.cocreating || h.exclusive != ""
	h.mu.Unlock()
	if blocked || h.engine.isRunning() {
		return fmt.Errorf("请先暂停并等待当前任务结束")
	}
	return h.requireCleanChapters()
}

func planningFoundationComplete(missing []string) bool {
	if len(missing) == 0 {
		return true
	}
	return len(missing) == 1 && missing[0] == "foundation_audit"
}

// currentFoundationFingerprint returns the exact foundation version that may
// enter writing. Legacy projects that already reached writing before audit
// files existed remain readable, but any newly approved project must carry a
// ready audit for that version.
func currentFoundationFingerprint(st *store.Store) (string, error) {
	fp, err := st.FoundationFingerprint()
	if err != nil {
		return "", err
	}
	audit, err := st.Outline.LoadFoundationAudit()
	if err != nil {
		return "", fmt.Errorf("读取基础设定审查: %w", err)
	}
	if audit != nil {
		if !audit.Ready {
			return "", fmt.Errorf("基础设定审查未通过，请按审查问题修改后重新审查")
		}
		if audit.Fingerprint != fp {
			return "", fmt.Errorf("基础设定审查已过期，请重新审查当前角色与世界设定")
		}
		return fp, nil
	}
	p, err := st.Progress.Load()
	if err != nil {
		return "", err
	}
	if p == nil || (p.Phase != domain.PhaseWriting && p.Phase != domain.PhaseComplete) {
		return "", fmt.Errorf("基础设定尚未完成语义审查")
	}
	return fp, nil
}

// ApprovePlanning binds explicit user consent to the exact persisted plan.
func (h *Host) ApprovePlanning(expected string) error {
	h.interMu.Lock()
	defer h.interMu.Unlock()
	if err := h.workbenchIdle(); err != nil {
		return err
	}
	missing, err := h.store.FoundationMissing()
	if err != nil {
		return err
	}
	if !planningFoundationComplete(missing) {
		return fmt.Errorf("规划尚未完成：%s", strings.Join(missing, ", "))
	}
	fp, err := h.store.PlanningFingerprint()
	if err != nil {
		return err
	}
	if expected == "" || expected != fp {
		return fmt.Errorf("规划已变化，请刷新阅读后重新确认")
	}
	foundationFP, err := currentFoundationFingerprint(h.store)
	if err != nil {
		return err
	}
	return h.store.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.ApprovedFingerprint = fp
		w.ApprovedFoundationFingerprint = foundationFP
		return nil
	})
}

// BeginWorkbenchBatch authorizes one contiguous batch. The engine runs through
// the whole range before asking for user review.
func (h *Host) BeginWorkbenchBatch(count int) error {
	h.interMu.Lock()
	defer h.interMu.Unlock()
	if err := h.workbenchIdle(); err != nil {
		return err
	}
	if count < 1 || count > 100 {
		return fmt.Errorf("单次章节数量须为 1–100")
	}
	if err := h.budget.Refuse(); err != nil {
		return err
	}
	p, err := h.store.Progress.Load()
	if err != nil {
		return err
	}
	if p == nil || p.Phase != domain.PhaseWriting {
		return fmt.Errorf("请先完成并确认大纲规划")
	}
	fp, err := h.store.PlanningFingerprint()
	if err != nil {
		return err
	}
	foundationFP, err := currentFoundationFingerprint(h.store)
	if err != nil {
		return err
	}
	if err := h.store.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		if w.ApprovedFingerprint != fp {
			return fmt.Errorf("请先确认当前大纲和目录")
		}
		if w.ApprovedFoundationFingerprint != foundationFP {
			return fmt.Errorf("角色或世界设定已变化，请重新审查并确认基础设定")
		}
		if p.LatestCompleted() > w.AcceptedChapter {
			return fmt.Errorf("请先验收当前批次内容")
		}
		w.BatchSize, w.BatchEnd = count, p.LatestCompleted()+count
		return nil
	}); err != nil {
		return err
	}
	h.refreshWriterRestore()
	if !h.startEngine(nil) {
		return fmt.Errorf("批次已保存，请稍后恢复创作")
	}
	return nil
}

func (h *Host) AcceptWorkbenchChapter(chapter int) error {
	h.interMu.Lock()
	defer h.interMu.Unlock()
	if err := h.workbenchIdle(); err != nil {
		return err
	}
	p, err := h.store.Progress.Load()
	if err != nil {
		return err
	}
	if p == nil || chapter <= 0 || p.LatestCompleted() != chapter {
		return fmt.Errorf("请验收当前最新完成章节")
	}
	if pending, err := h.store.Signals.LoadPendingCommit(); err != nil {
		return err
	} else if pending != nil {
		return fmt.Errorf("章节仍在提交，请稍后验收")
	}
	if h.store.Checkpoints.LatestByStep(domain.ChapterScope(chapter), "commit") == nil {
		return fmt.Errorf("章节提交尚未完成，请先恢复任务")
	}
	if len(p.PendingRewrites) > 0 {
		return fmt.Errorf("章节仍有待处理返工，请先恢复任务")
	}
	if err := h.store.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		if chapter < w.BatchEnd && p.Phase != domain.PhaseComplete {
			return fmt.Errorf("本批次尚未全部完成，请等待批次生成结束")
		}
		if w.AcceptedChapter >= chapter {
			return nil
		}
		w.AcceptedChapter = chapter
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// RecordWorkbenchFeedback persists intent before invoking any model.
func (h *Host) RecordWorkbenchFeedback(kind string, chapter int, text string) error {
	h.interMu.Lock()
	defer h.interMu.Unlock()
	if err := h.workbenchIdle(); err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("请填写修改意见")
	}
	if kind == "chapter" {
		p, err := h.store.Progress.Load()
		if err != nil {
			return err
		}
		if p == nil || chapter <= 0 || chapter > p.LatestCompleted() {
			return fmt.Errorf("请选择本批次内已完成的章节进行反馈")
		}
		meta, err := h.store.RunMeta.Load()
		if err != nil {
			return err
		}
		if meta == nil || meta.Workbench == nil ||
			(meta.Workbench.BatchEnd <= 0) ||
			(chapter > meta.Workbench.BatchEnd && p.Phase != domain.PhaseComplete) ||
			(p.LatestCompleted() < meta.Workbench.BatchEnd && p.Phase != domain.PhaseComplete) {
			return fmt.Errorf("本批次尚未全部完成，请等待批次生成结束后统一审核")
		}
	} else if kind != "planning" {
		return fmt.Errorf("无效反馈类型")
	}
	if err := h.store.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		if kind == "planning" {
			w.ApprovedFingerprint = ""
			w.ApprovedFoundationFingerprint = ""
			w.BatchEnd = 0
		}
		if kind == "chapter" && w.AcceptedChapter >= chapter {
			w.AcceptedChapter = chapter - 1
		}
		w.Feedback = append(w.Feedback, domain.WorkbenchFeedback{Kind: kind, Chapter: chapter, Text: text, Time: time.Now().Format(time.RFC3339)})
		return nil
	}); err != nil {
		return err
	}
	if kind == "chapter" {
		p, err := h.store.Progress.Load()
		if err != nil {
			return err
		}
		// The selected chapter is explicit user intent: persist the rewrite queue
		// directly so an arbiter cannot turn a requested edit into a chat reply.
		if p.Phase == domain.PhaseComplete {
			return tools.ReopenBook(h.store, []int{chapter}, text)
		}
		chapters := append(append([]int(nil), p.PendingRewrites...), chapter)
		_, err = h.store.Progress.ApplyReviewOutcome(domain.FlowRewriting, chapters, text)
		return err
	}
	return nil
}
