package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

func TestParseSSETextOnly(t *testing.T) {
	body := `event: message_start
data: {"type":"message_start","message":{"id":"msg","model":"claude-3","content":[],"usage":{"input_tokens":10,"output_tokens":1,"cache_creation_input_tokens":3,"cache_read_input_tokens":5}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"type":"stop_reason","stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}
`
	out := make(chan protocol.ProviderEvent, 16)
	go parseSSE(io.NopCloser(strings.NewReader(body)), out)

	var text string
	var usage protocol.Usage
	var done bool
	for ev := range out {
		switch e := ev.(type) {
		case protocol.PEText:
			text += e.Text
		case protocol.PEUsage:
			usage = e.Usage
		case protocol.PEDone:
			done = true
		case protocol.PEError:
			t.Fatalf("unexpected error: %v", e.Err)
		}
	}
	if text != "Hello world" {
		t.Fatalf("expected text 'Hello world', got %q", text)
	}
	if usage.PromptTokens != 18 || usage.CompletionTokens != 2 || usage.CachedPromptTokens != 5 || usage.CacheCreationTokens != 3 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	if !done {
		t.Fatal("expected PEDone")
	}
}

func TestListModelsPaginates(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("X-Api-Key"); got != "test-key" {
			t.Fatalf("X-Api-Key = %q", got)
		}
		if got := r.Header.Get("Anthropic-Version"); got != "2023-06-01" {
			t.Fatalf("Anthropic-Version = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after_id") == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-b"}],"has_more":true,"last_id":"claude-b"}`))
			return
		}
		if got := r.URL.Query().Get("after_id"); got != "claude-b" {
			t.Fatalf("after_id = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-a"}],"has_more":false,"last_id":"claude-a"}`))
	}))
	defer server.Close()

	models, err := New(server.URL, "test-key", Config{}).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if got, want := strings.Join(models, ","), "claude-a,claude-b"; got != want {
		t.Fatalf("models = %q, want %q", got, want)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestParseSSEToolUse(t *testing.T) {
	body := `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"echo hi"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}
`
	out := make(chan protocol.ProviderEvent, 16)
	go parseSSE(io.NopCloser(strings.NewReader(body)), out)

	var calls []protocol.ToolUse
	for ev := range out {
		switch e := ev.(type) {
		case protocol.PEToolCall:
			calls = append(calls, e.ToolUse)
		case protocol.PEError:
			t.Fatalf("unexpected error: %v", e.Err)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "bash" || calls[0].Args["command"] != "echo hi" {
		t.Fatalf("unexpected tool call: %+v", calls[0])
	}
}
