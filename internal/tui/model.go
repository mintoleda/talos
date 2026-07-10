package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/mintoleda/talos/internal/client"
	"github.com/mintoleda/talos/internal/pricing"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/tui/dialogs"
	"github.com/mintoleda/talos/internal/tui/panes"
	"github.com/mintoleda/talos/internal/tui/styles"
)

// Mode selects the TUI layout.
type Mode int

const (
	ModeSingleAgent Mode = iota
)

// VimMode tracks whether the user is typing (insert) or navigating (normal).
type VimMode int

const (
	InsertMode VimMode = iota
	NormalMode
)

// slashCommand describes one available slash command.
type slashCommand struct {
	Name string
	Desc string
}

// slashCommands is the canonical list used for both /help and live autocompletion.
var slashCommands = []slashCommand{
	{"/help", "Show this help"},
	{"/new", "Start a new session"},
	{"/compact", "Compact conversation history (optionally: /compact <focus message>)"},
	{"/stats", "Show aggregate token usage and cache hit rate"},
	{"/resume", "Resume a session (optionally: /resume <id>)"},
	{"/login", "Add an API key for a provider"},
	{"/model", "Switch provider/model (optionally: /model <query>)"},
	{"/thinking", "Cycle thinking level"},
	{"/permission", "Cycle permission mode (auto/ask/panic)"},
	{"/panic", "Toggle panic mode (blocks all tools)"},
	{"/subagents", "Toggle subagent delegation on/off"},
	{"/mcp", "List connected MCP servers and their tools"},
	{"/push", "Commit and push changes to GitHub"},
	{"/exit", "Quit Talos"},
}

// clearQuitConfirmMsg is sent 2s after the first ctrl+c to reset the quit prompt.
type clearQuitConfirmMsg struct{}

// clearEscClearConfirmMsg is sent 2s after the first esc to reset the esc-clear prompt.
type clearEscClearConfirmMsg struct{}

// EventMsg wraps a harness protocol.Event as a Bubble Tea message.
type EventMsg struct{ E protocol.Event }

// InputSubmitMsg is sent when the user presses Enter in the text input.
type InputSubmitMsg struct{ Text string }

// InterruptMsg signals the engine should cancel the current turn.
type InterruptMsg struct{}

// Config wires the TUI to the engine.
type Config struct {
	SessionID string
	Mode      Mode
	Engine    client.Engine
	Shutdown  func()
	Provider  string
	Model     string
	// Pricing is the pricing table used to compute dollar costs and context
	// windows for the token/cost status line. Nil disables cost display.
	Pricing *pricing.Table
	// InitialHistory, if non-empty, is replayed into the chat and tools panes
	// at startup. Used by `talos -c` / `talos --continue` to show the loaded
	// session's messages so the user can see they really continued the last
	// session instead of staring at a blank chat.
	InitialHistory []protocol.FrozenMessage

	// SeedStats, if non-zero, seeds the TUI's cumulative token and cost counters
	// at startup so resumed sessions show historical stats immediately.
	SeedStats struct {
		InputTokens  int
		OutputTokens int
		CacheMiss    int // non-cached prompt tokens
		Cost         float64
	}

	// ToggleSubagents toggles subagent visibility on/off at runtime and
	// returns a human-readable message describing the new state.
	ToggleSubagents func() string
}

// Model is the root Bubble Tea model.
type Model struct {
	cfg             Config
	mode            Mode
	vimMode         VimMode
	quitConfirm     bool
	escClearConfirm bool
	width           int
	height          int
	paneH           int // height allocated to panes, updated in relayout
	chat            panes.ChatModel
	subagents       panes.SubagentsModel
	input           textarea.Model
	busy            bool
	spinner         spinner.Model
	dialog          tea.Model
	thinkingLevel   string

	// toolNames maps tool call ID → name so ToolFinished can look up the name.
	toolNames map[string]string
	// toolArgs maps tool call ID → args so the inline chat entry can show the
	// call descriptor (path/command/query) when the tool finishes.
	toolArgs map[string]map[string]any

	// Cumulative usage across all turns (shown in the status line).
	totalInput     int
	totalOutput    int
	totalCost      float64
	totalCacheMiss int // cumulative non-cached prompt tokens
	contextUsed    int // latest turn's prompt tokens = current context size
	contextWin     int

	// busySince is set when a prompt is submitted; used to compute elapsed time.
	busySince time.Time

	// streamTextLen tracks cumulative bytes streamed in the current turn for
	// live token/cost estimation on the status bar. Reset each turn.
	streamTextLen int
	// cwd is the working directory captured at startup for the status bar.
	cwd string

	// Input history for up/down arrow cycling.
	inputHistory []string // previously submitted messages, oldest first
	historyIdx   int      // current position in history (-1 = blank/new input)
	historyDraft string   // saved draft when navigating back into history

	// Slash-command autocompletion.
	slashCompletions []slashCommand
	slashSelected    int // index into slashCompletions, -1 when none

	// File picker for @ autocomplete.
	filePicker filePickerState

	// mcpCount is the number of connected MCP servers; shown in the status bar.
	mcpCount int

	// subagentDisabled is true when the user has turned off subagents via
	// /subagents. When disabled, the spawn-tool schemas and system-prompt
	// listing are stripped from requests built by the PromptBuilder.
	subagentDisabled bool

	// permissionMode tracks the current permission mode for the status bar.
	permissionMode string

	// pendingCmd carries a tea.Cmd produced inside handleSlash (which cannot
	// return commands directly) out to the Update loop. Consumed and cleared by
	// the caller. Used by /resume to force a full repaint.
	pendingCmd tea.Cmd

	// inputRows is the number of visual rows the prompt box currently displays
	// for the (soft-wrapping) input, computed by relayout(). The textarea's own
	// height is kept pinned at maxInputRows (see relayout) so its internal
	// viewport never needs to scroll under the cap; inputRows only controls how
	// many of its rendered rows promptBoxView prints.
	inputRows int
}

