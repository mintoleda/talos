package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/mintoleda/talos/internal/agents"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/mcp"
	"github.com/mintoleda/talos/internal/models"
	"github.com/mintoleda/talos/internal/notify"
	"github.com/mintoleda/talos/internal/pricing"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/provider/openai"
	"github.com/mintoleda/talos/internal/safety"
	"github.com/mintoleda/talos/internal/session"
	"github.com/mintoleda/talos/internal/tui/dialogs"
)

// Params carries the dependencies needed to construct a LocalEngine.
type Params struct {
	Loop          *loop.Loop
	PromptBuilder *loop.PromptBuilder
	Prices        *pricing.Table
	Provider      string
	Model         string
	BaseDir       string
	CWD           string
	MCPManager    *mcp.Manager
	AgentBuilder  *agents.Builder // may be nil
	Checkpointer  *safety.Checkpointer
	Policy        *safety.Policy
	NotifyConfig  notify.Config
	Context       context.Context

	// SwitchProvider recreates the LLM provider when the user runs /model.
	// When nil, SwitchModel returns an error.
	SwitchProvider func(providerName, modelName string) error
}

// steerQueue is a thread-safe queue of steer messages. This is a simplified
// copy of the SteerQueue from the tui package, defined here so that
// LocalEngine does not depend on internal/tui (avoiding a future cycle when
// tui imports client).
type steerQueue struct {
	mu       sync.Mutex
	messages [][]protocol.ContentBlock
}

func (q *steerQueue) Enqueue(blocks []protocol.ContentBlock) {
	q.mu.Lock()
	q.messages = append(q.messages, blocks)
	q.mu.Unlock()
}

func (q *steerQueue) Drain() [][]protocol.ContentBlock {
	q.mu.Lock()
	msgs := q.messages
	q.messages = nil
	q.mu.Unlock()
	return msgs
}

func (q *steerQueue) Withdraw() []protocol.ContentBlock {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.messages)
	if n == 0 {
		return nil
	}
	last := q.messages[n-1]
	q.messages = q.messages[:n-1]
	return last
}

func (q *steerQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.messages)
}

// LocalEngine implements the Engine interface by driving a loop.Loop
// in-process with background goroutines for input processing and compaction.
type LocalEngine struct {
	lp           *loop.Loop
	pb           *loop.PromptBuilder
	prices       *pricing.Table
	providerName string
	modelName    string
	baseDir      string
	cwd          string
	mcpManager   *mcp.Manager
	agentBuilder *agents.Builder
	pol          *safety.Policy
	switchProv   func(providerName, modelName string) error

	evCh  chan protocol.Event
	inCh  chan []protocol.ContentBlock
	intCh chan struct{}
	cmpCh chan string
	sq    steerQueue

	wg     sync.WaitGroup
	closed chan struct{}

	modelCache []models.Entry
	mu         sync.Mutex // guards modelCache
	closeOnce  sync.Once
}

