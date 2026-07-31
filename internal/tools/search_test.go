package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSearchRegexMode(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "func main() {}")
	mustWrite(t, filepath.Join(dir, "b.go"), "var x = 1")

	tool := NewSearch(dir)
	res, _ := tool.Execute(context.Background(), map[string]any{"query": `func main`})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if res.Content == "" || res.Content == "(no matches)" {
		t.Fatal("expected regex matches")
	}
}

func TestSearchFuzzyMode(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "thinking.go"), "func ThinkingLevel() string { return \"\" }")

	tool := NewSearch(dir)
	res, _ := tool.Execute(context.Background(), map[string]any{"query": "thinking level"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if res.Content == "" || res.Content == "(no matches)" {
		t.Fatal("expected fuzzy matches")
	}
}

func TestSearchCapsHugeBinaryLine(t *testing.T) {
	dir := t.TempDir()
	blob := make([]byte, 4<<20)
	for i := range blob {
		blob[i] = 'x'
	}
	copy(blob, []byte("func main() {}"))
	mustWrite(t, filepath.Join(dir, "artifact.bin"), string(blob))
	mustWrite(t, filepath.Join(dir, "a.go"), "func main() {}")

	tool := NewSearch(dir)
	res, _ := tool.Execute(context.Background(), map[string]any{"query": `func main\(\)`})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if len(res.Content) > grepMaxOutput+512 {
		t.Fatalf("result not capped: %d bytes", len(res.Content))
	}
	if !strings.Contains(res.Content, "a.go") {
		t.Fatalf("real match dropped: %q", res.Content)
	}
}

func TestCapResultContent(t *testing.T) {
	if got := CapResultContent("short", 100); got != "short" {
		t.Fatalf("unexpected rewrite: %q", got)
	}
	big := strings.Repeat("line of output\n", 10000)
	got := CapResultContent(big, 4096)
	if len(got) > 4096+256 {
		t.Fatalf("cap exceeded: %d bytes", len(got))
	}
	if !strings.HasPrefix(got, "[truncated tool result — ") {
		t.Fatalf("missing marker: %q", got[:80])
	}
	if !utf8.ValidString(got) {
		t.Fatal("cap produced invalid utf-8")
	}
}

func TestSearchCapsLongLineInSmallFile(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "min.js"), "func main() {}"+strings.Repeat("y", 900<<10))

	tool := NewSearch(dir)
	res, _ := tool.Execute(context.Background(), map[string]any{"query": `func main\(\)`})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if len(res.Content) > grepMaxOutput+512 {
		t.Fatalf("long line not capped: %d bytes", len(res.Content))
	}
}
