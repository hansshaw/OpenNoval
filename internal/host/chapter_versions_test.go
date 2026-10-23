package host

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hansshaw/AInoval-web-ui/assets"
	"github.com/hansshaw/AInoval-web-ui/internal/bootstrap"
	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	"github.com/hansshaw/AInoval-web-ui/internal/revision"
	"github.com/hansshaw/AInoval-web-ui/internal/tools"
	"github.com/voocel/agentcore"
)

type chapterCandidateModel struct {
	fn    func([]agentcore.Message) ([]agentcore.StreamEvent, error)
	calls int
}

func (m *chapterCandidateModel) Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return nil, fmt.Errorf("must stream")
}
func (m *chapterCandidateModel) SupportsTools() bool { return false }
func (m *chapterCandidateModel) GenerateStream(_ context.Context, messages []agentcore.Message, specs []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	if len(specs) > 0 {
		return nil, fmt.Errorf("candidate must have no mutating tools")
	}
	m.calls++
	events, err := m.fn(messages)
	if err != nil {
		return nil, err
	}
	out := make(chan agentcore.StreamEvent, len(events))
	for _, event := range events {
		out <- event
	}
	close(out)
	return out, nil
}

func candidateDone(text string) agentcore.StreamEvent {
	return agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: agentcore.Message{Role: agentcore.RoleAssistant, StopReason: agentcore.StopReasonStop, Content: []agentcore.ContentBlock{agentcore.TextBlock(text)}}}
}

func chapterVersionHost(t *testing.T, model *chapterCandidateModel) *Host {
	t.Helper()
	h := workbenchRevisionHost(t)
	h.models = &bootstrap.ModelSet{Default: bootstrap.NewSwappableModel("test", "writer", model, nil)}
	h.bundle = assets.Load("default", assets.LoadOptions{})
	h.styleStats = tools.NewStyleStatsIndex(h.store)
	h.cfg.Style = "default"
	for _, err := range []error{
		h.store.Book.Save(domain.BookMetadata{Title: "故事", Synopsis: "简介"}),
		h.store.Outline.SavePremise("主角不能更换身份"),
		h.store.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "一", CoreEvent: "最新人工规划", Hook: "疑问"}, {Chapter: 2, Title: "二", CoreEvent: "见面", Hook: "转折"}}),
		h.store.Characters.Save(nil), h.store.World.SaveWorldRules(nil),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	for chapter := 1; chapter <= 2; chapter++ {
		text := fmt.Sprintf("原稿%d", chapter)
		if err := h.store.Drafts.SaveFinalChapter(chapter, text); err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.ChapterRecords.Accept(chapter, domain.ChapterOriginGenerated, text, domain.ChapterFacts{Title: fmt.Sprintf("章%d", chapter)}, domain.StyleDelta{}); err != nil {
			t.Fatal(err)
		}
		if err := h.store.Progress.MarkChapterComplete(chapter, domain.WordCount(text), "", ""); err != nil {
			t.Fatal(err)
		}
	}
	fp, err := h.store.PlanningFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	foundation, err := h.store.FoundationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.ApprovedFingerprint = fp
		w.ApprovedFoundationFingerprint = foundation
		w.AcceptedChapter = 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestBatchChapterRewriteProducesCandidatesWithoutReplacingConfirmedOriginals(t *testing.T) {
	model := &chapterCandidateModel{fn: func(messages []agentcore.Message) ([]agentcore.StreamEvent, error) {
		if !strings.Contains(messages[1].TextContent(), "最新人工规划") {
			t.Fatal("saved planning was not used")
		}
		return []agentcore.StreamEvent{{Type: agentcore.StreamEventTextDelta, Delta: "旧预览"}, candidateDone("完整重写正文")}, nil
	}}
	h := chapterVersionHost(t, model)
	versions, err := h.RewriteWorkbenchChapters(context.Background(), []int{2, 1}, "保持主角，语气更克制", false)
	if err != nil || len(versions) != 2 || model.calls != 2 {
		t.Fatal(versions, err, model.calls)
	}
	for chapter := 1; chapter <= 2; chapter++ {
		current, _ := h.store.Drafts.LoadChapterText(chapter)
		if current != fmt.Sprintf("原稿%d", chapter) {
			t.Fatal("original replaced", current)
		}
		items, _ := h.store.ChapterVersions.List(chapter, current)
		if len(items) != 2 || items[1].Number != 2 || items[1].Content != "完整重写正文" || items[1].Active || !items[0].Active {
			t.Fatal(items)
		}
	}
	changes, err := revision.Scan(h.store)
	if err != nil || len(changes) != 0 {
		t.Fatal("candidate polluted accepted facts", changes, err)
	}
	meta, _ := h.store.RunMeta.Load()
	if len(meta.Workbench.Feedback) != 2 || meta.Workbench.AcceptedChapter != 2 {
		t.Fatal(meta.Workbench)
	}
}

