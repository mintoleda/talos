package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/mintoleda/talos/internal/agents"
	"github.com/mintoleda/talos/internal/client"
	"github.com/mintoleda/talos/internal/client/rpc"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/executor"
	"github.com/mintoleda/talos/internal/jsonutil"
	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/mcp"
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

	if len(flag.Args()) > 0 && flag.Args()[0] == "dream" {
		return runDream(cfg, flag.Args()[1:])
	}

	if len(flag.Args()) > 0 && flag.Args()[0] == "server" {
		if len(flag.Args()) == 1 || (len(flag.Args()) >= 2 && flag.Args()[1] == "start") {
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
	if n := tx.RepairCount(); n > 0 {
		fmt.Fprintf(os.Stderr, "[notice] repaired session: removed %d orphaned tool call(s)\n", n)
	}
	defer tx.Close()

	repoRoot := findRepoRoot(cwd)

	if sp, err := config.LoadProjectSystemPrompt(repoRoot); err != nil {
		fmt.Fprintf(os.Stderr, "[warning] reading SYSTEM_PROMPT.md: %v\n", err)
	} else if sp != "" {
		cfg.SystemPrompt = sp
	}

	skillsDir := filepath.Join(cfg.BaseDir, "skills")
	projectSkillsDir := filepath.Join(repoRoot, ".talos", "skills")
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

	projectID := memory.ProjectID(repoRoot)
	memStore, err := memory.Open(cfg.BaseDir, projectID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[warning] loading memory: %v\n", err)
	}
	if memStore != nil {
		cfg.SystemPrompt += memory.Render(memStore.TopN(30, 4096))

		// Check if compaction count is high enough to warrant a dream nudge.
		if memStore.CompactionNudgeNeeded(50) {
			cfg.SystemPrompt += "\n\n[note: " + fmt.Sprint(memStore.CompactCount()) + " compactions since last dream — run 'talos dream' to curate project memories]"
		}
	}

	// Start a background verifier that re-checks high-importance agent-written
	// entries across restarts. It runs asynchronously so startup is not blocked.
	if memStore != nil && !f.NoTools {
		verifier := memory.NewVerifier(memory.StorePath(cfg.BaseDir, projectID))
		go func() {
			entries := memStore.All()
			var toFlag []string
			for _, e := range entries {
				if e.Source != "agent" || e.Importance < 0.7 {
					continue
				}
				if memoryReferencesMissingPath(repoRoot, e) {
					toFlag = append(toFlag, e.ID)
				}
			}
			for _, id := range toFlag {
				n := verifier.Flag(id)
				if n >= 3 {
					// 3 strikes: permanently downgrade.
					_ = memStore.Update(id, func(en *memory.Entry) {
						if en.Importance > 0.3 {
							en.Importance = 0.3
						}
					})
				}
			}
		}()
	}

	cp := safety.NewCheckpointer(repoRoot)

	reads, readErr := tools.LoadReadSet(sess.Path + ".reads.json")
	if readErr != nil {
		fmt.Fprintf(os.Stderr, "[warning] loading read set: %v\n", readErr)
		reads = tools.NewReadSet()
	}
	reads.SetSavePath(sess.Path + ".reads.json")

	mcpManager, mcpErrs := mcp.NewManager(context.Background(), cfg.MCPServers)
	defer mcpManager.Close()
	for _, e := range mcpErrs {
		fmt.Fprintf(os.Stderr, "[mcp] %v\n", e)
	}

	var (
		reg             *tools.Registry
		agentBuilder    *agents.Builder
		subagentListing string
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

		if cfg.EnableSubagents {
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
				subagentListing = agents.RenderListing(defs, allAgents)
				cfg.SystemPrompt += subagentListing
			}
		}
		if memStore != nil {
			reg.Add(tools.NewMemoryWrite(memStore), tools.NewMemorySearch(memStore), tools.NewMemoryDelete(memStore), tools.NewMemoryUpdate(memStore))
		}
		reg.Add(mcpManager.Tools()...)
	}

	prov, compactor, err := newProvider(cfg, f.NoTools)
	if err != nil {
		return fmt.Errorf("provider: %w", err)
	}
	if compactor != nil && cfg.Historian && memStore != nil {
		hist, err := newHistorian(cfg, memStore)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[warning] historian disabled: %v\n", err)
		} else {
			compactor.Historian = hist
		}
	}
	if compactor != nil && memStore != nil {
		compactor.OnCompaction = func() {
			_ = memStore.IncrementCompactions()
		}
	}

	pol := safety.NewPolicy(cfg.PermissionMode, cwd, safety.NewClassifier(), f.Print == "")
	exec := executor.New(reg, pol)
	prices := pricing.Load(cfg.BaseDir)
	pb := loop.NewPromptBuilder(cfg.SystemPrompt, reg.Schemas(), cfg.Model)
	pb.SetThinkingLevel(cfg.ThinkingLevel)
	pb.SetContextLimit(prices.ContextWindow(cfg.Model))

	// Wire subagent data into the prompt builder so subagent listing and
	// spawn-tool schemas can be toggled at runtime without losing them from
	// the registry.
	if agentBuilder != nil {
		subagentToolNames := make(map[string]bool)
		for _, n := range agentBuilder.SubagentToolNames() {
			subagentToolNames[n] = true
		}
		pb.SetSubagentData(subagentListing, subagentToolNames)
	}
	// Inject a per-turn reminder listing files the model has actually opened
	// in this session. Surfaced via Request.Volatile so it does not break
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
	lp.KillBgOnInterrupt = cfg.KillBgOnInterrupt
	defer lp.Close()

	a := &app{
		cfg:          cfg,
		lp:           lp,
		pb:           pb,
		prov:         prov,
		exec:         exec,
		agentBuilder: agentBuilder,
		mcpManager:   mcpManager,
		memStore:     memStore,
		cwd:          cwd,
		noTools:      f.NoTools,
	}

	fmt.Fprintf(os.Stderr, "[session %s] [%s/%s]\n", sess.ID, cfg.Provider, cfg.Model)

	switch {
	case serverMode:
		return runServer(context.Background(), cfg, a, lp, cp, sess.ID)

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
		_, _ = cp.Snapshot("before-run")
		err := lp.RunTurn(ctx, protocol.TextBlocks(f.Print), renderTo(os.Stdout, f.DebugCache))
		signal.Reset(syscall.SIGINT, syscall.SIGTERM)
		cancel()

		// Ephemeral session: -p runs must not leave traces on disk.
		// Remove the transcript and reads files so the session does not
		// show up in /resume or the resume picker.
		os.Remove(sess.Path)
		os.Remove(sess.Path + ".reads.json")
		if parent := filepath.Dir(sess.Path); parent != "." {
			entries, _ := os.ReadDir(parent)
			if len(entries) == 0 {
				os.Remove(parent)
			}
		}

		return err

	default:
		tuiCtx := context.Background()
		engine := newLocalEngine(tuiCtx, a, lp, cp, prices, cfg.Notifications)
		inputTokens, outputTokens, cacheMiss, seedCost, _ := engine.Stats()

		initialCfg := tui.Config{
			SessionID: sess.ID,
			Mode:      tui.ModeSingleAgent,
			Engine:    engine,
			Shutdown:  engine.Close,
			Provider:  cfg.Provider,
			Model:     cfg.Model,
			Pricing:   prices,
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
			InitialHistory: tx.Frozen(),
			ToggleSubagents: func() string {
				if agentBuilder == nil {
					return "subagents not enabled in config"
				}
				enabled := !pb.SubagentEnabled()
				pb.SetSubagentEnabled(enabled)
				if enabled {
					return "subagents: on"
				}
				return "subagents: off"
			},
		}

		return tui.RunTabs(tuiCtx, initialCfg, engine.Events(), a.makeNewTabFn(tuiCtx, cp, prices, cfg.Notifications))
	}
}

