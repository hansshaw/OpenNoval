// Modified by hansshaw for AI Novel Studio, 2026-10-24.
// Original third-party attribution and license terms are retained in NOTICE.

package host

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hansshaw/OpenNovel/internal/bootstrap"
	"github.com/hansshaw/OpenNovel/internal/store"
	"github.com/voocel/agentcore"
)

// 冷启动共创：从零澄清需求，产出整本书的创作指令。
const coCreateSystemPrompt = `你是一个小说共创助手。你的任务不是直接开始写小说，而是在正式规划之前，通过多轮对话把用户的创作意图问清楚，并持续整理出一段可直接交给创作引擎的详细中文创作指令。

每一轮回复严格按以下 XML 格式输出，包含四个标签，依次出现，每个标签都必须有正确的开闭标签：

<reply>
给用户看的中文自然回复：先简短复述你理解到的关键点，再提出当前最需要澄清的问题。
- 首轮必须主动提出 3 到 6 个高价值问题，并按“故事与人物 / 世界与规则 / 叙事与篇幅 / 边界与偏好”等主题分组，便于用户逐项回答。
- 后续轮次提出 1 到 3 个最关键的追问，或请用户确认存在歧义的选择；不要机械重复用户已经明确回答的信息。
- 优先澄清：作品类型、目标读者、主角及其目标、核心矛盾、主要阻力、世界或现实规则、叙事视角、整体语气、目标篇幅与章节规模、结局倾向、感情线尺度、禁忌和必须保留的元素。
- 如果用户只给出一句话灵感，不要直接宣称信息充分；先把必要取舍问清楚。
- 只有关键信息已经足够支撑规则、人物、世界、分层目录和可持续正文规划时，才告诉用户“可以确认并生成规划”。桌面端用 Ctrl+S、Web 端用确认按钮，不要假定具体快捷键。
</reply>

<draft>
当前完整的创作指令草稿，使用 Markdown：直接从二级标题开始，建议包含 "## 作品定位"、"## 核心故事"、"## 人物与关系"、"## 世界与规则"、"## 叙事方案"、"## 篇幅与节奏"、"## 边界与禁忌"、"## 待确认项"；用项目符号列出具体、可执行的结论。
每一轮都要在已有结论上**累积更新**，吸收用户最新意图；即使本轮没有新增也要把完整草稿原样再写一次——不要省略、不要写“（保持上一轮）”之类的占位。若仍有未确认项，必须明确列出，不能自行假装用户已经决定。
</draft>
` + coCreateProtocolTail

// 阶段共创：小说已写了一部分，规划"后续阶段"的走向。调用方需把当前故事状态摘要
// 追加到本 prompt 之后（"## 当前故事状态" 段），让模型在已写内容的基础上规划。
const stageCoCreateSystemPrompt = `你是一个小说"阶段共创"助手。这本小说已经写了一部分（进度见下方"当前故事状态"）。用户暂停下来，想和你一起规划"后续阶段"的走向，再继续创作。

你的任务不是续写正文，而是通过多轮简短对话帮用户想清楚后面这一段（接下来若干章 / 下一弧 / 下一卷）要往哪走，并持续整理出一段"后续方向 brief"，供创作引擎据此推进。

铁律：所有建议必须与"当前故事状态"里已发生的剧情、人物、伏笔一致，绝不推翻或忽略已写内容；只规划"后续怎么走"，不重新设计整本书。

每一轮回复严格按以下 XML 格式输出，包含四个标签，依次出现，每个标签都必须有正确的开闭标签：

<reply>
给用户看的中文自然回复：先回应用户的输入，再最多提出 1 到 2 个当前最关键的问题。如果后续方向已足够清晰，告诉用户可以按 Ctrl+S 把方向交给创作引擎、继续创作。
</reply>

<draft>
当前完整的"后续方向 brief"，使用 Markdown：直接从二级标题开始，例如 "## 后续走向"、"## 关键转折"、"## 要收的伏笔"、"## 节奏与篇幅"；用项目符号列出要点。每一轮都要在已有结论上**累积更新**，吸收用户最新意图；即使本轮没有新增也要把完整 brief 原样再写一次——不要省略、不要写"（保持上一轮）"之类的占位。
</draft>
` + coCreateProtocolTail