// NewModel builds the initial TUI model.
func NewModel(cfg Config) Model {
	ti := textarea.New()
	ti.Prompt = "" // the "›" glyph is drawn by promptBoxView, not textarea
	ti.Placeholder = "Type a message..."
	ti.ShowLineNumbers = false
	ti.CharLimit = 0 // no limit; long input soft-wraps instead
	// Neutralize the default cursor-line background so the input reads as plain
	// text inside our rounded box rather than a highlighted bar.
	ti.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ti.BlurredStyle.CursorLine = lipgloss.NewStyle()
	// Enter submits (intercepted before textarea sees it); these insert a literal
	// newline for multi-line messages.
	ti.KeyMap.InsertNewline.SetKeys("shift+enter", "alt+enter", "ctrl+j")
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	cwd, _ := os.Getwd()
	m := Model{
		cfg:              cfg,
		mode:             cfg.Mode,
		chat:             panes.NewChat(),
		subagents:        panes.NewSubagents(),
		input:            ti,
		spinner:          sp,
		toolNames:        make(map[string]string),
		toolArgs:         make(map[string]map[string]any),
		inputHistory:     nil,
		historyIdx:       -1,
		historyDraft:     "",
		slashCompletions: nil,
		slashSelected:    -1,
		cwd:              cwd,
		filePicker:       filePickerState{},
		totalInput:       cfg.SeedStats.InputTokens,
		totalOutput:      cfg.SeedStats.OutputTokens,
		totalCacheMiss:   cfg.SeedStats.CacheMiss,
		totalCost:        cfg.SeedStats.Cost,
		mcpCount:         0,
	}
	if cfg.Engine != nil {
		m.thinkingLevel = cfg.Engine.CurrentThinkingLevel()
	}
	if cfg.Engine != nil {
		m.mcpCount = cfg.Engine.MCPCount()
	}
	if cfg.Engine != nil {
		m.permissionMode = cfg.Engine.PermissionMode()
	}
	// Seed the context window from the pricing table so the status bar shows
	// context usage from the very first turn instead of waiting for TurnEnded.
	if cfg.Pricing != nil && cfg.Model != "" {
		m.contextWin = cfg.Pricing.ContextWindow(cfg.Model)
	}

	// Replay any pre-loaded transcript into the chat pane. This is what
	// makes `talos -c` visually feel like a continuation: without it the model
	// has the context but the user sees a blank screen and assumes the session
	// was reset. The relayout is deferred — terminal size isn't known yet, so
	// the first WindowSizeMsg will fix the viewport dimensions.
	if len(cfg.InitialHistory) > 0 {
		m.replayTranscript(cfg.InitialHistory)
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.spinner.Tick,
		m.subagents.Init(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Active dialog gets first crack at every message.
	if m.dialog != nil {
		newDlg, cmd := m.dialog.Update(msg)
		if d, ok := newDlg.(dialog); ok && d.IsDismissed() {
			if cd, ok := d.(*dialogs.ConfirmDialog); ok && m.cfg.Engine != nil {
				m.cfg.Engine.Approve(cd.Approved(), nil)
			}
			m.dialog = nil
		} else {
			m.dialog = newDlg
		}
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.relayout()

	case dialogs.LoginDoneMsg:
		if !msg.Canceled && msg.Provider != "" && msg.Key != "" {
			if m.cfg.Engine == nil {
				m.chat = m.chat.AppendNotice("error", "engine unavailable")
				return m, nil
			}
			if err := m.cfg.Engine.Login(msg.Provider, msg.Key); err != nil {
				m.chat = m.chat.AppendNotice("error", err.Error())
			} else {
				m.chat = m.chat.AppendNotice("info", "logged in to "+msg.Provider)
				// Recreate the provider so the new key takes effect immediately.
				if err := m.cfg.Engine.SwitchModel(m.cfg.Provider, m.cfg.Model); err != nil {
					m.chat = m.chat.AppendNotice("error", err.Error())
				}
			}
		}
		return m, nil

	case dialogs.SessionPickerDoneMsg:
		if !msg.Canceled && msg.ID != "" && m.cfg.Engine != nil {
			newID, history, err := m.cfg.Engine.Resume(msg.ID)
			if err != nil {
				m.chat = m.chat.AppendNotice("error", err.Error())
			} else {
				return m, m.applyResume(newID, history)
			}
		}
		return m, nil

	case dialogs.ModelPickerDoneMsg:
		if !msg.Canceled && msg.Provider != "" {
			if m.cfg.Engine == nil {
				m.chat = m.chat.AppendNotice("error", "engine unavailable")
				return m, nil
			}
			if err := m.cfg.Engine.SwitchModel(msg.Provider, msg.Model); err != nil {
				m.chat = m.chat.AppendNotice("error", err.Error())
			} else {
				m.cfg.Provider = msg.Provider
				m.cfg.Model = msg.Model
			}
		}
		return m, nil

	case clearQuitConfirmMsg:
		m.quitConfirm = false

	case clearEscClearConfirmMsg:
		m.escClearConfirm = false

	case tea.KeyMsg:
		// ctrl+v: paste image from clipboard (inserts @path into input).
		if msg.String() == "ctrl+v" && !m.busy {
			if ref := pasteClipboardImage(); ref != "" {
				cur := m.input.Value()
				if cur != "" && !strings.HasSuffix(cur, " ") {
					cur += " "
				}
				m.input.SetValue(cur + ref)
				m.input.CursorEnd()
				m.relayout()
			}
			return m, nil
		}

		// ctrl+c: interrupt if busy; otherwise double-press to quit.
		if msg.String() == "ctrl+c" {
			if m.busy {
				if m.cfg.Engine != nil {
					m.cfg.Engine.Interrupt()
				}
				return m, nil
			}
			if m.quitConfirm {
				return m, tea.Quit
			}
			m.quitConfirm = true
			return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return clearQuitConfirmMsg{} })
		}

		if m.vimMode == NormalMode {
			switch msg.String() {
			case "i":
				m.vimMode = InsertMode
				return m, m.input.Focus()
			case "j":
				m.chat = m.chat.ScrollDown(2)
			case "k":
				m.chat = m.chat.ScrollUp(2)
			case "g":
				m.chat = m.chat.ScrollTop()
			case "G":
				m.chat = m.chat.ScrollBottom()
			}
			return m, nil
		}

		// Insert mode key handling.
		// File picker (@) takes priority when active.
		if m.filePicker.active {
			switch msg.String() {
			case "up":
				if len(m.filePicker.results) > 0 {
					m.filePicker.selected = (m.filePicker.selected - 1 + len(m.filePicker.results)) % len(m.filePicker.results)
				}
				return m, nil
			case "down":
				if len(m.filePicker.results) > 0 {
					m.filePicker.selected = (m.filePicker.selected + 1) % len(m.filePicker.results)
				}
				return m, nil
			case "enter":
				m.insertFilePickerSelection()
				m.filePicker.deactivate()
				m.slashCompletions = nil
				m.slashSelected = -1
				m.relayout()
				return m, nil
			case "tab":
				m.insertFilePickerSelection()
				m.filePicker.deactivate()
				m.slashCompletions = nil
				m.slashSelected = -1
				m.relayout()
				return m, nil
			case "esc", "ctrl+c":
				m.filePicker.deactivate()
				m.relayout()
				return m, nil
			}
		}

		switch msg.String() {
		case "esc":
			if m.input.Value() == "" {
				m.vimMode = NormalMode
				m.input.Blur()
				return m, nil
			}
			if m.escClearConfirm {
				m.input.SetValue("")
				m.escClearConfirm = false
				m.slashCompletions = nil
				m.slashSelected = -1
				m.filePicker.deactivate()
				m.relayout()
				return m, nil
			}
			m.escClearConfirm = true
			return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return clearEscClearConfirmMsg{} })
		case "ctrl+l":
			if !m.busy {
				if m.cfg.Engine == nil {
					m.chat = m.chat.AppendNotice("error", "engine unavailable")
					return m, nil
				}
				m.dialog = dialogs.NewModelPickerDialog(m.cfg.Provider, m.cfg.Model, "", m.cfg.Engine.ListModels).WithSize(m.width, m.height)
				return m, m.dialog.Init()
			}
			return m, nil
		case "ctrl+o":
			m.chat = m.chat.ToggleToolExpand()
			return m, nil
		case "alt+g":
			m.chat = m.chat.ScrollBottom()
			return m, nil
		case "alt+t":
			m.chat = m.chat.ToggleThinkExpand()
			return m, nil
		case "ctrl+t":
			if !m.busy {
				if m.cfg.Engine != nil {
					newLevel, err := m.cfg.Engine.CycleThinking()
					if err != nil {
						m.chat = m.chat.AppendNotice("error", err.Error())
						return m, nil
					}
					m.thinkingLevel = newLevel
					m.chat = m.chat.AppendNotice("info", "thinking level: "+newLevel)
				}
			}
			return m, nil
		case "tab":
			if len(m.slashCompletions) > 0 {
				m.slashSelected = (m.slashSelected + 1) % len(m.slashCompletions)
			}
			return m, nil
		case "shift+tab":
			if len(m.slashCompletions) > 0 {
				m.slashSelected = (m.slashSelected - 1 + len(m.slashCompletions)) % len(m.slashCompletions)
				return m, nil
			}
			if !m.busy && m.cfg.Engine != nil {
				newMode, err := m.cfg.Engine.CyclePermissionMode()
				if err != nil {
					m.chat = m.chat.AppendNotice("error", err.Error())
					return m, nil
				}
				m.permissionMode = newMode
				m.chat = m.chat.AppendNotice("info", "permission mode: "+newMode)
			}
			return m, nil
		case "alt+p":
			if m.cfg.Engine != nil {
				newMode, err := m.cfg.Engine.TogglePanic()
				if err != nil {
					m.chat = m.chat.AppendNotice("error", err.Error())
					return m, nil
				}
				m.permissionMode = newMode
				m.chat = m.chat.AppendNotice("info", "panic mode: "+newMode)
			}
			return m, nil
		case "enter":
			if m.busy {
				// While busy, Enter queues as a "steer" message — it gets
				// injected after the current tool calls finish but before the
				// next LLM streaming call (like pi's steer mechanism).
				// Pending steers are withdrawable via up-arrow (they pop back
				// into the input bar so you can edit or discard them).
				text := m.input.Value()
				if text != "" && m.cfg.Engine != nil {
					// Bare text — engine ResolveInput runs server-side on Submit/Steer paths.
					blocks := protocol.TextBlocks(text)
					m.chat = m.chat.AppendUserBlocks(blocks)
					m.cfg.Engine.Steer(blocks)
					m.input.Reset()
					m.slashCompletions = nil
					m.slashSelected = -1
					m.relayout()
				}
				return m, nil
			}
			if !m.busy {
				text := m.input.Value()

				// If slash completions are shown, auto-complete to the selected match
				// when the input is still a proper prefix (not already an exact match).
				if len(m.slashCompletions) > 0 && m.slashSelected >= 0 {
					completed := m.slashCompletions[m.slashSelected].Name
					if text != completed {
						m.input.SetValue(completed)
						m.input.CursorEnd()
						m.slashCompletions = nil
						m.slashSelected = -1
						m.relayout()
						return m, nil
					}
				}
				// Clear completions and submit.
				m.slashCompletions = nil
				m.slashSelected = -1
				m.filePicker.deactivate()
				m.relayout()
				m.input.Reset()
				if text == "/exit" {
					return m, tea.Quit
				}
				if text != "" {
					handled, err := m.handleSlash(text)
					if err != nil {
						m.chat = m.chat.AppendNotice("error", err.Error())
						return m, nil
					}
					if handled {
						cmd := m.pendingCmd
						m.pendingCmd = nil
						if m.dialog != nil {
							return m, tea.Batch(cmd, m.dialog.Init())
						}
						return m, cmd // handled but no dialog (e.g. /new)
					}
					// Save to input history (non-slash messages only).
					m.inputHistory = append(m.inputHistory, text)
					m.historyIdx = -1
					m.historyDraft = ""
					if m.cfg.Engine == nil {
						m.chat = m.chat.AppendNotice("error", "engine unavailable")
						return m, nil
					}
					// Bare text — Engine.Submit ResolveInput's a single text block.
					blocks := protocol.TextBlocks(text)
					m.chat = m.chat.AppendUserBlocks(blocks)
					m.busy = true
					m.busySince = time.Now()
					m.relayout()
					m.cfg.Engine.Submit(blocks)
				}
			}
			return m, nil
		case "up":
			if len(m.slashCompletions) > 0 {
				m.slashSelected = (m.slashSelected - 1 + len(m.slashCompletions)) % len(m.slashCompletions)
				return m, nil
			}
			if m.busy {
				// If there are pending steer messages, up-arrow withdraws
				// the most recent one back into the input bar for editing.
				if m.cfg.Engine != nil {
					if last := m.cfg.Engine.WithdrawSteer(); last != nil {
						// Remove the chat entry so the user sees it's gone.
						m.chat = m.chat.PopLastSegment()
						// Put the text back in the input bar for editing.
						var parts []string
						for _, b := range last {
							if b.Type == protocol.BlockText && b.Text != "" {
								parts = append(parts, b.Text)
							}
						}
						text := strings.Join(parts, " ")
						m.input.SetValue(text)
						m.input.CursorEnd()
						m.escClearConfirm = false
						m.relayout()
						return m, nil
					}
				}
				// No pending steer — scroll chat.
				m.chat = m.chat.ScrollUp(2)
				return m, nil
			}
			// Not busy, no completions: cycle input history backward.
			if len(m.inputHistory) == 0 {
				return m, nil
			}
			if m.historyIdx == -1 {
				m.historyDraft = m.input.Value()
				m.historyIdx = len(m.inputHistory) - 1
			} else if m.historyIdx > 0 {
				m.historyIdx--
			}
			m.input.SetValue(m.inputHistory[m.historyIdx])
			m.input.CursorEnd()
			m.escClearConfirm = false
			m.filePicker.deactivate()
			m.relayout()
			return m, nil
		case "down":
			if len(m.slashCompletions) > 0 {
				m.slashSelected = (m.slashSelected + 1) % len(m.slashCompletions)
				return m, nil
			}
			if m.busy {
				m.chat = m.chat.ScrollDown(2)
				return m, nil
			}
			// Not busy, no completions: cycle input history forward.
			if len(m.inputHistory) == 0 {
				return m, nil
			}
			if m.historyIdx < len(m.inputHistory)-1 {
				m.historyIdx++
				m.input.SetValue(m.inputHistory[m.historyIdx])
				m.input.CursorEnd()
			} else {
				m.historyIdx = -1
				m.input.SetValue(m.historyDraft)
				m.input.CursorEnd()
				m.historyDraft = ""
			}
			m.escClearConfirm = false
			m.filePicker.deactivate()
			m.relayout()
			return m, nil
		default:
			m.escClearConfirm = false
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.updateSlashCompletions()
			m.updateFilePicker()
			m.relayout() // input height may have changed (soft-wrap / newline)
			return m, cmd
		}

	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonLeft:
		case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
			if msg.Button == tea.MouseButtonWheelUp {
				m.chat = m.chat.ScrollUp(2)
			} else {
				m.chat = m.chat.ScrollDown(2)
			}
		}
		return m, nil

	case EventMsg:
		m = m.handleEvent(msg.E)

	case spinner.TickMsg:
		// Always update the spinner model and schedule the next tick, even when
		// not busy, so the animation is ready to run the moment busy becomes true.
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.busy {
			// Send a broadcast tick (ID=0, tag=0) that all spinners accept so
			// the chat's active-tool animation advances.
			broadcast := spinner.TickMsg{}
			m.subagents, _ = m.subagents.Update(broadcast)
			m.chat, _ = m.chat.Update(broadcast)
		}
		return m, cmd
	}

	// Allow panes to handle their own updates (viewport scrolling, etc.).
	var chatCmd tea.Cmd
	m.chat, chatCmd = m.chat.Update(msg)
	return m, chatCmd
}

