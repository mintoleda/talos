package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/mcp"
	"github.com/mintoleda/talos/internal/models"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/session"
	"github.com/mintoleda/talos/internal/stack"
	"github.com/mintoleda/talos/internal/version"
)

// Agent serves ACP over one connection. Each ACP session maps to one Talos
// transcript; the runtime stack for a session is built lazily on first use so
// that clients probing capabilities with throwaway session/new calls cause no
// MCP connections, transcripts, or other side effects.
type Agent struct {
	cfg  *config.Config
	conn *Conn

	// listModels returns the model picker catalogue. Overridable in tests.
	listModels func(ctx context.Context) []models.Entry

	// loadMu serializes session/load so replacing a live session with its
	// reloaded copy is atomic (no orphaned runtime).
	loadMu sync.Mutex

	mu       sync.Mutex
	sessions map[string]*acpSession
	catalog  []models.Entry
}

// Serve runs an ACP agent on r/w until EOF.
func Serve(ctx context.Context, cfg *config.Config, r io.Reader, w io.Writer) error {
	a := newAgent(cfg)
	a.conn = NewConn(r, w, a.handle)
	err := a.conn.Serve(ctx)
	a.closeAll()
	return err
}

func newAgent(cfg *config.Config) *Agent {
	return &Agent{
		cfg:      cfg,
		sessions: make(map[string]*acpSession),
		listModels: func(ctx context.Context) []models.Entry {
			ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			return stack.ListModels(ctx, cfg.BaseDir)
		},
	}
}

func (a *Agent) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		return a.initialize(params)
	case "authenticate":
		return struct{}{}, nil
	case "session/new":
		return a.newSession(ctx, params)
	case "session/load":
		return a.loadSession(ctx, params)
	case "session/list":
		return a.listSessions(params)
	case "session/prompt":
		var req PromptRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		s, err := a.session(req.SessionID)
		if err != nil {
			return nil, err
		}
		return s.prompt(ctx, req.Prompt)
	case "session/cancel":
		var req CancelNotification
		if err := json.Unmarshal(params, &req); err == nil {
			if s, err := a.session(req.SessionID); err == nil {
				s.cancelTurn()
			}
		}
		return nil, nil
	case "session/close":
		var req CancelNotification
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		a.mu.Lock()
		s := a.sessions[req.SessionID]
		delete(a.sessions, req.SessionID)
		a.mu.Unlock()
		if s != nil {
			s.close()
		}
		return struct{}{}, nil
	case "session/set_model":
		var req SetModelRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		s, err := a.session(req.SessionID)
		if err != nil {
			return nil, err
		}
		return struct{}{}, s.setModel(req.ModelID)
	case "session/set_config_option":
		var req SetConfigOptionRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		s, err := a.session(req.SessionID)
		if err != nil {
			return nil, err
		}
		return s.setConfigOption(req.ConfigID, req.Value)
	}
	if strings.HasPrefix(method, "$/") {
		return nil, nil // extension notifications are optional
	}
	return nil, &RPCError{Code: codeMethodNotFound, Message: "method not found: " + method}
}

func (a *Agent) initialize(params json.RawMessage) (any, error) {
	var req InitializeRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, invalidParams(err)
	}
	return InitializeResponse{
		ProtocolVersion: ProtocolVersion,
		AgentCapabilities: AgentCapabilities{
			LoadSession:        true,
			PromptCapabilities: PromptCapabilities{Image: true, EmbeddedContext: true},
			MCPCapabilities:    MCPCapabilities{HTTP: true, SSE: true},
			SessionCapabilities: &SessionCapabilities{
				List:  &struct{}{},
				Close: &struct{}{},
			},
		},
		AuthMethods: []any{},
		AgentInfo:   &Implementation{Name: "talos", Title: "Talos", Version: version.VERSION},
	}, nil
}

func (a *Agent) session(id string) (*acpSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[id]
	if s == nil {
		return nil, &RPCError{Code: codeInvalidParams, Message: "unknown session: " + id}
	}
	return s, nil
}

func (a *Agent) closeAll() {
	a.mu.Lock()
	all := make([]*acpSession, 0, len(a.sessions))
	for _, s := range a.sessions {
		all = append(all, s)
	}
	a.sessions = map[string]*acpSession{}
	a.mu.Unlock()
	for _, s := range all {
		s.close()
	}
}

func (a *Agent) newSession(ctx context.Context, params json.RawMessage) (any, error) {
	var req NewSessionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, invalidParams(err)
	}
	cwd, err := absDir(req.CWD)
	if err != nil {
		return nil, invalidParams(err)
	}
	projectDir := stack.FindRepoRoot(cwd)
	s := a.addSession(session.NewSession(projectDir).ID, cwd, projectDir, req.MCPServers)
	resp := NewSessionResponse{SessionID: s.id}
	_, resp.Models, resp.ConfigOptions = s.state(a.modelCatalog(ctx))
	return afterResponse{resp, s.announceCommands}, nil
}

