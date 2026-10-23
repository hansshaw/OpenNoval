package host

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	"github.com/hansshaw/AInoval-web-ui/internal/store"
)

func TestDescribeArcEndLabelCompletePlanningDoesNotWaitForExpansion(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "完整规划",
		Arcs: []domain.ArcOutline{
			{
				Index: 1,
				Title: "第一弧",
				Goal:  "完成第一阶段",
				Chapters: []domain.OutlineEntry{{
					Chapter: 1, Title: "开端", CoreEvent: "阶段开始", Hook: "进入下一阶段",
				}},
			},
			{
				Index: 2,
				Title: "第二弧",
				Goal:  "完成第二阶段",
				Chapters: []domain.OutlineEntry{{
					Chapter: 2, Title: "延续", CoreEvent: "阶段推进", Hook: "接近卷末",
				}},
			},
		},
	}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	progress := &domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           true,
		CompletedChapters: []int{1},
		CurrentVolume:     1,
		CurrentArc:        1,
		CurrentChapter:    2,
	}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	if err := st.World.SaveReview(domain.ReviewEntry{
		Chapter: 1, Scope: "arc", Verdict: "accept", Summary: "第一弧通过",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveArcSummary(domain.ArcSummary{
		Volume: 1, Arc: 1, Summary: "第一弧摘要",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := describeArcEndLabel(st, progress)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("complete planning must continue without legacy expansion label, got %q", got)
	}
}

func TestDescribeArcEndLabelLegacySkeletonStillWaitsForExpansion(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "旧骨架",
		Arcs: []domain.ArcOutline{
			{
				Index: 1,
				Title: "第一弧",
				Goal:  "完成第一阶段",
				Chapters: []domain.OutlineEntry{{
					Chapter: 1, Title: "开端", CoreEvent: "阶段开始", Hook: "进入下一阶段",
				}},
			},
			{Index: 2, Title: "待展开弧", Goal: "旧规划的下一阶段", EstimatedChapters: 3},
		},
	}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	progress := &domain.Progress{
		Phase:             domain.PhaseWriting,
		Flow:              domain.FlowWriting,
		Layered:           true,
		CompletedChapters: []int{1},
		CurrentVolume:     1,
		CurrentArc:        1,
		CurrentChapter:    2,
	}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	if err := st.World.SaveReview(domain.ReviewEntry{
		Chapter: 1, Scope: "arc", Verdict: "accept", Summary: "第一弧通过",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Summaries.SaveArcSummary(domain.ArcSummary{
		Volume: 1, Arc: 1, Summary: "第一弧摘要",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := describeArcEndLabel(st, progress)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "待展开下一弧（V1 A2）") {
		t.Fatalf("legacy skeleton must keep expansion label, got %q", got)
	}
}

func TestRepairMissingLegacyPlanningPreservesStoryArtifacts(t *testing.T) {
	st, volumes := newLegacyPlanningStore(t)
	storyFiles := []string{
		"book.md",
		"outline.json",
		"layered_outline.json",
		"characters.json",
		"world_rules.json",
	}
	before := snapshotFiles(t, st.Dir(), storyFiles)

	if err := repairMissingLegacyPlanning(st); err != nil {
		t.Fatalf("repair legacy planning: %v", err)
	}
	after := snapshotFiles(t, st.Dir(), storyFiles)
	for _, name := range storyFiles {
		if !bytes.Equal(before[name], after[name]) {
			t.Fatalf("legacy story artifact %s changed during metadata repair", name)
		}
	}

	book, err := st.Book.Load()
	if err != nil {
		t.Fatal(err)
	}
	if book == nil || book.Title != "知微的裂缝" {
		t.Fatalf("unexpected repaired book metadata: %+v", book)
	}
	compass, err := st.Outline.LoadCompass()
	if err != nil {
		t.Fatal(err)
	}
	if compass == nil || compass.EndingDirection == "" || len(compass.OpenThreads) == 0 {
		t.Fatalf("legacy compass was not repaired: %+v", compass)
	}
	progress, err := st.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	if progress == nil || progress.Phase != domain.PhaseOutline {
		t.Fatalf("unexpected repaired progress: %+v", progress)
	}
	if want := len(domain.FlattenOutline(volumes)); progress.TotalChapters != want {
		t.Fatalf("total chapters = %d, want %d", progress.TotalChapters, want)
	}
	if len(progress.CompletedChapters) != 0 {
		t.Fatalf("legacy repair must not invent completed chapters: %v", progress.CompletedChapters)
	}
	run, err := st.RunMeta.Load()
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.PlanningTier != domain.PlanningTierLong || run.AdvanceMode != domain.ChapterAdvanceAuto {
		t.Fatalf("unexpected repaired run intent: %+v", run)
	}
	missing, err := st.FoundationMissing()
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "foundation_audit" {
		t.Fatalf("missing after repair = %v, want [foundation_audit]", missing)
	}
	if _, err := os.Stat(filepath.Join(st.Dir(), "meta", "foundation_audit.json")); !os.IsNotExist(err) {
		t.Fatalf("repair must not fabricate a foundation audit: %v", err)
	}

	compatibilityFiles := []string{
		"meta/book.json",
		"meta/compass.json",
		"meta/progress.json",
		"meta/run.json",
	}
	first := snapshotFiles(t, st.Dir(), compatibilityFiles)
	if err := repairMissingLegacyPlanning(st); err != nil {
		t.Fatalf("second repair should be idempotent: %v", err)
	}
	second := snapshotFiles(t, st.Dir(), compatibilityFiles)
	for _, name := range compatibilityFiles {
		if !bytes.Equal(first[name], second[name]) {
			t.Fatalf("second repair changed %s", name)
		}
	}
}

func TestRepairMissingLegacyPlanningStopsBeforeWriteOnCorruptArtifact(t *testing.T) {
	st, _ := newLegacyPlanningStore(t)
	if err := os.WriteFile(filepath.Join(st.Dir(), "characters.json"), []byte(`{"broken":`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := repairMissingLegacyPlanning(st); err == nil {
		t.Fatal("corrupt legacy artifact must abort repair")
	}
	for _, name := range []string{
		"meta/book.json",
		"meta/compass.json",
		"meta/progress.json",
		"meta/run.json",
		"meta/foundation_audit.json",
	} {
		if _, err := os.Stat(filepath.Join(st.Dir(), filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Fatalf("corrupt repair left compatibility file %s behind: %v", name, err)
		}
	}
}

func TestLegacyPlanningApprovalBindsPlanAndFoundationFingerprints(t *testing.T) {
	st, _ := newLegacyPlanningStore(t)
	if err := repairMissingLegacyPlanning(st); err != nil {
		t.Fatal(err)
	}
	h := &Host{
		store:  st,
		engine: &engine{},
		events: make(chan Event, 4),
	}
	approveCurrent := func() {
		t.Helper()
		foundationFingerprint, err := st.FoundationFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Outline.SaveFoundationAudit(domain.FoundationAudit{
			Fingerprint: foundationFingerprint,
			Ready:       true,
			Summary:     "基础设定一致",
			Issues:      []domain.FoundationAuditIssue{},
		}); err != nil {
			t.Fatal(err)
		}
		planningFingerprint, err := st.PlanningFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		if err := h.ApprovePlanning(planningFingerprint); err != nil {
			t.Fatalf("approve current legacy plan: %v", err)
		}
	}
	assertApprovalStale := func() {
		t.Helper()
		meta, err := st.RunMeta.Load()
		if err != nil {
			t.Fatal(err)
		}
		planningFingerprint, err := st.PlanningFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		foundationFingerprint, err := st.FoundationFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		if meta == nil || meta.Workbench == nil {
			t.Fatal("workbench approval was not persisted")
		}
		if meta.Workbench.ApprovedFingerprint == planningFingerprint &&
			meta.Workbench.ApprovedFoundationFingerprint == foundationFingerprint {
			t.Fatal("approval still matches after a protected artifact changed")
		}
		audit, err := st.Outline.LoadFoundationAudit()
		if err != nil {
			t.Fatal(err)
		}
		if audit != nil && audit.Fingerprint == foundationFingerprint {
			t.Fatal("old foundation audit still matches the mutated foundation")
		}
	}

	approveCurrent()

	if err := st.Outline.SavePremise("# 《知微的裂缝》\n\n## 终局方向\n修改后的终局\n\n## 核心兑现承诺\n- 修改后的承诺\n"); err != nil {
		t.Fatal(err)
	}
	assertApprovalStale()

	approveCurrent()
	if err := st.Characters.Save([]domain.Character{{
		Name: "知微", Role: "主角", Description: "修改后的角色", Arc: "完成蜕变",
	}}); err != nil {
		t.Fatal(err)
	}
	assertApprovalStale()

	approveCurrent()
	if err := st.World.SaveWorldRules([]domain.WorldRule{{
		Category: "society", Rule: "修改后的规则", Boundary: "修改后的边界",
	}}); err != nil {
		t.Fatal(err)
	}
	assertApprovalStale()
}

func newLegacyPlanningStore(t *testing.T) (*store.Store, []domain.VolumeOutline) {
	t.Helper()
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SavePremise("# 《知微的裂缝》\n\n## 终局方向\n知微在真相与记忆之间作出选择。\n\n## 核心兑现承诺\n- 揭开破碎记忆的来源\n- 完成人物关系与世界观收束\n"); err != nil {
		t.Fatal(err)
	}
	volumes := []domain.VolumeOutline{{
		Index: 1,
		Title: "裂缝",
		Theme: "记忆与身份",
		Arcs: []domain.ArcOutline{
			{
				Index: 1,
				Title: "失序",
				Goal:  "发现裂痕",
				Chapters: []domain.OutlineEntry{
					{Chapter: 1, Title: "最初裂痕", CoreEvent: "异常出现", Scenes: []string{"雨夜", "旧医院"}},
					{Chapter: 2, Title: "失落记录", CoreEvent: "找到记录", Scenes: []string{"档案室"}},
					{Chapter: 3, Title: "双重记忆", CoreEvent: "记忆冲突", Scenes: []string{"梦境"}},
				},
			},
			{Index: 2, Title: "追索", Goal: "逼近真相", EstimatedChapters: 2},
		},
	}}
	if err := st.Outline.SaveLayeredOutline(volumes); err != nil {
		t.Fatal(err)
	}
	if err := st.Characters.Save([]domain.Character{{
		Name: "知微", Role: "主角", Description: "追查破碎记忆的人", Arc: "从逃避到面对",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.World.SaveWorldRules([]domain.WorldRule{{
		Category: "other", Rule: "记忆可以被切割", Boundary: "记忆无法凭空创生",
	}}); err != nil {
		t.Fatal(err)
	}
	bookMarkdown := "# 《知微的裂缝》\n\n## 简介\n\n知微追查一段被切割的记忆。\n"
	if err := os.WriteFile(filepath.Join(st.Dir(), "book.md"), []byte(bookMarkdown), 0o644); err != nil {
		t.Fatal(err)
	}
	return st, volumes
}

func snapshotFiles(t *testing.T, root string, names []string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		files[name] = data
	}
	return files
}
