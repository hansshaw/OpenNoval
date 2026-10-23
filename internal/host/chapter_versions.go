package host

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hansshaw/AInoval-web-ui/internal/agents"
	"github.com/hansshaw/AInoval-web-ui/internal/bootstrap"
	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	"github.com/hansshaw/AInoval-web-ui/internal/tools"
	"github.com/voocel/agentcore"
)

func (h *Host) beginChapterVersionOperation(ctx context.Context, action string) (context.Context, func(), error) {
	if err := h.acquireExclusive(action); err != nil {
		return nil, nil, err
	}
	h.mu.Lock()
	if h.closing {
		h.exclusive = ""
		h.mu.Unlock()
		return nil, nil, fmt.Errorf("工作台正在关闭")
	}
	ctx, cancel := context.WithCancel(ctx)
	h.exclusiveCancel = cancel
	h.asyncWG.Add(1)
	h.mu.Unlock()
	return ctx, func() { h.releaseExclusive(); h.asyncWG.Done() }, nil
}

// SaveWorkbenchChapterContent keeps both manuscripts and saves immediately,
// without a model dependency. Existing revision-sync gates subsequent writing.
func (h *Host) SaveWorkbenchChapterContent(ctx context.Context, chapter int, title, content, expectedSHA string) (domain.ChapterVersion, error) {
	ctx, done, err := h.beginChapterVersionOperation(ctx, "保存章节正文")
	if err != nil {
		return domain.ChapterVersion{}, err
	}
	defer done()
	if err := ctx.Err(); err != nil {
		return domain.ChapterVersion{}, err
	}
	if err := h.requireNoPendingChapterTransaction(); err != nil {
		return domain.ChapterVersion{}, err
	}
	return h.store.ChapterVersions.SaveManual(chapter, title, content, expectedSHA)
}

func (h *Host) ApplyWorkbenchChapterVersion(ctx context.Context, chapter int, id, expectedSHA string) (domain.ChapterVersion, error) {
	ctx, done, err := h.beginChapterVersionOperation(ctx, "应用章节版本")
	if err != nil {
		return domain.ChapterVersion{}, err
	}
	defer done()
	if err := ctx.Err(); err != nil {
		return domain.ChapterVersion{}, err
	}
	if err := h.requireNoPendingChapterTransaction(); err != nil {
		return domain.ChapterVersion{}, err
	}
	return h.store.ChapterVersions.Apply(chapter, id, expectedSHA)
}

func (h *Host) requireNoPendingChapterTransaction() error {
	if pending, err := h.store.Signals.LoadPendingCommit(); err != nil {
		return err
	} else if pending != nil {
		return fmt.Errorf("章节提交尚未完成，请先恢复任务")
	}
	if pending, err := h.store.Revisions.LoadPending(); err != nil {
		return err
	} else if pending != nil {
		return fmt.Errorf("修订同步尚未完成，请先同步修订")
	}
	return nil
}