// replayTranscript populates the chat and tools panes from a loaded transcript.
// It replays user input, assistant text (as markdown), and historical tool calls.
func (m *Model) replayTranscript(msgs []protocol.FrozenMessage) {
	// Index tool results by their tool_use_id so we can finish tools immediately.
	toolResults := make(map[string]protocol.ToolResult)
	for _, fm := range msgs {
		if fm.Msg.Role != protocol.RoleTool {
			continue
		}
		for _, b := range fm.Msg.Content {
			if b.ToolResult != nil {
				toolResults[b.ToolResult.ToolUseID] = *b.ToolResult
			}
		}
	}

	for _, fm := range msgs {
		msg := fm.Msg
		switch msg.Role {
		case protocol.RoleUser:
			m.chat = m.chat.AppendUserBlocks(msg.Content)
			// Seed input history so the up-arrow in attach mode (and
			// continue/resume) cycles through past user messages.
			var textParts []string
			for _, b := range msg.Content {
				if b.Type == protocol.BlockText && b.Text != "" {
					textParts = append(textParts, b.Text)
				}
			}
			if len(textParts) > 0 {
				m.inputHistory = append(m.inputHistory, strings.Join(textParts, " "))
			}
		case protocol.RoleAssistant:
			var parts []string
			for _, b := range msg.Content {
				if b.Type == protocol.BlockText && b.Text != "" {
					parts = append(parts, b.Text)
				}
			}
			if len(parts) > 0 {
				m.chat = m.chat.AppendAssistantText(strings.Join(parts, "\n"))
			}
			for _, b := range msg.Content {
				if b.Type == protocol.BlockToolUse && b.ToolUse != nil {
					tu := b.ToolUse
					if result, ok := toolResults[tu.ID]; ok {
						m.chat = m.chat.AppendToolUse(tu.Name, tu.Args, !result.IsError, result.Content)
					} else {
						m.chat = m.chat.AppendToolUse(tu.Name, tu.Args, true, "")
					}
				}
			}
		}
	}
	// Estimate context size from the total conversation content so the
	// status bar shows non-zero usage immediately on resume (instead of
	// waiting for the first PromptEstimate or TurnEnded event).
	var rawBytes int
	for _, fm := range msgs {
		rawBytes += len(fm.Raw)
	}
	// System prompt and tool schemas add roughly 5KB of overhead.
	m.contextUsed = (rawBytes + 5000) / 4

	m.relayout()
}

