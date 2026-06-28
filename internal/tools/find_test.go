package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFindGlobMode(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.go"), "")
	mustWrite(t, filepath.Join(dir, "b.go"), "")
	mustWrite(t, filepath.Join(dir, "a.txt"), "")

	tool := NewFind(dir)
	res, _ := tool.Execute(context.Background(), map[string]any{"query": "*.go"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if res.Content == "" || res.Content == "(no matches)" {
		t.Fatal("expected glob matches")
	}
}

func TestFindFuzzyMode(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "prompt_builder.go"), "")

	tool := NewFind(dir)
	res, _ := tool.Execute(context.Background(), map[string]any{"query": "prompt build"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if res.Content == "" || res.Content == "(no matches)" {
		t.Fatal("expected fuzzy matches")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
