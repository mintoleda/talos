package safety

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Checkpointer struct {
	repo      string
	sessionID string
}

// NewCheckpointer creates a checkpointer for repo, namespaced by sessionID.
// Refs are written as refs/checkpoints/<sessionID>/<ts>. When sessionID is
// empty, refs use the legacy refs/checkpoints/<ts> form for back-compat.
func NewCheckpointer(repo, sessionID string) *Checkpointer {
	return &Checkpointer{repo: repo, sessionID: sessionID}
}

func (c *Checkpointer) refPrefix() string {
	if c.sessionID == "" {
		return "refs/checkpoints/"
	}
	return "refs/checkpoints/" + c.sessionID + "/"
}

func (c *Checkpointer) Snapshot(label string) (string, error) {
	tmpIndex, err := os.CreateTemp("", "talos-idx-*")
	if err != nil {
		return "", err
	}
	tmpIndex.Close()
	defer os.Remove(tmpIndex.Name())

	env := append(os.Environ(), "GIT_INDEX_FILE="+tmpIndex.Name())
	git := func(args ...string) (string, error) { return runGit(c.repo, env, args...) }

	head, headErr := git("rev-parse", "HEAD")
	if headErr == nil {
		if _, err = git("read-tree", strings.TrimSpace(head)); err != nil {
			return "", err
		}
	}
	if _, err = git("add", "-A"); err != nil {
		return "", err
	}
	tree, err := git("write-tree")
	if err != nil {
		return "", err
	}
	tree = strings.TrimSpace(tree)

	commitArgs := []string{"commit-tree", tree, "-m", "checkpoint: " + label}
	if headErr == nil {
		commitArgs = append(commitArgs, "-p", strings.TrimSpace(head))
	}
	commit, err := git(commitArgs...)
	if err != nil {
		return "", err
	}
	commit = strings.TrimSpace(commit)

	ref := c.refPrefix() + time.Now().UTC().Format("20060102T150405Z")
	if _, err = git("update-ref", ref, commit); err != nil {
		return "", err
	}
	return ref, nil
}

func (c *Checkpointer) Restore(ref string) error {
	_, err := runGit(c.repo, os.Environ(), "restore", "--source="+ref, "--worktree", ".")
	return err
}

func (c *Checkpointer) List() ([]string, error) {
	out, err := runGit(c.repo, os.Environ(), "for-each-ref", "--format=%(refname)", c.refPrefix())
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var refs []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// When sessionID is empty, exclude namespaced refs (sessionID/ts).
		if c.sessionID == "" {
			rest := strings.TrimPrefix(line, "refs/checkpoints/")
			if strings.Contains(rest, "/") {
				continue
			}
		}
		refs = append(refs, line)
	}
	return refs, nil
}

func runGit(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}
