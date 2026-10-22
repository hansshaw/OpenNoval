package store

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
)

// chapterTargetContract keeps the user's exact chapter target inside the same
// cross-domain lock as outline mutations. Only SetTargetChapters and
// ApplyTargetChapterRevision may replace that target; saving a different-sized
// model plan can never rewrite the user's contract implicitly.
type chapterTargetContract struct {
	meta *domain.RunMeta
}

func (s *Store) loadChapterTargetContractUnlocked() (chapterTargetContract, error) {
	meta, err := s.RunMeta.loadUnlocked()
	if err != nil {
		return chapterTargetContract{}, fmt.Errorf("load chapter target: %w: %w", errs.ErrStoreRead, err)
	}
	return chapterTargetContract{meta: meta}, nil
}

func (c chapterTargetContract) target() int {
	if c.meta == nil {
		return 0
	}
	return c.meta.TargetChapters
}

func (c chapterTargetContract) validateExact(actual int) error {
	target := c.target()
	if target <= 0 || actual == target {
		return nil
	}
	return fmt.Errorf(
		"完整规划章节数与项目目标不一致：目标 %d 章，实际 %d 章；请按用户要求完成全部卷弧的真实 chapters 后重新保存: %w",
		target, actual, errs.ErrToolArgs,
	)
}

// validateTail is used by legacy skeleton projects, where the exact target
// describes the final full plan rather than the chapters expanded so far.
// Every remaining skeleton arc must still have room for at least one real
// chapter, otherwise append/revise operations could silently make the target
// impossible before the final expansion.
func (c chapterTargetContract) validateTail(actual, skeletonArcs int) error {
	target := c.target()
	if target <= 0 {
		return nil
	}
	minimumFinalTotal := actual + skeletonArcs
	if minimumFinalTotal > target {
		return fmt.Errorf(
			"当前已展开章节与剩余骨架至少需要 %d 章（已展开 %d，骨架弧 %d），超过项目目标 %d 章；请按目标重新规划: %w",
			minimumFinalTotal, actual, skeletonArcs, target, errs.ErrToolArgs,
		)
	}
	if skeletonArcs == 0 && actual != target {
		return c.validateExact(actual)
	}
	return nil
}

// CheckExactChapterTarget verifies a completed/full plan against the user's
// target without treating any pending revision as implicit authorization.
func (s *Store) CheckExactChapterTarget(actual int) error {
	s.RunMeta.io.mu.RLock()
	defer s.RunMeta.io.mu.RUnlock()

	meta, err := s.RunMeta.loadUnlocked()
	if err != nil {
		return fmt.Errorf("load chapter target: %w: %w", errs.ErrStoreRead, err)
	}
	if meta == nil || meta.TargetChapters <= 0 || meta.TargetChapters == actual {
		return nil
	}
	return fmt.Errorf(
		"完整规划章节数与项目目标不一致：目标 %d 章，实际 %d 章；请先补齐或修订完整规划: %w",
		meta.TargetChapters, actual, errs.ErrToolPrecondition,
	)
}
