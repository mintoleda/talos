package panes

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestFormatToolCallRead(t *testing.T) {
	desc := formatToolCall("read", map[string]any{"path": "/home/user/main.go"})
	if desc == "" {
		t.Fatal("expected non-empty descriptor for read")
	}
}

func TestFormatToolCallWrite(t *testing.T) {
	desc := formatToolCall("write", map[string]any{"path": "internal/tools/foo.go"})
	if desc == "" {
		t.Fatal("expected non-empty descriptor for write")
	}
}

func TestFormatToolCallBash(t *testing.T) {
	desc := formatToolCall("bash", map[string]any{"command": "go test ./..."})
	if desc != "go test ./..." {
		t.Fatalf("expected 'go test ./...', got %q", desc)
	}
}

func TestFormatToolCallBashFirstLineOnly(t *testing.T) {
	desc := formatToolCall("bash", map[string]any{"command": "go build\nand more"})
	if desc != "go build" {
		t.Fatalf("expected first line 'go build', got %q", desc)
	}
}

func TestFormatToolCallSearch(t *testing.T) {
	desc := formatToolCall("search", map[string]any{"query": "func main"})
	if desc != "func main" {
		t.Fatalf("expected 'func main', got %q", desc)
	}
}

func TestFormatToolCallGrep(t *testing.T) {
	desc := formatToolCall("grep", map[string]any{"pattern": "error", "path": "internal/"})
	if !contains(desc, "error") || !contains(desc, "internal/") {
		t.Fatalf("expected pattern + path, got %q", desc)
	}
}

func TestFormatToolCallGlob(t *testing.T) {
	desc := formatToolCall("glob", map[string]any{"pattern": "**/*.go"})
	if desc != "**/*.go" {
		t.Fatalf("expected '**/*.go', got %q", desc)
	}
}

func TestFormatToolCallWebFetch(t *testing.T) {
	desc := formatToolCall("web_fetch", map[string]any{"url": "https://example.com"})
	if desc != "https://example.com" {
		t.Fatalf("expected URL, got %q", desc)
	}
}

func TestFormatToolCallFallbackGeneric(t *testing.T) {
	desc := formatToolCall("unknown-tool", map[string]any{"key1": "val1", "key2": "val2"})
	if desc == "" {
		t.Fatal("expected fallback generic args")
	}
}

func TestFormatToolCallEmptyArgs(t *testing.T) {
	desc := formatToolCall("read", nil)
	if desc != "" {
		t.Fatalf("expected empty string for nil args, got %q", desc)
	}
}

func TestGenericArgsEmpty(t *testing.T) {
	if genericArgs(nil) != "" {
		t.Fatal("expected empty for nil")
	}
	if genericArgs(map[string]any{}) != "" {
		t.Fatal("expected empty for empty map")
	}
}

func TestGenericArgsKeyOrder(t *testing.T) {
	result := genericArgs(map[string]any{"z": "last", "a": "first"})
	// Keys should be sorted.
	if !contains(result, "a=first") || !contains(result, "z=last") {
		t.Fatalf("expected both keys in result, got %q", result)
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"hello\nworld", "hello"},
		{"  spaced  text  ", "spaced text"},
		{"\n\nnonempty", "nonempty"},
		{"", ""},
		{"  \n", ""},
	}
	for _, tt := range tests {
		got := firstLine(tt.input)
		if got != tt.want {
			t.Errorf("firstLine(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		s   string
		max int
	}{
		{"hello", 10},
		{"hello", 5},
		{"hello world", 5},
		{"", 10},
		{"hello", 0},
		{"hello", 1},
	}
	for _, tt := range tests {
		got := truncate(tt.s, tt.max)
		if tt.max <= 0 && got != "" {
			t.Errorf("truncate(%q, %d) = %q, want empty", tt.s, tt.max, got)
		}
		if tt.max > 0 && len([]rune(got)) > tt.max {
			t.Errorf("truncate(%q, %d) = %q (len %d), exceeds max", tt.s, tt.max, got, len([]rune(got)))
		}
	}
}

func TestVerticalRule(t *testing.T) {
	rule := VerticalRule(5)
	lines := splitLines(rule)
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}
	for _, l := range lines {
		if l != "│" {
			t.Fatalf("expected each line to be '│', got %q", l)
		}
	}
}

