package dialogs

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mintoleda/talos/internal/protocol"
)

func TestConfirmDialogApprovesWithY(t *testing.T) {
	reply := make(chan bool, 1)
	ev := protocol.PermissionRequested{ToolName: "bash", Command: "rm -rf /", Reason: "dangerous", ReplyCh: reply}
	d := NewConfirmDialog(ev)

	// Press 'y' to approve.
	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	cd := m.(*ConfirmDialog)

	if !cd.IsDismissed() {
		t.Fatal("expected dialog dismissed after 'y'")
	}
	if !cd.Approved() {
		t.Fatal("expected approved=true")
	}
	select {
	case v := <-reply:
		if !v {
			t.Fatal("expected reply channel to receive true")
		}
	default:
		t.Fatal("expected reply on channel")
	}
}

func TestConfirmDialogDeniesWithN(t *testing.T) {
	reply := make(chan bool, 1)
	ev := protocol.PermissionRequested{ToolName: "bash", Command: "rm -rf /", Reason: "dangerous", ReplyCh: reply}
	d := NewConfirmDialog(ev)

	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	cd := m.(*ConfirmDialog)

	if !cd.IsDismissed() {
		t.Fatal("expected dialog dismissed after 'n'")
	}
	if cd.Approved() {
		t.Fatal("expected approved=false")
	}
	select {
	case v := <-reply:
		if v {
			t.Fatal("expected reply channel to receive false")
		}
	default:
		t.Fatal("expected reply on channel")
	}
}

func TestConfirmDialogDeniesWithEsc(t *testing.T) {
	reply := make(chan bool, 1)
	ev := protocol.PermissionRequested{ToolName: "bash", Reason: "dangerous", ReplyCh: reply}
	d := NewConfirmDialog(ev)

	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	cd := m.(*ConfirmDialog)

	if !cd.IsDismissed() {
		t.Fatal("expected dialog dismissed after esc")
	}
	if cd.Approved() {
		t.Fatal("expected approved=false")
	}
}

func TestConfirmDialogDeniesWithQ(t *testing.T) {
	reply := make(chan bool, 1)
	ev := protocol.PermissionRequested{ToolName: "bash", Reason: "dangerous", ReplyCh: reply}
	d := NewConfirmDialog(ev)

	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	cd := m.(*ConfirmDialog)

	if !cd.IsDismissed() {
		t.Fatal("expected dialog dismissed after 'q'")
	}
}

func TestConfirmDialogDeniesWithCapitalN(t *testing.T) {
	reply := make(chan bool, 1)
	ev := protocol.PermissionRequested{ToolName: "bash", Reason: "dangerous", ReplyCh: reply}
	d := NewConfirmDialog(ev)

	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	cd := m.(*ConfirmDialog)

	if !cd.IsDismissed() {
		t.Fatal("expected dialog dismissed after 'N'")
	}
}

func TestConfirmDialogStoresSize(t *testing.T) {
	d := NewConfirmDialog(protocol.PermissionRequested{ToolName: "bash", Reason: "test"})
	m, _ := d.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	cd := m.(*ConfirmDialog)

	if cd.width != 100 || cd.height != 40 {
		t.Fatalf("expected width=100 height=40, got width=%d height=%d", cd.width, cd.height)
	}
}

func TestConfirmDialogViewContainsToolName(t *testing.T) {
	ev := protocol.PermissionRequested{ToolName: "my-tool", Command: "dangerous-cmd", Reason: "safety check"}
	d := NewConfirmDialog(ev)
	d.width, d.height = 80, 24

	view := d.View()
	if !contains(view, "my-tool") {
		t.Fatal("view should contain tool name")
	}
	if !contains(view, "dangerous-cmd") {
		t.Fatal("view should contain command")
	}
	if !contains(view, "safety check") {
		t.Fatal("view should contain reason")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
