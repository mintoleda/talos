package server

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/gitutil"
)

// listProjects derives the project rail from persisted session metadata and
// currently loaded sessions. Talos remains the source of truth; the GUI only
// receives a compact projection.
func (d *Daemon) listProjects() (rpc.ListProjectsResult, error) {
	projects := make(map[string]rpc.ProjectInfo)
	add := func(dir string) {
		dir = filepath.Clean(dir)
		if dir == "." || dir == "" {
			return
		}
		if _, ok := projects[dir]; ok {
			return
		}
		last, _ := gitutil.LastCommitAt(dir)
		branch, _ := gitutil.DefaultBranch(dir)
		projects[dir] = rpc.ProjectInfo{
			ProjectDir:    dir,
			Name:          filepath.Base(dir),
			LastCommitAt:  last,
			DefaultBranch: branch,
		}
	}

	metas, err := ListAllSessionMetas()
	if err != nil {
		return rpc.ListProjectsResult{}, err
	}
	sessions := d.manager.List()
	for _, meta := range metas {
		add(meta.ProjectDir)
	}
	for _, session := range sessions {
		add(session.ProjectDir)
	}
	// Count the same live ∪ persisted session view returned by the daemon
	// session inventory so the project rail does not drift when a session is
	// loaded or when its sidecar has not been flushed yet.
	for _, session := range sessions {
		key := filepath.Clean(session.ProjectDir)
		project, ok := projects[key]
		if ok {
			project.SessionCount++
			projects[key] = project
		}
	}

	out := make([]rpc.ProjectInfo, 0, len(projects))
	for _, project := range projects {
		out = append(out, project)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastCommitAt.Equal(out[j].LastCommitAt) {
			return out[i].Name < out[j].Name
		}
		return out[i].LastCommitAt.After(out[j].LastCommitAt)
	})
	return rpc.ListProjectsResult{Projects: out}, nil
}

func sessionWorktree(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("sessionId is required")
	}
	meta, err := FindSessionMeta(id)
	if err != nil {
		return "", err
	}
	if meta.Dir == "" {
		return "", fmt.Errorf("session %s has no working directory", id)
	}
	return filepath.Abs(meta.Dir)
}

func (d *Daemon) gitStatus(params rpc.GitStatusParams) (rpc.GitStatusResult, error) {
	meta, err := FindSessionMeta(params.SessionID)
	if err != nil {
		return rpc.GitStatusResult{}, err
	}
	dir, err := sessionWorktree(params.SessionID)
	if err != nil {
		return rpc.GitStatusResult{}, err
	}
	base := "HEAD"
	if meta.Isolation == "worktree" && meta.DefaultBranch != "" {
		base = meta.DefaultBranch
	}
	base, err = gitutil.RevParse(dir, base)
	if err != nil {
		return rpc.GitStatusResult{}, err
	}
	statusBase := "HEAD"
	if meta.Isolation == "worktree" && meta.DefaultBranch != "" {
		statusBase = meta.DefaultBranch
	}
	files, err := gitutil.WorktreeStatusAt(dir, statusBase)
	if err != nil {
		return rpc.GitStatusResult{}, err
	}
	result := make([]rpc.MergeFileStat, 0, len(files))
	for _, file := range files {
		result = append(result, rpc.MergeFileStat{
			Path: file.Path, Status: file.Status,
			Additions: file.Additions, Deletions: file.Deletions,
		})
	}
	return rpc.GitStatusResult{Files: result, Base: base}, nil
}

