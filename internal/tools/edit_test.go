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
