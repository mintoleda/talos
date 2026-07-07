package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"

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
	"github.com/mintoleda/talos/internal/tui"
	"github.com/mintoleda/talos/internal/tui/dialogs"
)

type app struct {
	cfg          *config.Config
	lp           *loop.Loop
	pb           *loop.PromptBuilder
	prov         provider.Provider
	exec         executor.Executor
	agentBuilder *agents.Builder
	mcpManager   *mcp.Manager
	memStore     *memory.Store
	cwd          string
	noTools      bool

	modelCache []models.Entry
}

func (a *app) newSession() (*session.Transcript, string, error) {
	ns := session.NewSession(a.cwd)
	ntx, err := session.Create(ns.Path)
	if err != nil {
		return nil, "", err
	}
	return ntx, ns.ID, nil
}

func (a *app) resumeSession(id string) (*session.Transcript, string, error) {
	var sess session.Session
	var err error
	if id != "" {
		sess, err = session.OpenSession(a.cwd, id)
		if err != nil {
			return nil, "", fmt.Errorf("session not found: %s", id)
		}
	} else {
		sess, err = pickSession(a.cwd)
		if err != nil {
			return nil, "", err
		}
	}
	tx, err := session.Load(sess.Path)
	if err != nil {
		return nil, "", fmt.Errorf("load session: %w", err)
	}
	return tx, sess.ID, nil
}

func (a *app) switchProviderFor(lp *loop.Loop, pName, pModel string) error {
	oldProv := a.cfg.Provider
	oldModel := a.cfg.Model

	a.cfg.Provider = pName
	if pModel != "" {
		a.cfg.Model = pModel
	}
	if pName != oldProv {
		a.cfg.BaseURL = "" // let newProvider pick the canonical URL from the registry
	}
	a.cfg.ResolveAPIKey()

	prov, comp, err := newProvider(a.cfg, a.noTools)
	if err != nil {
		a.cfg.Provider = oldProv
		a.cfg.Model = oldModel
		return err
	}
	lp.SetProvider(prov)
	if comp != nil && a.cfg.Historian && a.memStore != nil {
		hist, err := newHistorian(a.cfg, a.memStore)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[warning] historian disabled: %v\n", err)
		} else {
			comp.Historian = hist
		}
	}
	lp.SetCompactor(comp)
	a.pb.SetModel(a.cfg.Model)
	a.prov = prov
	a.modelCache = nil

	if err := config.SaveProviderModel(a.cfg.BaseDir, a.cfg.Provider, a.cfg.Model); err != nil {
		fmt.Fprintf(os.Stderr, "[warning] save provider/model: %v\n", err)
	}
	return nil
}

func (a *app) switchProvider(pName, pModel string) error {
	return a.switchProviderFor(a.lp, pName, pModel)
}

func (a *app) toggleSubagents() string {
	if a.agentBuilder == nil {
		return "subagents not enabled in config"
	}
	enabled := !a.pb.SubagentEnabled()
	a.pb.SetSubagentEnabled(enabled)
	if enabled {
		return "subagents: on"
	}
	return "subagents: off"
}

// newLocalEngine constructs a client.Engine from the application's current
// state and dependencies. It wires the same objects that the inline Config
// closures in run() and makeNewTabFn build, but packaged behind the Engine
// interface. This is step 2 of the engine seam refactor — the TUI still uses
// the old Config path; step 3 switches it over.
//
// The returned Engine must be closed via Engine.Close() when done.
func newLocalEngine(ctx context.Context, a *app, lp *loop.Loop, cp *safety.Checkpointer, prices *pricing.Table, notifyCfg notify.Config) client.Engine {
	return client.NewLocalEngine(client.Params{
		Loop:          lp,
		PromptBuilder: a.pb,
		Prices:        prices,
		Provider:      a.cfg.Provider,
		Model:         a.cfg.Model,
		BaseDir:       a.cfg.BaseDir,
		CWD:           a.cwd,
		MCPManager:    a.mcpManager,
		AgentBuilder:  a.agentBuilder,
		Checkpointer:  cp,
		Policy:        a.exec.Policy(),
		NotifyConfig:  notifyCfg,
		Context:       ctx,
		SwitchProvider: func(pName, pModel string) error {
			return a.switchProviderFor(lp, pName, pModel)
		},
	})
}

