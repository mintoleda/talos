package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mintoleda/talos/internal/protocol"
)

func TestModelHandlesTextDelta(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24
	m.relayout()

	m2, _ := m.Update(EventMsg{E: protocol.TextDelta{Text: "hello"}})
	m3 := m2.(Model)
	// Chat pane height = terminal height - 3 (rounded prompt box folds status + input).
	// 24 - 3 = 21.
	if m3.chat.Height() != 21 {
		t.Fatalf("expected chat height 21, got %d", m3.chat.Height())
	}
}

func TestModelShowsConfirmDialog(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24
	m.relayout()

	reply := make(chan bool, 1)
	m2, _ := m.Update(EventMsg{E: protocol.PermissionRequested{
		ToolName: "bash",
		Reason:   "dangerous",
		ReplyCh:  reply,
	}})
	m3 := m2.(Model)
	if m3.dialog == nil {
		t.Fatal("expected dialog to be set")
	}
	// Approve with 'y'.
	m4, _ := m3.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m5 := m4.(Model)
	if m5.dialog != nil {
		t.Fatal("expected dialog dismissed")
	}
	select {
	case v := <-reply:
		if !v {
			t.Fatal("expected approved=true")
		}
	default:
		t.Fatal("expected reply on channel")
	}
}

// TestModelReplaysInitialHistory guards the `talos -c` continuation behavior:
// when a session is loaded, its messages must be visible in the chat pane.
// Without this, the model has the previous context but the user sees a blank
// screen and assumes the session was reset.
func TestModelReplaysInitialHistory(t *testing.T) {
	history := []protocol.FrozenMessage{
		{Msg: protocol.TextMessage(protocol.RoleUser, "hello, what is 2+2?")},
		{Msg: protocol.TextMessage(protocol.RoleAssistant, "4")},
		{Msg: protocol.TextMessage(protocol.RoleUser, "and 3+3?")},
		{Msg: protocol.TextMessage(protocol.RoleAssistant, "6")},
	}
	m := NewModel(Config{
		SessionID:      "test",
		Mode:           ModeSingleAgent,
		InitialHistory: history,
	})
	m.width, m.height = 80, 24
	m.relayout()

	// The chat pane should now have 4 entries (one per FrozenMessage).
	if got := m.chat.Len(); got != 4 {
		t.Fatalf("expected 4 chat entries after replay, got %d", got)
	}
}

// TestModelEmptyHistoryLeavesChatEmpty ensures we don't double-render or
// fabricate messages when there's nothing to replay.
func TestModelEmptyHistoryLeavesChatEmpty(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24
	m.relayout()

	if got := m.chat.Len(); got != 0 {
		t.Fatalf("expected empty chat with no history, got %d entries", got)
	}
}
