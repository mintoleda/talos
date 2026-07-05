package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

func decodeBody(t *testing.T, body []byte) msgRequest {
	t.Helper()
	var mr msgRequest
	if err := json.Unmarshal(body, &mr); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return mr
}

// TestBuildBodyBreakpointOnToolResultMessage verifies that when the last
// transcript message is a tool-result carrier (protocol.RoleTool, which
// protoToAPI renders as wire role "user"), the cache breakpoint still lands
// on its last content block. This gives incremental caching on every
// tool-loop iteration, not just plain user turns.
func TestBuildBodyBreakpointOnToolResultMessage(t *testing.T) {
	req := protocol.Request{
		Model: "claude-x",
		Messages: []protocol.FrozenMessage{
			{Msg: protocol.TextMessage(protocol.RoleUser, "hi")},
			{Msg: protocol.Message{
				Role: protocol.RoleAssistant,
				Content: []protocol.ContentBlock{{
					Type:    protocol.BlockToolUse,
					ToolUse: &protocol.ToolUse{ID: "1", Name: "read", Args: map[string]any{}},
				}},
			}},
			{Msg: protocol.Message{
				Role: protocol.RoleTool,
				Content: []protocol.ContentBlock{{
					Type:       protocol.BlockToolResult,
					ToolResult: &protocol.ToolResult{ToolUseID: "1", Content: "file contents"},
				}},
			}},
		},
	}

	body, err := buildBody(req, Config{})
	if err != nil {
		t.Fatal(err)
	}
	mr := decodeBody(t, body)

	if len(mr.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(mr.Messages))
	}
	last := mr.Messages[2]
	if last.Role != "user" {
		t.Fatalf("expected tool-result message to render as wire role user, got %q", last.Role)
	}
	if len(last.Content) != 1 || last.Content[0].CacheControl == nil {
		t.Fatalf("expected cache_control on the tool_result block, got %+v", last.Content)
	}
	if last.Content[0].Type != "tool_result" {
		t.Fatalf("expected tool_result block, got %q", last.Content[0].Type)
	}

	// The earlier assistant message must not carry a breakpoint.
	mid := mr.Messages[1]
	for _, blk := range mid.Content {
		if blk.CacheControl != nil {
			t.Fatalf("assistant message should not carry a cache breakpoint, got %+v", blk)
		}
	}
}

// TestBuildBodyVolatileMergedIntoLastUserMessage verifies that when the last
// message is wire-role "user" (either a normal user turn or a tool-result
// carrier), Volatile blocks are appended into that same message, after the
// block carrying the cache breakpoint — rather than as a separate trailing
// message that would violate role alternation.
func TestBuildBodyVolatileMergedIntoLastUserMessage(t *testing.T) {
	req := protocol.Request{
		Model: "claude-x",
		Messages: []protocol.FrozenMessage{
			{Msg: protocol.TextMessage(protocol.RoleUser, "hi")},
		},
		Volatile: []protocol.ContentBlock{{Type: protocol.BlockText, Text: "reminder text"}},
	}

	body, err := buildBody(req, Config{})
	if err != nil {
		t.Fatal(err)
	}
	mr := decodeBody(t, body)

	if len(mr.Messages) != 1 {
		t.Fatalf("expected volatile merged into the single existing message, got %d messages", len(mr.Messages))
	}
	msg := mr.Messages[0]
	if len(msg.Content) != 2 {
		t.Fatalf("expected 2 content blocks (original + volatile), got %d: %+v", len(msg.Content), msg.Content)
	}
	// Breakpoint block (the original text) must come first, cache_control set;
	// the volatile block must be appended after it, with no cache_control.
	if msg.Content[0].CacheControl == nil {
		t.Fatalf("expected cache_control on the original (stable) block, got %+v", msg.Content[0])
	}
	if msg.Content[1].CacheControl != nil {
		t.Fatalf("volatile block must not carry cache_control, got %+v", msg.Content[1])
	}
	if msg.Content[1].Text != "reminder text" {
		t.Fatalf("expected volatile text appended, got %q", msg.Content[1].Text)
	}
}

