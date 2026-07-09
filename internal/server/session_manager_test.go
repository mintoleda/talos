package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/engine"
	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/session"
	"github.com/mintoleda/talos/internal/testutil"
)

func testManager(t *testing.T) (*SessionManager, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	baseDir := filepath.Join(home, ".talos")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		BaseDir:  baseDir,
		Provider: "test",
		Model:    "test-model",
	}
	m := NewSessionManager(cfg)

	var seq int
	m.buildFn = func(_ context.Context, o engine.BuildOpts) (*engine.Built, error) {
		seq++
		id := o.SessionID
		if id == "" {
			id = fmtID(seq)
		}
		dir := o.Dir
		projectDir := o.ProjectDir
		if projectDir == "" {
			projectDir = dir
		}
		pid := session.ProjectHash(projectDir)
		txPath := filepath.Join(session.SessionsDir(), pid, id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(txPath), 0o755); err != nil {
			return nil, err
		}
		// Ensure transcript file exists for List/Delete.
		if _, err := os.Stat(txPath); os.IsNotExist(err) {
			if err := os.WriteFile(txPath, nil, 0o600); err != nil {
				return nil, err
			}
		}
		return &engine.Built{
			Cfg: &config.Config{
				BaseDir:  cfg.BaseDir,
				Provider: firstNonEmpty(o.Provider, cfg.Provider),
				Model:    firstNonEmpty(o.Model, cfg.Model),
			},
			Dir:        dir,
			ProjectDir: projectDir,
			Session:    session.Session{ID: id, ProjectID: pid, Path: txPath},
		}, nil
	}
	m.newEng = func(b *engine.Built, ctx context.Context) *engine.Engine {
		tx := testutil.NewTestTranscript(t)
		prov := &testutil.FakeProvider{}
		exec := &testutil.FakeExecutor{}
		pb := loop.NewPromptBuilder("system", nil, b.Cfg.Model)
		lp := loop.New(prov, exec, tx, pb)
		return engine.NewEngine(engine.Params{
			Loop:          lp,
			PromptBuilder: pb,
			Cfg:           b.Cfg,
			Provider:      b.Cfg.Provider,
			Model:         b.Cfg.Model,
			BaseDir:       b.Cfg.BaseDir,
			CWD:           b.Dir,
			SessionID:     b.Session.ID,
			Context:       ctx,
		})
	}
	return m, home
}

