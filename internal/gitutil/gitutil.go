package gitutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
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
	return WorktreeAddAtRef(projectDir, wtDir, branch, "HEAD")
}

// WorktreeAddAtRef creates a detached worktree from ref, then creates branch
// in that worktree. Callers that will merge back into a known base should pass
// that base rather than inheriting whatever branch happens to be checked out.
func WorktreeAddAtRef(projectDir, wtDir, branch, ref string) error {
	if ref == "" {
		ref = "HEAD"
	}
	if _, err := run(projectDir, "worktree", "add", "--detach", wtDir, ref); err != nil {
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

// WorktreeRemoveClean removes a verified-clean worktree without force.
func WorktreeRemoveClean(projectDir, wtDir string) error {
	_, err := run(projectDir, "worktree", "remove", wtDir)
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
// Used only after failed creation or verified squash-merge cleanup.
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

// LastCommitAt returns the committer timestamp of the repository's HEAD.
func LastCommitAt(dir string) (time.Time, error) {
	out, err := run(dir, "log", "-1", "--format=%cI")
	if err != nil {
		return time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(out))
	if err != nil {
		return time.Time{}, fmt.Errorf("parse last commit time: %w", err)
	}
	return t, nil
}

// VisibleFiles returns tracked and non-ignored untracked files using Git's
// own ignore rules. Paths are relative to dir and use slash separators.
func VisibleFiles(dir string) ([]string, error) {
	out, err := run(dir, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			files = append(files, filepath.ToSlash(path))
		}
	}
	return files, nil
}

// WorktreeStatus returns changes in the working tree relative to HEAD,
// including untracked files.
func WorktreeStatus(dir string) ([]FileStat, error) {
	nameOut, err := run(dir, "status", "--porcelain=v1", "-z")
	if err != nil {
		return nil, err
	}
	var files []FileStat
	for _, part := range strings.Split(nameOut, "\x00") {
		if len(part) < 4 {
			continue
		}
		status := strings.TrimSpace(part[:2])
		path := strings.TrimSpace(part[3:])
		if path == "" {
			continue
		}
		code := "M"
		switch {
		case strings.Contains(status, "A") || strings.Contains(status, "?"):
			code = "A"
		case strings.Contains(status, "D"):
			code = "D"
		case strings.Contains(status, "R"):
			code = "R"
		}
		files = append(files, FileStat{Path: filepath.ToSlash(path), Status: code})
	}
	if statOut, statErr := run(dir, "diff", "--no-ext-diff", "--numstat", "HEAD", "--"); statErr == nil {
		for _, line := range strings.Split(strings.TrimSpace(statOut), "\n") {
			fields := strings.SplitN(line, "\t", 3)
			if len(fields) != 3 {
				continue
			}
			add, _ := strconv.Atoi(fields[0])
			remove, _ := strconv.Atoi(fields[1])
			for i := range files {
				if files[i].Path == filepath.ToSlash(fields[2]) {
					files[i].Additions, files[i].Deletions = add, remove
				}
			}
		}
	}
	return files, nil
}

// WorktreeStatusAt returns tracked changes relative to base plus untracked
// files in dir. It is the status primitive used by Talos-owned worktrees:
// their baseline is the session's default branch rather than the worktree's
// current HEAD.
func WorktreeStatusAt(dir, base string) ([]FileStat, error) {
	if strings.TrimSpace(base) == "" || base == "HEAD" {
		return WorktreeStatus(dir)
	}

	nameOut, err := run(dir, "diff", "--no-ext-diff", "--name-status", "-z", base, "--")
	if err != nil {
		return nil, err
	}
	filesByPath := make(map[string]FileStat)
	parts := strings.Split(nameOut, "\x00")
	for index := 0; index+1 < len(parts); index += 2 {
		status := strings.TrimSpace(parts[index])
		path := strings.TrimSpace(parts[index+1])
		if path == "" {
			continue
		}
		code := "M"
		switch {
		case strings.HasPrefix(status, "A"):
			code = "A"
		case strings.HasPrefix(status, "D"):
			code = "D"
		case strings.HasPrefix(status, "R"):
			code = "R"
		}
		filesByPath[filepath.ToSlash(path)] = FileStat{Path: filepath.ToSlash(path), Status: code}
	}

	numstat, err := run(dir, "diff", "--no-ext-diff", "--numstat", base, "--")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(numstat), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		path := filepath.ToSlash(fields[2])
		file := filesByPath[path]
		file.Path = path
		file.Additions, _ = strconv.Atoi(fields[0])
		file.Deletions, _ = strconv.Atoi(fields[1])
		filesByPath[path] = file
	}

	// A worktree may contain new files that have not been committed yet. Add
	// them using the same ignore-aware status path used by HEAD-relative mode.
	working, err := WorktreeStatus(dir)
	if err != nil {
		return nil, err
	}
	for _, file := range working {
		if _, exists := filesByPath[file.Path]; !exists {
			filesByPath[file.Path] = file
		}
	}

	files := make([]FileStat, 0, len(filesByPath))
	for _, file := range filesByPath {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// WorktreeDiffAt returns a unified diff against a caller-selected ref.
func WorktreeDiffAt(dir, path string, contextLines int, base string) (string, error) {
	if contextLines < 0 || contextLines > 1000 {
		contextLines = 3
	}
	if base == "" {
		base = "HEAD"
	}
	text, err := run(dir, "diff", "--no-ext-diff", fmt.Sprintf("--unified=%d", contextLines), base, "--", path)
	if err == nil && text != "" {
		return text, nil
	}
	if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(path))); statErr == nil {
		untracked, noIndexErr := run(dir, "diff", "--no-ext-diff", "--no-index", fmt.Sprintf("--unified=%d", contextLines), "/dev/null", path)
		if noIndexErr == nil || untracked != "" {
			return untracked, nil
		}
	}
	if err != nil {
		return "", err
	}
	return text, nil
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
