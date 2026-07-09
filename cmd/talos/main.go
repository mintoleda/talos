package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mintoleda/talos/internal/client"
	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/engine"
	"github.com/mintoleda/talos/internal/jsonutil"
	"github.com/mintoleda/talos/internal/memory"
	"github.com/mintoleda/talos/internal/pricing"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/server"
	"github.com/mintoleda/talos/internal/session"
	"github.com/mintoleda/talos/internal/tui"
)

type Flags struct {
	Print        string
	Continue     bool
	Resume       bool
	SessionID    string
	Model        string
	Provider     string
	BaseURL      string
	NoTools      bool
	SystemPrompt string
	DebugCache   bool
}

func main() {
	if err := run(); err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "talos: %v\n", err)
			os.Exit(1)
		}
	}
}

func run() error {
	var f Flags
	flag.StringVar(&f.Print, "p", "", "run a single prompt and exit")
	flag.StringVar(&f.Print, "print", "", "run a single prompt and exit")
	flag.BoolVar(&f.Continue, "c", false, "continue the latest session")
	flag.BoolVar(&f.Continue, "continue", false, "continue the latest session")
	flag.BoolVar(&f.Resume, "r", false, "resume a session")
	flag.BoolVar(&f.Resume, "resume", false, "resume a session")
	flag.StringVar(&f.SessionID, "session", "", "exact session id")
	flag.StringVar(&f.Model, "model", "", "model override")
	flag.StringVar(&f.Provider, "provider", "", "provider: openai or anthropic")
	flag.StringVar(&f.BaseURL, "base-url", "", "base URL override")
	flag.BoolVar(&f.NoTools, "no-tools", false, "disable tools")
	flag.StringVar(&f.SystemPrompt, "system-prompt", "", "system prompt override")
	flag.BoolVar(&f.DebugCache, "debug-cache", false, "log cache prefix hashes")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Override(f.BaseURL, f.Model, "")
	cfg.OverrideProvider(f.Provider)

	args := flag.Args()
	if len(args) > 0 {
		switch args[0] {
		case "dream":
			return runDream(cfg, args[1:])
		case "serve":
			return runServe(cfg, args[1:])
		case "attach":
			sid := ""
			if len(args) > 1 {
				sid = args[1]
			}
			return runAttach(context.Background(), cfg, sid)
		case "server":
			return runServerCmd(cfg, args[1:])
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	sessionID := f.SessionID
	if sessionID == "" && f.Resume {
		// Interactive pick stays in main; pass the chosen ID into Build.
		// On failure, leave SessionID empty so Build creates a new session
		// (matches prior run() fallback behavior).
		if picked, pickErr := pickSession(cwd); pickErr == nil {
			sessionID = picked.ID
		}
	}

	built, err := engine.Build(context.Background(), engine.BuildOpts{
		Cfg:        cfg,
		Dir:        cwd,
		SessionID:  sessionID,
		Continue:   f.Continue && sessionID == "",
		NoTools:    f.NoTools,
		Provider:   f.Provider,
		Model:      f.Model,
		PrintMode:  f.Print != "",
		DebugCache: f.DebugCache,
	})
	if err != nil {
		return err
	}
	defer built.Close()

	a := &app{
		cfg:          built.Cfg,
		lp:           built.Loop,
		pb:           built.PromptBuilder,
		prov:         built.Provider,
		exec:         built.Executor,
		agentBuilder: built.AgentBuilder,
		mcpManager:   built.MCPManager,
		memStore:     built.MemStore,
		cwd:          built.Dir,
		noTools:      f.NoTools,
	}

	fmt.Fprintf(os.Stderr, "[session %s] [%s/%s]\n", built.Session.ID, built.Cfg.Provider, built.Cfg.Model)

	switch {
	case f.Print != "":
		ctx, cancel := context.WithCancel(context.Background())
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			select {
			case <-sigCh:
				cancel()
			case <-ctx.Done():
			}
		}()
		_, _ = built.Checkpointer.Snapshot("before-run")
		err := built.Loop.RunTurn(ctx, protocol.TextBlocks(f.Print), renderFinal(os.Stdout))
		signal.Reset(syscall.SIGINT, syscall.SIGTERM)
		cancel()

		// Ephemeral session: -p runs must not leave traces on disk.
		// Remove the transcript and reads files so the session does not
		// show up in /resume or the resume picker.
		os.Remove(built.Session.Path)
		os.Remove(built.Session.Path + ".reads.json")
		if parent := filepath.Dir(built.Session.Path); parent != "." {
			entries, _ := os.ReadDir(parent)
			if len(entries) == 0 {
				os.Remove(parent)
			}
		}

		return err

	default:
		tuiCtx := context.Background()
		localEng := built.NewEngine(tuiCtx)
		inputTokens, outputTokens, cacheMiss, seedCost, _ := localEng.Stats()

		initialCfg := tui.Config{
			SessionID: built.Session.ID,
			Mode:      tui.ModeSingleAgent,
			Engine:    localEng,
			Shutdown:  localEng.Close,
			Provider:  built.Cfg.Provider,
			Model:     built.Cfg.Model,
			Pricing:   built.Prices,
			SeedStats: struct {
				InputTokens  int
				OutputTokens int
				CacheMiss    int
				Cost         float64
			}{
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
				CacheMiss:    cacheMiss,
				Cost:         seedCost,
			},
			InitialHistory: built.Transcript.Frozen(),
			ToggleSubagents: func() string {
				return localEng.ToggleSubagents()
			},
		}

		return tui.RunTabs(tuiCtx, initialCfg, localEng.Events(), a.makeNewTabFn(tuiCtx, built.Checkpointer, built.Prices, built.Cfg.Notifications))
	}
}

// renderTo returns an EmitFunc that writes events in a format suitable for
// one-shot (-p) mode. Text deltas go to stdout (the agent's response); all
// metadata — tool calls, notices, turn summaries, permission prompts — goes
// to stderr so that stdout contains only the answer, suitable for piping.
// renderFinal emits only text deltas to out, suitable for -p / pipe mode.
func renderFinal(out *os.File) protocol.EmitFunc {
	return func(ev protocol.Event) {
		switch e := ev.(type) {
		case protocol.TextDelta:
			fmt.Fprint(out, e.Text)
		}
	}
}

func renderTo(out *os.File, debugCache bool) protocol.EmitFunc {
	errOut := os.Stderr
	return func(ev protocol.Event) {
		switch e := ev.(type) {
		case protocol.TextDelta:
			fmt.Fprint(out, e.Text)
		case protocol.ToolStarted:
			fmt.Fprintf(errOut, "\n▶ %s\n", e.Name)
		case protocol.ToolFinished:
			mark := "✓"
			if e.Result.IsError {
				mark = "✗"
			}
			text := e.Result.Content
			if len(text) > 400 {
				text = text[:200] + "\n[...]\n" + text[len(text)-200:]
			}
			fmt.Fprintf(errOut, "\n%s %s\n%s\n", mark, e.Result.ToolUseID, text)
		case protocol.Notice:
			fmt.Fprintf(errOut, "\n[%s] %s\n", e.Level, e.Text)
		case protocol.TurnEnded:
			extra := ""
			if e.Usage.CachedPromptTokens > 0 {
				extra = fmt.Sprintf(" | cached=%d", e.Usage.CachedPromptTokens)
			}
			fmt.Fprintf(errOut, "\n[turn ended: %s | prompt=%d completion=%d%s]\n",
				e.StopReason, e.Usage.PromptTokens, e.Usage.CompletionTokens, extra)
		case protocol.PermissionRequested:
			fmt.Fprintf(errOut, "\n⚠ %s requires approval: %s\nAllow? [y/N] ", e.ToolName, e.Reason)
			var ans string
			fmt.Scanln(&ans)
			if e.ReplyCh != nil {
				e.ReplyCh <- ans == "y" || ans == "Y"
			}
		case protocol.SubagentStarted:
			fmt.Fprintf(errOut, "\n  ⊳ %s: %s\n", e.Agent, e.Task)
		case protocol.SubagentEvent:
			switch ie := e.Inner.(type) {
			case protocol.ToolStarted:
				fmt.Fprintf(errOut, "    [%s] ▶ %s\n", e.Agent, ie.Name)
			case protocol.ToolFinished:
				mark := "✓"
				if ie.Result.IsError {
					mark = "✗"
				}
				fmt.Fprintf(errOut, "    [%s] %s\n", e.Agent, mark)
			}
		case protocol.SubagentFinished:
			fmt.Fprintf(errOut, "  ⊲ %s done (in=%d out=%d $%.4f)\n",
				e.Agent, e.Usage.InputTokens, e.Usage.OutputTokens, e.Usage.Cost)
		default:
			// Unknown event type: ignore safely so new events don't break
			// older frontends.
		}
	}
}

func runServe(cfg *config.Config, args []string) error {
	detach := false
	for _, a := range args {
		if a == "-d" || a == "--detach" {
			detach = true
		}
	}
	if detach && os.Getenv("TALOS_SERVE_DAEMON") != "1" {
		return detachServe()
	}
	return runDaemon(cfg)
}

func detachServe() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "serve")
	cmd.Env = append(os.Environ(), "TALOS_SERVE_DAEMON=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("detach serve: %w", err)
	}
	fmt.Fprintf(os.Stderr, "talos daemon started (pid %d)\n", cmd.Process.Pid)
	return nil
}

