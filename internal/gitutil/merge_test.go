package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultBranch(t *testing.T) {
	repo := initRepo(t)
	b, err := DefaultBranch(repo)
	if err != nil || b != "main" {
		t.Fatalf("DefaultBranch = %q err=%v", b, err)
	}
}

func TestMergeBaseCommitListNumstat(t *testing.T) {
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	branch := "talos/feat"
	if err := WorktreeAdd(repo, wt, branch); err != nil {
		t.Fatal(err)
	}
	write(t, wt, "feat.txt", "hello\n")
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-m", "add feat")

	base, err := MergeBase(repo, "main", branch)
	if err != nil {
		t.Fatal(err)
	}
	mainHEAD, _ := RevParse(repo, "main")
	if base != mainHEAD {
		t.Fatalf("merge-base = %s, want %s", base, mainHEAD)
	}

	commits, err := CommitList(repo, "main", branch)
	if err != nil || len(commits) != 1 {
		t.Fatalf("commits=%v err=%v", commits, err)
	}
	if commits[0].Subject != "add feat" {
		t.Fatalf("subject = %q", commits[0].Subject)
	}

	files, err := DiffNumstat(repo, "main", branch)
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	if files[0].Path != "feat.txt" || files[0].Additions < 1 {
		t.Fatalf("file stat = %+v", files[0])
	}

	diff, err := FileDiff(repo, "main", branch, "feat.txt", 3)
	if err != nil || !contains(diff, "hello") {
		t.Fatalf("diff=%q err=%v", diff, err)
	}

	ahead, err := AheadCount(wt, "main")
	if err != nil || ahead != 1 {
		t.Fatalf("ahead=%d err=%v", ahead, err)
	}
	behind, err := BehindCount(wt, "main")
	if err != nil || behind != 0 {
		t.Fatalf("behind=%d err=%v", behind, err)
	}
}

func TestDiffAndDirtyPathsPreserveWhitespace(t *testing.T) {
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAdd(repo, wt, "talos/whitespace"); err != nil {
		t.Fatal(err)
	}
	const changed = "dir/file with spaces\tand-tab.txt"
	write(t, wt, changed, "hello\n")
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-m", "whitespace path")

	files, err := DiffNumstat(repo, "main", "talos/whitespace")
	if err != nil || len(files) != 1 || files[0].Path != changed {
		t.Fatalf("DiffNumstat = %+v, err=%v", files, err)
	}
	diff, err := FileDiff(repo, "main", "talos/whitespace", changed, 3)
	if err != nil || !contains(diff, "hello") {
		t.Fatalf("FileDiff = %q, err=%v", diff, err)
	}

	const dirty = "another file with spaces\tand-tab.txt"
	write(t, repo, dirty, "dirty\n")
	paths, err := DirtyPaths(repo)
	if err != nil || len(paths) != 1 || paths[0] != dirty {
		t.Fatalf("DirtyPaths = %#v, err=%v", paths, err)
	}
}

func TestMergeTreeCleanAndConflict(t *testing.T) {
	repo := initRepo(t)

	// Clean branch: add a new file.
	wtClean := filepath.Join(t.TempDir(), "clean")
	if err := WorktreeAdd(repo, wtClean, "talos/clean"); err != nil {
		t.Fatal(err)
	}
	write(t, wtClean, "new.txt", "ok\n")
	git(t, wtClean, "add", ".")
	git(t, wtClean, "commit", "-m", "clean change")

	base, err := MergeBase(repo, "main", "talos/clean")
	if err != nil {
		t.Fatal(err)
	}
	conflicts, err := MergeTreeConflicts(repo, base, "main", "talos/clean")
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected clean merge, got %v", conflicts)
	}

	// Conflict: diverge on same file.
	write(t, repo, "tracked.txt", "main-side\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "main edit")

	wtConflict := filepath.Join(t.TempDir(), "conflict")
	// Branch from original base (before main edit) — recreate from earlier tip.
	// Simpler: create branch from first commit, edit same file differently.
	first, _ := RevParse(repo, "HEAD~1")
	git(t, repo, "branch", "talos/conflict", first)
	if err := WorktreeAddFromBranch(repo, wtConflict, "talos/conflict"); err != nil {
		t.Fatal(err)
	}
	write(t, wtConflict, "tracked.txt", "branch-side\n")
	git(t, wtConflict, "add", ".")
	git(t, wtConflict, "commit", "-m", "branch edit")

	base2, err := MergeBase(repo, "main", "talos/conflict")
	if err != nil {
		t.Fatal(err)
	}
	conflicts, err = MergeTreeConflicts(repo, base2, "main", "talos/conflict")
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) == 0 {
		t.Fatal("expected conflict")
	}
}

