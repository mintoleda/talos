package session

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

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

func frozenText(role protocol.Role, text string) protocol.FrozenMessage {
	m := protocol.TextMessage(role, text)
	raw, _ := m.MarshalCanonical()
	return protocol.FrozenMessage{Msg: m, Raw: raw}
}

func TestAlignedChunk_PureText(t *testing.T) {
	c := NewCompactor(nil)
	c.ChunkSize = 3
	c.KeepTail = 1

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
	c.KeepTail = 1

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
	c.KeepTail = 1

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
	c.KeepTail = 1

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

func TestAlignedChunk_NeverConsumesEntireTranscript(t *testing.T) {
	// Even with a huge ChunkSize, the last KeepTail messages are never
	// compacted, so the model always retains the most recent context.
	c := NewCompactor(nil)
	c.ChunkSize = 100
	c.KeepTail = 1

	frozen := []protocol.FrozenMessage{
		frozenText(protocol.RoleUser, "hello"),
		frozenText(protocol.RoleAssistant, "hi"),
	}

	got := c.alignedChunk(frozen)
	if len(got) != 1 {
		t.Fatalf("expected chunk size 1 (tail retained), got %d", len(got))
	}
}

func TestAlignedChunk_KeepTailDefault(t *testing.T) {
	c := NewCompactor(nil)
	c.ChunkSize = 100

	frozen := []protocol.FrozenMessage{
		frozenText(protocol.RoleUser, "q1"),
		frozenText(protocol.RoleAssistant, "a1"),
		frozenText(protocol.RoleUser, "q2"),
		frozenText(protocol.RoleAssistant, "a2"),
	}

	got := c.alignedChunk(frozen)
	if len(got) != 2 {
		t.Fatalf("expected chunk size 2 (KeepTail=2 retained), got %d", len(got))
	}
}

func TestAlignedChunk_ShrinksBackwardWhenTailSplitsPair(t *testing.T) {
	// The tool result sits inside the protected tail, so the boundary cannot
	// extend forward to include it — it must shrink back to a clean break.
	c := NewCompactor(nil)
	c.ChunkSize = 3
	c.KeepTail = 2

	frozen := []protocol.FrozenMessage{
		frozenText(protocol.RoleUser, "read f1"),
		frozenText(protocol.RoleAssistant, "ok"),
		frozenToolCall("tc1"),           // position 2
		frozenToolResult("tc1"),          // position 3 — inside protected tail
		frozenText(protocol.RoleUser, "next"),
	}

	got := c.alignedChunk(frozen)
	if len(got) != 2 {
		t.Fatalf("expected chunk size 2 (shrunk before tool_use), got %d", len(got))
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

func TestExtractSummarizer(t *testing.T) {
	msgs := []protocol.Message{
		protocol.TextMessage(protocol.RoleUser, "fix the jdtls crash"),
		protocol.TextMessage(protocol.RoleAssistant, "Let me check the log.\nSecond line dropped."),
		{Role: protocol.RoleAssistant, Content: []protocol.ContentBlock{{
			Type:    protocol.BlockToolUse,
			ToolUse: &protocol.ToolUse{ID: "r1", Name: "read", Args: map[string]any{"path": "/tmp/lsp.log"}},
		}}},
		{Role: protocol.RoleTool, Content: []protocol.ContentBlock{{
			Type:       protocol.BlockToolResult,
			ToolResult: &protocol.ToolResult{ToolUseID: "r1", Content: strings.Repeat("x", 100000)},
		}}},
	}

	got, err := ExtractSummarizer{}.Summarize(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "user: fix the jdtls crash") {
		t.Fatalf("missing user message: %q", got)
	}
	if !strings.Contains(got, "assistant: Let me check the log.") {
		t.Fatalf("missing assistant first line: %q", got)
	}
	if strings.Contains(got, "Second line") {
		t.Fatalf("assistant text not trimmed to first line: %q", got)
	}
	if !strings.Contains(got, "files touched: /tmp/lsp.log") {
		t.Fatalf("missing files touched: %q", got)
	}
	if strings.Contains(got, "xxxx") {
		t.Fatal("tool result content leaked into summary")
	}
	if len(got) > 8192+len("…") {
		t.Fatalf("summary exceeds total cap: %d bytes", len(got))
	}

	// Deterministic: same input, same output.
	again, _ := ExtractSummarizer{}.Summarize(context.Background(), msgs)
	if got != again {
		t.Fatal("extract summary not deterministic")
	}
}

func TestExtractSummarizerCapsHugeUserMessage(t *testing.T) {
	msgs := []protocol.Message{
		protocol.TextMessage(protocol.RoleUser, strings.Repeat("y", 100000)),
	}
	got, err := ExtractSummarizer{}.Summarize(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 8192+len("…") {
		t.Fatalf("summary exceeds total cap: %d bytes", len(got))
	}
}

func TestCompactionDropsOldestAndPrependsSummary(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	tx, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
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
