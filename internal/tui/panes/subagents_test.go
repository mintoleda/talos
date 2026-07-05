package panes

import (
	"testing"

	"github.com/mintoleda/talos/internal/protocol"
)

func TestNewSubagentsIsEmpty(t *testing.T) {
	m := NewSubagents()
	if m.Count() != 0 {
		t.Fatal("expected empty subagents model")
	}
}

func TestSubagentsHandleStarted(t *testing.T) {
	m := NewSubagents()
	m = m.HandleEvent(protocol.SubagentStarted{
		ID:    "1",
		Agent: "scout",
		Task:  "find files",
	})

	if m.Count() != 1 {
		t.Fatalf("expected 1 subagent, got %d", m.Count())
	}
	if m.index["1"] != 0 {
		t.Fatalf("expected index for ID 1 to be 0, got %d", m.index["1"])
	}
	if m.entries[0].status != subRunning {
		t.Fatalf("expected running status, got %v", m.entries[0].status)
	}
}

func TestSubagentsHandleFinished(t *testing.T) {
	m := NewSubagents()
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout", Task: "find"})
	m = m.HandleEvent(protocol.SubagentFinished{
		ID:      "1",
		Agent:   "scout",
		Result:  "done",
		IsError: false,
		Usage:   protocol.SubagentUsage{InputTokens: 100, OutputTokens: 50, Cost: 0.001},
	})

	if m.entries[0].status != subDone {
		t.Fatalf("expected done status, got %v", m.entries[0].status)
	}
	if m.entries[0].usage.InputTokens != 100 {
		t.Fatalf("expected input tokens 100, got %d", m.entries[0].usage.InputTokens)
	}
}

func TestSubagentsHandleFinishedError(t *testing.T) {
	m := NewSubagents()
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout", Task: "find"})
	m = m.HandleEvent(protocol.SubagentFinished{
		ID:      "1",
		IsError: true,
	})

	if m.entries[0].status != subFailed {
		t.Fatalf("expected failed status, got %v", m.entries[0].status)
	}
}

func TestSubagentsHandleStartedDuplicate(t *testing.T) {
	m := NewSubagents()
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout", Task: "a"})
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout", Task: "b"})

	if m.Count() != 1 {
		t.Fatalf("expected 1 subagent (deduplicated), got %d", m.Count())
	}
}

func TestSubagentsHandleEventToolStarted(t *testing.T) {
	m := NewSubagents()
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout", Task: "find"})
	m = m.HandleEvent(protocol.SubagentEvent{
		ID:    "1",
		Agent: "scout",
		Inner: protocol.ToolStarted{ID: "t1", Name: "read", Args: map[string]any{"path": "main.go"}},
	})

	if m.entries[0].tools.Count() != 1 {
		t.Fatalf("expected 1 nested tool, got %d", m.entries[0].tools.Count())
	}
}

func TestSubagentsHandleEventToolFinished(t *testing.T) {
	m := NewSubagents()
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout", Task: "find"})
	m = m.HandleEvent(protocol.SubagentEvent{
		ID:    "1",
		Agent: "scout",
		Inner: protocol.ToolStarted{ID: "t1", Name: "read", Args: map[string]any{"path": "main.go"}},
	})
	m = m.HandleEvent(protocol.SubagentEvent{
		ID:    "1",
		Agent: "scout",
		Inner: protocol.ToolFinished{ID: "t1", Result: protocol.ToolResult{ToolUseID: "t1", Content: "ok"}},
	})

	if m.entries[0].tools.entries[0].status != toolOK {
		t.Fatalf("expected tool OK status, got %v", m.entries[0].tools.entries[0].status)
	}
}

func TestSubagentsHandleEventForUnknownAgent(t *testing.T) {
	m := NewSubagents()
	// No panic.
	m = m.HandleEvent(protocol.SubagentEvent{
		ID:    "nonexistent",
		Agent: "scout",
		Inner: protocol.ToolStarted{ID: "t1", Name: "read"},
	})
}

