package domain

import "testing"

func TestArcOutlineSkeletonCompatibility(t *testing.T) {
	tests := []struct {
		name      string
		arc       ArcOutline
		skeleton  bool
		chapterNo int
	}{
		{
			name:     "missing chapters",
			arc:      ArcOutline{EstimatedChapters: 12},
			skeleton: true,
		},
		{
			name: "single placeholder chapter",
			arc: ArcOutline{
				EstimatedChapters: 12,
				Chapters:          []OutlineEntry{{Title: "待展开"}},
			},
			skeleton: true,
		},
		{
			name: "legitimate single chapter arc",
			arc: ArcOutline{
				Chapters: []OutlineEntry{{Title: "唯一一章"}},
			},
			chapterNo: 1,
		},
		{
			name: "expanded multi chapter arc",
			arc: ArcOutline{
				EstimatedChapters: 12,
				Chapters:          []OutlineEntry{{Title: "第一章"}, {Title: "第二章"}},
			},
			chapterNo: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.arc.IsSkeleton(); got != test.skeleton {
				t.Fatalf("IsSkeleton() = %v, want %v", got, test.skeleton)
			}
			if got := test.arc.IsExpanded(); got == test.skeleton {
				t.Fatalf("IsExpanded() = %v, want %v", got, !test.skeleton)
			}
			chapters := test.arc.DetailedChapters()
			if test.chapterNo == 0 {
				if chapters != nil {
					t.Fatalf("DetailedChapters() = %+v, want nil", chapters)
				}
				return
			}
			if len(chapters) == 0 {
				t.Fatal("DetailedChapters() unexpectedly empty")
			}
		})
	}
}

func TestFlattenOutlineSkipsPlaceholderSkeleton(t *testing.T) {
	volumes := []VolumeOutline{
		{Index: 1, Title: "第一卷", Arcs: []ArcOutline{
			{Index: 1, Title: "真实弧", Chapters: []OutlineEntry{{Title: "第一章"}}},
			{Index: 2, Title: "历史骨架", EstimatedChapters: 12, Chapters: []OutlineEntry{{Title: "占位"}}},
		}},
		{Index: 2, Title: "第二卷", Arcs: []ArcOutline{
			{Index: 1, Title: "后续弧", Chapters: []OutlineEntry{{Title: "第二章"}}},
		}},
	}
	flat := FlattenOutline(volumes)
	if len(flat) != 2 {
		t.Fatalf("FlattenOutline() returned %d chapters, want 2: %+v", len(flat), flat)
	}
	if flat[0].Title != "第一章" || flat[1].Title != "第二章" || flat[1].Chapter != 2 {
		t.Fatalf("unexpected flattened chapters: %+v", flat)
	}
}
