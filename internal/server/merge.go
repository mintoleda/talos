package server

import (
	"fmt"
	"strings"

	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/gitutil"
	"github.com/mintoleda/talos/internal/protocol"
)

// MergePreview builds a review summary for merging a worktree session into its origin.
func (m *SessionManager) MergePreview(params rpc.MergePreviewParams) (rpc.MergePreviewResult, error) {
	meta, branch, projectDir, wtDir, err := m.mergeSession(params.ID)
	if err != nil {
		return rpc.MergePreviewResult{}, err
	}
	base, err := m.resolveBase(meta, params.Base)
	if err != nil {
		return rpc.MergePreviewResult{}, err
	}

	ahead, err := gitutil.AheadCount(wtDir, base)
	if err != nil {
		return rpc.MergePreviewResult{}, fmt.Errorf("count commits ahead: %w", err)
	}
	behind, err := gitutil.BehindCount(wtDir, base)
	if err != nil {
		return rpc.MergePreviewResult{}, fmt.Errorf("count commits behind: %w", err)
	}
	dirtyWT, err := gitutil.IsDirty(wtDir)
	if err != nil {
		return rpc.MergePreviewResult{}, fmt.Errorf("check worktree status: %w", err)
	}
	dirtyMain, err := gitutil.IsDirty(projectDir)
	if err != nil {
		return rpc.MergePreviewResult{}, fmt.Errorf("check origin status: %w", err)
	}

	commits, err := gitutil.CommitList(projectDir, base, branch)
	if err != nil {
		return rpc.MergePreviewResult{}, fmt.Errorf("list commits: %w", err)
	}
	files, err := gitutil.DiffNumstat(projectDir, base, branch)
	if err != nil {
		return rpc.MergePreviewResult{}, fmt.Errorf("diff numstat: %w", err)
	}

	var dirtyHits []string
	if dirtyMain {
		mainPaths, err := gitutil.DirtyPaths(projectDir)
		if err != nil {
			return rpc.MergePreviewResult{}, fmt.Errorf("list origin changes: %w", err)
		}
		fileSet := map[string]bool{}
		for _, f := range files {
			fileSet[f.Path] = true
		}
		for _, p := range mainPaths {
			if fileSet[p] {
				dirtyHits = append(dirtyHits, p)
			}
		}
	}

	canFF, err := gitutil.IsAncestor(projectDir, base, branch)
	if err != nil {
		return rpc.MergePreviewResult{}, fmt.Errorf("check fast-forward: %w", err)
	}

	state := "unloaded"
	m.mu.Lock()
	if eng, ok := m.engines[meta.ID]; ok {
		if s := m.states[meta.ID]; s != "" {
			state = s
		} else if eng.Busy() {
			state = "busy"
		} else {
			state = "idle"
		}
	} else if meta.Merged {
		state = "merged"
	}
	m.mu.Unlock()

	outCommits := make([]rpc.MergeCommitInfo, 0, len(commits))
	for _, c := range commits {
		outCommits = append(outCommits, rpc.MergeCommitInfo{
			SHA: c.SHA, Subject: c.Subject, Author: c.Author, Time: c.Time,
		})
	}
	outFiles := make([]rpc.MergeFileStat, 0, len(files))
	for _, f := range files {
		outFiles = append(outFiles, rpc.MergeFileStat{
			Path: f.Path, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions,
		})
	}

	return rpc.MergePreviewResult{
		Base:          base,
		Branch:        branch,
		Ahead:         ahead,
		Behind:        behind,
		DirtyWorktree: dirtyWT,
		DirtyMain:     dirtyMain,
		DirtyMainHit:  dirtyHits,
		Commits:       outCommits,
		Files:         outFiles,
		CanFF:         canFF,
		SessionState:  state,
	}, nil
}

// MergeFileDiff returns the unified diff for one path in the session branch.
func (m *SessionManager) MergeFileDiff(params rpc.MergeFileDiffParams) (rpc.MergeFileDiffResult, error) {
	if params.Path == "" {
		return rpc.MergeFileDiffResult{}, fmt.Errorf("path is required")
	}
	meta, branch, projectDir, _, err := m.mergeSession(params.ID)
	if err != nil {
		return rpc.MergeFileDiffResult{}, err
	}
	base, err := m.resolveBase(meta, params.Base)
	if err != nil {
		return rpc.MergeFileDiffResult{}, err
	}
	unified, err := gitutil.FileDiff(projectDir, base, branch, params.Path, params.Context)
	if err != nil {
		return rpc.MergeFileDiffResult{}, err
	}
	return rpc.MergeFileDiffResult{Unified: unified}, nil
}

