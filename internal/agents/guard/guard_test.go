// Modified by hansshaw for AI Novel Studio, 2026-10-24.
// Original third-party attribution and license terms are retained in NOTICE.

package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hansshaw/OpenNovel/internal/domain"
	"github.com/hansshaw/OpenNovel/internal/store"
	"github.com/hansshaw/OpenNovel/internal/tools"
	"github.com/voocel/agentcore"
)

// An architect may repeat a successful audit on the same exact foundation.
// Checkpoint deduplication must not turn that success into an apparent abort.
func TestArchitectStopGuardAcceptsDeduplicatedAudit(t *testing.T) {
	s := newTestStore(t)
	if err := s.Progress.Init(1); err != nil {
		t.Fatal(err)
	}
	if err := s.RunMeta.Init("test", "initial brief", "model"); err != nil {
		t.Fatal(err)
	}
	if err := s.Book.Save(domain.BookMetadata{Title: "测试书", Synopsis: "审查同一版本"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SavePremise("前提"); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "第一章"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Characters.Save([]domain.Character{{Name: "主角", Role: "主角"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.World.SaveWorldRules([]domain.WorldRule{{Category: "世界", Rule: "规则"}}); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := s.FoundationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(domain.FoundationAudit{Fingerprint: fingerprint, Ready: true, Summary: "一致", Issues: []domain.FoundationAuditIssue{}})
	if err != nil {
		t.Fatal(err)
	}
	auditTool := tools.NewAuditFoundationTool(s)
	if _, err := auditTool.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	checkpoint := s.Checkpoints.LatestGlobal()
	if checkpoint == nil {
		t.Fatal("first audit must have a checkpoint")
	}
	if err := s.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.PlanningRevisionPending = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	guard := NewArchitectStopGuard(s, nil)
	stop := agentcore.StopInfo{TurnIndex: 1, Trigger: agentcore.StopTriggerEndTurn, Message: agentcore.Message{StopReason: agentcore.StopReasonStop}}
	if decision := guard(context.Background(), stop); decision.Allow || decision.Escalate {
		t.Fatalf("pending audit cannot end before the tool runs: %+v", decision)
	}
	if _, err := s.Checkpoints.Append(domain.GlobalScope(), "revise_outline", "outline.json", "new-plan"); err != nil {
		t.Fatal(err)
	}
	if decision := guard(context.Background(), stop); decision.Allow || decision.Escalate {
		t.Fatalf("a revised outline cannot satisfy the pending audit: %+v", decision)
	}
	stop.Trigger = agentcore.StopTriggerAfterTool
	if decision := guard(context.Background(), stop); decision.Allow || decision.Escalate {
		t.Fatalf("an unrelated terminal tool cannot bypass the pending audit: %+v", decision)
	}
	stop.Trigger = agentcore.StopTriggerEndTurn
	beforeRepeat := s.Checkpoints.LatestGlobal()
	if _, err := auditTool.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if latest := s.Checkpoints.LatestGlobal(); latest == nil || latest.Seq != beforeRepeat.Seq {
		t.Fatalf("identical audit should not append a checkpoint: before=%+v after=%+v", beforeRepeat, latest)
	}
	if auditCheckpoint := s.Checkpoints.LatestByStep(domain.GlobalScope(), "foundation_audit"); auditCheckpoint == nil || auditCheckpoint.Seq != checkpoint.Seq {
		t.Fatalf("identical audit should reuse the prior audit checkpoint: before=%+v after=%+v", checkpoint, auditCheckpoint)
	}
	if decision := guard(context.Background(), stop); !decision.Allow {
		t.Fatalf("completed audit with deduplicated checkpoint must be allowed: %+v", decision)
	}
	if err := s.Outline.SavePremise("用户手动保存的新前提"); err != nil {
		t.Fatal(err)
	}
	if err := s.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.PlanningRevisionPending = true
		w.PlanningManualEdit = true
		w.PlanningManualAuditFingerprint = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	guard = NewArchitectStopGuard(s, nil)
	if decision := guard(context.Background(), stop); decision.Allow {
		t.Fatal("manual audit cannot stop before reporting")
	}
	fingerprint, err = s.FoundationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	args, _ = json.Marshal(domain.FoundationAudit{Fingerprint: fingerprint, Ready: false, Summary: "手动规划存在冲突", Issues: []domain.FoundationAuditIssue{{Artifact: "outline", Description: "章节与第一阶段原则冲突", Evidence: "用户手改了人物动机"}}})
	result, err := auditTool.Execute(context.Background(), args)
	if err != nil || !strings.Contains(string(result), `"user_review_required":true`) {
		t.Fatalf("manual conflict result: %s %v", result, err)
	}
	if decision := guard(context.Background(), stop); !decision.Allow {
		t.Fatalf("a manual conflict report must return control to the user: %+v", decision)
	}
	meta, _ := s.RunMeta.Load()
	if !meta.Workbench.PlanningRevisionPending {
		t.Fatal("a conflict report must not approve the manual plan")
	}
	if err := s.Outline.SavePremise("用户又改了规划"); err != nil {
		t.Fatal(err)
	}
	if decision := guard(context.Background(), stop); decision.Allow {
		t.Fatal("a stale manual audit must not satisfy a new plan")
	}
}

func TestArchitectStopGuardTrustsSuccessfulTerminalTool(t *testing.T) {
	s := newTestStore(t)
	guard := NewArchitectStopGuard(s, nil)
	stop := agentcore.StopInfo{TurnIndex: 1, Message: agentcore.Message{StopReason: agentcore.StopReasonStop}}
	stop.Trigger = agentcore.StopTriggerEndTurn
	if decision := guard(context.Background(), stop); decision.Allow {
		t.Fatal("plain end_turn without a successful tool must remain blocked")
	}
	stop.Trigger = agentcore.StopTriggerAfterTool
	if decision := guard(context.Background(), stop); !decision.Allow {
		t.Fatalf("successful terminal tool should be allowed despite deduplicated checkpoint: %+v", decision)
	}
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s := store.NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return s
}

// TestSubAgentGuard_HardStopReasonEscalatesImmediately 验证：模型返回
// safety / content_filter 这类不可恢复的 provider 端拒答时，子代理 StopGuard
// 必须立即 Escalate 而不是注入催促消息。
//
// 历史背景：实测 hy3-preview:free 写第 2 章时连续 8 次 stop_reason='safety'
// 拒答；旧逻辑反复注入"必须 commit"，模型继续 safety，攒到 3 次 block 才 escalate，
// 之后 Engine 又重跑 writer 总共 3 次。每次都是新的 SubAgent → 缓存
// 前缀全部冷启动。修复后第一次 safety 立即 escalate，Engine 可直接按不可恢复错误暂停。
//
// 注意只测 safety / content_filter：StopReasonError / StopReasonAborted 走
// agentcore loop.go 直接终止 run 的分支，根本不会调用 StopGuard，列进来反而
// 引入死代码。
func TestSubAgentGuard_HardStopReasonEscalatesImmediately(t *testing.T) {
	cases := []agentcore.StopReason{
		agentcore.StopReason("safety"),
		agentcore.StopReason("content_filter"),
	}
	for _, sr := range cases {
		t.Run(string(sr), func(t *testing.T) {
			s := newTestStore(t)
			guard := NewWriterStopGuard(s, nil)
			info := agentcore.StopInfo{
				TurnIndex: 1,
				Message:   agentcore.Message{StopReason: sr},
			}
			d := guard(context.Background(), info)
			if !d.Escalate {
				t.Fatalf("stop_reason=%q must escalate immediately, got %#v", sr, d)
			}
			if d.InjectMessage != "" {
				t.Fatalf("stop_reason=%q must not inject any message, got %q", sr, d.InjectMessage)
			}
		})
	}
}

// TestSubAgentGuard_NormalStopStillBlocks 确保对正常 stop_reason 的拦截行为
// 不受硬错误旁路的影响——LLM 自停且没 commit 时仍然要催。
func TestSubAgentGuard_NormalStopStillBlocks(t *testing.T) {
	s := newTestStore(t)
	guard := NewWriterStopGuard(s, nil)
	info := agentcore.StopInfo{
		TurnIndex: 1,
		Message:   agentcore.Message{StopReason: agentcore.StopReasonStop},
	}
	d := guard(context.Background(), info)
	if d.Escalate {
		t.Fatal("normal stop must not escalate on first block")
	}
	if d.Allow {
		t.Fatal("normal stop must be blocked when no commit checkpoint exists")
	}
	if d.InjectMessage == "" {
		t.Fatal("normal stop must inject a follow-up message")
	}
}

// TestSubAgentGuard_ProgressBetweenBlocksResetsCounter 验证：两次拦截之间出现过
// 新 checkpoint（模型被催后重新 draft 等）时 consecutive 重置——升级只惩罚毫无
// 产物的连续空转，遵循"有进展即重置"语义（issue #75）。
func TestSubAgentGuard_ProgressBetweenBlocksResetsCounter(t *testing.T) {
	s := newTestStore(t)
	guard := NewWriterStopGuard(s, nil)
	normalStop := agentcore.StopInfo{TurnIndex: 1, Message: agentcore.Message{StopReason: agentcore.StopReasonStop}}

	// 拦截 → 落盘新草稿（有进展）→ 再拦截：往复超过阈值也不得升级。
	for i := 0; i < subagentMaxConsecutiveBlocks+2; i++ {
		if d := guard(context.Background(), normalStop); d.Escalate {
			t.Fatalf("escalated at block %d despite progress between blocks", i)
		}
		if _, err := s.Checkpoints.Append(domain.ChapterScope(1), "draft", "drafts/01.draft.md", fmt.Sprintf("d%d", i)); err != nil {
			t.Fatalf("append draft: %v", err)
		}
	}
	// 停止进展：连续空转拦截攒满阈值后才升级。
	for i := 0; i < subagentMaxConsecutiveBlocks; i++ {
		if d := guard(context.Background(), normalStop); d.Escalate {
			t.Fatalf("escalated too early at idle block %d", i)
		}
	}
	if d := guard(context.Background(), normalStop); !d.Escalate {
		t.Fatal("expected escalate after consecutive no-progress blocks")
	}
}

// TestWriterStopGuard_StageAwareBlockMessage 验证催促消息按已落盘 step 组装：
// 静态的"必须调 commit_chapter"在前置步骤缺失或 commit 报错时会误导模型（issue #75）。
func TestWriterStopGuard_StageAwareBlockMessage(t *testing.T) {
	s := newTestStore(t)
	guard := NewWriterStopGuard(s, nil)
	normalStop := agentcore.StopInfo{TurnIndex: 1, Message: agentcore.Message{StopReason: agentcore.StopReasonStop}}

	// 无任何产物：应引导完整流程，而不是直接催 commit。
	d := guard(context.Background(), normalStop)
	if !strings.Contains(d.InjectMessage, "draft_chapter") || !strings.Contains(d.InjectMessage, "plan_chapter") {
		t.Fatalf("no-draft message should walk through the protocol, got %q", d.InjectMessage)
	}

	// 草稿已落盘：应催 check_consistency 收尾。
	if _, err := s.Checkpoints.Append(domain.ChapterScope(1), "draft", "drafts/01.draft.md", "d1"); err != nil {
		t.Fatalf("append draft: %v", err)
	}
	d = guard(context.Background(), normalStop)
	if !strings.Contains(d.InjectMessage, "check_consistency") {
		t.Fatalf("draft-only message should point to check_consistency, got %q", d.InjectMessage)
	}

	// 草稿+一致性检查已完成：只差提交，且要为 commit 报错场景留出路。
	if _, err := s.Checkpoints.Append(domain.ChapterScope(1), "consistency_check", "meta/checks/01.json", "c1"); err != nil {
		t.Fatalf("append consistency_check: %v", err)
	}
	d = guard(context.Background(), normalStop)
	if !strings.Contains(d.InjectMessage, "commit_chapter") || !strings.Contains(d.InjectMessage, "错误") {
		t.Fatalf("ready-to-commit message should mention commit and error handling, got %q", d.InjectMessage)
	}
}

// TestSubAgentGuard_BlockHookReceivesAgentAndReason 验证审计回调收到正确的
// agent 名与 reason 序列——Host 靠它把拦截浮出到 TUI。
func TestSubAgentGuard_BlockHookReceivesAgentAndReason(t *testing.T) {
	s := newTestStore(t)
	var agents, reasons []string
	guard := NewWriterStopGuard(s, func(agent, reason string, _ int32) {
		agents = append(agents, agent)
		reasons = append(reasons, reason)
	})
	normalStop := agentcore.StopInfo{TurnIndex: 1, Message: agentcore.Message{StopReason: agentcore.StopReasonStop}}

	for i := 0; i < subagentMaxConsecutiveBlocks+1; i++ {
		guard(context.Background(), normalStop)
	}
	if len(reasons) != subagentMaxConsecutiveBlocks+1 {
		t.Fatalf("hook called %d times, want %d", len(reasons), subagentMaxConsecutiveBlocks+1)
	}
	for i, agent := range agents {
		if agent != "writer" {
			t.Fatalf("hook call %d: agent = %q, want writer", i, agent)
		}
	}
	for i := 0; i < subagentMaxConsecutiveBlocks; i++ {
		if reasons[i] != "blocked" {
			t.Fatalf("reason[%d] = %q, want blocked", i, reasons[i])
		}
	}
	if last := reasons[len(reasons)-1]; last != "escalated" {
		t.Fatalf("last reason = %q, want escalated", last)
	}

	// hard_stop 也要上报。
	var hardReasons []string
	hardGuard := NewWriterStopGuard(s, func(_, reason string, _ int32) {
		hardReasons = append(hardReasons, reason)
	})
	hardGuard(context.Background(), agentcore.StopInfo{
		TurnIndex: 1,
		Message:   agentcore.Message{StopReason: agentcore.StopReason("safety")},
	})
	if len(hardReasons) != 1 || hardReasons[0] != "hard_stop" {
		t.Fatalf("hard stop hook reasons = %v, want [hard_stop]", hardReasons)
	}
}

// TestEditorStopGuard_TaskAware 验证任务感知：被派生成弧摘要时，仅 save_review（复核）
// 不算完成，必须产出 arc_summary 才放行——封堵卷中骨架弧死循环的起点 Defect C。
func TestEditorStopGuard_TaskAware(t *testing.T) {
	normalStop := agentcore.StopInfo{TurnIndex: 1, Message: agentcore.Message{StopReason: agentcore.StopReasonStop}}

	// 摘要任务 + 只存了 review → 必须阻拦（review 不满足 arc_summary 要求）。
	t.Run("summary task blocks on review only", func(t *testing.T) {
		s := newTestStore(t)
		guard := NewEditorStopGuard(s, "生成第 5 卷第 1 弧摘要（save_arc_summary）", nil)
		if _, err := s.Checkpoints.Append(domain.ArcScope(5, 1), "review", "reviews/v05a01.json", "d1"); err != nil {
			t.Fatalf("append review: %v", err)
		}
		if d := guard(context.Background(), normalStop); d.Allow {
			t.Fatal("summary task must NOT be satisfied by a review checkpoint")
		}
	})

	// 摘要任务 + 已存 arc_summary → 放行。
	t.Run("summary task allows on arc_summary", func(t *testing.T) {
		s := newTestStore(t)
		guard := NewEditorStopGuard(s, "生成第 5 卷第 1 弧摘要（save_arc_summary）", nil)
		if _, err := s.Checkpoints.Append(domain.ArcScope(5, 1), "arc_summary", "summaries/arc-v05a01.json", "d1"); err != nil {
			t.Fatalf("append arc_summary: %v", err)
		}
		if d := guard(context.Background(), normalStop); !d.Allow {
			t.Fatal("summary task must be satisfied by an arc_summary checkpoint")
		}
	})

	// 评审任务 + 存了 review → 放行（默认宽松行为不变）。
	t.Run("review task allows on review", func(t *testing.T) {
		s := newTestStore(t)
		guard := NewEditorStopGuard(s, "对第 5 卷第 1 弧做弧级评审（scope=arc）", nil)
		if _, err := s.Checkpoints.Append(domain.ArcScope(5, 1), "review", "reviews/v05a01.json", "d1"); err != nil {
			t.Fatalf("append review: %v", err)
		}
		if d := guard(context.Background(), normalStop); !d.Allow {
			t.Fatal("review task must be satisfied by a review checkpoint")
		}
	})
}
