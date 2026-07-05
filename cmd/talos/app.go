package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/mintoleda/talos/internal/agents"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/executor"
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
	if a.pb == nil {
		return "subagents not configured"
	}
	enabled := !a.pb.SubagentEnabled()
	a.pb.SetSubagentEnabled(enabled)
	if enabled {
		return "subagents: on"
	}
	return "subagents: off"
}

func (a *app) makeNewTabFn(ctx context.Context, cp *safety.Checkpointer, prices *pricing.Table, notifyCfg notify.Config) tui.NewTabFunc {
	return func(tabCtx context.Context, tabID int) (tui.Config, <-chan protocol.Event, func(), error) {
		ntx, id, err := a.newSession()
		if err != nil {
			return tui.Config{}, nil, nil, fmt.Errorf("new tab session: %w", err)
		}

		newLp := loop.New(a.prov, a.exec, ntx, a.pb)
		var sum session.Summarizer = session.DropSummarizer{}
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
		newLp.SetCompactor(comp)

		inCh, steerQueue, intCh, cmpCh, evCh, engineCleanup := tui.StartEngine(tabCtx, newLp, cp, notifyCfg)

		// Wrap the engine cleanup to also close the per-tab loop.
		cleanup := func() {
			engineCleanup()
			newLp.Close()
		}

		cfg := tui.Config{
			SessionID:   id,
			Mode:        tui.ModeSingleAgent,
			InputCh:     inCh,
			SteerQueue:  steerQueue,
			InterruptCh: intCh,
			CompactCh:   cmpCh,
			Shutdown:    cleanup,
			Provider:    a.cfg.Provider,
			Model:       a.cfg.Model,
			Pricing:     prices,
			NewSession: func() (string, error) {
				ntx2, id2, err := a.newSession()
				if err != nil {
					return "", err
				}
				newLp.SetTranscript(ntx2)
				fmt.Fprintf(os.Stderr, "[started new session %s]\n", id2)
				return id2, nil
			},
			Stats: func() string {
				s := newLp.Stats()
				if s.Calls == 0 {
					return "[stats] no API calls yet"
				}
				return fmt.Sprintf("[stats] calls=%d | input=%d | output=%d | cached=%d (%.1f%%)",
					s.Calls, s.InputTokens, s.OutputTokens, s.CachedTokens, s.CacheHitRate()*100)
			},
			ResumeSession: func(sessID string) (string, []protocol.FrozenMessage, error) {
				tx, sid, err := a.resumeSession(sessID)
				if err != nil {
					return "", nil, err
				}
				newLp.SetTranscript(tx)
				fmt.Fprintf(os.Stderr, "[resumed session %s]\n", sid)
				return sid, tx.Frozen(), nil
			},
			SwitchProvider: func(pName, pModel string) error {
				return a.switchProviderFor(newLp, pName, pModel)
			},
			CycleThinking: func() string {
				caps := provider.SupportedLevels(a.pb.Model())
				cur := a.pb.ThinkingLevel()
				if cur == "" {
					cur = caps[0]
				}
				for i, l := range caps {
					if l == cur {
						next := caps[(i+1)%len(caps)]
						a.pb.SetThinkingLevel(next)
						if err := config.SaveThinkingLevel(a.cfg.BaseDir, next); err != nil {
							fmt.Fprintf(os.Stderr, "[warning] save thinking level: %v\n", err)
						}
						return next
					}
				}
				a.pb.SetThinkingLevel(caps[0])
				_ = config.SaveThinkingLevel(a.cfg.BaseDir, caps[0])
				return caps[0]
			},
			CurrentThinkingLevel: func() string { return a.pb.ThinkingLevel() },
			DeleteSession:        func(id string) error { return session.DeleteSession(a.cwd, id) },
			FetchSessions:        a.fetchSessions,
			FetchModels:          a.fetchModels,
			LoginProviders:       a.loginProviders,
			SaveLogin:            a.saveLogin,
			CancelSubagent: func(id string) {
				if a.agentBuilder != nil {
					a.agentBuilder.CancelSubagent(id)
				}
			},
			MCPStatus: func() string { return a.mcpManager.Status() },
			MCPCount: func() int { return a.mcpManager.ConnectedCount() },
			ToggleSubagents: func() string {
				return a.toggleSubagents()
			},
			StatsSnapshot: func() (int, int, int, float64) {
				s := newLp.Stats()
				cm := s.InputTokens - s.CachedTokens
				c := 0.0
				if prices != nil && a.cfg.Model != "" {
					c = prices.Cost(a.cfg.Model, s.InputTokens, s.OutputTokens)
				}
				return s.InputTokens, s.OutputTokens, cm, c
			},
		}

		return cfg, evCh, cleanup, nil
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
