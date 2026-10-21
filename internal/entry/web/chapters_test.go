package web

import (
	"encoding/json"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/store"
)

func TestStateShowsPlannedChaptersBeforeWriting(t *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "without-model"
		if configured {
			name = "with-model"
		}
		t.Run(name, func(t *testing.T) {
			s := newTestServer(t, configured)
			st := store.NewStore(s.dir)
			if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{
				{Title: "启程", Arcs: []domain.ArcOutline{{Title: "起步", Chapters: []domain.OutlineEntry{{Title: "灯火", CoreEvent: "收到来信", Hook: "有人敲门", Scenes: []string{"雨夜"}}}}}},
				{Title: "归来", Arcs: []domain.ArcOutline{{Title: "重逢", Chapters: []domain.OutlineEntry{{Title: "故人"}}}, {Title: "余波", EstimatedChapters: 20}}},
			}); err != nil {
				t.Fatal(err)
			}
			var state struct {
				Chapters []chapterWorkbenchItem `json:"chapters"`
				Snapshot host.UISnapshot        `json:"snapshot"`
			}
			response := request(s, "GET", "/api/state", "")
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &state) != nil {
				t.Fatalf("state: %d %s", response.Code, response.Body.String())
			}
			if len(state.Chapters) != 2 || len(state.Snapshot.Outline) != 2 {
				t.Fatalf("planned chapters missing before progress exists: %+v", state)
			}
			for i, chapter := range state.Chapters {
				if chapter.Number != i+1 || chapter.Source != "planned" || chapter.Status != "planned" || chapter.HasContent || chapter.VolumeID != i+1 || chapter.ArcID != 1 {
					t.Fatalf("planned chapter = %+v", chapter)
				}
			}
			if state.Chapters[0].CoreEvent != "收到来信" || state.Chapters[0].Hook != "有人敲门" || len(state.Chapters[0].Scenes) != 1 || state.Snapshot.Outline[1].VolumeTitle != "归来" {
				t.Fatalf("planned content and membership = %+v", state)
			}
			var planning struct {
				Outline []domain.OutlineEntry `json:"outline"`
			}
			response = request(s, "GET", "/api/planning", "")
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &planning) != nil || len(planning.Outline) != 2 || planning.Outline[1].VolumeID != 2 {
				t.Fatalf("planning membership: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestChapterWorkbenchMergesContentWithoutLosingPlanOrAcceptedTitle(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	for _, summary := range []domain.ChapterSummary{{Chapter: 1, Title: "终稿标题"}, {Chapter: 2, Title: "废弃旧稿标题"}} {
		if err := st.Summaries.SaveSummary(summary); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Drafts.SaveDraft(2, "新章节草稿"); err != nil {
		t.Fatal(err)
	}
	outline := []domain.OutlineEntry{
		{Chapter: 1, Title: "第一章计划", VolumeID: 1, VolumeTitle: "初见"},
		{Chapter: 2, Title: "第二章计划", VolumeID: 1, VolumeTitle: "初见"},
		{Chapter: 3, Title: "第三章计划", VolumeID: 2, VolumeTitle: "离别"},
	}
	progress := &domain.Progress{CompletedChapters: []int{4, 1}, InProgressChapter: 2, ChapterWordCounts: map[int]int{1: 100, 4: 300}}
	chapters := chapterWorkbenchItems(st, outline, progress)
	if len(chapters) != 4 || chapters[0].Title != "终稿标题" || chapters[0].Status != "completed" || chapters[0].Source != "final" || chapters[0].Words != 100 || !chapters[0].HasContent {
		t.Fatalf("accepted chapter missing: %+v", chapters)
	}
	if chapters[1].Title != "第二章计划" || chapters[1].Source != "draft" || !chapters[1].HasContent || chapters[1].Words != domain.WordCount("新章节草稿") {
		t.Fatalf("draft projection = %+v", chapters[1])
	}
	if chapters[2].Source != "planned" || chapters[2].HasContent || chapters[2].VolumeID != 2 || chapters[3].Number != 4 || chapters[3].VolumeID != 1 || chapters[3].Status != "completed" {
		t.Fatalf("planned or legacy chapter missing: %+v", chapters)
	}
	if err := st.Drafts.SaveDraft(1, "正在改写第一章"); err != nil {
		t.Fatal(err)
	}
	progress.InProgressChapter = 1
	chapters = chapterWorkbenchItems(st, outline, progress)
	if len(chapters) != 4 || chapters[0].Source != "final" || chapters[0].Title != "终稿标题" || chapters[0].Words != 100 || !chapters[0].HasDraft {
		t.Fatalf("rewrite draft must not hide or duplicate accepted chapter: %+v", chapters)
	}
}
