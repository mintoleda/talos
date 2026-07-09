package gitutil

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// RepoRoot returns the git toplevel for dir (git rev-parse --show-toplevel).
func RepoRoot(dir string) (string, error) {
	out, err := run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// IsRepo reports whether dir is inside a git working tree.
func IsRepo(dir string) bool {
	_, err := RepoRoot(dir)
	return err == nil
}

// IsDirty reports whether the working tree has uncommitted changes.
func IsDirty(dir string) (bool, error) {
	out, err := run(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// WorktreeAdd creates a detached worktree at wtDir from projectDir's HEAD,
// then creates and checks out branch in that worktree.
func WorktreeAdd(projectDir, wtDir, branch string) error {
	if _, err := run(projectDir, "worktree", "add", "--detach", wtDir); err != nil {
		return err
	}
	if _, err := run(wtDir, "switch", "-c", branch); err != nil {
		_ = WorktreeRemove(projectDir, wtDir)
		return err
	}
	return nil
}

// WorktreeAddFromBranch recreates a worktree at wtDir checked out to an existing branch.
func WorktreeAddFromBranch(projectDir, wtDir, branch string) error {
	_, err := run(projectDir, "worktree", "add", wtDir, branch)
	return err
}

// WorktreeRemove force-removes a worktree registration and directory.
func WorktreeRemove(projectDir, wtDir string) error {
	_, err := run(projectDir, "worktree", "remove", "--force", wtDir)
	return err
}

// WorktreeList returns absolute worktree paths from git worktree list --porcelain.
func WorktreeList(projectDir string) ([]string, error) {
	out, err := run(projectDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			paths = append(paths, strings.TrimSpace(strings.TrimPrefix(line, "worktree ")))
		}
	}
	return paths, nil
}

// BranchDelete deletes a fully-merged branch (git branch -d). Never uses -D.
func BranchDelete(projectDir, branch string) error {
	_, err := run(projectDir, "branch", "-d", branch)
	return err
}

// BranchForceDelete deletes a branch even if unmerged (git branch -D).
// Used only for cleanup after a failed Create, never for user Delete.
func BranchForceDelete(projectDir, branch string) error {
	_, err := run(projectDir, "branch", "-D", branch)
	return err
}

// BranchExists reports whether refs/heads/<branch> exists.
func BranchExists(projectDir, branch string) (bool, error) {
	out, err := run(projectDir, "branch", "--list", branch)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// AheadCount returns how many commits in dir's HEAD are not reachable from upstream.
// Equivalent to: git rev-list --count upstream..HEAD
func AheadCount(dir, upstream string) (int, error) {
	if upstream == "" {
		upstream = "HEAD@{upstream}"
	}
	out, err := run(dir, "rev-list", "--count", upstream+"..HEAD")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parse ahead count: %w", err)
	}
	return n, nil
}

// CurrentBranch returns the short branch name for HEAD, or empty if detached.
func CurrentBranch(dir string) (string, error) {
	out, err := run(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	b := strings.TrimSpace(out)
	if b == "HEAD" {
		return "", nil
	}
	return b, nil
}

// RevParse returns the resolved object name for ref in dir.
func RevParse(dir, ref string) (string, error) {
	out, err := run(dir, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}
