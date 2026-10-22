package tools

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// EnsureChapterExpanded verifies that chapter work is in the writing phase and,
// for layered books, inside the currently expanded outline.
func EnsureChapterExpanded(st *store.Store, chapter int) error {
	if st == nil {
		return fmt.Errorf("store 不能为空: %w", errs.ErrToolPrecondition)
	}
	if chapter <= 0 {
		return fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	progress, err := st.Progress.Load()
	if err != nil {
		return fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if progress == nil {
		return fmt.Errorf("progress 未初始化: %w", errs.ErrToolPrecondition)
	}
	if progress.Phase != domain.PhaseWriting {
		return fmt.Errorf("章节写作仅允许在 writing 阶段（当前 phase=%s）: %w", progress.Phase, errs.ErrToolPrecondition)
	}
	if !progress.Layered {
		return nil
	}
	boundary, err := st.Outline.CheckArcBoundary(chapter)
	if err != nil {
		return fmt.Errorf("check layered outline: %w: %w", errs.ErrStoreRead, err)
	}
	if boundary != nil {
		return nil
	}
	return layeredChapterOutOfRangeError(st, chapter)
}

func layeredChapterOutOfRangeError(st *store.Store, chapter int) error {
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		return fmt.Errorf("读取分层大纲: %w: %w", errs.ErrStoreRead, err)
	}
	if len(volumes) == 0 {
		return fmt.Errorf(
			"第 %d 章无法定位：当前没有分层大纲；请先用 save_foundation(type=\"layered_outline\") 保存完整卷弧章节规划: %w",
			chapter, errs.ErrToolPrecondition)
	}
	if domain.HasSkeletonArcs(volumes) {
		return fmt.Errorf(
			"第 %d 章不在当前已展开的分层大纲范围内：这是历史骨架项目，可先用 expand_next_arc 展开下一弧，或用 append_volume 追加包含完整章节的新卷；若全书已完结请调 save_foundation type=complete_book: %w",
			chapter, errs.ErrToolPrecondition)
	}
	return fmt.Errorf(
		"第 %d 章超出当前完整分层规划：请用 revise_outline 修订后续目录，或用 save_foundation(type=\"layered_outline\") 全量覆盖完整卷弧章节，或用 append_volume 追加包含完整章节的新卷；若全书已完结请调 save_foundation type=complete_book: %w",
		chapter, errs.ErrToolPrecondition)
}
