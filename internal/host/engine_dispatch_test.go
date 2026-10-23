package host

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/hansshaw/AInoval-web-ui/internal/arbiter"
	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	storepkg "github.com/hansshaw/AInoval-web-ui/internal/store"
	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/subagent"
)

func dispatchFixture(t *testing.T, model agentcore.ChatModel) (*engine, *storepkg.Store, chan struct{}) {
	t.Helper()
	st := storepkg.NewStore(t.TempDir())
	for _, err := range []error{st.Init(), st.Progress.Init(2), st.Progress.UpdatePhase(domain.PhaseWriting), st.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "一"}, {Chapter: 2, Title: "二"}})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	writer := subagent.Config{Name: "writer", Description: "test", Model: model, SystemPrompt: "test", MaxTurns: 2}
	e, _, done := newTestEngine(t, st, subagent.NewRunner(writer), nil)
	return e, st, done
}

func queueDispatch(t *testing.T, e *engine, st *storepkg.Store, text string) {
	t.Helper()
	if err := e.applyControlOp(context.Background(), controlOp{dispatch: &arbiter.DispatchOp{Agent: "writer", Task: "处理用户修改"}, text: text, facts: mustInterventionFacts(t, st)}); err != nil {
		t.Fatal(err)
	}
}

func requirePendingDispatch(t *testing.T, st *storepkg.Store, want string) {
	t.Helper()
	meta, err := st.RunMeta.Load()
	if err != nil || meta == nil || meta.PendingSteer != want {
		t.Fatalf("pending dispatch = %+v, want %q, err=%v", meta, want, err)
	}
}

func TestEngineAbortRestoresDispatchAlreadyMovedToNext(t *testing.T) {
	e, st, _ := dispatchFixture(t, &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message { return testTextMsg("done") }})
	queueDispatch(t, e, st, "已经转入next的原始要求")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.run(ctx)
	requirePendingDispatch(t, st, "已经转入next的原始要求")
	if e.next != nil || e.nextDispatch != nil {
		t.Fatal("restored dispatch remained in memory and could run twice")
	}
}

func TestEngineAbortRestoresDispatchTakenBeforeWorker(t *testing.T) {
	e, st, done := dispatchFixture(t, &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message { return testTextMsg("done") }})
	entered, release := make(chan struct{}), make(chan struct{})
	e.refresh = func() { close(entered); <-release }
	queueDispatch(t, e, st, "已takeNext但尚未执行")
	if !e.start(nil) {
		t.Fatal("engine start")
	}
	<-entered
	e.abort()
	close(release)
	waitEngineDone(t, done)
	e.wait()
	requirePendingDispatch(t, st, "已takeNext但尚未执行")
}

type cancelDispatchModel struct {
	entered chan struct{}
	once    sync.Once
}

func (m *cancelDispatchModel) Generate(ctx context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	m.once.Do(func() { close(m.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *cancelDispatchModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	_, err := m.Generate(ctx, messages, tools, opts...)
	return nil, err
}

func (*cancelDispatchModel) SupportsTools() bool { return true }

func TestEngineAbortRestoresDispatchFromCanceledWorker(t *testing.T) {
	model := &cancelDispatchModel{entered: make(chan struct{})}
	e, st, done := dispatchFixture(t, model)
	queueDispatch(t, e, st, "模型执行中取消的原始要求")
	if !e.start(nil) {
		t.Fatal("engine start")
	}
	<-model.entered
	e.abort()
	waitEngineDone(t, done)
	e.wait()
	requirePendingDispatch(t, st, "模型执行中取消的原始要求")
}

func TestEngineSuccessfulDispatchIsNotReplayedByConcurrentAbort(t *testing.T) {
	e, st, done := dispatchFixture(t, &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message { return testTextMsg("done") }})
	queueDispatch(t, e, st, "已经完成的原始要求")
	originalEmit := e.observer.emitEv
	e.observer.emitEv = func(ev Event) {
		originalEmit(ev)
		if ev.Category == "DISPATCH" && !ev.FinishedAt.IsZero() && !ev.Failed {
			e.abort()
		}
	}
	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)
	e.wait()
	requirePendingDispatch(t, st, "")
}

func TestEngineRecoveryWaitsForHostPendingClearHandoff(t *testing.T) {
	e, st, done := dispatchFixture(t, &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message { return testTextMsg("done") }})
	const text = "Host尚未清除的入队干预"
	if err := st.RunMeta.SetPendingSteer(text); err != nil {
		t.Fatal(err)
	}
	settled := make(chan struct{})
	e.running = true
	e.pending = []controlOp{{dispatch: &arbiter.DispatchOp{Agent: "writer", Task: "test"}, text: text, settled: settled}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	go e.run(ctx)
	deadline := time.After(5 * time.Second)
	for {
		e.mu.Lock()
		draining := e.draining
		e.mu.Unlock()
		if draining {
			break
		}
		select {
		case <-deadline:
			close(settled)
			t.Fatal("engine did not reach its exit handoff")
		default:
			runtime.Gosched()
		}
	}
	// Simulate the Host clearing its crash-protection record after enqueue.
	if err := st.ClearHandledSteer(); err != nil {
		close(settled)
		t.Fatal(err)
	}
	close(settled)
	waitEngineDone(t, done)
	requirePendingDispatch(t, st, text)
}

func TestEngineRejectsNewDispatchWhileDraining(t *testing.T) {
	e, st, _ := dispatchFixture(t, &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message { return testTextMsg("done") }})
	const text = "收尾期间到达的要求"
	if err := st.RunMeta.SetPendingSteer(text); err != nil {
		t.Fatal(err)
	}
	e.draining = true
	err := e.applyControlOp(context.Background(), controlOp{hold: &arbiter.AdvanceHoldOp{After: domain.AdvanceHoldAfterRewritesDrained}, dispatch: &arbiter.DispatchOp{Agent: "writer", Task: "test"}, text: text, facts: mustInterventionFacts(t, st)})
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("draining dispatch should return a retryable refusal: %v", err)
	}
	if e.next != nil {
		t.Fatal("dispatch was queued after the exit snapshot")
	}
	requirePendingDispatch(t, st, text)
}