// NewLocalEngine creates a LocalEngine and starts its background goroutines.
// The caller must call Close() to shut down the engine when done.
func NewLocalEngine(p Params) *LocalEngine {
	evCh := make(chan protocol.Event, 64)
	inCh := make(chan []protocol.ContentBlock, 1)
	intCh := make(chan struct{}, 1)
	cmpCh := make(chan string, 1)

	e := &LocalEngine{
		lp:           p.Loop,
		pb:           p.PromptBuilder,
		prices:       p.Prices,
		providerName: p.Provider,
		modelName:    p.Model,
		baseDir:      p.BaseDir,
		cwd:          p.CWD,
		mcpManager:   p.MCPManager,
		agentBuilder: p.AgentBuilder,
		pol:          p.Policy,
		switchProv:   p.SwitchProvider,
		evCh:         evCh,
		inCh:         inCh,
		intCh:        intCh,
		cmpCh:        cmpCh,
		closed:       make(chan struct{}),
	}

	// Wire the steer queue as the loop's drain function so steer messages
	// are consumed before each LLM call.
	p.Loop.SteerFunc = e.sq.Drain

	rawEmit := func(ev protocol.Event) { evCh <- ev }
	emit := notify.Wrap(rawEmit, p.NotifyConfig)

	// Input processing goroutine.
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		for blocks := range inCh {
			if p.Checkpointer != nil {
				_, _ = p.Checkpointer.Snapshot("before-run")
			}
			turnCtx, cancel := context.WithCancel(p.Context)
			go func() {
				select {
				case <-intCh:
					cancel()
				case <-turnCtx.Done():
				}
			}()
			if err := p.Loop.RunTurn(turnCtx, blocks, emit); err != nil {
				if errors.Is(err, context.Canceled) {
					emit(protocol.Notice{Level: "warn", Text: "interrupted"})
				} else {
					emit(protocol.Notice{Level: "error", Text: err.Error()})
				}
				emit(protocol.TurnEnded{})
			}
			cancel()
		}
	}()

	// Compaction goroutine.
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		for focus := range cmpCh {
			compactCtx, cancel := context.WithTimeout(p.Context, 120*time.Second)
			summary, err := p.Loop.CompactNow(compactCtx, focus)
			cancel()
			if err != nil {
				emit(protocol.Notice{Level: "error", Text: "/compact failed: " + err.Error()})
			} else if summary == "" {
				emit(protocol.Notice{Level: "info", Text: "nothing to compact"})
			} else {
				emit(protocol.Notice{Level: "info", Text: "compacted oldest chunk - summary: " + summary})
			}
		}
	}()

	return e
}

// Submit sends user content blocks to the engine for processing.
func (e *LocalEngine) Submit(blocks []protocol.ContentBlock) {
	select {
	case <-e.closed:
		return
	default:
	}
	select {
	case e.inCh <- blocks:
	case <-e.closed:
	}
}

// Interrupt cancels the currently running turn.
func (e *LocalEngine) Interrupt() {
	select {
	case e.intCh <- struct{}{}:
	default:
	}
}

// Approve is a no-op for LocalEngine — permission approval is handled
// directly through the PermissionRequested event's ReplyCh, which the
// TUI processes via the event channel without going through the Engine.
// The plan parameter is reserved for future diff/plan display.
func (e *LocalEngine) Approve(ok bool, plan []byte) {}

// Steer enqueues a steer message that is processed before the next LLM call.
func (e *LocalEngine) Steer(blocks []protocol.ContentBlock) {
	e.sq.Enqueue(blocks)
}

func (e *LocalEngine) WithdrawSteer() []protocol.ContentBlock {
	return e.sq.Withdraw()
}

func (e *LocalEngine) PendingSteers() int {
	return e.sq.Len()
}

// NewSession starts a fresh conversation and returns its ID.
func (e *LocalEngine) NewSession() (string, error) {
	ns := session.NewSession(e.cwd)
	tx, err := session.Create(ns.Path)
	if err != nil {
		return "", err
	}
	e.lp.SetTranscript(tx)
	fmt.Fprintf(os.Stderr, "[started new session %s]\n", ns.ID)
	return ns.ID, nil
}

// Resume loads an existing session by ID, returning the new session ID
// and the frozen message history for replay.
func (e *LocalEngine) Resume(id string) (string, []protocol.FrozenMessage, error) {
	var sess session.Session
	var err error
	if id != "" {
		sess, err = session.OpenSession(e.cwd, id)
		if err != nil {
			return "", nil, fmt.Errorf("session not found: %s", id)
		}
	} else {
		// Pick the most recent session.
		sess, err = session.LatestSession(e.cwd)
		if err != nil {
			return "", nil, err
		}
	}
	tx, err := session.Load(sess.Path)
	if err != nil {
		return "", nil, fmt.Errorf("load session: %w", err)
	}
	e.lp.SetTranscript(tx)
	fmt.Fprintf(os.Stderr, "[resumed session %s]\n", sess.ID)
	return sess.ID, tx.Frozen(), nil
}

