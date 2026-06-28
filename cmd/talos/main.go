package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mintoleda/talos/internal/agents"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/executor"
	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/memory"
	"github.com/mintoleda/talos/internal/pricing"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/provider/anthropic"
	"github.com/mintoleda/talos/internal/provider/openai"
	"github.com/mintoleda/talos/internal/safety"
	"github.com/mintoleda/talos/internal/server"
	"github.com/mintoleda/talos/internal/session"
	"github.com/mintoleda/talos/internal/skills"
	"github.com/mintoleda/talos/internal/tools"
	"github.com/mintoleda/talos/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)


// Flags holds the parsed CLI flags. Zero values are the defaults.
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
	var (
		f          Flags
		serverMode bool
	)
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
	flag.BoolVar(&serverMode, "server", false, "run as a long-lived daemon")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Override(f.BaseURL, f.Model, "")
	cfg.OverrideProvider(f.Provider)

	if len(flag.Args()) > 0 && flag.Args()[0] == "server" {
		if len(flag.Args()) == 1 || (len(flag.Args()) >= 2 && flag.Args()[1] == "start") {
			// talos server [start] — start a new server daemon.
			serverMode = true
		} else if len(flag.Args()) >= 2 && flag.Args()[1] == "help" {
			return fmt.Errorf(`talos server commands:
  talos server            Start a new server daemon (default)
  talos server list       List running servers
  talos server kill <id>  Kill a specific server
  talos server kill-all   Kill all running servers
  talos server help       Show this help`)
		} else {
			switch flag.Args()[1] {
			case "list":
				ids, err := server.ListRunning(cfg.BaseDir)
				if err != nil {
					return fmt.Errorf("list servers: %w", err)
				}
				if len(ids) == 0 {
					fmt.Fprintln(os.Stderr, "no running servers")
					return nil
				}
				fmt.Fprintln(os.Stderr, "Running servers:")
				for _, id := range ids {
					pid := server.ReadPID(filepath.Join(cfg.BaseDir, "server", id+".pid"))
					fmt.Fprintf(os.Stderr, "  %s (pid %d)\n", id, pid)
				}
				return nil
			case "kill":
				if len(flag.Args()) < 3 {
					return fmt.Errorf("usage: talos server kill <session-id>")
				}
				if err := server.Kill(cfg.BaseDir, flag.Args()[2]); err != nil {
					return fmt.Errorf("kill: %w", err)
				}
				fmt.Fprintf(os.Stderr, "killed server %s\n", flag.Args()[2])
				return nil
			case "kill-all":
				ids, err := server.ListRunning(cfg.BaseDir)
				if err != nil {
					return fmt.Errorf("list servers: %w", err)
				}
				if len(ids) == 0 {
					fmt.Fprintln(os.Stderr, "no running servers")
					return nil
				}
				for _, id := range ids {
					if err := server.Kill(cfg.BaseDir, id); err != nil {
						fmt.Fprintf(os.Stderr, "kill %s: %v\n", id, err)
					} else {
						fmt.Fprintf(os.Stderr, "killed %s\n", id)
					}
				}
				return nil
			default:
				return fmt.Errorf("unknown server command: %s\n\ntalos server help for usage", flag.Args()[1])
			}
		}
	}

	if len(flag.Args()) > 0 && flag.Args()[0] == "attach" {
		sid := ""
		if len(flag.Args()) > 1 {
			sid = flag.Args()[1]
		}
		if sid == "" {
			var err error
			sid, err = pickRunningServer(cfg.BaseDir)
			if err != nil {
				return err
			}
		}
		return runAttach(context.Background(), cfg, sid)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	var tx *session.Transcript
	var sess session.Session
	switch {
	case f.SessionID != "":
		sess, err = session.OpenSession(cwd, f.SessionID)
	case f.Resume:
		sess, err = pickSession(cwd)
	case f.Continue:
		sess, err = session.LatestSession(cwd)
	default:
		sess = session.NewSession(cwd)
	}
	if err != nil {
		sess = session.NewSession(cwd)
	}
	if _, statErr := os.Stat(sess.Path); statErr == nil {
		tx, err = session.Load(sess.Path)
	} else {
		tx, err = session.Create(sess.Path)
	}
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	defer tx.Close()

	repoRoot := findRepoRoot(cwd)

	// Load project-level system prompt from SYSTEM_PROMPT.md in the repo root.
	// If present, it takes precedence over the config file's system_prompt.
	if sp, err := config.LoadProjectSystemPrompt(repoRoot); err != nil {
		fmt.Fprintf(os.Stderr, "[warning] reading SYSTEM_PROMPT.md: %v\n", err)
	} else if sp != "" {
		cfg.SystemPrompt = sp
	}

	// Scan skills directories (global + project-local) and append a compact
	// listing to the system prompt so the LLM knows what's available.
	skillsDir := filepath.Join(cfg.BaseDir, "skills")               // ~/.talos/skills/
	projectSkillsDir := filepath.Join(repoRoot, ".talos", "skills") // .talos/skills/
	allSkills, err := skills.Scan([]skills.Dir{
		{Path: skillsDir, Label: "global"},
		{Path: projectSkillsDir, Label: "project"},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[warning] scanning skills: %v\n", err)
	}
	if listing := skills.RenderListing(allSkills); listing != "" {
		cfg.SystemPrompt += listing
	}

	// Inject persistent memory into the system prompt so it's available from turn one.
	if mem, err := memory.Load(cfg.BaseDir); err != nil {
		fmt.Fprintf(os.Stderr, "[warning] loading memory: %v\n", err)
	} else if mem != "" {
		cfg.SystemPrompt += "\n\n## Persistent Memory\n\nThe following was remembered from previous sessions:\n\n" + mem
	}

	cp := safety.NewCheckpointer(repoRoot)

	// Restore the read history from disk if this is a resumed session, then
	// re-enable persistence so every fresh read keeps the on-disk set current.
	// A missing file is fine; that just means no reads have been recorded yet.
	reads, readErr := tools.LoadReadSet(sess.Path + ".reads.json")
	if readErr != nil {
		fmt.Fprintf(os.Stderr, "[warning] loading read set: %v\n", readErr)
		reads = tools.NewReadSet()
	}
	reads.SetSavePath(sess.Path + ".reads.json")
	var (
		reg          *tools.Registry
		agentBuilder *agents.Builder
	)
	if f.NoTools {
		reg = tools.EmptyRegistry()
	} else {
		reg = tools.DefaultRegistry(cwd, reads, tools.BashConfig{
			DefaultTimeout: cfg.BashTimeout,
			MaxTimeout:     cfg.BashMaxTimeout,
			MaxOutput:      cfg.BashMaxOutput,
		}, cfg.SearchURL)
		reg.Add(tools.NewSkillTool([]string{
			filepath.Join(cfg.BaseDir, "skills"),
			filepath.Join(repoRoot, ".talos", "skills"),
		}))

		// Subagents: load markdown-defined agent loadouts (builtin + user dirs)
		// and give the primary agent a spawn tool for every loaded definition.
		// The listing is appended to the system prompt so the model knows to delegate.
		agentDirs := []agents.Dir{
			{Path: filepath.Join(cfg.BaseDir, "subagents"), Label: "global"},
			{Path: filepath.Join(repoRoot, ".talos", "subagents"), Label: "project"},
		}
		if defs, err := agents.Load(agentDirs); err != nil {
			fmt.Fprintf(os.Stderr, "[warning] loading agents: %v\n", err)
		} else if len(defs) > 0 {
			agentBuilder = agents.NewBuilder(agents.Config{
				Provider:       cfg.Provider,
				BaseURL:        cfg.BaseURL,
				APIKey:         cfg.APIKey,
				Model:          cfg.Model,
				ThinkingLevel:  cfg.ThinkingLevel,
				MaxAgentDepth:  cfg.MaxAgentDepth,
				Mode:           cfg.PermissionMode,
				BashTimeout:    cfg.BashTimeout,
				BashMaxTimeout: cfg.BashMaxTimeout,
				BashMaxOutput:  cfg.BashMaxOutput,
				SearchURL:      cfg.SearchURL,
				Cwd:            cwd,
				BaseDir:        cfg.BaseDir,
			}, defs)
			allAgents := make([]string, 0, len(defs))
			for name := range defs {
				allAgents = append(allAgents, name)
			}
			sort.Strings(allAgents)
			reg.Add(agentBuilder.SpawnTools(allAgents)...)
			cfg.SystemPrompt += agents.RenderListing(defs, allAgents)
		}
		reg.Add(tools.NewMemoryWrite(cfg.BaseDir))
	}

	prov, compactor, err := newProvider(cfg, f.NoTools)
	if err != nil {
		return fmt.Errorf("provider: %w", err)
	}

	pol := safety.NewPolicy(cfg.PermissionMode, cwd, safety.NewClassifier(), f.Print == "")
	exec := executor.New(reg, pol)
	prices := pricing.Load(cfg.BaseDir)
	pb := loop.NewPromptBuilder(cfg.SystemPrompt, reg.Schemas(), cfg.Model)
	pb.SetThinkingLevel(cfg.ThinkingLevel)
	pb.SetContextLimit(prices.ContextWindow(cfg.Model))
	// Inject a per-turn reminder listing files the model has actually opened
	// in this session. Goes into the last user message so it does not break
	// the cacheable prefix (system + tools + messages[:-1]).
	pb.SetContextFn(func() string {
		total := reads.Len()
		if total == 0 {
			return ""
		}
		recent := reads.RecentPaths(8)
		var b strings.Builder
		b.WriteString(fmt.Sprintf("<system-reminder>\n%d file(s) read this session. The read-before-edit and read-before-write rules apply to every file you mutate — call read first if the file is not in this list (or has changed since you last read it).\n\nMost recent reads:\n", total))
		for _, p := range recent {
			b.WriteString("- ")
			b.WriteString(p)
			b.WriteByte('\n')
		}
		b.WriteString("</system-reminder>")
		return b.String()
	})
	lp := loop.New(prov, exec, tx, pb)
	lp.SetCompactor(compactor)
	lp.DebugCache = f.DebugCache
	defer lp.Close()

	a := &app{cfg: cfg, lp: lp, pb: pb, cwd: cwd, noTools: f.NoTools}

	fmt.Fprintf(os.Stderr, "[session %s] [%s/%s]\n", sess.ID, cfg.Provider, cfg.Model)

	switch {
	case serverMode:
		return runServer(context.Background(), cfg, a, lp, cp, sess.ID)

	case f.Print != "":
		// One-shot mode: run a single prompt and exit.
		_, _ = cp.Snapshot("before-run")
		return lp.RunTurn(context.Background(), protocol.TextBlocks(f.Print), renderTo(os.Stdout, f.DebugCache))

	default:
		// Default: full-screen TUI.
		return tui.Run(context.Background(), lp, cp, sess.ID,
			cfg.Provider, cfg.Model,
			func() (string, error) {
				ntx, id, err := a.newSession()
				if err != nil {
					return "", err
				}
				lp.SetTranscript(ntx)
				fmt.Fprintf(os.Stderr, "[started new session %s]\n", id)
				return id, nil
			},
			func() string {
				s := lp.Stats()
				if s.Calls == 0 {
					return "[stats] no API calls yet"
				}
				return fmt.Sprintf("[stats] calls=%d | input=%d | output=%d | cached=%d (%.1f%%)",
					s.Calls, s.InputTokens, s.OutputTokens, s.CachedTokens, s.CacheHitRate()*100)
			},
			func(id string) (string, []protocol.FrozenMessage, error) {
				tx, sid, err := a.resumeSession(id)
				if err != nil {
					return "", nil, err
				}
				lp.SetTranscript(tx)
				fmt.Fprintf(os.Stderr, "[resumed session %s]\n", sid)
				return sid, tx.Frozen(), nil
			},
			a.switchProvider,
			func() string {
				caps := provider.SupportedLevels(pb.Model())
				cur := pb.ThinkingLevel()
				if cur == "" {
					cur = caps[0]
				}
				for i, l := range caps {
					if l == cur {
						next := caps[(i+1)%len(caps)]
						pb.SetThinkingLevel(next)
						// Persist the new level so it survives a restart.
						if err := config.SaveThinkingLevel(cfg.BaseDir, next); err != nil {
							fmt.Fprintf(os.Stderr, "[warning] save thinking level: %v\n", err)
						}
						return next
					}
				}
				pb.SetThinkingLevel(caps[0])
				_ = config.SaveThinkingLevel(cfg.BaseDir, caps[0])
				return caps[0]
			},
			func() string { return pb.ThinkingLevel() },
			func(id string) error {
				return session.DeleteSession(a.cwd, id)
			},
			a.fetchSessions,
			a.fetchModels,
			a.loginProviders,
			a.saveLogin,
			func(id string) {
				if agentBuilder != nil {
					agentBuilder.CancelSubagent(id)
				}
			},
			prices,
			tx.Frozen(),
		)
	}
}

// renderTo returns an EmitFunc that writes events to out in a human-readable
// format suitable for one-shot (-p) mode.
func renderTo(out *os.File, debugCache bool) protocol.EmitFunc {
	return func(ev protocol.Event) {
		switch e := ev.(type) {
		case protocol.TextDelta:
			fmt.Fprint(out, e.Text)
		case protocol.ToolStarted:
			fmt.Fprintf(out, "\n▶ %s\n", e.Name)
		case protocol.ToolFinished:
			mark := "✓"
			if e.Result.IsError {
				mark = "✗"
			}
			text := e.Result.Content
			if len(text) > 400 {
				text = text[:200] + "\n[...]\n" + text[len(text)-200:]
			}
			fmt.Fprintf(out, "\n%s %s\n%s\n", mark, e.Result.ToolUseID, text)
		case protocol.Notice:
			fmt.Fprintf(out, "\n[%s] %s\n", e.Level, e.Text)
		case protocol.TurnEnded:
			extra := ""
			if e.Usage.CachedPromptTokens > 0 {
				extra = fmt.Sprintf(" | cached=%d", e.Usage.CachedPromptTokens)
			}
			fmt.Fprintf(out, "\n[turn ended: %s | prompt=%d completion=%d%s]\n",
				e.StopReason, e.Usage.PromptTokens, e.Usage.CompletionTokens, extra)
		case protocol.PermissionRequested:
			fmt.Fprintf(out, "\n⚠ %s requires approval: %s\nAllow? [y/N] ", e.ToolName, e.Reason)
			var ans string
			fmt.Scanln(&ans)
			if e.ReplyCh != nil {
				e.ReplyCh <- ans == "y" || ans == "Y"
			}
		case protocol.SubagentStarted:
			fmt.Fprintf(out, "\n  ⊳ %s: %s\n", e.Agent, e.Task)
		case protocol.SubagentEvent:
			switch ie := e.Inner.(type) {
			case protocol.ToolStarted:
				fmt.Fprintf(out, "    [%s] ▶ %s\n", e.Agent, ie.Name)
			case protocol.ToolFinished:
				mark := "✓"
				if ie.Result.IsError {
					mark = "✗"
				}
				fmt.Fprintf(out, "    [%s] %s\n", e.Agent, mark)
			}
		case protocol.SubagentFinished:
			fmt.Fprintf(out, "  ⊲ %s done (in=%d out=%d $%.4f)\n",
				e.Agent, e.Usage.InputTokens, e.Usage.OutputTokens, e.Usage.Cost)
		default:
			// Unknown event type: ignore safely so new events don't break
			// older frontends.
		}
	}
}

// newProvider creates an LLM provider and compactor from the given config.
// Extracted so the switchProvider closure can re-create them at runtime.
func newProvider(cfg *config.Config, noTools bool) (provider.Provider, *session.Compactor, error) {
	var prov provider.Provider
	switch cfg.Provider {
	case "anthropic":
		base := cleanBaseURL(cfg.BaseURL)
		if base == "" || base == "https://api.deepseek.com" {
			base = "https://api.anthropic.com"
		}
		prov = anthropic.New(base, cfg.APIKey, anthropic.Config{
			MaxTokens:     8192,
			ThinkingLevel: cfg.ThinkingLevel,
		})
	default:
		// For all OpenAI-compatible providers, look up the canonical base URL
		// from the known-provider registry so switchProvider works correctly
		// after a model picker selection.
		base := cleanBaseURL(cfg.BaseURL)
		if base == "" {
			aliases := map[string]string{"go": "opencode-go", "zen": "opencode-zen", "opencode": "opencode-zen"}
			name := cfg.Provider
			if a, ok := aliases[name]; ok {
				name = a
			}
			if kp, ok := provider.ByName(name); ok {
				base = kp.BaseURL
			}
		}
		prov = openai.New(base, cfg.APIKey)
	}

	compactor := session.NewCompactor(session.NewLLMSummarizer(prov, cfg.Model, ""))
	return prov, compactor, nil
}

// cleanBaseURL strips trailing /v1 (and /v1/) suffixes from a provider base URL.
// The anthropic and openai clients already append their own /v1/<endpoint> paths,
// so a user-provided URL like https://api.openai.com/v1 would produce double /v1
// segments (e.g. /v1/v1/chat/completions).
func cleanBaseURL(raw string) string {
	s := strings.TrimRight(raw, "/")
	if strings.HasSuffix(s, "/v1") {
		s = s[:len(s)-3]
	}
	return s
}

func runServer(ctx context.Context, cfg *config.Config, a *app, lp *loop.Loop, cp *safety.Checkpointer, sessionID string) error {
	engine := server.NewLoopEngine(ctx, lp, cp, sessionID)
	engine.SetSlashHandler(func(cmd string, emit func(protocol.Event)) string {
		parts := strings.Fields(cmd)
		if len(parts) == 0 {
			return "empty command"
		}
		switch parts[0] {
		case "/thinking":
			old := a.pb.ThinkingLevel()
			caps := provider.SupportedLevels(a.cfg.Model)
			cur := old
			if cur == "" {
				cur = caps[0]
			}
			for i, l := range caps {
				if l == cur {
					next := caps[(i+1)%len(caps)]
					a.pb.SetThinkingLevel(next)
					_ = config.SaveThinkingLevel(a.cfg.BaseDir, next)
					emit(protocol.ModelChanged{
						Provider:      a.cfg.Provider,
						Model:         a.cfg.Model,
						ThinkingLevel: next,
					})
					return fmt.Sprintf("thinking level: %s → %s", cur, next)
				}
			}
			return fmt.Sprintf("thinking level: %s", cur)
		case "/model":
			if len(parts) >= 2 {
				// /model <provider/model> — switch directly.
				arg := parts[1]
				parts := strings.SplitN(arg, "/", 2)
				var pName, pModel string
				if len(parts) == 2 {
					pName = parts[0]
					pModel = parts[1]
				} else {
					pModel = arg
					pName = a.cfg.Provider
				}
				if err := a.switchProvider(pName, pModel); err != nil {
					return fmt.Sprintf("switch model: %v", err)
				}
				emit(protocol.ModelChanged{
					Provider:      a.cfg.Provider,
					Model:         a.cfg.Model,
					ThinkingLevel: a.pb.ThinkingLevel(),
				})
				return fmt.Sprintf("switched to %s/%s", pName, pModel)
			}
			// /model with no args — fetch and display available models.
			entries, err := a.fetchModels()
			if err != nil {
				return fmt.Sprintf("fetch models: %v", err)
			}
			if len(entries) == 0 {
				return "no models available"
			}
			var b strings.Builder
			b.WriteString("Available models:")
			for _, e := range entries {
				fmt.Fprintf(&b, "\n  %s/%s", e.Provider, e.ID)
			}
			b.WriteString("\n\nUse /model <provider/model> to switch.")
			return b.String()
		default:
			return fmt.Sprintf("unknown command: %s", parts[0])
		}
	})
	srv := server.New(engine, server.SocketPath(cfg.BaseDir, sessionID), server.PidFile(cfg.BaseDir, sessionID), 0)
	fmt.Fprintf(os.Stderr, "[server running for session %s]\n", sessionID)
	return srv.Start(ctx)
}

func runAttach(ctx context.Context, cfg *config.Config, sessionID string) error {
	client, events, err := server.RunClient(ctx, server.SocketPath(cfg.BaseDir, sessionID))
	if err != nil {
		return err
	}

	prices := pricing.Load(cfg.BaseDir)

	m := tui.NewModel(tui.Config{
		SessionID: sessionID,
		Mode:      tui.ModeSingleAgent,
		SubmitFn: func(text string) {
			_ = client.Send(text)
		},
		SubmitSlash: func(cmd string) {
			_ = client.Send(cmd)
		},
		InterruptFn: func() {
			_ = client.Interrupt()
		},
		Provider: cfg.Provider,
		Model:    cfg.Model,
		Pricing:  prices,
		Stats: func() string {
			return "[stats: attached to server — run /stats on the server terminal]"
		},
	})
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	// Forward server events to the Bubble Tea program.
	go func() {
		for ev := range events {
			p.Send(tui.EventMsg{E: ev})
		}
	}()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
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

// pickRunningServer returns the session ID of a running talos server.
// If exactly one server is alive it returns it immediately.
// If multiple are running it prompts the user to choose.
func pickRunningServer(baseDir string) (string, error) {
	ids, err := server.ListRunning(baseDir)
	if err != nil {
		return "", fmt.Errorf("list servers: %w", err)
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("no running servers — start one with 'talos --server'")
	case 1:
		return ids[0], nil
	default:
		fmt.Fprintln(os.Stderr, "Running servers:")
		for i, id := range ids {
			fmt.Fprintf(os.Stderr, "  %d: %s\n", i+1, id)
		}
		fmt.Fprint(os.Stderr, "Pick a server [1]: ")
		var choice int
		if _, err := fmt.Scanln(&choice); err != nil || choice < 1 || choice > len(ids) {
			choice = 1
		}
		return ids[choice-1], nil
	}
}

func findRepoRoot(dir string) string {
	for d := dir; d != "/" && d != "."; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
	}
	return dir
}

func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
