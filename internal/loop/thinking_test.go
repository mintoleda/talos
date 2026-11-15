package loop

import (
	"context"
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

func TestThinkingCompletesAtNextContentBoundary(t *testing.T) {
	for _, next := range []protocol.ProviderEvent{
		protocol.PEThinkingDone{},
		protocol.PEText{Text: "answer"},
		protocol.PEToolCall{ToolUse: protocol.ToolUse{ID: "read", Name: "read"}},
		protocol.PEDone{StopReason: "stop"},
	} {
		t.Run(protocolBoundaryName(next), func(t *testing.T) {
			provider := &fakeProvider{batches: [][]protocol.ProviderEvent{{
				protocol.PEThinking{Text: "plan"}, next,
			}}}
			loop := newTestLoop(t, provider, &fakeExecutor{})
			var events []protocol.Event
			_, _, err := loop.streamAndAssemble(context.Background(), protocol.Request{}, func(e protocol.Event) { events = append(events, e) })
			if err != nil {
				t.Fatal(err)
			}
			if len(events) < 2 {
				t.Fatalf("events = %#v", events)
			}
			if _, ok := events[0].(protocol.ThinkingDelta); !ok {
				t.Fatalf("first = %T", events[0])
			}
			if complete, ok := events[1].(protocol.ThinkingBlock); !ok || complete.Text != "plan" {
				t.Fatalf("completion = %#v", events[1])
			}
			count := 0
			for _, e := range events {
				if _, ok := e.(protocol.ThinkingBlock); ok {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("completion count = %d", count)
			}
		})
	}
}

func TestThinkingCompletesEachBlockExactlyOnce(t *testing.T) {
	provider := &fakeProvider{batches: [][]protocol.ProviderEvent{{
		protocol.PEThinking{Text: "first"}, protocol.PEThinkingDone{},
		protocol.PEThinking{Text: "second"}, protocol.PEThinkingDone{},
		protocol.PEText{Text: "answer"}, protocol.PEDone{StopReason: "stop"},
	}}}
	loop := newTestLoop(t, provider, &fakeExecutor{})
	var blocks []string
	_, _, err := loop.streamAndAssemble(context.Background(), protocol.Request{}, func(e protocol.Event) {
		if block, ok := e.(protocol.ThinkingBlock); ok {
			blocks = append(blocks, block.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || blocks[0] != "first" || blocks[1] != "second" {
		t.Fatalf("completed blocks = %#v", blocks)
	}
}

func protocolBoundaryName(e protocol.ProviderEvent) string {
	switch e.(type) {
	case protocol.PEThinkingDone:
		return "explicit completion"
	case protocol.PEText:
		return "text"
	case protocol.PEToolCall:
		return "tool"
	default:
		return "done"
	}
}