// renderTo returns an EmitFunc that writes events in a format suitable for
// one-shot (-p) mode. Text deltas go to stdout (the agent's response); all
// metadata — tool calls, notices, turn summaries, permission prompts — goes
// to stderr so that stdout contains only the answer, suitable for piping.
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
		// after a model picker selection. Always check known providers first,
		// and only fall back to cfg.BaseURL for custom/unknown providers —
		// the default "https://api.deepseek.com" should not override a known
		// provider's endpoint.
		aliases := map[string]string{"go": "opencode-go", "zen": "opencode-zen", "opencode": "opencode-zen"}
		name := cfg.Provider
		if a, ok := aliases[name]; ok {
			name = a
		}
		base, err := openAICompatibleBaseURL(cfg.BaseDir, name, cfg.BaseURL)
		if err != nil {
			return nil, nil, err
		}
		prov = openai.New(base, cfg.APIKey)
	}

	// Build the compactor. By default, use a deterministic, zero-cost
	// placeholder summarizer that keeps the prefix cacheable after compaction.
	// When the user sets summary_model in config, an LLM-based summarizer
	// (using the specified model) replaces it for richer summaries.
	var sum session.Summarizer = session.DropSummarizer{}
	if cfg.SummaryModel != "" {
		sum = session.NewLLMSummarizer(prov, cfg.SummaryModel, "")
	}
	compactor := session.NewCompactor(sum)
	if cfg.CompactThreshold > 0 {
		compactor.Threshold = cfg.CompactThreshold
	}
	if cfg.CompactEmergencyThreshold > 0 {
		compactor.EmergencyThreshold = cfg.CompactEmergencyThreshold
	}
	if cfg.CompactChunkSize > 0 {
		compactor.ChunkSize = cfg.CompactChunkSize
	}
	compactor.Clamp()
	return prov, compactor, nil
}