func (a *Agent) loadSession(ctx context.Context, params json.RawMessage) (any, error) {
	a.loadMu.Lock()
	defer a.loadMu.Unlock()
	var req LoadSessionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, invalidParams(err)
	}
	cwd, err := absDir(req.CWD)
	if err != nil {
		return nil, invalidParams(err)
	}
	// Prefer the meta sidecar's project key; fall back to cwd's repo root.
	projectDir := stack.FindRepoRoot(cwd)
	sess, err := session.OpenSession(projectDir, req.SessionID)
	if err != nil {
		meta, merr := session.FindSessionMeta(req.SessionID)
		if merr != nil {
			return nil, &RPCError{Code: codeInvalidParams, Message: "session not found: " + req.SessionID}
		}
		projectDir = session.MetaProjectKey(meta)
		if sess, err = session.OpenSession(projectDir, req.SessionID); err != nil {
			return nil, &RPCError{Code: codeInvalidParams, Message: "session not found: " + req.SessionID}
		}
	}
	tx, err := session.Load(sess.Path)
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	history := tx.Frozen()
	_ = tx.Close()

	a.mu.Lock()
	old := a.sessions[req.SessionID]
	a.mu.Unlock()
	if old != nil {
		old.close()
	}
	s := a.addSession(req.SessionID, cwd, projectDir, req.MCPServers)
	if meta, err := session.FindSessionMeta(req.SessionID); err == nil && meta.Provider != "" && meta.Model != "" {
		s.provider, s.model = meta.Provider, meta.Model
	}
	s.replay(history)

	var resp LoadSessionResponse
	_, resp.Models, resp.ConfigOptions = s.state(a.modelCatalog(ctx))
	return afterResponse{resp, s.announceCommands}, nil
}

func (a *Agent) addSession(id, cwd, projectDir string, servers []MCPServer) *acpSession {
	s := &acpSession{
		agent:       a,
		id:          id,
		cwd:         cwd,
		projectDir:  projectDir,
		mcp:         toMCPConfigs(servers),
		provider:    a.cfg.Provider,
		model:       a.cfg.Model,
		thinking:    a.cfg.ThinkingLevel,
		allowAlways: map[string]bool{},
	}
	a.mu.Lock()
	a.sessions[id] = s
	a.mu.Unlock()
	return s
}

func (a *Agent) listSessions(params json.RawMessage) (any, error) {
	var req ListSessionsRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams(err)
		}
	}
	metas, err := session.ListAllSessionMetas()
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var want string
	if req.CWD != "" {
		if abs, err := filepath.Abs(req.CWD); err == nil {
			want = stack.FindRepoRoot(abs)
		}
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].LastActive.After(metas[j].LastActive) })
	resp := ListSessionsResponse{Sessions: []SessionInfo{}}
	previews := map[string]map[string]string{}
	for _, m := range metas {
		key := session.MetaProjectKey(m)
		if want != "" && key != want && m.Dir != req.CWD {
			continue
		}
		if previews[key] == nil {
			previews[key] = map[string]string{}
			if ps, err := session.ListSessionPreviews(key); err == nil {
				for _, p := range ps {
					previews[key][p.ID] = p.Preview
				}
			}
		}
		title, ok := previews[key][m.ID]
		if !ok {
			continue // meta without a transcript (never prompted)
		}
		resp.Sessions = append(resp.Sessions, SessionInfo{
			SessionID: m.ID,
			CWD:       m.Dir,
			Title:     title,
			UpdatedAt: m.LastActive.UTC().Format(time.RFC3339),
		})
	}
	return resp, nil
}

// modelCatalog lists models once per process; it hits provider APIs, so the
// result is cached and bounded by a timeout.
func (a *Agent) modelCatalog(ctx context.Context) []models.Entry {
	a.mu.Lock()
	cached := a.catalog
	a.mu.Unlock()
	if cached != nil {
		return cached
	}
	entries := a.listModels(ctx)
	if entries == nil {
		entries = []models.Entry{}
	}
	a.mu.Lock()
	a.catalog = entries
	a.mu.Unlock()
	return entries
}

func absDir(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("cwd is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("cwd %q is not a directory", dir)
	}
	return abs, nil
}

func toMCPConfigs(servers []MCPServer) []mcp.ServerConfig {
	out := make([]mcp.ServerConfig, 0, len(servers))
	for _, s := range servers {
		c := mcp.ServerConfig{Name: s.Name, Command: s.Command, Args: s.Args, URL: s.URL}
		switch s.Type {
		case "sse":
			c.Transport = "sse"
		case "http":
			c.Transport = "http"
		}
		if len(s.Env) > 0 {
			c.Env = make(map[string]string, len(s.Env))
			for _, e := range s.Env {
				c.Env[e.Name] = e.Value
			}
		}
		if len(s.Headers) > 0 {
			c.Headers = make(map[string]string, len(s.Headers))
			for _, h := range s.Headers {
				c.Headers[h.Name] = h.Value
			}
		}
		out = append(out, c)
	}
	return out
}

func thinkingOption(model, current string) ConfigOption {
	levels := provider.SupportedLevels(model)
	if current == "" || !contains(levels, current) {
		current = provider.ClampThinkingLevel(model, current)
		if current == "" && len(levels) > 0 {
			current = levels[0]
		}
	}
	opt := ConfigOption{ID: "thought_level", Name: "Thinking", Category: "thought_level", Type: "select", CurrentValue: current}
	for _, l := range levels {
		opt.Options = append(opt.Options, ConfigOptionValue{Value: l, Name: l})
	}
	return opt
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