// applyResume swaps the TUI over to a freshly loaded session: it resets the
// per-session panes and counters, replays the transcript, and reseeds the
// cumulative stats. It returns tea.ClearScreen to force a full repaint —
// replacing the whole transcript changes the rendered height, and the standard
// renderer would otherwise leave the previous session's status bar ghosted
// above the new one. Shared by the /resume picker and the direct /resume <id>.
func (m *Model) applyResume(newID string, history []protocol.FrozenMessage) tea.Cmd {
	m.chat = panes.NewChat()
	m.toolNames = make(map[string]string)
	m.toolArgs = make(map[string]map[string]any)
	m.busy = false
	m.cfg.SessionID = newID
	m.relayout()
	m.replayTranscript(history)
	m.reseedStats()
	return tea.ClearScreen
}

// reseedStats refreshes cumulative token and cost counters from the engine.
// Used after /resume and TurnEnded so attached clients converge on the
// server-authoritative totals.
func (m *Model) reseedStats() {
	if m.cfg.Engine == nil {
		return
	}
	in, out, miss, cost, err := m.cfg.Engine.Stats()
	if err != nil {
		return
	}
	m.totalInput = in
	m.totalOutput = out
	m.totalCacheMiss = miss
	m.totalCost = cost
}

// maxInputRows caps how tall the input box may grow before it starts scrolling
// internally (like Claude Code / pi.dev, which grow to a point then scroll).
const maxInputRows = 10