func openAICompatibleBaseURL(baseDir, name, configured string) (string, error) {
	base := cleanBaseURL(configured)
	if name == "cloudflare" {
		if base == "" || base == "https://api.deepseek.com" {
			accountID := config.ResolveCloudflareAccountID(baseDir)
			if accountID == "" {
				return "", fmt.Errorf("cloudflare provider requires auth.json account_id, CLOUDFLARE_ACCOUNT_ID, or a base_url ending in /accounts/{id}/ai/v1")
			}
			base = fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/ai", accountID)
		}
		return base, nil
	}
	if kp, ok := provider.ByName(name); ok {
		if base == "" || (base == "https://api.deepseek.com" && name != "deepseek") {
			base = kp.BaseURL
		}
	}
	return base, nil
}

func roleLLMConfig(cfg *config.Config, providerName, modelName, baseURL, apiKey string) *config.Config {
	cc := *cfg
	if providerName != "" {
		cc.Provider = providerName
		if baseURL == "" {
			cc.BaseURL = ""
		}
	}
	if modelName != "" {
		cc.Model = modelName
	}
	if baseURL != "" {
		cc.BaseURL = baseURL
	}
	if apiKey != "" {
		cc.APIKey = apiKey
	} else {
		cc.ResolveAPIKey()
	}
	return &cc
}

func historianProvider(cfg *config.Config) (provider.Provider, string, error) {
	model := cfg.HistorianModel
	if model == "" {
		model = cfg.SummaryModel
	}
	if model == "" {
		model = cfg.Model
	}
	roleCfg := roleLLMConfig(cfg, cfg.HistorianProvider, model, cfg.HistorianBaseURL, cfg.HistorianAPIKey)
	prov, _, err := newProvider(roleCfg, true)
	return prov, model, err
}

