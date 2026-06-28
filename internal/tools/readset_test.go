package tools

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReadSetSeenAndFresh: a record is only "fresh" if the on-disk file still
// matches the hash + mtime we saw.
func TestReadSetSeenAndFresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "v1")

	rs := NewReadSet()
	if rs.SeenAndFresh(path) {
		t.Fatal("empty set should not be fresh")
	}
	if err := rs.Record(path); err != nil {
		t.Fatal(err)
	}
	if !rs.SeenAndFresh(path) {
		t.Fatal("after Record, file should be fresh")
	}

	// External modification: bump mtime and change content.
	time.Sleep(2 * time.Millisecond)
	writeFile(t, path, "v2")
	if rs.SeenAndFresh(path) {
		t.Fatal("after external write, file should be stale")
	}
}

// TestReadSetRecentPathsOrder: a re-read moves the path to the most-recent
// slot, so RecentPaths(1) returns it last.
func TestReadSetRecentPathsOrder(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	c := filepath.Join(dir, "c.txt")
	writeFile(t, a, "a")
	writeFile(t, b, "b")
	writeFile(t, c, "c")

	rs := NewReadSet()
	_ = rs.Record(a)
	_ = rs.Record(b)
	_ = rs.Record(c)

	recent := rs.RecentPaths(3)
	if len(recent) != 3 {
		t.Fatalf("expected 3 recent, got %d", len(recent))
	}
	if recent[0] != c || recent[1] != b || recent[2] != a {
		t.Fatalf("unexpected order: %v", recent)
	}

	// Re-reading a moves it to the end.
	_ = rs.Record(a)
	recent = rs.RecentPaths(3)
	if recent[0] != a || recent[1] != c || recent[2] != b {
		t.Fatalf("after re-read, expected [a, c, b], got %v", recent)
	}
}

// TestReadSetSaveLoad: a saved set round-trips through LoadReadSet.
func TestReadSetSaveLoad(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	writeFile(t, a, "alpha")
	writeFile(t, b, "beta")

	src := NewReadSet()
	src.SetSavePath(filepath.Join(dir, "rs.json"))
	_ = src.Record(a)
	_ = src.Record(b)

	loaded, err := LoadReadSet(filepath.Join(dir, "rs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.SeenAndFresh(a) || !loaded.SeenAndFresh(b) {
		t.Fatalf("loaded set missing entries: all=%v", loaded.AllPaths())
	}
	// And it remembers the save path so subsequent Updates persist.
	_ = loaded.Record(a)
	if _, err := os.Stat(filepath.Join(dir, "rs.json")); err != nil {
		t.Fatalf("save path not preserved: %v", err)
	}
}

// TestReadSetLoadMissing: a missing file gives an empty set, not an error.
func TestReadSetLoadMissing(t *testing.T) {
	rs, err := LoadReadSet(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if rs.Len() != 0 {
		t.Fatalf("expected empty, got %d entries", rs.Len())
	}
}

// TestReadSetLoadCorrupt: a corrupt file is reported, not silently dropped.
func TestReadSetLoadCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rs.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReadSet(path); err == nil {
		t.Fatal("expected error on corrupt readset")
	}
}
