package gitutil

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CommitInfo is a single commit on a branch relative to a base.
type CommitInfo struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	Time    time.Time `json:"time"`
}

// FileStat is a per-file diff summary (numstat).
type FileStat struct {
	Path      string `json:"path"`
	Status    string `json:"status"` // "A"|"M"|"D"|"R" (best-effort)
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// DefaultBranch detects the repo's default branch.
// Prefers origin/HEAD, then main, then master.
func DefaultBranch(projectDir string) (string, error) {
	out, err := run(projectDir, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		ref := strings.TrimSpace(out)
		const prefix = "refs/remotes/origin/"
		if strings.HasPrefix(ref, prefix) {
			b := strings.TrimPrefix(ref, prefix)
			if b != "" {
				return b, nil
			}
		}
	}
	for _, candidate := range []string{"main", "master"} {
		ok, err := BranchExists(projectDir, candidate)
		if err == nil && ok {
			return candidate, nil
		}
	}
	cur, err := CurrentBranch(projectDir)
	if err != nil {
		return "", fmt.Errorf("detect default branch: %w", err)
	}
	if cur == "" {
		return "", fmt.Errorf("detect default branch: detached HEAD and no main/master")
	}
	return cur, nil
}

// EnsureLocalBranch makes branch available as a local branch. Repositories
// cloned with only origin/<branch> still need a local target for merge-back.
func EnsureLocalBranch(projectDir, branch string) error {
	ok, err := BranchExists(projectDir, branch)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	remote := "origin/" + branch
	if _, err := run(projectDir, "rev-parse", "--verify", "refs/remotes/"+remote); err != nil {
		return fmt.Errorf("default branch %q is not available locally or as %s", branch, remote)
	}
	if _, err := run(projectDir, "branch", "--track", branch, remote); err != nil {
		return fmt.Errorf("create local default branch %q: %w", branch, err)
	}
	return nil
}

// MergeBase returns the merge-base of two refs.
func MergeBase(projectDir, a, b string) (string, error) {
	out, err := run(projectDir, "merge-base", a, b)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// BehindCount returns how many commits in upstream are not reachable from dir's HEAD.
// Equivalent to: git rev-list --count HEAD..upstream
func BehindCount(dir, upstream string) (int, error) {
	out, err := run(dir, "rev-list", "--count", "HEAD.."+upstream)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parse behind count: %w", err)
	}
	return n, nil
}

// CommitList returns commits reachable from tip but not from base (base..tip).
func CommitList(projectDir, base, tip string) ([]CommitInfo, error) {
	out, err := run(projectDir, "log", "--format=%H%x00%s%x00%an%x00%aI", base+".."+tip)
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	var commits []CommitInfo
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\x00")
		if len(parts) < 4 {
			continue
		}
		t, _ := time.Parse(time.RFC3339, parts[3])
		commits = append(commits, CommitInfo{
			SHA:     parts[0],
			Subject: parts[1],
			Author:  parts[2],
			Time:    t,
		})
	}
	return commits, nil
}

// DiffNumstat returns per-file addition/deletion counts for base..tip.
func DiffNumstat(projectDir, base, tip string) ([]FileStat, error) {
	// Disable rename detection so both commands have one NUL-delimited record
	// per path. This keeps names with whitespace or tabs intact.
	nameOut, err := run(projectDir, "diff", "--no-renames", "--name-status", "-z", base+".."+tip)
	if err != nil {
		return nil, err
	}
	statusByPath := map[string]string{}
	fields := strings.Split(nameOut, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		status := fields[i]
		path := fields[i+1]
		if len(status) > 0 {
			statusByPath[path] = string(status[0])
		}
	}

	out, err := run(projectDir, "diff", "--no-renames", "--numstat", "-z", base+".."+tip)
	if err != nil {
		return nil, err
	}
	var files []FileStat
	for _, record := range strings.Split(out, "\x00") {
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\t", 3)
		if len(fields) < 3 {
			continue
		}
		add, del := 0, 0
		if fields[0] != "-" {
			add, _ = strconv.Atoi(fields[0])
		}
		if fields[1] != "-" {
			del, _ = strconv.Atoi(fields[1])
		}
		path := fields[2]
		st := statusByPath[path]
		if st == "" {
			st = "M"
		}
		files = append(files, FileStat{
			Path:      path,
			Status:    st,
			Additions: add,
			Deletions: del,
		})
	}
	return files, nil
}

// FileDiff returns the unified diff for path between base and tip.
func FileDiff(projectDir, base, tip, path string, contextLines int) (string, error) {
	if contextLines <= 0 {
		contextLines = 3
	}
	out, err := run(projectDir, "diff",
		fmt.Sprintf("-U%d", contextLines),
		base+".."+tip, "--", path)
	if err != nil {
		return "", err
	}
	return out, nil
}

// DirtyPaths returns porcelain paths with uncommitted changes in dir.
func DirtyPaths(dir string) ([]string, error) {
	out, err := run(dir, "status", "--porcelain=v1", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	records := strings.Split(out, "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) < 3 {
			continue
		}
		status := record[:2]
		path := strings.TrimPrefix(record[2:], " ")
		if path != "" {
			paths = append(paths, path)
		}
		// With -z, rename/copy origins are emitted as a second path record.
		if strings.ContainsAny(status, "RC") && i+1 < len(records) {
			i++
			if origin := records[i]; origin != "" {
				paths = append(paths, origin)
			}
		}
	}
	return uniqueStrings(paths), nil
}

