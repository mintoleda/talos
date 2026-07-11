package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mintoleda/talos/internal/agents"
	"github.com/mintoleda/talos/internal/client"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/executor"
	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/mcp"
	"github.com/mintoleda/talos/internal/memory"
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

// Params carries dependencies needed to construct an Engine without a full Built.
// Used by tests and multi-tab construction that shares provider/exec/MCP.
type Params struct {
	Loop          *loop.Loop
	PromptBuilder *loop.PromptBuilder
	Prices        *pricing.Table
	Cfg           *config.Config // per-session copy; required for SwitchModel
	Provider      string
	Model         string
	BaseDir       string
	CWD           string
	ProjectDir    string // origin repo for session/memory keying; defaults to CWD
	MCPManager    *mcp.Manager
	AgentBuilder  *agents.Builder
	Checkpointer  *safety.Checkpointer
	Policy        *safety.Policy
	Executor      executor.Executor
	MemStore      *memory.Store
	NoTools       bool
	NotifyConfig  notify.Config
	SessionID     string
	Context       context.Context

	// OwnStack, when set, is closed by Engine.Close (loop/MCP/registry/transcript).
	// Leave nil for shared-stack tab engines that only own their loop.
	OwnStack *Built
}

// pendingApproval holds a captured permission reply channel plus metadata for Snapshot.
type pendingApproval struct {
	reply    func(bool, []byte)
	toolName string
	command  string
	reason   string
}

// Engine drives a loop.Loop for both local TUI and daemon clients.
// It satisfies client.Engine (block Submit/Steer + Events) and exposes
// SubmitText/SteerText/Subscribe/Snapshot/HandleRequest for the server.
type Engine struct {
	lp           *loop.Loop
	pb           *loop.PromptBuilder
	prices       *pricing.Table
	cfg          *config.Config
	providerName string
	modelName    string
	baseDir      string
	cwd          string
	projectDir   string
	mcpManager   *mcp.Manager
	agentBuilder *agents.Builder
	pol          *safety.Policy
	exec         executor.Executor
	memStore     *memory.Store
	cp           *safety.Checkpointer
	noTools      bool
	notifyCfg    notify.Config
	ownStack     *Built

	sessionID string

	evCh  chan protocol.Event
	inCh  chan []protocol.ContentBlock
	intCh chan struct{}
	cmpCh chan string
	sq    steerQueue

	ctx    context.Context
	cancel context.CancelFunc

	wg     sync.WaitGroup
	closed chan struct{}

	modelCache []models.Entry
	mu         sync.Mutex // guards modelCache, subscribers, pending, sessionID
	closeOnce  sync.Once

	subscribers map[int]func(protocol.Event)
	subNext     int
	pending     *pendingApproval

	stateMu    sync.Mutex
	stateBusy  bool
	stateText  string
	stateTools []protocol.ToolSnapshot
	lastUsed   time.Time
}