func (m *Model) relayout() {
	// Layout (top to bottom): chat pane + (if busy: thinking line) + completions
	// + file picker + rounded prompt box. The prompt box is (top border) +
	// (input rows) + (bottom border), and the input rows grow with soft-wrapped
	// content instead of overflowing on one line.
	inputContentW := m.width - 5 // │ > [content] │
	if inputContentW < 1 {
		inputContentW = 1
	}
	m.input.SetWidth(inputContentW)

	// Cap the box height, always leaving room for the borders and a little chat.
	maxRows := maxInputRows
	if lim := m.height - 4; lim < maxRows {
		maxRows = lim
	}
	if maxRows < 1 {
		maxRows = 1
	}

	// Keep the textarea's own height pinned at the cap at all times. textarea
	// scrolls its internal viewport to keep the cursor visible on every
	// keystroke (repositionView), using whatever height was set last. If we
	// shrink it to fit content (e.g. down to 1 row) after each render, the next
	// keystroke's Update runs against that stale small height, scrolls the
	// viewport to follow the cursor, and hides every row above it — the box
	// then only ever shows the current line. Pinning at the cap means
	// repositionView never has cause to scroll while content stays under the
	// cap; only measurement/display use the actual content row count.
	m.input.SetHeight(maxRows)
	inputRows := visibleRowCount(m.input.View())
	if inputRows > maxRows {
		inputRows = maxRows
	}
	if inputRows < 1 {
		inputRows = 1
	}
	m.inputRows = inputRows

	thinkingH := 0
	if m.busy {
		thinkingH = 1
	}
	boxH := inputRows + 2 // top border + input rows + bottom border
	paneH := m.height - boxH - thinkingH - m.completionHeight() - m.filePicker.height()
	if paneH < 1 {
		paneH = 1
	}
	m.paneH = paneH
	m.chat.SetSize(m.width, paneH)
}

// visibleRowCount returns the number of visual rows a textarea View occupies,
// ignoring the blank end-of-buffer rows textarea pads out to its height.
func visibleRowCount(view string) int {
	last := 0
	for i, ln := range strings.Split(view, "\n") {
		if strings.TrimSpace(ansi.Strip(ln)) != "" {
			last = i + 1
		}
	}
	if last < 1 {
		last = 1
	}
	return last
}

// handleSlash processes a slash command. Returns (handled, error) where handled=true
// means the input was a slash command and should not be forwarded to the AI.
func (m *Model) handleSlash(text string) (bool, error) {
	parts := strings.Fields(text)
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "/") {
		return false, nil
	}
	switch parts[0] {
	case "/new":
		if m.cfg.Engine == nil {
			return true, fmt.Errorf("engine unavailable")
		}
		newID, err := m.cfg.Engine.NewSession()
		if err != nil {
			return true, err
		}
		m.cfg.SessionID = newID
		m.chat = panes.NewChat()

		m.subagents = panes.NewSubagents()
		m.toolNames = make(map[string]string)
		m.toolArgs = make(map[string]map[string]any)
		m.busy = false
		m.totalInput = 0
		m.totalOutput = 0
		m.totalCost = 0
		m.streamTextLen = 0
		m.contextUsed = 0
		m.contextWin = 0
		m.inputHistory = nil
		m.historyIdx = -1
		m.historyDraft = ""
		m.relayout()
		return true, nil
	case "/resume":
		if m.cfg.Engine == nil {
			return true, fmt.Errorf("engine unavailable")
		}
		if len(parts) > 1 {
			// ID supplied directly — resume immediately.
			newID, history, err := m.cfg.Engine.Resume(parts[1])
			if err != nil {
				return true, err
			}
			m.pendingCmd = m.applyResume(newID, history)
			return true, nil
		}
		// No ID: open the session picker dialog.
		dlg := dialogs.NewSessionPickerDialog(m.cfg.Engine.ListSessions).WithDeleteFn(m.cfg.Engine.DeleteSession).WithSize(m.width, m.height)
		m.dialog = dlg
		return true, nil
	case "/restore", "/undo":
		return true, fmt.Errorf("%s not yet implemented in TUI", text)
	case "/login":
		if m.cfg.Engine == nil {
			return true, fmt.Errorf("engine unavailable")
		}
		providers, err := m.cfg.Engine.LoginProviders()
		if err != nil {
			return true, err
		}
		m.dialog = dialogs.NewLoginDialog(providers).WithSize(m.width, m.height)
		return true, nil
	case "/stats":
		if m.cfg.Engine == nil {
			return true, fmt.Errorf("engine unavailable")
		}
		in, out, miss, cost, err := m.cfg.Engine.Stats()
		if err != nil {
			return true, err
		}
		m.chat = m.chat.AppendNotice("info", formatStats(in, out, miss, cost))
		return true, nil
	case "/model":
		if m.cfg.Engine == nil {
			return true, fmt.Errorf("engine unavailable")
		}
		query := ""
		if len(parts) >= 2 {
			query = strings.Join(parts[1:], " ")
		}
		m.dialog = dialogs.NewModelPickerDialog(m.cfg.Provider, m.cfg.Model, query, m.cfg.Engine.ListModels).WithSize(m.width, m.height)
		return true, nil
	case "/thinking":
		if m.cfg.Engine == nil {
			return true, fmt.Errorf("engine unavailable")
		}
		newLevel, err := m.cfg.Engine.CycleThinking()
		if err != nil {
			return true, err
		}
		m.thinkingLevel = newLevel
		m.chat = m.chat.AppendNotice("info", "thinking level: "+newLevel)
		return true, nil
	case "/permission":
		if m.cfg.Engine == nil {
			return true, fmt.Errorf("engine unavailable")
		}
		newMode, err := m.cfg.Engine.CyclePermissionMode()
		if err != nil {
			return true, err
		}
		m.chat = m.chat.AppendNotice("info", "permission mode: "+newMode)
		return true, nil
	case "/panic":
		if m.cfg.Engine == nil {
			return true, fmt.Errorf("engine unavailable")
		}
		newMode, err := m.cfg.Engine.TogglePanic()
		if err != nil {
			return true, err
		}
		m.chat = m.chat.AppendNotice("info", "panic mode: "+newMode)
		return true, nil
	case "/push":
		if m.cfg.Engine == nil {
			return true, fmt.Errorf("engine unavailable")
		}
		msg, notice, err := m.cfg.Engine.PushInstruction()
		if err != nil {
			return true, err
		}
		if notice != "" {
			m.chat = m.chat.AppendNotice("info", notice)
		}
		if msg != "" {
			if m.cfg.Engine != nil {
				m.cfg.Engine.Submit(protocol.TextBlocks(msg))
			}
		}
		return true, nil
	case "/compact":
		focus := ""
		if len(parts) >= 2 {
			focus = strings.Join(parts[1:], " ")
		}
		if m.cfg.Engine == nil {
			return true, fmt.Errorf("engine unavailable")
		}
		if err := m.cfg.Engine.Compact(focus); err != nil {
			return true, err
		}
		return true, nil
	case "/subagents":
		if m.cfg.ToggleSubagents != nil {
			m.subagentDisabled = !m.subagentDisabled
			msg := m.cfg.ToggleSubagents()
			m.chat = m.chat.AppendNotice("info", msg)
		} else {
			m.chat = m.chat.AppendNotice("info", "subagents unavailable")
		}
		return true, nil
	case "/mcp":
		if m.cfg.Engine == nil {
			return true, fmt.Errorf("engine unavailable")
		}
		status, err := m.cfg.Engine.MCPStatus()
		if err != nil {
			return true, err
		}
		m.chat = m.chat.AppendNotice("info", status)
		return true, nil
	case "/help", "/":
		m.chat = m.chat.AppendNotice("info", m.slashHelp())
		return true, nil
	default:
		return true, fmt.Errorf("unknown slash command: %s\n%s", parts[0], m.slashHelp())
	}
}

