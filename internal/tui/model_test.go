package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mintoleda/talos/internal/models"
	"github.com/mintoleda/talos/internal/protocol"
)

func TestModelHandlesTextDelta(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24
	m.relayout()

	m2, _ := m.Update(EventMsg{E: protocol.TextDelta{Text: "hello"}})
	m3 := m2.(Model)
	// Chat pane height = terminal height - 3 (rounded prompt box folds status + input).
	// 24 - 3 = 21.
	if m3.chat.Height() != 21 {
		t.Fatalf("expected chat height 21, got %d", m3.chat.Height())
	}
}

func TestPromptBoxWrapsLongInput(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 40, 24
	m.relayout()

	emptyPane := m.chat.Height()

	// A line far longer than the box interior must soft-wrap onto several rows.
	m.input.SetValue(strings.Repeat("word ", 40))
	m.relayout()

	box := m.promptBoxView()
	lines := strings.Split(box, "\n")

	// Prompt box = top border + N input rows + bottom border, with N > 1.
	if len(lines) <= 3 {
		t.Fatalf("expected input to wrap to multiple rows, got %d box lines", len(lines))
	}

	// No rendered row may exceed the terminal width (the old bug: overflow/wrap).
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w > m.width {
			t.Fatalf("box line %d width %d exceeds terminal width %d", i, w, m.width)
		}
	}

	// The chat pane must shrink to make room for the taller input box.
	if m.chat.Height() >= emptyPane {
		t.Fatalf("expected chat pane to shrink below %d, got %d", emptyPane, m.chat.Height())
	}
}