// RewriteWorkbenchChapters uses the writer's configured streaming client and
// current novel_context, but provides no mutating tools. A model can only return
// a candidate manuscript. The original files, acceptance records and progress
// stay unchanged unless the user explicitly selected autoApply.
func (h *Host) RewriteWorkbenchChapters(ctx context.Context, chapters []int, instruction string, autoApply bool) ([]domain.ChapterVersion, error) {
	ctx, done, err := h.beginChapterVersionOperation(ctx, "批量重写章节")
	if err != nil {
		return nil, err
	}
	defer done()
	if err := h.budget.Refuse(); err != nil {
		return nil, err
	}
	if err := h.requireNoPendingChapterTransaction(); err != nil {
		return nil, err
	}
	if err := h.requireCleanChapters(); err != nil {
		return nil, err
	}
	instruction = strings.TrimSpace(instruction)
	if instruction == "" || len(instruction) > 64<<10 {
		return nil, fmt.Errorf("请填写 1–65536 字节的明确重写指令")
	}
	if len(chapters) == 0 || len(chapters) > 100 {
		return nil, fmt.Errorf("每批请选择 1–100 个已完成章节")
	}
	chapters = slices.Clone(chapters)
	slices.Sort(chapters)
	progress, err := h.store.Progress.Load()
	if err != nil {
		return nil, err
	}
	if progress == nil || len(progress.PendingRewrites) > 0 {
		return nil, fmt.Errorf("请先完成当前章节任务")
	}
	fingerprint, err := h.store.PlanningFingerprint()
	if err != nil {
		return nil, err
	}
	meta, err := h.store.RunMeta.Load()
	if err != nil {
		return nil, err
	}
	if meta == nil || meta.Workbench == nil || meta.Workbench.ApprovedFingerprint != fingerprint || meta.Workbench.PlanningRevisionPending {
		return nil, fmt.Errorf("请先完成当前规划审查并确认目录")
	}
	if foundation, err := currentFoundationFingerprint(h.store); err != nil {
		return nil, err
	} else if foundation != meta.Workbench.ApprovedFoundationFingerprint {
		return nil, fmt.Errorf("请先重新审查并确认当前基础设定")
	}
	for i, chapter := range chapters {
		if chapter <= 0 || !slices.Contains(progress.CompletedChapters, chapter) || i > 0 && chapter == chapters[i-1] {
			return nil, fmt.Errorf("仅支持不重复的已完成章节")
		}
	}
	if err := h.store.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		for _, chapter := range chapters {
			w.Feedback = append(w.Feedback, domain.WorkbenchFeedback{Kind: "chapter", Chapter: chapter, Text: instruction, Time: time.Now().Format(time.RFC3339)})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	contextTool := tools.NewContextTool(h.store, h.bundle.References, h.cfg.Style, h.styleStats, h.cfg.StyleReferenceProfiles)
	model := h.models.ForRoleWithFailover("writer", nil)
	provider, name, _ := h.models.CurrentSelection("writer")
	maxTokens, _ := h.cfg.ResolveMaxOutputTokens(provider, name)
	options := []agentcore.CallOption{agentcore.WithMaxTokens(maxTokens)}
	if level, err := agents.ParseThinkingLevel(h.cfg.ResolveReasoningEffort("writer")); err == nil {
		if resolved, ok := agents.ResolveThinkingForModel(model, level); ok {
			options = append(options, agentcore.WithThinking(resolved))
		}
	}
	var versions []domain.ChapterVersion
	baseHashes := map[int]string{}
	for _, chapter := range chapters {
		if err := ctx.Err(); err != nil {
			return versions, err
		}
		if err := h.budget.Refuse(); err != nil {
			return versions, err
		}
		content, err := h.store.Drafts.LoadChapterText(chapter)
		if err != nil || strings.TrimSpace(content) == "" {
			return versions, fmt.Errorf("第 %d 章原正文不可用", chapter)
		}
		entry, err := h.store.Outline.GetChapterOutline(chapter)
		if err != nil {
			return versions, err
		}
		title := fmt.Sprintf("第 %d 章", chapter)
		if entry != nil && entry.Title != "" {
			title = entry.Title
		}
		if _, err := h.store.ChapterVersions.EnsureCurrent(chapter, title, content); err != nil {
			return versions, err
		}
		baseHashes[chapter] = domain.ChapterContentSHA256(content)
		args, _ := json.Marshal(map[string]int{"chapter": chapter})
		currentContext, err := contextTool.Execute(ctx, args)
		if err != nil {
			return versions, err
		}
		payload, err := json.Marshal(map[string]any{"chapter": chapter, "title": title, "instruction": instruction, "original_content": content, "novel_context": json.RawMessage(currentContext)})
		if err != nil {
			return versions, err
		}
		system := "你是小说章节重写作者。仅按用户明确指令重写本章，保留未要求修改的情节、人物、设定与前后衔接。遵守 novel_context 中最新已保存的章节规划、用户原则、风格引用及事实；不要复制参考语料。本次只生成候选版本，不得调用工具或改变其他章节。只输出完整的新章节正文，保留本章标题；不要解释、评语、JSON 或代码围栏。"
		if h.bundle.Voice != "" {
			system += "\n\n写作标准：\n" + h.bundle.Voice
		}
		if style := h.bundle.Styles[h.cfg.Style]; style != "" {
			system += "\n\n项目风格：\n" + style
		}
		if directive := h.cfg.RoleDirectives["writer"]; directive != "" {
			system += "\n\n项目作者指令：\n" + directive
		}
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Agent: "writer", Summary: fmt.Sprintf("正在生成第 %d 章重写候选（原稿保留）", chapter)})
		message, err := streamChapterCandidate(bootstrap.WithModelRequestAgent(ctx, "writer"), model, []agentcore.Message{agentcore.SystemMsg(system), agentcore.UserMsg(string(payload))}, h.emitDelta, options...)
		if err != nil {
			return versions, fmt.Errorf("第 %d 章重写失败（原稿保留）: %w", chapter, err)
		}
		if h.usage != nil {
			h.usage.Record("writer", instruction, message)
		}
		version, err := h.store.ChapterVersions.Append(chapter, title, message.TextContent(), instruction, "rewrite")
		if err != nil {
			return versions, err
		}
		versions = append(versions, version)
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Agent: "writer", Level: "success", Summary: fmt.Sprintf("第 %d 章版本 %d 已保存，原稿保持原样", chapter, version.Number)})
	}
	if autoApply {
		for i, version := range versions {
			if err := ctx.Err(); err != nil {
				return versions, err
			}
			applied, err := h.store.ChapterVersions.Apply(version.Chapter, version.ID, baseHashes[version.Chapter])
			if err != nil {
				return versions, fmt.Errorf("候选已保存，第 %d 章自动覆盖失败: %w", version.Chapter, err)
			}
			versions[i] = applied
		}
	}
	return versions, nil
}

func streamChapterCandidate(ctx context.Context, model agentcore.ChatModel, messages []agentcore.Message, onDelta func(string), options ...agentcore.CallOption) (agentcore.Message, error) {
	stream, err := model.GenerateStream(ctx, messages, nil, options...)
	if err != nil {
		return agentcore.Message{}, err
	}
	for {
		select {
		case <-ctx.Done():
			return agentcore.Message{}, ctx.Err()
		case event, ok := <-stream:
			if !ok {
				return agentcore.Message{}, fmt.Errorf("流在完整结束标记前中断，未发布不完整版本")
			}
			switch event.Type {
			case agentcore.StreamEventTextDelta:
				if onDelta != nil {
					onDelta(event.Delta)
				}
			case agentcore.StreamEventError:
				if event.Err != nil {
					return agentcore.Message{}, event.Err
				}
				return agentcore.Message{}, fmt.Errorf("模型流读取失败")
			case agentcore.StreamEventDone:
				if err := ctx.Err(); err != nil {
					return agentcore.Message{}, err
				}
				reason := event.Message.StopReason
				if reason == "" {
					reason = event.StopReason
				}
				switch reason {
				case agentcore.StopReasonLength, agentcore.StopReasonSafety, agentcore.StopReasonError, agentcore.StopReasonAborted, agentcore.StopReasonToolUse:
					return agentcore.Message{}, fmt.Errorf("模型输出未完整结束：%s", reason)
				}
				if strings.TrimSpace(event.Message.TextContent()) == "" {
					return agentcore.Message{}, fmt.Errorf("模型未返回完整正文")
				}
				return event.Message, nil
			}
		}
	}
}
