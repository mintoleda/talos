package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteToolName(t *testing.T) {
	w := NewWrite(nil)
	if w.Name() != "write" {
		t.Fatalf("expected 'write', got %q", w.Name())
	}
}

func TestWriteToolSchema(t *testing.T) {
	w := NewWrite(nil)
	schema := w.Schema()
	if schema == nil {
		t.Fatal("expected non-nil schema")
	}
}

func TestWriteNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	w := NewWrite(NewReadSet())
	result, err := w.Execute(context.Background(), map[string]any{
		"path":    path,
		"content": "hello world",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}

	// Verify file was written.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", string(data))
	}
}

func TestWriteCreatesDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "dir", "file.txt")

	w := NewWrite(NewReadSet())
	result, err := w.Execute(context.Background(), map[string]any{
		"path":    path,
		"content": "nested",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("expected file to exist in created directories")
	}
}

func TestWriteExistingFileRequiresRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	os.WriteFile(path, []byte("original"), 0o644)

	reads := NewReadSet()
	w := NewWrite(reads)

	// Try to write without reading first.
	result, err := w.Execute(context.Background(), map[string]any{
		"path":    path,
		"content": "overwritten",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error for overwriting unread file")
	}
	if !contains(result.Content, "must read") {
		t.Fatalf("expected 'must read' error, got: %s", result.Content)
	}

	// Verify file was NOT overwritten.
	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Fatalf("file should not have been overwritten: got %q", string(data))
	}
}

func TestWriteExistingFileAfterRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	os.WriteFile(path, []byte("original"), 0o644)

	reads := NewReadSet()
	reads.Record(path)

	w := NewWrite(reads)
	result, err := w.Execute(context.Background(), map[string]any{
		"path":    path,
		"content": "updated",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success after read, got error: %s", result.Content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "updated" {
		t.Fatalf("expected 'updated', got %q", string(data))
	}
}

func TestWriteMissingPath(t *testing.T) {
	w := NewWrite(nil)
	result, err := w.Execute(context.Background(), map[string]any{"content": "test"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error for missing path")
	}
}

func TestWriteMissingContent(t *testing.T) {
	w := NewWrite(nil)
	result, err := w.Execute(context.Background(), map[string]any{"path": "/tmp/test.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error for missing content")
	}
}

func TestWriteUpdatesReadSet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update.txt")

	reads := NewReadSet()

	// New file write should not require a read.
	w := NewWrite(reads)
	result, _ := w.Execute(context.Background(), map[string]any{
		"path":    path,
		"content": "v1",
	})
	if result.IsError {
		t.Fatalf("expected success: %s", result.Content)
	}

	// ReadSet should be updated.
	if !reads.SeenAndFresh(path) {
		t.Fatal("expected file in readset after write")
	}
}
