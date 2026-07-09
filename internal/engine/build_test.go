package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/session"
)

func TestBuildSessionUsesProjectDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	baseDir := filepath.Join(home, ".talos")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}

	projectDir := t.TempDir()
	worktreeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktreeDir, "AGENTS.md"), []byte("from-wt"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		BaseDir:  baseDir,
		Provider: "openai",
		Model:    "test",
		BaseURL:  "http://127.0.0.1:1",
		APIKey:   "x",
	}
	sid := "prealloc123abc"
	built, err := Build(context.Background(), BuildOpts{
		Cfg:        cfg,
		Dir:        worktreeDir,
		ProjectDir: projectDir,
		SessionID:  sid,
		NoTools:    true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	if built.Session.ID != sid {
		t.Fatalf("session id = %q", built.Session.ID)
	}
	wantPath := filepath.Join(session.SessionsDir(), session.ProjectHash(projectDir), sid+".jsonl")
	if built.Session.Path != wantPath {
		t.Fatalf("path = %q, want %q", built.Session.Path, wantPath)
	}
	if built.Session.ProjectID != session.ProjectHash(projectDir) {
		t.Fatalf("ProjectID = %q", built.Session.ProjectID)
	}
	wrong := filepath.Join(session.SessionsDir(), session.ProjectHash(worktreeDir), sid+".jsonl")
	if built.Session.Path == wrong {
		t.Fatal("transcript should not be keyed by worktree dir")
	}
	if built.ProjectDir != projectDir {
		t.Fatalf("ProjectDir = %q", built.ProjectDir)
	}
	if built.Dir != worktreeDir {
		t.Fatalf("Dir = %q", built.Dir)
	}
	if !strings.Contains(built.Cfg.SystemPrompt, "from-wt") {
		t.Fatalf("expected SYSTEM_PROMPT from worktree, got %q", built.Cfg.SystemPrompt)
	}
}
