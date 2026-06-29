package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

// focusedPane tracks which pane receives j/k scroll in normal mode.
type focusedPane int

const (
	focusChat focusedPane = iota
	focusTools
	focusSubagents
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
	SessionID   string
	Mode        Mode
	InputCh     chan<- []protocol.ContentBlock
	// SteerQueue is a thread-safe queue shared with the Loop for steer
	// messages (typed while busy). The TUI enqueues on Enter and withdraws
	// on up-arrow; the Loop drains before each LLM call. Nil disables steer.
	SteerQueue *SteerQueue
	// SubmitFn, if set, is called with the raw user text on every input
	// submission (after slash-command handling). Used by attach mode to
	// forward input to a remote server instead of a local loop.
	SubmitFn    func(string)
	// SubmitSlash, if set, is called when the user types a slash command
	// that the client cannot handle locally. Used by attach mode to forward
	// commands like /model and /thinking to the server.
	SubmitSlash func(string)
	// InterruptFn, if set, is called when the user interrupts a busy turn.
	// Used by attach mode to forward interrupts to a remote server.
	InterruptFn func()
	InterruptCh chan<- struct{}
	NewSession  func() (string, error)
	Stats       func() string // returns formatted stats string for /stats
	// ResumeSession loads an existing session by id, sets it on the loop, and
	// returns the new session ID plus the transcript history for replay.
	ResumeSession func(id string) (string, []protocol.FrozenMessage, error)
	Provider      string
	Model         string
	// SwitchProvider creates a new provider client and updates the loop.
	SwitchProvider func(name, model string) error
	// CycleThinking cycles to the next abstract thinking level (off→minimal→low→medium→high→xhigh→off)
	// and returns the new level name for display.
	CycleThinking func() string
	// CurrentThinkingLevel returns the current abstract thinking level without cycling.
	CurrentThinkingLevel func() string
	// FetchSessions returns sessions for the current project (used by /resume picker).
	FetchSessions dialogs.FetchSessionsFunc
	// DeleteSession removes a session by ID (used by /resume picker's d key).
	DeleteSession func(id string) error
	// FetchModels fetches live models across all logged-in providers.
	FetchModels dialogs.FetchModelsFunc
	// LoginProviders is the list of known providers shown in the /login dialog.
	LoginProviders func() []dialogs.LoginProvider
	// SaveLogin persists a new API key for a provider.
	SaveLogin func(provider, key string) error
	// CancelSubagent cancels a running subagent by ID. Used when the user kills
	// one from the subagents pane.
	CancelSubagent func(id string)
	// Pricing is the pricing table used to compute dollar costs and context
	// windows for the token/cost status line. Nil disables cost display.
	Pricing *pricing.Table
	// CompactCh receives focus strings for manual compaction. An empty string
	// means compact without focus guidance. The compaction happens
	// asynchronously in the engine goroutine and emits events back through
	// the normal event channel.
	CompactCh chan<- string
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

	// StatsSnapshot returns the current cumulative stats from the loop.
	// Used by /resume to refresh the TUI counters after switching transcripts.
	StatsSnapshot func() (input, output, cacheMiss int, cost float64)
}

// SteerQueue is a thread-safe queue of pending steer messages shared between
// the TUI and the Loop. The TUI enqueues on Enter-while-busy and withdraws on
// up-arrow; the Loop drains before each LLM call.
type SteerQueue struct {
	mu       sync.Mutex
	Messages [][]protocol.ContentBlock
}

// Enqueue appends a steer message. Called from the TUI goroutine.
func (q *SteerQueue) Enqueue(blocks []protocol.ContentBlock) {
	q.mu.Lock()
	q.Messages = append(q.Messages, blocks)
	q.mu.Unlock()
}

// Drain returns all pending messages and clears the queue. Called from the
// loop goroutine. Returns nil if the queue is empty.
func (q *SteerQueue) Drain() [][]protocol.ContentBlock {
	q.mu.Lock()
	msgs := q.Messages
	q.Messages = nil
	q.mu.Unlock()
	return msgs
}

// Withdraw removes and returns the last pending message, or nil if empty.
// Called from the TUI goroutine (up-arrow withdrawal).
func (q *SteerQueue) Withdraw() []protocol.ContentBlock {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.Messages)
	if n == 0 {
		return nil
	}
	last := q.Messages[n-1]
	q.Messages = q.Messages[:n-1]
	return last
}

// Len returns the number of pending steer messages. Called from TUI.
func (q *SteerQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.Messages)
}