func TestSubagentsHandleFinishedForUnknown(t *testing.T) {
	m := NewSubagents()
	m = m.HandleEvent(protocol.SubagentFinished{ID: "nonexistent"}) // no-op
	if m.Count() != 0 {
		t.Fatal("expected no entries")
	}
}

func TestSubagentsActiveCount(t *testing.T) {
	m := NewSubagents()
	if m.ActiveCount() != 0 {
		t.Fatal("expected 0 active")
	}
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout"})
	m = m.HandleEvent(protocol.SubagentStarted{ID: "2", Agent: "worker"})
	if m.ActiveCount() != 2 {
		t.Fatalf("expected 2 active, got %d", m.ActiveCount())
	}
	m = m.HandleEvent(protocol.SubagentFinished{ID: "1", IsError: false})
	if m.ActiveCount() != 1 {
		t.Fatalf("expected 1 active after finish, got %d", m.ActiveCount())
	}
}

func TestSubagentsCursorNavigation(t *testing.T) {
	m := NewSubagents()
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "a"})
	m = m.HandleEvent(protocol.SubagentStarted{ID: "2", Agent: "b"})
	m = m.HandleEvent(protocol.SubagentStarted{ID: "3", Agent: "c"})

	m = m.CursorDown()
	if m.cursor != 1 {
		t.Fatalf("expected cursor=1, got %d", m.cursor)
	}
	m = m.CursorDown()
	if m.cursor != 2 {
		t.Fatalf("expected cursor=2, got %d", m.cursor)
	}
	m = m.CursorDown()
	if m.cursor != 2 {
		t.Fatalf("expected cursor=2 at end, got %d", m.cursor)
	}
	m = m.CursorUp()
	if m.cursor != 1 {
		t.Fatalf("expected cursor=1, got %d", m.cursor)
	}
	m = m.CursorUp()
	if m.cursor != 0 {
		t.Fatalf("expected cursor=0, got %d", m.cursor)
	}
	m = m.CursorUp()
	if m.cursor != 0 {
		t.Fatalf("expected cursor=0 at top, got %d", m.cursor)
	}
}

func TestSubagentsSelectedIsRunning(t *testing.T) {
	m := NewSubagents()
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout"})
	if !m.SelectedIsRunning() {
		t.Fatal("expected selected is running")
	}
	m = m.HandleEvent(protocol.SubagentFinished{ID: "1", IsError: false})
	if m.SelectedIsRunning() {
		t.Fatal("expected selected not running after finish")
	}
}

func TestSubagentsSelectedIsRunningEmpty(t *testing.T) {
	m := NewSubagents()
	if m.SelectedIsRunning() {
		t.Fatal("expected false for empty model")
	}
}

func TestSubagentsSelectedID(t *testing.T) {
	m := NewSubagents()
	m = m.HandleEvent(protocol.SubagentStarted{ID: "abc", Agent: "scout"})
	if m.SelectedID() != "abc" {
		t.Fatalf("expected 'abc', got %q", m.SelectedID())
	}
	m = m.CursorDown()
	if m.SelectedID() != "abc" {
		t.Fatalf("expected still 'abc', got %q", m.SelectedID())
	}
}

func TestSubagentsSelectedIDEmpty(t *testing.T) {
	m := NewSubagents()
	if m.SelectedID() != "" {
		t.Fatal("expected empty string for empty model")
	}
}

func TestSubagentsKillConfirmFlow(t *testing.T) {
	m := NewSubagents()
	if m.KillConfirmActive() {
		t.Fatal("expected no kill confirm initially")
	}
	m = m.KillConfirmStart()
	if !m.KillConfirmActive() {
		t.Fatal("expected kill confirm active after start")
	}
	m = m.KillConfirmCancel()
	if m.KillConfirmActive() {
		t.Fatal("expected kill confirm inactive after cancel")
	}
}