// MergeCommitWorktree commits all uncommitted changes in the session worktree.
func (m *SessionManager) MergeCommitWorktree(params rpc.MergeCommitWorktreeParams) (rpc.MergeCommitWorktreeResult, error) {
	if params.Message == "" {
		return rpc.MergeCommitWorktreeResult{}, fmt.Errorf("message is required")
	}
	_, _, projectDir, _, err := m.mergeSession(params.ID)
	if err != nil {
		return rpc.MergeCommitWorktreeResult{}, err
	}
	unlock := m.lockProject(projectDir)
	defer unlock()
	_, _, _, wtDir, err := m.mergeSession(params.ID)
	if err != nil {
		return rpc.MergeCommitWorktreeResult{}, err
	}
	m.mu.Lock()
	eng, live := m.engines[params.ID]
	busy := live && eng.Busy()
	m.mu.Unlock()
	if busy {
		return rpc.MergeCommitWorktreeResult{}, fmt.Errorf("session is busy; wait until idle")
	}
	sha, err := gitutil.CommitAll(wtDir, params.Message)
	if err != nil {
		return rpc.MergeCommitWorktreeResult{}, err
	}
	m.invalidateGitCache(params.ID)
	return rpc.MergeCommitWorktreeResult{SHA: sha}, nil
}

// MergeExecute merges the session branch into the origin default branch.
func (m *SessionManager) MergeExecute(params rpc.MergeExecuteParams) (rpc.MergeExecuteResult, error) {
	strategy := params.Strategy
	if strategy == "" {
		strategy = "squash"
	}
	switch strategy {
	case "squash", "merge", "ff":
	default:
		return rpc.MergeExecuteResult{}, fmt.Errorf("unknown strategy %q", strategy)
	}

	_, _, projectDir, _, err := m.mergeSession(params.ID)
	if err != nil {
		return rpc.MergeExecuteResult{}, err
	}
	unlock := m.lockProject(projectDir)
	defer unlock()
	meta, branch, projectDir, wtDir, err := m.mergeSession(params.ID)
	if err != nil {
		return rpc.MergeExecuteResult{}, err
	}
	base, err := m.resolveBase(meta, params.Base)
	if err != nil {
		return rpc.MergeExecuteResult{}, err
	}

	m.mu.Lock()
	eng, live := m.engines[params.ID]
	busy := live && eng.Busy()
	m.mu.Unlock()
	if busy {
		return rpc.MergeExecuteResult{}, fmt.Errorf("session is busy; wait until idle")
	}

	dirtyWT, err := gitutil.IsDirty(wtDir)
	if err != nil {
		return rpc.MergeExecuteResult{}, fmt.Errorf("check worktree status: %w", err)
	}
	if dirtyWT {
		return rpc.MergeExecuteResult{}, fmt.Errorf("worktree has uncommitted changes; commit them first")
	}

	dirtyMain, err := gitutil.IsDirty(projectDir)
	if err != nil {
		return rpc.MergeExecuteResult{}, fmt.Errorf("check origin status: %w", err)
	}
	if dirtyMain {
		return rpc.MergeExecuteResult{}, fmt.Errorf(
			"origin checkout has uncommitted changes; commit, stash, or discard them before merging")
	}
	branchTip, err := gitutil.RevParse(projectDir, branch)
	if err != nil {
		return rpc.MergeExecuteResult{}, fmt.Errorf("resolve session branch: %w", err)
	}

	mergeBase, err := gitutil.MergeBase(projectDir, base, branch)
	if err != nil {
		return rpc.MergeExecuteResult{}, fmt.Errorf("merge-base: %w", err)
	}
	conflicts, err := gitutil.MergeTreeConflicts(projectDir, mergeBase, base, branch)
	if err != nil {
		return rpc.MergeExecuteResult{}, fmt.Errorf("merge-tree: %w", err)
	}
	if len(conflicts) > 0 {
		return rpc.MergeExecuteResult{
			Merged:        false,
			Conflict:      true,
			ConflictFiles: conflicts,
		}, nil
	}

	if strategy == "ff" {
		ok, _ := gitutil.IsAncestor(projectDir, base, branch)
		if !ok {
			return rpc.MergeExecuteResult{}, fmt.Errorf("fast-forward not possible; choose squash or merge")
		}
	}

	msg := params.Message
	if msg == "" {
		msg = defaultMergeMessage(meta, branch)
	}

	var sha string
	switch strategy {
	case "ff":
		sha, err = gitutil.MergeFF(projectDir, base, branch)
	case "merge":
		sha, err = gitutil.MergeCommit(projectDir, base, branch, msg)
	case "squash":
		sha, err = gitutil.MergeSquash(projectDir, base, branch, msg)
	}
	if err != nil {
		gitutil.AbortMerge(projectDir)
		return rpc.MergeExecuteResult{}, fmt.Errorf("merge failed: %w", err)
	}

	result := rpc.MergeExecuteResult{Merged: true, SHA: sha}

	if params.Cleanup {
		if err := m.cleanupAfterMerge(meta, branch, branchTip, strategy); err != nil {
			return result, fmt.Errorf("merged at %s but cleanup failed: %w", sha, err)
		}
	}
	m.invalidateGitCache(params.ID)
	return result, nil
}

