package client

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mintoleda/talos/internal/protocol"
)

var imageExts = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

func ListFiles(root, prefix string) ([]string, error) {
	entries := collectFiles(root, 6)
	if prefix != "" {
		prefix = strings.ToLower(prefix)
		filtered := entries[:0]
		for _, e := range entries {
			if strings.Contains(strings.ToLower(e), prefix) {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	if len(entries) > 20 {
		entries = entries[:20]
	}
	return entries, nil
}

func collectFiles(root string, maxDepth int) []string {
	var entries []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		depth := strings.Count(rel, string(filepath.Separator))
		if depth > maxDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if shouldSkipFileDir(filepath.Base(path)) {
				return filepath.SkipDir
			}
			entries = append(entries, rel+"/")
			return nil
		}
		entries = append(entries, rel)
		return nil
	})
	sort.Slice(entries, func(i, j int) bool {
		di := strings.Count(entries[i], string(filepath.Separator))
		dj := strings.Count(entries[j], string(filepath.Separator))
		if di != dj {
			return di < dj
		}
		return entries[i] < entries[j]
	})
	return entries
}

func shouldSkipFileDir(name string) bool {
	switch name {
	case ".git", ".svn", ".hg", "node_modules", "vendor", "dist", "build", "out", "target", ".cache", ".talos":
		return true
	}
	return strings.HasPrefix(name, ".")
}

func ResolveInput(root, text string) ([]protocol.ContentBlock, string, error) {
	var blocks []protocol.ContentBlock
	words := strings.Fields(text)
	var remaining []string

	for _, w := range words {
		if !strings.HasPrefix(w, "@") {
			remaining = append(remaining, w)
			continue
		}
		path := strings.TrimPrefix(w, "@")
		ext := strings.ToLower(filepath.Ext(path))
		mime, ok := imageExts[ext]
		if !ok {
			remaining = append(remaining, w)
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			remaining = append(remaining, w)
			continue
		}
		blocks = append(blocks, protocol.ContentBlock{
			Type:  protocol.BlockImage,
			Image: &protocol.ImageBlock{MediaType: mime, Data: base64.StdEncoding.EncodeToString(data)},
		})
	}

	plainText := strings.Join(remaining, " ")
	if plainText != "" {
		blocks = append([]protocol.ContentBlock{{Type: protocol.BlockText, Text: plainText}}, blocks...)
	}
	return blocks, text, nil
}

func PushInstruction(root string) (msg string, notice string, err error) {
	dirName := filepath.Base(root)
	run := func(name string, args ...string) ([]byte, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir = root
		return cmd.Output()
	}

	isGit := false
	if out, err := run("git", "rev-parse", "--is-inside-work-tree"); err == nil {
		isGit = strings.TrimSpace(string(out)) == "true"
	}

	if !isGit {
		ghUser := ""
		if out, err := run("gh", "api", "user", "--jq", ".login"); err == nil {
			ghUser = strings.TrimSpace(string(out))
		}
		userClause := ""
		if ghUser != "" {
			userClause = " My GitHub username is " + ghUser + "."
		}
		return fmt.Sprintf(
			"Initialize a new git repository named %q in the current directory, add all current files as the initial commit, and create a corresponding public repository on GitHub using 'gh repo create %q --public --source=. --remote=origin --push'.%s",
			dirName, dirName, userClause,
		), "", nil
	}

	statusOut, err := run("git", "status", "--porcelain")
	if err != nil {
		return "", "", fmt.Errorf("push: git status: %w", err)
	}
	changes := strings.TrimSpace(string(statusOut))

	branch := ""
	if out, err := run("git", "branch", "--show-current"); err == nil {
		branch = strings.TrimSpace(string(out))
	}

	head := ""
	if out, err := run("git", "rev-parse", "HEAD"); err == nil {
		head = strings.TrimSpace(string(out))
	}

	unpushed := 0
	if out, err := run("git", "rev-list", "--count", "@{u}..HEAD"); err == nil {
		unpushed, _ = strconv.Atoi(strings.TrimSpace(string(out)))
	}

	if changes == "" {
		if unpushed > 0 {
			return fmt.Sprintf(
				"There were %d commit(s) already waiting to be pushed when /push was called. Push those existing commits to the remote repository. The branch is %q and HEAD is %s.",
				unpushed, branch, head,
			), "", nil
		}
		return "", "push: no changes or unpushed commits", nil
	}

	return fmt.Sprintf(
		"I want to handle all current changes and any commits that were already waiting to be pushed when /push was called. Please follow these rules strictly:\n"+
			"1. Ensure .gitignore exists before committing.\n"+
			"2. Examine all changed files (including untracked ones) using git status and git diff.\n"+
			"3. Group files that were changed for the same reason.\n"+
			"4. If changes perform different functions, split them into multiple commits. DO NOT commit 10+ files in one commit if they were changed for different reasons.\n"+
			"5. For each commit, use the format: 'type(abc): message', where:\n"+
			"   - 'type' is one of: feat, fix, chore, refactor, docs, style, test, perf.\n"+
			"   - 'abc' is a one-word representation of what was touched (e.g., 'api', 'ui', 'config', 'cli').\n"+
			"6. There were %d commit(s) already waiting to be pushed when /push was called. Push those pre-existing commits if this count is greater than zero. To avoid accidentally pushing newly-created commits, push before creating new commits or push only up to the command-start HEAD (%s) on branch %q.\n"+
			"7. After creating commits for the current changes, decide whether those newly-created commits should also be pushed. Do not automatically push them unless you determine it is appropriate.\n\n"+
			"Current changes reported by git:\n%s\n\n"+
			"Please analyze the diffs, perform the commits, and push only the commits that should be pushed under the rules above.",
		unpushed, head, branch, changes,
	), "", nil
}
