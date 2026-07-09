package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mintoleda/talos/internal/agents"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/executor"
	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/mcp"
	"github.com/mintoleda/talos/internal/memory"
	"github.com/mintoleda/talos/internal/pricing"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/safety"
	"github.com/mintoleda/talos/internal/session"
	"github.com/mintoleda/talos/internal/skills"
	"github.com/mintoleda/talos/internal/tools"
)

// BuildOpts configures assembly of a per-session engine stack.
type BuildOpts struct {
	Cfg        *config.Config // shared daemon-level config; Build MUST copy before mutating
	Dir        string         // session cwd
	ProjectDir string         // origin repo root (= Dir when isolation none); if empty, find from Dir
	SessionID  string         // "" => new session; otherwise open/resume that ID
	Continue   bool           // if SessionID empty and Continue, use LatestSession
	ResumePick bool           // reserved; for step 1, caller picks interactively and sets SessionID
	NoTools    bool
	Provider   string // per-session override
	Model      string // per-session override
	PrintMode  bool   // f.Print != "" — affects Policy interactive flag
	DebugCache bool
}

// Built is the assembled stack for one session.
type Built struct {
	Cfg             *config.Config // per-session copy
	Dir             string
	ProjectDir      string
	Session         session.Session
	Transcript      *session.Transcript
	Registry        *tools.Registry
	MCPManager      *mcp.Manager
	MemStore        *memory.Store
	AgentBuilder    *agents.Builder
	Policy          *safety.Policy
	Executor        executor.Executor
	Provider        provider.Provider
	Compactor       *session.Compactor
	Prices          *pricing.Table
	PromptBuilder   *loop.PromptBuilder
	Loop            *loop.Loop
	Checkpointer    *safety.Checkpointer
	Reads           *tools.ReadSet
	SubagentListing string

	// transferred is set when NewEngine takes ownership; Close becomes a no-op.
	transferred bool
}