func runDaemon(cfg *config.Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if cfg.ServerListen == "" {
		cfg.ServerListen = "ws:127.0.0.1:0"
	}
	idle := server.ResolveIdleTimeout(cfg)
	d := server.NewDaemon(cfg, idle)
	if webDir := findWebDist(); webDir != "" {
		d.SetWebDir(webDir)
	}
	fmt.Fprintf(os.Stderr, "[daemon] idle timeout %v (0=never)\n", idle)
	return d.Start(ctx)
}

func runServerCmd(cfg *config.Config, args []string) error {
	if len(args) == 0 || args[0] == "help" {
		return fmt.Errorf(`talos server commands:
  talos server list       List daemon sessions
  talos server kill       Kill the multi-session daemon
  talos server kill-all   Kill the multi-session daemon
  talos server help       Show this help

Start the daemon with: talos serve [-d]`)
	}
	switch args[0] {
	case "list":
		return listDaemonSessions(cfg)
	case "kill", "kill-all":
		if err := server.KillDaemon(cfg.BaseDir); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "killed daemon")
		return nil
	default:
		return fmt.Errorf("unknown server command: %s\n\ntalos server help for usage", args[0])
	}
}

func listDaemonSessions(cfg *config.Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := dialDaemon(ctx, cfg)
	if err != nil {
		return err
	}
	raw, err := conn.Request(ctx, rpc.DaemonListSessions, nil)
	if err != nil {
		return err
	}
	var listed rpc.ListSessionsResult
	if err := json.Unmarshal(raw, &listed); err != nil {
		return err
	}
	if len(listed.Sessions) == 0 {
		fmt.Fprintln(os.Stderr, "no sessions")
		return nil
	}
	fmt.Fprintln(os.Stderr, "Sessions:")
	for _, s := range listed.Sessions {
		live := "unloaded"
		if s.Live {
			live = s.State
		}
		fmt.Fprintf(os.Stderr, "  %s  %s  %s  [%s/%s]\n", s.ID, live, s.Dir, s.Provider, s.Model)
	}
	return nil
}

