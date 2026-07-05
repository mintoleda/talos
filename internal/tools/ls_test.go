package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLsToolName(t *testing.T) {
	l := NewLs()
	if l.Name() != "ls" {
		t.Fatalf("expected 'ls', got %q", l.Name())
	}
}

func TestLsToolSchema(t *testing.T) {
	l := NewLs()
	schema := l.Schema()
	if schema == nil {
		t.Fatal("expected non-nil schema")
	}
}

func TestLsDirectory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte(""), 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)

	l := NewLs()
	result, err := l.Execute(context.Background(), map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}

	if !contains(result.Content, "a.txt") {
		t.Fatal("result should contain a.txt")
	}
	if !contains(result.Content, "b.txt") {
		t.Fatal("result should contain b.txt")
	}
	if !contains(result.Content, "sub/") {
		t.Fatal("result should contain sub/ with trailing slash")
	}
}

func TestLsNonExistentDirectory(t *testing.T) {
	l := NewLs()
	result, err := l.Execute(context.Background(), map[string]any{"path": "/nonexistent"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error for non-existent directory")
	}
}

func TestLsMissingPath(t *testing.T) {
	l := NewLs()
	result, err := l.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error for missing path")
	}
}

func TestLsEmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	l := NewLs()
	result, err := l.Execute(context.Background(), map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success for empty dir, got error: %s", result.Content)
	}
}

func TestLsSortedOrder(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "z.txt"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(dir, "m.txt"), []byte(""), 0o644)

	l := NewLs()
	result, _ := l.Execute(context.Background(), map[string]any{"path": dir})

	// a.txt should come before z.txt.
	aIdx := indexOf(result.Content, "a.txt")
	zIdx := indexOf(result.Content, "z.txt")
	if aIdx < 0 || zIdx < 0 {
		t.Fatal("expected both files in listing")
	}
	if aIdx > zIdx {
		t.Fatal("expected sorted order: a.txt before z.txt")
	}
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
