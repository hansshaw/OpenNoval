// Modified by hansshaw for AI Novel Studio, 2026-10-24.
// Original third-party attribution and license terms are retained in NOTICE.

package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hansshaw/AInoval-web-ui/internal/domain"
)

func setupLayered(t *testing.T, volumes []domain.VolumeOutline) *Store {
	t.Helper()
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(0); err != nil {
		t.Fatalf("InitProgress: %v", err)
	}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}
	if err := s.Progress.SetLayered(true); err != nil {
		t.Fatalf("SetLayered: %v", err)
	}
	return s
}

func TestSaveLayeredOutlineRebuildsFlatProjection(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "陈旧标题"}}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}

	volumes := []domain.VolumeOutline{{
		Index: 1, Title: "第一卷", Theme: "启程",
		Arcs: []domain.ArcOutline{{
			Index: 1, Title: "第一弧", Goal: "进入新世界",
			Chapters: []domain.OutlineEntry{
				{Chapter: 99, Title: "新一", CoreEvent: "启程", Hook: "发现"},
				{Chapter: 100, Title: "新二", CoreEvent: "深入", Hook: "危机"},
			},
		}},
	}}
	if err := s.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatalf("SaveLayeredOutline: %v", err)
	}

	flat, err := s.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("LoadOutline: %v", err)
	}
	if len(flat) != 2 || flat[0].Chapter != 1 || flat[0].Title != "新一" || flat[1].Chapter != 2 || flat[1].Title != "新二" {
		t.Fatalf("flat projection = %+v", flat)
	}
	markdown, err := os.ReadFile(filepath.Join(s.Dir(), "outline.md"))
	if err != nil {
		t.Fatalf("read outline.md: %v", err)
	}
	if !strings.Contains(string(markdown), "新一") || strings.Contains(string(markdown), "陈旧标题") {
		t.Fatalf("outline.md 未同步重建:\n%s", markdown)
	}
}