// coCreateProtocolTail 是两种共创模式共用的输出协议尾部（<ready> / <suggestions> + 输出规范）。
// 两模式只在开场语境与 <draft> 语义上不同，协议完全一致。
const coCreateProtocolTail = `
<ready>false</ready>

<suggestions>
1-3 条"用户接下来可能想说的话"，每行一条以 "- " 开头。这是用户卡壳时的引导，
按数字键填入输入框，用户可再编辑后发送。

要求：
- 站在用户口吻，像用户对你说的话，不要写成助手反问。
- 每条不超过 25 字，多样化句式，避免千篇一律。
- 给倾向 / 选择 / 补充意图，不要一句话替用户写完整设定。
</suggestions>

输出规范：
- 必须使用四个 XML 标签：<reply> / <draft> / <ready> / <suggestions>，每个都必须完整开闭。
- 标签名只能小写英文，不要改写成 <REPLY> / <REWRITE> / <回复> 等任何变体。
- 标签外不要添加任何说明、思考或代码围栏。
- <draft> 内允许多行 Markdown，直接换行书写，不需要任何转义。
- <ready> 只写 true 或 false。只有足以开始正式规划、且没有会显著改变作品方向的关键未决项时才填 true；仍有题材、主角目标、核心矛盾、主要世界规则、篇幅或结局方向等关键歧义时必须填 false。
- <ready>true</ready> 时 <suggestions> 可以为空（保留空标签 <suggestions></suggestions> 即可）。`

// CoCreateProgressKind 标识流式回调的内容类型。
const (
	CoCreateProgressThinking = "thinking"
	CoCreateProgressReply    = "reply"
)

// 四段式 XML 标签输出。XML 风格比方括号 marker 更鲁棒——Claude/GPT 训练数据里
// 大量 <thinking>...</thinking> 这类格式，模型几乎不会把 <reply> 改写成 <REWRITE>
// 或其他变体；闭合标签也让流式中段截断更精确（不依赖找下一个 marker 来断尾）。
const (
	tagReply       = "reply"
	tagDraft       = "draft"
	tagReady       = "ready"
	tagSuggestions = "suggestions"
)