func dreamerProvider(cfg *config.Config, overrideModel string) (provider.Provider, string, error) {
	model := overrideModel
	if model == "" {
		model = cfg.DreamerModel
	}
	if model == "" {
		model = cfg.SummaryModel
	}
	if model == "" {
		model = cfg.Model
	}
	roleCfg := roleLLMConfig(cfg, cfg.DreamerProvider, model, cfg.DreamerBaseURL, cfg.DreamerAPIKey)
	prov, _, err := newProvider(roleCfg, true)
	return prov, model, err
}

func newHistorian(cfg *config.Config, store *memory.Store) (*session.Historian, error) {
	if !cfg.Historian || store == nil {
		return nil, nil
	}
	prov, model, err := historianProvider(cfg)
	if err != nil {
		return nil, err
	}
	return &session.Historian{Provider: prov, Model: model, Store: store}, nil
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

func rpcResult(v any, errs ...error) (json.RawMessage, error) {
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	if v == nil {
		return json.RawMessage(`{}`), nil
	}
	return json.Marshal(v)
}

func decodeRPC[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, nil
	}
	err := json.Unmarshal(raw, &v)
	return v, err
}

func cycleThinkingLevel(cfg *config.Config, pb *loop.PromptBuilder) (string, error) {
	caps := provider.SupportedLevels(pb.Model())
	cur := pb.ThinkingLevel()
	if cur == "" {
		cur = caps[0]
	}
	for i, l := range caps {
		if l == cur {
			next := caps[(i+1)%len(caps)]
			pb.SetThinkingLevel(next)
			if err := config.SaveThinkingLevel(cfg.BaseDir, next); err != nil {
				fmt.Fprintf(os.Stderr, "[warning] save thinking level: %v\n", err)
			}
			return next, nil
		}
	}
	pb.SetThinkingLevel(caps[0])
	if err := config.SaveThinkingLevel(cfg.BaseDir, caps[0]); err != nil {
		fmt.Fprintf(os.Stderr, "[warning] save thinking level: %v\n", err)
	}
	return caps[0], nil
}

