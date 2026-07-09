package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepo(t *testing.T) string {
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
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "initial")
	return dir
}

func TestRepoRootAndIsRepo(t *testing.T) {
	repo := initRepo(t)
	root, err := RepoRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	if root != repo {
		// macOS /tmp vs /private/tmp — compare abs
		absRepo, _ := filepath.Abs(repo)
		absRoot, _ := filepath.Abs(root)
		if absRoot != absRepo {
			t.Fatalf("RepoRoot = %q, want %q", root, repo)
		}
	}
	if !IsRepo(repo) {
		t.Fatal("expected IsRepo true")
	}
	if IsRepo(t.TempDir()) {
		t.Fatal("expected IsRepo false for non-repo")
	}
}

func TestIsDirty(t *testing.T) {
	repo := initRepo(t)
	dirty, err := IsDirty(repo)
	if err != nil || dirty {
		t.Fatalf("clean tree: dirty=%v err=%v", dirty, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err = IsDirty(repo)
	if err != nil || !dirty {
		t.Fatalf("dirty tree: dirty=%v err=%v", dirty, err)
	}
}

func TestWorktreeAddBranchRemove(t *testing.T) {
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt1")
	branch := "talos/sess1"

	if err := WorktreeAdd(repo, wt, branch); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	cur, err := CurrentBranch(wt)
	if err != nil || cur != branch {
		t.Fatalf("CurrentBranch = %q err=%v", cur, err)
	}
	ok, err := BranchExists(repo, branch)
	if err != nil || !ok {
		t.Fatalf("BranchExists: ok=%v err=%v", ok, err)
	}
	list, err := WorktreeList(repo)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range list {
		if filepath.Clean(p) == filepath.Clean(wt) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("worktree %s not in list %v", wt, list)
	}

	// Commit on worktree so branch is ahead; then merge into main and delete.
	if err := os.WriteFile(filepath.Join(wt, "extra.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = wt
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "wt commit")
	cmd.Dir = wt
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}

	head, err := RevParse(repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	ahead, err := AheadCount(wt, head)
	if err != nil || ahead != 1 {
		t.Fatalf("AheadCount = %d err=%v", ahead, err)
	}

	if err := WorktreeRemove(repo, wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	// Unmerged: branch -d should fail.
	if err := BranchDelete(repo, branch); err == nil {
		t.Fatal("expected BranchDelete to fail for unmerged branch")
	}
	ok, _ = BranchExists(repo, branch)
	if !ok {
		t.Fatal("unmerged branch should still exist")
	}

	// Merge then delete.
	cmd = exec.Command("git", "merge", branch)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	if err := BranchDelete(repo, branch); err != nil {
		t.Fatalf("BranchDelete after merge: %v", err)
	}
	ok, _ = BranchExists(repo, branch)
	if ok {
		t.Fatal("branch should be gone after -d")
	}
}

func TestWorktreeAddFromBranch(t *testing.T) {
	repo := initRepo(t)
	wt1 := filepath.Join(t.TempDir(), "wt-a")
	branch := "talos/recreate"
	if err := WorktreeAdd(repo, wt1, branch); err != nil {
		t.Fatal(err)
	}
	if err := WorktreeRemove(repo, wt1); err != nil {
		t.Fatal(err)
	}
	wt2 := filepath.Join(t.TempDir(), "wt-b")
	if err := WorktreeAddFromBranch(repo, wt2, branch); err != nil {
		t.Fatalf("WorktreeAddFromBranch: %v", err)
	}
	cur, err := CurrentBranch(wt2)
	if err != nil || cur != branch {
		t.Fatalf("branch = %q err=%v", cur, err)
	}
	if !strings.Contains(wt2, "wt-b") {
		t.Fatal("unexpected path")
	}
}
