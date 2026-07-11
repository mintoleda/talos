package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/engine"
	"github.com/mintoleda/talos/internal/gitutil"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/session"
)

// SessionMeta lives in internal/session so the engine (TUI path) can write
// the same sidecar the daemon does; aliased here for existing callers.
type SessionMeta = session.SessionMeta

var (
	MetaPath            = session.MetaPath
	WriteSessionMeta    = session.WriteSessionMeta
	ReadSessionMeta     = session.ReadSessionMeta
	FindSessionMeta     = session.FindSessionMeta
	ListAllSessionMetas = session.ListAllSessionMetas
	TouchSessionMeta    = session.TouchSessionMeta
)

type gitEnrichment struct {
	ahead int
	dirty bool
	at    time.Time
}

// SessionManager owns live engines and persisted session metadata.
type SessionManager struct {
	mu           sync.Mutex
	mergeMu      sync.Mutex
	projectLocks map[string]*sync.Mutex
	cfg          *config.Config
	engines      map[string]*engine.Engine
	states       map[string]string // last known SessionStatus state for live engines
	statusFn     func(protocol.SessionStatus)
	buildFn      func(ctx context.Context, o engine.BuildOpts) (*engine.Built, error)
	newEng       func(b *engine.Built, ctx context.Context) *engine.Engine
	gitCache     map[string]gitEnrichment
}

// NewSessionManager creates an empty manager. Engines are created on demand.
func NewSessionManager(cfg *config.Config) *SessionManager {
	return &SessionManager{
		cfg:          cfg,
		engines:      make(map[string]*engine.Engine),
		states:       make(map[string]string),
		gitCache:     make(map[string]gitEnrichment),
		projectLocks: make(map[string]*sync.Mutex),
		buildFn:      engine.Build,
		newEng: func(b *engine.Built, ctx context.Context) *engine.Engine {
			return b.NewEngine(ctx)
		},
	}
}

// SetStatusFn registers a daemon-wide SessionStatus broadcast hook.
func (m *SessionManager) SetStatusFn(fn func(protocol.SessionStatus)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statusFn = fn
}