func runServer(ctx context.Context, cfg *config.Config, a *app, lp *loop.Loop, cp *safety.Checkpointer, sessionID string) error {
	engine := server.NewLoopEngine(ctx, lp, cp, sessionID)
	engine.SetNotifyConfig(cfg.Notifications)
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
		case "/mcp":
			return a.mcpManager.Status()
		case "/subagents":
			return a.toggleSubagents()
		default:
			return fmt.Sprintf("unknown command: %s", parts[0])
		}
	})
	srv := server.New(engine, server.SocketPath(cfg.BaseDir, sessionID), server.PidFile(cfg.BaseDir, sessionID), 0)
	if cfg.ServerListen != "" {
		network := "tcp"
		addr := cfg.ServerListen
		if strings.HasPrefix(addr, "ws:") {
			network = "ws"
			addr = strings.TrimPrefix(addr, "ws:")
		} else {
			addr = strings.TrimPrefix(addr, "tcp:")
		}
		srv.SetListen(network, addr)
		token := cfg.ServerToken
		if token == "" {
			token = generateID()
			fmt.Fprintf(os.Stderr, "[server token %s]\n", token)
		}
		srv.SetToken(token)
		fmt.Fprintf(os.Stderr, "[server listening on %s %s]\n", network, addr)
	}
	prices := pricing.Load(cfg.BaseDir)
	srv.SetRequestHandler(func(reqCtx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		switch method {
		case rpc.NewSession:
			tx, id, err := a.newSession()
			if err != nil {
				return nil, err
			}
			lp.SetTranscript(tx)
			engine.SetSessionID(id)
			fmt.Fprintf(os.Stderr, "[started new session %s]\n", id)
			return rpcResult(struct {
				ID string `json:"id"`
			}{ID: id})
		case rpc.Resume:
			p, err := decodeRPC[rpc.ResumeParams](params)
			if err != nil {
				return nil, err
			}
			tx, id, err := a.resumeSession(p.ID)
			if err != nil {
				return nil, err
			}
			lp.SetTranscript(tx)
			engine.SetSessionID(id)
			fmt.Fprintf(os.Stderr, "[resumed session %s]\n", id)
			return rpcResult(rpc.ResumeResult{ID: id, History: tx.Frozen()})
		case rpc.ListSessions:
			sessions, err := a.fetchSessions()
			if err != nil {
				return nil, err
			}
			return rpcResult(rpc.SessionsResult{Sessions: sessions})
		case rpc.DeleteSession:
			p, err := decodeRPC[rpc.DeleteSessionParams](params)
			if err != nil {
				return nil, err
			}
			return rpcResult(nil, session.DeleteSession(a.cwd, p.ID))
		case rpc.ListModels:
			models, err := a.fetchModels()
			if err != nil {
				return nil, err
			}
			return rpcResult(rpc.ModelsResult{Models: models})
		case rpc.SwitchModel:
			p, err := decodeRPC[rpc.SwitchModelParams](params)
			if err != nil {
				return nil, err
			}
			if err := a.switchProvider(p.Provider, p.Model); err != nil {
				return nil, err
			}
			engine.Emit(protocol.ModelChanged{
				Provider:      a.cfg.Provider,
				Model:         a.cfg.Model,
				ThinkingLevel: a.pb.ThinkingLevel(),
			})
			return rpcResult(nil)
		case rpc.CycleThinking:
			level, err := cycleThinkingLevel(a.cfg, a.pb)
			if err != nil {
				return nil, err
			}
			engine.Emit(protocol.ModelChanged{
				Provider:      a.cfg.Provider,
				Model:         a.cfg.Model,
				ThinkingLevel: level,
			})
			return rpcResult(rpc.LevelResult{Level: level})
		case rpc.CurrentThinking:
			return rpcResult(rpc.LevelResult{Level: a.pb.ThinkingLevel()})
		case rpc.WithdrawSteer:
			return rpcResult(rpc.BlocksResult{Blocks: engine.WithdrawSteer()})
		case rpc.Compact:
			p, err := decodeRPC[rpc.CompactParams](params)
			if err != nil {
				return nil, err
			}
			go func() {
				compactCtx, cancel := context.WithTimeout(reqCtx, 120*time.Second)
				summary, err := lp.CompactNow(compactCtx, p.Focus)
				cancel()
				if err != nil {
					engine.Emit(protocol.Notice{Level: "error", Text: "/compact failed: " + err.Error()})
				} else if summary == "" {
					engine.Emit(protocol.Notice{Level: "info", Text: "nothing to compact"})
				} else {
					engine.Emit(protocol.Notice{Level: "info", Text: "compacted oldest chunk - summary: " + summary})
				}
			}()
			return rpcResult(nil)
		case rpc.Stats:
			s := lp.Stats()
			cost := 0.0
			if prices != nil && a.cfg.Model != "" {
				cost = prices.Cost(a.cfg.Model, s.InputTokens, s.OutputTokens)
			}
			return rpcResult(rpc.StatsResult{
				Input:     s.InputTokens,
				Output:    s.OutputTokens,
				CacheMiss: s.InputTokens - s.CachedTokens,
				Cost:      cost,
			})
		case rpc.LoginProviders:
			return rpcResult(rpc.LoginProvidersResult{Providers: a.loginProviders()})
		case rpc.Login:
			p, err := decodeRPC[rpc.LoginParams](params)
			if err != nil {
				return nil, err
			}
			return rpcResult(nil, a.saveLogin(p.Provider, p.Key))
		case rpc.MCPStatus:
			return rpcResult(rpc.StatusResult{Status: a.mcpManager.Status()})
		case rpc.MCPCount:
			return rpcResult(rpc.CountResult{Count: a.mcpManager.ConnectedCount()})
		case rpc.CancelSubagent:
			p, err := decodeRPC[rpc.CancelSubagentParams](params)
			if err != nil {
				return nil, err
			}
			if a.agentBuilder != nil {
				a.agentBuilder.CancelSubagent(p.ID)
			}
			return rpcResult(nil)
		case rpc.History:
			return rpcResult(rpc.HistoryResult{History: lp.History()})
		case rpc.ListFiles:
			p, err := decodeRPC[rpc.ListFilesParams](params)
			if err != nil {
				return nil, err
			}
			files, err := client.ListFiles(a.cwd, p.Prefix)
			if err != nil {
				return nil, err
			}
			return rpcResult(rpc.ListFilesResult{Files: files})
		case rpc.ResolveInput:
			p, err := decodeRPC[rpc.ResolveInputParams](params)
			if err != nil {
				return nil, err
			}
			blocks, display, err := client.ResolveInput(a.cwd, p.Text)
			if err != nil {
				return nil, err
			}
			return rpcResult(rpc.ResolveInputResult{Blocks: blocks, Display: display})
		case rpc.PushInstruction:
			msg, notice, err := client.PushInstruction(a.cwd)
			if err != nil {
				return nil, err
			}
			return rpcResult(rpc.PushInstructionResult{Message: msg, Notice: notice})
		default:
			return nil, fmt.Errorf("unknown method %s", method)
		}
	})
	fmt.Fprintf(os.Stderr, "[server running for session %s]\n", sessionID)
	return srv.Start(ctx)
}

