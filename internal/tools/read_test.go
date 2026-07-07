package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadToolName(t *testing.T) {
	r := NewRead(nil, nil)
	if r.Name() != "read" {
		t.Fatalf("expected 'read', got %q", r.Name())
	}
}

func TestReadToolDescription(t *testing.T) {
	r := NewRead(nil, nil)
	if r.Description() == "" {
		t.Fatal("expected non-empty description")
	}
}

func TestReadToolSchema(t *testing.T) {
	r := NewRead(nil, nil)
	schema := r.Schema()
	if schema == nil {
		t.Fatal("expected non-nil schema")
	}
}

func TestReadExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello\nworld\n"), 0o644)

	reads := NewReadSet()
	r := NewRead(reads, nil)
	result, err := r.Execute(context.Background(), map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}
	if !reads.SeenAndFresh(path) {
		t.Fatal("expected file to be recorded as read")
	}
}

func TestReadWithOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("line1\nline2\nline3\nline4\nline5\n"), 0o644)

	reads := NewReadSet()
	r := NewRead(reads, nil)
	result, err := r.Execute(context.Background(), map[string]any{"path": path, "offset": 3})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}
	// Should contain line3 onward.
	if !contains(result.Content, "line3") {
		t.Fatal("result should contain line3")
	}
	// Should NOT contain line1.
	if contains(result.Content, "line1") {
		t.Fatal("result should NOT contain line1 (offset 3)")
	}
}

func TestReadWithLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644)

	reads := NewReadSet()
	r := NewRead(reads, nil)
	result, err := r.Execute(context.Background(), map[string]any{"path": path, "limit": 2})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}
	// Should contain truncated notice.
	if !contains(result.Content, "truncated") {
		t.Fatal("result should contain truncated notice")
	}
}

func TestReadNonExistentFile(t *testing.T) {
	r := NewRead(nil, nil)
	result, err := r.Execute(context.Background(), map[string]any{"path": "/nonexistent/file.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error for non-existent file")
	}
}

func TestReadMissingPath(t *testing.T) {
	r := NewRead(nil, nil)
	result, err := r.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error for missing path")
	}
}

func TestReadLineNumbers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lines.txt")
	os.WriteFile(path, []byte("a\nb\nc\n"), 0o644)

	r := NewRead(NewReadSet(), nil)
	result, _ := r.Execute(context.Background(), map[string]any{"path": path})
	// Should contain line numbers.
	if !contains(result.Content, "1 | ") {
		t.Fatal("should contain line number '1'")
	}
	if !contains(result.Content, "3 | c") {
		t.Fatal("should contain line 3 content")
	}
}

func TestReadByteCap(t *testing.T) {
	// A file with few but enormous lines must not blow past the byte cap.
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.log")
	line := strings.Repeat("x", 500*1024)
	if err := os.WriteFile(path, []byte(line+"\n"+line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRead(NewReadSet(), nil)
	result, _ := r.Execute(context.Background(), map[string]any{"path": path})
	if len(result.Content) > maxReadBytes+maxReadLineBytes+1024 {
		t.Fatalf("output not capped: %d bytes", len(result.Content))
	}
	if !contains(result.Content, "[line truncated]") {
		t.Fatal("expected per-line truncation marker")
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