func TestReplaceFlatOutlineAllowsFullOverrideAfterWriting(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Progress.Init(3); err != nil {
		t.Fatalf("Progress.Init: %v", err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{
		testOutlineEntry("旧一"), testOutlineEntry("旧二"), testOutlineEntry("旧三"),
	}); err != nil {
		t.Fatalf("SaveOutline: %v", err)
	}
	p, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	p.Phase = domain.PhaseWriting
	p.CurrentChapter = 2
	p.InProgressChapter = 2
	p.CompletedChapters = []int{1}
	p.CompletedScenes = []int{1}
	p.Flow = domain.FlowWriting
	if err := s.Progress.Save(p); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, "仍在当前目录范围内的正文草稿"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	total, err := s.ReplaceFlatOutline([]domain.OutlineEntry{
		testOutlineEntry("新一"), testOutlineEntry("新二"),
	})
	if err != nil {
		t.Fatalf("ReplaceFlatOutline: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	outline, err := s.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("LoadOutline: %v", err)
	}
	if len(outline) != 2 || outline[0].Title != "新一" || outline[1].Title != "新二" {
		t.Fatalf("outline not fully replaced: %+v", outline)
	}
	p, err = s.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	if p.Phase != domain.PhaseWriting || p.TotalChapters != 2 {
		t.Fatalf("writing progress not preserved: %+v", p)
	}
	if p.CurrentChapter != 2 || p.InProgressChapter != 2 || len(p.CompletedScenes) != 0 {
		t.Fatalf("in-range draft state not preserved correctly: %+v", p)
	}
	if p.Layered || p.CurrentVolume != 0 || p.CurrentArc != 0 {
		t.Fatalf("flat outline must clear layered position: %+v", p)
	}
}

func TestReplaceLayeredOutlinePreservesInRangeDraft(t *testing.T) {
	s := setupLayered(t, []domain.VolumeOutline{testOutlineVolume(1, "旧卷", 3)})
	p, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	p.Phase = domain.PhaseWriting
	p.CurrentChapter = 2
	p.InProgressChapter = 2
	p.CompletedChapters = []int{1}
	p.CompletedScenes = []int{1, 2}
	p.Flow = domain.FlowRewriting
	p.PendingRewrites = []int{1}
	p.RewriteReason = "保留中的返工事实"
	if err := s.Progress.Save(p); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	if err := s.Drafts.SaveDraft(2, "范围内草稿"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	volumes := []domain.VolumeOutline{
		{Title: "新第一卷", Arcs: []domain.ArcOutline{
			{Title: "第一弧", Goal: "开局", Chapters: []domain.OutlineEntry{testOutlineEntry("新一")}},
		}},
		{Title: "新第二卷", Arcs: []domain.ArcOutline{
			{Title: "第二弧", Goal: "承接", Chapters: []domain.OutlineEntry{testOutlineEntry("新二")}},
		}},
	}
	total, err := s.ReplaceLayeredOutline(volumes)
	if err != nil {
		t.Fatalf("ReplaceLayeredOutline: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	p, err = s.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	if p.Phase != domain.PhaseWriting || p.Flow != domain.FlowRewriting {
		t.Fatalf("phase/flow not preserved: %+v", p)
	}
	if len(p.PendingRewrites) != 1 || p.PendingRewrites[0] != 1 || p.RewriteReason != "保留中的返工事实" {
		t.Fatalf("rewrite facts not preserved: %+v", p)
	}
	if p.CurrentChapter != 2 || p.InProgressChapter != 2 {
		t.Fatalf("in-range draft position = current %d in-progress %d, want both 2", p.CurrentChapter, p.InProgressChapter)
	}
	if len(p.CompletedScenes) != 0 {
		t.Fatalf("CompletedScenes must be cleared after outline replacement: %+v", p.CompletedScenes)
	}
	if !p.Layered || p.CurrentVolume != 2 || p.CurrentArc != 1 || p.TotalChapters != 2 {
		t.Fatalf("layered position not rebuilt: %+v", p)
	}
	draft, err := s.Drafts.LoadDraft(2)
	if err != nil || draft != "范围内草稿" {
		t.Fatalf("draft not preserved: %q err=%v", draft, err)
	}
}

func TestReplaceLayeredOutlineRejectsProtectedState(t *testing.T) {
	t.Run("completed chapter would be truncated", func(t *testing.T) {
		s := setupLayered(t, []domain.VolumeOutline{testOutlineVolume(1, "旧卷", 3)})
		p, _ := s.Progress.Load()
		p.Phase = domain.PhaseWriting
		p.CompletedChapters = []int{1, 2}
		p.CurrentChapter = 3
		if err := s.Progress.Save(p); err != nil {
			t.Fatalf("Save progress: %v", err)
		}
		if _, err := s.ReplaceLayeredOutline([]domain.VolumeOutline{testOutlineVolume(1, "新卷", 1)}); err == nil {
			t.Fatal("expected completed chapter truncation to fail")
		}
	})

	t.Run("non-empty draft chapter would be truncated", func(t *testing.T) {
		s := setupLayered(t, []domain.VolumeOutline{testOutlineVolume(1, "旧卷", 3)})
		p, _ := s.Progress.Load()
		p.Phase = domain.PhaseWriting
		p.CurrentChapter = 3
		p.InProgressChapter = 3
		if err := s.Progress.Save(p); err != nil {
			t.Fatalf("Save progress: %v", err)
		}
		if err := s.Drafts.SaveDraft(3, "不能被截掉的草稿"); err != nil {
			t.Fatalf("SaveDraft: %v", err)
		}
		if _, err := s.ReplaceLayeredOutline([]domain.VolumeOutline{testOutlineVolume(1, "新卷", 2)}); err == nil {
			t.Fatal("expected non-empty draft truncation to fail")
		}
	})

	t.Run("pending commit", func(t *testing.T) {
		s := setupLayered(t, []domain.VolumeOutline{testOutlineVolume(1, "旧卷", 3)})
		p, _ := s.Progress.Load()
		p.Phase = domain.PhaseWriting
		p.CurrentChapter = 2
		if err := s.Progress.Save(p); err != nil {
			t.Fatalf("Save progress: %v", err)
		}
		if err := s.Signals.SavePendingCommit(domain.PendingCommit{Chapter: 2, Stage: domain.CommitStageStarted}); err != nil {
			t.Fatalf("SavePendingCommit: %v", err)
		}
		if _, err := s.ReplaceLayeredOutline([]domain.VolumeOutline{testOutlineVolume(1, "新卷", 3)}); err == nil {
			t.Fatal("expected pending commit replacement to fail")
		}
	})
}

func TestReplaceLayeredOutlineClearsTruncatedEmptyDraftState(t *testing.T) {
	s := setupLayered(t, []domain.VolumeOutline{testOutlineVolume(1, "旧卷", 3)})
	p, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	p.Phase = domain.PhaseWriting
	p.CurrentChapter = 3
	p.InProgressChapter = 3
	p.CompletedChapters = []int{1}
	if err := s.Progress.Save(p); err != nil {
		t.Fatalf("Save progress: %v", err)
	}
	if err := s.Drafts.SaveDraft(3, " \n\t "); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	if _, err := s.ReplaceLayeredOutline([]domain.VolumeOutline{testOutlineVolume(1, "新卷", 2)}); err != nil {
		t.Fatalf("ReplaceLayeredOutline: %v", err)
	}
	p, err = s.Progress.Load()
	if err != nil {
		t.Fatalf("Load progress: %v", err)
	}
	if p.InProgressChapter != 0 {
		t.Fatalf("empty truncated draft must clear InProgressChapter: %+v", p)
	}
	if p.CurrentChapter != 2 || p.TotalChapters != 2 {
		t.Fatalf("next chapter should clamp to new outline end: %+v", p)
	}
}

func TestReplaceLayeredOutlineKeepsEveryArcPlanned(t *testing.T) {
	s := setupLayered(t, []domain.VolumeOutline{testOutlineVolume(1, "旧卷", 2)})
	p, _ := s.Progress.Load()
	p.Phase = domain.PhaseWriting
	p.CompletedChapters = []int{1, 2, 3}
	p.CurrentChapter = 4
	if err := s.Progress.Save(p); err != nil {
		t.Fatalf("Save progress: %v", err)
	}

	volumes := []domain.VolumeOutline{
		{Title: "第一卷", Final: true, Arcs: []domain.ArcOutline{
			{Title: "一弧", Goal: "目标一", Chapters: []domain.OutlineEntry{testOutlineEntry("章一"), testOutlineEntry("章二")}},
			{Title: "二弧", Goal: "目标二", Chapters: []domain.OutlineEntry{testOutlineEntry("章三"), testOutlineEntry("章四")}},
		}},
		{Title: "第二卷", Arcs: []domain.ArcOutline{
			{Title: "三弧", Goal: "目标三", Chapters: []domain.OutlineEntry{testOutlineEntry("章五"), testOutlineEntry("章六")}},
			{Title: "四弧", Goal: "目标四", Chapters: []domain.OutlineEntry{testOutlineEntry("章七"), testOutlineEntry("章八")}},
		}},
	}
	total, err := s.ReplaceLayeredOutline(volumes)
	if err != nil {
		t.Fatalf("ReplaceLayeredOutline: %v", err)
	}
	if total != 8 {
		t.Fatalf("total = %d, want 8", total)
	}
	saved, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("LoadLayeredOutline: %v", err)
	}
	chapters := 0
	for _, volume := range saved {
		for _, arc := range volume.Arcs {
			if len(arc.Chapters) == 0 {
				t.Fatalf("arc %q has no real chapters", arc.Title)
			}
			chapters += len(arc.Chapters)
		}
	}
	if chapters != 8 {
		t.Fatalf("saved chapters = %d, want 8", chapters)
	}
	if saved[0].Final || !saved[1].Final || domain.FinaleVolume(saved) != 2 {
		t.Fatalf("完整规划应只把最后一卷规范为收官卷: %+v", saved)
	}
	p, _ = s.Progress.Load()
	if p.CurrentChapter != 4 || p.CurrentVolume != 1 || p.CurrentArc != 2 {
		t.Fatalf("current layered position = %+v, want chapter 4 in volume 1 arc 2", p)
	}
}

func testOutlineEntry(title string) domain.OutlineEntry {
	return domain.OutlineEntry{Title: title, CoreEvent: title + "事件", Hook: title + "钩子"}
}

func testOutlineVolume(index int, title string, chapterCount int) domain.VolumeOutline {
	chapters := make([]domain.OutlineEntry, 0, chapterCount)
	for i := 1; i <= chapterCount; i++ {
		chapters = append(chapters, testOutlineEntry(title+"章"+string(rune('0'+i))))
	}
	return domain.VolumeOutline{
		Index: index,
		Title: title,
		Arcs: []domain.ArcOutline{{
			Index: 1, Title: title + "弧", Goal: title + "目标", Chapters: chapters,
		}},
	}
}

func TestCheckArcBoundaryNeedsNewVolume(t *testing.T) {
	// 只有 1 卷 1 弧 1 章，且非 Final → 应触发 NeedsNewVolume
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "第一卷", Theme: "起步",
		Arcs: []domain.ArcOutline{{
			Index: 1, Title: "首弧", Goal: "目标",
			Chapters: []domain.OutlineEntry{{Title: "第一章", CoreEvent: "开局", Hook: "继续"}},
		}},
	}})

	b, err := s.Outline.CheckArcBoundary(1) // 第 1 章 = 弧/卷最后一章
	if err != nil {
		t.Fatalf("CheckArcBoundary: %v", err)
	}
	if b == nil {
		t.Fatal("expected boundary, got nil")
	}
	if !b.IsArcEnd || !b.IsVolumeEnd {
		t.Fatalf("expected arc+volume end, got arc=%v vol=%v", b.IsArcEnd, b.IsVolumeEnd)
	}
	if !b.NeedsNewVolume {
		t.Fatal("expected NeedsNewVolume=true")
	}
	if b.NextVolume != 0 || b.NextArc != 0 {
		t.Fatalf("expected no next, got vol=%d arc=%d", b.NextVolume, b.NextArc)
	}
}

