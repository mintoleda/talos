package session

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

func txWithBigReadResult(t *testing.T) *Transcript {
	t.Helper()
	tx, err := Create(filepath.Join(t.TempDir(), "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	msgs := []protocol.Message{
		protocol.TextMessage(protocol.RoleUser, "look at the file"),
		{Role: protocol.RoleAssistant, Content: []protocol.ContentBlock{
			{Type: protocol.BlockToolUse, ToolUse: &protocol.ToolUse{ID: "id1", Name: "read", Args: map[string]any{"path": "big.go"}}},
		}},
		{Role: protocol.RoleTool, Content: []protocol.ContentBlock{
			{Type: protocol.BlockToolResult, ToolResult: &protocol.ToolResult{ToolUseID: "id1", Content: strings.Repeat("contents\n", 5000)}},
		}},
		protocol.TextMessage(protocol.RoleAssistant, "done"),
		protocol.TextMessage(protocol.RoleUser, "thanks"),
		protocol.TextMessage(protocol.RoleAssistant, "sure"),
		protocol.TextMessage(protocol.RoleUser, "next"),
		protocol.TextMessage(protocol.RoleAssistant, "ok"),
		protocol.TextMessage(protocol.RoleUser, "and again"),
		protocol.TextMessage(protocol.RoleAssistant, "sure"),
		protocol.TextMessage(protocol.RoleUser, "last"),
		protocol.TextMessage(protocol.RoleAssistant, "done"),
	}
	for _, m := range msgs {
		if err := tx.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	return tx
}

func snippedContent(tx *Transcript) string {
	for i, n := 0, tx.FrozenLen(); i < n; i++ {
		fm := tx.FrozenAt(i)
		for _, b := range fm.Msg.Content {
			if b.Type == protocol.BlockToolResult && b.ToolResult != nil && strings.HasPrefix(b.ToolResult.Content, snipMarker) {
				return b.ToolResult.Content
			}
		}
	}
	return ""
}

func TestSnipPruneNamesTheTool(t *testing.T) {
	tx := txWithBigReadResult(t)
	c := NewCompactor(DropSummarizer{})

	modified, err := c.SnipPrune(tx, 700, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("expected snip")
	}
	got := snippedContent(tx)
	if got == "" {
		t.Fatal("no snipped result found")
	}
	if !strings.HasPrefix(got, snipMarker+"read, ") {
		t.Fatalf("tool name not resolved: %q", got[:60])
	}
}

func TestSnipUsesReadToolBudget(t *testing.T) {
	tx := txWithBigReadResult(t)
	c := NewCompactor(DropSummarizer{})

	if _, err := c.SnipPrune(tx, 700, 1000); err != nil {
		t.Fatal(err)
	}
	got := snippedContent(tx)
	body := strings.TrimPrefix(got, snipMarker)
	if i := strings.Index(body, "; "); i >= 0 {
		body = body[i+2:]
	}
	if len(body) < 4096 {
		t.Fatalf("read tool got the generic 2048-byte head, kept %d bytes", len(body))
	}
}

func TestMaybeCompactReportsReclaimVsFold(t *testing.T) {
	ctx := context.Background()

	tx := txWithBigReadResult(t)
	c := NewCompactor(DropSummarizer{})
	c.Threshold = 0.8
	compacted, err := c.MaybeCompact(ctx, tx, 3000, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if !compacted || c.LastAction() != ActionReclaim {
		t.Fatalf("expected reclaim, got compacted=%v action=%v", compacted, c.LastAction())
	}

	tx2 := txWithBigReadResult(t)
	c2 := NewCompactor(DropSummarizer{})
	c2.Threshold = 0.0
	c2.TailBudgetTokens = 1
	c2.KeepTail = 1
	c2.FoldMinTokens = 1
	compacted, err = c2.MaybeCompact(ctx, tx2, 1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !compacted || c2.LastAction() != ActionFold {
		t.Fatalf("expected fold, got compacted=%v action=%v", compacted, c2.LastAction())
	}
}
