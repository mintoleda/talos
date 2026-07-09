package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mintoleda/talos/internal/agents"
	"github.com/mintoleda/talos/internal/config"
	"github.com/mintoleda/talos/internal/engine"
	"github.com/mintoleda/talos/internal/executor"
	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/mcp"
	"github.com/mintoleda/talos/internal/memory"
	"github.com/mintoleda/talos/internal/notify"
	"github.com/mintoleda/talos/internal/pricing"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/provider"
	"github.com/mintoleda/talos/internal/safety"
	"github.com/mintoleda/talos/internal/session"
	"github.com/mintoleda/talos/internal/tui"
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
}

func (a *app) makeNewTabFn(ctx context.Context, cp *safety.Checkpointer, prices *pricing.Table, notifyCfg notify.Config) tui.NewTabFunc {
	_ = ctx
	return func(tabCtx context.Context, tabID int) (tui.Config, <-chan protocol.Event, func(), error) {
		ns := session.NewSession(a.cwd)
		ntx, err := session.Create(ns.Path)
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
			hist, err := engine.NewHistorian(a.cfg, a.memStore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[warning] historian disabled: %v\n", err)
			} else {
				comp.Historian = hist
			}
		}
		newLp.SetCompactor(comp)

		// Per-tab cfg copy so SwitchModel does not mutate the shared app cfg.
		cfgCopy := *a.cfg
		eng := engine.NewEngine(engine.Params{
			Loop:          newLp,
			PromptBuilder: a.pb,
			Prices:        prices,
			Cfg:           &cfgCopy,
			Provider:      a.cfg.Provider,
			Model:         a.cfg.Model,
			BaseDir:       a.cfg.BaseDir,
			CWD:           a.cwd,
			MCPManager:    a.mcpManager,
			AgentBuilder:  a.agentBuilder,
			Checkpointer:  cp,
			Policy:        a.exec.Policy(),
			Executor:      a.exec,
			MemStore:      a.memStore,
			NoTools:       a.noTools,
			NotifyConfig:  notifyCfg,
			SessionID:     ns.ID,
			Context:       tabCtx,
		})

		cfg := tui.Config{
			SessionID: ns.ID,
			Mode:      tui.ModeSingleAgent,
			Engine:    eng,
			Shutdown:  eng.Close,
			Provider:  a.cfg.Provider,
			Model:     a.cfg.Model,
			Pricing:   prices,
			ToggleSubagents: func() string {
				return eng.ToggleSubagents()
			},
		}

		return cfg, eng.Events(), eng.Close, nil
	}
}