func TestCheckArcBoundaryLastVolumeRequiresDecision(t *testing.T) {
	// 单卷最后一章 → 触发 NeedsNewVolume，让 Router 让架构师二选一：
	// append_volume 续写 / complete_book 收尾。
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "唯一卷", Theme: "主题",
		Arcs: []domain.ArcOutline{{
			Index: 1, Title: "唯一弧", Goal: "收束",
			Chapters: []domain.OutlineEntry{{Title: "终章", CoreEvent: "结局", Hook: "无"}},
		}},
	}})

	b, err := s.Outline.CheckArcBoundary(1)
	if err != nil {
		t.Fatalf("CheckArcBoundary: %v", err)
	}
	if !b.NeedsNewVolume {
		t.Fatal("expected NeedsNewVolume=true at last expanded chapter")
	}
	if b.HasNextArc() {
		t.Fatal("expected no next arc")
	}
}

func TestCheckArcBoundaryNextArcInSameVolume(t *testing.T) {
	// 2 弧：第 1 弧结束应指向第 2 弧，不触发 NeedsNewVolume
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "第一卷", Theme: "起步",
		Arcs: []domain.ArcOutline{
			{Index: 1, Title: "首弧", Goal: "目标", Chapters: []domain.OutlineEntry{{Title: "章一", CoreEvent: "事件", Hook: "钩子"}}},
			{Index: 2, Title: "次弧", Goal: "目标2", EstimatedChapters: 10},
		},
	}})

	b, err := s.Outline.CheckArcBoundary(1)
	if err != nil {
		t.Fatalf("CheckArcBoundary: %v", err)
	}
	if !b.IsArcEnd {
		t.Fatal("expected arc end")
	}
	if b.IsVolumeEnd {
		t.Fatal("expected not volume end (second arc exists)")
	}
	if b.NeedsNewVolume {
		t.Fatal("expected NeedsNewVolume=false")
	}
	if b.NextVolume != 1 || b.NextArc != 2 {
		t.Fatalf("expected next vol=1 arc=2, got vol=%d arc=%d", b.NextVolume, b.NextArc)
	}
	if !b.NeedsExpansion {
		t.Fatal("expected NeedsExpansion=true for skeleton arc")
	}
}

