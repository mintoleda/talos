package panes

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/tui/styles"
)

type subStatus string

const (
	subRunning subStatus = "running"
	subDone    subStatus = "done"
	subFailed  subStatus = "failed"
)

// subEntry is one subagent run: its identity, live nested tool activity, and
// (once finished) its accounting.
type subEntry struct {
	id     string
	agent  string
	task   string
	status subStatus
	tools  ToolsModel // reused as a container for the nested tool list
	usage  protocol.SubagentUsage
}

// SubagentsModel renders the live, expandable list of subagents the primary
// agent has spawned. Collapsed, it shows one row per subagent; expanded, it
// shows that subagent's nested tool calls plus its tokens, cost, and context.
type SubagentsModel struct {
	entries     []subEntry
	index       map[string]int
	cursor      int
	expanded    bool
	killConfirm bool // y/n pending to kill the selected entry
	width       int
	height      int
	sp          spinner.Model
}

func NewSubagents() SubagentsModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return SubagentsModel{index: make(map[string]int), sp: s}
}

func (m SubagentsModel) Init() tea.Cmd { return m.sp.Tick }

func (m SubagentsModel) Update(msg tea.Msg) (SubagentsModel, tea.Cmd) {
	var cmd tea.Cmd
	m.sp, cmd = m.sp.Update(msg)
	return m, cmd
}

func (m *SubagentsModel) SetSize(w, h int) { m.width, m.height = w, h }

// Count reports how many subagents have been spawned. The pane stays hidden
// until the first one appears.
func (m SubagentsModel) Count() int { return len(m.entries) }

// ActiveCount reports how many subagents are currently running.
func (m SubagentsModel) ActiveCount() int {
	n := 0
	for _, e := range m.entries {
		if e.status == subRunning {
			n++
		}
	}
	return n
}

// SelectedIsRunning reports whether the cursor is on a running subagent.
func (m SubagentsModel) SelectedIsRunning() bool {
	return m.cursor < len(m.entries) && m.entries[m.cursor].status == subRunning
}

// SelectedID returns the ID of the cursor's subagent entry, or "".
func (m SubagentsModel) SelectedID() string {
	if m.cursor < len(m.entries) {
		return m.entries[m.cursor].id
	}
	return ""
}

// SelectedAgent returns the agent name of the cursor's entry, or "".
func (m SubagentsModel) SelectedAgent() string {
	if m.cursor < len(m.entries) {
		return m.entries[m.cursor].agent
	}
	return ""
}

// KillConfirmActive reports whether we're waiting for y/n to kill the selection.
func (m SubagentsModel) KillConfirmActive() bool { return m.killConfirm }

// KillConfirmStart enters kill-confirm mode for the selected running entry.
func (m SubagentsModel) KillConfirmStart() SubagentsModel {
	m.killConfirm = true
	return m
}

// KillConfirmCancel leaves kill-confirm mode without killing anything.
func (m SubagentsModel) KillConfirmCancel() SubagentsModel {
	m.killConfirm = false
	return m
}

func (m SubagentsModel) CursorDown() SubagentsModel {
	m.expanded = false
	if m.cursor < len(m.entries)-1 {
		m.cursor++
	}
	return m
}

func (m SubagentsModel) CursorUp() SubagentsModel {
	m.expanded = false
	if m.cursor > 0 {
		m.cursor--
	}
	return m
}

func (m SubagentsModel) ToggleExpand() SubagentsModel {
	if m.cursor < len(m.entries) {
		m.expanded = !m.expanded
	}
	return m
}

// Click selects and toggles the item on row y (0-based, relative to the pane
// top). The title row occupies y=0; list items start at y=1.
func (m SubagentsModel) Click(y int) SubagentsModel {
	if y <= 0 || y >= m.height {
		return m
	}
	listH := m.height - 1
	start, _ := windowEntries(len(m.entries), m.cursor, listH, true)
	idx := start + y - 1
	if idx < 0 || idx >= len(m.entries) {
		return m
	}
	m.cursor = idx
	return m.ToggleExpand()
}

// HandleEvent folds a Subagent* event into the model. Nested subagents (a
// subagent spawning its own) are flattened into the same list so all activity
// is visible.
func (m SubagentsModel) HandleEvent(e protocol.Event) SubagentsModel {
	switch ev := e.(type) {
	case protocol.SubagentStarted:
		if _, ok := m.index[ev.ID]; ok {
			return m
		}
		m.index[ev.ID] = len(m.entries)
		m.entries = append(m.entries, subEntry{
			id:     ev.ID,
			agent:  ev.Agent,
			task:   ev.Task,
			status: subRunning,
			tools:  NewTools(),
		})

	case protocol.SubagentFinished:
		if i, ok := m.index[ev.ID]; ok {
			m.entries[i].status = subDone
			if ev.IsError {
				m.entries[i].status = subFailed
			}
			m.entries[i].usage = ev.Usage
		}

	case protocol.SubagentEvent:
		// Flatten deeper nesting: a subagent's own subagent events arrive wrapped.
		switch ev.Inner.(type) {
		case protocol.SubagentStarted, protocol.SubagentEvent, protocol.SubagentFinished:
			return m.HandleEvent(ev.Inner)
		}
		i, ok := m.index[ev.ID]
		if !ok {
			return m
		}
		switch in := ev.Inner.(type) {
		case protocol.ToolStarted:
			m.entries[i].tools = m.entries[i].tools.AddTool(in.ID, in.Name, in.Args)
		case protocol.ToolFinished:
			m.entries[i].tools = m.entries[i].tools.FinishTool(in.ID, in.Result)
		}
	}
	return m
}