func (m *Model) slashHelp() string {
	var b strings.Builder
	b.WriteString("Available commands:\n")
	for _, sc := range slashCommands {
		fmt.Fprintf(&b, "  %-12s %s\n", sc.Name, sc.Desc)
	}
	return b.String()
}

func formatStats(input, output, cacheMiss int, cost float64) string {
	cached := input - cacheMiss
	if cached < 0 {
		cached = 0
	}
	rate := 0.0
	if input > 0 {
		rate = float64(cached) / float64(input) * 100
	}
	if cost > 0 {
		return fmt.Sprintf("[stats] input=%d | output=%d | cached=%d (%.1f%%) | cost=$%.4f", input, output, cached, rate, cost)
	}
	return fmt.Sprintf("[stats] input=%d | output=%d | cached=%d (%.1f%%)", input, output, cached, rate)
}

func (m *Model) updateFilePicker() {
	val := m.input.Value()

	atIdx, query := extractLastAt(val)
	if atIdx < 0 {
		// No valid @ — deactivate if active.
		if m.filePicker.active {
			m.filePicker.deactivate()
			m.relayout()
		}
		return
	}

	// Don't re-activate if the file picker was manually closed (query unchanged
	// and still showing the same @ position).
	if m.filePicker.active && m.filePicker.atIndex == atIdx && m.filePicker.query == query {
		return
	}

	// Activate / refresh the file picker.
	if m.cfg.Engine != nil {
		paths, err := m.cfg.Engine.ListFiles(query)
		if err == nil {
			m.filePicker.activatePaths(paths, query, atIdx)
			m.relayout()
			return
		}
	}
	m.filePicker.activate(m.cwd, query, atIdx)
	m.relayout()
}

func (m *Model) insertFilePickerSelection() {
	path := m.filePicker.selectedPath()
	if path == "" {
		return
	}
	val := m.input.Value()
	atIdx := m.filePicker.atIndex
	if atIdx < 0 || atIdx >= len(val) {
		return
	}
	// Replace from @ to end of current word with @path.
	// Find where the current word after @ ends (next space or end).
	end := atIdx + 1
	for end < len(val) && val[end] != ' ' {
		end++
	}
	newVal := val[:atIdx] + "@" + path
	if end < len(val) && val[end] == ' ' {
		newVal += " "
	}
	m.input.SetValue(newVal)
	m.input.CursorEnd()
}

func (m *Model) updateSlashCompletions() {
	val := m.input.Value()
	m.slashCompletions = nil
	m.slashSelected = -1
	if !strings.HasPrefix(val, "/") || val == "" {
		return
	}
	prefix := strings.ToLower(val)
	for _, sc := range slashCommands {
		if strings.HasPrefix(strings.ToLower(sc.Name), prefix) {
			m.slashCompletions = append(m.slashCompletions, sc)
		}
	}
	if len(m.slashCompletions) > 0 {
		m.slashSelected = 0
	}
	// The completion popup changes the total view height, so the panes must be
	// resized to make room (or reclaim space). Without this, the rendered view
	// can exceed the terminal height, causing the alt-screen renderer to scroll
	// and leave ghost copies of the input line behind.
	m.relayout()
}

func (m *Model) completionHeight() int {
	n := len(m.slashCompletions)
	if n == 0 {
		return 0
	}
	if n > 5 {
		n = 5
	}
	return n + 1 // items + blank line separator
}

