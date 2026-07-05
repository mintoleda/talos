package dialogs

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mintoleda/talos/internal/models"
)

func TestModelPickerStartsLoading(t *testing.T) {
	d := NewModelPickerDialog("openai", "gpt-4", "", func() ([]models.Entry, error) {
		return []models.Entry{{Provider: "openai", ID: "gpt-4"}, {Provider: "openai", ID: "gpt-5"}}, nil
	})
	if !d.loading {
		t.Fatal("expected dialog to start loading")
	}
	if d.done {
		t.Fatal("expected dialog not done")
	}
}

func TestModelPickerShowsLoadingView(t *testing.T) {
	d := NewModelPickerDialog("openai", "gpt-4", "", nil)
	d.width, d.height = 80, 24

	view := d.View()
	if !contains(view, "fetching models") {
		t.Fatal("loading view should show fetching indicator")
	}
}

func TestModelPickerLoadsModels(t *testing.T) {
	d := NewModelPickerDialog("openai", "gpt-4", "", nil)

	// Simulate models loaded.
	m, _ := d.Update(modelsLoadedMsg{
		entries: []models.Entry{
			{Provider: "openai", ID: "gpt-4"},
			{Provider: "openai", ID: "gpt-5"},
			{Provider: "anthropic", ID: "claude-3"},
		},
	})
	d2 := m.(*ModelPickerDialog)

	if d2.loading {
		t.Fatal("expected loading to be false after load")
	}
	if len(d2.all) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(d2.all))
	}
	if len(d2.filtered) != 3 {
		t.Fatalf("expected 3 filtered entries, got %d", len(d2.filtered))
	}
}

func TestModelPickerHighlightsCurrent(t *testing.T) {
	d := NewModelPickerDialog("openai", "gpt-4", "", nil)

	m, _ := d.Update(modelsLoadedMsg{
		entries: []models.Entry{
			{Provider: "openai", ID: "gpt-5"},
			{Provider: "openai", ID: "gpt-4"},
			{Provider: "anthropic", ID: "claude-3"},
		},
	})
	d2 := m.(*ModelPickerDialog)

	// Should have selected the entry matching "openai/gpt-4".
	for i, e := range d2.filtered {
		if e.Full() == "openai/gpt-4" && d2.selected != i {
			t.Fatalf("expected selected index to point to openai/gpt-4, got selected=%d", d2.selected)
		}
	}
}

func TestModelPickerFiltersByQuery(t *testing.T) {
	d := NewModelPickerDialog("openai", "gpt-4", "", nil)

	// Load models.
	m, _ := d.Update(modelsLoadedMsg{
		entries: []models.Entry{
			{Provider: "openai", ID: "gpt-4"},
			{Provider: "openai", ID: "gpt-5"},
			{Provider: "anthropic", ID: "claude-sonnet"},
		},
	})
	d2 := m.(*ModelPickerDialog)

	// Type "claude" to filter.
	d2.input.SetValue("claude")
	d2.refilter()

	if len(d2.filtered) != 1 {
		t.Fatalf("expected 1 filtered entry for 'claude', got %d", len(d2.filtered))
	}
	if d2.filtered[0].ID != "claude-sonnet" {
		t.Fatalf("expected filtered entry to be claude-sonnet, got %s", d2.filtered[0].ID)
	}
}

func TestModelPickerNavigation(t *testing.T) {
	d := NewModelPickerDialog("openai", "gpt-4", "", nil)

	// Load models.
	m, _ := d.Update(modelsLoadedMsg{
		entries: []models.Entry{
			{Provider: "openai", ID: "gpt-4"},
			{Provider: "openai", ID: "gpt-5"},
			{Provider: "anthropic", ID: "claude-sonnet"},
		},
	})
	d2 := m.(*ModelPickerDialog)
	d2.loading = false

	// Press down.
	m, _ = d2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("down")})
	d3 := m.(*ModelPickerDialog)
	if d3.selected != 1 {
		t.Fatalf("expected selected=1 after down, got %d", d3.selected)
	}

	// Press down again.
	m, _ = d3.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("down")})
	d4 := m.(*ModelPickerDialog)
	if d4.selected != 2 {
		t.Fatalf("expected selected=2 after down, got %d", d4.selected)
	}

	// Press up.
	m, _ = d4.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("up")})
	d5 := m.(*ModelPickerDialog)
	if d5.selected != 1 {
		t.Fatalf("expected selected=1 after up, got %d", d5.selected)
	}
}