func (m *SessionManager) cleanupAfterMerge(meta SessionMeta, branch, expectedTip, strategy string) error {
	m.mu.Lock()
	_, live := m.engines[meta.ID]
	m.mu.Unlock()
	if live {
		if err := m.Stop(meta.ID); err != nil {
			return err
		}
	}

	currentTip, err := gitutil.RevParse(meta.ProjectDir, branch)
	if err != nil {
		return fmt.Errorf("resolve session branch for cleanup: %w", err)
	}
	if currentTip != expectedTip {
		return fmt.Errorf("session branch changed during merge; refusing cleanup")
	}
	worktreeDirty, err := gitutil.IsDirty(meta.Dir)
	if err != nil {
		return fmt.Errorf("check worktree before cleanup: %w", err)
	}
	if worktreeDirty {
		return fmt.Errorf("worktree changed during merge; refusing cleanup")
	}

	if meta.Dir != "" && meta.ProjectDir != "" {
		if err := gitutil.WorktreeRemoveClean(meta.ProjectDir, meta.Dir); err != nil {
			return fmt.Errorf("remove worktree: %w", err)
		}
	}

	if strategy == "squash" {
		if err := gitutil.BranchForceDelete(meta.ProjectDir, branch); err != nil {
			return fmt.Errorf("delete squash branch: %w", err)
		}
	} else if err := gitutil.BranchDelete(meta.ProjectDir, branch); err != nil {
		return fmt.Errorf("delete merged branch: %w", err)
	}

	meta.Merged = true
	meta.Dir = ""
	if err := WriteSessionMeta(meta); err != nil {
		return err
	}
	m.emitStatus(protocol.SessionStatus{ID: meta.ID, State: "merged", Dir: meta.ProjectDir})
	return nil
}

func (m *SessionManager) mergeSession(id string) (SessionMeta, string, string, string, error) {
	if id == "" {
		return SessionMeta{}, "", "", "", fmt.Errorf("id is required")
	}
	meta, err := FindSessionMeta(id)
	if err != nil {
		return SessionMeta{}, "", "", "", err
	}
	if meta.Merged {
		return SessionMeta{}, "", "", "", fmt.Errorf("session already merged")
	}
	if meta.Isolation != "worktree" {
		return SessionMeta{}, "", "", "", fmt.Errorf("session is not worktree-isolated")
	}
	if meta.ProjectDir == "" || meta.Dir == "" {
		return SessionMeta{}, "", "", "", fmt.Errorf("session missing project/worktree paths")
	}
	branch := meta.Branch
	if branch == "" {
		branch = "talos/" + id
	}
	ok, err := gitutil.BranchExists(meta.ProjectDir, branch)
	if err != nil || !ok {
		return SessionMeta{}, "", "", "", fmt.Errorf("branch %s not found", branch)
	}
	return meta, branch, meta.ProjectDir, meta.Dir, nil
}

func (m *SessionManager) resolveBase(meta SessionMeta, override string) (string, error) {
	base := override
	if base == "" {
		base = meta.DefaultBranch
	}
	if base == "" {
		var err error
		base, err = gitutil.DefaultBranch(meta.ProjectDir)
		if err != nil {
			return "", err
		}
		meta.DefaultBranch = base
		if err := WriteSessionMeta(meta); err != nil {
			return "", fmt.Errorf("cache default branch: %w", err)
		}
	}
	if err := gitutil.EnsureLocalBranch(meta.ProjectDir, base); err != nil {
		return "", err
	}
	return base, nil
}

func (m *SessionManager) invalidateGitCache(id string) {
	m.mu.Lock()
	delete(m.gitCache, id)
	m.mu.Unlock()
}

func defaultMergeMessage(meta SessionMeta, branch string) string {
	preview := sessionPreview(meta)
	if preview != "" {
		if i := strings.IndexByte(preview, '\n'); i >= 0 {
			preview = preview[:i]
		}
		if len(preview) > 72 {
			preview = preview[:72]
		}
		return preview
	}
	return "Merge " + branch
}
