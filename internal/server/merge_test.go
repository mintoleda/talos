package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/gitutil"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestMergePreviewAndSquashCleanup(t *testing.T) {
	m, _ := testManager(t)
	repo := initGitRepo(t)
	ctx := context.Background()

	info, err := m.Create(ctx, rpc.CreateSessionParams{Dir: repo})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, info.Dir, "feat.txt", "hello\n")
	gitIn(t, info.Dir, "add", ".")
	gitIn(t, info.Dir, "commit", "-m", "add feat")

	prev, err := m.MergePreview(rpc.MergePreviewParams{ID: info.ID})
	if err != nil {
		t.Fatalf("MergePreview: %v", err)
	}
	if prev.Base != "main" || prev.Branch != info.Branch {
		t.Fatalf("preview base/branch = %q/%q", prev.Base, prev.Branch)
	}
	if prev.Ahead != 1 || len(prev.Commits) != 1 || len(prev.Files) != 1 {
		t.Fatalf("preview = %+v", prev)
	}
	if !prev.CanFF {
		t.Fatal("expected can_ff")
	}

	diff, err := m.MergeFileDiff(rpc.MergeFileDiffParams{ID: info.ID, Path: "feat.txt"})
	if err != nil || diff.Unified == "" {
		t.Fatalf("FileDiff: %v %#v", err, diff)
	}

	result, err := m.MergeExecute(rpc.MergeExecuteParams{
		ID:       info.ID,
		Strategy: "squash",
		Message:  "squash feat",
		Cleanup:  true,
	})
	if err != nil {
		t.Fatalf("MergeExecute: %v", err)
	}
	if !result.Merged || result.Conflict || result.SHA == "" {
		t.Fatalf("result = %+v", result)
	}

	// Worktree gone, branch gone, main has the file.
	if _, err := os.Stat(info.Dir); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	ok, _ := gitutil.BranchExists(repo, info.Branch)
	if ok {
		t.Fatal("branch should be deleted")
	}
	if _, err := os.Stat(filepath.Join(repo, "feat.txt")); err != nil {
		t.Fatalf("feat.txt missing on main: %v", err)
	}

	meta, err := FindSessionMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Merged {
		t.Fatal("expected meta.Merged")
	}
	list := m.List()
	found := false
	for _, s := range list {
		if s.ID == info.ID {
			found = true
			if !s.Merged || s.State != "merged" || s.Live {
				t.Fatalf("listed session = %+v", s)
			}
		}
	}
	if !found {
		t.Fatal("merged session missing from list")
	}
	if _, err := m.Resume(ctx, info.ID); err == nil {
		t.Fatal("expected merged session resume to be rejected")
	}
}

func TestMergeDirtyWorktreeGate(t *testing.T) {
	m, _ := testManager(t)
	repo := initGitRepo(t)
	info, err := m.Create(context.Background(), rpc.CreateSessionParams{Dir: repo})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, info.Dir, "dirty.txt", "x\n")

	prev, err := m.MergePreview(rpc.MergePreviewParams{ID: info.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !prev.DirtyWorktree {
		t.Fatal("expected dirty_worktree")
	}

	_, err = m.MergeExecute(rpc.MergeExecuteParams{ID: info.ID, Strategy: "squash", Cleanup: false})
	if err == nil {
		t.Fatal("expected execute to refuse dirty worktree")
	}

	sha, err := m.MergeCommitWorktree(rpc.MergeCommitWorktreeParams{
		ID: info.ID, Message: "commit dirty",
	})
	if err != nil || sha.SHA == "" {
		t.Fatalf("CommitWorktree: %v %#v", err, sha)
	}

	result, err := m.MergeExecute(rpc.MergeExecuteParams{
		ID: info.ID, Strategy: "ff", Cleanup: true,
	})
	if err != nil || !result.Merged {
		t.Fatalf("execute after commit: %v %+v", err, result)
	}
}

func TestMergeConflictPreflight(t *testing.T) {
	m, _ := testManager(t)
	repo := initGitRepo(t)
	ctx := context.Background()

	info, err := m.Create(ctx, rpc.CreateSessionParams{Dir: repo})
	if err != nil {
		t.Fatal(err)
	}
	// Diverge: edit same file on main and in worktree.
	writeFile(t, repo, "f.txt", "main-side\n")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-m", "main edit")

	writeFile(t, info.Dir, "f.txt", "branch-side\n")
	gitIn(t, info.Dir, "add", ".")
	gitIn(t, info.Dir, "commit", "-m", "branch edit")

	result, err := m.MergeExecute(rpc.MergeExecuteParams{
		ID: info.ID, Strategy: "merge", Cleanup: false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Merged || !result.Conflict || len(result.ConflictFiles) == 0 {
		t.Fatalf("expected conflict result, got %+v", result)
	}
	// Origin checkout untouched.
	data, _ := os.ReadFile(filepath.Join(repo, "f.txt"))
	if string(data) != "main-side\n" {
		t.Fatalf("main file changed: %q", data)
	}
	ok, _ := gitutil.BranchExists(repo, info.Branch)
	if !ok {
		t.Fatal("branch should still exist after conflict")
	}
}

func TestMergeDirtyMainGate(t *testing.T) {
	m, _ := testManager(t)
	repo := initGitRepo(t)
	info, err := m.Create(context.Background(), rpc.CreateSessionParams{Dir: repo})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, info.Dir, "branch.txt", "branch\n")
	gitIn(t, info.Dir, "add", ".")
	gitIn(t, info.Dir, "commit", "-m", "branch edit")

	// A staged but unrelated primary-checkout change must never be folded into
	// the squash commit.
	writeFile(t, repo, "unrelated.txt", "staged-main\n")
	gitIn(t, repo, "add", "unrelated.txt")

	prev, err := m.MergePreview(rpc.MergePreviewParams{ID: info.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !prev.DirtyMain || len(prev.DirtyMainHit) != 0 {
		t.Fatalf("expected unrelated dirty main, got %+v", prev)
	}

	_, err = m.MergeExecute(rpc.MergeExecuteParams{ID: info.ID, Strategy: "squash"})
	if err == nil {
		t.Fatal("expected refuse on dirty main checkout")
	}
	if _, err := os.Stat(filepath.Join(repo, "branch.txt")); !os.IsNotExist(err) {
		t.Fatalf("branch change was merged despite dirty-main gate: %v", err)
	}
}

func TestSessionWorktreeStartsFromDefaultBranch(t *testing.T) {
	m, _ := testManager(t)
	repo := initGitRepo(t)
	gitIn(t, repo, "switch", "-c", "develop")
	writeFile(t, repo, "develop-only.txt", "develop\n")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-m", "develop work")

	info, err := m.Create(context.Background(), rpc.CreateSessionParams{Dir: repo})
	if err != nil {
		t.Fatal(err)
	}
	base, err := gitutil.MergeBase(repo, "main", info.Branch)
	if err != nil {
		t.Fatal(err)
	}
	mainHead, err := gitutil.RevParse(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if base != mainHead {
		t.Fatalf("worktree base = %s, want main %s", base, mainHead)
	}
	meta, err := FindSessionMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.DefaultBranch != "main" {
		t.Fatalf("default branch = %q, want main", meta.DefaultBranch)
	}
}

func TestMergeNonWorktreeRejected(t *testing.T) {
	m, _ := testManager(t)
	info, err := m.Create(context.Background(), rpc.CreateSessionParams{
		Dir: t.TempDir(), Isolation: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.MergePreview(rpc.MergePreviewParams{ID: info.ID})
	if err == nil {
		t.Fatal("expected reject for none isolation")
	}
}
