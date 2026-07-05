package dialogs

import (
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSessionPickerStartsLoading(t *testing.T) {
	d := NewSessionPickerDialog(func() ([]SessionEntry, error) {
		return []SessionEntry{{ID: "abc", ModTime: time.Now(), Preview: "hello"}}, nil
	})
	if !d.loading {
		t.Fatal("expected dialog to start loading")
	}
}

func TestSessionPickerShowsLoadingView(t *testing.T) {
	d := NewSessionPickerDialog(nil)
	d.width, d.height = 80, 24

	view := d.View()
	if !contains(view, "loading sessions") {
		t.Fatal("loading view should show loading indicator")
	}
}

func TestSessionPickerLoadsSessions(t *testing.T) {
	d := NewSessionPickerDialog(nil)

	now := time.Now()
	m, _ := d.Update(sessionsLoadedMsg{
		entries: []SessionEntry{
			{ID: "abc", ModTime: now, Preview: "first session"},
			{ID: "def", ModTime: now.Add(-time.Hour), Preview: "second session"},
		},
	})
	d2 := m.(*SessionPickerDialog)

	if d2.loading {
		t.Fatal("expected loading to be false after load")
	}
	if len(d2.entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(d2.entries))
	}
}

func TestSessionPickerNavigation(t *testing.T) {
	d := NewSessionPickerDialog(nil)
	d.loading = false
	d.entries = []SessionEntry{
		{ID: "abc", Preview: "first"},
		{ID: "def", Preview: "second"},
		{ID: "ghi", Preview: "third"},
	}

	// Press down (j variant).
	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	d2 := m.(*SessionPickerDialog)
	if d2.selected != 1 {
		t.Fatalf("expected selected=1 after 'j', got %d", d2.selected)
	}

	// Press up (k variant).
	m, _ = d2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	d3 := m.(*SessionPickerDialog)
	if d3.selected != 0 {
		t.Fatalf("expected selected=0 after 'k', got %d", d3.selected)
	}
}

func TestSessionPickerSelectsSession(t *testing.T) {
	d := NewSessionPickerDialog(nil)
	d.loading = false
	d.entries = []SessionEntry{
		{ID: "abc", Preview: "first"},
		{ID: "def", Preview: "second"},
	}

	m, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	d2 := m.(*SessionPickerDialog)

	if !d2.done {
		t.Fatal("expected dialog done after enter")
	}
	msg := cmd()
	doneMsg, ok := msg.(SessionPickerDoneMsg)
	if !ok {
		t.Fatalf("expected SessionPickerDoneMsg, got %T", msg)
	}
	if doneMsg.ID != "abc" {
		t.Fatalf("expected id=abc, got %q", doneMsg.ID)
	}
}

func TestSessionPickerCancelsWithEsc(t *testing.T) {
	d := NewSessionPickerDialog(nil)
	d.loading = false

	m, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	d2 := m.(*SessionPickerDialog)

	if !d2.done {
		t.Fatal("expected dialog done after esc")
	}
	msg := cmd()
	doneMsg, ok := msg.(SessionPickerDoneMsg)
	if !ok || !doneMsg.Canceled {
		t.Fatal("expected Canceled SessionPickerDoneMsg")
	}
}

func TestSessionPickerCancelsDuringLoad(t *testing.T) {
	d := NewSessionPickerDialog(nil)
	d.loading = true

	m, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	d2 := m.(*SessionPickerDialog)

	if !d2.done {
		t.Fatal("expected dialog done after esc during load")
	}
	msg := cmd()
	doneMsg, ok := msg.(SessionPickerDoneMsg)
	if !ok || !doneMsg.Canceled {
		t.Fatal("expected Canceled SessionPickerDoneMsg")
	}
}

