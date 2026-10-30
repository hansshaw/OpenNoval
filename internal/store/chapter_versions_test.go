package store

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/hansshaw/OpenNovel/internal/domain"
)

func seedVersionChapter(t *testing.T, st *Store, chapter int, text string) {
	t.Helper()
	if err := st.Drafts.SaveFinalChapter(chapter, text); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ChapterRecords.Accept(chapter, domain.ChapterOriginGenerated, text, domain.ChapterFacts{Title: "原章名"}, domain.StyleDelta{}); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkChapterComplete(chapter, domain.WordCount(text), "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestChapterVersionsPreserveOriginalAndKeepFactsPendingUntilSync(t *testing.T) {
	st := NewStore(t.TempDir())
	for _, err := range []error{st.Init(), st.Progress.Init(2), st.Progress.UpdatePhase(domain.PhaseWriting)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	seedVersionChapter(t, st, 1, "原始正文")
	seedVersionChapter(t, st, 2, "第二章原稿")
	originalHash := domain.ChapterContentSHA256("原始正文")
	original, err := st.ChapterVersions.EnsureCurrent(1, "原章名", "原始正文")
	if err != nil || original.Number != 1 {
		t.Fatal(original, err)
	}
	newVersion, err := st.ChapterVersions.Append(1, "原章名", "候选新正文较长", "改变叙述语气", "rewrite")
	if err != nil || newVersion.Number != 2 {
		t.Fatal(newVersion, err)
	}
	current, _ := st.Drafts.LoadChapterText(1)
	if current != "原始正文" {
		t.Fatal("candidate replaced original")
	}
	if _, err := st.ChapterVersions.Apply(1, newVersion.ID, "stale"); err == nil {
		t.Fatal("stale overwrite accepted")
	}
	if _, err := st.ChapterVersions.Apply(1, newVersion.ID, originalHash); err != nil {
		t.Fatal(err)
	}
	baseline, _ := st.ChapterRecords.Load(1)
	if baseline.Content != "原始正文" || baseline.ContentSHA256 != originalHash {
		t.Fatal("offline apply must preserve accepted facts baseline")
	}
	p, _ := st.Progress.Load()
	if p.ChapterWordCounts[1] != domain.WordCount(newVersion.Content) || p.TotalWordCount != domain.WordCount(newVersion.Content)+domain.WordCount("第二章原稿") {
		t.Fatalf("stale word counts: %+v", p)
	}
	unchanged, _ := st.Drafts.LoadChapterText(2)
	if unchanged != "第二章原稿" {
		t.Fatal("applying chapter one changed another chapter")
	}
	versions, err := st.ChapterVersions.List(1, newVersion.Content)
	if err != nil || len(versions) != 2 || versions[0].Active || !versions[1].Active || versions[0].Content != "原始正文" {
		t.Fatal(versions, err)
	}
	if _, err := st.ChapterVersions.Apply(1, original.ID, newVersion.ContentHash); err != nil {
		t.Fatal(err)
	}
	current, _ = st.Drafts.LoadChapterText(1)
	if current != "原始正文" {
		t.Fatal("restoring original version failed")
	}
}

func TestManualChapterSaveIsOfflineVersionedAndRejectsInvalidWrites(t *testing.T) {
	st := NewStore(t.TempDir())
	for _, err := range []error{st.Init(), st.Progress.Init(1), st.Progress.UpdatePhase(domain.PhaseWriting)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	seedVersionChapter(t, st, 1, "旧稿")
	manual, err := st.ChapterVersions.SaveManual(1, "手动章名", "手工保存的新正文", domain.ChapterContentSHA256("旧稿"))
	if err != nil || manual.Number != 2 || !manual.Active || manual.Kind != "manual" {
		t.Fatal(manual, err)
	}
	if _, err := st.ChapterVersions.SaveManual(1, "", "", manual.ContentHash); err == nil {
		t.Fatal("empty manuscript accepted")
	}
	if _, err := st.ChapterVersions.SaveManual(1, "", "new", ""); err == nil {
		t.Fatal("missing hash accepted")
	}
	if _, err := st.ChapterVersions.Apply(1, "../../elsewhere", manual.ContentHash); err == nil {
		t.Fatal("invalid identity accepted")
	}
	versions, _ := st.ChapterVersions.List(1, manual.Content)
	if len(versions) != 2 || versions[0].Content != "旧稿" {
		t.Fatal("rejected save changed versions", versions)
	}
	if _, err := st.ChapterVersions.EnsureCurrent(1, "手动章名", manual.Content); err != nil {
		t.Fatal(err)
	}
	versions, _ = st.ChapterVersions.List(1, manual.Content)
	if len(versions) != 2 {
		t.Fatal("refresh duplicated the active manuscript")
	}
}

func TestChapterVersionStoreSerializesSeparateStoreInstances(t *testing.T) {
	directory := t.TempDir()
	var wg sync.WaitGroup
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := NewStore(directory).ChapterVersions.Append(1, "title", fmt.Sprintf("body %d", n), "instruction", "rewrite")
			if err != nil {
				t.Error(err)
			}
		}(n)
	}
	wg.Wait()
	versions, err := NewStore(directory).ChapterVersions.List(1, "")
	if err != nil || len(versions) != 12 {
		t.Fatal("lost concurrent version", len(versions), err)
	}
	for i, version := range versions {
		if version.Number != i+1 {
			t.Fatal("non-monotonic version numbers", versions)
		}
	}
}

func TestManualSaveWithoutLegacyFactsDoesNotLoseOriginal(t *testing.T) {
	st := NewStore(t.TempDir())
	for _, err := range []error{st.Init(), st.Progress.Init(1), st.Progress.UpdatePhase(domain.PhaseWriting), st.Drafts.SaveFinalChapter(1, "旧作品正文"), st.Progress.MarkChapterComplete(1, 5, "", "")} {
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := st.ChapterVersions.SaveManual(1, "old", "new", domain.ChapterContentSHA256("旧作品正文"))
	if err == nil || !strings.Contains(err.Error(), "迁移") {
		t.Fatal(err)
	}
	text, _ := st.Drafts.LoadChapterText(1)
	if text != "旧作品正文" {
		t.Fatal("original lost without facts baseline")
	}
}

func TestChapterVersionFailedApplyRollsBackActiveManuscriptAndCounts(t *testing.T) {
	for _, stage := range []string{"progress", "active-version"} {
		t.Run(stage, func(t *testing.T) {
			st := NewStore(t.TempDir())
			for _, err := range []error{st.Init(), st.Progress.Init(1), st.Progress.UpdatePhase(domain.PhaseWriting)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			seedVersionChapter(t, st, 1, "原稿不可丢失")
			oldProgress, err := st.Progress.io.ReadFile("meta/progress.json")
			if err != nil {
				t.Fatal(err)
			}
			versionWrites := 0
			st.ChapterVersions.io.mutationHook = func(path string) error {
				if path == chapterVersionsPath(1) {
					versionWrites++
					if stage == "active-version" && versionWrites == 2 {
						return errors.New("fail active metadata")
					}
				}
				if stage == "progress" && path == "meta/progress.json" {
					return errors.New("fail counts")
				}
				return nil
			}
			if _, err := st.ChapterVersions.SaveManual(1, "章名", "新正文更长", domain.ChapterContentSHA256("原稿不可丢失")); err == nil {
				t.Fatal("expected injected failure")
			}
			current, _ := st.Drafts.LoadChapterText(1)
			if current != "原稿不可丢失" {
				t.Fatal("failed save changed original", current)
			}
			progress, _ := st.Progress.io.ReadFile("meta/progress.json")
			if !bytes.Equal(progress, oldProgress) {
				t.Fatal("failed save changed counts")
			}
			versions, err := st.ChapterVersions.List(1, current)
			if err != nil || len(versions) != 2 || !versions[0].Active || versions[1].Active {
				t.Fatal(versions, err)
			}
		})
	}
}

func TestChapterVersionSaveRejectsPendingRecoveryTransactions(t *testing.T) {
	st := NewStore(t.TempDir())
	for _, err := range []error{st.Init(), st.Progress.Init(1), st.Progress.UpdatePhase(domain.PhaseWriting)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	seedVersionChapter(t, st, 1, "原稿")
	if err := st.Revisions.SavePending(domain.PendingRevision{Stage: domain.RevisionStagePrepared}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ChapterVersions.SaveManual(1, "章", "new", domain.ChapterContentSHA256("原稿")); err == nil || !strings.Contains(err.Error(), "同步") {
		t.Fatal(err)
	}
	text, _ := st.Drafts.LoadChapterText(1)
	if text != "原稿" {
		t.Fatal("pending recovery overwritten")
	}
}