// Create builds a new (or resumed-via-params) session engine and registers it.
func (m *SessionManager) Create(ctx context.Context, params rpc.CreateSessionParams) (rpc.SessionInfo, error) {
	if params.Dir == "" {
		return rpc.SessionInfo{}, fmt.Errorf("dir is required")
	}
	if params.Resume != "" {
		if _, err := FindSessionMeta(params.Resume); err == nil {
			eng, err := m.Resume(ctx, params.Resume)
			if err != nil {
				return rpc.SessionInfo{}, err
			}
			meta, _ := FindSessionMeta(params.Resume)
			return m.sessionInfo(eng, meta, true, "idle"), nil
		}
	}

	dir, err := filepath.Abs(params.Dir)
	if err != nil {
		return rpc.SessionInfo{}, fmt.Errorf("resolve dir: %w", err)
	}

	isolation := params.Isolation
	if isolation == "" {
		isolation = "worktree"
	}

	projectDir := dir
	dirtyMain := false
	if isolation == "worktree" {
		if !gitutil.IsRepo(dir) {
			isolation = "none"
			projectDir = dir
		} else {
			root, err := gitutil.RepoRoot(dir)
			if err != nil {
				isolation = "none"
				projectDir = dir
			} else {
				projectDir = root
				dirtyMain, _ = gitutil.IsDirty(projectDir)
			}
		}
	} else {
		projectDir = dir
	}

	var sessionID string
	if params.Resume != "" {
		sessionID = params.Resume
	} else {
		sessionID = session.NewSession(projectDir).ID
	}

	wtDir := dir
	branch := ""
	defaultBranch := ""
	if isolation == "worktree" {
		unlock := m.lockProject(projectDir)
		defer unlock()
		defaultBranch, err = gitutil.DefaultBranch(projectDir)
		if err != nil {
			return rpc.SessionInfo{}, fmt.Errorf("detect default branch: %w", err)
		}
		if err := gitutil.EnsureLocalBranch(projectDir, defaultBranch); err != nil {
			return rpc.SessionInfo{}, err
		}
		wtDir = filepath.Join(m.cfg.BaseDir, "worktrees", session.ProjectHash(projectDir), sessionID)
		branch = "talos/" + sessionID
		if err := os.MkdirAll(filepath.Dir(wtDir), 0o755); err != nil {
			return rpc.SessionInfo{}, fmt.Errorf("worktree parent: %w", err)
		}
		if err := gitutil.WorktreeAddAtRef(projectDir, wtDir, branch, defaultBranch); err != nil {
			return rpc.SessionInfo{}, fmt.Errorf("worktree add: %w", err)
		}
	}

	m.mu.Lock()
	buildFn := m.buildFn
	newEng := m.newEng
	cfg := m.cfg
	m.mu.Unlock()

	built, err := buildFn(ctx, engine.BuildOpts{
		Cfg:        cfg,
		Dir:        wtDir,
		ProjectDir: projectDir,
		SessionID:  sessionID,
		Provider:   params.Provider,
		Model:      params.Model,
	})
	if err != nil {
		if isolation == "worktree" {
			_ = gitutil.WorktreeRemove(projectDir, wtDir)
			_ = gitutil.BranchForceDelete(projectDir, branch)
		}
		return rpc.SessionInfo{}, err
	}

	now := time.Now()
	meta := SessionMeta{
		ID:            built.Session.ID,
		Dir:           wtDir,
		ProjectDir:    projectDir,
		Isolation:     isolation,
		Branch:        branch,
		DefaultBranch: defaultBranch,
		Provider:      built.Cfg.Provider,
		Model:         built.Cfg.Model,
		CreatedAt:     now,
		LastActive:    now,
	}
	if err := WriteSessionMeta(meta); err != nil {
		built.Close()
		if isolation == "worktree" {
			_ = gitutil.WorktreeRemove(projectDir, wtDir)
			_ = gitutil.BranchForceDelete(projectDir, branch)
		}
		return rpc.SessionInfo{}, fmt.Errorf("write session meta: %w", err)
	}

	eng := newEng(built, ctx)
	m.register(eng, meta)
	m.emitStatus(protocol.SessionStatus{ID: meta.ID, State: "idle", Dir: meta.Dir, Preview: sessionPreview(meta)})
	if dirtyMain {
		eng.Emit(protocol.Notice{
			Level: "info",
			Text:  "main checkout has uncommitted changes; this session branched from HEAD",
		})
	}
	return m.sessionInfo(eng, meta, true, "idle"), nil
}

// Get returns a live engine by ID.
func (m *SessionManager) Get(id string) (*engine.Engine, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	eng, ok := m.engines[id]
	return eng, ok
}

// Stop closes a live engine but keeps transcript + meta on disk.
func (m *SessionManager) Stop(id string) error {
	m.mu.Lock()
	eng, ok := m.engines[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session not live: %s", id)
	}
	delete(m.engines, id)
	delete(m.states, id)
	statusFn := m.statusFn
	m.mu.Unlock()

	dir := eng.CWD()
	eng.Close()
	_ = TouchSessionMeta(id)

	// statusFn already captured under lock above; call emitStatus without re-locking states.
	if statusFn != nil {
		statusFn(protocol.SessionStatus{ID: id, State: "unloaded", Dir: dir})
	}
	return nil
}