func TestEngineDeadlockRerouteKeepsInterventionUntilActualCompletion(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "success"
		if canceled {
			name = "canceled"
		}
		t.Run(name, func(t *testing.T) {
			var model agentcore.ChatModel = &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message { return testTextMsg("done") }}
			blocking := &cancelDispatchModel{entered: make(chan struct{})}
			if canceled {
				model = blocking
			}
			e, st, done := dispatchFixture(t, model)
			const text = "失败裁定改派仍需保留的原始授权"
			queueDispatch(t, e, st, text)
			e.arbiterModel = &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
				return testTextMsg(`{"action":"reroute","dispatch":{"agent":"writer","task":"按裁定改派后的任务执行"},"reason":"恢复处理"}`)
			}}
			var seed sync.Once
			e.refresh = func() {
				seed.Do(func() {
					e.lastKey = instructionKey(e.activeDispatch.instruction)
					e.repeats = deadlockConsultAt - 1
				})
			}
			if !canceled {
				originalEmit := e.observer.emitEv
				e.observer.emitEv = func(ev Event) {
					originalEmit(ev)
					if ev.Category == "DISPATCH" && !ev.FinishedAt.IsZero() && !ev.Failed {
						e.abort()
					}
				}
			}
			if !e.start(nil) {
				t.Fatal("engine start")
			}
			if canceled {
				select {
				case <-blocking.entered:
				case <-time.After(5 * time.Second):
					e.abort()
					t.Fatal("rerouted worker did not start")
				}
				e.abort()
			}
			waitEngineDone(t, done)
			e.wait()
			want := ""
			if canceled {
				want = text
			}
			requirePendingDispatch(t, st, want)
		})
	}
}

func TestEnginePrerequisiteDoesNotConsumePairedDispatchHold(t *testing.T) {
	writerCalls := 0
	writerModel := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		writerCalls++
		return testTextMsg("writer done")
	}}
	e, st, done := dispatchFixture(t, writerModel)
	if err := st.Progress.SetLayered(true); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{Index: 1, Title: "卷", Arcs: []domain.ArcOutline{{Index: 1, Title: "未展开弧", EstimatedChapters: 2}}}}); err != nil {
		t.Fatal(err)
	}
	architectCalls := 0
	architectModel := &scriptedChatModel{fn: func([]agentcore.Message) agentcore.Message {
		architectCalls++
		if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{Index: 1, Title: "卷", Arcs: []domain.ArcOutline{{Index: 1, Title: "展开弧", Chapters: []domain.OutlineEntry{{Chapter: 1, Title: "第一章"}}}}}}); err != nil {
			t.Error(err)
		}
		return testTextMsg("prerequisite done")
	}}
	e.workers = subagent.NewRunner(
		subagent.Config{Name: "writer", Description: "test", Model: writerModel, SystemPrompt: "test", MaxTurns: 2},
		subagent.Config{Name: "architect_long", Description: "test prerequisite", Model: architectModel, SystemPrompt: "test", MaxTurns: 2},
	)
	const text = "先展开必要规划，然后执行用户修改"
	if err := e.applyControlOp(context.Background(), controlOp{hold: &arbiter.AdvanceHoldOp{After: domain.AdvanceHoldAfterRewritesDrained, Reason: "修改后暂停"}, dispatch: &arbiter.DispatchOp{Agent: "writer", Task: "用户修改"}, text: text, facts: mustInterventionFacts(t, st)}); err != nil {
		t.Fatal(err)
	}
	originalEmit := e.observer.emitEv
	e.observer.emitEv = func(ev Event) {
		originalEmit(ev)
		if ev.Agent == "writer" && ev.Category == "DISPATCH" && !ev.FinishedAt.IsZero() && !ev.Failed {
			e.abort()
		}
	}
	if !e.start(nil) {
		t.Fatal("engine start")
	}
	waitEngineDone(t, done)
	e.wait()
	if architectCalls != 1 || writerCalls != 1 {
		t.Fatalf("prerequisite/original dispatch calls = %d/%d, want 1/1", architectCalls, writerCalls)
	}
	requirePendingDispatch(t, st, "")
}
