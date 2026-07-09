package dialogs

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mintoleda/talos/internal/protocol"
)

func TestConfirmDialogApprovesWithY(t *testing.T) {
	ev := protocol.PermissionRequested{ToolName: "bash", Command: "rm -rf /", Reason: "dangerous"}
	d := NewConfirmDialog(ev)
	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	cd := m.(*ConfirmDialog)
	if !cd.IsDismissed() {
		t.Fatal("expected dismissed")
	}
	if !cd.Approved() {
		t.Fatal("expected approved")
	}
}

func TestConfirmDialogDeniesWithN(t *testing.T) {
	ev := protocol.PermissionRequested{ToolName: "bash", Command: "rm -rf /", Reason: "dangerous"}
	d := NewConfirmDialog(ev)
	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	cd := m.(*ConfirmDialog)
	if !cd.IsDismissed() {
		t.Fatal("expected dismissed")
	}
	if cd.Approved() {
		t.Fatal("expected denied")
	}
}

func TestConfirmDialogDeniesWithEsc(t *testing.T) {
	ev := protocol.PermissionRequested{ToolName: "bash", Reason: "dangerous"}
	d := NewConfirmDialog(ev)
	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	cd := m.(*ConfirmDialog)
	if !cd.IsDismissed() {
		t.Fatal("expected dismissed")
	}
	if cd.Approved() {
		t.Fatal("expected denied")
	}
}

func TestConfirmDialogDeniesWithQ(t *testing.T) {
	ev := protocol.PermissionRequested{ToolName: "bash", Reason: "dangerous"}
	d := NewConfirmDialog(ev)
	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	cd := m.(*ConfirmDialog)
	if !cd.IsDismissed() {
		t.Fatal("expected dismissed")
	}
}

func TestConfirmDialogDeniesWithCapitalN(t *testing.T) {
	ev := protocol.PermissionRequested{ToolName: "bash", Reason: "dangerous"}
	d := NewConfirmDialog(ev)
	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
	cd := m.(*ConfirmDialog)
	if !cd.IsDismissed() {
		t.Fatal("expected dismissed")
	}
}

func TestConfirmDialogStoresSize(t *testing.T) {
	d := NewConfirmDialog(protocol.PermissionRequested{ToolName: "bash", Reason: "test"})
	m, _ := d.WithSize(80, 24).Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	cd := m.(*ConfirmDialog)
	if cd.width != 100 || cd.height != 40 {
		t.Fatalf("size not updated: %dx%d", cd.width, cd.height)
	}
}

func TestConfirmDialogViewContainsToolName(t *testing.T) {
	ev := protocol.PermissionRequested{ToolName: "my-tool", Command: "dangerous-cmd", Reason: "safety check"}
	d := NewConfirmDialog(ev).WithSize(80, 24)
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
