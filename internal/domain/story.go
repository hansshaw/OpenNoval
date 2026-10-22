package domain

import (
	"fmt"
	"strings"
)

// BookMetadata 是面向读者和出版物的作品信息。
// 创作设定属于 Foundation，运行进度属于 Progress，二者都不承载这份数据。
type BookMetadata struct {
	Title    string `json:"title"`
	Synopsis string `json:"synopsis"`
}

// Normalized 返回可持久化、可比较的规范值。
func (b BookMetadata) Normalized() BookMetadata {
	b.Title = strings.TrimSpace(b.Title)
	b.Synopsis = strings.TrimSpace(b.Synopsis)
	return b
}

// Validate 检查作品信息的必填字段。
func (b BookMetadata) Validate() error {
	b = b.Normalized()
	if b.Title == "" {
		return fmt.Errorf("book title is required")
	}
	if b.Synopsis == "" {
		return fmt.Errorf("book synopsis is required")
	}
	return nil
}

// OutlineEntry 大纲条目，对应一章。
type OutlineEntry struct {
	Chapter     int      `json:"chapter"`
	Title       string   `json:"title"`
	CoreEvent   string   `json:"core_event"`
	Hook        string   `json:"hook"`
	Scenes      []string `json:"scenes"`
	VolumeID    int      `json:"volume_id,omitempty"` // 扁平大纲可显式分卷；分层大纲由父卷派生。
	VolumeTitle string   `json:"volume_title,omitempty"`
	ArcID       int      `json:"arc_id,omitempty"` // 卷内弧序号；分层大纲由父弧派生。
	ArcTitle    string   `json:"arc_title,omitempty"`
}

// Character 角色档案。
type Character struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"` // 别名/称号/绰号（如"废物少年"、"炎哥"）
	Role        string   `json:"role"`
	Description string   `json:"description"`
	Arc         string   `json:"arc"`
	Traits      []string `json:"traits"`
	Tier        string   `json:"tier,omitempty"` // core / important / secondary / decorative（默认 important）
}

// VolumeOutline 卷级大纲（长篇分层模式）。
type VolumeOutline struct {
	Index int          `json:"index"`
	Title string       `json:"title"`
	Theme string       `json:"theme"`           // 本卷核心冲突/主题
	Final bool         `json:"final,omitempty"` // 收官卷：全书在本卷收束；完整规划由 Store 规范最后一卷，续卷时由 append_volume 宣告
	Arcs  []ArcOutline `json:"arcs"`
}

// IsExpanded 判断卷是否已展开（有弧级结构）。
func (v *VolumeOutline) IsExpanded() bool { return len(v.Arcs) > 0 }

// FinaleVolume 返回已宣告的收官卷序号，未宣告返回 0。
// 收官事实 = "最后一卷带 Final 标记"：宣告后全书进入收束态（规划收线、终卷结构
// 写完即完结）；若此后又追加了未标记的新卷，新卷成为最后一卷，收束态自然解除——
// 因此无需撤销工具，状态永远可从大纲数据推导。
func FinaleVolume(volumes []VolumeOutline) int {
	if n := len(volumes); n > 0 && volumes[n-1].Final {
		return volumes[n-1].Index
	}
	return 0
}

// StoryCompass 终局方向指南针，替代固定的骨架卷列表。
// Architect 在每次卷边界时可更新，允许故事方向随创作演化。
type StoryCompass struct {
	EndingDirection string   `json:"ending_direction"`          // 终局方向（主题性描述）
	OpenThreads     []string `json:"open_threads,omitempty"`    // 活跃长线（需收束才能结局）
	EstimatedScale  string   `json:"estimated_scale,omitempty"` // 模糊规模（如"预计 4-6 卷"）
	LastUpdated     int      `json:"last_updated,omitempty"`    // 更新时的已完成章节数
}

// ArcOutline 弧级大纲。
type ArcOutline struct {
	Index             int            `json:"index"` // 卷内弧序号
	Title             string         `json:"title"`
	Goal              string         `json:"goal"`                         // 弧目标（起承转合）
	EstimatedChapters int            `json:"estimated_chapters,omitempty"` // 仅兼容历史骨架弧的容量估算；新规划不使用
	Chapters          []OutlineEntry `json:"chapters"`
}

// IsSkeleton 判断弧是否仍是旧版按需展开模型留下的骨架。
// 旧数据可能完全没有 chapters，也可能用单个占位章节加 estimated_chapters
// 表示待展开。正常单章弧不会同时携带容量估算，因此不会被误判为骨架。
func (a *ArcOutline) IsSkeleton() bool {
	if a == nil {
		return false
	}
	if len(a.Chapters) == 0 {
		return true
	}
	return len(a.Chapters) == 1 && a.EstimatedChapters > 0
}

// IsExpanded 判断弧是否已有可写的真实章节。
func (a *ArcOutline) IsExpanded() bool { return a != nil && !a.IsSkeleton() }

// DetailedChapters 返回可进入故事顺序的真实章节。历史骨架弧即使带一个
// 占位章节也不得参与编号、定位、边界或容量计算。
func (a *ArcOutline) DetailedChapters() []OutlineEntry {
	if a == nil || a.IsSkeleton() {
		return nil
	}
	return a.Chapters
}

// ArcExpansion 是 Architect 在结构边界对一个未写弧作出的完整规划。
// Title/Goal 不是骨架的机械副本：模型可依据已完成正文修订尚未发生的计划。
type ArcExpansion struct {
	Title    string         `json:"title"`
	Goal     string         `json:"goal"`
	Chapters []OutlineEntry `json:"chapters"`
}

// EstimatedChapterCapacity 计算历史分层大纲的兼容容量：已展开弧按真实章节数，
// 骨架弧按 EstimatedChapters。新规划必须为每弧提供真实 chapters，因此业务层
// 展示和 Progress.TotalChapters 应统一使用 len(FlattenOutline(volumes))。
func EstimatedChapterCapacity(volumes []VolumeOutline) int {
	n := 0
	for _, v := range volumes {
		for _, a := range v.Arcs {
			if a.IsExpanded() {
				n += len(a.DetailedChapters())
			} else {
				n += a.EstimatedChapters
			}
		}
	}
	return n
}

// SkeletonArcCount 返回仍只有估算、没有真实章节的历史骨架弧数量。
func SkeletonArcCount(volumes []VolumeOutline) int {
	n := 0
	for _, v := range volumes {
		for _, a := range v.Arcs {
			if !a.IsExpanded() {
				n++
			}
		}
	}
	return n
}

// HasSkeletonArcs 报告分层大纲是否仍依赖历史的按需展开机制。
func HasSkeletonArcs(volumes []VolumeOutline) bool {
	return SkeletonArcCount(volumes) > 0
}

// FlattenOutline 将分层大纲展开为扁平章节列表，保持全局章节号连续。
func FlattenOutline(volumes []VolumeOutline) []OutlineEntry {
	var result []OutlineEntry
	ch := 1
	for _, v := range volumes {
		for _, a := range v.Arcs {
			for _, e := range a.DetailedChapters() {
				e.Chapter = ch
				e.VolumeID, e.VolumeTitle = v.Index, v.Title
				e.ArcID, e.ArcTitle = a.Index, a.Title
				result = append(result, e)
				ch++
			}
		}
	}
	return result
}

// WorldRule 世界观规则条目。
type WorldRule struct {
	Category string `json:"category"` // magic / technology / geography / society / other
	Rule     string `json:"rule"`     // 规则描述
	Boundary string `json:"boundary"` // 不可违反的边界
}