func (a *app) makeNewTabFn(ctx context.Context, cp *safety.Checkpointer, prices *pricing.Table, notifyCfg notify.Config) tui.NewTabFunc {
	return func(tabCtx context.Context, tabID int) (tui.Config, <-chan protocol.Event, func(), error) {
		ntx, id, err := a.newSession()
		if err != nil {
			return tui.Config{}, nil, nil, fmt.Errorf("new tab session: %w", err)
		}

		newLp := loop.New(a.prov, a.exec, ntx, a.pb)
		var sum session.Summarizer = session.ExtractSummarizer{}
		if a.cfg.SummaryModel != "" {
			sum = session.NewLLMSummarizer(a.prov, a.cfg.SummaryModel, "")
		}
		comp := session.NewCompactor(sum)
		if a.cfg.CompactThreshold > 0 {
			comp.Threshold = a.cfg.CompactThreshold
		}
		if a.cfg.CompactEmergencyThreshold > 0 {
			comp.EmergencyThreshold = a.cfg.CompactEmergencyThreshold
		}
		if a.cfg.CompactChunkSize > 0 {
			comp.ChunkSize = a.cfg.CompactChunkSize
		}
		comp.Clamp()
		if a.cfg.Historian && a.memStore != nil {
			hist, err := newHistorian(a.cfg, a.memStore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[warning] historian disabled: %v\n", err)
			} else {
				comp.Historian = hist
			}
		}
		newLp.SetCompactor(comp)

		engine := newLocalEngine(tabCtx, a, newLp, cp, prices, notifyCfg)

		cfg := tui.Config{
			SessionID: id,
			Mode:      tui.ModeSingleAgent,
			Engine:    engine,
			Shutdown:  engine.Close,
			Provider:  a.cfg.Provider,
			Model:     a.cfg.Model,
			Pricing:   prices,
			ToggleSubagents: func() string {
				return a.toggleSubagents()
			},
		}

		return cfg, engine.Events(), engine.Close, nil
	}
}

func (a *app) fetchModels() ([]models.Entry, error) {
	if a.modelCache != nil {
		return a.modelCache, nil
	}
	type result struct {
		entries []models.Entry
	}
	ch := make(chan result, len(provider.All))
	var wg sync.WaitGroup
	for _, kp := range provider.All {
		// Anthropic doesn't expose a /v1/models endpoint; skip it and fall
		// back to its hardcoded model list elsewhere.
		if kp.Name == "anthropic" {
			continue
		}
		key := config.ResolveKeyFor(a.cfg.BaseDir, kp.Name, kp.EnvVar)
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
	a.modelCache = all
	return all, nil
}

func (a *app) loginProviders() []dialogs.LoginProvider {
	var out []dialogs.LoginProvider
	for _, kp := range provider.All {
		key := config.ResolveKeyFor(a.cfg.BaseDir, kp.Name, kp.EnvVar)
		out = append(out, dialogs.LoginProvider{
			Name:     kp.Name,
			Label:    kp.Label,
			LoggedIn: key != "",
		})
	}
	return out
}

func (a *app) fetchSessions() ([]dialogs.SessionEntry, error) {
	previews, err := session.ListSessionPreviews(a.cwd)
	if err != nil {
		return nil, err
	}
	entries := make([]dialogs.SessionEntry, len(previews))
	for i, p := range previews {
		entries[i] = dialogs.SessionEntry{ID: p.ID, ModTime: p.ModTime, Preview: p.Preview}
	}
	return entries, nil
}

func (a *app) saveLogin(provName, key string) error {
	if err := config.WriteAuthKey(a.cfg.BaseDir, provName, key); err != nil {
		return err
	}
	a.modelCache = nil
	return nil
}
