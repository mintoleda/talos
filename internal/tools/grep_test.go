package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGrepToolName(t *testing.T) {
	g := NewGrep()
	if g.Name() != "grep" {
		t.Fatalf("expected 'grep', got %q", g.Name())
	}
}

func TestGrepToolSchema(t *testing.T) {
	g := NewGrep()
	schema := g.Schema()
	if schema == nil {
		t.Fatal("expected non-nil schema")
	}
}

func TestGrepWithGoRegexp(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\nfoo bar\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("foo hello\n"), 0o644)

	g := NewGrep()
	result, err := g.Execute(context.Background(), map[string]any{
		"pattern": "hello",
		"path":    dir,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}

	// Should find matches.
	if !contains(result.Content, "hello") {
		t.Fatalf("expected 'hello' in results, got: %s", result.Content)
	}
}

func TestGrepNoMatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\n"), 0o644)

	g := NewGrep()
	result, err := g.Execute(context.Background(), map[string]any{
		"pattern": "nonexistent",
		"path":    dir,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}
	if !contains(result.Content, "no matches") {
		t.Fatalf("expected '(no matches)', got: %s", result.Content)
	}
}

func TestGrepInvalidPattern(t *testing.T) {
	g := NewGrep()
	result, err := g.Execute(context.Background(), map[string]any{
		"pattern": `\zInvalid\yPattern`,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// May error or return no matches depending on regexp implementation.
	// Either is acceptable — just shouldn't panic.
	_ = result
}

func TestGrepMissingPattern(t *testing.T) {
	g := NewGrep()
	result, err := g.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error for missing pattern")
	}
}

func TestGrepWithPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.txt")
	os.WriteFile(path, []byte("secret: 42\n"), 0o644)
	// Another file without the pattern.
	os.WriteFile(filepath.Join(dir, "other.txt"), []byte("nothing\n"), 0o644)

	g := NewGrep()
	result, err := g.Execute(context.Background(), map[string]any{
		"pattern": "secret",
		"path":    path,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}
	if !contains(result.Content, "secret: 42") {
		t.Fatalf("expected 'secret: 42' in results, got: %s", result.Content)
	}
}

func TestGrepMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("common pattern\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("common pattern\n"), 0o644)

	g := NewGrep()
	result, err := g.Execute(context.Background(), map[string]any{
		"pattern": "common",
		"path":    dir,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}
	// Should include both files (with filenames).
	if !contains(result.Content, "a.txt") || !contains(result.Content, "b.txt") {
		t.Fatalf("expected both files in results, got: %s", result.Content)
	}
}