func dialDaemon(ctx context.Context, cfg *config.Config) (*client.ClientConn, <-chan protocol.Event, error) {
	disc, err := server.ReadDiscovery(server.DiscoveryPath(cfg.BaseDir))
	if err != nil {
		return nil, nil, fmt.Errorf("no daemon — start with 'talos serve': %w", err)
	}
	token := disc.Token
	if cfg.ServerToken != "" {
		token = cfg.ServerToken
	}
	return client.RunClientNetwork(ctx, "unix", disc.Socket, token)
}

func runAttach(ctx context.Context, cfg *config.Config, sessionID string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	clientConn, events, err := dialDaemon(ctx, cfg)
	if err != nil {
		return err
	}

	if sessionID == "" {
		sessionID, err = pickDaemonSession(ctx, clientConn)
		if err != nil {
			return err
		}
	}
	clientConn.Session = sessionID
	if err := clientConn.Subscribe(sessionID); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	prices := pricing.Load(cfg.BaseDir)
	eng := client.NewRemoteEngine(clientConn, events)
	eng.Session = sessionID
	initialHistory, _ := eng.History()
	inputTokens, outputTokens, cacheMiss, seedCost, _ := eng.Stats()

	provider, model := cfg.Provider, cfg.Model
	if listed, err := listSessionsRPC(ctx, clientConn); err == nil {
		for _, s := range listed {
			if s.ID == sessionID {
				if s.Provider != "" {
					provider = s.Provider
				}
				if s.Model != "" {
					model = s.Model
				}
				break
			}
		}
	}

	initialCfg := tui.Config{
		SessionID:      sessionID,
		Mode:           tui.ModeSingleAgent,
		InitialHistory: initialHistory,
		Engine:         eng,
		Shutdown:       cancel,
		Provider:       provider,
		Model:          model,
		Pricing:        prices,
		SeedStats: struct {
			InputTokens  int
			OutputTokens int
			CacheMiss    int
			Cost         float64
		}{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			CacheMiss:    cacheMiss,
			Cost:         seedCost,
		},
	}

	return tui.RunTabs(ctx, initialCfg, events, attachNewTab(ctx, cfg, sessionID, prices))
}

