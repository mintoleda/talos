package agents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/executor"
	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/pricing"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/provider/anthropic"
	"github.com/mintoleda/talos/internal/provider/openai"
	"github.com/mintoleda/talos/internal/safety"
	"github.com/mintoleda/talos/internal/session"
	"github.com/mintoleda/talos/internal/tools"
)

// defaultMaxDepth bounds subagent nesting regardless of definition wiring, as a
// backstop against pathological recursion. The primary agent is depth 0.
const defaultMaxDepth = 3

// Config carries the shared runtime settings a Builder needs to construct nested
// agent loops. Subagents inherit the caller's provider, credentials, and working
// directory; a definition may override the model, thinking level, and provider.
type Config struct {
	Provider       string
	BaseURL        string
	APIKey         string
	Model          string // default model, inherited when a definition omits one
	ThinkingLevel  string // default thinking level, inherited when omitted
	MaxAgentDepth  int    // max subagent nesting depth (0 = use default of 3)
	Mode           safety.Mode
	BashTimeout    time.Duration
	BashMaxTimeout time.Duration
	BashMaxOutput  int
	SearchURL      string
	Cwd            string // directory subagents read/operate in
	BaseDir        string // ~/.talos, for pricing overrides and temp transcripts
}

// Builder constructs subagent loops and the spawn tools that drive them.
type Builder struct {
	cfg       Config
	defs      map[string]Definition
	prices    *pricing.Table
	maxDepth  int
	cancelMap sync.Map // subagent id → context.CancelFunc for running subagents
}

func (b *Builder) registerCancel(id string, cancel context.CancelFunc) {
	b.cancelMap.Store(id, cancel)
}

func (b *Builder) removeCancel(id string) {
	b.cancelMap.Delete(id)
}

func (b *Builder) CancelSubagent(id string) {
	if fn, ok := b.cancelMap.Load(id); ok {
		fn.(context.CancelFunc)()
	}
}

func NewBuilder(cfg Config, defs map[string]Definition) *Builder {
	depth := cfg.MaxAgentDepth
	if depth <= 0 {
		depth = defaultMaxDepth
	}
	return &Builder{
		cfg:      cfg,
		defs:     defs,
		prices:   pricing.Load(cfg.BaseDir),
		maxDepth: depth,
	}
}

// SpawnTools returns one spawn tool per named agent that exists, for injection
// into a registry. These are the agents the holder may delegate to.
func (b *Builder) SpawnTools(allowed []string) []tools.Tool {
	return b.spawnTools(allowed, 0)
}

func (b *Builder) spawnTools(allowed []string, depth int) []tools.Tool {
	var out []tools.Tool
	for _, name := range allowed {
		def, ok := b.defs[name]
		if !ok {
			continue
		}
		out = append(out, &spawnTool{builder: b, def: def, depth: depth})
	}
	return out
}

// SubagentToolNames returns the sorted list of subagent definition names,
// corresponding to the spawn tool names registered in the primary agent.
func (b *Builder) SubagentToolNames() []string {
	names := make([]string, 0, len(b.defs))
	for name := range b.defs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// builtLoop bundles a constructed subagent loop with the data needed to account
// for it after the run.
type builtLoop struct {
	lp      *loop.Loop
	tx      *session.Transcript
	pb      *loop.PromptBuilder
	model   string
	cleanup func()
}

// build assembles a fresh, isolated loop for one subagent run. depth is the
// depth of the agent being built (used to wire its own nested spawn tools).
func (b *Builder) build(def Definition, depth int) builtLoop {
	model := def.Model
	if model == "" {
		model = b.cfg.Model
	}
	thinking := def.Thinking
	if thinking == "" {
		thinking = b.cfg.ThinkingLevel
	}

	reads := tools.NewReadSet()
	full := tools.DefaultRegistry(b.cfg.Cwd, reads, tools.BashConfig{
		DefaultTimeout: b.cfg.BashTimeout,
		MaxTimeout:     b.cfg.BashMaxTimeout,
		MaxOutput:      b.cfg.BashMaxOutput,
	}, b.cfg.SearchURL)
	reg := full.Filter(def.Tools)
	// The agent's own subagents become spawn tools one level deeper.
	reg.Add(b.spawnTools(def.Subagents, depth+1)...)

	// interactive=true: a human is reachable via the event stream, so risky
	// bash in ask-mode routes a PermissionRequested up to the same dialog.
	pol := safety.NewPolicy(b.cfg.Mode, b.cfg.Cwd, safety.NewClassifier(), true)
	exec := executor.New(reg, pol)

	pb := loop.NewPromptBuilder(def.Prompt, reg.Schemas(), model)
	pb.SetThinkingLevel(thinking)
	pb.SetContextLimit(b.prices.ContextWindow(model))

	path := filepath.Join(os.TempDir(), "talos-subagent-"+newID()+".jsonl")
	tx, _ := session.Create(path)
	lp := loop.New(b.providerFor(def, thinking), exec, tx, pb)

	cleanup := func() {
		lp.Close()
		_ = os.Remove(path)
	}
	return builtLoop{lp: lp, tx: tx, pb: pb, model: model, cleanup: cleanup}
}

// providerFor builds an LLM client for a subagent. If the definition specifies
// a provider override, that provider's credentials are resolved from auth.json
// and its default base URL is used. Otherwise the caller's provider is inherited.
func (b *Builder) providerFor(def Definition, thinking string) provider.Provider {
	provName := b.cfg.Provider
	apiKey := b.cfg.APIKey

	if def.Provider != "" && def.Provider != b.cfg.Provider {
		provName = def.Provider
		apiKey = config.ReadAuthKey(b.cfg.BaseDir, provName)
	}

	if provName == "anthropic" {
		base := b.cfg.BaseURL
		// Don't inherit the caller's generic default for a known provider.
		if base == "" || base == "https://api.deepseek.com" {
			if kp, ok := provider.ByName(provName); ok {
				base = kp.BaseURL
			}
		}
		return anthropic.New(base, apiKey, anthropic.Config{
			MaxTokens:     8192,
			ThinkingLevel: thinking,
		})
	}
	// OpenAI-compatible: check known providers first, only fall back to
	// b.cfg.BaseURL for custom/unknown providers — the default
	// "https://api.deepseek.com" should not override a known provider's
	// endpoint.
	base := ""
	if kp, ok := provider.ByName(provName); ok {
		base = kp.BaseURL
	} else {
		base = b.cfg.BaseURL
	}
	return openai.New(base, apiKey)
}

func (b *Builder) usage(bl builtLoop) protocol.SubagentUsage {
	st := bl.lp.Stats()
	ctxTokens := bl.pb.EstimatedTokens(bl.pb.Build(bl.tx))
	ctxLimit := b.prices.ContextWindow(bl.model)
	if ctxLimit == 0 {
		ctxLimit = bl.pb.ContextLimit()
	}
	return protocol.SubagentUsage{
		InputTokens:   st.InputTokens,
		OutputTokens:  st.OutputTokens,
		ContextTokens: ctxTokens,
		ContextLimit:  ctxLimit,
		Cost:          b.prices.Cost(bl.model, st.InputTokens, st.OutputTokens),
	}
}

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