func TestCheckArcBoundaryReportsExactArcSpan(t *testing.T) {
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1,
		Arcs: []domain.ArcOutline{
			{Index: 1, Chapters: []domain.OutlineEntry{{Title: "一"}, {Title: "二"}}},
			{Index: 2, Chapters: []domain.OutlineEntry{{Title: "三"}, {Title: "四"}, {Title: "五"}}},
		},
	}})

	b, err := s.Outline.CheckArcBoundary(5)
	if err != nil {
		t.Fatalf("CheckArcBoundary: %v", err)
	}
	if b == nil || !b.IsArcEnd || b.StartChapter != 3 || b.EndChapter != 5 {
		t.Fatalf("unexpected arc span: %+v", b)
	}
}

func TestExpandArcCalibratesUnwrittenPlan(t *testing.T) {
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "第一卷", Theme: "起步",
		Arcs: []domain.ArcOutline{
			{Index: 1, Title: "旧弧", Goal: "造成计划外的决裂", Chapters: []domain.OutlineEntry{{Title: "决裂", CoreEvent: "同伴离队", Hook: "去向不明"}}},
			{Index: 2, Title: "原骨架", Goal: "按原计划同行", EstimatedChapters: 8, Chapters: []domain.OutlineEntry{{Title: "待展开", CoreEvent: "占位", Hook: "待定"}}},
		},
	}})

	expansion := domain.ArcExpansion{
		Title: "分途追索",
		Goal:  "承认决裂已经发生，让两条行动线分别逼近同一真相",
		Chapters: []domain.OutlineEntry{
			{Title: "两张地图", CoreEvent: "两队从不同线索出发", Hook: "线索指向同一地点"},
			{Title: "隔墙回声", CoreEvent: "双方隔空影响彼此选择", Hook: "重逢代价浮现"},
		},
	}
	p, err := s.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.CompletedChapters = []int{1}
	if err := s.Progress.Save(p); err != nil {
		t.Fatal(err)
	}
	position, err := s.ExpandNextArc(expansion)
	if err != nil {
		t.Fatalf("ExpandNextArc: %v", err)
	}
	if position.Volume != 1 || position.Arc != 2 {
		t.Fatalf("unexpected position: %+v", position)
	}

	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatalf("LoadLayeredOutline: %v", err)
	}
	got := volumes[0].Arcs[1]
	if got.Title != expansion.Title || got.Goal != expansion.Goal {
		t.Fatalf("expected calibrated title/goal, got title=%q goal=%q", got.Title, got.Goal)
	}
	if got.EstimatedChapters != 0 || len(got.Chapters) != 2 {
		t.Fatalf("expected expanded arc, got estimated=%d chapters=%d", got.EstimatedChapters, len(got.Chapters))
	}
	flat, err := s.Outline.LoadOutline()
	if err != nil {
		t.Fatalf("LoadOutline: %v", err)
	}
	if len(flat) != 3 || flat[1].Chapter != 2 || flat[2].Chapter != 3 {
		t.Fatalf("expected continuous flattened outline, got %+v", flat)
	}
	progress, err := s.Progress.Load()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if progress.TotalChapters != 3 {
		t.Fatalf("expected total chapters 3, got %d", progress.TotalChapters)
	}

	if _, err := s.ExpandNextArc(expansion); err != nil {
		t.Fatalf("same expansion must be idempotent: %v", err)
	}
	// 模拟上次只写完 layered JSON、派生 flat outline 与 Progress 尚未补齐。
	if err := os.Remove(filepath.Join(s.Dir(), "outline.json")); err != nil {
		t.Fatalf("remove flat outline: %v", err)
	}
	if err := s.Progress.SetTotalChapters(1); err != nil {
		t.Fatalf("set stale total: %v", err)
	}
	if _, err := s.ExpandNextArc(expansion); err != nil {
		t.Fatalf("idempotent retry should repair derived state: %v", err)
	}
	flat, err = s.Outline.LoadOutline()
	if err != nil || len(flat) != 3 {
		t.Fatalf("flat outline was not repaired: len=%d err=%v", len(flat), err)
	}
	progress, err = s.Progress.Load()
	if err != nil || progress.TotalChapters != 3 {
		t.Fatalf("progress total was not repaired: progress=%+v err=%v", progress, err)
	}
	changed := expansion
	changed.Goal = "事后改写已展开弧"
	if _, err := s.ExpandNextArc(changed); err == nil {
		t.Fatal("expected a different expansion to reject overwriting the expanded arc")
	}
}

