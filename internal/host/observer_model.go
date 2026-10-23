// Modified by hansshaw for AI Novel Studio, 2026-10-24.
// Original third-party attribution and license terms are retained in NOTICE.

package host

import (
	"fmt"
	"time"

	"github.com/hansshaw/AInoval-web-ui/internal/bootstrap"
	"github.com/voocel/agentcore"
)

// handleWorkerEvent 用 AgentLoop 的真实边界投影模型响应：
// TurnStart 发生在请求前，assistant MessageEnd 发生在工具执行前。
func (o *observer) handleWorkerEvent(agent string, ev agentcore.Event) {
	switch ev.Type {
	case agentcore.EventTurnStart:
		o.startModelResponse(agent)
	case agentcore.EventMessageEnd:
		if ev.Message != nil && ev.Message.GetRole() == agentcore.RoleAssistant {
			o.finishModelResponse(agent)
			if message, ok := ev.Message.(agentcore.Message); ok &&
				message.StopReason == agentcore.StopReasonLength {
				o.emitOutputTruncated(agent, &message)
			}
		}
	}
}

// emitOutputTruncated makes a provider-side max_tokens stop visible instead of
// silently treating a partial answer or malformed tool arguments as success.
// agentcore may automatically ask the model to continue after this event, so
// the warning describes the boundary rather than claiming the run has failed.
func (o *observer) emitOutputTruncated(agent string, message *agentcore.Message) {
	provider, modelName := "", ""
	contextWindow, outputLimit := 0, 0
	outputSource := ""
	if message.Usage != nil {
		provider = message.Usage.Provider
		modelName = message.Usage.Model
	}
	if o.models != nil {
		role := agentRoleName(agent)
		if provider == "" || modelName == "" {
			resolvedProvider, resolvedModel, _ := o.models.CurrentSelection(role)
			if provider == "" {
				provider = resolvedProvider
			}
			if modelName == "" {
				modelName = resolvedModel
			}
		}
		contextWindow, _ = o.models.ResolveContextWindow(provider, modelName)
		var resolvedSource bootstrap.OutputTokenSource
		outputLimit, resolvedSource = o.models.ResolveMaxOutputTokens(provider, modelName)
		outputSource = string(resolvedSource)
	}
	outputTokens := 0
	if message.Usage != nil {
		outputTokens = message.Usage.Output
	}
	detail := fmt.Sprintf(
		"stop_reason=%s provider=%s model=%s usage_output=%d max_output_tokens=%d output_source=%s context_window=%d。模型在单次输出上限处停止；本轮文本或工具参数可能不完整，创作引擎会按续写策略继续，建议检查章节是否断裂或结构是否完整。",
		message.StopReason,
		orUnknown(provider),
		orUnknown(modelName),
		outputTokens,
		outputLimit,
		orUnknown(outputSource),
		contextWindow,
	)
	o.emitAndLog(Event{
		Time:     time.Now(),
		Category: "MODEL",
		Agent:    agent,
		Summary:  "模型输出达到单次上限，内容可能尚未完成",
		Detail:   detail,
		Kind:     "output_truncated",
		Level:    "warn",
		Depth:    1,
	})
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func (o *observer) startModelResponse(agent string) {
	if agent == "" {
		return
	}
	now := time.Now()
	call := &activeCall{id: nextEventID(), start: now, summary: "等待模型", depth: 1}
	o.modelStarts[agent] = call
	o.emitAndLog(Event{
		ID:       call.id,
		Time:     now,
		Category: "MODEL",
		Agent:    agent,
		Summary:  call.summary,
		Level:    "info",
		Depth:    call.depth,
	})
}

func (o *observer) updateModelState(agent, summary string) {
	call := o.modelStarts[agent]
	if call == nil || summary == "" || call.summary == summary {
		return
	}
	call.summary = summary
	o.emitEv(Event{
		ID:       call.id,
		Time:     call.start,
		Category: "MODEL",
		Agent:    agent,
		Summary:  summary,
		Level:    "info",
		Depth:    call.depth,
	})
}

func (o *observer) finishModelResponse(agent string) {
	call := o.modelStarts[agent]
	if call == nil {
		return
	}
	delete(o.modelStarts, agent)
	call.summary = "模型响应"
	// MODEL 是实时观测事件，只写日志并投递 UI，不进入 runtime queue。
	o.emitEv(Event{
		ID:         call.id,
		Time:       call.start,
		FinishedAt: time.Now(),
		Category:   "MODEL",
		Agent:      agent,
		Summary:    call.summary,
		Level:      "success",
		Depth:      call.depth,
		Duration:   time.Since(call.start),
	})
}
