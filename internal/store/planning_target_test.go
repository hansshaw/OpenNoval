package store

import (
	"errors"
	"testing"

	"github.com/hansshaw/OpenNovel/internal/domain"
	"github.com/hansshaw/OpenNovel/internal/errs"
)

func TestUnauthorizedReviseOutlineCannotChangeChapterTarget(t *testing.T) {
	s := setupLayered(t, []domain.VolumeOutline{testOutlineVolume(1, "完整规划", 2)})
	if err := s.RunMeta.SetTargetChapters(3); err != nil {
		t.Fatal(err)
	}

	_, err := s.ReviseOutline(2, []domain.OutlineEntry{testOutlineEntry("只保留一章")})
	if !errors.Is(err, errs.ErrToolArgs) {
		t.Fatalf("expected target contract rejection, got %v", err)
	}
	meta, err := s.RunMeta.Load()
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.TargetChapters != 3 {
		t.Fatalf("unauthorized revision changed target: %+v", meta)
	}
}

func TestAppendVolumeCannotBypassTargetWithSkeletonArcs(t *testing.T) {
	s := setupLayered(t, []domain.VolumeOutline{{
		Title: "第一卷",
		Arcs: []domain.ArcOutline{
			{Title: "已展开", Goal: "开局", Chapters: []domain.OutlineEntry{testOutlineEntry("章一")}},
			{Title: "旧骨架", Goal: "承接", EstimatedChapters: 3},
		},
	}})
	if err := s.RunMeta.SetTargetChapters(3); err != nil {
		t.Fatal(err)
	}

	_, err := s.AppendVolume(domain.VolumeOutline{
		Title: "第二卷",
		Arcs: []domain.ArcOutline{{
			Title: "新弧", Goal: "续写",
			Chapters: []domain.OutlineEntry{testOutlineEntry("章二"), testOutlineEntry("章三")},
		}},
	})
	if !errors.Is(err, errs.ErrToolArgs) {
		t.Fatalf("expected append target rejection, got %v", err)
	}
	meta, err := s.RunMeta.Load()
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.TargetChapters != 3 {
		t.Fatalf("rejected append changed target: %+v", meta)
	}
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 1 || domain.SkeletonArcCount(volumes) != 1 {
		t.Fatalf("rejected append changed outline: %+v", volumes)
	}
}

func TestLegacyArcExpansionReservesOneChapterPerSkeleton(t *testing.T) {
	s := setupLayered(t, []domain.VolumeOutline{{
		Title: "第一卷",
		Arcs: []domain.ArcOutline{
			{Title: "已展开", Goal: "开局", Chapters: []domain.OutlineEntry{testOutlineEntry("章一")}},
			{Title: "骨架二", Goal: "承接", EstimatedChapters: 5},
			{Title: "骨架三", Goal: "收束", EstimatedChapters: 5},
		},
	}})
	if err := s.RunMeta.SetTargetChapters(6); err != nil {
		t.Fatal(err)
	}
	p, err := s.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.CompletedChapters = []int{1}
	if err := s.Progress.Save(p); err != nil {
		t.Fatal(err)
	}

	_, err = s.ExpandNextArc(domain.ArcExpansion{
		Title: "展开二", Goal: "承接",
		Chapters: []domain.OutlineEntry{testOutlineEntry("章二"), testOutlineEntry("章三")},
	})
	if err != nil {
		t.Fatalf("partial legacy expansion should retain target room: %v", err)
	}
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	if len(domain.FlattenOutline(volumes)) != 3 || domain.SkeletonArcCount(volumes) != 1 {
		t.Fatalf("unexpected partial expansion: %+v", volumes)
	}
	meta, err := s.RunMeta.Load()
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.TargetChapters != 6 {
		t.Fatalf("partial expansion changed target: %+v", meta)
	}
}

func TestFinalLegacyArcExpansionMustHitTargetExactly(t *testing.T) {
	t.Run("short", func(t *testing.T) {
		s := finalLegacyExpansionStore(t, 5)
		_, err := s.ExpandNextArc(domain.ArcExpansion{
			Title: "终弧", Goal: "收束",
			Chapters: []domain.OutlineEntry{testOutlineEntry("章二"), testOutlineEntry("章三"), testOutlineEntry("章四")},
		})
		if !errors.Is(err, errs.ErrToolArgs) {
			t.Fatalf("expected final target rejection, got %v", err)
		}
		volumes, err := s.Outline.LoadLayeredOutline()
		if err != nil {
			t.Fatal(err)
		}
		if domain.SkeletonArcCount(volumes) != 1 {
			t.Fatalf("rejected final expansion changed outline: %+v", volumes)
		}
	})

	t.Run("exact", func(t *testing.T) {
		s := finalLegacyExpansionStore(t, 5)
		_, err := s.ExpandNextArc(domain.ArcExpansion{
			Title: "终弧", Goal: "收束",
			Chapters: []domain.OutlineEntry{
				testOutlineEntry("章二"), testOutlineEntry("章三"),
				testOutlineEntry("章四"), testOutlineEntry("章五"),
			},
		})
		if err != nil {
			t.Fatalf("exact final expansion failed: %v", err)
		}
		volumes, err := s.Outline.LoadLayeredOutline()
		if err != nil {
			t.Fatal(err)
		}
		if len(domain.FlattenOutline(volumes)) != 5 || domain.SkeletonArcCount(volumes) != 0 {
			t.Fatalf("unexpected exact final expansion: %+v", volumes)
		}
		meta, err := s.RunMeta.Load()
		if err != nil {
			t.Fatal(err)
		}
		if meta == nil || meta.TargetChapters != 5 {
			t.Fatalf("exact final expansion changed target: %+v", meta)
		}
	})
}

func finalLegacyExpansionStore(t *testing.T, target int) *Store {
	t.Helper()
	s := setupLayered(t, []domain.VolumeOutline{{
		Title: "第一卷",
		Arcs: []domain.ArcOutline{
			{Title: "已展开", Goal: "开局", Chapters: []domain.OutlineEntry{testOutlineEntry("章一")}},
			{Title: "终骨架", Goal: "收束", EstimatedChapters: 4},
		},
	}})
	if err := s.RunMeta.SetTargetChapters(target); err != nil {
		t.Fatal(err)
	}
	p, err := s.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.CompletedChapters = []int{1}
	if err := s.Progress.Save(p); err != nil {
		t.Fatal(err)
	}
	return s
}
