package host

import (
	"fmt"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/tools"
	"strings"
	"time"
)

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
	if len(missing) != 0 {
		return fmt.Errorf("规划尚未完成：%s", strings.Join(missing, ", "))
	}
	fp, err := h.store.PlanningFingerprint()
	if err != nil {
		return err
	}
	if expected == "" || expected != fp {
		return fmt.Errorf("规划已变化，请刷新阅读后重新确认")
	}
	return h.store.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.ApprovedFingerprint = fp
		return nil
	})
}

// BeginWorkbenchBatch authorizes a range; every committed chapter still needs acceptance.
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
	if err := h.store.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		if w.ApprovedFingerprint != fp {
			return fmt.Errorf("请先确认当前大纲和目录")
		}
		if p.LatestCompleted() > w.AcceptedChapter {
			return fmt.Errorf("请先验收最近完成的章节")
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
	more := false
	if err := h.store.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		if w.AcceptedChapter >= chapter {
			return nil
		}
		w.AcceptedChapter = chapter
		more = p.Phase != domain.PhaseComplete && chapter < w.BatchEnd
		return nil
	}); err != nil {
		return err
	}
	if more {
		if err := h.budget.Refuse(); err != nil {
			return err
		}
		h.refreshWriterRestore()
		if !h.startEngine(nil) {
			return fmt.Errorf("验收已保存，请稍后恢复创作")
		}
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
		if p == nil || chapter <= 0 || p.LatestCompleted() != chapter {
			return fmt.Errorf("请选择最新完成章节进行反馈")
		}
	} else if kind != "planning" {
		return fmt.Errorf("无效反馈类型")
	}
	if err := h.store.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		if kind == "planning" {
			w.ApprovedFingerprint = ""
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
