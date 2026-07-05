package dialogs

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestLoginDialogShowsProviders(t *testing.T) {
	providers := []LoginProvider{
		{Name: "openai", Label: "openai.com", LoggedIn: false},
		{Name: "anthropic", Label: "anthropic.com", LoggedIn: true},
	}
	d := NewLoginDialog(providers)

	view := d.View()
	if !contains(view, "openai") {
		t.Fatal("view should contain 'openai'")
	}
	if !contains(view, "anthropic") {
		t.Fatal("view should contain 'anthropic'")
	}
}

func TestLoginDialogNavigatesProviders(t *testing.T) {
	providers := []LoginProvider{
		{Name: "openai", Label: "openai.com"},
		{Name: "anthropic", Label: "anthropic.com"},
		{Name: "deepseek", Label: "deepseek.com"},
	}
	d := NewLoginDialog(providers)

	// Initial selection is 0.
	if d.selected != 0 {
		t.Fatalf("expected selected=0, got %d", d.selected)
	}

	// Press down.
	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("down")})
	d2 := m.(*LoginDialog)
	if d2.selected != 1 {
		t.Fatalf("expected selected=1 after down, got %d", d2.selected)
	}

	// Press down again.
	m, _ = d2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("down")})
	d3 := m.(*LoginDialog)
	if d3.selected != 2 {
		t.Fatalf("expected selected=2 after second down, got %d", d3.selected)
	}

	// Press down at end — should stay.
	m, _ = d3.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("down")})
	d4 := m.(*LoginDialog)
	if d4.selected != 2 {
		t.Fatalf("expected selected=2 at end, got %d", d4.selected)
	}

	// Press up.
	m, _ = d4.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("up")})
	d5 := m.(*LoginDialog)
	if d5.selected != 1 {
		t.Fatalf("expected selected=1 after up, got %d", d5.selected)
	}
}

func TestLoginDialogSelectsProvider(t *testing.T) {
	providers := []LoginProvider{
		{Name: "openai", Label: "openai.com"},
		{Name: "anthropic", Label: "anthropic.com"},
	}
	d := NewLoginDialog(providers)

	// Enter on first provider should move to key step.
	m, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	d2 := m.(*LoginDialog)

	if d2.step != loginStepKey {
		t.Fatal("expected to advance to key entry step")
	}
	if cmd == nil {
		t.Fatal("expected a command (textinput.Blink)")
	}
}

func TestLoginDialogCancelsWithEscDuringPick(t *testing.T) {
	providers := []LoginProvider{
		{Name: "openai", Label: "openai.com"},
	}
	d := NewLoginDialog(providers)

	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	d2 := m.(*LoginDialog)

	if !d2.done {
		t.Fatal("expected dialog done after esc")
	}
}

func TestLoginDialogKeyEntryBackToPick(t *testing.T) {
	providers := []LoginProvider{
		{Name: "openai", Label: "openai.com"},
		{Name: "anthropic", Label: "anthropic.com"},
	}
	d := NewLoginDialog(providers)

	// Advance to key entry.
	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	d2 := m.(*LoginDialog)

	// Esc should go back to pick step.
	m, _ = d2.Update(tea.KeyMsg{Type: tea.KeyEsc})
	d3 := m.(*LoginDialog)
	if d3.step != loginStepPick {
		t.Fatal("expected to return to pick step after esc")
	}
}

func TestLoginDialogSubmitsKey(t *testing.T) {
	providers := []LoginProvider{
		{Name: "openai", Label: "openai.com"},
	}
	d := NewLoginDialog(providers)

	// Advance to key entry.
	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	d2 := m.(*LoginDialog)

	// Type a key.
	m, _ = d2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sk-abc123")})
	d3 := m.(*LoginDialog)
	// Manually update the textinput's value by sending the key events through.
	// Actually the textinput handles runes, so let's just simulate via enter directly.
	// Set the value manually as textinput would have done.
	d3.keyInput.SetValue("sk-abc123")

	// Submit.
	m, cmd := d3.Update(tea.KeyMsg{Type: tea.KeyEnter})
	d4 := m.(*LoginDialog)

	if !d4.done {
		t.Fatal("expected dialog done after key submit")
	}

	// The command should produce a LoginDoneMsg.
	msg := cmd()
	doneMsg, ok := msg.(LoginDoneMsg)
	if !ok {
		t.Fatalf("expected LoginDoneMsg, got %T", msg)
	}
	if doneMsg.Provider != "openai" {
		t.Fatalf("expected provider=openai, got %q", doneMsg.Provider)
	}
	if doneMsg.Key != "sk-abc123" {
		t.Fatalf("expected key=sk-abc123, got %q", doneMsg.Key)
	}
	if doneMsg.Canceled {
		t.Fatal("expected Canceled=false")
	}
}

func TestLoginDialogEmptyKeyDoesNotSubmit(t *testing.T) {
	providers := []LoginProvider{
		{Name: "openai", Label: "openai.com"},
	}
	d := NewLoginDialog(providers)

	// Advance to key entry.
	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	d2 := m.(*LoginDialog)

	// Submit with empty key.
	m, cmd := d2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	d3 := m.(*LoginDialog)

	if d3.done {
		t.Fatal("expected dialog not done with empty key")
	}
	if cmd != nil {
		t.Fatal("expected nil command for empty key submit")
	}
}

func TestLoginDialogViewStepPick(t *testing.T) {
	providers := []LoginProvider{
		{Name: "openai", Label: "openai.com"},
	}
	d := NewLoginDialog(providers)
	d.width, d.height = 80, 24

	view := d.View()
	if !contains(view, "Login to a provider") {
		t.Fatal("view should contain title")
	}
	if !contains(view, "openai") {
		t.Fatal("view should contain provider name")
	}
}

func TestLoginDialogViewStepKey(t *testing.T) {
	providers := []LoginProvider{
		{Name: "openai", Label: "openai.com"},
	}
	d := NewLoginDialog(providers)
	d.width, d.height = 80, 24

	// Advance to key entry.
	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	d2 := m.(*LoginDialog)

	view := d2.View()
	if !contains(view, "Login to openai") {
		t.Fatal("view should contain 'Login to openai'")
	}
}

func TestLoginDialogCancelsWithCtrlC(t *testing.T) {
	providers := []LoginProvider{
		{Name: "openai", Label: "openai.com"},
	}
	d := NewLoginDialog(providers)

	// Cancel during pick step.
	m, cmd := d.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	d2 := m.(*LoginDialog)

	if !d2.done {
		t.Fatal("expected dialog done after ctrl+c")
	}
	msg := cmd()
	doneMsg, ok := msg.(LoginDoneMsg)
	if !ok || !doneMsg.Canceled {
		t.Fatal("expected Canceled LoginDoneMsg")
	}
}

func TestLoginDialogIsDismissed(t *testing.T) {
	d := NewLoginDialog(nil)
	if d.IsDismissed() {
		t.Fatal("new dialog should not be dismissed")
	}
	d.done = true
	if !d.IsDismissed() {
		t.Fatal("dialog should be dismissed after done")
	}
}