func listSessionsRPC(ctx context.Context, conn *client.ClientConn) ([]rpc.SessionInfo, error) {
	raw, err := conn.Request(ctx, rpc.DaemonListSessions, nil)
	if err != nil {
		return nil, err
	}
	var listed rpc.ListSessionsResult
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, err
	}
	return listed.Sessions, nil
}

func pickDaemonSession(ctx context.Context, conn *client.ClientConn) (string, error) {
	sessions, err := listSessionsRPC(ctx, conn)
	if err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		resp, err := conn.Request(ctx, rpc.DaemonCreateSession, rpc.CreateSessionParams{Dir: cwd})
		if err != nil {
			return "", fmt.Errorf("create session: %w", err)
		}
		var created rpc.CreateSessionResult
		if err := json.Unmarshal(resp, &created); err != nil {
			return "", err
		}
		fmt.Fprintf(os.Stderr, "[created session %s for %s]\n", created.Session.ID, cwd)
		return created.Session.ID, nil
	}
	if len(sessions) == 1 {
		return sessions[0].ID, nil
	}
	fmt.Fprintln(os.Stderr, "Sessions:")
	for i, s := range sessions {
		live := "unloaded"
		if s.Live {
			live = s.State
		}
		fmt.Fprintf(os.Stderr, "  %d: %s  %s  %s\n", i+1, s.ID, live, s.Dir)
	}
	fmt.Fprint(os.Stderr, "Pick a session [1]: ")
	var choice int
	if _, err := fmt.Scanln(&choice); err != nil || choice < 1 || choice > len(sessions) {
		choice = 1
	}
	return sessions[choice-1].ID, nil
}

// attachNewTab opens a fresh daemon connection subscribed to the same session.
func attachNewTab(ctx context.Context, cfg *config.Config, sessionID string, prices *pricing.Table) tui.NewTabFunc {
	return func(tabCtx context.Context, tabID int) (tui.Config, <-chan protocol.Event, func(), error) {
		tabCtx, cancel := context.WithCancel(tabCtx)
		conn, evs, err := dialDaemon(tabCtx, cfg)
		if err != nil {
			cancel()
			return tui.Config{}, nil, nil, err
		}
		conn.Session = sessionID
		if err := conn.Subscribe(sessionID); err != nil {
			cancel()
			return tui.Config{}, nil, nil, err
		}
		eng := client.NewRemoteEngine(conn, evs)
		eng.Session = sessionID
		history, _ := eng.History()
		in, out, miss, cost, _ := eng.Stats()
		return tui.Config{
			SessionID:      sessionID,
			Mode:           tui.ModeSingleAgent,
			Engine:         eng,
			Shutdown:       cancel,
			Provider:       cfg.Provider,
			Model:          cfg.Model,
			Pricing:        prices,
			InitialHistory: history,
			SeedStats: struct {
				InputTokens  int
				OutputTokens int
				CacheMiss    int
				Cost         float64
			}{
				InputTokens:  in,
				OutputTokens: out,
				CacheMiss:    miss,
				Cost:         cost,
			},
		}, evs, cancel, nil
	}
}

// pickSession lists this project's sessions and lets the user choose one,
// falling back to the latest on empty input.
func pickSession(cwd string) (session.Session, error) {
	sessions, err := session.ListSessions(cwd)
	if err != nil || len(sessions) == 0 {
		return session.LatestSession(cwd)
	}
	fmt.Fprintln(os.Stderr, "Sessions for this project:")
	for i, s := range sessions {
		fmt.Fprintf(os.Stderr, "  %d: %s\n", i+1, s.ID)
	}
	fmt.Fprint(os.Stderr, "Pick a session [1, default=latest]: ")
	var choice int
	if _, err := fmt.Scanln(&choice); err != nil || choice < 1 || choice > len(sessions) {
		return session.LatestSession(cwd)
	}
	return sessions[choice-1], nil
}

