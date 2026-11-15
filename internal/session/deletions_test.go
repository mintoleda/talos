package session

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSessionDeletionEvidence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	dir := filepath.Join(SessionsDir(), ProjectHash(project))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("removed.jsonl")
	write("removed.meta.json")
	write("legacy.meta.json")
	write("present.meta.json")
	write("present.jsonl")
	write("restored.deleted")
	write("restored.jsonl")

	if err := DeleteSession(project, "removed"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "removed.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("transcript still exists: %v", err)
	}
	if err := DeleteSession(project, "never-created"); !os.IsNotExist(err) {
		t.Fatalf("expected missing transcript error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "never-created.deleted")); !os.IsNotExist(err) {
		t.Fatalf("failed deletion left a marker: %v", err)
	}
	got, err := ListSessionDeletions()
	if err != nil {
		t.Fatal(err)
	}
	want := SessionDeletions{DeletedSessionIDs: []string{"removed"}, MissingSessionIDs: []string{"legacy"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSessionDeletionsMissingHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := ListSessionDeletions()
	if err != nil || len(got.DeletedSessionIDs) != 0 || len(got.MissingSessionIDs) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}