// NewEngine creates an Engine and starts its background goroutines.
// The caller must call Close() when done.
func NewEngine(p Params) *Engine {
	parent := p.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)

	evCh := make(chan protocol.Event, 64)
	inCh := make(chan []protocol.ContentBlock, 1)
	intCh := make(chan struct{}, 1)
	cmpCh := make(chan string, 1)

	cfg := p.Cfg
	if cfg == nil {
		cfg = &config.Config{Provider: p.Provider, Model: p.Model, BaseDir: p.BaseDir}
	}

	e := &Engine{
		lp:           p.Loop,
		pb:           p.PromptBuilder,
		prices:       p.Prices,
		cfg:          cfg,
		providerName: p.Provider,
		modelName:    p.Model,
		baseDir:      p.BaseDir,
		cwd:          p.CWD,
		projectDir:   p.ProjectDir,
		mcpManager:   p.MCPManager,
		agentBuilder: p.AgentBuilder,
		pol:          p.Policy,
		exec:         p.Executor,
		memStore:     p.MemStore,
		cp:           p.Checkpointer,
		noTools:      p.NoTools,
		notifyCfg:    p.NotifyConfig,
		ownStack:     p.OwnStack,
		sessionID:    p.SessionID,
		evCh:         evCh,
		inCh:         inCh,
		intCh:        intCh,
		cmpCh:        cmpCh,
		ctx:          ctx,
		cancel:       cancel,
		closed:       make(chan struct{}),
		subscribers:  make(map[int]func(protocol.Event)),
		lastUsed:     time.Now(),
	}
	if e.providerName == "" {
		e.providerName = cfg.Provider
	}
	if e.modelName == "" {
		e.modelName = cfg.Model
	}
	if e.baseDir == "" {
		e.baseDir = cfg.BaseDir
	}
	if e.projectDir == "" {
		e.projectDir = e.cwd
	}

	p.Loop.SteerFunc = e.sq.Drain

	if p.OwnStack != nil && p.OwnStack.Registry != nil {
		if bg := p.OwnStack.Registry.Background(); bg != nil {
			bg.SetEmit(e.Emit)
		}
	}

	e.wg.Add(1)
	go e.runInput(inCh, intCh)

	e.wg.Add(1)
	go e.runCompact(cmpCh)

	return e
}

// NewEngine constructs a running Engine that owns the Built stack.
// After this call, Built.Close is a no-op; use Engine.Close instead.
func (b *Built) NewEngine(ctx context.Context) *Engine {
	if b == nil {
		return nil
	}
	pol := b.Policy
	if pol == nil && b.Executor != nil {
		pol = b.Executor.Policy()
	}
	eng := NewEngine(Params{
		Loop:          b.Loop,
		PromptBuilder: b.PromptBuilder,
		Prices:        b.Prices,
		Cfg:           b.Cfg,
		Provider:      b.Cfg.Provider,
		Model:         b.Cfg.Model,
		BaseDir:       b.Cfg.BaseDir,
		CWD:           b.Dir,
		ProjectDir:    b.ProjectDir,
		MCPManager:    b.MCPManager,
		AgentBuilder:  b.AgentBuilder,
		Checkpointer:  b.Checkpointer,
		Policy:        pol,
		Executor:      b.Executor,
		MemStore:      b.MemStore,
		NotifyConfig:  b.Cfg.Notifications,
		SessionID:     b.Session.ID,
		Context:       ctx,
		OwnStack:      b,
	})
	b.transferred = true
	return eng
}

func (e *Engine) runInput(inCh <-chan []protocol.ContentBlock, intCh <-chan struct{}) {
	defer e.wg.Done()
	for blocks := range inCh {
		if e.cp != nil {
			_, _ = e.cp.Snapshot("before-run")
		}
		e.markBusy()
		turnCtx, cancel := context.WithCancel(e.ctx)
		go func() {
			select {
			case <-intCh:
				cancel()
			case <-turnCtx.Done():
			}
		}()
		emit := e.turnEmit()
		if err := e.lp.RunTurn(turnCtx, blocks, emit); err != nil {
			if errors.Is(err, context.Canceled) {
				emit(protocol.Notice{Level: "warn", Text: "interrupted"})
			} else {
				emit(protocol.Notice{Level: "error", Text: err.Error()})
			}
			emit(protocol.TurnEnded{})
		}
		cancel()
	}
}

func (e *Engine) runCompact(cmpCh <-chan string) {
	defer e.wg.Done()
	for focus := range cmpCh {
		compactCtx, cancel := context.WithTimeout(e.ctx, 120*time.Second)
		summary, err := e.lp.CompactNow(compactCtx, focus)
		cancel()
		if err != nil {
			e.Emit(protocol.Notice{Level: "error", Text: "/compact failed: " + err.Error()})
		} else if summary == "" {
			e.Emit(protocol.Notice{Level: "info", Text: "nothing to compact"})
		} else {
			e.Emit(protocol.Notice{Level: "info", Text: "compacted oldest chunk - summary: " + summary})
		}
	}
}

