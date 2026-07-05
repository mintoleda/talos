package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGlobToolName(t *testing.T) {
	g := NewGlob()
	if g.Name() != "glob" {
		t.Fatalf("expected 'glob', got %q", g.Name())
	}
}

func TestGlobToolSchema(t *testing.T) {
	g := NewGlob()
	schema := g.Schema()
	if schema == nil {
		t.Fatal("expected non-nil schema")
	}
}

func TestGlobSimple(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte(""), 0o644)

	g := NewGlob()
	result, err := g.Execute(context.Background(), map[string]any{
		"pattern": filepath.Join(dir, "*.go"),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}

	if !contains(result.Content, "a.go") {
		t.Fatal("result should contain a.go")
	}
	if !contains(result.Content, "b.go") {
		t.Fatal("result should contain b.go")
	}
	if contains(result.Content, "README.md") {
		t.Fatal("result should NOT contain README.md")
	}
}

func TestGlobNoMatches(t *testing.T) {
	dir := t.TempDir()

	g := NewGlob()
	result, err := g.Execute(context.Background(), map[string]any{
		"pattern": filepath.Join(dir, "*.xyz"),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}
	// Should be empty string (no matches).
	if result.Content != "" {
		t.Fatalf("expected empty result, got: %q", result.Content)
	}
}

func TestGlobMissingPattern(t *testing.T) {
	g := NewGlob()
	result, err := g.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error for missing pattern")
	}
}

func TestGlobInvalidPattern(t *testing.T) {
	g := NewGlob()
	result, err := g.Execute(context.Background(), map[string]any{
		"pattern": `[`,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error for invalid pattern")
	}
}

func TestGlobSorted(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "z.go"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte(""), 0o644)

	g := NewGlob()
	result, _ := g.Execute(context.Background(), map[string]any{
		"pattern": filepath.Join(dir, "*.go"),
	})

	// a.go should come before z.go.
	aIdx := indexOf(result.Content, "a.go")
	zIdx := indexOf(result.Content, "z.go")
	if aIdx < 0 || zIdx < 0 {
		t.Fatal("expected both files in results")
	}
	if aIdx > zIdx {
		t.Fatal("expected sorted output")
	}
}

func TestDoublestarGlobEmptyRoot(t *testing.T) {
	matches := doublestarGlob("*.go")
	if matches != nil {
		t.Fatal("expected nil for non-doublestar pattern")
	}
}

func TestDoublestarGlobNoSuffix(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "base.go"), []byte(""), 0o644)

	// Pattern "dir/**" should match everything under dir.
	pattern := filepath.Join(dir, "**")
	matches := doublestarGlob(pattern)
	_ = matches
}
