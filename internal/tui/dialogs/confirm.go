package dialogs

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

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
		case "y", "Y", "enter":
			d.approved = true
			d.dismissed = true
		case "n", "N", "esc", "q", "ctrl+c":
			d.approved = false
			d.dismissed = true
		}
	}
	return d, nil
}

func (d *ConfirmDialog) View() string {
	prompt := styles.DimStyle.Render("[Y] allow  [N] deny  [enter] allow  [esc] cancel")

	var parts []string
	parts = append(parts, styles.ToolRunningStyle.Render(d.ev.ToolName))
	if d.ev.Command != "" {
		maxCmd := d.width - 30
		if maxCmd < 20 {
			maxCmd = 20
		}
		cmd := d.ev.Command
		if len(cmd) > maxCmd {
			cmd = cmd[:maxCmd-1] + "\u2026"
		}
		parts = append(parts, styles.DimStyle.Render(cmd))
	}
	if d.ev.Reason != "" {
		reason := d.ev.Reason
		maxReason := d.width - 12
		if maxReason < 20 {
			maxReason = 20
		}
		if len(reason) > maxReason {
			reason = reason[:maxReason-1] + "\u2026"
		}
		parts = append(parts, styles.DimStyle.Render(reason))
	}

	body := styles.DialogTitleStyle.Render("\u26a0 Permission ") + strings.Join(parts, " \u00b7 ") + "\n" + prompt

	w := d.width - 4
	if w < 30 {
		w = 30
	}
	return styles.DialogBoxStyle.Width(w).Render(body)
}

func (d *ConfirmDialog) IsDismissed() bool { return d.dismissed }
func (d *ConfirmDialog) Approved() bool    { return d.approved }