func (e *Engine) markBusy() {
	e.stateMu.Lock()
	e.stateBusy = true
	e.stateText = ""
	e.stateTools = nil
	e.stateMu.Unlock()
	e.emitSessionStatus("busy")
}

func (e *Engine) turnEmit() protocol.EmitFunc {
	emit := func(ev protocol.Event) {
		switch evt := ev.(type) {
		case protocol.PermissionRequested:
			replyCh := evt.ReplyCh
			e.setPending(pendingApproval{
				reply: func(approved bool, _ []byte) {
					if replyCh != nil {
						replyCh <- approved
					}
				},
				toolName: evt.ToolName,
				command:  evt.Command,
				reason:   evt.Reason,
			})
			// Strip ReplyCh so subscribers cannot double-send.
			evt.ReplyCh = nil
			e.emitSessionStatus("awaiting_approval")
			e.Emit(evt)
			return
		}
		e.Emit(ev)
	}
	emit = e.trackState(emit)
	return notify.Wrap(emit, e.notifyCfg)
}

func (e *Engine) setPending(p pendingApproval) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending = &p
}

func (e *Engine) emitSessionStatus(state string) {
	e.mu.Lock()
	id := e.sessionID
	e.mu.Unlock()
	if id == "" {
		return
	}
	e.Emit(protocol.SessionStatus{ID: id, State: state, Dir: e.cwd})
}

// Emit fans out to Subscribe callbacks and the Events() channel.
// When Subscribe listeners exist (daemon), Events sends are non-blocking so a
// full/unread Events buffer cannot stall the turn. Local TUI has no
// subscribers and uses blocking Events delivery.
func (e *Engine) Emit(ev protocol.Event) {
	select {
	case <-e.closed:
		return
	default:
	}
	e.mu.Lock()
	subs := make([]func(protocol.Event), 0, len(e.subscribers))
	for _, fn := range e.subscribers {
		subs = append(subs, fn)
	}
	e.mu.Unlock()
	for _, fn := range subs {
		fn(ev)
	}
	if len(subs) > 0 {
		select {
		case e.evCh <- ev:
		case <-e.closed:
		default:
		}
		return
	}
	select {
	case e.evCh <- ev:
	case <-e.closed:
	}
}

// Subscribe registers a fan-out callback (daemon / server clients).
// The returned cancel removes the callback; it is safe to call more than once.
func (e *Engine) Subscribe(fn func(protocol.Event)) (cancel func()) {
	e.mu.Lock()
	id := e.subNext
	e.subNext++
	e.subscribers[id] = fn
	e.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			e.mu.Lock()
			delete(e.subscribers, id)
			e.mu.Unlock()
		})
	}
}

// Busy reports whether a turn is in progress.
func (e *Engine) Busy() bool {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	return e.stateBusy
}

// LastUsed returns the last time the engine handled client activity.
func (e *Engine) LastUsed() time.Time {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	return e.lastUsed
}

// Touch records client activity for idle-timeout accounting.
func (e *Engine) Touch() {
	e.stateMu.Lock()
	e.lastUsed = time.Now()
	e.stateMu.Unlock()
}

func (e *Engine) SessionID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sessionID
}

// CWD returns the session working directory.
func (e *Engine) CWD() string {
	return e.cwd
}

// ProviderName returns the active provider name.
func (e *Engine) ProviderName() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.providerName
}

// ModelName returns the active model name.
func (e *Engine) ModelName() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.modelName
}

func (e *Engine) SetSessionID(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sessionID = id
}

// Submit sends user content blocks (client.Engine / TUI path).
// A single text block is ResolveInput'd so @image paths become ImageBlocks
// (TUI/local clients submit bare text; no client-side pre-resolution).
func (e *Engine) Submit(blocks []protocol.ContentBlock) {
	select {
	case <-e.closed:
		return
	default:
	}
	if len(blocks) == 1 && blocks[0].Type == protocol.BlockText {
		resolved, _, err := e.ResolveInput(blocks[0].Text)
		if err != nil {
			e.Emit(protocol.Notice{Level: "error", Text: err.Error()})
			return
		}
		blocks = resolved
	}
	e.enqueueTurn(blocks)
}

