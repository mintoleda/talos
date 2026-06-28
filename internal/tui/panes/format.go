package panes

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/mintoleda/talos/internal/tui/styles"
)

// workdir is resolved once at startup; tool paths are shown relative to it when
// possible so the side panel reads "internal/skills/skills.go" instead of a long
// absolute path.
var workdir, _ = os.Getwd()

// formatToolCall turns a tool name plus its raw arguments into a short, human
// descriptor of *what the call targets* — a filename, a command, a query. This
// is intentionally the call (stable, meaningful) rather than the raw result,
// whose first line is usually noise in a narrow column.
func formatToolCall(name string, args map[string]any) string {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := args[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
				if v != nil {
					return fmt.Sprintf("%v", v)
				}
			}
		}
		return ""
	}

	switch name {
	case "read", "write", "edit", "ls":
		return shortPath(get("path"))
	case "bash", "bash_background", "bash_read_output", "bash_kill":
		return firstLine(get("command"))
	case "search", "find", "fff", "ffgrep", "web_search":
		return firstLine(get("query", "q", "pattern"))
	case "grep":
		pat := firstLine(get("pattern"))
		if p := get("path"); p != "" {
			return pat + " in " + shortPath(p)
		}
		return pat
	case "glob":
		return get("pattern")
	case "web_fetch":
		return get("url")
	}
	return genericArgs(args)
}

// genericArgs is the fallback descriptor for tools we don't special-case: the
// first meaningful value, or a compact key=value join in stable key order.
func genericArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, args[k]))
	}
	return firstLine(strings.Join(parts, " "))
}

// shortPath renders p relative to the working directory when it sits inside it,
// otherwise returns it unchanged.
func shortPath(p string) string {
	if p == "" {
		return ""
	}
	if workdir != "" && filepath.IsAbs(p) {
		if rel, err := filepath.Rel(workdir, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return p
}

// firstLine collapses a possibly multi-line value to its first non-empty line,
// trimmed. Interior runs of whitespace are squeezed so commands stay compact.
func firstLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return strings.Join(strings.Fields(t), " ")
		}
	}
	return ""
}

// truncate clips s to at most max display columns (rune-aware), appending an
// ellipsis when it had to cut.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

// VerticalRule returns an n-line tall "│" bar used to separate the chat and
// tools panes. A single "│" would only render on the first row once joined
// horizontally with taller content, so we repeat it down the full height.
func VerticalRule(n int) string {
	if n < 1 {
		n = 1
	}
	lines := make([]string, n)
	for i := range lines {
		lines[i] = "│"
	}
	return strings.Join(lines, "\n")
}

// statusGlyph maps a tool status to its icon and color. The spinner frame is
// right-trimmed because spinner.Dot's frames carry a trailing space that would
// otherwise misalign the running row by a column.
func statusGlyph(sp string, status toolStatus) (icon string, style lipgloss.Style) {
	switch status {
	case toolRunning:
		return strings.TrimRight(sp, " "), styles.ToolRunningStyle
	case toolOK:
		return "✓", styles.ToolOKStyle
	case toolError:
		return "✗", styles.ToolErrorStyle
	}
	return "·", styles.DimStyle
}

// toolLine composes one styled, single-line tool entry:
//
//	<icon> <name…>  <descriptor…>
//
// Widths are computed on the plain text so ANSI styling never breaks the layout;
// the descriptor is truncated to fill (but never exceed) the available width.
func toolLine(icon string, iconStyle lipgloss.Style, name, desc string, width, nameW int) string {
	if width < 1 {
		width = 1
	}
	padded := name
	if pad := nameW - lipgloss.Width(name); pad > 0 {
		padded = name + strings.Repeat(" ", pad)
	}

	var b strings.Builder
	b.WriteString(iconStyle.Render(icon))
	b.WriteString(" ")
	b.WriteString(styles.ToolNameStyle.Render(padded))

	// Remaining room for the descriptor: width − icon(1) − space(1) − name − gap(2).
	descW := width - 2 - lipgloss.Width(padded) - 2
	if desc != "" && descW >= 2 {
		b.WriteString("  ")
		b.WriteString(styles.ToolArgStyle.Render(truncate(desc, descW)))
	}
	// Final guard against any overflow from an over-long name.
	return ansi.Truncate(b.String(), width, "")
}

// nameWidth returns the column width to pad tool names to, so the descriptors
// line up. Capped so a single long name (web_search) can't shove everything.
func nameWidth(entries []toolEntry) int {
	w := 0
	for _, e := range entries {
		if l := lipgloss.Width(e.name); l > w {
			w = l
		}
	}
	if w > 9 {
		w = 9
	}
	return w
}

// windowEntries returns the slice of indices visible in a pane of the given
// height. Unfocused, it tails to the most recent entries (live monitoring);
// focused, it scrolls just enough to keep the cursor in view.
func windowEntries(count, cursor, height int, focused bool) (start, end int) {
	if height < 1 {
		height = 1
	}
	if count <= height {
		return 0, count
	}
	start = count - height // tail by default
	if focused {
		if cursor < start {
			start = cursor
		}
		if cursor >= start+height {
			start = cursor - height + 1
		}
		if start < 0 {
			start = 0
		}
	}
	end = start + height
	if end > count {
		end = count
	}
	return start, end
}
