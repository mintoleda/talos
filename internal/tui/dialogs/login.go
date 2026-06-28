package dialogs

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mintoleda/talos/internal/tui/styles"
)

// LoginDoneMsg is sent when the user saves credentials or cancels.
type LoginDoneMsg struct {
	Provider string
	Key      string
	Canceled bool
}

// LoginProvider describes a provider entry in the login picker.
type LoginProvider struct {
	Name      string
	Label     string
	LoggedIn  bool
}

type loginStep int

const (
	loginStepPick loginStep = iota
	loginStepKey
)

// LoginDialog is a two-step dialog: pick a provider, then enter an API key.
type LoginDialog struct {
	providers []LoginProvider
	selected  int
	step      loginStep
	keyInput  textinput.Model
	width     int
	height    int
	done      bool
}

func NewLoginDialog(providers []LoginProvider) *LoginDialog {
	ti := textinput.New()
	ti.Placeholder = "paste API key…"
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'

	return &LoginDialog{
		providers: providers,
		keyInput:  ti,
	}
}

func (d *LoginDialog) Init() tea.Cmd { return textinput.Blink }

func (d *LoginDialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width, d.height = msg.Width, msg.Height
		return d, nil

	case tea.KeyMsg:
		switch d.step {
		case loginStepPick:
			switch msg.String() {
			case "esc", "ctrl+c":
				d.done = true
				return d, func() tea.Msg { return LoginDoneMsg{Canceled: true} }
			case "up":
				if d.selected > 0 {
					d.selected--
				}
			case "down":
				if d.selected < len(d.providers)-1 {
					d.selected++
				}
			case "enter":
				d.step = loginStepKey
				d.keyInput.SetValue("")
				d.keyInput.Focus()
				return d, textinput.Blink
			}

		case loginStepKey:
			switch msg.String() {
			case "esc":
				d.step = loginStepPick
				return d, nil
			case "ctrl+c":
				d.done = true
				return d, func() tea.Msg { return LoginDoneMsg{Canceled: true} }
			case "enter":
				key := strings.TrimSpace(d.keyInput.Value())
				if key == "" {
					return d, nil
				}
				provider := d.providers[d.selected].Name
				d.done = true
				return d, func() tea.Msg {
					return LoginDoneMsg{Provider: provider, Key: key}
				}
			default:
				var cmd tea.Cmd
				d.keyInput, cmd = d.keyInput.Update(msg)
				return d, cmd
			}
		}
	}
	return d, nil
}

func (d *LoginDialog) View() string {
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("82"))

	var sb strings.Builder

	switch d.step {
	case loginStepPick:
		sb.WriteString(styles.DialogTitleStyle.Render("Login to a provider"))
		sb.WriteString("\n\n")
		for i, p := range d.providers {
			check := "  "
			if p.LoggedIn {
				check = green.Render("✓ ")
			}
			label := muted.Render("[" + p.Label + "]")
			if i == d.selected {
				sb.WriteString(accent.Render("→ "+p.Name) + "  " + label + "  " + check + "\n")
			} else {
				sb.WriteString("  " + p.Name + "  " + label + "  " + check + "\n")
			}
		}
		sb.WriteString("\n" + muted.Render("↑↓ navigate  Enter select  Esc cancel"))

	case loginStepKey:
		p := d.providers[d.selected]
		sb.WriteString(styles.DialogTitleStyle.Render("Login to " + p.Name))
		sb.WriteString("\n\n")
		sb.WriteString(muted.Render("API key:") + "\n")
		inputW := d.width - 12
		if inputW < 20 {
			inputW = 20
		}
		d.keyInput.Width = inputW
		sb.WriteString(styles.InputStyle.Width(inputW).Render(d.keyInput.View()))
		sb.WriteString("\n\n" + muted.Render("Enter save  Esc back"))
	}

	body := sb.String()
	dialog := styles.DialogBoxStyle.Render(
		lipgloss.Place(d.width-8, d.height-8, lipgloss.Left, lipgloss.Center, body),
	)
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}

func (d *LoginDialog) IsDismissed() bool { return d.done }
