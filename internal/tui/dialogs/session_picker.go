package dialogs

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mintoleda/talos/internal/tui/styles"
)

// SessionEntry carries the data shown in the session picker for one session.
type SessionEntry struct {
	ID      string
	ModTime time.Time
	Preview string // truncated last user message
}

// SessionPickerDoneMsg is sent when the user selects or cancels.
type SessionPickerDoneMsg struct {
	ID       string
	Canceled bool
}

// FetchSessionsFunc returns all sessions for the current project.
type FetchSessionsFunc func() ([]SessionEntry, error)

// DeleteSessionFunc removes a session by ID. It should return nil on success.
type DeleteSessionFunc func(id string) error

type sessionsLoadedMsg struct {
	entries []SessionEntry
	err     error
}

// SessionPickerDialog is a full-screen interactive session selector.
type SessionPickerDialog struct {
	entries       []SessionEntry
	selected      int
	spinner       spinner.Model
	loading       bool
	loadErr       string
	width         int
	height        int
	done          bool
	fetch         FetchSessionsFunc
	deleteFn      DeleteSessionFunc
	confirmDelete int // index of session pending deletion, -1 = none
	deleteErr     string
}

func NewSessionPickerDialog(fetch FetchSessionsFunc) *SessionPickerDialog {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return &SessionPickerDialog{
		spinner:       sp,
		loading:       true,
		fetch:         fetch,
		confirmDelete: -1,
	}
}

// WithDeleteFn sets the delete callback for the picker.
func (d *SessionPickerDialog) WithDeleteFn(fn DeleteSessionFunc) *SessionPickerDialog {
	d.deleteFn = fn
	return d
}

func (d *SessionPickerDialog) Init() tea.Cmd {
	return tea.Batch(d.spinner.Tick, d.doFetch())
}

func (d *SessionPickerDialog) doFetch() tea.Cmd {
	f := d.fetch
	return func() tea.Msg {
		if f == nil {
			return sessionsLoadedMsg{}
		}
		entries, err := f()
		return sessionsLoadedMsg{entries: entries, err: err}
	}
}

func (d *SessionPickerDialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width, d.height = msg.Width, msg.Height
		return d, nil

	case spinner.TickMsg:
		if d.loading {
			var cmd tea.Cmd
			d.spinner, cmd = d.spinner.Update(msg)
			return d, cmd
		}

	case sessionsLoadedMsg:
		d.loading = false
		if msg.err != nil {
			d.loadErr = msg.err.Error()
		} else {
			d.entries = msg.entries
		}
		return d, nil

	case tea.KeyMsg:
		if d.loading {
			if msg.String() == "esc" || msg.String() == "ctrl+c" {
				d.done = true
				return d, func() tea.Msg { return SessionPickerDoneMsg{Canceled: true} }
			}
			return d, nil
		}

		// If in delete confirmation mode, only y/n/esc are accepted.
		if d.confirmDelete >= 0 {
			switch msg.String() {
			case "y", "Y":
				id := d.entries[d.confirmDelete].ID
				d.confirmDelete = -1
				d.deleteErr = ""
				if d.deleteFn != nil {
					if err := d.deleteFn(id); err != nil {
						d.deleteErr = err.Error()
					} else {
						// Refresh after deletion.
						return d, d.doFetch()
					}
				}
				return d, nil
			case "n", "N", "esc":
				d.confirmDelete = -1
				return d, nil
			}
			return d, nil
		}

		switch msg.String() {
		case "esc", "ctrl+c":
			d.done = true
			return d, func() tea.Msg { return SessionPickerDoneMsg{Canceled: true} }
		case "enter":
			if len(d.entries) > 0 {
				id := d.entries[d.selected].ID
				d.done = true
				return d, func() tea.Msg { return SessionPickerDoneMsg{ID: id} }
			}
		case "up", "k":
			if len(d.entries) > 0 {
				d.selected = (d.selected - 1 + len(d.entries)) % len(d.entries)
			}
			return d, nil
		case "down", "j":
			if len(d.entries) > 0 {
				d.selected = (d.selected + 1) % len(d.entries)
			}
			return d, nil
		case "d":
			if len(d.entries) > 0 && d.deleteFn != nil {
				d.confirmDelete = d.selected
			}
			return d, nil
		}
	}
	return d, nil
}

func sessionTimeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func (d *SessionPickerDialog) View() string {
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))

	var sb strings.Builder

	sb.WriteString(accent.Render("  Resume session") + "\n\n")

	const maxVisible = 10
	switch {
	case d.loading:
		sb.WriteString("  " + d.spinner.View() + " loading sessions…")
	case d.loadErr != "":
		sb.WriteString(errStyle.Render("  error: " + d.loadErr))
	case len(d.entries) == 0:
		sb.WriteString(muted.Render("  no sessions found"))
	default:
		n := len(d.entries)
		startIdx := d.selected - maxVisible/2
		if startIdx < 0 {
			startIdx = 0
		}
		if startIdx+maxVisible > n {
			startIdx = n - maxVisible
			if startIdx < 0 {
				startIdx = 0
			}
		}
		endIdx := startIdx + maxVisible
		if endIdx > n {
			endIdx = n
		}

		for i := startIdx; i < endIdx; i++ {
			e := d.entries[i]
			ago := muted.Render(fmt.Sprintf("%-10s", sessionTimeAgo(e.ModTime)))
			preview := e.Preview
			if preview == "" {
				preview = muted.Render("(empty)")
			}
			var line string
			if i == d.selected {
				line = accent.Render("→ ") + ago + "  " + accent.Render(preview)
			} else {
				line = "  " + ago + "  " + preview
			}
			sb.WriteString(line + "\n")
		}
		if n > maxVisible {
			sb.WriteString(muted.Render(fmt.Sprintf("  (%d/%d)", d.selected+1, n)) + "\n")
		}
	}

	// Delete confirmation or inline instructions.
	if d.confirmDelete >= 0 {
		id := d.entries[d.confirmDelete].ID
		sb.WriteString(accent.Render("\n  Delete session " + id + "? ") + "[" + accent.Render("Y") + "/" + muted.Render("n") + "]  " + muted.Render("Esc cancel"))
	} else if d.deleteErr != "" {
		sb.WriteString(errStyle.Render("\n  delete error: " + d.deleteErr))
	} else {
		sb.WriteString("\n" + muted.Render("  ↑↓ navigate  Enter select  d delete  Esc cancel"))
	}

	body := sb.String()
	dialog := styles.DialogBoxStyle.Render(
		lipgloss.Place(d.width-8, d.height-8, lipgloss.Left, lipgloss.Center, body),
	)
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}

func (d *SessionPickerDialog) IsDismissed() bool { return d.done }
