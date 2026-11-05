package dialogs

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/provider/codex"
	"github.com/mintoleda/talos/internal/tui/styles"
)

// LoginDoneMsg is sent when the user saves credentials or cancels. Exactly
// one of Key (API-key providers) or OAuth (OAuth providers) is set on success.
type LoginDoneMsg struct {
	Provider string
	Key      string
	OAuth    *config.OAuthCredential
	Canceled bool
}

// LoginProvider describes a provider entry in the login picker.
type LoginProvider struct {
	Name     string
	Label    string
	LoggedIn bool
	OAuth    bool // logs in via browser/device-code flow instead of an API key
}

type loginStep int

const (
	loginStepPick loginStep = iota
	loginStepKey
	loginStepMethod // OAuth: choose browser vs device code
	loginStepOAuth  // OAuth: flow in progress
)

type oauthMethod int

const (
	oauthBrowser oauthMethod = iota
	oauthDevice
)

var oauthMethodLabels = []string{"Browser login (default)", "Device code login (headless)"}

// openBrowser launches the system browser; overridden in tests.
var openBrowser = func(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// oauthStartedMsg reports that a flow obtained its URL or device code.
type oauthStartedMsg struct {
	flow    int
	browser *codex.BrowserLogin
	device  *codex.DeviceLogin
	err     error
}

// oauthResultMsg reports a finished flow.
type oauthResultMsg struct {
	flow int
	cred config.OAuthCredential
	err  error
}

// LoginDialog picks a provider, then either takes an API key or runs an
// OAuth flow in this (the TUI) process, where the user's browser is.
type LoginDialog struct {
	providers []LoginProvider
	selected  int
	step      loginStep
	keyInput  textinput.Model
	width     int
	height    int
	done      bool

	// OAuth flow state. flow increments per attempt so results from a
	// cancelled attempt are ignored.
	oauth      *codex.OAuth
	method     oauthMethod
	flow       int
	ctx        context.Context
	cancel     context.CancelFunc
	manual     chan string
	browser    *codex.BrowserLogin
	device     *codex.DeviceLogin
	pasteInput textinput.Model
	status     string
	errText    string
}

func NewLoginDialog(providers []LoginProvider) *LoginDialog {
	ti := textinput.New()
	ti.Placeholder = "paste API key…"
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'

	pi := textinput.New()
	pi.Placeholder = codex.RedirectURI + "?code=…"

	return &LoginDialog{
		providers:  providers,
		keyInput:   ti,
		pasteInput: pi,
		oauth:      codex.NewOAuth(),
	}
}

func (d *LoginDialog) WithSize(w, h int) *LoginDialog {
	d.width, d.height = w, h
	return d
}

func (d *LoginDialog) Init() tea.Cmd { return textinput.Blink }

func (d *LoginDialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width, d.height = msg.Width, msg.Height
		return d, nil

	case oauthStartedMsg:
		return d.handleStarted(msg)

	case oauthResultMsg:
		return d.handleResult(msg)

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
				if len(d.providers) == 0 {
					return d, nil
				}
				if d.providers[d.selected].OAuth {
					d.step = loginStepMethod
					d.method = oauthBrowser
					return d, nil
				}
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

		case loginStepMethod:
			switch msg.String() {
			case "esc":
				d.step = loginStepPick
			case "ctrl+c":
				d.done = true
				return d, func() tea.Msg { return LoginDoneMsg{Canceled: true} }
			case "up":
				d.method = oauthBrowser
			case "down":
				d.method = oauthDevice
			case "enter":
				return d, d.startOAuth()
			}

		case loginStepOAuth:
			switch msg.String() {
			case "esc":
				d.stopOAuth()
				d.step = loginStepMethod
				return d, nil
			case "ctrl+c":
				d.stopOAuth()
				d.done = true
				return d, func() tea.Msg { return LoginDoneMsg{Canceled: true} }
			case "enter":
				if d.browser == nil || d.errText != "" {
					return d, nil
				}
				input := strings.TrimSpace(d.pasteInput.Value())
				if input == "" {
					return d, nil
				}
				select {
				case d.manual <- input:
					d.status = "Verifying…"
					d.pasteInput.Blur()
				default: // already submitted
				}
				return d, nil
			default:
				if d.browser != nil && d.errText == "" {
					var cmd tea.Cmd
					d.pasteInput, cmd = d.pasteInput.Update(msg)
					return d, cmd
				}
			}
		}
	}
	return d, nil
}

// startOAuth begins a new flow attempt with the selected method.
func (d *LoginDialog) startOAuth() tea.Cmd {
	d.stopOAuth()
	d.flow++
	ctx, cancel := context.WithCancel(context.Background())
	d.ctx, d.cancel = ctx, cancel
	d.manual = make(chan string, 1)
	d.step = loginStepOAuth
	d.errText = ""
	d.status = "Starting login…"
	d.pasteInput.SetValue("")

	flow, oauth, method := d.flow, d.oauth, d.method
	return func() tea.Msg {
		if method == oauthDevice {
			dev, err := oauth.StartDeviceLogin(ctx)
			return oauthStartedMsg{flow: flow, device: dev, err: err}
		}
		b, err := oauth.StartBrowserLogin(ctx)
		return oauthStartedMsg{flow: flow, browser: b, err: err}
	}
}