func coCreateStream(ctx context.Context, models *bootstrap.ModelSet, sessions *store.SessionStore, sysPrompt string, history []CoCreateMessage, onProgress func(kind, text string)) (reply CoCreateReply, err error) {
	ctx = bootstrap.WithModelRequestAgent(ctx, "thinking")
	if len(history) == 0 {
		return CoCreateReply{}, fmt.Errorf("cocreate history is empty")
	}
	model := models.ForRole("thinking")
	budget := coCreateLimits(models, model)
	var raw, thinking strings.Builder
	var attempts []coCreateAttempt
	start := time.Now()
	defer func() {
		if sessions == nil {
			return
		}
		if logErr := sessions.LogCoCreate(coCreateLogEntry{
			Time: time.Now(), DurationMS: time.Since(start).Milliseconds(), InputHistory: history,
			RawResponse: raw.String(), RawLen: len([]rune(raw.String())), Thinking: thinking.String(),
			ParsedReply: reply.Message, ParsedDraft: reply.Prompt, ParsedReady: reply.Ready,
			ParsedSugs: reply.Suggestions, Error: errString(err), Budget: budget, Attempts: attempts,
		}); logErr != nil {
			slog.Warn("共创会话日志落盘失败", "module", "cocreate", "err", logErr)
		}
	}()

	prepared, err := prepareCoCreateContext(ctx, model, sysPrompt, history, budget)
	budget = prepared.Budget
	if err != nil {
		return CoCreateReply{}, err
	}
	messages := prepared.Messages
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return CoCreateReply{}, err
		}
		raw.Reset()
		thinking.Reset()
		if attempt > 0 && onProgress != nil {
			onProgress(CoCreateProgressThinking, "")
			onProgress(CoCreateProgressReply, "")
		}
		stop, callErr := receiveCoCreateStream(ctx, model, messages, budget.Output, &raw, &thinking, onProgress)
		attempts = append(attempts, coCreateAttempt{MaxOutputTokens: budget.Output, StopReason: string(stop), RawRunes: len([]rune(raw.String())), Error: errString(callErr)})
		if callErr != nil {
			if agentcore.IsContextOverflow(callErr) {
				return CoCreateReply{}, fmt.Errorf("上游明确报告上下文窗口溢出（不同于 stop_reason=length 的输出截断）：本地预计输入 %d + 输出预留 %d，窗口 %d（来源 %s）；请刷新上游模型参数或校正窗口配置。原始对话和草稿已保留: %w", budget.Input, budget.Output, budget.Window, budget.WindowSource, callErr)
			}
			return CoCreateReply{}, callErr
		}
		rawText := raw.String()
		// Some reasoning models return their complete structured answer only in
		// reasoning_content. An incomplete/length response is never rescued here.
		if strings.TrimSpace(rawText) == "" {
			rawText = strings.TrimSpace(thinking.String())
		}
		switch stop {
		case agentcore.StopReasonSafety, agentcore.StopReasonError, agentcore.StopReasonAborted, agentcore.StopReasonToolUse:
			return CoCreateReply{}, fmt.Errorf("cocreate generate incomplete (stop_reason=%s)", stop)
		}
		incompleteFormat := coCreateHasIncompleteProtocol(rawText)
		if stop == agentcore.StopReasonLength || incompleteFormat {
			if attempt == 0 {
				// Re-generate from the last committed state; never append a partial
				// draft or ask the model to concatenate an uncertain XML suffix.
				retry := append([]agentcore.Message(nil), prepared.Messages...)
				retry[0] = agentcore.SystemMsg(sysPrompt + "\n\n本轮重新生成：请保持用户明确约束和最新完整草稿的事实，合并重复措辞，回复和追问简短，完整输出四个闭合 XML 标签。不要续接此前截断内容，不要为了缩短而删除关键设定。")
				input := coCreateTokens(retry)
				available := min(budget.ModelOutput, budget.Window-budget.Safety-input)
				if available >= budget.Output {
					messages, budget.Input, budget.Output = retry, input, available
					continue
				}
			}
			if stop == agentcore.StopReasonLength {
				return CoCreateReply{}, fmt.Errorf("共创输出达到单次生成上限 (stop_reason=length)：本轮 max_tokens=%d，模型输出上限=%d（来源 %s），预计输入=%d，窗口=%d（来源 %s）。length 表示生成被截断，不能据此认定上下文窗口已满；旧草稿已保留，请检查上游输出/推理额度或调整模型限制", budget.Output, budget.ModelOutput, budget.OutputSource, budget.Input, budget.Window, budget.WindowSource)
			}
			return CoCreateReply{}, fmt.Errorf("共创回复格式不完整：重新生成后仍缺少完整标签，旧草稿已保留")
		}
		if onProgress != nil {
			onProgress(CoCreateProgressThinking, thinking.String())
			onProgress(CoCreateProgressReply, extractReplyPreview(rawText))
		}
		if err := ctx.Err(); err != nil {
			return CoCreateReply{}, err
		}
		reply, err = parseCoCreateResponse(rawText)
		if err == nil {
			reply.Checkpoint = prepared.Checkpoint
		}
		return reply, err
	}
	return CoCreateReply{}, fmt.Errorf("cocreate generate failed")
}

func receiveCoCreateStream(ctx context.Context, model agentcore.ChatModel, messages []agentcore.Message, output int, raw, thinking *strings.Builder, onProgress func(string, string)) (agentcore.StopReason, error) {
	stream, err := model.GenerateStream(ctx, messages, nil, agentcore.WithMaxTokens(output))
	if err != nil {
		return "", fmt.Errorf("cocreate generate: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case ev, ok := <-stream:
			if !ok {
				return "", fmt.Errorf("cocreate generate: %w", &agentcore.PartialStreamError{Partial: agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(raw.String()), agentcore.ThinkingBlock(thinking.String())}}})
			}
			switch ev.Type {
			case agentcore.StreamEventThinkingDelta:
				thinking.WriteString(ev.Delta)
				if onProgress != nil {
					onProgress(CoCreateProgressThinking, thinking.String())
				}
			case agentcore.StreamEventTextDelta:
				raw.WriteString(ev.Delta)
				if onProgress != nil {
					onProgress(CoCreateProgressReply, extractReplyPreview(raw.String()))
				}
			case agentcore.StreamEventDone:
				// Done is authoritative, including when transport recovered using a
				// non-streaming request and replaced a stale partial preview.
				raw.Reset()
				raw.WriteString(ev.Message.TextContent())
				thinking.Reset()
				thinking.WriteString(ev.Message.ThinkingContent())
				if err := ctx.Err(); err != nil {
					return "", err
				}
				if ev.Message.StopReason != "" {
					return ev.Message.StopReason, nil
				}
				return ev.StopReason, nil
			case agentcore.StreamEventError:
				if ev.Err != nil {
					return "", fmt.Errorf("cocreate generate: %w", ev.Err)
				}
				return "", fmt.Errorf("cocreate generate failed")
			}
		}
	}
}