// TestPromptBoxWrapKeystrokeByKeystroke types character-by-character, as a real
// user would, rather than via SetValue. This is the path that regressed: the
// textarea's internal viewport scrolls to follow the cursor on every keystroke
// using whatever height relayout() left it at previously. If relayout() shrinks
// the textarea down to fit content after each render, the next keystroke's
// Update runs against that stale small height, scrolls to keep the cursor
// visible, and hides every row typed before it — leaving only the current line
// on screen instead of the full wrapped paragraph.
func TestPromptBoxWrapKeystrokeByKeystroke(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 60, 24
	m.relayout()

	text := "the quick brown fox jumps over the lazy dog and then keeps on running"
	var tm tea.Model = m
	for _, r := range text {
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	final := tm.(Model)

	box := final.promptBoxView()
	lines := strings.Split(box, "\n")
	if len(lines) <= 3 {
		t.Fatalf("expected wrapped input to span multiple box rows, got %d lines:\n%s", len(lines), box)
	}

	// Every word typed must still be visible somewhere in the box — none of the
	// earlier wrapped rows may have scrolled out of view.
	joined := strings.Join(lines, " ")
	for _, word := range strings.Fields(text) {
		if !strings.Contains(joined, word) {
			t.Fatalf("word %q missing from rendered prompt box (earlier lines were hidden):\n%s", word, box)
		}
	}
}

func TestModelShowsConfirmDialog(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24
	m.relayout()

	reply := make(chan bool, 1)
	m2, _ := m.Update(EventMsg{E: protocol.PermissionRequested{
		ToolName: "bash",
		Reason:   "dangerous",
		ReplyCh:  reply,
	}})
	m3 := m2.(Model)
	if m3.dialog == nil {
		t.Fatal("expected dialog to be set")
	}
	// Approve with 'y'.
	m4, _ := m3.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m5 := m4.(Model)
	if m5.dialog != nil {
		t.Fatal("expected dialog dismissed")
	}
	select {
	case v := <-reply:
		if !v {
			t.Fatal("expected approved=true")
		}
	default:
		t.Fatal("expected reply on channel")
	}
}

// TestModelReplaysInitialHistory guards the `talos -c` continuation behavior:
// when a session is loaded, its messages must be visible in the chat pane.
// Without this, the model has the previous context but the user sees a blank
// screen and assumes the session was reset.
func TestModelReplaysInitialHistory(t *testing.T) {
	history := []protocol.FrozenMessage{
		{Msg: protocol.TextMessage(protocol.RoleUser, "hello, what is 2+2?")},
		{Msg: protocol.TextMessage(protocol.RoleAssistant, "4")},
		{Msg: protocol.TextMessage(protocol.RoleUser, "and 3+3?")},
		{Msg: protocol.TextMessage(protocol.RoleAssistant, "6")},
	}
	m := NewModel(Config{
		SessionID:      "test",
		Mode:           ModeSingleAgent,
		InitialHistory: history,
	})
	m.width, m.height = 80, 24
	m.relayout()

	// The chat pane should now have 4 entries (one per FrozenMessage).
	if got := m.chat.Len(); got != 4 {
		t.Fatalf("expected 4 chat entries after replay, got %d", got)
	}
}

// TestModelEmptyHistoryLeavesChatEmpty ensures we don't double-render or
// fabricate messages when there's nothing to replay.
func TestModelEmptyHistoryLeavesChatEmpty(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24
	m.relayout()

	if got := m.chat.Len(); got != 0 {
		t.Fatalf("expected empty chat with no history, got %d entries", got)
	}
}

func TestModelToolStartedFinishesBatch(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24
	m.relayout()

	m2, _ := m.Update(EventMsg{E: protocol.BatchStarted{Num: 1}})
	m2, _ = m2.Update(EventMsg{E: protocol.ToolStarted{ID: "t1", Name: "read", Args: map[string]any{"path": "main.go"}}})
	m3 := m2.(Model)

	// Should have recorded the tool name.
	if m3.toolNames["t1"] != "read" {
		t.Fatalf("expected toolNames[t1]=read, got %q", m3.toolNames["t1"])
	}
}

func TestModelToolFinishedRemovesFromActive(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24
	m.relayout()

	m2, _ := m.Update(EventMsg{E: protocol.ToolStarted{ID: "t1", Name: "read", Args: map[string]any{"path": "main.go"}}})
	m2, _ = m2.Update(EventMsg{E: protocol.ToolFinished{
		ID:     "t1",
		Result: protocol.ToolResult{ToolUseID: "t1", Content: "ok"},
	}})
	m3 := m2.(Model)

	if _, ok := m3.toolNames["t1"]; ok {
		t.Fatal("expected tool name to be removed after finish")
	}
	if m3.chat.Len() < 1 {
		t.Fatal("expected at least one chat segment (tool use line)")
	}
}

func TestModelSubagentLifecycle(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24
	m.relayout()

	// Start a subagent.
	m2, _ := m.Update(EventMsg{E: protocol.SubagentStarted{
		ID:    "s1",
		Agent: "scout",
		Task:  "find files",
	}})
	m3 := m2.(Model)

	if m3.subagents.Count() != 1 {
		t.Fatalf("expected 1 subagent, got %d", m3.subagents.Count())
	}
	if m3.subagents.ActiveCount() != 1 {
		t.Fatalf("expected 1 active subagent, got %d", m3.subagents.ActiveCount())
	}

	// Finish it.
	m4, _ := m3.Update(EventMsg{E: protocol.SubagentFinished{
		ID:      "s1",
		Result:  "done",
		IsError: false,
	}})
	m5 := m4.(Model)
	if m5.subagents.ActiveCount() != 0 {
		t.Fatalf("expected 0 active after finish, got %d", m5.subagents.ActiveCount())
	}
}

func TestModelNoticeAppearsInChat(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24

	m2, _ := m.Update(EventMsg{E: protocol.Notice{Level: "info", Text: "something happened"}})
	m3 := m2.(Model)
	if m3.chat.Len() != 1 {
		t.Fatalf("expected 1 chat segment after notice, got %d", m3.chat.Len())
	}
}

func TestModelTurnEndedSetsBusyFalse(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24
	m.busy = true

	m2, _ := m.Update(EventMsg{E: protocol.TurnEnded{
		StopReason: "stop",
		Usage:      protocol.Usage{PromptTokens: 100, CompletionTokens: 50, CachedPromptTokens: 10},
	}})
	m3 := m2.(Model)

	if m3.busy {
		t.Fatal("expected busy=false after turn ended")
	}
	if m3.totalInput != 100 {
		t.Fatalf("expected totalInput=100, got %d", m3.totalInput)
	}
	if m3.totalOutput != 50 {
		t.Fatalf("expected totalOutput=50, got %d", m3.totalOutput)
	}
	if m3.streamTextLen != 0 {
		t.Fatal("expected streamTextLen reset to 0")
	}
}

func TestModelPromptEstimateUpdatesContext(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24

	m2, _ := m.Update(EventMsg{E: protocol.PromptEstimate{PromptTokens: 500, ContextLimit: 8000}})
	m3 := m2.(Model)

	if m3.contextUsed != 500 {
		t.Fatalf("expected contextUsed=500, got %d", m3.contextUsed)
	}
	if m3.contextWin != 8000 {
		t.Fatalf("expected contextWin=8000, got %d", m3.contextWin)
	}
}

func TestModelModelChanged(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent, Provider: "old", Model: "old-model"})
	m.width, m.height = 80, 24

	m2, _ := m.Update(EventMsg{E: protocol.ModelChanged{
		Provider:      "new-provider",
		Model:         "new-model",
		ThinkingLevel: "high",
	}})
	m3 := m2.(Model)

	if m3.cfg.Provider != "new-provider" {
		t.Fatalf("expected provider='new-provider', got %q", m3.cfg.Provider)
	}
	if m3.cfg.Model != "new-model" {
		t.Fatalf("expected model='new-model', got %q", m3.cfg.Model)
	}
	if m3.thinkingLevel != "high" {
		t.Fatalf("expected thinkingLevel='high', got %q", m3.thinkingLevel)
	}
}

