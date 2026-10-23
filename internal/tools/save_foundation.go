// Modified by hansshaw for AI Novel Studio, 2026-10-24.
// Original third-party attribution and license terms are retained in NOTICE.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	"github.com/hansshaw/AInoval-web-ui/internal/errs"
	"github.com/hansshaw/AInoval-web-ui/internal/store"
	"github.com/voocel/agentcore/schema"
)

// SaveFoundationTool 保存基础设定（premise/outline/characters），Architect 专用。
type SaveFoundationTool struct {
	store *store.Store
}

func NewSaveFoundationTool(store *store.Store) *SaveFoundationTool {
	return &SaveFoundationTool{store: store}
}

func (t *SaveFoundationTool) Name() string { return "save_foundation" }
func (t *SaveFoundationTool) Description() string {
	return "保存小说基础设定（premise/outline/characters/world_rules/compass 等）。**这是这些设定的持久化入口**：未经工具调用保存的内容不会进入 store，只在消息里输出 Markdown/JSON 等于丢失。type 可选 premise / outline / layered_outline / characters / world_rules / append_volume / update_compass / complete_book。premise 时 content 必须是 Markdown 字符串；其他类型 content 优先直接传 JSON 数组或对象。outline 全量覆盖逐章目录，章号由系统按数组顺序生成，无需提交 chapter；layered_outline 全量覆盖卷弧目录，必须一次提交用户要求范围内的全部卷、全部弧和每弧真实 chapters，不再保存 estimated_chapters 骨架。已有正文时仍可全量覆盖，但 Store 会拒绝删除已完成章节、截掉非空在写草稿所在章或绕过未恢复提交事务；草稿仍在覆盖范围内时保留。append_volume 追加新卷（content 为 VolumeOutline JSON，卷弧序号由系统生成；新卷的每个弧都必须有真实 chapters；顶层带 \"final\": true 即宣告收官卷——全书在该卷收束，所有章节写完后自动完结，无需再调 complete_book）；update_compass 更新终局方向（content 为 StoryCompass JSON）；complete_book 宣告全书完结（content 传空对象 {}，直接推 Phase=Complete；工具会校验：大纲内章节已全部写完、无返工队列、compass 无未收束 open_threads——确认长线已收束须先 update_compass 清空 open_threads 落盘，想提前收束用 append_volume 的 final 收官卷）。append_volume / complete_book 必须带 reason 参数（一句话判定理由，对照完结判定清单，记入裁定审计）。scale 可选，仅允许 short / mid / long。"
}
func (t *SaveFoundationTool) Label() string { return "保存设定" }

// 写工具（跨域更新 Outline/Progress/Characters），禁止并发。
func (t *SaveFoundationTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveFoundationTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *SaveFoundationTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("type", schema.Enum("设定类型", "premise", "outline", "layered_outline", "characters", "world_rules", "append_volume", "update_compass", "complete_book")).Required(),
		schema.Property("content", map[string]any{
			"description": "内容。premise 传 Markdown 字符串；其他类型直接传 JSON 数组或对象即可，也兼容传 JSON 字符串。",
		}).Required(),
		schema.Property("scale", schema.Enum("规划级别", "short", "mid", "long")),
		schema.Property("reason", schema.String("卷末判定理由（append_volume / complete_book 时必填）：对照完结判定清单，一句话说明为何续卷、宣告收官或完结")),
	)
}