func TestExpandNextArcRejectsCompletePlan(t *testing.T) {
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "第一卷", Theme: "完整规划",
		Arcs: []domain.ArcOutline{
			{Index: 1, Title: "第一弧", Goal: "目标一", Chapters: []domain.OutlineEntry{testOutlineEntry("章一")}},
			{Index: 2, Title: "第二弧", Goal: "目标二", Chapters: []domain.OutlineEntry{testOutlineEntry("章二")}},
		},
	}})
	p, err := s.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.CompletedChapters = []int{1}
	if err := s.Progress.Save(p); err != nil {
		t.Fatal(err)
	}

	_, err = s.ExpandNextArc(domain.ArcExpansion{
		Title:    "改写后的第二弧",
		Goal:     "事后改写完整规划",
		Chapters: []domain.OutlineEntry{testOutlineEntry("章二")},
	})
	if err == nil || !strings.Contains(err.Error(), "完整规划") {
		t.Fatalf("完整规划必须拒绝 expand_next_arc, err=%v", err)
	}
}

func TestReplaceLayeredOutlineRejectsLegacyPlaceholderArc(t *testing.T) {
	s := setupLayered(t, []domain.VolumeOutline{testOutlineVolume(1, "旧卷", 1)})
	_, err := s.ReplaceLayeredOutline([]domain.VolumeOutline{{
		Title: "新卷",
		Arcs: []domain.ArcOutline{{
			Title:             "待展开弧",
			Goal:              "后续",
			EstimatedChapters: 12,
			Chapters:          []domain.OutlineEntry{testOutlineEntry("占位章")},
		}},
	}})
	if err == nil || !strings.Contains(err.Error(), "必须包含真实章节") {
		t.Fatalf("legacy placeholder arc must be rejected, err=%v", err)
	}
}

