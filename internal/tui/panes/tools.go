package panes

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/tui/styles"
)

type toolStatus string

const (
	toolRunning toolStatus = "running"
	toolOK      toolStatus = "ok"
	toolError   toolStatus = "error"
)

type toolEntry struct {
	id      string
	name    string
	status  toolStatus
	title   string // human descriptor of the call (path/command/query)
	content string // full raw output, shown when expanded
}

// ToolsModel renders the live tool status list.
type ToolsModel struct {
	entries  []toolEntry
	cursor   int
	expanded bool
	vp       viewport.Model // scrollable view for expanded output
	width    int
	height   int
	sp       spinner.Model
}

func NewTools() ToolsModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	vp := viewport.New(0, 0)
	vp.KeyMap = viewport.KeyMap{} // disable built-in key bindings; we manage nav ourselves
	return ToolsModel{sp: s, vp: vp}
}

func (t ToolsModel) Init() tea.Cmd { return t.sp.Tick }

func (t ToolsModel) Update(msg tea.Msg) (ToolsModel, tea.Cmd) {
	var cmd tea.Cmd
	t.sp, cmd = t.sp.Update(msg)
	if t.expanded {
		t.vp, _ = t.vp.Update(msg)
	}
	return t, cmd
}

func (t *ToolsModel) SetSize(w, h int) {
	t.width, t.height = w, h
	// expanded viewport: content area minus header (1) + separator (1) + padding top (1)
	t.vp.Width = w - 2 // PaneStyle adds 1 col padding each side
	t.vp.Height = h - 3
	if t.vp.Height < 1 {
		t.vp.Height = 1
	}
}

// Count reports how many tools have been registered. The tools pane stays
// hidden until the first tool runs.
func (t ToolsModel) Count() int { return len(t.entries) }

func (t ToolsModel) CursorDown() ToolsModel {
	t.expanded = false
	if t.cursor < len(t.entries)-1 {
		t.cursor++
	}
	return t
}

func (t ToolsModel) CursorUp() ToolsModel {
	t.expanded = false
	if t.cursor > 0 {
		t.cursor--
	}
	return t
}

func (t ToolsModel) ScrollDown(n int) ToolsModel {
	if t.expanded {
		t.vp.LineDown(n)
		return t
	}
	return t.CursorDown()
}

func (t ToolsModel) ScrollUp(n int) ToolsModel {
	if t.expanded {
		t.vp.LineUp(n)
		return t
	}
	return t.CursorUp()
}

func (t ToolsModel) ToggleExpand() ToolsModel {
	if t.cursor >= len(t.entries) {
		return t
	}
	t.expanded = !t.expanded
	if t.expanded {
		t.vp.SetContent(t.entries[t.cursor].content)
		t.vp.GotoTop()
	}
	return t
}

// Click selects and toggles the item on row y (0-based, relative to the pane
// top). The title row occupies y=0; list items start at y=1.
func (t ToolsModel) Click(y int) ToolsModel {
	if y <= 0 || y >= t.height {
		return t
	}
	listH := t.height - 1
	start, _ := windowEntries(len(t.entries), t.cursor, listH, true)
	idx := start + y - 1
	if idx < 0 || idx >= len(t.entries) {
		return t
	}
	t.cursor = idx
	return t.ToggleExpand()
}

func (t ToolsModel) AddTool(id, name string, args map[string]any) ToolsModel {
	t.entries = append(t.entries, toolEntry{
		id:     id,
		name:   name,
		status: toolRunning,
		title:  formatToolCall(name, args),
	})
	return t
}

func (t ToolsModel) FinishTool(id string, result protocol.ToolResult) ToolsModel {
	for i := range t.entries {
		if t.entries[i].id == id {
			if result.IsError {
				t.entries[i].status = toolError
			} else {
				t.entries[i].status = toolOK
			}
			t.entries[i].content = result.Content
			break
		}
	}
	return t
}

func (t ToolsModel) View() string {
	return t.view(false)
}

func (t ToolsModel) ViewFocused() string {
	return t.view(true)
}

func (t ToolsModel) view(focused bool) string {
	if focused && t.expanded && t.cursor < len(t.entries) {
		return t.expandedView()
	}

	innerW := t.width - 2 // PaneStyle adds 1 col padding each side
	if innerW < 1 {
		innerW = 1
	}
	nameW := nameWidth(t.entries)

	// The pane title occupies one row, so the entry list gets height − 1.
	listH := t.height - 1
	if listH < 1 {
		listH = 1
	}
	start, end := windowEntries(len(t.entries), t.cursor, listH, focused)

	rows := []string{t.titleRow(innerW, focused)}
	for i := start; i < end; i++ {
		e := t.entries[i]
		icon, style := statusGlyph(t.sp.View(), e.status)
		if focused && i == t.cursor {
			rows = append(rows, t.selectedRow(icon, e, innerW, nameW))
			continue
		}
		rows = append(rows, toolLine(icon, style, e.name, e.title, innerW, nameW))
	}

	content := strings.Join(rows, "\n")
	return styles.PaneStyle.Width(t.width).Height(t.height).Render(content)
}

// titleRow renders the "tools" header with a live count, accented when focused.
func (t ToolsModel) titleRow(width int, focused bool) string {
	label := fmt.Sprintf("tools · %d", len(t.entries))
	s := styles.ToolPaneTitleStyle
	if focused {
		s = styles.ToolPaneFocusedTitleStyle
	}
	return s.Render(truncate(label, width))
}

// selectedRow draws the cursor row: an accent ▌ gutter plus a highlighted line.
func (t ToolsModel) selectedRow(icon string, e toolEntry, width, nameW int) string {
	bar := styles.ToolCursorStyle.Render("▌")
	inner := width - 1 // the bar consumes one column
	if inner < 1 {
		inner = 1
	}
	padded := e.name
	if pad := nameW - lipgloss.Width(e.name); pad > 0 {
		padded = e.name + strings.Repeat(" ", pad)
	}
	line := icon + " " + padded
	if descW := inner - lipgloss.Width(line) - 2; descW >= 2 && e.title != "" {
		line += "  " + truncate(e.title, descW)
	}
	line = ansi.Truncate(line, inner, "")
	return bar + styles.ToolSelectedStyle.Width(inner).Render(line)
}

func (t ToolsModel) expandedView() string {
	e := t.entries[t.cursor]
	icon, style := statusGlyph(t.sp.View(), e.status)

	innerW := t.width - 2 // account for PaneStyle padding
	if innerW < 1 {
		innerW = 1
	}
	header := toolLine(icon, style, e.name, e.title, innerW, lipgloss.Width(e.name))
	sep := styles.PaneSepStyle.Render(strings.Repeat("─", innerW))
	body := lipgloss.JoinVertical(lipgloss.Left, header, sep, t.vp.View())
	return styles.PaneStyle.Width(t.width).Height(t.height).Render(body)
}