func TestSessionPickerDeleteConfirmation(t *testing.T) {
	var deleted string
	d := NewSessionPickerDialog(nil)
	d.loading = false
	d.entries = []SessionEntry{
		{ID: "abc", Preview: "first"},
		{ID: "def", Preview: "second"},
	}
	d.WithDeleteFn(func(id string) error {
		deleted = id
		return nil
	})

	// Press 'd' to start delete confirmation.
	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	d2 := m.(*SessionPickerDialog)

	if d2.confirmDelete != 0 {
		t.Fatalf("expected confirmDelete=0, got %d", d2.confirmDelete)
	}

	// Confirm with 'y'.
	m, _ = d2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	d3 := m.(*SessionPickerDialog)

	if d3.confirmDelete != -1 {
		t.Fatal("expected confirmDelete reset after y")
	}
	if deleted != "abc" {
		t.Fatalf("expected deleted=abc, got %q", deleted)
	}
}

func TestSessionPickerDeleteCancelWithN(t *testing.T) {
	d := NewSessionPickerDialog(nil)
	d.loading = false
	d.entries = []SessionEntry{{ID: "abc", Preview: "first"}}
	d.confirmDelete = 0

	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	d2 := m.(*SessionPickerDialog)

	if d2.confirmDelete != -1 {
		t.Fatal("expected confirmDelete reset after n")
	}
}

func TestSessionPickerDeleteShowsError(t *testing.T) {
	d := NewSessionPickerDialog(nil)
	d.loading = false
	d.entries = []SessionEntry{{ID: "abc", Preview: "first"}}
	d.WithDeleteFn(func(id string) error {
		return errors.New("permission denied")
	})
	d.confirmDelete = 0

	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	d2 := m.(*SessionPickerDialog)

	if d2.deleteErr != "permission denied" {
		t.Fatalf("expected deleteErr='permission denied', got %q", d2.deleteErr)
	}
}

func TestSessionPickerShowsLoadError(t *testing.T) {
	d := NewSessionPickerDialog(nil)

	m, _ := d.Update(sessionsLoadedMsg{err: errors.New("connection failed")})
	d2 := m.(*SessionPickerDialog)

	if d2.loadErr != "connection failed" {
		t.Fatalf("expected loadErr='connection failed', got %q", d2.loadErr)
	}
	view := d2.View()
	if !contains(view, "connection failed") {
		t.Fatal("view should show error message")
	}
}

func TestSessionPickerShowsNoSessions(t *testing.T) {
	d := NewSessionPickerDialog(nil)
	d.loading = false

	view := d.View()
	if !contains(view, "no sessions found") {
		t.Fatal("view should show 'no sessions found'")
	}
}

func TestSessionPickerDeleteNotAvailableWithoutFn(t *testing.T) {
	d := NewSessionPickerDialog(nil)
	d.loading = false
	d.entries = []SessionEntry{{ID: "abc", Preview: "first"}}

	// Press 'd' — should not enter delete confirmation since deleteFn is nil.
	m, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	d2 := m.(*SessionPickerDialog)

	if d2.confirmDelete != -1 {
		t.Fatal("expected no delete confirmation without deleteFn")
	}
}

func TestSessionPickerViewWithSessions(t *testing.T) {
	d := NewSessionPickerDialog(nil)
	d.width, d.height = 80, 24
	d.loading = false
	d.entries = []SessionEntry{
		{ID: "abc", ModTime: time.Now(), Preview: "hello world"},
	}

	view := d.View()
	if !contains(view, "Resume session") {
		t.Fatal("view should contain title")
	}
	if !contains(view, "hello world") {
		t.Fatal("view should contain session preview")
	}
}

func TestSessionPickerDeleteConfirmationView(t *testing.T) {
	d := NewSessionPickerDialog(nil)
	d.width, d.height = 80, 24
	d.loading = false
	d.entries = []SessionEntry{{ID: "abc", ModTime: time.Now(), Preview: "hello"}}
	d.confirmDelete = 0

	view := d.View()
	if !contains(view, "Delete session abc") {
		t.Fatalf("view should show delete confirmation, got: %s", view)
	}
}

func TestSessionTimeAgo(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{50 * time.Hour, "2d ago"},
	}
	for _, tt := range tests {
		got := sessionTimeAgo(time.Now().Add(-tt.d))
		if got != tt.want {
			t.Errorf("sessionTimeAgo(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