func TestSubagentsToggleExpand(t *testing.T) {
	m := NewSubagents()
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout"})

	if m.expanded {
		t.Fatal("expected not expanded initially")
	}
	m = m.ToggleExpand()
	if !m.expanded {
		t.Fatal("expected expanded after toggle")
	}
	m = m.ToggleExpand()
	if m.expanded {
		t.Fatal("expected collapsed after second toggle")
	}
}

func TestSubagentsToggleExpandNoopWhenEmpty(t *testing.T) {
	m := NewSubagents()
	m = m.ToggleExpand() // no panic
}

func TestSubagentsClick(t *testing.T) {
	m := NewSubagents()
	m.SetSize(40, 10)
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout"})
	m = m.HandleEvent(protocol.SubagentStarted{ID: "2", Agent: "worker"})

	m = m.Click(2) // second entry
	if m.cursor != 1 {
		t.Fatalf("expected cursor=1 after click, got %d", m.cursor)
	}
	if !m.expanded {
		t.Fatal("expected expanded after click")
	}
}

func TestSubagentsClickOutOfBounds(t *testing.T) {
	m := NewSubagents()
	m.SetSize(40, 10)
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout"})

	before := m.cursor
	m = m.Click(0) // title row
	if m.cursor != before {
		t.Fatal("click on title should be noop")
	}
}

func TestSubagentsView(t *testing.T) {
	m := NewSubagents()
	m.SetSize(80, 24)
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout", Task: "find files"})

	view := m.View()
	if view == "" {
		t.Fatal("expected non-empty view")
	}
}

func TestSubagentsViewFocused(t *testing.T) {
	m := NewSubagents()
	m.SetSize(80, 24)
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout", Task: "find files"})

	view := m.ViewFocused()
	if view == "" {
		t.Fatal("expected non-empty focused view")
	}
}

func TestSubagentsExpandedView(t *testing.T) {
	m := NewSubagents()
	m.SetSize(80, 24)
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout", Task: "find files"})
	m = m.HandleEvent(protocol.SubagentEvent{
		ID:    "1",
		Agent: "scout",
		Inner: protocol.ToolStarted{ID: "t1", Name: "read", Args: map[string]any{"path": "main.go"}},
	})
	m = m.ToggleExpand()

	view := m.View()
	if view == "" {
		t.Fatal("expected non-empty expanded view")
	}
}

func TestSubagentsExpandedViewNoTools(t *testing.T) {
	m := NewSubagents()
	m.SetSize(80, 24)
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout", Task: "find"})
	m = m.HandleEvent(protocol.SubagentFinished{ID: "1", IsError: false, Usage: protocol.SubagentUsage{
		InputTokens: 100, OutputTokens: 50, Cost: 0.001,
	}})
	m = m.ToggleExpand()

	view := m.View()
	if view == "" {
		t.Fatal("expected non-empty expanded view for finished subagent")
	}
}

func TestSubagentsViewWithKillConfirm(t *testing.T) {
	m := NewSubagents()
	m.SetSize(80, 24)
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout", Task: "find files"})
	m = m.KillConfirmStart()

	view := m.ViewFocused()
	if !contains(view, "kill") {
		t.Fatal("kill confirm view should contain 'kill'")
	}
}

func TestSubagentsSetSize(t *testing.T) {
	m := NewSubagents()
	m.SetSize(50, 20)

	if m.width != 50 || m.height != 20 {
		t.Fatalf("expected width=50 height=20, got %d/%d", m.width, m.height)
	}
}

func TestSubagentsStatsLines(t *testing.T) {
	m := NewSubagents()
	m.SetSize(80, 24)
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout"})
	m = m.HandleEvent(protocol.SubagentFinished{ID: "1", IsError: false, Usage: protocol.SubagentUsage{
		InputTokens: 1000, OutputTokens: 500, Cost: 0.002, ContextTokens: 2000, ContextLimit: 8000,
	}})
	m = m.ToggleExpand()

	view := m.View()
	if view == "" {
		t.Fatal("expected non-empty expanded view with stats")
	}
}