func TestAppendVolumeValidation(t *testing.T) {
	s := setupLayered(t, []domain.VolumeOutline{{
		Index: 1, Title: "第一卷", Theme: "起步",
		Arcs: []domain.ArcOutline{{
			Index: 1, Title: "首弧", Goal: "目标",
			Chapters: []domain.OutlineEntry{{Title: "章", CoreEvent: "事件", Hook: "钩子"}},
		}},
	}})

	validVol := domain.VolumeOutline{
		Index: 2, Title: "第二卷", Theme: "升级",
		Arcs: []domain.ArcOutline{{
			Index: 1, Title: "弧一", Goal: "目标",
			Chapters: []domain.OutlineEntry{{Title: "新章", CoreEvent: "推进", Hook: "钩子"}},
		}},
	}

	// 正常追加应成功
	if _, err := s.AppendVolume(validVol); err != nil {
		t.Fatalf("AppendVolume valid: %v", err)
	}

	// 卷弧序号由系统生成，不信任调用方提供的 Index。
	saved, err := s.AppendVolume(domain.VolumeOutline{
		Index: 99, Title: "第三卷", Theme: "x",
		Arcs: []domain.ArcOutline{{Index: 99, Title: "弧", Goal: "g", Chapters: []domain.OutlineEntry{{Title: "ch", CoreEvent: "e", Hook: "h"}}}},
	})
	if err != nil {
		t.Fatalf("AppendVolume numbered: %v", err)
	}
	if saved.Index != 3 || saved.Arcs[0].Index != 1 {
		t.Fatalf("unexpected system numbering: %+v", saved)
	}

	// 无弧 → 失败
	if _, err := s.AppendVolume(domain.VolumeOutline{Title: "空", Theme: "x"}); err == nil {
		t.Fatal("expected error for volume with no arcs")
	}

	// 首弧无章节 → 失败
	if _, err := s.AppendVolume(domain.VolumeOutline{
		Title: "骨架", Theme: "x",
		Arcs: []domain.ArcOutline{{Title: "弧", Goal: "g", EstimatedChapters: 10}},
	}); err == nil {
		t.Fatal("expected error for first arc without chapters")
	}
}