// StatsSnapshot returns the current cumulative stats from the loop.
// Used by /resume to refresh the TUI counters after switching transcripts.
type StatsSnapshot struct {
	InputTokens  int
	OutputTokens int
	CacheMiss    int
	Cost         float64
}

// Model is the root Bubble Tea model.
type Model struct {
	cfg           Config
	mode          Mode
	vimMode       VimMode
	focusPane     focusedPane
	quitConfirm    bool
	escClearConfirm bool
	width          int
	height         int
	paneH          int // height allocated to panes, updated in relayout
	chat      panes.ChatModel
	tools     panes.ToolsModel
	subagents panes.SubagentsModel
	input     textinput.Model
	busy      bool
	spinner       spinner.Model
	dialog        tea.Model
	thinkingLevel string

	// toolNames maps tool call ID → name so ToolFinished can look up the name.
	toolNames map[string]string
	// toolArgs maps tool call ID → args so the inline chat entry can show the
	// call descriptor (path/command/query) when the tool finishes.
	toolArgs map[string]map[string]any

	// Cumulative usage across all turns (shown in the status line).
	totalInput    int
	totalOutput   int
	totalCost     float64
	totalCacheMiss int // cumulative non-cached prompt tokens
	contextUsed   int  // latest turn's prompt tokens = current context size
	contextWin    int

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
}