// SubmitText is the daemon entry: slash intercept, ResolveInput, UserInput, then turn.
func (e *Engine) SubmitText(text string) {
	select {
	case <-e.closed:
		return
	default:
	}
	e.Touch()
	if strings.HasPrefix(text, "/") {
		result := e.handleSlash(text)
		e.Emit(protocol.Notice{Level: "info", Text: result})
		return
	}
	blocks, display, err := e.ResolveInput(text)
	if err != nil {
		e.Emit(protocol.Notice{Level: "error", Text: err.Error()})
		return
	}
	e.Emit(protocol.UserInput{Text: display})
	e.enqueueTurn(blocks)
}

func (e *Engine) enqueueTurn(blocks []protocol.ContentBlock) {
	select {
	case e.inCh <- blocks:
	case <-e.closed:
	}
}

// Interrupt cancels the currently running turn.
func (e *Engine) Interrupt() {
	e.Touch()
	select {
	case e.intCh <- struct{}{}:
	default:
	}
}

// Approve consumes a pending permission request (first-wins). Emits ApprovalResolved.
func (e *Engine) Approve(ok bool, plan []byte) {
	e.Touch()
	e.mu.Lock()
	pending := e.pending
	e.pending = nil
	e.mu.Unlock()
	if pending == nil {
		return
	}
	pending.reply(ok, plan)
	e.Emit(protocol.ApprovalResolved{Approved: ok})
	if ok {
		e.emitSessionStatus("busy")
	} else {
		// Denial still leaves the turn running until the executor finishes.
		e.emitSessionStatus("busy")
	}
}

// Steer enqueues blocks for the next LLM call (client.Engine).
func (e *Engine) Steer(blocks []protocol.ContentBlock) {
	e.sq.Enqueue(blocks)
}

// SteerText enqueues text for the next LLM call (daemon).
func (e *Engine) SteerText(text string) {
	e.Touch()
	e.sq.Enqueue(protocol.TextBlocks(text))
}

func (e *Engine) WithdrawSteer() []protocol.ContentBlock {
	return e.sq.Withdraw()
}

func (e *Engine) PendingSteers() int {
	return e.sq.Len()
}

// Snapshot returns live turn state for newly-attached clients.
func (e *Engine) Snapshot() protocol.EngineSnapshot {
	e.stateMu.Lock()
	toolSnaps := make([]protocol.ToolSnapshot, len(e.stateTools))
	copy(toolSnaps, e.stateTools)
	snap := protocol.EngineSnapshot{
		Busy:         e.stateBusy,
		StreamedText: e.stateText,
		ActiveTools:  toolSnaps,
	}
	e.stateMu.Unlock()

	e.mu.Lock()
	pending := e.pending
	e.mu.Unlock()
	if pending != nil {
		snap.PendingPermission = &protocol.PendingPermission{
			ToolName: pending.toolName,
			Command:  pending.command,
			Reason:   pending.reason,
		}
	}
	if bg := e.bgRegistry(); bg != nil {
		for _, s := range bg.List() {
			snap.BgProcs = append(snap.BgProcs, protocol.BgSnapshot{
				ID:           s.ID,
				Command:      s.Command,
				Dir:          s.Dir,
				Running:      s.Running,
				ExitCode:     s.ExitCode,
				RecentOutput: s.RecentOutput,
				StartedAt:    s.StartedAt,
			})
		}
	}
	return snap
}

