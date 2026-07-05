package dialogs

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mintoleda/talos/internal/models"
	"github.com/mintoleda/talos/internal/tui/styles"
)

const maxVisible = 10

// ModelPickerDoneMsg is sent when the user selects or cancels.
type ModelPickerDoneMsg struct {
	Provider string
	Model    string
	Canceled bool
}

// modelsLoadedMsg carries the result of the async fetch.
type modelsLoadedMsg struct {
	entries []models.Entry
	err     error
}

// FetchModelsFunc fetches all available models across all logged-in providers.
type FetchModelsFunc func() ([]models.Entry, error)

// ModelPickerDialog is a full-screen interactive model selector.
type ModelPickerDialog struct {
	all      []models.Entry
	filtered []models.Entry
	selected int
	current  string // "provider/model" of the active model
	input    textinput.Model
	spinner  spinner.Model
	loading  bool
	loadErr  string
	width    int
	height   int
	done     bool
	fetch    FetchModelsFunc
}

func NewModelPickerDialog(currentProvider, currentModel, initialQuery string, fetch FetchModelsFunc) *ModelPickerDialog {
	ti := textinput.New()
	ti.Placeholder = "Search models…"
	ti.SetValue(initialQuery)
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return &ModelPickerDialog{
		current: currentProvider + "/" + currentModel,
		input:   ti,
		spinner: sp,
		loading: true,
		fetch:   fetch,
	}
}

func (d *ModelPickerDialog) WithSize(w, h int) *ModelPickerDialog {
	d.width, d.height = w, h
	return d
}

func (d *ModelPickerDialog) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, d.spinner.Tick, d.doFetch())
}

func (d *ModelPickerDialog) doFetch() tea.Cmd {
	f := d.fetch
	return func() tea.Msg {
		if f == nil {
			return modelsLoadedMsg{}
		}
		entries, err := f()
		return modelsLoadedMsg{entries: entries, err: err}
	}
}

func (d *ModelPickerDialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

	case modelsLoadedMsg:
		d.loading = false
		if msg.err != nil {
			d.loadErr = msg.err.Error()
		} else {
			d.all = msg.entries
			d.refilter()
			for i, e := range d.filtered {
				if e.Full() == d.current {
					d.selected = i
					break
				}
			}
		}
		return d, nil

	case tea.KeyMsg:
		if d.loading {
			if msg.String() == "esc" || msg.String() == "ctrl+c" {
				d.done = true
				return d, func() tea.Msg { return ModelPickerDoneMsg{Canceled: true} }
			}
			return d, nil
		}
		switch msg.String() {
		case "esc", "ctrl+c":
			d.done = true
			return d, func() tea.Msg { return ModelPickerDoneMsg{Canceled: true} }
		case "enter":
			if len(d.filtered) > 0 {
				e := d.filtered[d.selected]
				d.done = true
				return d, func() tea.Msg {
					return ModelPickerDoneMsg{Provider: e.Provider, Model: e.ID}
				}
			}
		case "up":
			if len(d.filtered) > 0 {
				d.selected = (d.selected - 1 + len(d.filtered)) % len(d.filtered)
			}
			return d, nil
		case "down":
			if len(d.filtered) > 0 {
				d.selected = (d.selected + 1) % len(d.filtered)
			}
			return d, nil
		default:
			var cmd tea.Cmd
			d.input, cmd = d.input.Update(msg)
			d.refilter()
			return d, cmd
		}
	}
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	return d, cmd
}

func (d *ModelPickerDialog) refilter() {
	d.filtered = models.Filter(d.all, d.input.Value())
	if d.selected >= len(d.filtered) {
		d.selected = 0
	}
}

func (d *ModelPickerDialog) View() string {
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))

	var sb strings.Builder

	inputW := d.width - 8
	if inputW < 20 {
		inputW = 20
	}
	d.input.Width = inputW
	sb.WriteString(styles.InputStyle.Width(inputW).Render(d.input.View()))
	sb.WriteString("\n\n")

	switch {
	case d.loading:
		sb.WriteString("  " + d.spinner.View() + " fetching models…")
	case d.loadErr != "":
		sb.WriteString(errStyle.Render("  error: " + d.loadErr))
	case len(d.filtered) == 0:
		sb.WriteString(muted.Render("  no models match"))
	default:
		n := len(d.filtered)
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
			e := d.filtered[i]
			isCurrent := e.Full() == d.current
			isSelected := i == d.selected

			provTag := muted.Render("[" + e.Provider + "]")
			check := ""
			if isCurrent {
				check = "  " + green.Render("✓")
			}

			var line string
			if isSelected {
				line = accent.Render("→ "+e.Full()) + "  " + provTag + check
			} else {
				line = "  " + e.Full() + "  " + provTag + check
			}
			sb.WriteString(line + "\n")
		}

		if n > maxVisible {
			sb.WriteString(muted.Render(fmt.Sprintf("  (%d/%d)", d.selected+1, n)))
			sb.WriteString("\n")
		}
		if d.selected < len(d.filtered) {
			e := d.filtered[d.selected]
			sb.WriteString("\n" + muted.Render("  "+e.Full()))
		}
	}

	sb.WriteString("\n\n" + muted.Render("  ↑↓ navigate  Enter select  Esc cancel"))

	body := sb.String()
	dialog := styles.DialogBoxStyle.Render(
		lipgloss.Place(d.width-8, d.height-8, lipgloss.Left, lipgloss.Center, body),
	)
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}

func (d *ModelPickerDialog) IsDismissed() bool { return d.done }