func findWebDist() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	candidates := []string{
		filepath.Join(dir, "app", "out", "renderer"),
		filepath.Join(dir, "..", "app", "out", "renderer"),
		filepath.Join(dir, "..", "..", "app", "out", "renderer"),
		filepath.Join(dir, "app", "renderer", "dist"),
		filepath.Join(dir, "..", "app", "renderer", "dist"),
		filepath.Join(dir, "..", "..", "app", "renderer", "dist"),
	}
	for _, c := range candidates {
		if info, err := os.Stat(filepath.Join(c, "index.html")); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

func runDream(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("dream", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "show curation report without writing")
	model := fs.String("model", "", "curation model override")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	repoRoot := engine.FindRepoRoot(cwd)
	store, err := memory.Open(cfg.BaseDir, memory.ProjectID(repoRoot))
	if err != nil {
		return err
	}
	entries := store.All()
	var downgraded int
	for _, e := range entries {
		if !engine.MemoryReferencesMissingPath(repoRoot, e) {
			continue
		}
		downgraded++
		if !*dryRun {
			id := e.ID
			_ = store.Update(id, func(en *memory.Entry) {
				if en.Importance > 0.2 {
					en.Importance = 0.2
				}
			})
		}
	}

	// Phase 1-b: flag contradicting entries (same category, opposite polarity).
	var contradictingPairs [][2]string
	for i, a := range entries {
		for j := i + 1; j < len(entries); j++ {
			if memory.HasContradictingEntries(a, entries[j]) {
				contradictingPairs = append(contradictingPairs, [2]string{a.ID, entries[j].ID})
				if !*dryRun {
					// Downgrade both so the LLM pass can resolve them.
					_ = store.Update(a.ID, func(en *memory.Entry) {
						if en.Importance > 0.4 {
							en.Importance = 0.4
						}
					})
					_ = store.Update(entries[j].ID, func(en *memory.Entry) {
						if en.Importance > 0.4 {
							en.Importance = 0.4
						}
					})
				}
			}
		}
	}
	var deleted int
	prov, modelName, err := engine.DreamerProvider(cfg, *model)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[dream] curation skipped: %v\n", err)
	} else if modelName != "" {
		decisions, err := dreamDecisions(prov, modelName, entries, repoRoot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[dream] curation skipped: %v\n", err)
		} else {
			for _, d := range decisions {
				switch d.Action {
				case "delete":
					deleted++
					if !*dryRun {
						_ = store.Delete(d.ID)
					}
				case "keep":
					if !*dryRun {
						_ = store.Update(d.ID, func(e *memory.Entry) {
							if d.Importance > 0 {
								e.Importance = d.Importance
							}
							if d.Text != "" {
								e.Text = d.Text
							}
						})
					}
				case "merge":
					deleted++
					if !*dryRun {
						_ = store.Delete(d.ID)
					}
				}
			}
		}
	}
	if !*dryRun {
		if err := store.Compact(); err != nil {
			return err
		}
		_ = store.ResetCompactions()
	}
	mode := "applied"
	if *dryRun {
		mode = "dry-run"
	}
	contradictions := len(contradictingPairs)
	if contradictions > 0 {
		fmt.Fprintf(os.Stdout, "dream %s: kept=%d downgraded=%d deleted=%d contradictions=%d\n", mode, len(entries)-deleted, downgraded, deleted, contradictions)
	} else {
		fmt.Fprintf(os.Stdout, "dream %s: kept=%d downgraded=%d deleted=%d\n", mode, len(entries)-deleted, downgraded, deleted)
	}
	return nil
}

type dreamDecision struct {
	ID         string  `json:"id"`
	Action     string  `json:"action"`
	MergeInto  string  `json:"merge_into"`
	Importance float64 `json:"importance"`
	Text       string  `json:"text"`
}

func dreamDecisions(prov provider.Provider, model string, entries []memory.Entry, repoRoot string) ([]dreamDecision, error) {
	data, _ := json.Marshal(entries)
	msg := protocol.TextMessage(protocol.RoleUser, "Project root: "+repoRoot+"\nMemories JSON:\n"+string(data))
	rawMsg, _ := json.Marshal(msg)
	req := protocol.Request{
		System: "Curate project memories. Return only a JSON array of {id, action, merge_into, importance, text}. action is keep, merge, or delete. Delete stale/wrong duplicates; merge near-duplicates by marking the duplicate action merge with merge_into set. Keep text concise.",
		Messages: []protocol.FrozenMessage{{
			Msg: msg,
			Raw: rawMsg,
		}},
		Model: model,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	stream, err := prov.StreamTurn(ctx, req)
	if err != nil {
		return nil, err
	}
	var out string
	for ev := range stream {
		switch e := ev.(type) {
		case protocol.PEText:
			out += e.Text
		case protocol.PEError:
			return nil, e.Err
		}
	}
	var decisions []dreamDecision
	if err := jsonutil.UnmarshalArrayFromText(out, &decisions); err != nil {
		return nil, err
	}
	return decisions, nil
}