func (e *Engine) trackState(inner func(protocol.Event)) func(protocol.Event) {
	return func(ev protocol.Event) {
		switch evt := ev.(type) {
		case protocol.TextDelta:
			e.stateMu.Lock()
			e.stateText += evt.Text
			e.stateMu.Unlock()
		case protocol.ToolStarted:
			e.stateMu.Lock()
			e.stateTools = append(e.stateTools, protocol.ToolSnapshot{
				ID: evt.ID, Name: evt.Name, Args: evt.Args,
			})
			e.stateMu.Unlock()
		case protocol.ToolFinished:
			e.stateMu.Lock()
			for i, t := range e.stateTools {
				if t.ID == evt.ID {
					e.stateTools = append(e.stateTools[:i], e.stateTools[i+1:]...)
					break
				}
			}
			e.stateMu.Unlock()
		case protocol.TurnEnded:
			e.stateMu.Lock()
			e.stateBusy = false
			e.stateText = ""
			e.stateTools = nil
			e.stateMu.Unlock()
			e.mu.Lock()
			e.pending = nil
			e.mu.Unlock()
			e.emitSessionStatus("idle")
		}
		inner(ev)
	}
}

func (e *Engine) NewSession() (string, error) {
	ns := session.NewSession(e.projectDir)
	tx, err := session.Create(ns.Path)
	if err != nil {
		return "", err
	}
	e.lp.SetTranscript(tx)
	e.SetSessionID(ns.ID)
	session.EnsureSessionMeta(ns, e.cwd, e.projectDir, e.ProviderName(), e.ModelName())
	fmt.Fprintf(os.Stderr, "[started new session %s]\n", ns.ID)
	return ns.ID, nil
}

func (e *Engine) Resume(id string) (string, []protocol.FrozenMessage, error) {
	var sess session.Session
	var err error
	if id != "" {
		sess, err = session.OpenSession(e.projectDir, id)
		if err != nil {
			return "", nil, fmt.Errorf("session not found: %s", id)
		}
	} else {
		sess, err = session.LatestSession(e.projectDir)
		if err != nil {
			return "", nil, err
		}
	}
	tx, err := session.Load(sess.Path)
	if err != nil {
		return "", nil, fmt.Errorf("load session: %w", err)
	}
	e.lp.SetTranscript(tx)
	e.SetSessionID(sess.ID)
	session.EnsureSessionMeta(sess, e.cwd, e.projectDir, e.ProviderName(), e.ModelName())
	fmt.Fprintf(os.Stderr, "[resumed session %s]\n", sess.ID)
	return sess.ID, tx.Frozen(), nil
}

