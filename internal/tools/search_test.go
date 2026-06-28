package tools

import (
	"context"
	"path/filepath"
	"testing"
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