// ListSessions returns all sessions for the current project.
// Returns an empty list if no sessions directory exists yet.
func (e *LocalEngine) ListSessions() ([]dialogs.SessionEntry, error) {
	previews, err := session.ListSessionPreviews(e.cwd)
	if err != nil {
		// No sessions directory yet is not an error.
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	entries := make([]dialogs.SessionEntry, len(previews))
	for i, p := range previews {
		entries[i] = dialogs.SessionEntry{ID: p.ID, ModTime: p.ModTime, Preview: p.Preview}
	}
	return entries, nil
}

// DeleteSession removes a session by ID.
func (e *LocalEngine) DeleteSession(id string) error {
	return session.DeleteSession(e.cwd, id)
}

// ListModels fetches available models across all logged-in providers.
// Results are cached and refreshed when a new login happens.
func (e *LocalEngine) ListModels() ([]models.Entry, error) {
	e.mu.Lock()
	cached := e.modelCache
	e.mu.Unlock()
	if cached != nil {
		return cached, nil
	}

	type result struct {
		entries []models.Entry
	}
	ch := make(chan result, len(provider.All))
	var wg sync.WaitGroup
	for _, kp := range provider.All {
		// Anthropic does not expose a /v1/models endpoint; skip it.
		if kp.Name == "anthropic" {
			continue
		}
		key := config.ResolveKeyFor(e.baseDir, kp.Name, kp.EnvVar)
		if key == "" {
			continue
		}
		wg.Add(1)
		go func(kp provider.Known, key string) {
			defer wg.Done()
			lister := openai.New(kp.BaseURL, key)
			ids, err := lister.ListModels(context.Background())
			if err != nil {
				return
			}
			entries := make([]models.Entry, len(ids))
			for i, id := range ids {
				entries[i] = models.Entry{Provider: kp.Name, ID: id}
			}
			ch <- result{entries: entries}
		}(kp, key)
	}
	go func() { wg.Wait(); close(ch) }()

	var all []models.Entry
	for r := range ch {
		all = append(all, r.entries...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Provider != all[j].Provider {
			return all[i].Provider < all[j].Provider
		}
		return all[i].ID < all[j].ID
	})

	e.mu.Lock()
	e.modelCache = all
	e.mu.Unlock()
	return all, nil
}

// SwitchModel recreates the LLM provider and swaps it into the loop.
func (e *LocalEngine) SwitchModel(pName, pModel string) error {
	if e.switchProv == nil {
		return fmt.Errorf("switch provider not configured")
	}
	if err := e.switchProv(pName, pModel); err != nil {
		return err
	}
	e.providerName = pName
	e.modelName = pModel
	e.mu.Lock()
	e.modelCache = nil
	e.mu.Unlock()
	return nil
}

// CycleThinking advances to the next abstract thinking level and returns it.
func (e *LocalEngine) CycleThinking() (string, error) {
	caps := provider.SupportedLevels(e.pb.Model())
	cur := e.pb.ThinkingLevel()
	if cur == "" {
		cur = caps[0]
	}
	for i, l := range caps {
		if l == cur {
			next := caps[(i+1)%len(caps)]
			e.pb.SetThinkingLevel(next)
			if err := config.SaveThinkingLevel(e.baseDir, next); err != nil {
				fmt.Fprintf(os.Stderr, "[warning] save thinking level: %v\n", err)
			}
			return next, nil
		}
	}
	e.pb.SetThinkingLevel(caps[0])
	_ = config.SaveThinkingLevel(e.baseDir, caps[0])
	return caps[0], nil
}

// CurrentThinkingLevel returns the current thinking level without cycling.
func (e *LocalEngine) CurrentThinkingLevel() string {
	return e.pb.ThinkingLevel()
}

// CyclePermissionMode advances to the next permission mode (auto→ask→auto)
// and returns the new mode name. Panic is excluded from the cycle; use
// TogglePanic instead.
func (e *LocalEngine) CyclePermissionMode() (string, error) {
	if e.pol == nil {
		return "", fmt.Errorf("permission policy not configured")
	}
	next := safety.NextMode(e.pol.Mode())
	e.pol.SetMode(next)
	return next.String(), nil
}

// PermissionMode returns the current permission mode name without cycling.
func (e *LocalEngine) PermissionMode() string {
	if e.pol == nil {
		return "auto"
	}
	return e.pol.Mode().String()
}

// TogglePanic toggles panic mode on/off. Returns the resulting mode name.
func (e *LocalEngine) TogglePanic() (string, error) {
	if e.pol == nil {
		return "", fmt.Errorf("permission policy not configured")
	}
	return e.pol.TogglePanic().String(), nil
}
// Compact triggers manual compaction, optionally guided by a focus string.
// The compaction runs asynchronously; progress is reported via the event
// channel.
func (e *LocalEngine) Compact(focus string) error {
	select {
	case <-e.closed:
		return fmt.Errorf("engine closed")
	default:
	}
	select {
	case e.cmpCh <- focus:
		return nil
	default:
		return fmt.Errorf("compaction request dropped (busy)")
	}
}

// Stats returns cumulative token and cost counters.
func (e *LocalEngine) Stats() (int, int, int, float64, error) {
	s := e.lp.Stats()
	cm := s.InputTokens - s.CachedTokens
	c := 0.0
	if e.prices != nil && e.modelName != "" {
		c = e.prices.Cost(e.modelName, s.InputTokens, s.OutputTokens)
	}
	return s.InputTokens, s.OutputTokens, cm, c, nil
}

// LoginProviders returns the list of known providers with login status.
func (e *LocalEngine) LoginProviders() ([]dialogs.LoginProvider, error) {
	var out []dialogs.LoginProvider
	for _, kp := range provider.All {
		key := config.ResolveKeyFor(e.baseDir, kp.Name, kp.EnvVar)
		out = append(out, dialogs.LoginProvider{
			Name:     kp.Name,
			Label:    kp.Label,
			LoggedIn: key != "",
		})
	}
	return out, nil
}

// Login persists an API key for the given provider and clears the model cache
// so ListModels picks up the new key on the next call.
func (e *LocalEngine) Login(provName, key string) error {
	if err := config.WriteAuthKey(e.baseDir, provName, key); err != nil {
		return err
	}
	e.mu.Lock()
	e.modelCache = nil
	e.mu.Unlock()
	return nil
}

// MCPStatus returns a human-readable summary of MCP server connections.
func (e *LocalEngine) MCPStatus() (string, error) {
	return e.mcpManager.Status(), nil
}

// MCPCount returns the number of connected MCP servers.
func (e *LocalEngine) MCPCount() int {
	return e.mcpManager.ConnectedCount()
}

// CancelSubagent cancels a running subagent by ID.
func (e *LocalEngine) CancelSubagent(id string) {
	if e.agentBuilder != nil {
		e.agentBuilder.CancelSubagent(id)
	}
}

func (e *LocalEngine) History() ([]protocol.FrozenMessage, error) {
	return e.lp.History(), nil
}

func (e *LocalEngine) ListFiles(prefix string) ([]string, error) {
	return ListFiles(e.cwd, prefix)
}

func (e *LocalEngine) ResolveInput(text string) ([]protocol.ContentBlock, string, error) {
	return ResolveInput(e.cwd, text)
}

func (e *LocalEngine) PushInstruction() (string, string, error) {
	return PushInstruction(e.cwd)
}

// Events returns the read-only event channel for protocol events.
func (e *LocalEngine) Events() <-chan protocol.Event {
	return e.evCh
}

// Close shuts down the engine goroutines, closes the event channel, and
// releases the underlying loop resources. Safe to call multiple times.
func (e *LocalEngine) Close() {
	e.closeOnce.Do(func() {
		close(e.closed)
		close(e.inCh)
		close(e.cmpCh)
		e.wg.Wait()
		close(e.evCh)
		e.lp.Close()
	})
}
