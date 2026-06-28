package fff

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIndexBuildAndFind(t *testing.T) {
	dir := t.TempDir()
	idxDir := t.TempDir()

	// Create some files.
	mustWrite(t, filepath.Join(dir, "prompt.go"), "package main")
	mustWrite(t, filepath.Join(dir, "builder.go"), "package main")
	mustWrite(t, filepath.Join(dir, "nested", "deep_prompt.go"), "package nested")
	mustWrite(t, filepath.Join(dir, "node_modules", "bad.js"), "ignore me")
	mustWrite(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main")

	idx := NewIndex(dir, idxDir)
	if err := idx.Build(); err != nil {
		t.Fatal(err)
	}

	if idx.Stats() != 3 {
		t.Fatalf("expected 3 indexed files, got %d", idx.Stats())
	}

	results := idx.Find("prompt", 10)
	if len(results) == 0 {
		t.Fatal("expected matches for 'prompt'")
	}
	if results[0].Path != filepath.Join(dir, "prompt.go") {
		t.Fatalf("expected prompt.go first, got %s", results[0].Path)
	}

	// Frecency boost.
	idx.RecordRead(filepath.Join(dir, "nested", "deep_prompt.go"))
	results = idx.Find("prompt", 10)
	if results[0].Path != filepath.Join(dir, "nested", "deep_prompt.go") {
		t.Fatalf("expected boosted deep_prompt.go first, got %s", results[0].Path)
	}
}

func TestIndexFuzzyNormalizesSeparators(t *testing.T) {
	dir := t.TempDir()
	idxDir := t.TempDir()

	mustWrite(t, filepath.Join(dir, "prompt_builder.go"), "package main")

	idx := NewIndex(dir, idxDir)
	if err := idx.Build(); err != nil {
		t.Fatal(err)
	}

	results := idx.Find("prompt build", 10)
	if len(results) == 0 || results[0].Path != filepath.Join(dir, "prompt_builder.go") {
		t.Fatalf("expected prompt_builder.go to match 'prompt build', got %+v", results)
	}
}

func TestIndexSearchFiles(t *testing.T) {
	dir := t.TempDir()
	idxDir := t.TempDir()

	mustWrite(t, filepath.Join(dir, "a.go"), "func ThinkingLevel() string\nfunc main() {}")
	mustWrite(t, filepath.Join(dir, "b.go"), "// thinking budget\nvar x = 1")

	idx := NewIndex(dir, idxDir)
	if err := idx.Build(); err != nil {
		t.Fatal(err)
	}

	results, err := idx.SearchFiles("thinking level", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected content matches")
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