func TestAppendVolumeNormalizesFinaleMarker(t *testing.T) {
	tests := []struct {
		name          string
		appendedFinal bool
		replay        bool
		wantFinal     []bool
		wantFinale    int
	}{
		{
			name:          "ordinary volume releases prior finale",
			appendedFinal: false,
			wantFinal:     []bool{false, false},
			wantFinale:    0,
		},
		{
			name:          "finale volume becomes the only finale",
			appendedFinal: true,
			wantFinal:     []bool{false, true},
			wantFinale:    2,
		},
		{
			name:          "idempotent finale replay keeps one finale",
			appendedFinal: true,
			replay:        true,
			wantFinal:     []bool{false, true},
			wantFinale:    2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := setupLayered(t, []domain.VolumeOutline{{
				Index: 1, Title: "旧终卷", Theme: "收束", Final: true,
				Arcs: []domain.ArcOutline{{
					Index: 1, Title: "旧终弧", Goal: "完成旧终局",
					Chapters: []domain.OutlineEntry{testOutlineEntry("旧终章")},
				}},
			}})
			next := domain.VolumeOutline{
				Title: "新卷", Theme: "继续", Final: tt.appendedFinal,
				Arcs: []domain.ArcOutline{{
					Index: 9, Title: "新弧", Goal: "推进新阶段",
					Chapters: []domain.OutlineEntry{testOutlineEntry("新章")},
				}},
			}
			if _, err := s.AppendVolume(next); err != nil {
				t.Fatalf("AppendVolume: %v", err)
			}
			if tt.replay {
				if _, err := s.AppendVolume(next); err != nil {
					t.Fatalf("replay AppendVolume: %v", err)
				}
			}

			volumes, err := s.Outline.LoadLayeredOutline()
			if err != nil {
				t.Fatalf("LoadLayeredOutline: %v", err)
			}
			if len(volumes) != len(tt.wantFinal) {
				t.Fatalf("volumes = %d, want %d", len(volumes), len(tt.wantFinal))
			}
			for i, want := range tt.wantFinal {
				if volumes[i].Final != want {
					t.Fatalf("volume %d Final = %v, want %v: %+v", i+1, volumes[i].Final, want, volumes)
				}
			}
			if got := domain.FinaleVolume(volumes); got != tt.wantFinale {
				t.Fatalf("FinaleVolume = %d, want %d", got, tt.wantFinale)
			}
		})
	}
}

