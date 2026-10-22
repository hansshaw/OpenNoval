package host

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
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
	meta, err := st.RunMeta.Load()
	if err != nil {
		return "", err
	}
	if meta != nil && meta.Workbench != nil && meta.Workbench.PlanningRevisionPending {
		return "", fmt.Errorf("规划修改尚未完成语义审查，请重新审查调整内容后再确认")
	}
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
	return h.recordWorkbenchFeedback(kind, chapter, text)
}

func (h *Host) recordWorkbenchFeedback(kind string, chapter int, text string) error {
	if err := h.workbenchIdle(); err != nil {
		return err
	}
	if pending, err := h.store.Signals.LoadPendingCommit(); err != nil {
		return err
	} else if pending != nil {
		return fmt.Errorf("第 %d 章提交尚未完成，请先恢复任务完成提交，再调整规划或重写章节", pending.Chapter)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("请填写修改意见")
	}
	if kind == "chapter" {
		p, err := h.store.Progress.Load()
		if err != nil {
			return err
		}
		if p == nil || chapter <= 0 || !slices.Contains(p.CompletedChapters, chapter) {
			return fmt.Errorf("请选择已完成的章节并提交单章修改意见")
		}
		if len(p.PendingRewrites) > 0 && (len(p.PendingRewrites) != 1 || p.PendingRewrites[0] != chapter) {
			return fmt.Errorf("请先完成当前单章重写，再修改另一章")
		}
	} else if kind != "planning" {
		return fmt.Errorf("无效反馈类型")
	}
	if kind == "planning" {
		p, err := h.store.Progress.Load()
		if err != nil {
			return err
		}
		if p != nil && p.Phase == domain.PhaseComplete {
			return fmt.Errorf("作品已完结，没有待生成的目录；修改正文请提交单章修改意见，续写请先重开作品")
		}
		if p != nil && len(p.PendingRewrites) > 0 {
			return fmt.Errorf("请先完成当前单章重写，再调整目录规划")
		}
	}
	if err := h.store.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		if kind == "planning" {
			w.PlanningRevisionPending = true
			w.PlanningRevisionRequest = strings.TrimSpace(text)
			w.ApprovedFingerprint = ""
			w.ApprovedFoundationFingerprint = ""
			w.BatchEnd = 0
		}
		if kind == "chapter" {
			w.RewriteChapter = chapter
			if w.AcceptedChapter >= chapter {
				w.AcceptedChapter = chapter - 1
			}
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
		_, err = h.store.Progress.ApplyReviewOutcome(domain.FlowRewriting, []int{chapter}, text)
		return err
	}
	return nil
}

// ReviseWorkbenchPlanning dispatches the requested planning operation directly;
// no arbiter may reinterpret it as a rewrite of completed prose.
func (h *Host) ReviseWorkbenchPlanning(text string) error {
	h.interMu.Lock()
	defer h.interMu.Unlock()
	if err := h.budget.Refuse(); err != nil {
		return err
	}
	if err := h.recordWorkbenchFeedback("planning", 0, text); err != nil {
		return err
	}
	inst, err := workbenchPlanningInstruction(h.store)
	if err != nil {
		return err
	}
	if !h.startEngine(inst) {
		return fmt.Errorf("规划修改意见已保存，请稍后恢复任务")
	}
	return nil
}

// RewriteWorkbenchChapter creates one durable user-authorized rewrite and
// starts its writer directly, even when an earlier batch was paused midway.
func (h *Host) RewriteWorkbenchChapter(chapter int, text string) error {
	h.interMu.Lock()
	defer h.interMu.Unlock()
	if err := h.budget.Refuse(); err != nil {
		return err
	}
	if err := h.recordWorkbenchFeedback("chapter", chapter, text); err != nil {
		return err
	}
	h.refreshWriterRestore()
	if !h.startEngine(&flow.Instruction{Agent: "writer", Chapter: chapter,
		Task: fmt.Sprintf("仅重写第 %d 章，保留其余已完成章节。严格按用户本次单章指令修改并提交：\n%s", chapter, text), Reason: "用户明确指令单章重写"}) {
		return fmt.Errorf("单章修改意见已保存，请稍后恢复任务")
	}
	return nil
}

func workbenchPlanningInstruction(st *store.Store) (*flow.Instruction, error) {
	meta, err := st.RunMeta.Load()
	if err != nil || meta == nil || meta.Workbench == nil || !meta.Workbench.PlanningRevisionPending {
		return nil, err
	}
	p, err := st.Progress.Load()
	if err != nil {
		return nil, err
	}
	planner := "architect_short"
	if meta.PlanningTier == "" && meta.PlanStart != nil && (meta.PlanStart.Planner == "architect_short" || meta.PlanStart.Planner == "architect_long") {
		planner = meta.PlanStart.Planner
	}
	if meta.PlanningTier == domain.PlanningTierMid || meta.PlanningTier == domain.PlanningTierLong || p != nil && p.Layered {
		planner = "architect_long"
	}
	task := "重新调用 novel_context，核对当前已落盘目录、设定、第一阶段原则及已完成正文的一致性，然后用最新 fingerprint 调用 audit_foundation；审查完成后等待用户确认，不生成正文。"
	missing, err := st.FoundationMissing()
	if err != nil {
		return nil, err
	}
	if !planningFoundationComplete(missing) {
		task = "继续未完成的规划，仅补齐缺项（" + strings.Join(missing, "、") + "），不要覆盖已经落盘且无需修改的设定。" + task
		if meta.PlanStart != nil {
			task += "\n\n第一阶段已裁定的规划任务：\n" + meta.PlanStart.PlannerTask
		}
	}
	if request := meta.Workbench.PlanningRevisionRequest; request != "" {
		task = "用户要求修改规划。先读取 novel_context，以现有已保存结果继续完成本次修改，不重复已完成的修改。正文尚未开始时可用 save_foundation 覆盖目录和设定；已有正文时仍可用 save_foundation(type=\"layered_outline\") 或 save_foundation(type=\"outline\") 做受保护的全量替换，也可用 revise_outline 做受保护局部修订。Store 会拒绝删除已完成章节、截掉非空草稿所在章或绕过未恢复提交事务；全量覆盖范围内仍有草稿时会保留草稿。不得用目录调整隐式改写已完成正文。保留第一阶段记录、创作原则和 user_rules，按本次明确修改要求调整相关设定。\n用户修改意见：\n" + request + "\n\n" + task
	}
	if meta.StartPrompt != "" {
		task += "\n\n第一阶段保留的原始创作需求（与最新明确修改范围合并，未涉及的原则必须继续遵守）：\n" + meta.StartPrompt
	}
	return &flow.Instruction{Agent: planner, Task: task, Reason: "工作台规划修改与重新审查"}, nil
}