// Delete stops the engine (if live) and removes transcript + meta + reads.
// For worktree sessions, removes the worktree and deletes the branch only if merged.
func (m *SessionManager) Delete(id string) error {
	meta, metaErr := FindSessionMeta(id)
	if metaErr == nil && meta.Isolation == "worktree" && meta.ProjectDir != "" {
		unlock := m.lockProject(meta.ProjectDir)
		defer unlock()
	}

	m.mu.Lock()
	_, live := m.engines[id]
	m.mu.Unlock()
	if live {
		_ = m.Stop(id)
	}
	if metaErr != nil {
		return metaErr
	}

	projectKey := session.MetaProjectKey(meta)
	txPath := filepath.Join(session.SessionsDir(), session.ProjectHash(projectKey), id+".jsonl")
	_ = os.Remove(txPath)
	_ = os.Remove(txPath + ".reads.json")
	_ = os.Remove(MetaPath(txPath))
	_ = session.DeleteSession(projectKey, id)

	if meta.Isolation == "worktree" && meta.ProjectDir != "" && meta.Dir != "" {
		if err := gitutil.WorktreeRemove(meta.ProjectDir, meta.Dir); err != nil {
			fmt.Fprintf(os.Stderr, "[notice] worktree remove %s: %v\n", meta.Dir, err)
		}
		branch := meta.Branch
		if branch == "" {
			branch = "talos/" + id
		}
		if err := gitutil.BranchDelete(meta.ProjectDir, branch); err != nil {
			fmt.Fprintf(os.Stderr, "[notice] keeping branch %s (not fully merged): %v\n", branch, err)
		}
	}
	m.emitStatus(protocol.SessionStatus{ID: id, State: "deleted", Dir: meta.Dir})
	return nil
}

// List returns live ∪ persisted sessions across all projects, newest first.
func (m *SessionManager) List() []rpc.SessionInfo {
	persisted, _ := ListAllSessionMetas()

	m.mu.Lock()
	live := make(map[string]*engine.Engine, len(m.engines))
	states := make(map[string]string, len(m.states))
	for id, eng := range m.engines {
		live[id] = eng
		states[id] = m.states[id]
	}
	m.mu.Unlock()

	byID := make(map[string]rpc.SessionInfo)
	for _, meta := range persisted {
		preview := sessionPreview(meta)
		state := "unloaded"
		if meta.Merged {
			state = "merged"
		}
		info := rpc.SessionInfo{
			ID:         meta.ID,
			Dir:        meta.Dir,
			ProjectDir: meta.ProjectDir,
			Isolation:  meta.Isolation,
			Branch:     meta.Branch,
			Merged:     meta.Merged,
			State:      state,
			Live:       false,
			Provider:   meta.Provider,
			Model:      meta.Model,
			Preview:    preview,
			CreatedAt:  meta.CreatedAt,
			LastActive: meta.LastActive,
		}
		if eng, ok := live[meta.ID]; ok {
			state := states[meta.ID]
			if state == "" {
				if eng.Busy() {
					state = "busy"
				} else {
					state = "idle"
				}
			}
			info = m.sessionInfo(eng, meta, true, state)
			info.Preview = preview
			m.enrichGit(&info, meta)
			delete(live, meta.ID)
		}
		byID[meta.ID] = info
	}
	// Live engines without meta (shouldn't happen, but be complete).
	for id, eng := range live {
		state := states[id]
		if state == "" {
			if eng.Busy() {
				state = "busy"
			} else {
				state = "idle"
			}
		}
		meta := SessionMeta{
			ID:         id,
			Dir:        eng.CWD(),
			ProjectDir: eng.CWD(),
			Isolation:  "none",
			Provider:   eng.ProviderName(),
			Model:      eng.ModelName(),
			LastActive: eng.LastUsed(),
		}
		byID[id] = m.sessionInfo(eng, meta, true, state)
	}

	out := make([]rpc.SessionInfo, 0, len(byID))
	for _, info := range byID {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastActive.After(out[j].LastActive)
	})
	return out
}