// stopOAuth cancels any in-flight attempt; its Wait shuts the callback
// server down.
func (d *LoginDialog) stopOAuth() {
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	if d.browser != nil {
		d.browser.Close()
	}
	d.browser, d.device = nil, nil
}

func (d *LoginDialog) handleStarted(msg oauthStartedMsg) (tea.Model, tea.Cmd) {
	if msg.flow != d.flow || d.done || d.step != loginStepOAuth {
		if msg.browser != nil {
			msg.browser.Close()
		}
		return d, nil
	}
	if msg.err != nil {
		d.fail(msg.err)
		return d, nil
	}
	flow, waitCtx := d.flow, d.ctx
	if msg.device != nil {
		d.device = msg.device
		d.status = "Waiting for approval…"
		dev := msg.device
		return d, func() tea.Msg {
			cred, err := dev.Wait(waitCtx)
			return oauthResultMsg{flow: flow, cred: cred, err: err}
		}
	}
	d.browser = msg.browser
	if d.browser.Listening {
		d.status = "A browser window should open. Complete login to finish, or paste the redirect URL below."
	} else {
		d.status = "Callback port 1455 is busy. Complete login in a browser, then paste the redirect URL below."
	}
	d.pasteInput.Focus()
	b, manual, url := d.browser, d.manual, d.browser.URL
	return d, tea.Batch(
		func() tea.Msg { _ = openBrowser(url); return nil },
		func() tea.Msg {
			cred, err := b.Wait(waitCtx, manual)
			return oauthResultMsg{flow: flow, cred: cred, err: err}
		},
		textinput.Blink,
	)
}

func (d *LoginDialog) handleResult(msg oauthResultMsg) (tea.Model, tea.Cmd) {
	if msg.flow != d.flow || d.done || d.step != loginStepOAuth {
		return d, nil
	}
	if msg.err != nil {
		if errors.Is(msg.err, codex.ErrLoginCancelled) {
			return d, nil
		}
		d.fail(msg.err)
		return d, nil
	}
	provider := d.providers[d.selected].Name
	cred := msg.cred
	d.stopOAuth()
	d.done = true
	return d, func() tea.Msg { return LoginDoneMsg{Provider: provider, OAuth: &cred} }
}

func (d *LoginDialog) fail(err error) {
	d.stopOAuth()
	d.errText = err.Error()
	d.status = ""
}

func (d *LoginDialog) View() string {
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("203"))

	inputW := d.width - 12
	if inputW < 20 {
		inputW = 20
	}

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
		d.keyInput.Width = inputW
		sb.WriteString(styles.InputStyle.Width(inputW).Render(d.keyInput.View()))
		sb.WriteString("\n\n" + muted.Render("Enter save  Esc back"))

	case loginStepMethod:
		p := d.providers[d.selected]
		sb.WriteString(styles.DialogTitleStyle.Render("Login to " + p.Name))
		sb.WriteString("\n\n" + muted.Render("Select login method:") + "\n\n")
		for i, label := range oauthMethodLabels {
			if oauthMethod(i) == d.method {
				sb.WriteString(accent.Render("→ "+label) + "\n")
			} else {
				sb.WriteString("  " + label + "\n")
			}
		}
		sb.WriteString("\n" + muted.Render("↑↓ navigate  Enter select  Esc back"))

	case loginStepOAuth:
		p := d.providers[d.selected]
		sb.WriteString(styles.DialogTitleStyle.Render("Login to " + p.Name))
		sb.WriteString("\n\n")
		switch {
		case d.errText != "":
			sb.WriteString(red.Render("Login failed: "+d.errText) + "\n")
			sb.WriteString("\n" + muted.Render("Esc back  Ctrl+C close"))
		case d.device != nil:
			sb.WriteString("Open " + accent.Render(d.device.VerificationURI) + " and enter the code:\n\n")
			sb.WriteString("    " + styles.DialogTitleStyle.Render(d.device.UserCode) + "\n\n")
			sb.WriteString(muted.Render(d.status) + "\n")
			sb.WriteString("\n" + muted.Render("Esc cancel"))
		case d.browser != nil:
			sb.WriteString(muted.Render(d.status) + "\n\n")
			// Hard-wrap so the renderer does not truncate the URL; browsers
			// drop the line breaks when it is pasted.
			sb.WriteString(accent.Render(hardWrap(d.browser.URL, inputW)) + "\n\n")
			sb.WriteString(muted.Render("Authorization code or redirect URL:") + "\n")
			d.pasteInput.Width = inputW
			sb.WriteString(styles.InputStyle.Width(inputW).Render(d.pasteInput.View()))
			sb.WriteString("\n\n" + muted.Render("Enter submit  Esc cancel"))
		default:
			sb.WriteString(muted.Render(d.status) + "\n")
			sb.WriteString("\n" + muted.Render("Esc cancel"))
		}
	}

	body := sb.String()
	dialog := styles.DialogBoxStyle.Render(
		lipgloss.Place(d.width-8, d.height-8, lipgloss.Left, lipgloss.Center, body),
	)
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}

func hardWrap(s string, width int) string {
	if width <= 0 || len(s) <= width {
		return s
	}
	var lines []string
	for len(s) > width {
		lines = append(lines, s[:width])
		s = s[width:]
	}
	return strings.Join(append(lines, s), "\n")
}

func (d *LoginDialog) IsDismissed() bool { return d.done }
