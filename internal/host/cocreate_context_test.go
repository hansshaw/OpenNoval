package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hansshaw/OpenNovel/internal/bootstrap"
	"github.com/voocel/agentcore"
)

type coCreateBudgetModel struct {
	requests        [][]agentcore.Message
	outputs         []int
	summaries       [][]agentcore.Message
	summaryOutputs  []int
	summaryResponse string
	summaryStop     agentcore.StopReason
	summaryError    error
	cancel          context.CancelFunc
	firstStop       agentcore.StopReason
}

func (m *coCreateBudgetModel) SupportsTools() bool { return false }
func (m *coCreateBudgetModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, options ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.summaries = append(m.summaries, messages)
	m.summaryOutputs = append(m.summaryOutputs, agentcore.ResolveCallConfig(options).MaxTokens)
	if m.cancel != nil {
		m.cancel()
	}
	if m.summaryError != nil {
		return nil, m.summaryError
	}
	text := m.summaryResponse
	if text == "" {
		text = "<summary>用户明确要求：主角叫林月；禁止恋爱线；助手的开放式结局只是待确认建议。</summary>"
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{Content: []agentcore.ContentBlock{agentcore.TextBlock(text)}, StopReason: m.summaryStop}}, nil
}
func (m *coCreateBudgetModel) GenerateStream(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, options ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	m.requests = append(m.requests, messages)
	m.outputs = append(m.outputs, agentcore.ResolveCallConfig(options).MaxTokens)
	text, stop := coCreateTestResponse("整理完成", "完整且已确认的草稿", true), agentcore.StopReasonStop
	if len(m.requests) == 1 && m.firstStop != "" {
		text, stop = "<reply>不能采用的半段", m.firstStop
	}
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.StreamEvent{Type: agentcore.StreamEventDone, Message: agentcore.Message{Content: []agentcore.ContentBlock{agentcore.TextBlock(text)}, StopReason: stop}}
	close(ch)
	return ch, nil
}

func longCoCreateHistory(turns int) []CoCreateMessage {
	var history []CoCreateMessage
	for i := 0; i < turns; i++ {
		history = append(history, CoCreateMessage{Role: "user", Content: fmt.Sprintf("第 %d 轮要求：主角林月，禁止恋爱线。%s", i, strings.Repeat("故事细节", 50))},
			CoCreateMessage{Role: "assistant", Content: coCreateTestResponse(fmt.Sprintf("第 %d 轮问题：选择方案甲还是乙？", i), "## 完整草稿\n主角林月；禁止恋爱线。"+strings.Repeat("旧稿重复说明", 100), false)})
	}
	return append(history, CoCreateMessage{Role: "user", Content: "选择方案乙，结局仍待确认。"})
}

func TestCoCreateProjectionRemovesOnlySupersededDrafts(t *testing.T) {
	history := longCoCreateHistory(12)
	before := coCreateHistoryPrefixHash(history)
	messages := coCreateProject("system", history, nil)
	var combined strings.Builder
	for _, message := range messages {
		combined.WriteString(message.TextContent())
	}
	if count := strings.Count(combined.String(), "## 完整草稿"); count != 1 {
		t.Fatalf("got %d copies of the draft", count)
	}
	for _, message := range history {
		if message.Role == "user" && !strings.Contains(combined.String(), message.Content) {
			t.Fatal("a user requirement was discarded")
		}
	}
	if !strings.Contains(combined.String(), "第 0 轮问题") || !strings.Contains(combined.String(), "第 11 轮问题") {
		t.Fatal("questions behind user answers must remain")
	}
	if coCreateHistoryPrefixHash(history) != before {
		t.Fatal("projection mutated the durable history")
	}
}

