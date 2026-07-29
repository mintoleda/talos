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

func TestFoldTail_PureText(t *testing.T) {
	c := NewCompactor(nil)
	c.KeepTail = 1

	frozen := []protocol.FrozenMessage{
		frozenText(protocol.RoleUser, "hello"),
		frozenText(protocol.RoleAssistant, "hi"),
		frozenText(protocol.RoleUser, "how are you"),
		frozenText(protocol.RoleAssistant, "good"),
		frozenText(protocol.RoleUser, "cool"),
	}

	// Small tail budget — should fold everything except KeepTail.
	got := c.foldTail(frozen, 10)
	if got <= 0 {
		t.Fatalf("pure text: expected non-zero fold boundary, got %d", got)
	}
	if len(frozen)-got < 1 {
		t.Fatalf("pure text: tail too small, got boundary %d for len %d", got, len(frozen))
	}
}

func TestFoldTail_RespectsKeepTail(t *testing.T) {
	c := NewCompactor(nil)
	c.KeepTail = 1

	frozen := []protocol.FrozenMessage{
		frozenText(protocol.RoleUser, "hello"),
		frozenText(protocol.RoleAssistant, "hi"),
	}

	got := c.foldTail(frozen, 100000)
	if got != 1 {
		t.Fatalf("expected boundary 1 (1 msg kept), got %d", got)
	}
}

func TestFoldTail_RespectsBudget(t *testing.T) {
	c := NewCompactor(nil)
	c.KeepTail = 1

	frozen := []protocol.FrozenMessage{
		frozenText(protocol.RoleUser, "hello"),
		frozenText(protocol.RoleAssistant, "hi there"),
		frozenText(protocol.RoleUser, "how are you today"),
		frozenText(protocol.RoleAssistant, "I am doing well"),
		frozenText(protocol.RoleUser, "great to hear"),
	}

	// Large budget — 4 messages fit in the tail budget.
	got := c.foldTail(frozen, 100000)
	if got <= 0 || len(frozen)-got < 1 {
		t.Fatalf("large budget: expected non-zero fold, got boundary %d (tail=%d)", got, len(frozen)-got)
	}

	// Small budget — only 1 message fits in tail.
	got = c.foldTail(frozen, 5)
	if len(frozen)-got != 1 {
		t.Fatalf("small budget: expected tail=1, got boundary %d (tail=%d)", got, len(frozen)-got)
	}
}

func TestFoldTail_AvoidsSplittingToolPair(t *testing.T) {
	c := NewCompactor(nil)
	c.KeepTail = 1

	frozen := []protocol.FrozenMessage{
		frozenText(protocol.RoleUser, "read f1"),
		frozenText(protocol.RoleAssistant, "ok"),
		frozenToolCall("tc1"),
		frozenToolResult("tc1"),
		frozenText(protocol.RoleUser, "next"),
	}

	// Budget small enough that the tail would naturally start at index 2
	// (splitting the tool-call/result pair). foldTail must extend forward
	// to include the result, so boundary ends at the last foldable index.
	got := c.foldTail(frozen, 20)
	if got != 4 {
		t.Fatalf("expected boundary 4 (pair intact, only 'next' in tail), got %d", got)
	}
}

func TestFoldTail_Empty(t *testing.T) {
	c := NewCompactor(nil)
	got := c.foldTail(nil, 16384)
	if got != 0 {
		t.Fatalf("empty: expected boundary 0, got %d", got)
	}
}

func TestPinMessages(t *testing.T) {
	c := NewCompactor(nil)

	frozen := []protocol.FrozenMessage{
		frozenText(protocol.RoleUser, "short first"),       // should be pinned
		frozenText(protocol.RoleAssistant, "response 1"),
		frozenText(protocol.RoleUser, "another small"),     // should be pinned
		frozenText(protocol.RoleAssistant, "response 2 which is quite long and verbose"),
	}

	pinned, remaining := c.pinMessages(frozen)
	// First small user + third small user = 2 pinned
	if len(pinned) != 2 {
		t.Fatalf("expected 2 pinned, got %d", len(pinned))
	}
	if len(remaining) != 2 {
		t.Fatalf("expected 2 remaining, got %d", len(remaining))
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
	if len(got) > 8192+len("\u2026") {
		t.Fatalf("summary exceeds total cap: %d bytes", len(got))
	}

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
	if len(got) > 8192+len("\u2026") {
		t.Fatalf("summary exceeds total cap: %d bytes", len(got))
	}
}

func TestFoldDropsOldestAndPrependsSummary(t *testing.T) {
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
	compactor.Threshold = 0.0
	compactor.TailBudgetTokens = 1 // keep almost nothing in tail
	compactor.KeepTail = 0

	compacted, err := compactor.MaybeCompact(ctx, tx, 1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !compacted {
		t.Fatal("expected compaction")
	}
	if len(tx.Frozen()) == 5 {
		t.Fatalf("expected fewer frozen after drop, got %d", len(tx.Frozen()))
	}
	if len(tx.Summaries()) == 0 {
		t.Fatalf("expected at least 1 summary, got %d", len(tx.Summaries()))
	}
}
