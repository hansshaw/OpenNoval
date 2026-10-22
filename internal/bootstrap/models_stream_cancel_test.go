package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

func TestFailoverStreamCancellationUnblocksFullOutputBuffer(t *testing.T) {
	for _, terminal := range []agentcore.StreamEventType{agentcore.StreamEventTextDelta, agentcore.StreamEventDone, agentcore.StreamEventError} {
		t.Run(string(terminal), func(t *testing.T) {
			const bufferSize = 100
			source := make(chan agentcore.StreamEvent, bufferSize+1)
			for index := 0; index < bufferSize; index++ {
				source <- agentcore.StreamEvent{Type: agentcore.StreamEventTextDelta, Delta: "preview"}
			}
			source <- agentcore.StreamEvent{Type: terminal, Err: errors.New("terminal error")}
			close(source)
			inner := &streamPolicyModel{stream: func(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
				return source, nil
			}}
			model := &failoverModel{primary: NewSwappableModel("test", "model", inner, nil)}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			out, err := model.GenerateStream(ctx, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			for len(out) != bufferSize || len(source) != 0 {
				select {
				case <-deadline.C:
					t.Fatal("producer did not reach the full output buffer")
				case <-tick.C:
				}
			}
			cancel()
			// Leave the output full while cancellation wakes the producer. A
			// plain channel send would stay blocked until a consumer drained it.
			settle := time.NewTimer(10 * time.Millisecond)
			defer settle.Stop()
			<-settle.C
			count := 0
			for range out {
				count++
			}
			if count != bufferSize {
				t.Fatalf("canceled producer remained blocked and later sent another event: got %d, want %d", count, bufferSize)
			}
		})
	}
}
