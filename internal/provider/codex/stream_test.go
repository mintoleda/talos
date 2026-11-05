package codex

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

func collect(t *testing.T, sse string) []protocol.ProviderEvent {
	t.Helper()
	out := make(chan protocol.ProviderEvent)
	go parseStream(context.Background(), io.NopCloser(strings.NewReader(sse)), out)
	var evs []protocol.ProviderEvent
	for ev := range out {
		evs = append(evs, ev)
	}
	return evs
}

func TestParseStreamTextReasoningUsage(t *testing.T) {
	sse := `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}

data: {"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"Thinking"}

data: {"type":"response.reasoning_summary_part.done","output_index":0}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"text":"Thinking"}]}}

data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1"}}

data: {"type":"response.output_text.delta","output_index":1,"delta":"Hello"}

data: {"type":"response.output_text.delta","output_index":1,"delta":" world"}

data: {"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"Hello world"}]}}

data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":120,"output_tokens":7,"total_tokens":127,"input_tokens_details":{"cached_tokens":100}}}}

`
	var text, thinking string
	var usage *protocol.Usage
	var done *protocol.PEDone
	for _, ev := range collect(t, sse) {
		switch e := ev.(type) {
		case protocol.PEText:
			text += e.Text
		case protocol.PEThinking:
			thinking += e.Text
		case protocol.PEUsage:
			usage = &e.Usage
		case protocol.PEDone:
			done = &e
		case protocol.PEError:
			t.Fatalf("unexpected error: %v", e.Err)
		}
	}
	if text != "Hello world" {
		t.Fatalf("text = %q", text)
	}
	if thinking != "Thinking\n\n" {
		t.Fatalf("thinking = %q", thinking)
	}
	if usage == nil || usage.PromptTokens != 120 || usage.CompletionTokens != 7 || usage.CachedPromptTokens != 100 {
		t.Fatalf("usage = %+v", usage)
	}
	if done == nil || done.StopReason != "stop" {
		t.Fatalf("done = %+v", done)
	}
}

func TestParseStreamToolCall(t *testing.T) {
	sse := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":""}}

data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":"}

data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"x.go\"}"}

data: {"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"path\":\"x.go\"}"}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"x.go\"}"}}

data: {"type":"response.done","response":{"status":"completed"}}

`
	var calls []protocol.ToolUse
	var stop string
	for _, ev := range collect(t, sse) {
		switch e := ev.(type) {
		case protocol.PEToolCall:
			calls = append(calls, e.ToolUse)
		case protocol.PEDone:
			stop = e.StopReason
		case protocol.PEError:
			t.Fatalf("unexpected error: %v", e.Err)
		}
	}
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "read" || calls[0].Args["path"] != "x.go" {
		t.Fatalf("calls = %+v", calls)
	}
	if stop != "tool_calls" {
		t.Fatalf("stop = %q", stop)
	}
}

func TestParseStreamMessageWithoutDeltas(t *testing.T) {
	// No deltas: the final item text is emitted on output_item.done. The
	// trailing frame has no blank line; EOF terminates it.
	sse := "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}}\r\n\r\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}"
	var text string
	var gotDone bool
	for _, ev := range collect(t, sse) {
		switch e := ev.(type) {
		case protocol.PEText:
			text += e.Text
		case protocol.PEDone:
			gotDone = true
		case protocol.PEError:
			t.Fatalf("unexpected error: %v", e.Err)
		}
	}
	if text != "hi" || !gotDone {
		t.Fatalf("text=%q done=%v", text, gotDone)
	}
}

func TestParseStreamErrors(t *testing.T) {
	cases := map[string]struct{ sse, want string }{
		"error event": {
			`data: {"type":"error","code":"rate_limit_exceeded","message":"slow down"}` + "\n\n",
			"codex error: slow down",
		},
		"nested error": {
			`data: {"type":"error","error":{"code":"server_error"}}` + "\n\n",
			"codex error: server_error",
		},
		"failed": {
			`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"x","message":"boom"}}}` + "\n\n",
			"boom",
		},
		"incomplete": {
			`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}` + "\n\n",
			"content_filter",
		},
		"truncated": {
			`data: {"type":"response.output_text.delta","output_index":0,"delta":"x"}` + "\n\n",
			"ended before a terminal",
		},
		"bad json": {
			"data: {nope\n\n",
			"invalid Codex SSE JSON",
		},
		"unfinished tool": {
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"c","name":"bash"}}` + "\n\n" +
				`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n",
			"unfinished tool call",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			evs := collect(t, c.sse)
			if len(evs) == 0 {
				t.Fatal("no events")
			}
			last, ok := evs[len(evs)-1].(protocol.PEError)
			if !ok {
				t.Fatalf("last event = %#v, want PEError", evs[len(evs)-1])
			}
			if !strings.Contains(last.Err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", last.Err, c.want)
			}
		})
	}
}

func TestParseStreamMaxTokens(t *testing.T) {
	evs := collect(t, `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`+"\n\n")
	if d, ok := evs[len(evs)-1].(protocol.PEDone); !ok || d.StopReason != "length" {
		t.Fatalf("events = %#v", evs)
	}
}

func TestParseStreamStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan protocol.ProviderEvent)
	pr, pw := io.Pipe()
	go parseStream(ctx, pr, out)
	go func() {
		_, _ = pw.Write([]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message"}}` + "\n\n" +
			`data: {"type":"response.output_text.delta","output_index":0,"delta":"a"}` + "\n\n"))
	}()
	// Nobody reads the delta; cancelling must still let the parser exit.
	cancel()
	pw.Close()
	for range out {
	}
}