// Resume loads a session by explicit ID. If already live, returns it.
func (m *SessionManager) Resume(ctx context.Context, id string) (*engine.Engine, error) {
	if id == "" {
		return nil, fmt.Errorf("session id is required")
	}
	m.mu.Lock()
	if eng, ok := m.engines[id]; ok {
		m.mu.Unlock()
		return eng, nil
	}
	buildFn := m.buildFn
	newEng := m.newEng
	cfg := m.cfg
	m.mu.Unlock()

	meta, err := FindSessionMeta(id)
	if err != nil {
		return nil, err
	}
	if meta.Merged {
		return nil, fmt.Errorf("session %s has already been merged and cannot be resumed", id)
	}

	if meta.Isolation == "worktree" {
		unlock := m.lockProject(meta.ProjectDir)
		defer unlock()
		if err := m.ensureWorktree(meta); err != nil {
			return nil, err
		}
	}

	built, err := buildFn(ctx, engine.BuildOpts{
		Cfg:        cfg,
		Dir:        meta.Dir,
		ProjectDir: meta.ProjectDir,
		SessionID:  id,
		Provider:   meta.Provider,
		Model:      meta.Model,
	})
	if err != nil {
		return nil, err
	}
	meta.LastActive = time.Now()
	_ = WriteSessionMeta(meta)

	eng := newEng(built, ctx)
	m.register(eng, meta)
	m.emitStatus(protocol.SessionStatus{ID: id, State: "idle", Dir: meta.Dir, Preview: sessionPreview(meta)})
	return eng, nil
}

func (m *SessionManager) ensureWorktree(meta SessionMeta) error {
	if meta.ProjectDir == "" || meta.Dir == "" {
		return fmt.Errorf("worktree session missing project/dir")
	}
	branch := meta.Branch
	if branch == "" {
		branch = "talos/" + meta.ID
	}
	// Already present?
	if st, err := os.Stat(meta.Dir); err == nil && st.IsDir() {
		list, err := gitutil.WorktreeList(meta.ProjectDir)
		if err == nil {
			want := filepath.Clean(meta.Dir)
			for _, p := range list {
				if filepath.Clean(p) == want {
					return nil
				}
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(meta.Dir), 0o755); err != nil {
		return err
	}
	return gitutil.WorktreeAddFromBranch(meta.ProjectDir, meta.Dir, branch)
}

// CloseAll stops every live engine.
func (m *SessionManager) CloseAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.engines))
	for id := range m.engines {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Stop(id)
	}
}

// AnyBusy reports whether any live engine is mid-turn.
func (m *SessionManager) AnyBusy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, eng := range m.engines {
		if eng.Busy() {
			return true
		}
	}
	return false
}

// LastEngineActivity is the most recent LastUsed across live engines.
// Returns zero time when no engines are loaded.
func (m *SessionManager) LastEngineActivity() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest time.Time
	for _, eng := range m.engines {
		if t := eng.LastUsed(); t.After(latest) {
			latest = t
		}
	}
	return latest
}

// LiveCount returns the number of loaded engines.
func (m *SessionManager) LiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.engines)
}

// OrphanWorktrees returns worktree dirs under baseDir with no matching session meta.
func (m *SessionManager) OrphanWorktrees() []string {
	root := filepath.Join(m.cfg.BaseDir, "worktrees")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	metas, _ := ListAllSessionMetas()
	known := make(map[string]bool)
	for _, meta := range metas {
		if meta.Isolation == "worktree" && meta.Dir != "" {
			known[filepath.Clean(meta.Dir)] = true
		}
	}
	var orphans []string
	for _, proj := range entries {
		if !proj.IsDir() {
			continue
		}
		projPath := filepath.Join(root, proj.Name())
		sessions, err := os.ReadDir(projPath)
		if err != nil {
			continue
		}
		for _, s := range sessions {
			if !s.IsDir() {
				continue
			}
			p := filepath.Clean(filepath.Join(projPath, s.Name()))
			if !known[p] {
				orphans = append(orphans, p)
			}
		}
	}
	sort.Strings(orphans)
	return orphans
}

