package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hansshaw/OpenNovel/internal/domain"
)

func workbenchText(value string) *string { return &value }

func workbenchPlanFixture(t *testing.T) *Store {
	t.Helper()
	s := setupLayered(t, []domain.VolumeOutline{
		{Index: 1, Title: "启程", Theme: "寻找旧信", Arcs: []domain.ArcOutline{{Index: 1, Title: "来信", Goal: "出发", Chapters: []domain.OutlineEntry{
			{Title: "旧一", CoreEvent: "收到信", Hook: "敲门", Scenes: []string{"雨夜"}},
			{Title: "旧二", CoreEvent: "离城", Hook: "上车"},
		}}}},
		{Index: 2, Title: "归来", Theme: "解开旧事", Final: true, Arcs: []domain.ArcOutline{
			{Index: 1, Title: "旧友", Goal: "重逢", Chapters: []domain.OutlineEntry{{Title: "旧三", CoreEvent: "见旧友", Hook: "身份"}}},
			{Index: 2, Title: "历史骨架", Goal: "待展开", EstimatedChapters: 3},
		}},
	})
	if err := s.Outline.SavePremise("第一阶段原始原则保持不变"); err != nil {
		t.Fatal(err)
	}
	if err := s.RunMeta.Save(domain.RunMeta{
		StartPrompt: "原始创作需求", TargetChapters: 6, AdvanceMode: domain.ChapterAdvanceAuto,
		Workbench: &domain.WorkbenchControl{BatchSize: 2, BatchEnd: 2, AcceptedChapter: 1, ApprovedFingerprint: "approved-plan", ApprovedFoundationFingerprint: "approved-foundation"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Summaries.SaveSummary(domain.ChapterSummary{Chapter: 1, Title: "旧一", Summary: "原摘要", Characters: []string{"林"}, KeyEvents: []string{"得到线索"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChapterRecords.Accept(1, domain.ChapterOriginGenerated, "原有正文", domain.ChapterFacts{Title: "旧一", Summary: "原摘要", KeyEvents: []string{"得到线索"}}, domain.StyleDelta{Prose: []string{"短句"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveFinalChapter(1, "原有正文"); err != nil {
		t.Fatal(err)
	}
	if err := s.Drafts.SaveDraft(1, "保留未完成草稿"); err != nil {
		t.Fatal(err)
	}
	for _, chapter := range []int{1, 2, 3} {
		if err := s.Drafts.SaveChapterPlan(domain.ChapterPlan{Chapter: chapter, Title: "旧构思", Goal: "旧目标"}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func workbenchFingerprint(t *testing.T, s *Store) string {
	t.Helper()
	fingerprint, err := s.PlanningFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func TestWorkbenchPlanEditUsesLayeredSourceAndPreservesCompletedProse(t *testing.T) {
	s := workbenchPlanFixture(t)
	p, _ := s.Progress.Load()
	p.Phase = domain.PhaseComplete
	p.CompletedChapters = []int{1}
	if err := s.Progress.Save(p); err != nil {
		t.Fatal(err)
	}
	progressBefore, _ := os.ReadFile(filepath.Join(s.Dir(), "meta/progress.json"))
	recordBefore, _ := s.ChapterRecords.Load(1)
	if title, err := s.Summaries.LoadSummaryTitle(1); err != nil || title != "旧一" {
		t.Fatalf("seed title cache: %q %v", title, err)
	}
	fingerprint := workbenchFingerprint(t, s)
	scenes := []string{"车站", "旧信封"}
	edit := WorkbenchChapterPlanEdit{Title: workbenchText(" 新标题 "), VolumeTitle: workbenchText("寻找"), CoreEvent: workbenchText("在车站发现旧信"), Hook: workbenchText("列车将至"), Scenes: &scenes}
	updated, err := s.UpdateWorkbenchChapterPlan(1, edit, fingerprint)
	if err != nil || updated == fingerprint || updated != workbenchFingerprint(t, s) {
		t.Fatalf("edit fingerprint: %s %v", updated, err)
	}
	volumes, err := s.Outline.LoadLayeredOutline()
	if err != nil || len(volumes) != 2 || volumes[0].Title != "寻找" || volumes[0].Theme != "寻找旧信" || !volumes[1].Final || !volumes[1].Arcs[1].IsSkeleton() {
		t.Fatalf("layered structure changed: %+v %v", volumes, err)
	}
	entry, err := s.Outline.GetChapterOutline(1)
	flat, flatErr := s.Outline.LoadOutline()
	if err != nil || flatErr != nil || entry == nil || len(flat) != 3 || !reflect.DeepEqual(*entry, flat[0]) {
		t.Fatalf("authoritative and derived plan mismatch: %+v %+v %v %v", entry, flat, err, flatErr)
	}
	if entry.Title != "新标题" || entry.CoreEvent != "在车站发现旧信" || entry.Hook != "列车将至" || !reflect.DeepEqual(entry.Scenes, scenes) || entry.ArcTitle != "来信" || flat[1].VolumeTitle != "寻找" || flat[2].VolumeTitle != "归来" {
		t.Fatalf("saved plan fields: %+v", flat)
	}
	for _, rel := range []string{"outline.md", "layered_outline.md"} {
		data, _ := os.ReadFile(filepath.Join(s.Dir(), rel))
		if !strings.Contains(string(data), "新标题") || !strings.Contains(string(data), "寻找") {
			t.Fatalf("derived Markdown stale: %s", data)
		}
	}
	if title, err := s.Summaries.LoadSummaryTitle(1); err != nil || title != "新标题" {
		t.Fatalf("title cache was stale: %q %v", title, err)
	}
	summary, _ := s.Summaries.LoadSummary(1)
	if summary.Summary != "原摘要" || !reflect.DeepEqual(summary.Characters, []string{"林"}) {
		t.Fatalf("summary facts changed: %+v", summary)
	}
	record, err := s.ChapterRecords.Load(1)
	if err != nil || record.Facts.Title != "新标题" || record.Revision != recordBefore.Revision+1 || record.Content != recordBefore.Content || record.ContentSHA256 != recordBefore.ContentSHA256 || record.Origin != recordBefore.Origin || !reflect.DeepEqual(record.Facts.KeyEvents, recordBefore.Facts.KeyEvents) || !reflect.DeepEqual(record.StyleDelta, recordBefore.StyleDelta) {
		t.Fatalf("accepted chapter baseline changed unexpectedly: %+v %v", record, err)
	}
	if body, _ := s.Drafts.LoadChapterText(1); body != "原有正文" {
		t.Fatalf("completed prose changed: %q", body)
	}
	if draft, _ := s.Drafts.LoadDraft(1); draft != "保留未完成草稿" {
		t.Fatalf("nonempty draft changed: %q", draft)
	}
	for _, chapter := range []int{1, 2} {
		if plan, err := s.Drafts.LoadChapterPlan(chapter); err != nil || plan != nil {
			t.Fatalf("chapter %d retained stale construction: %+v %v", chapter, plan, err)
		}
	}
	if plan, err := s.Drafts.LoadChapterPlan(3); err != nil || plan == nil {
		t.Fatalf("unrelated construction removed: %+v %v", plan, err)
	}
	progressAfter, _ := os.ReadFile(filepath.Join(s.Dir(), "meta/progress.json"))
	if !bytes.Equal(progressBefore, progressAfter) {
		t.Fatal("editing planning changed progress or completed phase")
	}
	meta, _ := s.RunMeta.Load()
	if meta.StartPrompt != "原始创作需求" || meta.TargetChapters != 6 || !meta.Workbench.PlanningRevisionPending || meta.Workbench.ApprovedFingerprint != "" || meta.Workbench.ApprovedFoundationFingerprint != "" || meta.Workbench.BatchEnd != 0 || meta.Workbench.AcceptedChapter != 1 || !strings.Contains(meta.Workbench.PlanningRevisionRequest, "保留用户手动内容") {
		t.Fatalf("approval was not invalidated safely: %+v", meta)
	}
}

func TestWorkbenchFlatPlanEditRenamesOnlyMatchingVolume(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	_ = s.Outline.SavePremise("premise")
	entries := []domain.OutlineEntry{
		{Chapter: 1, Title: "一", CoreEvent: "事件一", Hook: "钩子一"},
		{Chapter: 2, Title: "二", CoreEvent: "事件二", Hook: "钩子二", VolumeID: 1},
		{Chapter: 3, Title: "三", CoreEvent: "事件三", Hook: "钩子三", VolumeID: 2, VolumeTitle: "末卷"},
	}
	_ = s.Outline.SaveOutline(entries)
	_, err := s.UpdateWorkbenchChapterPlan(2, WorkbenchChapterPlanEdit{VolumeTitle: workbenchText("卷一"), Hook: workbenchText("新钩子")}, workbenchFingerprint(t, s))
	if err != nil {
		t.Fatal(err)
	}
	result, _ := s.Outline.LoadOutline()
	if result[0].VolumeTitle != "卷一" || result[1].VolumeTitle != "卷一" || result[1].Hook != "新钩子" || !reflect.DeepEqual(result[2], entries[2]) || result[0].Chapter != 1 || result[1].VolumeID != 1 {
		t.Fatalf("flat edit changed membership or unrelated entry: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "layered_outline.json")); !os.IsNotExist(err) {
		t.Fatalf("flat edit created a layered source: %v", err)
	}
}

func TestWorkbenchPlanEditRejectsStaleEditorsAndNoopPreservesApproval(t *testing.T) {
	s := workbenchPlanFixture(t)
	fingerprint := workbenchFingerprint(t, s)
	before := captureWorkbenchFiles(t, s)
	if result, err := s.UpdateWorkbenchChapterPlan(1, WorkbenchChapterPlanEdit{Title: workbenchText("旧一")}, fingerprint); err != nil || result != fingerprint {
		t.Fatalf("identical edit: %s %v", result, err)
	}
	assertWorkbenchFiles(t, s, before)
	if _, err := s.UpdateWorkbenchChapterPlan(1, WorkbenchChapterPlanEdit{Title: workbenchText("新一")}, fingerprint); err != nil {
		t.Fatal(err)
	}
	updated := captureWorkbenchFiles(t, s)
	if _, err := s.UpdateWorkbenchChapterPlan(2, WorkbenchChapterPlanEdit{Title: workbenchText("陈旧编辑")}, fingerprint); !errors.Is(err, ErrWorkbenchPlanChanged) {
		t.Fatalf("stale edit was accepted: %v", err)
	}
	assertWorkbenchFiles(t, s, updated)
}

func TestWorkbenchPlanEditRollsBackAllViewsTitlesAndConstruction(t *testing.T) {
	for _, rel := range []string{"layered_outline.json", "layered_outline.md", "outline.json", "outline.md", "summaries/01.json", ChapterRecordPath(1), "drafts/01.plan.json", "drafts/02.plan.json", "meta/run.json"} {
		t.Run(rel, func(t *testing.T) {
			s := workbenchPlanFixture(t)
			_, _ = s.Summaries.LoadSummaryTitle(1)
			before := captureWorkbenchFiles(t, s)
			fingerprint := workbenchFingerprint(t, s)
			fired := false
			hook := func(path string) error {
				if filepath.ToSlash(path) == rel && !fired {
					fired = true
					return errInjectedOutlineMutation
				}
				return nil
			}
			for _, io := range []*IO{s.Outline.io, s.RunMeta.io, s.Summaries.io, s.ChapterRecords.io, s.Drafts.io} {
				io.setMutationHookForTest(hook)
			}
			_, err := s.UpdateWorkbenchChapterPlan(1, WorkbenchChapterPlanEdit{Title: workbenchText("新一"), VolumeTitle: workbenchText("新卷")}, fingerprint)
			if !errors.Is(err, errInjectedOutlineMutation) {
				t.Fatalf("expected injected error for %s: %v", rel, err)
			}
			assertWorkbenchFiles(t, s, before)
			if title, _ := s.Summaries.LoadSummaryTitle(1); title != "旧一" {
				t.Fatalf("failed save changed title cache: %q", title)
			}
		})
	}
}

func TestWorkbenchPlanEditSerializesCompetingEditors(t *testing.T) {
	s := workbenchPlanFixture(t)
	fingerprint := workbenchFingerprint(t, s)
	var wg sync.WaitGroup
	errorsByEditor := make([]error, 2)
	for i, title := range []string{"编辑甲", "编辑乙"} {
		wg.Add(1)
		go func(i int, title string) {
			defer wg.Done()
			_, errorsByEditor[i] = s.UpdateWorkbenchChapterPlan(1, WorkbenchChapterPlanEdit{Title: workbenchText(title)}, fingerprint)
		}(i, title)
	}
	wg.Wait()
	successes, conflicts := 0, 0
	for _, err := range errorsByEditor {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrWorkbenchPlanChanged) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("competing edits: %v", errorsByEditor)
	}
}

func TestWorkbenchPlanEditRejectsIncompleteOrInvalidRequestsWithoutWrites(t *testing.T) {
	s := workbenchPlanFixture(t)
	fingerprint := workbenchFingerprint(t, s)
	before := captureWorkbenchFiles(t, s)
	emptyScene := []string{" "}
	for _, edit := range []WorkbenchChapterPlanEdit{
		{},
		{Title: workbenchText(" ")},
		{Title: workbenchText("多行\n标题")},
		{VolumeTitle: workbenchText(" ")},
		{CoreEvent: workbenchText(" ")},
		{Scenes: &emptyScene},
	} {
		if _, err := s.UpdateWorkbenchChapterPlan(1, edit, fingerprint); !errors.Is(err, ErrWorkbenchPlanInvalid) {
			t.Fatalf("invalid request was accepted: %+v %v", edit, err)
		}
		assertWorkbenchFiles(t, s, before)
	}
	if _, err := s.UpdateWorkbenchChapterPlan(99, WorkbenchChapterPlanEdit{Title: workbenchText("不存在")}, fingerprint); !errors.Is(err, ErrOutlineChapterNotFound) {
		t.Fatalf("nonexistent chapter was accepted: %v", err)
	}
	assertWorkbenchFiles(t, s, before)
	if _, err := s.UpdateWorkbenchChapterPlan(1, WorkbenchChapterPlanEdit{Title: workbenchText("缺指纹")}, ""); !errors.Is(err, ErrWorkbenchPlanInvalid) {
		t.Fatalf("missing fingerprint was accepted: %v", err)
	}
	assertWorkbenchFiles(t, s, before)
	if err := os.Remove(filepath.Join(s.Dir(), "premise.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateWorkbenchChapterPlan(1, WorkbenchChapterPlanEdit{Title: workbenchText("尚无内核")}, fingerprint); !errors.Is(err, ErrWorkbenchPlanInvalid) {
		t.Fatalf("incomplete planning error unclear: %v", err)
	}
}

func TestWorkbenchPlanEditMayClearEndingHookAndScenes(t *testing.T) {
	s := workbenchPlanFixture(t)
	emptyScenes := []string{}
	if _, err := s.UpdateWorkbenchChapterPlan(1, WorkbenchChapterPlanEdit{Hook: workbenchText(""), Scenes: &emptyScenes}, workbenchFingerprint(t, s)); err != nil {
		t.Fatal(err)
	}
	entry, err := s.Outline.GetChapterOutline(1)
	if err != nil || entry.Hook != "" || len(entry.Scenes) != 0 || entry.CoreEvent != "收到信" {
		t.Fatalf("clearing optional fields: %+v %v", entry, err)
	}
	if plan, _ := s.Drafts.LoadChapterPlan(2); plan == nil {
		t.Fatal("editing one chapter deleted another chapter's construction")
	}
}

func TestWorkbenchPlanEditCannotRaceUnfinishedCommitOrRevision(t *testing.T) {
	for _, rel := range []string{"meta/pending_commit.json", pendingRevisionPath} {
		t.Run(rel, func(t *testing.T) {
			s := workbenchPlanFixture(t)
			if err := s.Signals.io.WriteJSON(rel, map[string]any{"chapter": 1}); err != nil {
				t.Fatal(err)
			}
			before := captureWorkbenchFiles(t, s)
			if _, err := s.UpdateWorkbenchChapterPlan(1, WorkbenchChapterPlanEdit{Title: workbenchText("新标题")}, workbenchFingerprint(t, s)); err == nil {
				t.Fatal("unfinished transaction did not block plan edit")
			}
			assertWorkbenchFiles(t, s, before)
		})
	}
}

func captureWorkbenchFiles(t *testing.T, s *Store) []outlineMutationSnapshot {
	t.Helper()
	var files []outlineMutationSnapshot
	if err := filepath.WalkDir(s.Dir(), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.Dir(), path)
		if err != nil {
			return err
		}
		snapshot, err := s.snapshotWorkbenchPlanFile(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		files = append(files, snapshot)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func assertWorkbenchFiles(t *testing.T, s *Store, want []outlineMutationSnapshot) {
	t.Helper()
	if got := captureWorkbenchFiles(t, s); !reflect.DeepEqual(got, want) {
		for i := range want {
			if i >= len(got) || !reflect.DeepEqual(want[i], got[i]) {
				t.Fatalf("file %s changed or disappeared after rejected/failed edit", want[i].rel)
			}
		}
		t.Fatalf("file count changed: %d → %d", len(want), len(got))
	}
}