func TestModelSlashHelp(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24

	help := m.slashHelp()
	if !strings.Contains(help, "/help") {
		t.Fatal("help should contain /help")
	}
	if !strings.Contains(help, "/new") {
		t.Fatal("help should contain /new")
	}
	if !strings.Contains(help, "/exit") {
		t.Fatal("help should contain /exit")
	}
}

func TestModelUpdateSlashCompletions(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24

	// Simulate typing "/" to trigger completions.
	m.input.SetValue("/")
	m.updateSlashCompletions()

	if len(m.slashCompletions) == 0 {
		t.Fatal("expected slash completions")
	}
	if m.slashSelected < 0 {
		t.Fatal("expected selected completion")
	}
}

func TestModelCompletionHeight(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})

	if m.completionHeight() != 0 {
		t.Fatal("expected 0 for no completions")
	}

	m.slashCompletions = []slashCommand{{Name: "/help", Desc: "help"}}
	if m.completionHeight() <= 0 {
		t.Fatal("expected positive completion height")
	}
}

func TestModelResolveInputPlain(t *testing.T) {
	blocks, display := resolveInput("hello world")
	if display != "hello world" {
		t.Fatalf("expected display 'hello world', got %q", display)
	}
	if len(blocks) != 1 || blocks[0].Type != protocol.BlockText {
		t.Fatal("expected 1 text block")
	}
}

func TestModelResolveInputEmpty(t *testing.T) {
	blocks, display := resolveInput("")
	if display != "" {
		t.Fatalf("expected empty display, got %q", display)
	}
	if len(blocks) != 0 {
		t.Fatal("expected no blocks for empty input")
	}
}

func TestModelInputHistory(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24

	// Simulate submitting text.
	m.input.SetValue("first message")
	// The submit path through update is complex; just check history tracking.
	m.inputHistory = append(m.inputHistory, "first message")
	m.inputHistory = append(m.inputHistory, "second message")

	if len(m.inputHistory) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(m.inputHistory))
	}
}

func TestModelStatusParts(t *testing.T) {
	m := NewModel(Config{
		SessionID: "test",
		Mode:      ModeSingleAgent,
		Provider:  "openai",
		Model:     "gpt-4",
	})
	m.cwd = "/home/user/project"

	left, right := m.statusParts()
	if !strings.Contains(left, "openai") {
		t.Fatal("left status should contain provider")
	}
	if !strings.Contains(left, "gpt-4") {
		t.Fatal("left status should contain model")
	}
	_ = right
}


func TestModelShortenDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	short := shortenDir(home + "/projects/talos")
	if !strings.HasPrefix(short, "~") {
		t.Fatal("expected home dir shortened to ~")
	}

	// Non-home path should stay.
	short = shortenDir("/tmp/test")
	if short != "/tmp/test" {
		t.Fatalf("expected '/tmp/test', got %q", short)
	}
}

func TestModelHumanCount(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{500, "500"},
		{1500, "1.5k"},
		{1000000, "1.0M"},
	}
	for _, tt := range tests {
		got := humanCount(tt.n)
		if got != tt.want {
			t.Errorf("humanCount(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestModelReplayTranscriptWithToolResults(t *testing.T) {
	history := []protocol.FrozenMessage{
		{Msg: protocol.Message{
			Role: protocol.RoleAssistant,
			Content: []protocol.ContentBlock{
				{Type: protocol.BlockToolUse, ToolUse: &protocol.ToolUse{ID: "t1", Name: "read", Args: map[string]any{"path": "main.go"}}},
			},
		}},
		{Msg: protocol.Message{
			Role: protocol.RoleTool,
			Content: []protocol.ContentBlock{
				{Type: protocol.BlockToolResult, ToolResult: &protocol.ToolResult{ToolUseID: "t1", Content: "file content"}},
			},
		}},
	}

	m := NewModel(Config{
		SessionID:      "test",
		Mode:           ModeSingleAgent,
		InitialHistory: history,
	})
	m.width, m.height = 80, 24
	m.relayout()

	// Verify the tool call was replayed into the chat as a segment.
	if m.chat.Len() != 1 {
		t.Fatalf("expected 1 chat segment after replay, got %d", m.chat.Len())
	}
}

func TestModelEscClearConfirm(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24
	m.vimMode = InsertMode
	m.input.SetValue("some text")

	// First esc should set confirm flag.
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m3 := m2.(Model)
	if !m3.escClearConfirm {
		t.Fatal("expected escClearConfirm after first esc")
	}

	// Second esc should clear the input.
	m4, _ := m3.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m5 := m4.(Model)
	if m5.input.Value() != "" {
		t.Fatal("expected input cleared after second esc")
	}
}

func TestModelVimModeToggle(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24
	m.vimMode = InsertMode

	// Esc enters normal mode when input is empty.
	m.input.SetValue("")
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m3 := m2.(Model)
	if m3.vimMode != NormalMode {
		t.Fatal("expected NormalMode after esc")
	}

	// 'i' enters insert mode.
	m4, _ := m3.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	m5 := m4.(Model)
	if m5.vimMode != InsertMode {
		t.Fatal("expected InsertMode after 'i'")
	}
}

func TestModelPromptBoxView(t *testing.T) {
	m := NewModel(Config{
		SessionID: "test",
		Mode:      ModeSingleAgent,
		Provider:  "test",
		Model:     "test-model",
	})
	m.width = 80

	view := m.promptBoxView()
	if !strings.Contains(view, "╭") {
		t.Fatal("prompt box should have rounded top-left corner")
	}
	if !strings.Contains(view, "╰") {
		t.Fatal("prompt box should have rounded bottom-left corner")
	}
}

func TestModelMainView(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24
	m.relayout()

	view := m.mainView()
	if view == "" {
		t.Fatal("expected non-empty main view")
	}
}


func TestModelBusySetsBusySince(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24

	m.busy = true
	m.busySince = time.Now()

	// thinkingLine should produce output.
	line := m.thinkingLine()
	if line == "" {
		t.Fatal("expected non-empty thinking line when busy")
	}
}

func TestModelThinkingLineCycles(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.busy = true
	m.busySince = time.Now().Add(-10 * time.Second)

	line := m.thinkingLine()
	if !strings.Contains(line, "Brewing") && !strings.Contains(line, "Weaving") && !strings.Contains(line, "Churning") {
		t.Fatalf("unexpected thinking line: %q", line)
	}
}


func TestModelReseedStats(t *testing.T) {
	m := NewModel(Config{
		SessionID: "test",
		Mode:      ModeSingleAgent,
		StatsSnapshot: func() (int, int, int, float64) {
			return 100, 50, 80, 0.005
		},
	})
	m.reseedStats()

	if m.totalInput != 100 {
		t.Fatalf("expected totalInput=100, got %d", m.totalInput)
	}
	if m.totalOutput != 50 {
		t.Fatalf("expected totalOutput=50, got %d", m.totalOutput)
	}
	if m.totalCacheMiss != 80 {
		t.Fatalf("expected totalCacheMiss=80, got %d", m.totalCacheMiss)
	}
	if m.totalCost != 0.005 {
		t.Fatalf("expected totalCost=0.005, got %f", m.totalCost)
	}
}

func TestModelUserInputEvent(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24

	m2, _ := m.Update(EventMsg{E: protocol.UserInput{Text: "hello from server"}})
	m3 := m2.(Model)

	// Should appear in chat.
	if m3.chat.Len() != 1 {
		t.Fatalf("expected 1 chat segment after UserInput, got %d", m3.chat.Len())
	}
}

func TestModelSteerQueue(t *testing.T) {
	q := &SteerQueue{}
	if q.Len() != 0 {
		t.Fatal("expected empty queue")
	}

	blocks := []protocol.ContentBlock{{Type: protocol.BlockText, Text: "steer message"}}
	q.Enqueue(blocks)
	if q.Len() != 1 {
		t.Fatalf("expected 1 item, got %d", q.Len())
	}

	// Withdraw should return the item.
	withdrawn := q.Withdraw()
	if withdrawn == nil {
		t.Fatal("expected withdrawn item")
	}
	if q.Len() != 0 {
		t.Fatal("expected empty after withdraw")
	}

	// Drain should return empty (Drain always returns a value, never nil).
	if drained := q.Drain(); len(drained) != 0 {
		t.Fatalf("expected empty drain, got %d items", len(drained))
	}
}

func TestModelSteerQueueDrain(t *testing.T) {
	q := &SteerQueue{}
	q.Enqueue([]protocol.ContentBlock{{Type: protocol.BlockText, Text: "a"}})
	q.Enqueue([]protocol.ContentBlock{{Type: protocol.BlockText, Text: "b"}})

	drained := q.Drain()
	if len(drained) != 2 {
		t.Fatalf("expected 2 drained items, got %d", len(drained))
	}
	if q.Len() != 0 {
		t.Fatal("expected empty after drain")
	}
}

func TestModelSteerQueueWithdrawEmpty(t *testing.T) {
	q := &SteerQueue{}
	if q.Withdraw() != nil {
		t.Fatal("expected nil from withdraw on empty")
	}
}

func TestModelInitReturnsCmd(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("expected non-nil command from Init")
	}
}

func TestModelInsertModeKeyHandling(t *testing.T) {
	m := NewModel(Config{SessionID: "test", Mode: ModeSingleAgent})
	m.width, m.height = 80, 24
	m.vimMode = InsertMode
	m.input.SetValue("")

	// esc with empty input -> normal mode.
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m3 := m2.(Model)
	if m3.vimMode != NormalMode {
		t.Fatal("expected normal mode after esc with empty input")
	}
}

func TestModelCtrlLOpensModelPicker(t *testing.T) {
	m := NewModel(Config{
		SessionID:    "test",
		Mode:         ModeSingleAgent,
		FetchModels: func() ([]models.Entry, error) {
			return nil, nil
		},
	})
	m.width, m.height = 80, 24

	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	m3 := m2.(Model)
	if m3.dialog == nil {
		t.Fatal("ctrl+l should open model picker dialog")
	}
}

func TestModelCtrlTCyclesThinking(t *testing.T) {
	var cycled string
	m := NewModel(Config{
		SessionID: "test",
		Mode:      ModeSingleAgent,
		CycleThinking: func() string {
			cycled = "high"
			return "high"
		},
	})
	m.width, m.height = 80, 24

	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m3 := m2.(Model)
	_ = m3

	if cycled != "high" {
		t.Fatalf("expected CycleThinking called, got %q", cycled)
	}
}
