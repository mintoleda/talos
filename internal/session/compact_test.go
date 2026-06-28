package session

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

// helper to build a frozen message with tool_use content blocks.
func frozenToolCall(ids ...string) protocol.FrozenMessage {
	blocks := make([]protocol.ContentBlock, len(ids))
	for i, id := range ids {
		blocks[i] = protocol.ContentBlock{
			Type:    protocol.BlockToolUse,
			ToolUse: &protocol.ToolUse{ID: id, Name: "test_tool"},
		}
	}
	m := protocol.Message{Role: protocol.RoleAssistant, Content: blocks}
	raw, _ := m.MarshalCanonical()
	return protocol.FrozenMessage{Msg: m, Raw: raw}
}

// helper to build a frozen message with tool_result content blocks.
func frozenToolResult(ids ...string) protocol.FrozenMessage {
	blocks := make([]protocol.ContentBlock, len(ids))
	for i, id := range ids {
		blocks[i] = protocol.ContentBlock{
			Type: protocol.BlockToolResult,
			ToolResult: &protocol.ToolResult{ToolUseID: id, Content: "ok"},
		}
	}
	m := protocol.Message{Role: protocol.RoleTool, Content: blocks}
	raw, _ := m.MarshalCanonical()
	return protocol.FrozenMessage{Msg: m, Raw: raw}
}

// helper to build a plain text frozen message.
func frozenText(role protocol.Role, text string) protocol.FrozenMessage {
	m := protocol.TextMessage(role, text)
	raw, _ := m.MarshalCanonical()
	return protocol.FrozenMessage{Msg: m, Raw: raw}
}

func TestAlignedChunk_PureText(t *testing.T) {
	c := NewCompactor(nil)
	c.ChunkSize = 3

	frozen := []protocol.FrozenMessage{
		frozenText(protocol.RoleUser, "hello"),
		frozenText(protocol.RoleAssistant, "hi"),
		frozenText(protocol.RoleUser, "how are you"),
		frozenText(protocol.RoleAssistant, "good"),
		frozenText(protocol.RoleUser, "cool"),
	}

	got := c.alignedChunk(frozen)
	if len(got) != 3 {
		t.Fatalf("pure text: expected chunk size 3, got %d", len(got))
	}
}

func TestAlignedChunk_ToolCallSplit(t *testing.T) {
	// The chunk boundary (size 3) falls between the tool_call and its result.
	// The aligned chunk must extend to include the tool result at position 3.
	c := NewCompactor(nil)
	c.ChunkSize = 3

	frozen := []protocol.FrozenMessage{
		frozenText(protocol.RoleUser, "read f1"),
		frozenText(protocol.RoleAssistant, "ok"),
		frozenToolCall("tc1"),           // position 2 — in chunk
		frozenToolResult("tc1"),          // position 3 — out of chunk but must be included
		frozenText(protocol.RoleUser, "next"),
	}

	got := c.alignedChunk(frozen)
	if len(got) != 4 {
		t.Fatalf("tool-call split: expected chunk size 4, got %d", len(got))
	}
}

func TestAlignedChunk_MultipleToolCalls(t *testing.T) {
	// ChunkSize=3 but the tool_call at position 2 has two results at 3 and 4.
	// Must extend to 5.
	c := NewCompactor(nil)
	c.ChunkSize = 3

	frozen := []protocol.FrozenMessage{
		frozenText(protocol.RoleUser, "do stuff"),
		frozenText(protocol.RoleAssistant, "sure"),
		frozenToolCall("tc1", "tc2"),                      // position 2
		frozenToolResult("tc1"),                              // position 3
		frozenToolResult("tc2"),                              // position 4
		frozenText(protocol.RoleUser, "thanks"),
	}

	got := c.alignedChunk(frozen)
	if len(got) != 5 {
		t.Fatalf("multiple tool calls: expected chunk size 5, got %d", len(got))
	}
}

func TestAlignedChunk_CleanBreak(t *testing.T) {
	// The tool call and its result are both inside the chunk boundary.
	// No extension needed.
	c := NewCompactor(nil)
	c.ChunkSize = 4

	frozen := []protocol.FrozenMessage{
		frozenText(protocol.RoleUser, "read f1"),
		frozenToolCall("tc1"),
		frozenToolResult("tc1"),
		frozenText(protocol.RoleAssistant, "done"),  // complete exchange inside chunk
		frozenText(protocol.RoleUser, "next request"),
	}

	got := c.alignedChunk(frozen)
	if len(got) != 4 {
		t.Fatalf("clean break: expected chunk size 4, got %d", len(got))
	}
}

func TestAlignedChunk_AllMessages(t *testing.T) {
	c := NewCompactor(nil)
	c.ChunkSize = 100

	frozen := []protocol.FrozenMessage{
		frozenText(protocol.RoleUser, "hello"),
		frozenText(protocol.RoleAssistant, "hi"),
	}

	got := c.alignedChunk(frozen)
	if len(got) != 2 {
		t.Fatalf("all messages: expected chunk size 2, got %d", len(got))
	}
}

func TestAlignedChunk_Empty(t *testing.T) {
	c := NewCompactor(nil)
	c.ChunkSize = 5

	got := c.alignedChunk(nil)
	if len(got) != 0 {
		t.Fatalf("empty: expected chunk size 0, got %d", len(got))
	}
}

func TestCompactionDropsOldestAndPrependsSummary(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	tx, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Append 5 messages.
	for i := 0; i < 5; i++ {
		if err := tx.Append(protocol.TextMessage(protocol.RoleUser, "msg")); err != nil {
			t.Fatal(err)
		}
	}
	if len(tx.Frozen()) != 5 {
		t.Fatalf("expected 5 frozen, got %d", len(tx.Frozen()))
	}

	compactor := NewCompactor(SummarizerFunc(func(context.Context, []protocol.Message) (string, error) {
		return "summary", nil
	}))
	compactor.ChunkSize = 2
	compactor.Threshold = 0.0 // force compaction

	compacted, err := compactor.MaybeCompact(ctx, tx, 1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !compacted {
		t.Fatal("expected compaction")
	}
	if len(tx.Frozen()) != 3 {
		t.Fatalf("expected 3 frozen after drop, got %d", len(tx.Frozen()))
	}
	if len(tx.Summaries()) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(tx.Summaries()))
	}

	// Close and reload; the summary should survive.
	if err := tx.Close(); err != nil {
		t.Fatal(err)
	}
	tx2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Close()
	if len(tx2.Summaries()) != 1 {
		t.Fatalf("expected 1 summary after load, got %d", len(tx2.Summaries()))
	}
	if len(tx2.Frozen()) != 3 {
		t.Fatalf("expected 3 frozen after load, got %d", len(tx2.Frozen()))
	}
}