func coCreateHasIncompleteProtocol(raw string) bool {
	known := false
	for _, tag := range []string{tagReply, tagDraft, tagReady, tagSuggestions} {
		known = known || strings.Contains(raw, "<"+tag+">") || strings.Contains(raw, "</"+tag+">")
	}
	if !known {
		return false
	} // Preserve natural-language fallback, with Ready=false.
	position := 0
	for _, tag := range []string{tagReply, tagDraft, tagReady, tagSuggestions} {
		open, close := "<"+tag+">", "</"+tag+">"
		if strings.Count(raw, open) != 1 || strings.Count(raw, close) != 1 {
			return true
		}
		start, end := strings.Index(raw, open), strings.Index(raw, close)
		if start < position || end < start+len(open) {
			return true
		}
		position = end + len(close)
	}
	return false
}

type coCreateAttempt struct {
	MaxOutputTokens int    `json:"max_output_tokens"`
	StopReason      string `json:"stop_reason,omitempty"`
	RawRunes        int    `json:"raw_runes"`
	Error           string `json:"error,omitempty"`
}

// coCreateLogEntry 是写入 meta/sessions/cocreate.jsonl 的一行结构。
// 字段命名贴近 jsonl 直查习惯（snake_case），方便 jq 过滤。
type coCreateLogEntry struct {
	Time         time.Time         `json:"time"`
	DurationMS   int64             `json:"duration_ms"`
	InputHistory []CoCreateMessage `json:"input_history"`
	RawResponse  string            `json:"raw_response"`
	RawLen       int               `json:"raw_len"`
	Thinking     string            `json:"thinking,omitempty"`
	ParsedReply  string            `json:"parsed_reply"`
	ParsedDraft  string            `json:"parsed_draft"`
	ParsedReady  bool              `json:"parsed_ready"`
	ParsedSugs   []string          `json:"parsed_sugs,omitempty"`
	Error        string            `json:"error,omitempty"`
	Budget       coCreateBudget    `json:"budget"`
	Attempts     []coCreateAttempt `json:"attempts,omitempty"`
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func assistantMsg(text string) agentcore.Message {
	return agentcore.Message{
		Role:      agentcore.RoleAssistant,
		Content:   []agentcore.ContentBlock{agentcore.TextBlock(text)},
		Timestamp: time.Now(),
	}
}

// parseCoCreateResponse 解析 XML 标签输出。模型若没遵守协议（直接说自然语言），
// 整段作为 reply 显示，draft 留空让 session 保留上一轮。
func parseCoCreateResponse(raw string) (CoCreateReply, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CoCreateReply{}, fmt.Errorf("cocreate empty response")
	}

	reply, draft, ready, suggestions := splitCoCreateMarkers(raw)
	if reply == "" {
		// 模型没遵守 XML 协议：整段作为 reply。
		return CoCreateReply{Message: raw, Prompt: "", Ready: false, Raw: raw}, nil
	}
	return CoCreateReply{
		Message:     reply,
		Prompt:      draft,
		Ready:       ready,
		Suggestions: suggestions,
		Raw:         raw,
	}, nil
}

// splitCoCreateMarkers 按四个 XML 标签切分文本。
// 标签可能缺失（流式中段或模型遗漏），缺失部分对应字段为空 / false / nil。
// 缺失闭标签时，extractTagContent 会取到字符串末尾，仍尽力解析。
func splitCoCreateMarkers(s string) (reply, draft string, ready bool, suggestions []string) {
	reply = extractTagContent(s, tagReply)
	draft = extractTagContent(s, tagDraft)
	readyStr := strings.ToLower(extractTagContent(s, tagReady))
	ready = readyStr == "true" || readyStr == "yes"
	suggestions = parseSuggestions(extractTagContent(s, tagSuggestions))
	return
}