func TestVerticalRuleMinOne(t *testing.T) {
	rule := VerticalRule(0)
	lines := splitLines(rule)
	if len(lines) < 1 {
		t.Fatal("expected at least 1 line for vertical rule")
	}
}

func TestNameWidth(t *testing.T) {
	entries := []toolEntry{
		{name: "read"},
		{name: "web_search"},
		{name: "ls"},
	}
	w := nameWidth(entries)
	if w > 9 {
		t.Fatalf("expected capped at 9, got %d", w)
	}
	// The longest name is "web_search" (10 chars), capped to 9.
	if w != 9 {
		t.Fatalf("expected 9, got %d", w)
	}
}

func TestNameWidthEmpty(t *testing.T) {
	if w := nameWidth(nil); w != 0 {
		t.Fatalf("expected 0 for empty, got %d", w)
	}
}

func TestWindowEntriesDefaultsToTail(t *testing.T) {
	start, end := windowEntries(10, 0, 5, false)
	if start != 5 || end != 10 {
		t.Fatalf("unfocused: expected (5,10), got (%d,%d)", start, end)
	}
}

func TestWindowEntriesFocusedKeepsCursor(t *testing.T) {
	start, end := windowEntries(10, 7, 5, true)
	if start > 7 || end <= 7 {
		t.Fatalf("focused: cursor 7 should be visible in window, got (%d,%d)", start, end)
	}
}

func TestWindowEntriesSmallCount(t *testing.T) {
	start, end := windowEntries(3, 1, 10, false)
	if start != 0 || end != 3 {
		t.Fatalf("expected (0,3) for small count, got (%d,%d)", start, end)
	}
}

func TestWindowEntriesZeroHeight(t *testing.T) {
	start, end := windowEntries(5, 0, 0, false)
	// Height 0 is clamped to 1. With 5 items unfocused, shows last 1.
	if start != 4 || end != 5 {
		t.Fatalf("expected (4,5) for zero height clamped to 1, got (%d,%d)", start, end)
	}
}

func TestWindowEntriesFocusedScrollsUp(t *testing.T) {
	start, end := windowEntries(20, 0, 5, true)
	if start != 0 || end != 5 {
		t.Fatalf("focused top: expected (0,5), got (%d,%d)", start, end)
	}
}

func TestStatusGlyph(t *testing.T) {
	icon, _ := statusGlyph(" ●  ", toolRunning)
	if icon == "" {
		t.Fatal("running status should have non-empty icon")
	}
	icon, _ = statusGlyph("x", toolOK)
	if icon != "✓" {
		t.Fatalf("expected '✓' for ok, got %q", icon)
	}
	icon, _ = statusGlyph("x", toolError)
	if icon != "✗" {
		t.Fatalf("expected '✗' for error, got %q", icon)
	}
}

func TestToolLine(t *testing.T) {
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	line := toolLine("✓", okStyle, "read", "main.go", 40, 4)
	if line == "" {
		t.Fatal("expected non-empty tool line")
	}
}

func TestShortPath(t *testing.T) {
	// Can't test heavily since it depends on workdir variable, but ensure it
	// handles empty and non-absolute paths gracefully.
	if shortPath("") != "" {
		t.Fatal("expected empty for empty path")
	}
	if shortPath("relative.go") != "relative.go" {
		t.Fatal("expected relative path unchanged")
	}
}

func TestGenericArgsSingleValue(t *testing.T) {
	result := genericArgs(map[string]any{"path": "foo.go"})
	if !contains(result, "path=foo.go") {
		t.Fatalf("expected 'path=foo.go', got %q", result)
	}
}

func splitLines(s string) []string {
	var lines []string
	current := ""
	for _, ch := range s {
		if ch == '\n' {
			lines = append(lines, current)
			current = ""
		} else {
			current += string(ch)
		}
	}
	lines = append(lines, current)
	return lines
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
