package panes

import (
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

func TestNewToolsIsEmpty(t *testing.T) {
	tm := NewTools()
	if tm.Count() != 0 {
		t.Fatal("expected empty tools model")
	}
}

func TestToolsAddTool(t *testing.T) {
	tm := NewTools()
	tm = tm.AddTool("1", "read", map[string]any{"path": "main.go"})

	if tm.Count() != 1 {
		t.Fatalf("expected 1 tool, got %d", tm.Count())
	}
}

func TestToolsAddMultipleTools(t *testing.T) {
	tm := NewTools()
	tm = tm.AddTool("1", "read", map[string]any{"path": "a.go"})
	tm = tm.AddTool("2", "bash", map[string]any{"command": "go test"})

	if tm.Count() != 2 {
		t.Fatalf("expected 2 tools, got %d", tm.Count())
	}
}

func TestToolsFinishToolOK(t *testing.T) {
	tm := NewTools()
	tm = tm.AddTool("1", "read", map[string]any{"path": "main.go"})
	tm = tm.FinishTool("1", protocol.ToolResult{ToolUseID: "1", Content: "file content", IsError: false})

	if tm.entries[0].status != toolOK {
		t.Fatalf("expected status ok, got %v", tm.entries[0].status)
	}
	if tm.entries[0].content != "file content" {
		t.Fatalf("expected content 'file content', got %q", tm.entries[0].content)
	}
}

func TestToolsFinishToolError(t *testing.T) {
	tm := NewTools()
	tm = tm.AddTool("1", "read", map[string]any{"path": "main.go"})
	tm = tm.FinishTool("1", protocol.ToolResult{ToolUseID: "1", Content: "error: not found", IsError: true})

	if tm.entries[0].status != toolError {
		t.Fatalf("expected status error, got %v", tm.entries[0].status)
	}
}

func TestToolsFinishUnknownTool(t *testing.T) {
	tm := NewTools()
	// No panic.
	tm = tm.FinishTool("nonexistent", protocol.ToolResult{ToolUseID: "nonexistent", Content: "ok", IsError: false})
}

func TestToolsCursorNavigation(t *testing.T) {
	tm := NewTools()
	tm = tm.AddTool("1", "read", nil)
	tm = tm.AddTool("2", "bash", nil)
	tm = tm.AddTool("3", "write", nil)

	// Initial cursor at 0.
	// Press down twice.
	tm = tm.CursorDown()
	if tm.cursor != 1 {
		t.Fatalf("expected cursor=1, got %d", tm.cursor)
	}
	tm = tm.CursorDown()
	if tm.cursor != 2 {
		t.Fatalf("expected cursor=2, got %d", tm.cursor)
	}
	// Down at end should stay.
	tm = tm.CursorDown()
	if tm.cursor != 2 {
		t.Fatalf("expected cursor=2 at end, got %d", tm.cursor)
	}

	tm = tm.CursorUp()
	if tm.cursor != 1 {
		t.Fatalf("expected cursor=1 after up, got %d", tm.cursor)
	}
	tm = tm.CursorUp()
	if tm.cursor != 0 {
		t.Fatalf("expected cursor=0 after up, got %d", tm.cursor)
	}
	// Up at top should stay.
	tm = tm.CursorUp()
	if tm.cursor != 0 {
		t.Fatalf("expected cursor=0 at top, got %d", tm.cursor)
	}
}

func TestToolsToggleExpand(t *testing.T) {
	tm := NewTools()
	tm = tm.AddTool("1", "read", map[string]any{"path": "main.go"})

	if tm.expanded {
		t.Fatal("expected not expanded initially")
	}

	tm = tm.ToggleExpand()
	if !tm.expanded {
		t.Fatal("expected expanded after toggle")
	}

	tm = tm.ToggleExpand()
	if tm.expanded {
		t.Fatal("expected collapsed after second toggle")
	}
}

func TestToolsToggleExpandNoopOnEmpty(t *testing.T) {
	tm := NewTools()
	// No panic.
	tm = tm.ToggleExpand()
}

func TestToolsToggleExpandCollapsesCursor(t *testing.T) {
	tm := NewTools()
	tm = tm.AddTool("1", "read", nil)
	tm = tm.AddTool("2", "bash", nil)

	// CursorDown should collapse.
	tm = tm.ToggleExpand() // expand
	tm = tm.CursorDown()
	if tm.expanded {
		t.Fatal("expected collapsed after cursor movement")
	}
}

func TestToolsScrollDownExpands(t *testing.T) {
	tm := NewTools()
	tm = tm.AddTool("1", "read", nil)
	tm = tm.AddTool("2", "bash", nil)

	tm = tm.ToggleExpand() // expand cursor 0
	// Scroll down while expanded should scroll viewport.
	tm = tm.ScrollDown(1)
}

func TestToolsScrollDownCollapsed(t *testing.T) {
	tm := NewTools()
	tm = tm.AddTool("1", "read", nil)
	tm = tm.AddTool("2", "bash", nil)

	// Scroll down without expand should move cursor.
	before := tm.cursor
	tm = tm.ScrollDown(1)
	if tm.cursor != before+1 {
		t.Fatalf("expected cursor=%d after scroll, got %d", before+1, tm.cursor)
	}
}

func TestToolsClickSelects(t *testing.T) {
	tm := NewTools()
	tm.SetSize(40, 10)
	tm = tm.AddTool("1", "read", nil)
	tm = tm.AddTool("2", "bash", nil)

	// Click on row 2 (0=title, 1=first entry, 2=second entry).
	tm = tm.Click(2)
	if tm.cursor != 1 {
		t.Fatalf("expected cursor=1 after click, got %d", tm.cursor)
	}
	// Should also toggle expand.
	if !tm.expanded {
		t.Fatal("expected expanded after click")
	}
}

func TestToolsClickOutOfBounds(t *testing.T) {
	tm := NewTools()
	tm.SetSize(40, 10)
	tm = tm.AddTool("1", "read", nil)

	// Click on title row (y=0) — noop.
	before := tm.cursor
	tm = tm.Click(0)
	if tm.cursor != before {
		t.Fatal("click on title should be noop")
	}
}

func TestToolsSetSize(t *testing.T) {
	tm := NewTools()
	tm.SetSize(50, 20)

	if tm.width != 50 || tm.height != 20 {
		t.Fatalf("expected width=50 height=20, got %d/%d", tm.width, tm.height)
	}
}

func TestToolsView(t *testing.T) {
	tm := NewTools()
	tm.SetSize(80, 24)
	tm = tm.AddTool("1", "read", map[string]any{"path": "main.go"})
	tm = tm.FinishTool("1", protocol.ToolResult{ToolUseID: "1", Content: "ok", IsError: false})

	view := tm.View()
	if view == "" {
		t.Fatal("expected non-empty view")
	}
}

func TestToolsViewFocused(t *testing.T) {
	tm := NewTools()
	tm.SetSize(80, 24)
	tm = tm.AddTool("1", "read", map[string]any{"path": "main.go"})

	view := tm.ViewFocused()
	if view == "" {
		t.Fatal("expected non-empty focused view")
	}
}

func TestToolsExpandedView(t *testing.T) {
	tm := NewTools()
	tm.SetSize(80, 24)
	tm = tm.AddTool("1", "read", map[string]any{"path": "main.go"})
	tm = tm.FinishTool("1", protocol.ToolResult{ToolUseID: "1", Content: "line1\nline2\nline3", IsError: false})
	tm = tm.ToggleExpand()

	view := tm.View()
	if view == "" {
		t.Fatal("expected non-empty expanded view")
	}
}

func TestToolsTitleRow(t *testing.T) {
	tm := NewTools()
	tm.SetSize(80, 24)
	tm = tm.AddTool("1", "read", nil)
	tm = tm.AddTool("2", "bash", nil)

	// Test the title row via view.
	view := tm.View()
	if view == "" {
		t.Fatal("expected non-empty view with title")
	}
}