// MergeTreeConflicts runs git merge-tree to detect conflicts without touching the worktree.
// ours is the branch being merged into; theirs is the tip being merged.
// base is unused with modern merge-tree (it computes merge-bases itself) but kept for
// callers that already have it; classic fallback uses it.
func MergeTreeConflicts(projectDir, base, ours, theirs string) ([]string, error) {
	// Modern: git merge-tree --write-tree --name-only --no-messages <ours> <theirs>
	// Exit 0 = clean, 1 = conflicts. Stdout: tree-oid\n<path>...
	out, err := runAllowExit(projectDir, []int{0, 1},
		"merge-tree", "--write-tree", "--name-only", "--no-messages", ours, theirs)
	if err != nil {
		// Older git without --write-tree: fall back to classic merge-tree output parse.
		classic, cerr := run(projectDir, "merge-tree", base, ours, theirs)
		if cerr != nil {
			return nil, err
		}
		return parseClassicMergeTreeConflicts(classic), nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) <= 1 {
		return nil, nil
	}
	var conflicts []string
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		conflicts = append(conflicts, line)
	}
	return uniqueStrings(conflicts), nil
}

func parseClassicMergeTreeConflicts(out string) []string {
	var conflicts []string
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "changed in both") {
			// Next lines describe the file; path is on this line in some versions,
			// otherwise look for "<<<<<<<" blocks — take last field as best effort.
			fields := strings.Fields(trimmed)
			if len(fields) > 0 {
				conflicts = append(conflicts, fields[len(fields)-1])
			}
		}
		if strings.Contains(line, "CONFLICT") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				conflicts = append(conflicts, fields[len(fields)-1])
			}
		}
	}
	return uniqueStrings(conflicts)
}

// IsAncestor reports whether ancestor is an ancestor of tip.
func IsAncestor(projectDir, ancestor, tip string) (bool, error) {
	_, err := run(projectDir, "merge-base", "--is-ancestor", ancestor, tip)
	if err != nil {
		// non-zero exit means not ancestor
		return false, nil
	}
	return true, nil
}

// MergeFF fast-forwards baseBranch to tipBranch (must be ancestor).
func MergeFF(projectDir, baseBranch, tipBranch string) (string, error) {
	return runOnBaseBranch(projectDir, baseBranch, func() (string, error) {
		if _, err := run(projectDir, "merge", "--ff-only", tipBranch); err != nil {
			return "", err
		}
		return RevParse(projectDir, "HEAD")
	})
}

// MergeCommit creates a merge commit of tipBranch into baseBranch.
func MergeCommit(projectDir, baseBranch, tipBranch, message string) (string, error) {
	return runOnBaseBranch(projectDir, baseBranch, func() (string, error) {
		args := []string{"merge", "--no-ff", tipBranch}
		if message != "" {
			args = append(args, "-m", message)
		}
		if _, err := run(projectDir, args...); err != nil {
			_, _ = run(projectDir, "merge", "--abort")
			return "", err
		}
		return RevParse(projectDir, "HEAD")
	})
}

// MergeSquash squash-merges tipBranch into baseBranch and commits.
func MergeSquash(projectDir, baseBranch, tipBranch, message string) (string, error) {
	return runOnBaseBranch(projectDir, baseBranch, func() (string, error) {
		if _, err := run(projectDir, "merge", "--squash", tipBranch); err != nil {
			_, _ = run(projectDir, "reset", "--merge")
			return "", err
		}
		if message == "" {
			message = "Squash merge " + tipBranch
		}
		if _, err := run(projectDir, "commit", "-m", message); err != nil {
			// Undo the squash index without discarding independently modified files.
			_, _ = run(projectDir, "reset", "--merge", "HEAD")
			return "", err
		}
		return RevParse(projectDir, "HEAD")
	})
}

// runOnBaseBranch checks out baseBranch for a merge and restores the branch the
// user had checked out afterward. The caller must ensure the worktree is clean.
func runOnBaseBranch(projectDir, baseBranch string, fn func() (string, error)) (sha string, err error) {
	current, err := CurrentBranch(projectDir)
	if err != nil {
		return "", err
	}
	if current == "" {
		return "", fmt.Errorf("origin checkout is detached; check out %q before merging", baseBranch)
	}
	if current != baseBranch {
		if _, err := run(projectDir, "checkout", baseBranch); err != nil {
			return "", err
		}
		defer func() {
			if _, restoreErr := run(projectDir, "checkout", current); restoreErr != nil {
				if err == nil {
					sha = ""
					err = fmt.Errorf("merge succeeded but restore origin branch %q: %w", current, restoreErr)
				}
			}
		}()
	}
	return fn()
}

// CommitAll stages all changes in dir and commits with message.
func CommitAll(dir, message string) (string, error) {
	if message == "" {
		return "", fmt.Errorf("commit message is required")
	}
	if _, err := run(dir, "add", "-A"); err != nil {
		return "", err
	}
	dirty, err := IsDirty(dir)
	if err != nil {
		return "", err
	}
	if !dirty {
		// Nothing staged after add — check if there was anything to commit.
		// git add -A on clean tree leaves index clean; treat as no-op error.
		return "", fmt.Errorf("nothing to commit")
	}
	if _, err := run(dir, "commit", "-m", message); err != nil {
		return "", err
	}
	return RevParse(dir, "HEAD")
}

// AbortMerge aborts an in-progress merge.
func AbortMerge(projectDir string) {
	_, _ = run(projectDir, "merge", "--abort")
}

func runAllowExit(dir string, allowed []int, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code := ee.ExitCode()
		for _, a := range allowed {
			if code == a {
				return string(out), nil
			}
		}
	}
	return string(out), fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, string(out))
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