func TestMergeStrategies(t *testing.T) {
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAdd(repo, wt, "talos/ff"); err != nil {
		t.Fatal(err)
	}
	write(t, wt, "a.txt", "a\n")
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-m", "ff commit")

	ok, err := IsAncestor(repo, "main", "talos/ff")
	if err != nil || !ok {
		t.Fatalf("expected ancestor: ok=%v err=%v", ok, err)
	}

	sha, err := MergeFF(repo, "main", "talos/ff")
	if err != nil || sha == "" {
		t.Fatalf("MergeFF: sha=%q err=%v", sha, err)
	}
	if err := WorktreeRemove(repo, wt); err != nil {
		t.Fatal(err)
	}
	if err := BranchDelete(repo, "talos/ff"); err != nil {
		t.Fatal(err)
	}

	// Squash
	wt2 := filepath.Join(t.TempDir(), "wt2")
	if err := WorktreeAdd(repo, wt2, "talos/sq"); err != nil {
		t.Fatal(err)
	}
	write(t, wt2, "b.txt", "b\n")
	git(t, wt2, "add", ".")
	git(t, wt2, "commit", "-m", "sq1")
	write(t, wt2, "c.txt", "c\n")
	git(t, wt2, "add", ".")
	git(t, wt2, "commit", "-m", "sq2")

	sha, err = MergeSquash(repo, "main", "talos/sq", "squash talos/sq")
	if err != nil || sha == "" {
		t.Fatalf("MergeSquash: %v", err)
	}
	if err := WorktreeRemove(repo, wt2); err != nil {
		t.Fatal(err)
	}
	// Squash does not mark the branch as merged for `branch -d`.
	if err := BranchForceDelete(repo, "talos/sq"); err != nil {
		t.Fatal(err)
	}

	// Merge commit
	wt3 := filepath.Join(t.TempDir(), "wt3")
	if err := WorktreeAdd(repo, wt3, "talos/mc"); err != nil {
		t.Fatal(err)
	}
	write(t, wt3, "d.txt", "d\n")
	git(t, wt3, "add", ".")
	git(t, wt3, "commit", "-m", "mc")

	sha, err = MergeCommit(repo, "main", "talos/mc", "merge talos/mc")
	if err != nil || sha == "" {
		t.Fatalf("MergeCommit: %v", err)
	}
}

func TestMergeSquashCommitFailurePreservesUnrelatedChanges(t *testing.T) {
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAdd(repo, wt, "talos/failing-squash"); err != nil {
		t.Fatal(err)
	}
	write(t, wt, "feature.txt", "feature\n")
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-m", "feature")

	write(t, repo, "tracked.txt", "local change\n")
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := MergeSquash(repo, "main", "talos/failing-squash", "failing squash"); err == nil {
		t.Fatal("expected squash commit failure")
	}
	got, err := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if err != nil || string(got) != "local change\n" {
		t.Fatalf("unrelated primary change lost: %q err=%v", got, err)
	}
}

func TestMergeRestoresOriginBranch(t *testing.T) {
	repo := initRepo(t)
	git(t, repo, "switch", "-c", "develop")
	wt := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAddAtRef(repo, wt, "talos/restore", "main"); err != nil {
		t.Fatal(err)
	}
	write(t, wt, "feature.txt", "feature\n")
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-m", "feature")

	if _, err := MergeSquash(repo, "main", "talos/restore", "squash feature"); err != nil {
		t.Fatal(err)
	}
	current, err := CurrentBranch(repo)
	if err != nil || current != "develop" {
		t.Fatalf("current branch = %q, err=%v", current, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("develop checkout retained merged file: %v", err)
	}
	if _, err := RevParse(repo, "main"); err != nil {
		t.Fatalf("main missing after merge: %v", err)
	}
}

func TestCommitAllAndDirtyPaths(t *testing.T) {
	repo := initRepo(t)
	write(t, repo, "tracked.txt", "changed\n")
	write(t, repo, "extra.txt", "x\n")
	paths, err := DirtyPaths(repo)
	if err != nil || len(paths) < 2 {
		t.Fatalf("DirtyPaths=%v err=%v", paths, err)
	}
	sha, err := CommitAll(repo, "commit all")
	if err != nil || sha == "" {
		t.Fatalf("CommitAll: %v", err)
	}
	dirty, _ := IsDirty(repo)
	if dirty {
		t.Fatal("expected clean after CommitAll")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})())
}