// extractTagContent 从 s 中抠出 <tag>...</tag> 之间的文本。
// 三种偶发故障场景兜底，避免直接走降级丢字段：
//  1. 有开无闭（流式中段）→ 切到下一个已知开标签前
//  2. 无开有闭（模型 typo，如 <suggestions> 写成 <uggestions>）→ 从最近一个已知
//     完整闭合标签的结束位置开始，到 </tag> 之前
//  3. reply 完全无开标签（模型直接以自然语言开篇，末尾贴 </reply>）→ 从开头到 </reply>
func extractTagContent(s, tag string) string {
	open := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	oIdx := strings.Index(s, open)
	if oIdx >= 0 {
		rest := s[oIdx+len(open):]
		if cIdx := strings.Index(rest, closeTag); cIdx >= 0 {
			return strings.TrimSpace(rest[:cIdx])
		}
		// 有开无闭 → 切到下一个已知开标签前
		for _, other := range []string{"<reply>", "<draft>", "<ready>", "<suggestions>"} {
			if other == open {
				continue
			}
			if idx := strings.Index(rest, other); idx >= 0 {
				rest = rest[:idx]
			}
		}
		return strings.TrimSpace(rest)
	}

	// 无开有闭 → 从最近一个已知完整闭合标签的结束位置开始，到 </tag>。
	if cIdx := strings.Index(s, closeTag); cIdx >= 0 {
		prefix := s[:cIdx]
		start := 0
		for _, t := range []string{"</reply>", "</draft>", "</ready>", "</suggestions>"} {
			if t == closeTag {
				continue
			}
			if i := strings.LastIndex(prefix, t); i >= 0 {
				if end := i + len(t); end > start {
					start = end
				}
			}
		}
		return strings.TrimSpace(prefix[start:])
	}
	return ""
}

// parseSuggestions 把 <suggestions> 段每行抠出来，去掉 "- " / "* " / "1. " 等列表前缀。
// 最多保留 3 条；空行、过短（<2 字）、整行像 XML 标签的（typo 开标签兜底残留，
// 例如 <uggestions>）忽略。
func parseSuggestions(text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 整行像 XML 标签 → 跳过（防 typo 开标签污染）
		if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") {
			continue
		}
		// 剥列表前缀
		switch {
		case strings.HasPrefix(line, "- "):
			line = strings.TrimSpace(line[2:])
		case strings.HasPrefix(line, "* "):
			line = strings.TrimSpace(line[2:])
		case isOrderedSuggestion(line):
			line = stripOrderedPrefix(line)
		}
		if len([]rune(line)) < 2 {
			continue
		}
		out = append(out, line)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

// isOrderedSuggestion 判断行首是否形如 "1. " / "12. "（数字+点+空格）。
func isOrderedSuggestion(line string) bool {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(line) && line[i] == '.' && line[i+1] == ' '
}

func stripOrderedPrefix(line string) string {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(line) {
		return line
	}
	return strings.TrimSpace(line[i+2:])
}

// extractReplyPreview 流式预览：raw 还在生长时给 UI 一段可显示的文本。
// 找到 <reply> 之后的内容，切到 </reply> 或下一个开标签 <draft> 之前。
// 模型半遵守（漏 <reply> 开标签）时，开头到 </reply> 或 <draft> 都算 reply。
func extractReplyPreview(raw string) string {
	trimmed := strings.TrimSpace(raw)
	open := "<" + tagReply + ">"
	closeTag := "</" + tagReply + ">"
	draftOpen := "<" + tagDraft + ">"

	rest := trimmed
	if rIdx := strings.Index(trimmed, open); rIdx >= 0 {
		rest = trimmed[rIdx+len(open):]
	}
	if cIdx := strings.Index(rest, closeTag); cIdx >= 0 {
		return strings.TrimSpace(rest[:cIdx])
	}
	if dIdx := strings.Index(rest, draftOpen); dIdx >= 0 {
		rest = rest[:dIdx]
	}
	return strings.TrimSpace(rest)
}