func (m SubagentsModel) View() string        { return m.view(false) }
func (m SubagentsModel) ViewFocused() string { return m.view(true) }

func (m SubagentsModel) view(focused bool) string {
	if focused && m.expanded && m.cursor < len(m.entries) {
		return m.expandedView()
	}

	innerW := m.width - 2
	if innerW < 1 {
		innerW = 1
	}
	nameW := subNameWidth(m.entries)

	// Reserve one line for the kill-confirm prompt when active.
	listH := m.height - 1
	if focused && m.killConfirm {
		listH--
	}
	if listH < 1 {
		listH = 1
	}
	start, end := windowEntries(len(m.entries), m.cursor, listH, focused)

	titleStyle := styles.ToolPaneTitleStyle
	if focused {
		titleStyle = styles.ToolPaneFocusedTitleStyle
	}
	rows := []string{titleStyle.Render(truncate(fmt.Sprintf("subagents · %d", len(m.entries)), innerW))}
	for i := start; i < end; i++ {
		e := m.entries[i]
		icon, style := m.statusGlyph(e.status)
		if focused && i == m.cursor {
			rows = append(rows, m.selectedRow(icon, e, innerW, nameW))
			continue
		}
		rows = append(rows, toolLine(icon, style, e.agent, e.task, innerW, nameW))
	}
	if focused && m.killConfirm && m.cursor < len(m.entries) {
		name := m.entries[m.cursor].agent
		prompt := styles.ToolCursorStyle.Render("kill "+name+"?") + styles.DimStyle.Render(" [y/n]")
		rows = append(rows, ansi.Truncate(prompt, innerW, ""))
	}
	return styles.PaneStyle.Width(m.width).Height(m.height).Render(strings.Join(rows, "\n"))
}

// selectedRow draws the cursor row: an accent ▌ gutter plus a highlighted line,
// mirroring the tools pane.
func (m SubagentsModel) selectedRow(icon string, e subEntry, width, nameW int) string {
	bar := styles.ToolCursorStyle.Render("▌")
	inner := width - 1
	if inner < 1 {
		inner = 1
	}
	padded := e.agent
	if pad := nameW - lipgloss.Width(e.agent); pad > 0 {
		padded = e.agent + strings.Repeat(" ", pad)
	}
	line := icon + " " + padded
	if descW := inner - lipgloss.Width(line) - 2; descW >= 2 && e.task != "" {
		line += "  " + truncate(e.task, descW)
	}
	line = ansi.Truncate(line, inner, "")
	return bar + styles.ToolSelectedStyle.Width(inner).Render(line)
}

func (m SubagentsModel) expandedView() string {
	e := m.entries[m.cursor]
	icon, style := m.statusGlyph(e.status)
	innerW := m.width - 2
	if innerW < 1 {
		innerW = 1
	}

	header := toolLine(icon, style, e.agent, e.task, innerW, lipgloss.Width(e.agent))
	sep := styles.PaneSepStyle.Render(strings.Repeat("─", innerW))

	rows := []string{header, sep}
	rows = append(rows, m.statsLines(e.usage)...)
	rows = append(rows, sep)

	// Nested tool calls (same package, so we read the container's entries).
	if len(e.tools.entries) == 0 {
		rows = append(rows, styles.DimStyle.Render("  (no tool calls)"))
	} else {
		nameW := nameWidth(e.tools.entries)
		for _, te := range e.tools.entries {
			ti, ts := statusGlyph(m.sp.View(), te.status)
			rows = append(rows, toolLine(ti, ts, te.name, te.title, innerW, nameW))
		}
	}
	body := clipLines(strings.Join(rows, "\n"), m.height)
	return styles.PaneStyle.Width(m.width).Height(m.height).Render(body)
}

// statsLines renders the per-subagent accounting: input/output tokens, dollar
// cost, and context usage. Cache stats are intentionally omitted.
func (m SubagentsModel) statsLines(u protocol.SubagentUsage) []string {
	cost := "$—"
	if u.Cost > 0 {
		cost = fmt.Sprintf("$%.4f", u.Cost)
	}
	ctx := fmt.Sprintf("ctx %s", humanCount(u.ContextTokens))
	if u.ContextLimit > 0 {
		ctx = fmt.Sprintf("ctx %s/%s (%d%%)", humanCount(u.ContextTokens),
			humanCount(u.ContextLimit), u.ContextTokens*100/u.ContextLimit)
	}
	label := styles.ToolArgStyle.Render
	val := styles.ToolNameStyle.Render
	return []string{
		"  " + label("in ") + val(humanCount(u.InputTokens)) + "   " + label("out ") + val(humanCount(u.OutputTokens)),
		"  " + val(cost) + "   " + label(ctx),
	}
}

func (m SubagentsModel) statusGlyph(s subStatus) (string, lipgloss.Style) {
	switch s {
	case subRunning:
		return strings.TrimRight(m.sp.View(), " "), styles.ToolRunningStyle
	case subDone:
		return "✓", styles.ToolOKStyle
	case subFailed:
		return "✗", styles.ToolErrorStyle
	}
	return "·", styles.DimStyle
}

// humanCount formats a token count compactly: 942, 12.3k, 1.2M.
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

// clipLines truncates s to at most n lines so an over-long expanded view never
// pushes the pane past its allotted height.
func clipLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func subNameWidth(entries []subEntry) int {
	w := 0
	for _, e := range entries {
		if l := lipgloss.Width(e.agent); l > w {
			w = l
		}
	}
	if w > 10 {
		w = 10
	}
	return w
}
