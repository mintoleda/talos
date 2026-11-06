package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLastModelForDirIsScopedAndOrdered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	dir := filepath.Join(project, "one")
	other := filepath.Join(project, "two")
	for _, p := range []string{dir, other} {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	for _, meta := range []SessionMeta{
		{ID: "old", Dir: dir, ProjectDir: project, Provider: "openai", Model: "old", LastActive: now.Add(-time.Hour)},
		{ID: "latest", Dir: dir, ProjectDir: project, Provider: "openai-codex", Model: "remembered", LastActive: now},
		{ID: "sibling", Dir: other, ProjectDir: project, Provider: "openai", Model: "wrong-directory", LastActive: now.Add(time.Hour)},
		{ID: "empty", Dir: dir, ProjectDir: project, LastActive: now.Add(2 * time.Hour)},
	} {
		if err := WriteSessionMeta(meta); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LastModelForDir(dir)
	if err != nil || got.ID != "latest" {
		t.Fatalf("last model = %+v, %v", got, err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if got, err := LastModelForDir(alias); err != nil || got.ID != "latest" {
		t.Fatalf("symlink last model = %+v, %v", got, err)
	}
	if _, err := LastModelForDir(t.TempDir()); err == nil {
		t.Fatal("an unused directory must not inherit another directory's model")
	}
}

func TestEnsureSessionMetaUpdatesModelAndPreservesIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project, dir := t.TempDir(), t.TempDir()
	sess := NewSession(project)
	created := time.Now().Add(-time.Hour)
	if err := WriteSessionMeta(SessionMeta{
		ID: sess.ID, Dir: dir, ProjectDir: project, Isolation: "worktree", Branch: "topic",
		Provider: "openai", Model: "old", CreatedAt: created, LastActive: created,
	}); err != nil {
		t.Fatal(err)
	}
	EnsureSessionMeta(sess, dir, project, "openai-codex", "new")
	got, err := ReadSessionMeta(MetaPath(sess.Path))
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "openai-codex" || got.Model != "new" || !got.LastActive.After(created) {
		t.Fatalf("model was not updated: %+v", got)
	}
	if got.Dir != dir || got.ProjectDir != project || got.Branch != "topic" || got.Isolation != "worktree" || !got.CreatedAt.Equal(created) {
		t.Fatalf("session identity changed: %+v", got)
	}
}
