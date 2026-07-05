package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEditSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	os.WriteFile(path, []byte("hello world\nsecond line\n"), 0o644)

	rs := NewReadSet()
	rs.Record(path)
	edit := NewEdit(rs)

	res, err := edit.Execute(context.Background(), map[string]any{
		"path":        path,
		"old_string":  "hello world",
		"new_string":  "hi world",
		"replace_all": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "hi world\nsecond line\n" {
		t.Fatalf("edit failed: %q", string(data))
	}
}

func TestEditWithoutRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	os.WriteFile(path, []byte("x"), 0o644)
	edit := NewEdit(NewReadSet())
	res, _ := edit.Execute(context.Background(), map[string]any{"path": path, "old_string": "x", "new_string": "y"})
	if !res.IsError {
		t.Fatal("expected read-before-edit rejection")
	}
}

func TestEditAmbiguous(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	os.WriteFile(path, []byte("aaa aaa"), 0o644)
	rs := NewReadSet()
	rs.Record(path)
	edit := NewEdit(rs)
	res, _ := edit.Execute(context.Background(), map[string]any{"path": path, "old_string": "aaa", "new_string": "b"})
	if !res.IsError {
		t.Fatal("expected ambiguous rejection")
	}
}

func TestEditFuzzyMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	// Go code with tabs for indentation
	os.WriteFile(path, []byte("package main\n\nfunc main() {\n\treg.Add(foo)\n\treg.Add(bar)\n}\n"), 0o644)

	rs := NewReadSet()
	rs.Record(path)
	edit := NewEdit(rs)

	// LLM provides old_string with wrong tab count (2 tabs instead of 1)
	res, _ := edit.Execute(context.Background(), map[string]any{
		"path":       path,
		"old_string": "\t\treg.Add(foo)\n\t\treg.Add(bar)",
		"new_string": "\t\treg.Add(foo)\n\t\treg.Add(baz)",
	})
	if res.IsError {
		t.Fatalf("expected fuzzy match to succeed, got: %s", res.Content)
	}
	data, _ := os.ReadFile(path)
	want := "package main\n\nfunc main() {\n\treg.Add(foo)\n\treg.Add(baz)\n}\n"
	if string(data) != want {
		t.Fatalf("fuzzy edit failed:\ngot:  %q\nwant: %q", string(data), want)
	}
}

func TestEditFuzzyMultiLineBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.go")
	content := "type Config struct {\n\tHost string\n\tPort int\n}\n"
	os.WriteFile(path, []byte(content), 0o644)

	rs := NewReadSet()
	rs.Record(path)
	edit := NewEdit(rs)

	// Agent uses spaces instead of tabs for indentation
	res, _ := edit.Execute(context.Background(), map[string]any{
		"path":       path,
		"old_string": "    Host string\n    Port int",
		"new_string": "    Host string\n    Port int\n    Timeout time.Duration",
	})
	if res.IsError {
		t.Fatalf("expected fuzzy match to succeed, got: %s", res.Content)
	}
	data, _ := os.ReadFile(path)
	want := "type Config struct {\n\tHost string\n\tPort int\n\tTimeout time.Duration\n}\n"
	if string(data) != want {
		t.Fatalf("fuzzy edit failed:\ngot:  %q\nwant: %q", string(data), want)
	}
}

func TestEditFuzzyAmbiguous(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dup.go")
	// Two blocks with identical content but different indentation
	content := "func a() {\n\tfoo()\n}\nfunc b() {\n\tfoo()\n}\n"
	os.WriteFile(path, []byte(content), 0o644)

	rs := NewReadSet()
	rs.Record(path)
	edit := NewEdit(rs)

	res, _ := edit.Execute(context.Background(), map[string]any{
		"path":       path,
		"old_string": "    foo()",
		"new_string": "    bar()",
	})
	if !res.IsError {
		t.Fatal("expected fuzzy ambiguous rejection")
	}
}