func (e *Engine) ListSessions() ([]dialogs.SessionEntry, error) {
	previews, err := session.ListSessionPreviews(e.projectDir)
	if err != nil {
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

func (e *Engine) DeleteSession(id string) error {
	return session.DeleteSession(e.projectDir, id)
}

func (e *Engine) ListModels() ([]models.Entry, error) {
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

// SwitchModel recreates the LLM provider on this engine's cfg copy.
// It does NOT call config.SaveProviderModel — per-session only.
func (e *Engine) SwitchModel(pName, pModel string) error {
	if e.cfg == nil {
		return fmt.Errorf("switch provider not configured")
	}
	oldProv := e.cfg.Provider
	oldModel := e.cfg.Model

	e.cfg.Provider = pName
	if pModel != "" {
		e.cfg.Model = pModel
	}
	if pName != oldProv {
		e.cfg.BaseURL = ""
	}
	e.cfg.ResolveAPIKey()

	prov, comp, err := NewProvider(e.cfg, e.noTools)
	if err != nil {
		e.cfg.Provider = oldProv
		e.cfg.Model = oldModel
		return err
	}
	e.lp.SetProvider(prov)
	if comp != nil && e.cfg.Historian && e.memStore != nil {
		hist, err := NewHistorian(e.cfg, e.memStore)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[warning] historian disabled: %v\n", err)
		} else {
			comp.Historian = hist
		}
	}
	e.lp.SetCompactor(comp)
	e.pb.SetModel(e.cfg.Model)
	e.providerName = e.cfg.Provider
	e.modelName = e.cfg.Model
	e.mu.Lock()
	e.modelCache = nil
	e.mu.Unlock()
	return nil
}

func (e *Engine) CycleThinking() (string, error) {
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

func (e *Engine) CurrentThinkingLevel() string {
	return e.pb.ThinkingLevel()
}

func (e *Engine) CyclePermissionMode() (string, error) {
	if e.pol == nil {
		return "", fmt.Errorf("permission policy not configured")
	}
	next := safety.NextMode(e.pol.Mode())
	e.pol.SetMode(next)
	return next.String(), nil
}

// SetPermissionMode sets the policy mode from a string (auto/ask/panic).
func (e *Engine) SetPermissionMode(mode string) error {
	if e.pol == nil {
		return fmt.Errorf("permission policy not configured")
	}
	e.pol.SetMode(safety.ParseMode(mode))
	return nil
}

func (e *Engine) PermissionMode() string {
	if e.pol == nil {
		return "auto"
	}
	return e.pol.Mode().String()
}

func (e *Engine) TogglePanic() (string, error) {
	if e.pol == nil {
		return "", fmt.Errorf("permission policy not configured")
	}
	return e.pol.TogglePanic().String(), nil
}

func (e *Engine) Compact(focus string) error {
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

func (e *Engine) Stats() (int, int, int, float64, error) {
	s := e.lp.Stats()
	cm := s.InputTokens - s.CachedTokens
	c := 0.0
	if e.prices != nil && e.modelName != "" {
		c = e.prices.Cost(e.modelName, s.InputTokens, s.OutputTokens)
	}
	return s.InputTokens, s.OutputTokens, cm, c, nil
}

func (e *Engine) LoginProviders() ([]dialogs.LoginProvider, error) {
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

func (e *Engine) Login(provName, key string) error {
	if err := config.WriteAuthKey(e.baseDir, provName, key); err != nil {
		return err
	}
	e.mu.Lock()
	e.modelCache = nil
	e.mu.Unlock()
	return nil
}

func (e *Engine) MCPStatus() (string, error) {
	if e.mcpManager == nil {
		return "no MCP manager", nil
	}
	return e.mcpManager.Status(), nil
}

func (e *Engine) MCPCount() int {
	if e.mcpManager == nil {
		return 0
	}
	return e.mcpManager.ConnectedCount()
}

func (e *Engine) CancelSubagent(id string) {
	if e.agentBuilder != nil {
		e.agentBuilder.CancelSubagent(id)
	}
}

func (e *Engine) History() ([]protocol.FrozenMessage, error) {
	return e.lp.History(), nil
}

func (e *Engine) ListFiles(prefix string) ([]string, error) {
	return client.ListFiles(e.cwd, prefix)
}

func (e *Engine) ResolveInput(text string) ([]protocol.ContentBlock, string, error) {
	return client.ResolveInput(e.cwd, text)
}

func (e *Engine) PushInstruction() (string, string, error) {
	return client.PushInstruction(e.cwd)
}

func (e *Engine) Events() <-chan protocol.Event {
	return e.evCh
}

func (e *Engine) ToggleSubagents() string {
	if e.agentBuilder == nil {
		return "subagents not enabled in config"
	}
	enabled := !e.pb.SubagentEnabled()
	e.pb.SetSubagentEnabled(enabled)
	if enabled {
		return "subagents: on"
	}
	return "subagents: off"
}

// Close shuts down goroutines and releases owned resources.
func (e *Engine) Close() {
	e.closeOnce.Do(func() {
		close(e.closed)
		e.cancel()
		close(e.inCh)
		close(e.cmpCh)
		e.wg.Wait()
		close(e.evCh)
		if e.ownStack != nil {
			e.ownStack.forceClose()
		} else if e.lp != nil {
			e.lp.Close()
		}
	})
}

// Compile-time check: Engine satisfies client.Engine.
var _ client.Engine = (*Engine)(nil)

// steerQueue is a thread-safe queue of steer messages.
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
