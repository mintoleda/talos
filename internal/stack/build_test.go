package stack

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/session"
)

func TestBuildRestoresDirectoryModelUnlessExplicitlyOverridden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project, dir, other := t.TempDir(), t.TempDir(), t.TempDir()
	if err := session.WriteSessionMeta(session.SessionMeta{
		ID: "previous", Dir: dir, ProjectDir: project,
		Provider: "openai-codex", Model: "remembered", LastActive: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		BaseDir: filepath.Join(home, ".talos"), Provider: "openai",
		Model: "default", BaseURL: "http://127.0.0.1:1", APIKey: "x",
	}
	for _, tc := range []struct {
		name, dir, provider, model, wantProvider, wantModel string
	}{
		{"remembered", dir, "", "", "openai-codex", "remembered"},
		{"other directory", other, "", "", "openai", "default"},
		{"explicit model", dir, "", "explicit", "openai", "explicit"},
		{"explicit provider", dir, "openai", "", "openai", "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			built, err := Build(context.Background(), BuildOpts{
				Cfg: cfg, Dir: tc.dir, ProjectDir: project, NoTools: true,
				Provider: tc.provider, Model: tc.model,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			if built.Cfg.Provider != tc.wantProvider || built.Cfg.Model != tc.wantModel {
				t.Fatalf("model = %s/%s, want %s/%s", built.Cfg.Provider, built.Cfg.Model, tc.wantProvider, tc.wantModel)
			}
			if tc.wantProvider != cfg.Provider && built.Cfg.BaseURL == cfg.BaseURL {
				t.Fatal("remembered provider inherited another provider's endpoint")
			}
		})
	}
	if cfg.Provider != "openai" || cfg.Model != "default" || cfg.BaseURL != "http://127.0.0.1:1" {
		t.Fatalf("shared config mutated: %+v", cfg)
	}
}

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
