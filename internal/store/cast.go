// Modified by hansshaw for AI Novel Studio, 2026-10-24.
// Original third-party attribution and license terms are retained in NOTICE.

package store

import (
	"fmt"

	"github.com/hansshaw/AInoval-web-ui/internal/domain"
)

// BuildCast 从接纳记录构建当前配角视图。
func (s *Store) BuildCast(chapters []int) ([]domain.CastEntry, error) {
	records, err := s.ChapterRecords.LoadCompleted(chapters)
	if err != nil {
		return nil, fmt.Errorf("读取章节记录: %w", err)
	}
	characters, err := s.Characters.Load()
	if err != nil {
		return nil, fmt.Errorf("读取核心角色: %w", err)
	}
	return domain.ProjectCast(records, characters), nil
}
