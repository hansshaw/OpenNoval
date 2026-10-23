// Modified by hansshaw for AI Novel Studio, 2026-10-24.
// Original third-party attribution and license terms are retained in NOTICE.

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	"github.com/hansshaw/AInoval-web-ui/internal/errs"
	"github.com/hansshaw/AInoval-web-ui/internal/store"
	"github.com/voocel/agentcore/schema"
)

// ExpandNextArcTool 将旧项目当前已完成弧之后的骨架展开为详细章节。
type ExpandNextArcTool struct {
	store *store.Store
}

func NewExpandNextArcTool(store *store.Store) *ExpandNextArcTool {
	return &ExpandNextArcTool{store: store}
}

func (t *ExpandNextArcTool) Name() string  { return "expand_next_arc" }
func (t *ExpandNextArcTool) Label() string { return "展开下一弧" }
func (t *ExpandNextArcTool) Description() string {
	return "仅用于兼容仍含历史骨架弧的旧项目：展开当前已完成弧之后的下一骨架弧。新项目必须一次保存全部卷弧章节。目标卷弧由系统根据进度和大纲确定；只需提交结合已完成事实校准后的 title、goal 和 chapters。"
}

func (t *ExpandNextArcTool) ReadOnly(json.RawMessage) bool        { return false }
func (t *ExpandNextArcTool) ConcurrencySafe(json.RawMessage) bool { return false }
func (t *ExpandNextArcTool) StrictSchema() bool                   { return true }

func (t *ExpandNextArcTool) Schema() map[string]any {
	chapter := schema.Object(
		schema.Property("title", schema.String("章节标题")).Required(),
		schema.Property("core_event", schema.String("本章核心事件")).Required(),
		schema.Property("hook", schema.String("章末钩子")).Required(),
		schema.Property("scenes", schema.Array("计划场景；无则为空数组", schema.String(""))).Required(),
	)
	return schema.Object(
		schema.Property("title", schema.String("结合已完成事实校准后的弧标题")).Required(),
		schema.Property("goal", schema.String("结合已完成事实校准后的弧目标")).Required(),
		schema.Property("chapters", schema.Array("该弧的详细章节计划", chapter)).Required(),
	)
}

func (t *ExpandNextArcTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	if err := ensureWorkbenchPlanningMutationAllowed(t.store); err != nil {
		return nil, err
	}
	var expansion domain.ArcExpansion
	if err := json.Unmarshal(args, &expansion); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	position, err := t.store.ExpandNextArc(expansion)
	if err != nil {
		return nil, fmt.Errorf("expand next arc: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := consumeWriterFeedback(t.store); err != nil {
		return nil, err
	}
	if _, err := t.store.Checkpoints.AppendArtifact(domain.ArcScope(position.Volume, position.Arc), t.Name(), "layered_outline.json"); err != nil {
		return nil, fmt.Errorf("checkpoint %s: %w: %w", t.Name(), errs.ErrStoreWrite, err)
	}
	if err := invalidateWorkbenchPlanning(t.store); err != nil {
		return nil, err
	}
	result := map[string]any{
		"saved":    true,
		"type":     t.Name(),
		"volume":   position.Volume,
		"arc":      position.Arc,
		"title":    expansion.Title,
		"goal":     expansion.Goal,
		"chapters": len(expansion.Chapters),
	}
	volumes, err := t.store.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, fmt.Errorf("load layered outline after expand: %w: %w", errs.ErrStoreRead, err)
	}
	total := len(domain.FlattenOutline(volumes))
	skeletons := domain.SkeletonArcCount(volumes)
	result["outlined_chapters"] = total
	result["skeleton_arcs"] = skeletons
	result["dynamic_planning"] = skeletons > 0
	meta, err := t.store.RunMeta.Load()
	if err != nil {
		return nil, fmt.Errorf("load target chapter contract after expand: %w: %w", errs.ErrStoreRead, err)
	}
	if meta != nil && meta.TargetChapters > 0 {
		addTargetChapterStatus(result, meta.TargetChapters, total, skeletons)
	}
	return json.Marshal(result)
}