// TestBuildBodyVolatileMergedIntoToolResultMessage exercises the same merge
// path when the last message is a tool-result carrier rather than a plain
// user turn.
func TestBuildBodyVolatileMergedIntoToolResultMessage(t *testing.T) {
	req := protocol.Request{
		Model: "claude-x",
		Messages: []protocol.FrozenMessage{
			{Msg: protocol.Message{
				Role: protocol.RoleTool,
				Content: []protocol.ContentBlock{{
					Type:       protocol.BlockToolResult,
					ToolResult: &protocol.ToolResult{ToolUseID: "1", Content: "result"},
				}},
			}},
		},
		Volatile: []protocol.ContentBlock{{Type: protocol.BlockText, Text: "reminder text"}},
	}

	body, err := buildBody(req, Config{})
	if err != nil {
		t.Fatal(err)
	}
	mr := decodeBody(t, body)

	if len(mr.Messages) != 1 {
		t.Fatalf("expected volatile merged into the tool-result message, got %d messages", len(mr.Messages))
	}
	msg := mr.Messages[0]
	if msg.Role != "user" {
		t.Fatalf("expected wire role user, got %q", msg.Role)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("expected 2 content blocks (tool_result + volatile), got %d: %+v", len(msg.Content), msg.Content)
	}
	if msg.Content[0].Type != "tool_result" || msg.Content[0].CacheControl == nil {
		t.Fatalf("expected cache_control on the tool_result block, got %+v", msg.Content[0])
	}
	if msg.Content[1].Type != "text" || msg.Content[1].Text != "reminder text" {
		t.Fatalf("expected volatile text block appended after tool_result, got %+v", msg.Content[1])
	}
}

// TestBuildBodyVolatileFallsBackWhenLastMessageIsAssistant verifies the
// separate-trailing-message fallback: when the last message is wire-role
// "assistant", merging volatile content into it would violate role
// alternation, so it must be appended as its own user message instead.
func TestBuildBodyVolatileFallsBackWhenLastMessageIsAssistant(t *testing.T) {
	req := protocol.Request{
		Model: "claude-x",
		Messages: []protocol.FrozenMessage{
			{Msg: protocol.TextMessage(protocol.RoleUser, "hi")},
			{Msg: protocol.TextMessage(protocol.RoleAssistant, "hello there")},
		},
		Volatile: []protocol.ContentBlock{{Type: protocol.BlockText, Text: "reminder text"}},
	}

	body, err := buildBody(req, Config{})
	if err != nil {
		t.Fatal(err)
	}
	mr := decodeBody(t, body)

	if len(mr.Messages) != 3 {
		t.Fatalf("expected a separate trailing message, got %d messages", len(mr.Messages))
	}
	last := mr.Messages[2]
	if last.Role != "user" {
		t.Fatalf("expected fallback message wire role user, got %q", last.Role)
	}
	if len(last.Content) != 1 || last.Content[0].Text != "reminder text" {
		t.Fatalf("expected fallback message to carry the volatile text, got %+v", last.Content)
	}

	// The assistant message itself must be unaffected.
	assistantMsg := mr.Messages[1]
	if assistantMsg.Role != "assistant" || len(assistantMsg.Content) != 1 {
		t.Fatalf("assistant message unexpectedly changed: %+v", assistantMsg)
	}
}

// TestBuildBodyVolatileFallsBackWhenNoMessages covers the edge case where
// there are no prior messages at all — volatile content still needs a home.
func TestBuildBodyVolatileFallsBackWhenNoMessages(t *testing.T) {
	req := protocol.Request{
		Model:    "claude-x",
		Volatile: []protocol.ContentBlock{{Type: protocol.BlockText, Text: "reminder text"}},
	}

	body, err := buildBody(req, Config{})
	if err != nil {
		t.Fatal(err)
	}
	mr := decodeBody(t, body)

	if len(mr.Messages) != 1 {
		t.Fatalf("expected a single fallback message, got %d", len(mr.Messages))
	}
	if mr.Messages[0].Role != "user" {
		t.Fatalf("expected fallback message wire role user, got %q", mr.Messages[0].Role)
	}
}

// TestBuildBodyNoVolatileNoChange ensures the absence of Volatile content
// leaves message rendering exactly as before (regression guard).
func TestBuildBodyNoVolatileNoChange(t *testing.T) {
	req := protocol.Request{
		Model: "claude-x",
		Messages: []protocol.FrozenMessage{
			{Msg: protocol.TextMessage(protocol.RoleUser, "hi")},
		},
	}

	body, err := buildBody(req, Config{})
	if err != nil {
		t.Fatal(err)
	}
	mr := decodeBody(t, body)

	if len(mr.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(mr.Messages))
	}
	if len(mr.Messages[0].Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(mr.Messages[0].Content))
	}
	if mr.Messages[0].Content[0].CacheControl == nil {
		t.Fatal("expected cache_control on the sole block")
	}
}