func TestModelPickerSelectsModel(t *testing.T) {
	d := NewModelPickerDialog("openai", "gpt-4", "", nil)

	// Load models.
	m, _ := d.Update(modelsLoadedMsg{
		entries: []models.Entry{
			{Provider: "openai", ID: "gpt-4"},
			{Provider: "anthropic", ID: "claude-sonnet"},
		},
	})
	d2 := m.(*ModelPickerDialog)
	d2.loading = false

	// Press enter to select.
	m, cmd := d2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	d3 := m.(*ModelPickerDialog)

	if !d3.done {
		t.Fatal("expected dialog done after enter")
	}
	msg := cmd()
	doneMsg, ok := msg.(ModelPickerDoneMsg)
	if !ok {
		t.Fatalf("expected ModelPickerDoneMsg, got %T", msg)
	}
	if doneMsg.Provider != "openai" || doneMsg.Model != "gpt-4" {
		t.Fatalf("expected openai/gpt-4, got %s/%s", doneMsg.Provider, doneMsg.Model)
	}
}

func TestModelPickerCancelsWithEscAfterLoad(t *testing.T) {
	d := NewModelPickerDialog("openai", "gpt-4", "", nil)
	d.loading = false

	m, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	d2 := m.(*ModelPickerDialog)

	if !d2.done {
		t.Fatal("expected dialog done after esc")
	}
	msg := cmd()
	doneMsg, ok := msg.(ModelPickerDoneMsg)
	if !ok || !doneMsg.Canceled {
		t.Fatal("expected Canceled ModelPickerDoneMsg")
	}
}

func TestModelPickerCancelsWithEscDuringLoad(t *testing.T) {
	d := NewModelPickerDialog("openai", "gpt-4", "", nil)
	d.loading = true

	m, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	d2 := m.(*ModelPickerDialog)

	if !d2.done {
		t.Fatal("expected dialog done after esc during load")
	}
	msg := cmd()
	doneMsg, ok := msg.(ModelPickerDoneMsg)
	if !ok || !doneMsg.Canceled {
		t.Fatal("expected Canceled ModelPickerDoneMsg")
	}
}

func TestModelPickerShowsLoadError(t *testing.T) {
	d := NewModelPickerDialog("openai", "gpt-4", "", nil)

	m, _ := d.Update(modelsLoadedMsg{err: Err("network error")})
	d2 := m.(*ModelPickerDialog)

	if d2.loadErr != "network error" {
		t.Fatalf("expected loadErr='network error', got %q", d2.loadErr)
	}
	view := d2.View()
	if !contains(view, "network error") {
		t.Fatal("view should show error message")
	}
}

func TestModelPickerShowsNoModels(t *testing.T) {
	d := NewModelPickerDialog("openai", "gpt-4", "", nil)
	d.loading = false
	d.filtered = nil

	view := d.View()
	if !contains(view, "no models match") {
		t.Fatal("view should show 'no models match'")
	}
}

func TestModelPickerResetsSelectedOnRefilter(t *testing.T) {
	d := NewModelPickerDialog("openai", "gpt-4", "", nil)

	m, _ := d.Update(modelsLoadedMsg{
		entries: []models.Entry{
			{Provider: "openai", ID: "gpt-4"},
			{Provider: "openai", ID: "gpt-5"},
		},
	})
	d2 := m.(*ModelPickerDialog)
	d2.selected = 5 // out of range

	d2.refilter()
	if d2.selected != 0 {
		t.Fatalf("expected selected reset to 0, got %d", d2.selected)
	}
}

func TestModelPickerInit(t *testing.T) {
	d := NewModelPickerDialog("openai", "gpt-4", "", nil)
	cmd := d.Init()
	if cmd == nil {
		t.Fatal("expected Init to return a command")
	}
}

// Err is a simple error string for test purposes.
type Err string

func (e Err) Error() string { return string(e) }