// NewModel builds the initial TUI model.
func NewModel(cfg Config) Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = "Type a message..."
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	cwd, _ := os.Getwd()
	m := Model{
		cfg:              cfg,
		mode:             cfg.Mode,
		chat:             panes.NewChat(),
		tools:            panes.NewTools(),
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
		totalInput:       cfg.SeedStats.InputTokens,
		totalOutput:      cfg.SeedStats.OutputTokens,
		totalCacheMiss:   cfg.SeedStats.CacheMiss,
		totalCost:        cfg.SeedStats.Cost,
	}
	if cfg.CurrentThinkingLevel != nil {
		m.thinkingLevel = cfg.CurrentThinkingLevel()
	}
	// Seed the context window from the pricing table so the status bar shows
	// context usage from the very first turn instead of waiting for TurnEnded.
	if cfg.Pricing != nil && cfg.Model != "" {
		m.contextWin = cfg.Pricing.ContextWindow(cfg.Model)
	}

	// Replay any pre-loaded transcript into the chat/tools panes. This is what
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
		textinput.Blink,
		m.spinner.Tick,
		m.tools.Init(),
		m.subagents.Init(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Active dialog gets first crack at every message.
	if m.dialog != nil {
		newDlg, cmd := m.dialog.Update(msg)
		if d, ok := newDlg.(dialog); ok && d.IsDismissed() {
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
			if m.cfg.SaveLogin != nil {
				if err := m.cfg.SaveLogin(msg.Provider, msg.Key); err != nil {
					m.chat = m.chat.AppendNotice("error", err.Error())
				} else {
					m.chat = m.chat.AppendNotice("info", "logged in to "+msg.Provider)
					// Recreate the provider so the new key takes effect immediately.
					if m.cfg.SwitchProvider != nil {
						if err := m.cfg.SwitchProvider(m.cfg.Provider, m.cfg.Model); err != nil {
							m.chat = m.chat.AppendNotice("error", err.Error())
						}
					}
				}
			}
		}
		return m, nil

	case dialogs.SessionPickerDoneMsg:
		if !msg.Canceled && msg.ID != "" && m.cfg.ResumeSession != nil {
			newID, history, err := m.cfg.ResumeSession(msg.ID)
			if err != nil {
				m.chat = m.chat.AppendNotice("error", err.Error())
			} else {
				m.chat = panes.NewChat()
				m.tools = panes.NewTools()
				m.toolNames = make(map[string]string)
				m.toolArgs = make(map[string]map[string]any)
				m.busy = false
				m.cfg.SessionID = newID
				m.relayout()
				m.replayTranscript(history)
				m.reseedStats()
			}
		}
		return m, nil

	case dialogs.ModelPickerDoneMsg:
		if !msg.Canceled && msg.Provider != "" {
			if m.cfg.SwitchProvider != nil {
				if err := m.cfg.SwitchProvider(msg.Provider, msg.Model); err != nil {
					m.chat = m.chat.AppendNotice("error", err.Error())
				} else {
					m.cfg.Provider = msg.Provider
					m.cfg.Model = msg.Model
				}
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
				m.input.SetCursor(len(m.input.Value()))
			}
			return m, nil
		}

		// ctrl+c: interrupt if busy; otherwise double-press to quit.
		if msg.String() == "ctrl+c" {
			if m.busy {
				if m.cfg.InterruptCh != nil {
					select {
					case m.cfg.InterruptCh <- struct{}{}:
					default:
					}
				} else if m.cfg.InterruptFn != nil {
					m.cfg.InterruptFn()
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
			// While kill-confirm is pending on the subagents pane, only y/n/esc
			// are accepted; all other navigation is suppressed.
			if m.focusPane == focusSubagents && m.subagents.KillConfirmActive() {
				switch msg.String() {
				case "y", "Y":
					id := m.subagents.SelectedID()
					m.subagents = m.subagents.KillConfirmCancel()
					if id != "" && m.cfg.CancelSubagent != nil {
						m.cfg.CancelSubagent(id)
					}
				case "n", "N", "esc":
					m.subagents = m.subagents.KillConfirmCancel()
				}
				return m, nil
			}

			switch msg.String() {
			case "i":
				m.vimMode = InsertMode
				m.input.Focus()
			case "h":
				m.cycleFocus(false)
			case "l":
				m.cycleFocus(true)
			case "j":
				switch m.focusPane {
				case focusTools:
					m.tools = m.tools.CursorDown()
				case focusSubagents:
					m.subagents = m.subagents.CursorDown()
				default:
					m.chat = m.chat.ScrollDown(1)
				}
			case "k":
				switch m.focusPane {
				case focusTools:
					m.tools = m.tools.CursorUp()
				case focusSubagents:
					m.subagents = m.subagents.CursorUp()
				default:
					m.chat = m.chat.ScrollUp(1)
				}
			case "enter":
				switch m.focusPane {
				case focusTools:
					m.tools = m.tools.ToggleExpand()
				case focusSubagents:
					m.subagents = m.subagents.ToggleExpand()
				}
			case "d":
				if m.focusPane == focusSubagents && m.subagents.SelectedIsRunning() {
					m.subagents = m.subagents.KillConfirmStart()
				}
			case "g":
				m.chat = m.chat.ScrollTop()
			case "G":
				m.chat = m.chat.ScrollBottom()
			}
			return m, nil
		}

		// Insert mode key handling.
		switch msg.String() {
		case "esc":
			if m.input.Value() == "" {
				// No text to clear — act as normal blur / vim normal mode.
				m.vimMode = NormalMode
				m.input.Blur()
				return m, nil
			}
			if m.escClearConfirm {
				// Second esc: clear the prompt bar.
				m.input.SetValue("")
				m.escClearConfirm = false
				m.slashCompletions = nil
				m.slashSelected = -1
				m.relayout()
				return m, nil
			}
			// First esc with non-empty input: show clear hint.
			m.escClearConfirm = true
			return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return clearEscClearConfirmMsg{} })
		case "ctrl+l":
			if !m.busy {
				if m.cfg.FetchModels == nil {
					m.chat = m.chat.AppendNotice("info", "model selection is managed on the server terminal")
					return m, nil
				}
				m.dialog = dialogs.NewModelPickerDialog(m.cfg.Provider, m.cfg.Model, "", m.cfg.FetchModels)
				return m, m.dialog.Init()
			}
			return m, nil
		case "ctrl+t":
			if !m.busy {
				if m.cfg.CycleThinking != nil {
					newLevel := m.cfg.CycleThinking()
					m.thinkingLevel = newLevel
					m.chat = m.chat.AppendNotice("info", "thinking level: "+newLevel)
				} else if m.cfg.SubmitSlash != nil {
					m.cfg.SubmitSlash("/thinking")
				}
			}
			return m, nil
		case "tab":
			if len(m.slashCompletions) > 0 {
				m.slashSelected = (m.slashSelected + 1) % len(m.slashCompletions)
				return m, nil
			}
			if m.busy && (m.tools.Count() > 0 || m.subagents.Count() > 0) {
				m.cycleFocus(true)
			}
			return m, nil
		case "shift+tab":
			if len(m.slashCompletions) > 0 {
				m.slashSelected = (m.slashSelected - 1 + len(m.slashCompletions)) % len(m.slashCompletions)
				return m, nil
			}
			if m.busy && (m.tools.Count() > 0 || m.subagents.Count() > 0) {
				m.cycleFocus(false)
			}
			return m, nil
		case "enter":
			if m.busy && m.focusPane == focusTools && m.tools.Count() > 0 {
				m.tools = m.tools.ToggleExpand()
				return m, nil
			}
			if m.busy && m.focusPane == focusSubagents && m.subagents.Count() > 0 {
				m.subagents = m.subagents.ToggleExpand()
				return m, nil
			}
			if m.busy {
				// While busy, Enter queues as a "steer" message — it gets
				// injected after the current tool calls finish but before the
				// next LLM streaming call (like pi's steer mechanism).
				// Pending steers are withdrawable via up-arrow (they pop back
				// into the input bar so you can edit or discard them).
				text := m.input.Value()
				if text != "" && m.cfg.SteerQueue != nil {
					blocks, _ := resolveInput(text)
					m.chat = m.chat.AppendUserBlocks(blocks)
					m.cfg.SteerQueue.Enqueue(blocks)
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
						m.input.SetCursor(len(completed))
						m.slashCompletions = nil
						m.slashSelected = -1
						m.relayout()
						return m, nil
					}
				}
				// Clear completions and submit.
				m.slashCompletions = nil
				m.slashSelected = -1
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
						if m.dialog != nil {
							return m, m.dialog.Init()
						}
						return m, nil // handled but no dialog (e.g. /new)
					}
					// Save to input history (non-slash messages only).
					m.inputHistory = append(m.inputHistory, text)
					m.historyIdx = -1
					m.historyDraft = ""
					blocks, _ := resolveInput(text)
					m.chat = m.chat.AppendUserBlocks(blocks)
					if m.cfg.InputCh != nil {
						m.busy = true
						m.busySince = time.Now()
						m.relayout()
						m.cfg.InputCh <- blocks
					} else if m.cfg.SubmitFn != nil {
						m.busy = true
						m.busySince = time.Now()
						m.relayout()
						m.cfg.SubmitFn(text)
					}
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
				if m.cfg.SteerQueue != nil {
					if last := m.cfg.SteerQueue.Withdraw(); last != nil {
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
						m.input.SetCursor(len(text))
						m.escClearConfirm = false
						return m, nil
					}
				}
				// No pending steer — scroll behavior as before.
				switch {
				case m.focusPane == focusTools && m.tools.Count() > 0:
					m.tools = m.tools.CursorUp()
				case m.focusPane == focusSubagents && m.subagents.Count() > 0:
					m.subagents = m.subagents.CursorUp()
				default:
					m.chat = m.chat.ScrollUp(1)
				}
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
			m.input.SetCursor(len(m.inputHistory[m.historyIdx]))
			m.escClearConfirm = false
			return m, nil
		case "down":
			if len(m.slashCompletions) > 0 {
				m.slashSelected = (m.slashSelected + 1) % len(m.slashCompletions)
				return m, nil
			}
			if m.busy {
				switch {
				case m.focusPane == focusTools && m.tools.Count() > 0:
					m.tools = m.tools.CursorDown()
				case m.focusPane == focusSubagents && m.subagents.Count() > 0:
					m.subagents = m.subagents.CursorDown()
				default:
					m.chat = m.chat.ScrollDown(1)
				}
				return m, nil
			}
			// Not busy, no completions: cycle input history forward.
			if len(m.inputHistory) == 0 {
				return m, nil
			}
			if m.historyIdx < len(m.inputHistory)-1 {
				m.historyIdx++
				m.input.SetValue(m.inputHistory[m.historyIdx])
				m.input.SetCursor(len(m.inputHistory[m.historyIdx]))
			} else {
				m.historyIdx = -1
				m.input.SetValue(m.historyDraft)
				m.input.SetCursor(len(m.historyDraft))
				m.historyDraft = ""
			}
			m.escClearConfirm = false
			return m, nil
		default:
			m.escClearConfirm = false
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.updateSlashCompletions()
			return m, cmd
		}

	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonLeft:
			if msg.Y >= 0 && msg.Y < m.paneH {
				chatW := m.width * 7 / 10
				// Click in the right column toggles the item under the cursor.
				if msg.X >= chatW+1 {
					m.handlePaneClick(msg.Y)
					return m, nil
				}
				// Click in the chat focuses it.
				if msg.X >= 0 && msg.X < chatW {
					m.focusPane = focusChat
					return m, nil
				}
			}
		case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
			up := msg.Button == tea.MouseButtonWheelUp
			// Route to tools pane when mouse is over it (right 30% when tools visible).
			if m.tools.Count() > 0 {
				chatW := m.width * 7 / 10
				if msg.X >= chatW+1 {
					if up {
						m.tools = m.tools.ScrollUp(1)
					} else {
						m.tools = m.tools.ScrollDown(1)
					}
					return m, nil
				}
			}
			if up {
				m.chat = m.chat.ScrollUp(1)
			} else {
				m.chat = m.chat.ScrollDown(1)
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
			// Each spinner instance carries a unique ID and rejects TickMsgs
			// whose ID doesn't match. Sub-model spinners (tools, subagents)
			// never receive their own ticks because (a) their Init() tick
			// arrives while m.busy is false, and (b) their returned commands
			// are discarded.  Send a broadcast tick (ID=0, tag=0) that all
			// spinners accept so the sub-model animations actually advance.
			broadcast := spinner.TickMsg{}
			m.tools, _ = m.tools.Update(broadcast)
			m.subagents, _ = m.subagents.Update(broadcast)
			m.chat, _ = m.chat.Update(broadcast)
		}
		return m, cmd
	}

	// Allow panes to handle their own updates (viewport scrolling, etc.).
	var chatCmd, toolsCmd tea.Cmd
	m.chat, chatCmd = m.chat.Update(msg)
	m.tools, toolsCmd = m.tools.Update(msg)
	return m, tea.Batch(chatCmd, toolsCmd)
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
					first := m.tools.Count() == 0
					m.tools = m.tools.AddTool(tu.ID, tu.Name, tu.Args)
					if first {
						m.relayout()
					}
					if result, ok := toolResults[tu.ID]; ok {
						m.tools = m.tools.FinishTool(tu.ID, result)
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

// reseedStats refreshes the cumulative token and cost counters from the
// loop's StatsSnapshot callback. Used after /resume to show the loaded
// session's historical stats instead of stale values from the old session.
func (m *Model) reseedStats() {
	if m.cfg.StatsSnapshot != nil {
		in, out, miss, cost := m.cfg.StatsSnapshot()
		m.totalInput = in
		m.totalOutput = out
		m.totalCacheMiss = miss
		m.totalCost = cost
	}
}

func (m *Model) relayout() {
	// Layout: panes + (if busy: thinking line = 1) + rounded prompt box (3) + completions.
	// The box folds the old separate status line and input border into a single 3-line unit.
	thinkingH := 0
	if m.busy {
		thinkingH = 1
	}
	paneH := m.height - 3 - thinkingH - m.completionHeight()

	// Tell the textinput how much room it has so it handles horizontal scrolling.
	inputContentW := m.width - 5 // │ > [content] │
	if inputContentW < 1 {
		inputContentW = 1
	}
	m.input.Width = inputContentW
	if paneH < 1 {
		paneH = 1
	}
	m.paneH = paneH
	if m.tools.Count() > 0 || m.subagents.Count() > 0 {
		// Split into chat + right column once a tool has run or a subagent spawned.
		chatW := m.width * 7 / 10
		rightW := m.width - chatW - 1
		m.chat.SetSize(chatW, paneH)
		switch {
		case m.tools.Count() > 0 && m.subagents.Count() > 0:
			// Stack tools over subagents in the right column.
			toolsH := paneH / 2
			subsH := paneH - toolsH
			m.tools.SetSize(rightW, toolsH)
			m.subagents.SetSize(rightW, subsH)
		case m.subagents.Count() > 0:
			m.subagents.SetSize(rightW, paneH)
		default:
			m.tools.SetSize(rightW, paneH)
		}
	} else {
		m.chat.SetSize(m.width, paneH)
	}
}

// clipHeight truncates s to at most n lines. lipgloss's Height() only pads
// content up to n lines — it never clips — so we enforce the ceiling here to
// keep each pane truly independent of the other's content length.
func clipHeight(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
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
		if m.cfg.NewSession != nil {
			newID, err := m.cfg.NewSession()
			if err != nil {
				return true, err
			}
			m.cfg.SessionID = newID
			m.chat = panes.NewChat()
			m.tools = panes.NewTools()
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
		}
		return true, nil
	case "/resume":
		if m.cfg.ResumeSession == nil {
			return true, fmt.Errorf("[/resume is unavailable]")
		}
		if len(parts) > 1 {
			// ID supplied directly — resume immediately.
			newID, history, err := m.cfg.ResumeSession(parts[1])
			if err != nil {
				return true, err
			}
			m.chat = panes.NewChat()
			m.tools = panes.NewTools()
			m.toolNames = make(map[string]string)
			m.toolArgs = make(map[string]map[string]any)
			m.busy = false
			m.cfg.SessionID = newID
			m.relayout()
			m.replayTranscript(history)
			return true, nil
		}
		// No ID: open the session picker dialog.
		dlg := dialogs.NewSessionPickerDialog(m.cfg.FetchSessions)
		if m.cfg.DeleteSession != nil {
			dlg = dlg.WithDeleteFn(m.cfg.DeleteSession)
		}
		m.dialog = dlg
		return true, nil
	case "/restore", "/undo":
		return true, fmt.Errorf("%s not yet implemented in TUI", text)
	case "/login":
		if m.cfg.LoginProviders == nil {
			return true, fmt.Errorf("login not available in this mode")
		}
		m.dialog = dialogs.NewLoginDialog(m.cfg.LoginProviders())
		return true, nil
	case "/stats":
		if m.cfg.Stats != nil {
			m.chat = m.chat.AppendNotice("info", m.cfg.Stats())
		} else {
			m.chat = m.chat.AppendNotice("error", "stats unavailable")
		}
		return true, nil
	case "/model":
		if m.cfg.FetchModels != nil {
			query := ""
			if len(parts) >= 2 {
				query = strings.Join(parts[1:], " ")
			}
			m.dialog = dialogs.NewModelPickerDialog(m.cfg.Provider, m.cfg.Model, query, m.cfg.FetchModels)
			return true, nil
		}
		if m.cfg.SubmitSlash != nil {
			m.cfg.SubmitSlash(text)
			return true, nil
		}
		m.chat = m.chat.AppendNotice("info", "model selection is managed on the server terminal")
		return true, nil
	case "/thinking":
		if m.cfg.CycleThinking != nil {
			newLevel := m.cfg.CycleThinking()
			m.chat = m.chat.AppendNotice("info", "thinking level: "+newLevel)
			return true, nil
		}
		if m.cfg.SubmitSlash != nil {
			m.cfg.SubmitSlash(text)
			return true, nil
		}
		m.chat = m.chat.AppendNotice("info", "thinking level is managed on the server terminal")
		return true, nil
	case "/push":
		msg, notice := pushInstruction()
		if notice != "" {
			m.chat = m.chat.AppendNotice("info", notice)
		}
		if msg != "" {
			if m.cfg.InputCh != nil {
				m.cfg.InputCh <- protocol.TextBlocks(msg)
			} else if m.cfg.SubmitFn != nil {
				m.cfg.SubmitFn(msg)
			}
		}
		return true, nil
	case "/compact":
		focus := ""
		if len(parts) >= 2 {
			focus = strings.Join(parts[1:], " ")
		}
		if m.cfg.CompactCh != nil {
			select {
			case m.cfg.CompactCh <- focus:
			default:
				m.chat = m.chat.AppendNotice("error", "compaction request dropped (busy)")
			}
		}
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
	case protocol.UserInput:
		// In attach mode (SubmitFn set), the enter handler already appended
		// non-slash messages locally for instant feedback. The server echoes
		// them back as UserInput — skip the duplicate. Slash commands *are*
		// needed here because they're forwarded via SubmitSlash and the local
		// handler doesn't append them (they'd never appear otherwise).
		if m.cfg.SubmitFn != nil && !strings.HasPrefix(ev.Text, "/") {
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
	case protocol.TextDelta:
		m.chat = m.chat.AppendDelta(ev.Text)
		m.streamTextLen += len(ev.Text)
	case protocol.BatchStarted:
		m.chat = m.chat.FlushStreaming()
		m.chat = m.chat.AppendBatchHeading(ev.Num)
	case protocol.ToolStarted:
		m.chat = m.chat.FlushStreaming()
		m.chat = m.chat.AddActiveTool(ev.ID, ev.Name)
		if m.toolNames == nil {
			m.toolNames = make(map[string]string)
		}
		if m.toolArgs == nil {
			m.toolArgs = make(map[string]map[string]any)
		}
		m.toolNames[ev.ID] = ev.Name
		m.toolArgs[ev.ID] = ev.Args
		first := m.tools.Count() == 0
		m.tools = m.tools.AddTool(ev.ID, ev.Name, ev.Args)
		if first {
			// First tool just appeared: shrink the chat to make room.
			m.relayout()
		}
	case protocol.ToolFinished:
		m.chat = m.chat.RemoveActiveTool(ev.ID)
		m.tools = m.tools.FinishTool(ev.ID, ev.Result)
		if name, ok := m.toolNames[ev.ID]; ok {
			m.chat = m.chat.AppendToolUse(name, m.toolArgs[ev.ID], !ev.Result.IsError)
			delete(m.toolNames, ev.ID)
			delete(m.toolArgs, ev.ID)
		}
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
		m.streamTextLen = 0
		m.relayout()
	case protocol.PermissionRequested:
		m.dialog = dialogs.NewConfirmDialog(ev)
	case protocol.SubagentStarted:
		first := m.subagents.Count() == 0
		m.subagents = m.subagents.HandleEvent(ev)
		if first {
			// First subagent appeared: split the right column to make room.
			m.relayout()
		}
	case protocol.SubagentEvent:
		m.subagents = m.subagents.HandleEvent(ev)
	case protocol.SubagentFinished:
		m.subagents = m.subagents.HandleEvent(ev)
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
	var body string
	if m.tools.Count() > 0 || m.subagents.Count() > 0 {
		rightFocused := m.vimMode == NormalMode && (m.focusPane == focusTools || m.focusPane == focusSubagents)
		sepColor := lipgloss.Color("238")
		if rightFocused {
			sepColor = lipgloss.Color("39")
		}
		sep := lipgloss.NewStyle().Foreground(sepColor).Render(panes.VerticalRule(m.paneH))
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			clipHeight(m.chat.View(), m.paneH),
			sep,
			clipHeight(m.rightColumn(), m.paneH),
		)
	} else {
		body = m.chat.View()
	}
	var sections []string
	sections = append(sections, body)
	if m.busy {
		sections = append(sections, m.thinkingLine())
	}
	if comps := m.renderCompletions(); comps != "" {
		sections = append(sections, comps)
	}
	sections = append(sections, m.promptBoxView())
	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

// rightColumn renders the chat-adjacent column: the tools pane, the subagents
// pane, or both stacked (tools over subagents). The focused pane renders in its
// focused (navigable) variant.
func (m Model) rightColumn() string {
	toolsOn := m.tools.Count() > 0
	subsOn := m.subagents.Count() > 0
	toolsView := func() string {
		if m.vimMode == NormalMode && m.focusPane == focusTools {
			return m.tools.ViewFocused()
		}
		return m.tools.View()
	}
	subsView := func() string {
		if m.vimMode == NormalMode && m.focusPane == focusSubagents {
			return m.subagents.ViewFocused()
		}
		return m.subagents.View()
	}
	switch {
	case toolsOn && subsOn:
		return lipgloss.JoinVertical(lipgloss.Left, toolsView(), subsView())
	case subsOn:
		return subsView()
	default:
		return toolsView()
	}
}

func (m Model) availFocus() []focusedPane {
	out := []focusedPane{focusChat}
	if m.tools.Count() > 0 {
		out = append(out, focusTools)
	}
	if m.subagents.Count() > 0 {
		out = append(out, focusSubagents)
	}
	return out
}

func (m *Model) cycleFocus(forward bool) {
	av := m.availFocus()
	idx := 0
	for i, f := range av {
		if f == m.focusPane {
			idx = i
		}
	}
	if forward {
		idx = (idx + 1) % len(av)
	} else {
		idx = (idx - 1 + len(av)) % len(av)
	}
	m.focusPane = av[idx]
}

func (m *Model) handlePaneClick(y int) {
	toolsH := m.paneH
	if m.tools.Count() > 0 && m.subagents.Count() > 0 {
		toolsH = m.paneH / 2
	}
	switch {
	case m.tools.Count() > 0 && y < toolsH:
		m.tools = m.tools.Click(y)
		m.focusPane = focusTools
	case m.subagents.Count() > 0 && y >= toolsH:
		m.subagents = m.subagents.Click(y - toolsH)
		m.focusPane = focusSubagents
	}
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
	// Fixed chars: "╭─ " (3) + " " (1) + dashes + " " (1) + " ─╮" (3) = 8 + lw + fillN + rw = m.width
	fillN := m.width - 8 - lw - rw
	if fillN < 1 {
		fillN = 1
	}
	topBorder := b.Render("╭─ ") + leftStatus +
		b.Render(" "+strings.Repeat("─", fillN)+" ") +
		rightStatus + b.Render(" ─╮")

	// Input line: │ > inputContent │
	// Fixed: "│" (1) + " " (1) + ">" (1) + " " (1) + content + "│" (1) = 5 + inputW = m.width
	inputW := m.width - 5
	if inputW < 1 {
		inputW = 1
	}
	prompt := styles.StatusModelStyle.Render(">")
	inputContent := lipgloss.NewStyle().Width(inputW).Render(m.input.View())
	inputLine := b.Render("│") + " " + prompt + " " + inputContent + b.Render("│")

	// Bottom border: ╰──────────╯
	bottomDashes := m.width - 2
	if bottomDashes < 0 {
		bottomDashes = 0
	}
	bottomBorder := b.Render("╰" + strings.Repeat("─", bottomDashes) + "╯")

	return lipgloss.JoinVertical(lipgloss.Left, topBorder, inputLine, bottomBorder)
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

// pushInstruction runs git checks and returns a message to send to the agent
// plus an optional notice for the chat pane. Mirrors pi's /push extension.
func pushInstruction() (msg string, notice string) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Sprintf("push: getwd: %v", err)
	}

	// 1. Check if inside a git repo.
	isGit := false
	if out, err := exec.Command("git", "rev-parse", "--is-inside-work-tree").Output(); err == nil {
		isGit = strings.TrimSpace(string(out)) == "true"
	}

	if !isGit {
		dirName := filepath.Base(cwd)
		ghUser := ""
		if out, err := exec.Command("gh", "api", "user", "--jq", ".login").Output(); err == nil {
			ghUser = strings.TrimSpace(string(out))
		}
		userClause := ""
		if ghUser != "" {
			userClause = " My GitHub username is " + ghUser + "."
		}
		return fmt.Sprintf(
			"Initialize a new git repository named %q in the current directory, add all current files as the initial commit, and create a corresponding public repository on GitHub using 'gh repo create %q --public --source=. --remote=origin --push'.%s",
			dirName, dirName, userClause,
		), ""
	}

	// 2. It is a git repo — check for changes.
	statusOut, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		return "", fmt.Sprintf("push: git status: %v", err)
	}
	changes := strings.TrimSpace(string(statusOut))

	branch := ""
	if out, err := exec.Command("git", "branch", "--show-current").Output(); err == nil {
		branch = strings.TrimSpace(string(out))
	}

	head := ""
	if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		head = strings.TrimSpace(string(out))
	}

	unpushed := 0
	if out, err := exec.Command("git", "rev-list", "--count", "@{u}..HEAD").Output(); err == nil {
		unpushed, _ = strconv.Atoi(strings.TrimSpace(string(out)))
	}

	if changes == "" {
		if unpushed > 0 {
			return fmt.Sprintf(
				"There were %d commit(s) already waiting to be pushed when /push was called. Push those existing commits to the remote repository. The branch is %q and HEAD is %s.",
				unpushed, branch, head,
			), ""
		}
		return "", "push: no changes or unpushed commits"
	}

	// 3. Construct detailed instruction.
	return fmt.Sprintf(
		"I want to handle all current changes and any commits that were already waiting to be pushed when /push was called. Please follow these rules strictly:\n"+
			"1. Ensure .gitignore exists before committing.\n"+
			"2. Examine all changed files (including untracked ones) using git status and git diff.\n"+
			"3. Group files that were changed for the same reason.\n"+
			"4. If changes perform different functions, split them into multiple commits. DO NOT commit 10+ files in one commit if they were changed for different reasons.\n"+
			"5. For each commit, use the format: 'type(abc): message', where:\n"+
			"   - 'type' is one of: feat, fix, chore, refactor, docs, style, test, perf.\n"+
			"   - 'abc' is a one-word representation of what was touched (e.g., 'api', 'ui', 'config', 'cli').\n"+
			"6. There were %d commit(s) already waiting to be pushed when /push was called. Push those pre-existing commits if this count is greater than zero. To avoid accidentally pushing newly-created commits, push before creating new commits or push only up to the command-start HEAD (%s) on branch %q.\n"+
			"7. After creating commits for the current changes, decide whether those newly-created commits should also be pushed. Do not automatically push them unless you determine it is appropriate.\n\n"+
			"Current changes reported by git:\n%s\n\n"+
			"Please analyze the diffs, perform the commits, and push only the commits that should be pushed under the rules above.",
		unpushed, head, branch, changes,
	), ""
}

// imageExts is the set of file extensions we treat as images.
var imageExts = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// resolveInput parses @path tokens from the user text, loads image files as
// BlockImage blocks, and returns the full []ContentBlock plus a display string
// (with @paths left in place so the user sees what was attached).
func resolveInput(text string) ([]protocol.ContentBlock, string) {
	var blocks []protocol.ContentBlock
	words := strings.Fields(text)
	var remaining []string

	for _, w := range words {
		if !strings.HasPrefix(w, "@") {
			remaining = append(remaining, w)
			continue
		}
		path := w[1:]
		ext := strings.ToLower(filepath.Ext(path))
		mime, ok := imageExts[ext]
		if !ok {
			remaining = append(remaining, w)
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			remaining = append(remaining, w)
			continue
		}
		blocks = append(blocks, protocol.ContentBlock{
			Type:  protocol.BlockImage,
			Image: &protocol.ImageBlock{MediaType: mime, Data: base64.StdEncoding.EncodeToString(data)},
		})
	}

	plainText := strings.Join(remaining, " ")
	if plainText != "" {
		blocks = append([]protocol.ContentBlock{{Type: protocol.BlockText, Text: plainText}}, blocks...)
	}
	return blocks, text
}

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
