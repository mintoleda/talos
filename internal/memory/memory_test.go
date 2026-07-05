package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPath(t *testing.T) {
	p := Path("/tmp/talos-test")
	if !filepath.IsAbs(p) {
		t.Fatal("expected absolute path")
	}
	if !strings.HasSuffix(p, "memory.md") {
		t.Fatal("path should end with memory.md")
	}
}

func TestLoadNonExistent(t *testing.T) {
	content, err := Load("/nonexistent/talos-test-dir")
	if err != nil {
		t.Fatalf("Load should not error for non-existent file: %v", err)
	}
	if content != "" {
		t.Fatalf("expected empty content, got %q", content)
	}
}

func TestAppendAndLoad(t *testing.T) {
	dir := t.TempDir()

	err := Append(dir, "user said something important")
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	content, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if content == "" {
		t.Fatal("expected non-empty content after append")
	}
	if !strings.Contains(content, "user said something important") {
		t.Fatalf("content should contain appended entry, got %q", content)
	}
}

func TestAppendMultiple(t *testing.T) {
	dir := t.TempDir()

	err := Append(dir, "first memory")
	if err != nil {
		t.Fatalf("Append first: %v", err)
	}
	err = Append(dir, "second memory")
	if err != nil {
		t.Fatalf("Append second: %v", err)
	}

	content, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !strings.Contains(content, "first memory") {
		t.Fatal("content should contain first entry")
	}
	if !strings.Contains(content, "second memory") {
		t.Fatal("content should contain second entry")
	}
}

func TestAppendTimestamps(t *testing.T) {
	dir := t.TempDir()

	err := Append(dir, "test entry")
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Read the raw file to check timestamp format.
	data, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	// Should contain RFC3339 timestamp.
	if !strings.Contains(string(data), "T") || !strings.Contains(string(data), "Z") {
		t.Fatal("expected RFC3339 timestamp in memory entry")
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir)

	// Write directly.
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(path, []byte("existing memory content"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	content, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if content != "existing memory content" {
		t.Fatalf("expected 'existing memory content', got %q", content)
	}
}

func TestLoadTrimsSpace(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir)

	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("  content with spaces  \n\n"), 0o644)

	content, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if content != "content with spaces" {
		t.Fatalf("expected trimmed content, got %q", content)
	}
}

func TestAppendEmptyEntry(t *testing.T) {
	dir := t.TempDir()

	err := Append(dir, "")
	if err != nil {
		t.Fatalf("Append empty: %v", err)
	}

	content, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if content == "" {
		t.Fatal("expected at least timestamp after appending empty entry")
	}
}