func (d *Daemon) gitDiff(params rpc.GitDiffParams) (rpc.GitDiffResult, error) {
	meta, err := FindSessionMeta(params.SessionID)
	if err != nil {
		return rpc.GitDiffResult{}, err
	}
	dir, err := sessionWorktree(params.SessionID)
	if err != nil {
		return rpc.GitDiffResult{}, err
	}
	path := filepath.Clean(params.Path)
	if path == "." || filepath.IsAbs(params.Path) || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return rpc.GitDiffResult{}, fmt.Errorf("path must be relative to the session worktree")
	}
	if params.Base != "" && (strings.HasPrefix(params.Base, "-") || strings.ContainsAny(params.Base, " \t\n\r\x00")) {
		return rpc.GitDiffResult{}, fmt.Errorf("invalid base")
	}
	base := params.Base
	if base == "" && meta.Isolation == "worktree" && meta.DefaultBranch != "" {
		base = meta.DefaultBranch
	}
	text, err := gitutil.WorktreeDiffAt(dir, path, params.Context, base)
	if err != nil {
		return rpc.GitDiffResult{}, err
	}
	return rpc.GitDiffResult{Unified: text}, nil
}

const maxTreeEntries = 10000

func fsTree(params rpc.FSTreeParams) (rpc.FSTreeResult, error) {
	if params.Dir == "" {
		return rpc.FSTreeResult{}, fmt.Errorf("dir is required")
	}
	dir, err := filepath.Abs(params.Dir)
	if err != nil {
		return rpc.FSTreeResult{}, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return rpc.FSTreeResult{}, err
	}
	if !info.IsDir() {
		return rpc.FSTreeResult{}, fmt.Errorf("not a directory: %s", dir)
	}
	depth := params.Depth
	if depth <= 0 || depth > 32 {
		depth = 8
	}
	files, err := gitutil.VisibleFiles(dir)
	if err == nil {
		return fsGitTree(dir, files, depth), nil
	}

	entries := make([]rpc.FsEntry, 0, 128)
	err = filepath.WalkDir(dir, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == dir {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if item.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		level := strings.Count(rel, string(filepath.Separator)) + 1
		if level > depth {
			if item.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if len(entries) >= maxTreeEntries {
			return filepath.SkipDir
		}
		entry := rpc.FsEntry{Path: filepath.ToSlash(rel), Name: item.Name(), IsDir: item.IsDir()}
		if !entry.IsDir {
			if stat, statErr := item.Info(); statErr == nil {
				entry.Size = stat.Size()
			}
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return rpc.FSTreeResult{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return rpc.FSTreeResult{Entries: entries}, nil
}

func fsGitTree(dir string, files []string, depth int) rpc.FSTreeResult {
	seen := make(map[string]bool)
	entries := make([]rpc.FsEntry, 0, len(files))
	for _, file := range files {
		parts := strings.Split(filepath.ToSlash(file), "/")
		if len(parts) > depth {
			parts = parts[:depth]
		}
		fullParts := strings.Split(filepath.ToSlash(file), "/")
		for i := 1; i <= len(parts); i++ {
			rel := strings.Join(parts[:i], "/")
			if seen[rel] {
				continue
			}
			seen[rel] = true
			isDir := i < len(fullParts)
			entry := rpc.FsEntry{Path: rel, Name: parts[i-1], IsDir: isDir}
			if !isDir {
				if stat, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); statErr == nil {
					entry.Size = stat.Size()
				}
			}
			entries = append(entries, entry)
			if len(entries) >= maxTreeEntries {
				break
			}
		}
		if len(entries) >= maxTreeEntries {
			break
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return rpc.FSTreeResult{Entries: entries}
}

const maxReadBytes = 1 << 20

func fsRead(params rpc.FSReadParams) (rpc.FSReadResult, error) {
	if params.Path == "" {
		return rpc.FSReadResult{}, fmt.Errorf("path is required")
	}
	file, err := os.Open(params.Path)
	if err != nil {
		return rpc.FSReadResult{}, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxReadBytes+1))
	if err != nil {
		return rpc.FSReadResult{}, err
	}
	limit := len(content)
	truncated := false
	if limit > maxReadBytes {
		limit = maxReadBytes
		truncated = true
	}
	return rpc.FSReadResult{Content: string(content[:limit]), Truncated: truncated}, nil
}