func fmtID(n int) string {
	return fmt.Sprintf("sess%d", n)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func TestSessionManagerCreateListStopResumeDelete(t *testing.T) {
	m, _ := testManager(t)
	ctx := context.Background()
	dir := t.TempDir()

	info, err := m.Create(ctx, rpc.CreateSessionParams{Dir: dir, Isolation: "none"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.ID == "" || !info.Live || info.State != "idle" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if info.Isolation != "none" {
		t.Fatalf("isolation = %q, want none", info.Isolation)
	}
	if info.Dir != dir && info.Dir != mustAbs(t, dir) {
		t.Fatalf("dir = %q, want %q", info.Dir, dir)
	}

	// Meta sidecar written.
	meta, err := FindSessionMeta(info.ID)
	if err != nil {
		t.Fatalf("FindSessionMeta: %v", err)
	}
	if meta.Dir != mustAbs(t, dir) {
		t.Fatalf("meta.Dir = %q", meta.Dir)
	}

	list := m.List()
	if len(list) != 1 || list[0].ID != info.ID || !list[0].Live {
		t.Fatalf("List = %+v", list)
	}

	if err := m.Stop(info.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, ok := m.Get(info.ID); ok {
		t.Fatal("expected engine unloaded after Stop")
	}
	list = m.List()
	if len(list) != 1 || list[0].Live || list[0].State != "unloaded" {
		t.Fatalf("after Stop List = %+v", list)
	}

	eng, err := m.Resume(ctx, info.ID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if eng.SessionID() != info.ID {
		t.Fatalf("Resume id = %s", eng.SessionID())
	}
	if _, ok := m.Get(info.ID); !ok {
		t.Fatal("expected live after Resume")
	}

	// Resume of live returns same engine.
	eng2, err := m.Resume(ctx, info.ID)
	if err != nil || eng2 != eng {
		t.Fatalf("Resume live: eng=%v err=%v", eng2 == eng, err)
	}

	if err := m.Delete(info.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := FindSessionMeta(info.ID); err == nil {
		t.Fatal("expected meta gone after Delete")
	}
	if len(m.List()) != 0 {
		t.Fatalf("expected empty list, got %+v", m.List())
	}
}

func TestSessionManagerResumeRequiresID(t *testing.T) {
	m, _ := testManager(t)
	if _, err := m.Resume(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestSessionManagerStatusFn(t *testing.T) {
	m, _ := testManager(t)
	got := make(chan protocol.SessionStatus, 4)
	m.SetStatusFn(func(st protocol.SessionStatus) {
		got <- st
	})
	ctx := context.Background()
	info, err := m.Create(ctx, rpc.CreateSessionParams{Dir: t.TempDir(), Isolation: "none"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	eng, _ := m.Get(info.ID)
	eng.Emit(protocol.SessionStatus{ID: info.ID, State: "busy", Dir: info.Dir})

	select {
	case st := <-got:
		if st.State != "busy" || st.ID != info.ID {
			t.Fatalf("status = %+v", st)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for status")
	}

	if err := m.Stop(info.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case st := <-got:
		if st.State != "unloaded" {
			t.Fatalf("stop status = %+v", st)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for unloaded")
	}
}

func TestWriteFindSessionMeta(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	meta := SessionMeta{
		ID:         "abc123",
		Dir:        dir,
		ProjectDir: dir,
		Isolation:  "none",
		Provider:   "openai",
		Model:      "gpt",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
	}
	if err := WriteSessionMeta(meta); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := FindSessionMeta("abc123")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Dir != dir || got.Provider != "openai" {
		t.Fatalf("got %+v", got)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func TestSessionManagerCreateWorktreeFallbackNone(t *testing.T) {
	m, _ := testManager(t)
	info, err := m.Create(context.Background(), rpc.CreateSessionParams{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if info.Isolation != "none" {
		t.Fatalf("expected fallback none, got %q", info.Isolation)
	}
}

func TestSessionManagerCreateWorktree(t *testing.T) {
	m, home := testManager(t)
	repo := initGitRepo(t)
	ctx := context.Background()

	info, err := m.Create(ctx, rpc.CreateSessionParams{Dir: repo})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Isolation != "worktree" {
		t.Fatalf("isolation = %q", info.Isolation)
	}
	if info.Branch != "talos/"+info.ID {
		t.Fatalf("branch = %q", info.Branch)
	}
	wantWT := filepath.Join(home, ".talos", "worktrees", session.ProjectHash(mustAbs(t, repo)), info.ID)
	if info.Dir != wantWT {
		t.Fatalf("dir = %q, want %q", info.Dir, wantWT)
	}
	if info.ProjectDir != mustAbs(t, repo) && info.ProjectDir != repo {
		// RepoRoot may return cleaned path
		root, _ := filepath.EvalSymlinks(mustAbs(t, repo))
		pd, _ := filepath.EvalSymlinks(info.ProjectDir)
		if pd != root && info.ProjectDir != mustAbs(t, repo) {
			t.Fatalf("projectDir = %q", info.ProjectDir)
		}
	}
	meta, err := FindSessionMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Transcript keyed by ProjectDir, not worktree.
	tx := filepath.Join(session.SessionsDir(), session.ProjectHash(meta.ProjectDir), info.ID+".jsonl")
	if _, err := os.Stat(tx); err != nil {
		t.Fatalf("transcript missing at %s: %v", tx, err)
	}

	if err := m.Delete(info.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestSessionManagerDeleteWorktreeMergedVsUnmerged(t *testing.T) {
	m, _ := testManager(t)
	repo := initGitRepo(t)
	ctx := context.Background()

	info, err := m.Create(ctx, rpc.CreateSessionParams{Dir: repo})
	if err != nil {
		t.Fatal(err)
	}
	// Commit on worktree so branch is unmerged.
	if err := os.WriteFile(filepath.Join(info.Dir, "extra.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(info.Dir, "add", ".")
	run(info.Dir, "commit", "-m", "wt")

	branch := info.Branch
	if err := m.Delete(info.ID); err != nil {
		t.Fatal(err)
	}
	// Branch should remain (unmerged).
	cmd := exec.Command("git", "branch", "--list", branch)
	cmd.Dir = repo
	out, _ := cmd.CombinedOutput()
	if strings.TrimSpace(string(out)) == "" {
		t.Fatal("expected unmerged branch retained")
	}

	// Merged case: create, merge, delete.
	info2, err := m.Create(ctx, rpc.CreateSessionParams{Dir: repo})
	if err != nil {
		t.Fatal(err)
	}
	run(info2.Dir, "commit", "--allow-empty", "-m", "empty")
	run(repo, "merge", info2.Branch)
	branch2 := info2.Branch
	if err := m.Delete(info2.ID); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "branch", "--list", branch2)
	cmd.Dir = repo
	out, _ = cmd.CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("expected merged branch deleted, still have %q", out)
	}
}

func TestSessionManagerOrphanGC(t *testing.T) {
	m, home := testManager(t)
	orphan := filepath.Join(home, ".talos", "worktrees", "deadproj", "orphansess")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	orphans := m.OrphanWorktrees()
	found := false
	for _, o := range orphans {
		if o == orphan {
			found = true
		}
	}
	if !found {
		t.Fatalf("orphan not reported: %v", orphans)
	}
	removed, err := m.GCWorktrees()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) == 0 {
		t.Fatal("expected removed")
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan dir still present")
	}
}
