package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/engine"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/session"
)

// SessionMeta is the sidecar written next to a transcript so Resume/List can
// recover cwd and provider without scanning JSONL.
type SessionMeta struct {
	ID         string    `json:"id"`
	Dir        string    `json:"dir"`
	ProjectDir string    `json:"project_dir"`
	Isolation  string    `json:"isolation"` // "none" for now (worktree in plan 3)
	Provider   string    `json:"provider"`
	Model      string    `json:"model"`
	CreatedAt  time.Time `json:"created_at"`
	LastActive time.Time `json:"last_active"`
}

// SessionManager owns live engines and persisted session metadata.
type SessionManager struct {
	mu       sync.Mutex
	cfg      *config.Config
	engines  map[string]*engine.Engine
	states   map[string]string // last known SessionStatus state for live engines
	statusFn func(protocol.SessionStatus)
	buildFn  func(ctx context.Context, o engine.BuildOpts) (*engine.Built, error)
	newEng   func(b *engine.Built, ctx context.Context) *engine.Engine
}

// NewSessionManager creates an empty manager. Engines are created on demand.
func NewSessionManager(cfg *config.Config) *SessionManager {
	return &SessionManager{
		cfg:     cfg,
		engines: make(map[string]*engine.Engine),
		states:  make(map[string]string),
		buildFn: engine.Build,
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
	dir, err := filepath.Abs(params.Dir)
	if err != nil {
		return rpc.SessionInfo{}, fmt.Errorf("resolve dir: %w", err)
	}
	projectDir := dir // isolation "none" for now
	isolation := "none"
	// Worktree isolation lands in plan 3; ignore until then.
	_ = params.Isolation

	m.mu.Lock()
	buildFn := m.buildFn
	newEng := m.newEng
	cfg := m.cfg
	m.mu.Unlock()

	built, err := buildFn(ctx, engine.BuildOpts{
		Cfg:        cfg,
		Dir:        dir,
		ProjectDir: projectDir,
		SessionID:  params.Resume,
		Provider:   params.Provider,
		Model:      params.Model,
	})
	if err != nil {
		return rpc.SessionInfo{}, err
	}

	now := time.Now()
	meta := SessionMeta{
		ID:         built.Session.ID,
		Dir:        dir,
		ProjectDir: projectDir,
		Isolation:  isolation,
		Provider:   built.Cfg.Provider,
		Model:      built.Cfg.Model,
		CreatedAt:  now,
		LastActive: now,
	}
	if err := WriteSessionMeta(meta); err != nil {
		built.Close()
		return rpc.SessionInfo{}, fmt.Errorf("write session meta: %w", err)
	}

	eng := newEng(built, ctx)
	m.register(eng, meta)
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

	if statusFn != nil {
		statusFn(protocol.SessionStatus{ID: id, State: "unloaded", Dir: dir})
	}
	return nil
}

// Delete stops the engine (if live) and removes transcript + meta + reads.
func (m *SessionManager) Delete(id string) error {
	meta, metaErr := FindSessionMeta(id)

	m.mu.Lock()
	_, live := m.engines[id]
	m.mu.Unlock()
	if live {
		_ = m.Stop(id)
	}
	if metaErr != nil {
		return metaErr
	}

	txPath := filepath.Join(session.SessionsDir(), session.ProjectHash(meta.Dir), id+".jsonl")
	_ = os.Remove(txPath)
	_ = os.Remove(txPath + ".reads.json")
	_ = os.Remove(MetaPath(txPath))
	// Also try session.DeleteSession for consistency.
	_ = session.DeleteSession(meta.Dir, id)
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
		info := rpc.SessionInfo{
			ID:         meta.ID,
			Dir:        meta.Dir,
			ProjectDir: meta.ProjectDir,
			Isolation:  meta.Isolation,
			State:      "unloaded",
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
	return eng, nil
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
			m.mu.Lock()
			m.states[st.ID] = st.State
			fn := m.statusFn
			m.mu.Unlock()
			if fn != nil {
				fn(st)
			}
			_ = TouchSessionMeta(st.ID)
		}
	})
}

func (m *SessionManager) sessionInfo(eng *engine.Engine, meta SessionMeta, live bool, state string) rpc.SessionInfo {
	info := rpc.SessionInfo{
		ID:         meta.ID,
		Dir:        meta.Dir,
		ProjectDir: meta.ProjectDir,
		Isolation:  meta.Isolation,
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

// MetaPath returns the sidecar path for a transcript path.
func MetaPath(transcriptPath string) string {
	return strings.TrimSuffix(transcriptPath, ".jsonl") + ".meta.json"
}

// WriteSessionMeta persists meta next to the transcript.
func WriteSessionMeta(meta SessionMeta) error {
	dir := filepath.Join(session.SessionsDir(), session.ProjectHash(meta.Dir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, meta.ID+".meta.json")
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadSessionMeta loads meta from an explicit path.
func ReadSessionMeta(path string) (SessionMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionMeta{}, err
	}
	var meta SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return SessionMeta{}, err
	}
	return meta, nil
}

// FindSessionMeta searches all project dirs under SessionsDir for id.meta.json.
func FindSessionMeta(id string) (SessionMeta, error) {
	root := session.SessionsDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return SessionMeta{}, fmt.Errorf("session not found: %s", id)
		}
		return SessionMeta{}, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, e.Name(), id+".meta.json")
		meta, err := ReadSessionMeta(path)
		if err == nil {
			return meta, nil
		}
		// Fallback: transcript exists without meta — cannot recover dir.
		tx := filepath.Join(root, e.Name(), id+".jsonl")
		if _, err := os.Stat(tx); err == nil {
			return SessionMeta{}, fmt.Errorf("session %s has no meta sidecar (dir unknown)", id)
		}
	}
	return SessionMeta{}, fmt.Errorf("session not found: %s", id)
}

// ListAllSessionMetas walks every project under SessionsDir.
func ListAllSessionMetas() ([]SessionMeta, error) {
	root := session.SessionsDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []SessionMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		proj := filepath.Join(root, e.Name())
		files, err := os.ReadDir(proj)
		if err != nil {
			continue
		}
		seen := make(map[string]bool)
		for _, f := range files {
			name := f.Name()
			if strings.HasSuffix(name, ".meta.json") {
				meta, err := ReadSessionMeta(filepath.Join(proj, name))
				if err != nil {
					continue
				}
				out = append(out, meta)
				seen[meta.ID] = true
			}
		}
		// Transcripts without meta are skipped (cannot recover dir).
		_ = seen
	}
	return out, nil
}

// TouchSessionMeta updates LastActive on an existing meta file.
func TouchSessionMeta(id string) error {
	meta, err := FindSessionMeta(id)
	if err != nil {
		return err
	}
	meta.LastActive = time.Now()
	return WriteSessionMeta(meta)
}

func sessionPreview(meta SessionMeta) string {
	path := filepath.Join(session.SessionsDir(), session.ProjectHash(meta.Dir), meta.ID+".jsonl")
	previews, err := session.ListSessionPreviews(meta.Dir)
	if err != nil {
		return ""
	}
	for _, p := range previews {
		if p.ID == meta.ID {
			return p.Preview
		}
	}
	_ = path
	return ""
}