// 注：原先用 Final 卷拒绝 append 的语义已下沉到 save_foundation 层（Phase=Complete 拒绝），
// 见 save_foundation_test.go::TestSaveFoundationAppendVolumeRejectsAfterComplete。
// store 层只保留内容结构校验，机械序号由自身生成。

func TestSaveAndLoadCompass(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// 空 direction 应失败
	if err := s.Outline.SaveCompass(domain.StoryCompass{EstimatedScale: "3 卷"}); err == nil {
		t.Fatal("expected error for empty ending_direction")
	}

	// 正常保存
	compass := domain.StoryCompass{
		EndingDirection: "主角面对最终抉择",
		OpenThreads:     []string{"线索A", "关系B"},
		EstimatedScale:  "预计 4-6 卷",
		LastUpdated:     12,
	}
	if err := s.Outline.SaveCompass(compass); err != nil {
		t.Fatalf("SaveCompass: %v", err)
	}

	loaded, err := s.Outline.LoadCompass()
	if err != nil {
		t.Fatalf("LoadCompass: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected compass, got nil")
	}
	if loaded.EndingDirection != "主角面对最终抉择" {
		t.Fatalf("expected direction %q, got %q", "主角面对最终抉择", loaded.EndingDirection)
	}
	if len(loaded.OpenThreads) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(loaded.OpenThreads))
	}
}

// TestOutlineFeedbackPool 反馈池闭环:commit 落盘 → 跨重启可读 → 结构操作消费清空。
func TestOutlineFeedbackPool(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Outline.AppendOutlineFeedback(ChapterFeedback{Chapter: 3, Deviation: "支线膨胀", Suggestion: "下一弧收线"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Outline.AppendOutlineFeedback(ChapterFeedback{Chapter: 3, Deviation: "支线膨胀", Suggestion: "下一弧收线"}); err != nil {
		t.Fatalf("append duplicate: %v", err)
	}
	if err := s.Outline.AppendOutlineFeedback(ChapterFeedback{Chapter: 4, Suggestion: "反派提前登场"}); err != nil {
		t.Fatalf("append2: %v", err)
	}

	// 跨重启(新 Store 实例)可读——不是内存态
	s2 := NewStore(dir)
	fbs, err := s2.Outline.LoadPendingOutlineFeedback()
	if err != nil {
		t.Fatalf("load feedback: %v", err)
	}
	if len(fbs) != 2 || fbs[0].Chapter != 3 || fbs[1].Suggestion != "反派提前登场" {
		t.Fatalf("跨重启读取失败: %+v", fbs)
	}
	for _, fb := range fbs {
		if fb.At == "" {
			t.Fatal("At 应自动补齐")
		}
	}

	if err := s2.Outline.ClearOutlineFeedback(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if left, err := s2.Outline.LoadPendingOutlineFeedback(); err != nil || len(left) != 0 {
		t.Fatalf("消费后应为空: %+v", left)
	}
	// 幂等清空
	if err := s2.Outline.ClearOutlineFeedback(); err != nil {
		t.Fatalf("clear idempotent: %v", err)
	}
}

func TestOutlineFeedbackCorruptionIsNotSilentlyConsumed(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	path := filepath.Join(dir, outlineFeedbackFile)
	if err := os.WriteFile(path, []byte("{\n"), 0o644); err != nil {
		t.Fatalf("write corrupt feedback: %v", err)
	}
	if _, err := s.Outline.LoadPendingOutlineFeedback(); err == nil {
		t.Fatal("corrupt feedback must return a read error")
	}
	if err := s.Outline.ClearOutlineFeedback(); err == nil {
		t.Fatal("corrupt feedback must not be cleared as if it had been consumed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("corrupt feedback should remain for diagnosis: %v", err)
	}
}
