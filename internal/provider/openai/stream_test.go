package openai

import (
	"io"
	"strings"
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

func TestParseSSETextOnly(t *testing.T) {
	body := strings.NewReader(`data: {"choices":[{"delta":{"content":"Hello"}}]}

data: {"choices":[{"delta":{"content":" world"}}]}
data: [DONE]
`)
	out := make(chan protocol.ProviderEvent, 16)
	go parseSSE(io.NopCloser(body), out)

	var text string
	var done bool
	for ev := range out {
		switch e := ev.(type) {
		case protocol.PEText:
			text += e.Text
		case protocol.PEDone:
			done = true
		}
	}
	if text != "Hello world" {
		t.Fatalf("unexpected text: %q", text)
	}
	if !done {
		t.Fatal("expected done")
	}
}

func TestParseSSEToolCallFragmented(t *testing.T) {
	body := strings.NewReader(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\""}}]}}]}
data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"path\": \"x.go"}}]}}]}
data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]}}]}
data: [DONE]
`)
	out := make(chan protocol.ProviderEvent, 16)
	go parseSSE(io.NopCloser(body), out)

	var calls []protocol.ToolUse
	for ev := range out {
		if e, ok := ev.(protocol.PEToolCall); ok {
			calls = append(calls, e.ToolUse)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Name != "read" || calls[0].Args["path"] != "x.go" {
		t.Fatalf("unexpected call: %+v", calls[0])
	}
}
