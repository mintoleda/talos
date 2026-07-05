package dialogs

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/tui/styles"
)

type ConfirmDialog struct {
	ev        protocol.PermissionRequested
	dismissed bool
	approved  bool
	width     int
	height    int
}

func NewConfirmDialog(ev protocol.PermissionRequested) *ConfirmDialog {
	return &ConfirmDialog{ev: ev}
}

func (d *ConfirmDialog) WithSize(w, h int) *ConfirmDialog {
	d.width, d.height = w, h
	return d
}

func (d *ConfirmDialog) Init() tea.Cmd { return nil }

func (d *ConfirmDialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width, d.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "y", "Y":
			d.approved = true
			d.dismissed = true
			if d.ev.ReplyCh != nil {
				d.ev.ReplyCh <- true
			}
		case "n", "N", "esc", "q":
			d.approved = false
			d.dismissed = true
			if d.ev.ReplyCh != nil {
				d.ev.ReplyCh <- false
			}
		}
	}
	return d, nil
}

func (d *ConfirmDialog) View() string {
	body := fmt.Sprintf("%s\n\n%s\n\n%s",
		styles.DialogTitleStyle.Render("Permission Request"),
		styles.DimStyle.Render("Tool:"),
		d.ev.ToolName)
	if d.ev.Command != "" {
		body += fmt.Sprintf("\n\n%s\n%s", styles.DimStyle.Render("Command:"), d.ev.Command)
	}
	body += fmt.Sprintf("\n\n%s\n%s", styles.DimStyle.Render("Reason:"), d.ev.Reason)
	body += "\n\n" + styles.DimStyle.Render("[Y] allow  [N] deny")

	dialog := styles.DialogBoxStyle.Render(lipgloss.Place(d.width-4, d.height-4, lipgloss.Center, lipgloss.Center, body))
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}

func (d *ConfirmDialog) IsDismissed() bool { return d.dismissed }
func (d *ConfirmDialog) Approved() bool    { return d.approved }