func runAttach(ctx context.Context, cfg *config.Config, sessionID string) error {
	var clientConn *server.ClientConn
	var events <-chan protocol.Event
	var err error
	if cfg.ServerListen != "" {
		isWS := strings.HasPrefix(cfg.ServerListen, "ws:")
		addr := strings.TrimPrefix(cfg.ServerListen, "tcp:")
		addr = strings.TrimPrefix(addr, "ws:")
		addr = strings.Replace(addr, "0.0.0.0:", "127.0.0.1:", 1)
		if isWS {
			clientConn, events, err = server.RunClientWebSocket(ctx, "ws://"+addr+"/ws", cfg.ServerToken)
		} else {
			clientConn, events, err = server.RunClientNetwork(ctx, "tcp", addr, cfg.ServerToken)
		}
	} else {
		clientConn, events, err = server.RunClient(ctx, server.SocketPath(cfg.BaseDir, sessionID))
	}
	if err != nil {
		return err
	}

	prices := pricing.Load(cfg.BaseDir)

	engine := client.NewRemoteEngine(clientConn, events)
	initialHistory, _ := engine.History()
	inputTokens, outputTokens, cacheMiss, seedCost, _ := engine.Stats()

	m := tui.NewModel(tui.Config{
		SessionID:      sessionID,
		Mode:           tui.ModeSingleAgent,
		InitialHistory: initialHistory,
		Engine:         engine,
		Provider:       cfg.Provider,
		Model:          cfg.Model,
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
	})
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

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

func runDream(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("dream", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "show curation report without writing")
	model := fs.String("model", "", "curation model override")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	repoRoot := findRepoRoot(cwd)
	store, err := memory.Open(cfg.BaseDir, memory.ProjectID(repoRoot))
	if err != nil {
		return err
	}
	entries := store.All()
	var downgraded int
	for _, e := range entries {
		if !memoryReferencesMissingPath(repoRoot, e) {
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
	prov, modelName, err := dreamerProvider(cfg, *model)
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

func memoryReferencesMissingPath(root string, e memory.Entry) bool {
	fields := append(strings.Fields(e.Text), e.Tags...)
	for _, f := range fields {
		f = strings.Trim(f, "`'\".,:;()[]{}")
		if !strings.Contains(f, "/") && !strings.Contains(f, ".") {
			continue
		}
		if strings.HasPrefix(f, "http://") || strings.HasPrefix(f, "https://") {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			return true
		}
	}
	return false
}