// Build assembles the per-cwd stack for one session. The returned Built owns
// transcript, loop, MCP, and registry resources; call Close when done.
func Build(ctx context.Context, o BuildOpts) (*Built, error) {
	if o.Cfg == nil {
		return nil, fmt.Errorf("engine: Config is required")
	}
	if o.Dir == "" {
		return nil, fmt.Errorf("engine: Dir is required")
	}

	cfgCopy := *o.Cfg
	cfg := &cfgCopy
	if o.Provider != "" {
		cfg.OverrideProvider(o.Provider)
	}
	if o.Model != "" {
		cfg.Override("", o.Model, "")
	}

	dir := o.Dir
	projectDir := o.ProjectDir
	if projectDir == "" {
		projectDir = FindRepoRoot(dir)
	}

	var (
		tx   *session.Transcript
		sess session.Session
		err  error
	)
	// Transcripts are keyed by ProjectDir (origin repo), not the worktree Dir.
	switch {
	case o.SessionID != "":
		sess, err = session.OpenSession(projectDir, o.SessionID)
		if err != nil {
			// Fresh create with a pre-allocated ID (worktree path allocated the ID first).
			sess = session.SessionAt(projectDir, o.SessionID)
			err = nil
		}
	case o.Continue:
		sess, err = session.LatestSession(projectDir)
		if err != nil {
			sess = session.NewSession(projectDir)
			err = nil
		}
	default:
		sess = session.NewSession(projectDir)
	}
	if _, statErr := os.Stat(sess.Path); statErr == nil {
		tx, err = session.Load(sess.Path)
	} else {
		tx, err = session.Create(sess.Path)
	}
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	if n := tx.RepairCount(); n > 0 {
		fmt.Fprintf(os.Stderr, "[notice] repaired session: removed %d orphaned tool call(s)\n", n)
	}

	// Versioned inputs load from Dir (worktree checkout).
	if sp, err := config.LoadProjectSystemPrompt(dir); err != nil {
		fmt.Fprintf(os.Stderr, "[warning] reading SYSTEM_PROMPT.md: %v\n", err)
	} else if sp != "" {
		cfg.SystemPrompt = sp
	}

	skillsDir := filepath.Join(cfg.BaseDir, "skills")
	projectSkillsDir := filepath.Join(dir, ".talos", "skills")
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

	// Unversioned state (memory, transcripts, read-set) keyed by ProjectDir.
	projectID := memory.ProjectID(projectDir)
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
	if memStore != nil && !o.NoTools {
		verifier := memory.NewVerifier(memory.StorePath(cfg.BaseDir, projectID))
		go func() {
			entries := memStore.All()
			var toFlag []string
			for _, e := range entries {
				if e.Source != "agent" || e.Importance < 0.7 {
					continue
				}
				if MemoryReferencesMissingPath(projectDir, e) {
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

	// Checkpointer operates in the session Dir (worktree), namespaced by sessionID.
	cp := safety.NewCheckpointer(dir, sess.ID)

	reads, readErr := tools.LoadReadSet(sess.Path + ".reads.json")
	if readErr != nil {
		fmt.Fprintf(os.Stderr, "[warning] loading read set: %v\n", readErr)
		reads = tools.NewReadSet()
	}
	reads.SetSavePath(sess.Path + ".reads.json")

	mcpManager, mcpErrs := mcp.NewManager(ctx, cfg.MCPServers)
	for _, e := range mcpErrs {
		fmt.Fprintf(os.Stderr, "[mcp] %v\n", e)
	}

	var (
		reg             *tools.Registry
		agentBuilder    *agents.Builder
		subagentListing string
	)
	if o.NoTools {
		reg = tools.EmptyRegistry()
	} else {
		reg = tools.DefaultRegistry(dir, reads, tools.BashConfig{
			DefaultTimeout: cfg.BashTimeout,
			MaxTimeout:     cfg.BashMaxTimeout,
			MaxOutput:      cfg.BashMaxOutput,
		}, cfg.SearchURL)
		reg.Add(tools.NewSkillTool([]string{
			filepath.Join(cfg.BaseDir, "skills"),
			filepath.Join(dir, ".talos", "skills"),
		}))

		if cfg.EnableSubagents {
			agentDirs := []agents.Dir{
				{Path: filepath.Join(cfg.BaseDir, "subagents"), Label: "global"},
				{Path: filepath.Join(dir, ".talos", "subagents"), Label: "project"},
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
					Cwd:            dir,
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

	prov, compactor, err := NewProvider(cfg, o.NoTools)
	if err != nil {
		_ = tx.Close()
		mcpManager.Close()
		reg.Close()
		return nil, fmt.Errorf("provider: %w", err)
	}
	if compactor != nil && cfg.Historian && memStore != nil {
		hist, err := NewHistorian(cfg, memStore)
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

	pol := safety.NewPolicy(cfg.PermissionMode, dir, safety.NewClassifier(), !o.PrintMode)
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
	lp.DebugCache = o.DebugCache
	lp.KillBgOnInterrupt = cfg.KillBgOnInterrupt

	return &Built{
		Cfg:             cfg,
		Dir:             dir,
		ProjectDir:      projectDir,
		Session:         sess,
		Transcript:      tx,
		Registry:        reg,
		MCPManager:      mcpManager,
		MemStore:        memStore,
		AgentBuilder:    agentBuilder,
		Policy:          pol,
		Executor:        exec,
		Provider:        prov,
		Compactor:       compactor,
		Prices:          prices,
		PromptBuilder:   pb,
		Loop:            lp,
		Checkpointer:    cp,
		Reads:           reads,
		SubagentListing: subagentListing,
	}, nil
}

// Close releases transcript, loop, MCP, and registry resources.
// No-op after NewEngine transfers ownership to Engine.Close.
func (b *Built) Close() {
	if b == nil || b.transferred {
		return
	}
	b.forceClose()
}

func (b *Built) forceClose() {
	if b == nil {
		return
	}
	if b.Loop != nil {
		b.Loop.Close()
	}
	if b.MCPManager != nil {
		b.MCPManager.Close()
	}
	if b.Registry != nil {
		b.Registry.Close()
	}
	if b.Transcript != nil {
		_ = b.Transcript.Close()
	}
}