func TestCoCreateLengthRetriesWithModelBudgetWithoutPartialDraft(t *testing.T) {
	model := &coCreateBudgetModel{firstStop: agentcore.StopReasonLength}
	models := &bootstrap.ModelSet{Default: bootstrap.NewSwappableModel("test", "cocreate", model, nil)}
	reply, err := coCreateStream(context.Background(), models, nil, "system", []CoCreateMessage{{Role: "user", Content: "规划一部小说"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Ready || len(model.requests) != 2 || model.outputs[0] <= 2048 || model.outputs[1] <= model.outputs[0] || model.outputs[1] > bootstrap.DefaultMaxOutputTokens {
		t.Fatalf("wrong recovery budgets: reply=%+v outputs=%v", reply, model.outputs)
	}
	for _, message := range model.requests[1] {
		if strings.Contains(message.TextContent(), "不能采用的半段") {
			t.Fatal("retry fed the partial draft into committed context")
		}
	}
}

func TestCoCreateSafetyDoesNotRetry(t *testing.T) {
	model := &coCreateBudgetModel{firstStop: agentcore.StopReasonSafety}
	models := &bootstrap.ModelSet{Default: bootstrap.NewSwappableModel("test", "cocreate", model, nil)}
	reply, err := coCreateStream(context.Background(), models, nil, "system", []CoCreateMessage{{Role: "user", Content: "规划一部小说"}}, nil)
	if err == nil || reply.Ready || len(model.requests) != 1 {
		t.Fatalf("safety response retried: reply=%+v calls=%d err=%v", reply, len(model.requests), err)
	}
}

func TestCoCreateCheckpointChunksCachesAndValidatesPrefix(t *testing.T) {
	history := longCoCreateHistory(20)
	// Remove the intentionally huge repeated draft for this small-window test.
	for i := range history {
		if history[i].Role == "assistant" {
			history[i].Content = coCreateTestResponse(fmt.Sprintf("第%d轮问题，甲还是乙？", i), "## 已确认\n林月；禁止恋爱线。", false)
		}
	}
	original := coCreateHistoryPrefixHash(history)
	model := &coCreateBudgetModel{}
	budget := coCreateBudget{Window: 3500, Output: 512, ModelOutput: 512, Safety: 300}
	prepared, err := prepareCoCreateContext(context.Background(), model, "system", history, budget)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Checkpoint == nil || len(model.summaries) < 2 {
		t.Fatalf("expected chunked compaction, calls=%d checkpoint=%+v", len(model.summaries), prepared.Checkpoint)
	}
	for i, messages := range model.summaries {
		if coCreateTokens(messages)+model.summaryOutputs[i]+budget.Safety > budget.Window {
			t.Fatalf("summary request %d exceeded window", i)
		}
	}
	if prepared.Budget.Input+prepared.Budget.Output+budget.Safety > budget.Window {
		t.Fatal("main request exceeded window")
	}
	if coCreateHistoryPrefixHash(history) != original {
		t.Fatal("raw history mutated")
	}
	joined := ""
	for _, message := range prepared.Messages {
		joined += message.TextContent()
	}
	if !strings.Contains(joined, "选择方案乙") || !strings.Contains(joined, "第 19 轮要求") || !strings.Contains(joined, "## 已确认") {
		t.Fatal("recent turn or latest draft missing")
	}
	calls := len(model.summaries)
	next := append(append([]CoCreateMessage(nil), history...), CoCreateCheckpointMessage(prepared.Checkpoint))
	reused, err := prepareCoCreateContext(context.Background(), model, "system", next, budget)
	if err != nil || len(model.summaries) != calls || reused.Checkpoint == nil {
		t.Fatalf("checkpoint was not reused: %v, calls=%d", err, len(model.summaries))
	}
	next[0].Content = "这是另外一条分支，主角周海。"
	_, checkpoint := coCreateHistoryAndCheckpoint(next)
	if checkpoint != nil {
		t.Fatal("checkpoint from a different branch was accepted")
	}
}

func TestCoCreateCompactionFailureDoesNotPublishCheckpoint(t *testing.T) {
	history := longCoCreateHistory(8)
	for i := range history {
		if history[i].Role == "assistant" {
			history[i].Content = coCreateTestResponse("继续澄清", "完整草稿", false)
		}
	}
	for _, kind := range []string{"length", "format", "error", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			model := &coCreateBudgetModel{}
			switch kind {
			case "length":
				model.summaryStop = agentcore.StopReasonLength
			case "format":
				model.summaryResponse = "没有闭合的 <summary>"
			case "error":
				model.summaryError = errors.New("upstream unavailable")
			case "cancel":
				model.cancel = cancel
			}
			before := coCreateHistoryPrefixHash(history)
			prepared, err := prepareCoCreateContext(ctx, model, "system", history, coCreateBudget{Window: 2400, Output: 512, ModelOutput: 512, Safety: 256})
			if err == nil || prepared.Checkpoint != nil {
				t.Fatalf("failed summary published: %+v %v", prepared, err)
			}
			if len(model.requests) != 0 || coCreateHistoryPrefixHash(history) != before {
				t.Fatal("failure changed source data or generated a draft")
			}
			if kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}

func TestCoCreateOversizedRecentInputIsNotTruncated(t *testing.T) {
	model := &coCreateBudgetModel{}
	history := []CoCreateMessage{{Role: "user", Content: strings.Repeat("不能丢失的细节", 2000)}}
	before := history[0].Content
	_, err := prepareCoCreateContext(context.Background(), model, "system", history, coCreateBudget{Window: 4000, Output: 1000, ModelOutput: 1000, Safety: 300})
	if err == nil || !strings.Contains(err.Error(), "输入超出上下文窗口") {
		t.Fatalf("missing input-specific error: %v", err)
	}
	if history[0].Content != before || len(model.summaries) != 0 || len(model.requests) != 0 {
		t.Fatal("oversized input was clipped or sent upstream")
	}
}

func TestCoCreateRetryHonorsSmallConfiguredOutputCap(t *testing.T) {
	model := &coCreateBudgetModel{}
	prepared, err := prepareCoCreateContext(context.Background(), model, "system", []CoCreateMessage{{Role: "user", Content: "故事"}}, coCreateBudget{Window: 4096, Output: 256, ModelOutput: 256, Safety: 256})
	if err != nil || prepared.Budget.Output != 256 {
		t.Fatalf("explicit small cap changed: %+v %v", prepared.Budget, err)
	}
}