func TestBatchAutoApplyWaitsForAllCandidatesAndRequiresFactSync(t *testing.T) {
	var h *Host
	model := &chapterCandidateModel{fn: func([]agentcore.Message) ([]agentcore.StreamEvent, error) {
		for chapter := 1; chapter <= 2; chapter++ {
			current, _ := h.store.Drafts.LoadChapterText(chapter)
			if current != fmt.Sprintf("原稿%d", chapter) {
				t.Fatal("autoApply wrote before all candidates completed")
			}
		}
		return []agentcore.StreamEvent{candidateDone("修改后的正文")}, nil
	}}
	h = chapterVersionHost(t, model)
	versions, err := h.RewriteWorkbenchChapters(context.Background(), []int{1, 2}, "重写明确选择的两章", true)
	if err != nil || len(versions) != 2 || !versions[0].Active || !versions[1].Active {
		t.Fatal(versions, err)
	}
	changes, err := revision.Scan(h.store)
	if err != nil || len(changes) != 2 {
		t.Fatal(changes, err)
	}
	if err := h.requireCleanChapters(); err == nil {
		t.Fatal("generation must be blocked until facts are synced")
	}
}

func TestCandidateStreamRejectsPartialAndLengthButUsesAuthoritativeDone(t *testing.T) {
	for _, test := range []struct {
		name      string
		events    []agentcore.StreamEvent
		wantError bool
	}{
		{"partial", []agentcore.StreamEvent{{Type: agentcore.StreamEventTextDelta, Delta: "半章"}}, true},
		{"length", []agentcore.StreamEvent{{Type: agentcore.StreamEventDone, Message: agentcore.Message{Role: agentcore.RoleAssistant, StopReason: agentcore.StopReasonLength, Content: []agentcore.ContentBlock{agentcore.TextBlock("半章")}}}}, true},
		{"complete", []agentcore.StreamEvent{{Type: agentcore.StreamEventTextDelta, Delta: "过期预览"}, candidateDone("完整正文")}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &chapterCandidateModel{fn: func([]agentcore.Message) ([]agentcore.StreamEvent, error) { return test.events, nil }}
			message, err := streamChapterCandidate(context.Background(), model, nil, nil)
			if (err != nil) != test.wantError {
				t.Fatal(message, err)
			}
			if !test.wantError && message.TextContent() != "完整正文" {
				t.Fatal(message)
			}
		})
	}
}

func TestBatchRewriteValidatesAllSelectedChaptersBeforeAnyModelRequest(t *testing.T) {
	model := &chapterCandidateModel{fn: func([]agentcore.Message) ([]agentcore.StreamEvent, error) {
		return []agentcore.StreamEvent{candidateDone("new")}, nil
	}}
	h := chapterVersionHost(t, model)
	for _, chapters := range [][]int{{1, 1}, {1, 3}, {0}, nil} {
		if _, err := h.RewriteWorkbenchChapters(context.Background(), chapters, "重写", false); err == nil {
			t.Fatal("invalid batch accepted", chapters)
		}
	}
	if model.calls != 0 {
		t.Fatal("invalid batch called model")
	}
	if err := h.store.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error { w.ApprovedFingerprint = "old"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := h.RewriteWorkbenchChapters(context.Background(), []int{1}, "重写", false); err == nil {
		t.Fatal("unapproved planning accepted")
	}
	if model.calls != 0 {
		t.Fatal("unapproved plan called model")
	}
}

func TestBatchEOFOrCancellationNeverAutoAppliesEarlierCandidates(t *testing.T) {
	for _, cancelSecond := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelSecond), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			model := &chapterCandidateModel{fn: func([]agentcore.Message) ([]agentcore.StreamEvent, error) {
				calls++
				if calls == 1 {
					return []agentcore.StreamEvent{candidateDone("第一章完整候选")}, nil
				}
				if cancelSecond {
					cancel()
				}
				return []agentcore.StreamEvent{{Type: agentcore.StreamEventTextDelta, Delta: "第二章中断的半稿"}}, nil
			}}
			h := chapterVersionHost(t, model)
			versions, err := h.RewriteWorkbenchChapters(ctx, []int{1, 2}, "两章重写", true)
			if err == nil || len(versions) != 1 || versions[0].Active {
				t.Fatal(versions, err)
			}
			for chapter := 1; chapter <= 2; chapter++ {
				text, _ := h.store.Drafts.LoadChapterText(chapter)
				if text != fmt.Sprintf("原稿%d", chapter) {
					t.Fatal("failed batch applied partial output", text)
				}
				items, _ := h.store.ChapterVersions.List(chapter, text)
				want := 1
				if chapter == 1 {
					want = 2
				}
				if len(items) != want {
					t.Fatal("published incomplete chapter", items)
				}
			}
		})
	}
}

func TestBatchAutoApplyReportsAlreadyAppliedVersionsOnAConflictingLaterChapter(t *testing.T) {
	var h *Host
	calls := 0
	model := &chapterCandidateModel{fn: func([]agentcore.Message) ([]agentcore.StreamEvent, error) {
		calls++
		if calls == 2 {
			if err := h.store.Drafts.SaveFinalChapter(2, "用户外部编辑不能丢失"); err != nil {
				t.Fatal(err)
			}
		}
		return []agentcore.StreamEvent{candidateDone("完整新稿")}, nil
	}}
	h = chapterVersionHost(t, model)
	versions, err := h.RewriteWorkbenchChapters(context.Background(), []int{1, 2}, "重写", true)
	if err == nil || len(versions) != 2 || !versions[0].Active || versions[1].Active || !strings.Contains(err.Error(), "第 2 章自动覆盖失败") {
		t.Fatal(versions, err)
	}
	text, _ := h.store.Drafts.LoadChapterText(2)
	if text != "用户外部编辑不能丢失" {
		t.Fatal("external edit overwritten", text)
	}
}