// GCWorktrees removes orphaned worktree directories (and tries git worktree remove).
func (m *SessionManager) GCWorktrees() ([]string, error) {
	orphans := m.OrphanWorktrees()
	var removed []string
	for _, wt := range orphans {
		// Best-effort: find a project root via git from the worktree itself.
		projectDir, err := gitutil.RepoRoot(wt)
		if err == nil {
			unlock := m.lockProject(projectDir)
			_ = gitutil.WorktreeRemove(projectDir, wt)
			unlock()
		}
		if err := os.RemoveAll(wt); err != nil {
			fmt.Fprintf(os.Stderr, "[notice] gc worktree %s: %v\n", wt, err)
			continue
		}
		removed = append(removed, wt)
	}
	return removed, nil
}

func (m *SessionManager) register(eng *engine.Engine, meta SessionMeta) {
	id := eng.SessionID()
	if id == "" {
		id = meta.ID
		eng.SetSessionID(id)
	}
	m.mu.Lock()
	m.engines[id] = eng
	m.states[id] = "idle"
	m.mu.Unlock()

	eng.Subscribe(func(ev protocol.Event) {
		if st, ok := ev.(protocol.SessionStatus); ok {
			m.emitStatus(st)
			_ = TouchSessionMeta(st.ID)
		}
	})
}

// emitStatus broadcasts a SessionStatus via the daemon-wide hook.
func (m *SessionManager) emitStatus(st protocol.SessionStatus) {
	m.mu.Lock()
	if st.State != "deleted" && st.State != "unloaded" {
		m.states[st.ID] = st.State
	}
	fn := m.statusFn
	m.mu.Unlock()
	if fn != nil {
		fn(st)
	}
}

func (m *SessionManager) sessionInfo(eng *engine.Engine, meta SessionMeta, live bool, state string) rpc.SessionInfo {
	if meta.Merged {
		state = "merged"
		live = false
	}
	info := rpc.SessionInfo{
		ID:         meta.ID,
		Dir:        meta.Dir,
		ProjectDir: meta.ProjectDir,
		Isolation:  meta.Isolation,
		Branch:     meta.Branch,
		Merged:     meta.Merged,
		State:      state,
		Live:       live,
		Provider:   meta.Provider,
		Model:      meta.Model,
		CreatedAt:  meta.CreatedAt,
		LastActive: meta.LastActive,
	}
	if eng != nil {
		if info.Dir == "" {
			info.Dir = eng.CWD()
		}
		if info.Provider == "" {
			info.Provider = eng.ProviderName()
		}
		if info.Model == "" {
			info.Model = eng.ModelName()
		}
		if t := eng.LastUsed(); !t.IsZero() {
			info.LastActive = t
		}
	}
	return info
}

// lockProject serializes Git mutations that touch one primary checkout.
func (m *SessionManager) lockProject(projectDir string) func() {
	key := filepath.Clean(projectDir)
	m.mergeMu.Lock()
	lock := m.projectLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		m.projectLocks[key] = lock
	}
	m.mergeMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (m *SessionManager) enrichGit(info *rpc.SessionInfo, meta SessionMeta) {
	if meta.Merged || meta.Isolation != "worktree" || meta.Dir == "" || meta.ProjectDir == "" {
		return
	}
	const ttl = 5 * time.Second
	m.mu.Lock()
	if cached, ok := m.gitCache[meta.ID]; ok && time.Since(cached.at) < ttl {
		info.Ahead = cached.ahead
		info.Dirty = cached.dirty
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	ahead := 0
	if base, err := gitutil.RevParse(meta.ProjectDir, "HEAD"); err == nil {
		if n, err := gitutil.AheadCount(meta.Dir, base); err == nil {
			ahead = n
		}
	}
	dirty, _ := gitutil.IsDirty(meta.Dir)

	m.mu.Lock()
	m.gitCache[meta.ID] = gitEnrichment{ahead: ahead, dirty: dirty, at: time.Now()}
	m.mu.Unlock()
	info.Ahead = ahead
	info.Dirty = dirty
}

func sessionPreview(meta SessionMeta) string {
	return session.PreviewSession(session.MetaProjectKey(meta), meta.ID)
}