func (t *SaveFoundationTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	if err := ensureWorkbenchPlanningMutationAllowed(t.store); err != nil {
		return nil, err
	}
	var a struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
		Scale   string          `json:"scale"`
		Reason  string          `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	content, err := normalizeFoundationContent(a.Content)
	if err != nil {
		return nil, err
	}
	if a.Scale != "" {
		switch domain.PlanningTier(a.Scale) {
		case domain.PlanningTierShort, domain.PlanningTierMid, domain.PlanningTierLong:
		default:
			return nil, fmt.Errorf("invalid scale %q, expected short/mid/long: %w", a.Scale, errs.ErrToolArgs)
		}
	}

	// Progress 是所有创作写操作共享的状态事实。先统一读取，确保损坏状态会让
	// scale/write 等任何落盘动作在发生前失败，而不是写了一半才发现无法推进。
	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}

	result := map[string]any{"saved": true, "type": a.Type, "scale": a.Scale}

	if a.Scale != "" {
		if err := t.store.RunMeta.SetPlanningTier(domain.PlanningTier(a.Scale)); err != nil {
			return nil, fmt.Errorf("save planning tier: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	// 卷末三选一（续卷/收官/完结）是全书最重的语义判断，理由必须成为审计事实
	// （decisions.jsonl，与 plan_start/intervention 同一条流水），否则收官过早/
	// 续卷失当只能翻会话日志排障。事实快照取判定时刻（变更落盘前）的进度。
	volumeEnd := a.Type == "append_volume" || a.Type == "complete_book"
	if volumeEnd && strings.TrimSpace(a.Reason) == "" {
		return nil, fmt.Errorf("%s 必须带 reason 参数：对照完结判定清单，一句话说明本次为何续卷、宣告收官或完结: %w", a.Type, errs.ErrToolArgs)
	}
	var volumeEndFacts json.RawMessage
	if volumeEnd {
		if progress != nil {
			facts := map[string]any{"completed_chapters": len(progress.CompletedChapters)}
			volumes, volumesErr := t.store.Outline.LoadLayeredOutline()
			if volumesErr != nil {
				return nil, fmt.Errorf("load layered outline for volume-end facts: %w: %w", errs.ErrStoreRead, volumesErr)
			}
			if len(volumes) > 0 {
				outline := domain.FlattenOutline(volumes)
				facts["total_chapters"] = len(outline)
				facts["outlined_chapters"] = len(outline)
				if skeletons := domain.SkeletonArcCount(volumes); skeletons > 0 {
					facts["dynamic_planning"] = true
					facts["skeleton_arcs"] = skeletons
				}
			} else {
				outline, outlineErr := t.store.Outline.LoadOutlineView()
				if outlineErr != nil {
					return nil, fmt.Errorf("load outlined chapters for volume-end facts: %w: %w", errs.ErrStoreRead, outlineErr)
				}
				total := len(outline)
				if total == 0 {
					total = progress.TotalChapters
				}
				facts["total_chapters"] = total
				facts["outlined_chapters"] = total
			}
			volumeEndFacts, err = json.Marshal(facts)
			if err != nil {
				return nil, fmt.Errorf("marshal volume-end facts: %w", err)
			}
		}
	}

	decode := func(typeName string, out any) error {
		return decodeFoundationJSON(typeName, content, out)
	}

	switch a.Type {
	case "premise":
		if err := t.store.Outline.SavePremise(content); err != nil {
			return nil, fmt.Errorf("save premise: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := t.store.Progress.AdvancePhase(domain.PhasePremise); err != nil {
			return nil, fmt.Errorf("update premise phase: %w: %w", errs.ErrStoreWrite, err)
		}

	case "outline":
		var entries []domain.OutlineEntry
		if err := decode("outline", &entries); err != nil {
			return nil, err
		}
		facts, err := validateOutlineStructure(entries)
		if err != nil {
			return nil, err
		}
		total, err := t.store.ReplaceFlatOutline(entries)
		if err != nil {
			return nil, fmt.Errorf("replace outline: %w", err)
		}
		target, err := t.targetChapterCount()
		if err != nil {
			return nil, err
		}
		result["chapters"] = len(entries)
		result["total_chapters"] = total
		result["dynamic_planning"] = false
		result["missing_scenes"] = facts.MissingScenes
		addTargetChapterStatus(result, target, total, 0)
		if err := consumeWriterFeedback(t.store); err != nil {
			return nil, err
		}

	case "layered_outline":
		var volumes []domain.VolumeOutline
		if err := decode("layered_outline", &volumes); err != nil {
			return nil, err
		}
		facts, err := validateLayeredOutlineStructure(volumes)
		if err != nil {
			return nil, err
		}
		total, err := t.store.ReplaceLayeredOutline(volumes)
		if err != nil {
			return nil, fmt.Errorf("replace layered_outline: %w", err)
		}
		savedVolumes, err := t.store.Outline.LoadLayeredOutline()
		if err != nil {
			return nil, fmt.Errorf("load saved layered_outline: %w: %w", errs.ErrStoreRead, err)
		}
		target, err := t.targetChapterCount()
		if err != nil {
			return nil, err
		}
		result["volumes"] = len(volumes)
		result["dynamic_planning"] = facts.SkeletonArcs > 0
		result["total_chapters"] = total
		result["outlined_chapters"] = total
		result["arcs"] = facts.Arcs
		result["skeleton_arcs"] = facts.SkeletonArcs
		result["missing_scenes"] = facts.MissingScenes
		addTargetChapterStatus(result, target, total, facts.SkeletonArcs)
		if domain.FinaleVolume(savedVolumes) > 0 {
			result["final_volume"] = true
		}
		if err := consumeWriterFeedback(t.store); err != nil {
			return nil, err
		}

	case "characters":
		var chars []domain.Character
		if err := decode("characters", &chars); err != nil {
			return nil, err
		}
		if err := t.store.Characters.Save(chars); err != nil {
			return nil, fmt.Errorf("save characters: %w: %w", errs.ErrStoreWrite, err)
		}
		result["count"] = len(chars)

	case "world_rules":
		var rules []domain.WorldRule
		if err := decode("world_rules", &rules); err != nil {
			return nil, err
		}
		if err := t.store.World.SaveWorldRules(rules); err != nil {
			return nil, fmt.Errorf("save world_rules: %w: %w", errs.ErrStoreWrite, err)
		}
		result["count"] = len(rules)

	case "append_volume":
		if progress != nil && progress.Phase == domain.PhaseComplete {
			return nil, fmt.Errorf("全书已完结（phase=complete），不允许追加新卷: %w", errs.ErrToolPrecondition)
		}
		var vol domain.VolumeOutline
		if err := decode("append_volume", &vol); err != nil {
			return nil, err
		}
		if _, err := validateVolumeStructure(vol, 1); err != nil {
			return nil, err
		}
		prior, err := t.store.Outline.LoadLayeredOutline()
		if err != nil {
			return nil, fmt.Errorf("load layered outline: %w: %w", errs.ErrStoreRead, err)
		}
		saved, err := t.store.AppendVolume(vol)
		if err != nil {
			return nil, fmt.Errorf("append volume: %w: %w", errs.ErrStoreWrite, err)
		}
		result["volume"] = saved.Index
		if saved.Final {
			result["final_volume"] = true
		} else if domain.FinaleVolume(prior) > 0 {
			// 事实回显：此前宣告的收官态因追加普通新卷而解除（新卷成为末卷）
			result["finale_released"] = true
		}
		result["arcs"] = len(saved.Arcs)
		chCount := 0
		for _, arc := range saved.Arcs {
			chCount += len(arc.DetailedChapters())
		}
		if chCount > 0 {
			result["chapters"] = chCount
		}
		total, skeletons, err := t.layeredChapterStatus()
		if err != nil {
			return nil, err
		}
		result["total_chapters"] = total
		result["outlined_chapters"] = total
		result["skeleton_arcs"] = skeletons
		result["dynamic_planning"] = skeletons > 0
		target, err := t.targetChapterCount()
		if err != nil {
			return nil, err
		}
		addTargetChapterStatus(result, target, total, skeletons)
		if err := consumeWriterFeedback(t.store); err != nil {
			return nil, err
		}

	case "complete_book":
		// 全书完结的唯一入口：直接推 Phase=Complete。
		// 仅 Writing 阶段允许，防止规划阶段误调跳过整本写作。
		// 拒绝有返工队列时调用——保证 PendingRewrites 跑完才能结束。
		if progress == nil {
			return nil, fmt.Errorf("progress 未初始化: %w", errs.ErrToolPrecondition)
		}
		if progress.Phase != domain.PhaseWriting {
			return nil, fmt.Errorf("complete_book 仅在 writing 阶段可调用（当前 phase=%s）: %w", progress.Phase, errs.ErrToolPrecondition)
		}
		if len(progress.PendingRewrites) > 0 {
			return nil, fmt.Errorf("还有 %d 章在返工队列中，处理完再调 complete_book: %w", len(progress.PendingRewrites), errs.ErrToolPrecondition)
		}
		// 可枚举的完本前置校验必须在代码层(三分法),不能只依赖提示词里的
		// "完结判定清单"——真实事故:规划刚落盘 phase 翻到 writing,弱模型顺手
		// 误调 complete_book,0/68 章被直接标记完本。
		if len(progress.CompletedChapters) == 0 {
			return nil, fmt.Errorf("一章未写不可完本;规划完成后写作由系统自动推进,无需调用 complete_book: %w", errs.ErrToolPrecondition)
		}
		next := progress.NextChapter()
		plannedTotal := progress.TotalChapters
		if progress.Layered {
			volumes, outlineErr := t.store.Outline.LoadLayeredOutline()
			if outlineErr != nil {
				return nil, fmt.Errorf("load layered outline: %w: %w", errs.ErrStoreRead, outlineErr)
			}
			if skeletons := domain.SkeletonArcCount(volumes); skeletons > 0 {
				return nil, fmt.Errorf("分层大纲仍有 %d 个历史骨架弧未展开为真实章节，不能完本；请先逐弧展开并写完全部规划: %w", skeletons, errs.ErrToolPrecondition)
			}
			outline := domain.FlattenOutline(volumes)
			if next <= len(outline) {
				return nil, fmt.Errorf("当前详细大纲还有未写章节（下一章 %d/当前已细化 %d），不可完本；想提前收束请改用 append_volume 且卷 JSON 顶层带 \"final\": true 宣告收官卷: %w", next, len(outline), errs.ErrToolPrecondition)
			}
			plannedTotal = len(outline)
		} else if progress.TotalChapters > 0 && next <= progress.TotalChapters {
			return nil, fmt.Errorf("大纲内还有未写章节（下一章 %d/共 %d），不可完本；想提前收束请改用 append_volume 且卷 JSON 顶层带 \"final\": true 宣告收官卷: %w", next, progress.TotalChapters, errs.ErrToolPrecondition)
		}
		if err := t.store.CheckExactChapterTarget(plannedTotal); err != nil {
			return nil, err
		}
		// 活跃长线未收束不可完本——OpenThreads 的字段契约即"需收束才能结局"。这不是
		// 语义复判：真认为已全部收束，先 update_compass 清空 open_threads 再完本，把
		// "论述里豁免"变成可审计的落盘动作（实测导入完本书续写时，架构师引经据典绕过
		// 完结清单第 3 条直接完本，用户的续写诉求被完本规则锁死）。
		compass, err := t.store.Outline.LoadCompass()
		if err != nil {
			return nil, fmt.Errorf("load compass: %w: %w", errs.ErrStoreRead, err)
		}
		if compass != nil && len(compass.OpenThreads) > 0 {
			return nil, fmt.Errorf("compass 还有 %d 条活跃长线未收束（如：%s），不可完本。确认已全部收束请先 update_compass 清空 open_threads 再调 complete_book；仍需展开请 append_volume（可带 \"final\": true 宣告收官卷）: %w",
				len(compass.OpenThreads), compass.OpenThreads[0], errs.ErrToolPrecondition)
		}
		if err := t.store.Progress.MarkComplete(); err != nil {
			return nil, fmt.Errorf("mark complete: %w: %w", errs.ErrStoreWrite, err)
		}
		result["book_complete"] = true
		result["phase"] = string(domain.PhaseComplete)

	case "update_compass":
		var compass domain.StoryCompass
		if err := decode("compass", &compass); err != nil {
			return nil, err
		}
		// 工具层强制覆盖 LastUpdated 为当前已完成章节数，不信任 LLM 自填。
		// LLM 通常忘填或留 0，会让 diag.CompassDrift 误报、Router 路由失真。
		if progress != nil {
			compass.LastUpdated = progress.LatestCompleted()
		}
		if err := t.store.Outline.SaveCompass(compass); err != nil {
			return nil, fmt.Errorf("save compass: %w: %w", errs.ErrStoreWrite, err)
		}
		result["ending_direction"] = compass.EndingDirection
		result["last_updated"] = compass.LastUpdated
		if err := consumeWriterFeedback(t.store); err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("unknown type %q, expected premise/outline/layered_outline/characters/world_rules/append_volume/update_compass/complete_book: %w", a.Type, errs.ErrToolArgs)
	}

	if _, err := t.store.Checkpoints.AppendArtifact(domain.GlobalScope(), a.Type, foundationArtifact(a.Type)); err != nil {
		return nil, fmt.Errorf("checkpoint foundation %s: %w: %w", a.Type, errs.ErrStoreWrite, err)
	}

	if volumeEnd {
		t.recordVolumeEndDecision(a.Type, a.Reason, volumeEndFacts, result)
	}
	if a.Type != "complete_book" {
		if err := invalidateWorkbenchPlanning(t.store); err != nil {
			return nil, err
		}
	}

	// 返回剩余未完成项。初始工件齐全后仍会剩 foundation_audit；只有
	// audit_foundation 对实际落盘版本给出 ready=true，才允许进入 writing。
	remaining, err := t.store.FoundationMissing()
	if err != nil {
		return nil, fmt.Errorf("load foundation state: %w: %w", errs.ErrStoreRead, err)
	}
	ready := len(remaining) == 0
	result["remaining"] = remaining
	result["foundation_ready"] = ready
	return json.Marshal(result)
}

type outlineStructureFacts struct {
	MissingScenes int
}

type layeredOutlineStructureFacts struct {
	Arcs          int
	SkeletonArcs  int
	Chapters      int
	MissingScenes int
}

func validateOutlineStructure(entries []domain.OutlineEntry) (outlineStructureFacts, error) {
	if len(entries) == 0 {
		return outlineStructureFacts{}, fmt.Errorf("outline 不能为空：至少需要一章详细规划: %w", errs.ErrToolArgs)
	}
	facts := outlineStructureFacts{}
	for i, entry := range entries {
		if err := validateOutlineEntry(entry, i+1); err != nil {
			return facts, err
		}
		if len(nonBlankStrings(entry.Scenes)) == 0 {
			facts.MissingScenes++
		}
	}
	return facts, nil
}

func validateLayeredOutlineStructure(volumes []domain.VolumeOutline) (layeredOutlineStructureFacts, error) {
	if len(volumes) == 0 {
		return layeredOutlineStructureFacts{}, fmt.Errorf("layered_outline 不能为空：至少需要一卷，且每一卷的每一弧都必须包含真实章节: %w", errs.ErrToolArgs)
	}
	facts := layeredOutlineStructureFacts{}
	for i := range volumes {
		volumeFacts, err := validateVolumeStructure(volumes[i], i+1)
		if err != nil {
			return facts, err
		}
		facts.Arcs += volumeFacts.Arcs
		facts.SkeletonArcs += volumeFacts.SkeletonArcs
		facts.Chapters += volumeFacts.Chapters
		facts.MissingScenes += volumeFacts.MissingScenes
	}
	return facts, nil
}

func validateVolumeStructure(volume domain.VolumeOutline, position int) (layeredOutlineStructureFacts, error) {
	facts := layeredOutlineStructureFacts{}
	if strings.TrimSpace(volume.Title) == "" {
		return facts, fmt.Errorf("第 %d 卷缺少 title；拒绝保存可能被截断的规划: %w", position, errs.ErrToolArgs)
	}
	if len(volume.Arcs) == 0 {
		return facts, fmt.Errorf("第 %d 卷 %q 没有 arcs；每一卷至少需要一个包含真实章节的弧: %w", position, volume.Title, errs.ErrToolArgs)
	}
	for i, arc := range volume.Arcs {
		if strings.TrimSpace(arc.Title) == "" {
			return facts, fmt.Errorf("第 %d 卷第 %d 弧缺少 title: %w", position, i+1, errs.ErrToolArgs)
		}
		if strings.TrimSpace(arc.Goal) == "" {
			return facts, fmt.Errorf("第 %d 卷 %q 的第 %d 弧缺少 goal: %w", position, volume.Title, i+1, errs.ErrToolArgs)
		}
		facts.Arcs++
		if arc.IsSkeleton() {
			facts.SkeletonArcs++
			return facts, fmt.Errorf(
				"第 %d 卷 %q 的第 %d 弧没有真实 chapters；新规划必须一次完成所有卷弧章节，不接受 estimated_chapters 骨架: %w",
				position, volume.Title, i+1, errs.ErrToolArgs,
			)
		}
		for j, entry := range arc.Chapters {
			if err := validateOutlineEntry(entry, j+1); err != nil {
				return facts, fmt.Errorf("第 %d 卷 %q 第 %d 弧：%w", position, volume.Title, i+1, err)
			}
			facts.Chapters++
			if len(nonBlankStrings(entry.Scenes)) == 0 {
				facts.MissingScenes++
			}
		}
	}
	return facts, nil
}

func (t *SaveFoundationTool) targetChapterCount() (int, error) {
	meta, err := t.store.RunMeta.Load()
	if err != nil {
		return 0, fmt.Errorf("load target chapter contract: %w: %w", errs.ErrStoreRead, err)
	}
	if meta == nil {
		return 0, nil
	}
	return meta.TargetChapters, nil
}

func (t *SaveFoundationTool) layeredChapterStatus() (total, skeletons int, err error) {
	volumes, err := t.store.Outline.LoadLayeredOutline()
	if err != nil {
		return 0, 0, fmt.Errorf("load layered outline status: %w: %w", errs.ErrStoreRead, err)
	}
	return len(domain.FlattenOutline(volumes)), domain.SkeletonArcCount(volumes), nil
}

func addTargetChapterStatus(result map[string]any, target, actual, skeletonArcs int) {
	if target <= 0 {
		return
	}
	result["target_chapters"] = target
	met := skeletonArcs == 0 && actual == target
	result["target_met"] = met
	if !met && skeletonArcs == 0 {
		result["target_delta"] = actual - target
	}
}

func validateOutlineEntry(entry domain.OutlineEntry, displayChapter int) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "title", value: entry.Title},
		{name: "core_event", value: entry.CoreEvent},
		{name: "hook", value: entry.Hook},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("章节 %d 缺少 %s；拒绝保存可能被截断的章节规划: %w", displayChapter, field.name, errs.ErrToolArgs)
		}
	}
	return nil
}

func nonBlankStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}

func foundationArtifact(t string) string {
	switch t {
	case "premise":
		return "premise.md"
	case "outline":
		return "outline.json"
	case "layered_outline", "append_volume":
		return "layered_outline.json"
	case "complete_book":
		return "meta/progress.json"
	case "characters":
		return "characters.json"
	case "world_rules":
		return "world_rules.json"
	case "update_compass":
		return "meta/compass.json"
	default:
		return ""
	}
}

// decodeFoundationJSON 解析 save_foundation 的 content 字段，失败时附上行列位置
// 和最常见的修复提示，让 LLM 下一次重试能直接定位而不是盲猜。
func decodeFoundationJSON(typeName, content string, out any) error {
	err := json.Unmarshal([]byte(content), out)
	if err == nil {
		return nil
	}
	hint := `常见原因：字符串值中的双引号未转义为 \", 换行未转义为 \n, 或对象字段间漏了逗号。请整段重新生成一次。`
	if se, ok := err.(*json.SyntaxError); ok {
		line, col := offsetToLineCol(content, int(se.Offset))
		return fmt.Errorf("parse %s JSON (line %d col %d): %w — %s", typeName, line, col, err, hint)
	}
	return fmt.Errorf("parse %s JSON: %w — %s", typeName, err, hint)
}

