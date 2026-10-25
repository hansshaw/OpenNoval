package startup

import (
	"testing"

	"github.com/hansshaw/AInoval-web-ui/internal/host"
)

func TestCoCreateSessionTransportsCheckpointWithoutChangingHistory(t *testing.T) {
	session := NewCoCreateSession("最早要求：不写恋爱线")
	checkpoint := &host.CoCreateCheckpoint{CoveredMessages: 1, PrefixHash: "prefix", Summary: "不写恋爱线"}
	session.ApplyReply(host.CoCreateReply{Message: "还有什么要求？", Prompt: "## 边界\n不写恋爱线", Raw: "完整协议回复", Checkpoint: checkpoint})
	session.AppendUser("主角叫林月")
	history := session.History()
	if len(history) != 4 || history[0].Content != "最早要求：不写恋爱线" || history[2].Content != "主角叫林月" || history[3].Checkpoint == nil {
		t.Fatalf("wrong checkpoint transport: %+v", history)
	}
	if len(session.history) != 3 || session.InitialInput() != "最早要求：不写恋爱线" {
		t.Fatal("checkpoint mutated durable conversation")
	}
	session.ApplyReply(host.CoCreateReply{Message: "收到", Prompt: "更新草稿", Raw: "更新协议回复"})
	if got := session.History(); len(got) != 4 || got[len(got)-1].Checkpoint != nil {
		t.Fatalf("invalidated checkpoint retained: %+v", got)
	}
}

func TestCoCreateNewRequirementInvalidatesReadinessAndPreservesDraft(t *testing.T) {
	session := NewCoCreateSession("灵感")
	session.ApplyReply(host.CoCreateReply{Message: "可以规划", Prompt: "上一轮完整草稿", Raw: "上一轮原文", Ready: true})
	session.AppendUser("我改主意了，主角不应复仇。")
	if session.Ready() || session.DraftPrompt() != "上一轮完整草稿" {
		t.Fatal("new requirement can still confirm the old draft, or old draft was destroyed")
	}
}