func (m Model) renderCompletions() string {
	if len(m.slashCompletions) == 0 {
		return ""
	}
	// Find the longest command name for column alignment.
	maxNameLen := 0
	for _, sc := range m.slashCompletions {
		if l := len(sc.Name); l > maxNameLen {
			maxNameLen = l
		}
	}

	var b strings.Builder
	maxShow := 5
	for i, sc := range m.slashCompletions {
		if i >= maxShow {
			fmt.Fprintf(&b, "  … %d more\n", len(m.slashCompletions)-maxShow)
			break
		}
		line := fmt.Sprintf("  %-*s  %s", maxNameLen, sc.Name, sc.Desc)
		if i == m.slashSelected {
			b.WriteString(styles.ToolSelectedStyle.Render(line))
		} else {
			b.WriteString(styles.DimStyle.Render(line))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m Model) handleEvent(e protocol.Event) Model {
	switch ev := e.(type) {
	case protocol.PermissionModeChanged:
		m.permissionMode = ev.Mode
	case protocol.UserInput:
		if _, ok := m.cfg.Engine.(*client.RemoteEngine); ok && !strings.HasPrefix(ev.Text, "/") {
			break
		}
		m.chat = m.chat.AppendUserBlocks(protocol.TextBlocks(ev.Text))
	case protocol.ModelChanged:
		m.cfg.Provider = ev.Provider
		m.cfg.Model = ev.Model
		m.thinkingLevel = ev.ThinkingLevel
		if m.cfg.Pricing != nil && ev.Model != "" {
			m.contextWin = m.cfg.Pricing.ContextWindow(ev.Model)
		}
	case protocol.PromptEstimate:
		m.contextUsed = ev.PromptTokens
		if m.contextWin == 0 && ev.ContextLimit > 0 {
			m.contextWin = ev.ContextLimit
		}
	case protocol.ThinkingBlock:
		m.chat = m.chat.AppendThinkingBlock(ev.Text)
	case protocol.ThinkingDelta:
		m.chat = m.chat.AppendThinkDelta(ev.Text)
	case protocol.TextDelta:
		m.chat = m.chat.AppendDelta(ev.Text)
		m.streamTextLen += len(ev.Text)
	case protocol.BatchStarted:
		m.chat = m.chat.FlushStreaming()
		m.chat = m.chat.AppendBatchHeading(ev.Num)
	case protocol.ToolStarted:
		m.chat = m.chat.FlushStreaming()
		m.chat = m.chat.AddActiveTool(ev.ID, ev.Name)
		m.chat = m.chat.AddProvisionalTool(ev.ID, ev.Name, ev.Args)
		if m.toolNames == nil {
			m.toolNames = make(map[string]string)
		}
		if m.toolArgs == nil {
			m.toolArgs = make(map[string]map[string]any)
		}
		m.toolNames[ev.ID] = ev.Name
		m.toolArgs[ev.ID] = ev.Args
	case protocol.ToolOutputDelta:
		m.chat = m.chat.AppendToolDelta(ev.ID, ev.Text)
	case protocol.ToolFinished:
		m.chat = m.chat.RemoveActiveTool(ev.ID)
		m.chat = m.chat.FinalizeProvisionalTool(ev.ID, !ev.Result.IsError, ev.Result.Content)
		delete(m.toolNames, ev.ID)
		delete(m.toolArgs, ev.ID)
	case protocol.BatchFinished:
	case protocol.Notice:
		m.chat = m.chat.AppendNotice(ev.Level, ev.Text)
	case protocol.TurnEnded:
		m.busy = false
		m.chat = m.chat.FinalizeTurn(ev.Usage)
		// Accumulate token and cost stats for the live status line.
		m.totalInput += ev.Usage.PromptTokens
		m.totalOutput += ev.Usage.CompletionTokens
		m.totalCacheMiss += ev.Usage.PromptTokens - ev.Usage.CachedPromptTokens
		m.contextUsed = ev.Usage.PromptTokens // latest turn = current context size
		if m.cfg.Pricing != nil && m.cfg.Model != "" {
			m.totalCost += m.cfg.Pricing.Cost(m.cfg.Model, ev.Usage.PromptTokens, ev.Usage.CompletionTokens)
			if m.contextWin == 0 {
				m.contextWin = m.cfg.Pricing.ContextWindow(m.cfg.Model)
			}
		}
		m.reseedStats()
		m.streamTextLen = 0
		m.relayout()
	case protocol.PermissionRequested:
		m.dialog = dialogs.NewConfirmDialog(ev).WithSize(m.width, m.height)
	case protocol.ApprovalResolved:
		if _, ok := m.dialog.(*dialogs.ConfirmDialog); ok {
			m.dialog = nil
		}
	case protocol.EngineSnapshot:
		// Newly-attached client syncing with a server that is mid-turn.
		m.busy = ev.Busy
		if ev.Busy {
			m.busySince = time.Now()
		}
		if ev.StreamedText != "" {
			m.chat = m.chat.AppendDelta(ev.StreamedText)
			m.streamTextLen = len(ev.StreamedText)
		}
		for _, t := range ev.ActiveTools {
			m.chat = m.chat.AddActiveTool(t.ID, t.Name)
			if m.toolNames == nil {
				m.toolNames = make(map[string]string)
			}
			if m.toolArgs == nil {
				m.toolArgs = make(map[string]map[string]any)
			}
			m.toolNames[t.ID] = t.Name
			m.toolArgs[t.ID] = t.Args
		}
		if ev.PendingPermission != nil {
			m.dialog = dialogs.NewConfirmDialog(protocol.PermissionRequested{
				ToolName: ev.PendingPermission.ToolName,
				Command:  ev.PendingPermission.Command,
				Reason:   ev.PendingPermission.Reason,
			}).WithSize(m.width, m.height)
		}
		if ev.Busy || len(ev.ActiveTools) > 0 || ev.PendingPermission != nil {
			m.relayout()
		}
	case protocol.SubagentStarted:
		m.subagents = m.subagents.HandleEvent(ev)
	case protocol.SubagentEvent:
		m.subagents = m.subagents.HandleEvent(ev)
	case protocol.SubagentFinished:
		m.subagents = m.subagents.HandleEvent(ev)
	case protocol.BgStarted, protocol.BgOutput, protocol.BgExited:
		// Desktop shell sessions panel; TUI ignores for now.
	}
	return m
}

func (m Model) View() string {
	if m.dialog != nil {
		return m.dialog.View()
	}
	return m.mainView()
}

func (m Model) mainView() string {
	body := (&m.chat).View()
	var sections []string
	sections = append(sections, body)
	if m.busy {
		sections = append(sections, m.thinkingLine())
	}
	if comps := m.renderCompletions(); comps != "" {
		sections = append(sections, comps)
	}
	if fp := m.filePicker.render(m.width); fp != "" {
		sections = append(sections, fp)
	}
	sections = append(sections, m.promptBoxView())
	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

// thinkingMessages are cycled while the model is busy (one per 4 seconds).
var thinkingMessages = []string{
	"Brewing tokens...",
	"Weaving context...",
	"Churning through thoughts...",
	"Distilling ideas...",
	"Assembling a response...",
	"Connecting the dots...",
	"Thinking hard...",
	"Working on it...",
}

// thinkingLine returns the animated indicator shown above the status bar while busy.
func (m Model) thinkingLine() string {
	elapsed := time.Since(m.busySince)
	idx := (int(elapsed.Seconds()) / 4) % len(thinkingMessages)
	msg := thinkingMessages[idx]
	content := m.spinner.View() + "  " + styles.ThinkingMsgStyle.Render(msg)
	return styles.ThinkingLineStyle.Width(m.width).Render(content)
}

// shortenDir replaces the home directory prefix with ~.
func shortenDir(cwd string) string {
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(cwd, home) {
		return "~" + cwd[len(home):]
	}
	return cwd
}

// statusParts returns the left and right styled strings for the prompt box header.
func (m Model) statusParts() (left, right string) {
	sep := styles.StatusSepStyle.Render(" · ")

	level := m.thinkingLevel
	if level == "" {
		level = "off"
	}
	provider := m.cfg.Provider
	if provider == "" {
		provider = "?"
	}
	model := m.cfg.Model
	if model == "" {
		model = "?"
	}

	var lp []string
	lp = append(lp, styles.StatusDirStyle.Render(shortenDir(m.cwd)))
	lp = append(lp, styles.StatusModelStyle.Render(provider+"/"+model))
	lp = append(lp, styles.StatusLevelStyle.Render(level))
	if m.permissionMode != "" {
		lp = append(lp, styles.StatusPermStyle.Render(m.permissionMode))
	}
	if m.mcpCount > 0 {
		lp = append(lp, styles.StatusMCPStyle.Render(fmt.Sprintf("mcp:%d", m.mcpCount)))
	}
	if active := m.subagents.ActiveCount(); active > 0 {
		lp = append(lp, styles.StatusDirStyle.Render(fmt.Sprintf("sub:%d", active)))
	}
	if m.busy && !m.busySince.IsZero() {
		elapsed := time.Since(m.busySince)
		lp = append(lp, styles.StatusTimeStyle.Render(fmt.Sprintf("%.0fs", elapsed.Seconds())))
	}
	if val := m.input.Value(); val != "" {
		lp = append(lp, styles.StatusTokStyle.Render("~"+humanCount(max(1, len(val)/4))+" tok"))
	}
	left = strings.Join(lp, sep)

	var rp []string
	if m.contextUsed > 0 || m.contextWin > 0 {
		if m.contextWin > 0 {
			pct := float64(m.contextUsed) / float64(m.contextWin) * 100
			rp = append(rp, styles.StatusPctStyle.Render(fmt.Sprintf("%.1f%%", pct))+
				" "+styles.StatusSepStyle.Render(fmt.Sprintf("(%s/%s)", humanCount(m.contextUsed), humanCount(m.contextWin))))
		} else {
			rp = append(rp, styles.StatusPctStyle.Render(humanCount(m.contextUsed)))
		}
	}
	// Live streaming estimates: add estimated tokens from the current turn's
	// streamed text so the user sees output/cost grow in real-time.
	liveOut := m.streamTextLen / 4
	showOut := m.totalOutput + liveOut
	showCost := m.totalCost
	if liveOut > 0 && m.cfg.Pricing != nil && m.cfg.Model != "" {
		showCost += m.cfg.Pricing.Cost(m.cfg.Model, 0, liveOut)
	}
	if showCost > 0 {
		rp = append(rp, styles.StatusCostStyle.Render(fmt.Sprintf("$%.3f", showCost)))
	}
	if showOut > 0 {
		rp = append(rp, styles.StatusOutStyle.Render("OUT "+humanCount(showOut)))
	}
	if m.totalCacheMiss > 0 {
		rp = append(rp, styles.StatusMissStyle.Render("MISS "+humanCount(m.totalCacheMiss)))
	}
	if m.quitConfirm {
		rp = append(rp, styles.VimNormalStyle.Render("ctrl+c again to exit"))
	}
	if m.escClearConfirm {
		rp = append(rp, styles.VimNormalStyle.Render("esc again to clear"))
	}
	right = strings.Join(rp, sep)
	return
}

// promptBoxView renders the entire bottom area as a single rounded box:
//
//	╭─ dir · model · thinking · elapsed · ~X tok ─── stats ─╮
//	│ > input text here                                        │
//	╰──────────────────────────────────────────────────────────╯
func (m Model) promptBoxView() string {
	b := styles.StatusSepStyle // border character color

	leftStatus, rightStatus := m.statusParts()
	lw := lipgloss.Width(leftStatus)
	rw := lipgloss.Width(rightStatus)

	// Top border: ╭─ leftStatus ─────── rightStatus ─╮
	// Fixed chars: "╭─ " (3) + " " (1) + dashes + " " (1) + " ─╮" (3) = 8 + lw + fillN + rw = m.width.
	// On a narrow terminal the two statuses can exceed the available room; if we
	// let them, the border overflows m.width and the terminal wraps it, which
	// desyncs the renderer and ghosts a stale bar. Truncate to fit instead:
	// keep whichever side is shorter intact and shrink the longer one, splitting
	// evenly when both are long. budget leaves room for the fixed chars + 1 dash.
	budget := m.width - 9
	if budget < 1 {
		budget = 1
	}
	if lw+rw > budget {
		half := budget / 2
		var lBudget, rBudget int
		switch {
		case lw <= half:
			lBudget, rBudget = lw, budget-lw
		case rw <= half:
			lBudget, rBudget = budget-rw, rw
		default:
			lBudget, rBudget = budget-half, half
		}
		leftStatus = ansi.Truncate(leftStatus, lBudget, "…")
		rightStatus = ansi.Truncate(rightStatus, rBudget, "…")
		lw = lipgloss.Width(leftStatus)
		rw = lipgloss.Width(rightStatus)
	}

	fillN := m.width - 8 - lw - rw
	if fillN < 1 {
		fillN = 1
	}
	topBorder := b.Render("╭─ ") + leftStatus +
		b.Render(" "+strings.Repeat("─", fillN)+" ") +
		rightStatus + b.Render(" ─╮")

	// Input area: one "│ > line │" row per visual line of the (soft-wrapping)
	// textarea. The ">" glyph is drawn only on the first row; wrapped/continuation
	// rows get a blank gutter so the text keeps aligned inside the box.
	// Fixed: "│" (1) + " " (1) + glyph (1) + " " (1) + content + "│" (1) = 5 + inputW = m.width
	inputW := m.width - 5
	if inputW < 1 {
		inputW = 1
	}
	glyph := styles.StatusModelStyle.Render(">")
	blank := styles.StatusModelStyle.Render(" ")
	pad := lipgloss.NewStyle().Width(inputW).MaxWidth(inputW)
	// The textarea itself always renders at the (taller) cap height — see the
	// comment in relayout() — so trim its output down to the rows relayout()
	// determined actually hold content.
	rendered := strings.Split(m.input.View(), "\n")
	if len(rendered) > m.inputRows {
		rendered = rendered[:m.inputRows]
	}
	var inputRows []string
	for i, ln := range rendered {
		g := glyph
		if i > 0 {
			g = blank
		}
		inputRows = append(inputRows, b.Render("│")+" "+g+" "+pad.Render(ln)+b.Render("│"))
	}
	inputArea := strings.Join(inputRows, "\n")

	// Bottom border: ╰──────────╯
	bottomDashes := m.width - 2
	if bottomDashes < 0 {
		bottomDashes = 0
	}
	bottomBorder := b.Render("╰" + strings.Repeat("─", bottomDashes) + "╯")

	return lipgloss.JoinVertical(lipgloss.Left, topBorder, inputArea, bottomBorder)
}

func humanCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

type dialog interface {
	tea.Model
	IsDismissed() bool
}

var _ dialog = (*dialogs.ConfirmDialog)(nil)
var _ dialog = (*dialogs.ModelPickerDialog)(nil)
var _ dialog = (*dialogs.LoginDialog)(nil)

// pasteClipboardImage tries to read a PNG image from the system clipboard,
// writes it to a temp file, and returns the @path string to insert into the
// input, or "" if no image was found.
func pasteClipboardImage() string {
	tmp, err := os.CreateTemp("", "talos-clipboard-*.png")
	if err != nil {
		return ""
	}
	tmp.Close()
	path := tmp.Name()

	// Try Wayland first, then X11.
	cmds := [][]string{
		{"wl-paste", "--type", "image/png"},
		{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"},
	}
	for _, args := range cmds {
		out, err := exec.Command(args[0], args[1:]...).Output()
		if err == nil && len(out) > 0 {
			if writeErr := os.WriteFile(path, out, 0600); writeErr == nil {
				return "@" + path
			}
		}
	}
	os.Remove(path)
	return ""
}