func offsetToLineCol(s string, offset int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(s) {
		offset = len(s)
	}
	line, col := 1, 1
	for i := 0; i < offset; i++ {
		if s[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

func normalizeFoundationContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("content is required: %w", errs.ErrToolArgs)
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}

	if !json.Valid(raw) {
		return "", fmt.Errorf("invalid content: expected Markdown string or valid JSON value: %w", errs.ErrToolArgs)
	}
	return string(raw), nil
}

// recordVolumeEndDecision 把卷末三选一（续卷/收官/完结）的判定理由落进裁定审计。
// best-effort：结构变更已落盘，审计失败只告警不回滚——报错会让模型重试已完成
// 的操作（重复追加卷）。
func (t *SaveFoundationTool) recordVolumeEndDecision(action, reason string, facts json.RawMessage, result map[string]any) {
	decision := map[string]any{"action": action}
	if v, ok := result["volume"]; ok {
		decision["volume"] = v
	}
	if _, ok := result["final_volume"]; ok {
		decision["final"] = true
	}
	raw, err := json.Marshal(decision)
	if err != nil {
		slog.Error("卷末裁定序列化失败", "module", "tools", "action", action, "err", err)
		return
	}
	if _, err := t.store.Decisions.Append(store.DecisionRecord{
		Kind:     "volume_end",
		Decider:  "architect",
		Facts:    facts,
		Decision: raw,
		Reason:   reason,
	}); err != nil {
		slog.Error("卷末裁定审计落盘失败", "module", "tools", "action", action, "err", err)
	}
}

// consumeWriterFeedback 在结构操作成功后清除已处理的规划反馈。
func consumeWriterFeedback(st *store.Store) error {
	if err := st.Outline.ClearOutlineFeedback(); err != nil {
		return fmt.Errorf("clear outline feedback: %w: %w", errs.ErrStoreWrite, err)
	}
	return nil
}