func TestSubagentsStatusGlyph(t *testing.T) {
	m := NewSubagents()
	icon, _ := m.statusGlyph(subRunning)
	if icon == "" {
		t.Fatal("running should have non-empty icon")
	}
	icon, _ = m.statusGlyph(subDone)
	if icon != "✓" {
		t.Fatalf("expected '✓', got %q", icon)
	}
	icon, _ = m.statusGlyph(subFailed)
	if icon != "✗" {
		t.Fatalf("expected '✗', got %q", icon)
	}
}

func TestSubagentsNestedSubagentsAreFlattened(t *testing.T) {
	m := NewSubagents()
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "worker", Task: "do work"})
	m = m.HandleEvent(protocol.SubagentEvent{
		ID:    "1",
		Agent: "worker",
		Inner: protocol.SubagentStarted{ID: "2", Agent: "scout", Task: "nested find"},
	})

	if m.Count() != 2 {
		t.Fatalf("expected 2 entries (flattened), got %d", m.Count())
	}
}

func TestSubagentsNestedSubagentEventsAreFlattened(t *testing.T) {
	m := NewSubagents()
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "worker"})
	m = m.HandleEvent(protocol.SubagentEvent{
		ID:    "1",
		Agent: "worker",
		Inner: protocol.SubagentStarted{ID: "2", Agent: "scout"},
	})

	m = m.HandleEvent(protocol.SubagentEvent{
		ID:    "2",
		Agent: "scout",
		Inner: protocol.ToolStarted{ID: "t1", Name: "read", Args: map[string]any{"path": "main.go"}},
	})

	if m.entries[1].tools.Count() != 1 {
		t.Fatalf("expected 1 nested tool on scout, got %d", m.entries[1].tools.Count())
	}
}

func TestSubagentsViewEmpty(t *testing.T) {
	m := NewSubagents()
	view := m.View()
	if view == "" {
		t.Fatal("expected non-empty view even when empty")
	}
}

func TestHumanCount(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{500, "500"},
		{1500, "1.5k"},
		{1000000, "1.0M"},
		{2500000, "2.5M"},
	}
	for _, tt := range tests {
		got := humanCount(tt.n)
		if got != tt.want {
			t.Errorf("humanCount(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestClipLines(t *testing.T) {
	input := "line1\nline2\nline3\nline4\nline5"
	got := clipLines(input, 3)
	lines := splitLines(got)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
}

func TestClipLinesZero(t *testing.T) {
	if clipLines("test", 0) != "" {
		t.Fatal("expected empty for n=0")
	}
}

func TestSubNameWidth(t *testing.T) {
	entries := []subEntry{
		{agent: "scout"},
		{agent: "researcher-extra"},
	}
	w := subNameWidth(entries)
	if w > 10 {
		t.Fatalf("expected capped at 10, got %d", w)
	}
}

func TestSubNameWidthEmpty(t *testing.T) {
	if w := subNameWidth(nil); w != 0 {
		t.Fatalf("expected 0 for empty, got %d", w)
	}
}

func TestSubagentsSelectedAgent(t *testing.T) {
	m := NewSubagents()
	if m.SelectedAgent() != "" {
		t.Fatal("expected empty for empty model")
	}
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout"})
	if m.SelectedAgent() != "scout" {
		t.Fatalf("expected 'scout', got %q", m.SelectedAgent())
	}
}

func TestSubagentsHandleEventFinishedPreservesTools(t *testing.T) {
	m := NewSubagents()
	m = m.HandleEvent(protocol.SubagentStarted{ID: "1", Agent: "scout", Task: "find"})
	m = m.HandleEvent(protocol.SubagentEvent{
		ID:    "1",
		Agent: "scout",
		Inner: protocol.ToolStarted{ID: "t1", Name: "read", Args: map[string]any{"path": "main.go"}},
	})
	m = m.HandleEvent(protocol.SubagentFinished{ID: "1", IsError: false, Result: "done"})

	if m.entries[0].tools.Count() != 1 {
		t.Fatal("tools should be preserved after finish")
	}
}
