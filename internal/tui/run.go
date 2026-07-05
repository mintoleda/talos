package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mintoleda/talos/internal/loop"
	"github.com/mintoleda/talos/internal/notify"
	"github.com/mintoleda/talos/internal/pricing"
	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/safety"
	"github.com/mintoleda/talos/internal/tui/dialogs"
)

func QuitOnSignal(p *tea.Program) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	go func() {
		<-ch
		p.Quit()
	}()
}

// StartEngine launches the goroutines that drive a loop and returns the
// channels needed to wire them into a TUI model Config.
func StartEngine(ctx context.Context, lp *loop.Loop, cp *safety.Checkpointer, notifyCfg notify.Config) (
	inputCh chan<- []protocol.ContentBlock,
	steerQueue *SteerQueue,
	interruptCh chan<- struct{},
	compactCh chan<- string,
	eventCh <-chan protocol.Event,
	cleanup func(),
) {
	evCh := make(chan protocol.Event, 64)
	inCh := make(chan []protocol.ContentBlock, 1)
	sq := &SteerQueue{}
	lp.SteerFunc = sq.Drain
	intCh := make(chan struct{}, 1)
	cmpCh := make(chan string, 1)
	rawEmit := func(e protocol.Event) { evCh <- e }
	emit := notify.Wrap(rawEmit, notifyCfg)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for blocks := range inCh {
			if cp != nil {
				_, _ = cp.Snapshot("before-run")
			}
			turnCtx, cancel := context.WithCancel(ctx)
			go func() {
				select {
				case <-intCh:
					cancel()
				case <-turnCtx.Done():
				}
			}()
			if err := lp.RunTurn(turnCtx, blocks, emit); err != nil {
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

	wg.Add(1)
	go func() {
		defer wg.Done()
		for focus := range cmpCh {
			compactCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
			summary, err := lp.CompactNow(compactCtx, focus)
			cancel()
			if err != nil {
				emit(protocol.Notice{Level: "error", Text: fmt.Sprintf("/compact failed: %v", err)})
			} else if summary == "" {
				emit(protocol.Notice{Level: "info", Text: "nothing to compact"})
			} else {
				emit(protocol.Notice{Level: "info", Text: fmt.Sprintf("compacted oldest chunk - summary: %s", summary)})
			}
		}
	}()

	cleanup = func() {
		close(inCh)
		close(cmpCh)
		close(evCh)
		wg.Wait()
	}

	return inCh, sq, intCh, cmpCh, evCh, cleanup
}

// RunTabs starts the TUI in multi-tab mode. initial is the fully-wired Config
// for the first tab (with InputCh, InterruptCh, CompactCh set). initialEventCh
// is the event channel returned by StartEngine for that tab. newTab is the
// factory used to spawn additional tabs on ctrl+n.
func RunTabs(ctx context.Context, initial Config, initialEventCh <-chan protocol.Event, newTab NewTabFunc) error {
	m := NewTabsModel(ctx, initial, initialEventCh, newTab)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	QuitOnSignal(p)
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	m.ShutdownAll()
	return nil
}

// Run starts the TUI for single-agent mode. It bridges the loop to Bubble Tea
// via channels, running the engine in a background goroutine.
func Run(
	ctx context.Context,
	lp *loop.Loop,
	cp *safety.Checkpointer,
	sessionID string,
	providerName string,
	modelName string,
	newSession func() (string, error),
	stats func() string,
	resumeSession func(id string) (string, []protocol.FrozenMessage, error),
	switchProvider func(name, model string) error,
	cycleThinking func() string,
	currentThinkingLevel func() string,
	deleteSession func(id string) error,
	fetchSessions dialogs.FetchSessionsFunc,
	fetchModels dialogs.FetchModelsFunc,
	loginProviders func() []dialogs.LoginProvider,
	saveLogin func(provider, key string) error,
	cancelSubagent func(id string),
	pricingTable *pricing.Table,
	initialHistory []protocol.FrozenMessage,
) error {
	eventCh := make(chan protocol.Event, 64)
	inputCh := make(chan []protocol.ContentBlock, 1)
	steerQueue := &SteerQueue{}
	lp.SteerFunc = steerQueue.Drain
	interruptCh := make(chan struct{}, 1)
	compactCh := make(chan string, 1)

	emit := func(e protocol.Event) { eventCh <- e }

	// Seed the TUI's cumulative counters from the loop's restored stats so
	// resumed sessions show historical token/cost data on the status bar.
	ls := lp.Stats()
	cacheMiss := ls.InputTokens - ls.CachedTokens
	seedCost := 0.0
	if pricingTable != nil && modelName != "" {
		seedCost = pricingTable.Cost(modelName, ls.InputTokens, ls.OutputTokens)
	}

	seedStats := struct {
		InputTokens  int
		OutputTokens int
		CacheMiss    int
		Cost         float64
	}{
		InputTokens:  ls.InputTokens,
		OutputTokens: ls.OutputTokens,
		CacheMiss:    cacheMiss,
		Cost:         seedCost,
	}

	m := NewModel(Config{
		SessionID:     sessionID,
		Mode:          ModeSingleAgent,
		InputCh:       inputCh,
		SteerQueue:    steerQueue,
		InterruptCh:   interruptCh,
		CompactCh:     compactCh,
		NewSession:    newSession,
		Stats:         stats,
		ResumeSession: resumeSession,
		Provider:      providerName,
		Model:         modelName,
		SwitchProvider: switchProvider,
		CycleThinking:        cycleThinking,
		CurrentThinkingLevel: currentThinkingLevel,
		DeleteSession:        deleteSession,
		FetchSessions: fetchSessions,
		FetchModels:   fetchModels,
		LoginProviders: loginProviders,
		SaveLogin:      saveLogin,
		CancelSubagent: cancelSubagent,
		Pricing:        pricingTable,
		InitialHistory: initialHistory,
		SeedStats:      seedStats,
		StatsSnapshot: func() (int, int, int, float64) {
			s := lp.Stats()
			cm := s.InputTokens - s.CachedTokens
			c := 0.0
			if pricingTable != nil && modelName != "" {
				c = pricingTable.Cost(modelName, s.InputTokens, s.OutputTokens)
			}
			return s.InputTokens, s.OutputTokens, cm, c
		},
	})
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	QuitOnSignal(p)

	var producerWg sync.WaitGroup
	var bridgeWg sync.WaitGroup

	bridgeWg.Add(1)
	go func() {
		defer bridgeWg.Done()
		for e := range eventCh {
			p.Send(EventMsg{E: e})
		}
	}()

	producerWg.Add(1)
	go func() {
		defer producerWg.Done()
		for blocks := range inputCh {
			if cp != nil {
				_, _ = cp.Snapshot("before-run")
			}
			turnCtx, cancel := context.WithCancel(ctx)
			go func() {
				select {
				case <-interruptCh:
					cancel()
				case <-turnCtx.Done():
				}
			}()
			if err := lp.RunTurn(turnCtx, blocks, emit); err != nil {
				if errors.Is(err, context.Canceled) {
					emit(protocol.Notice{Level: "warn", Text: "interrupted"})
				} else {
					emit(protocol.Notice{Level: "error", Text: err.Error()})
				}
				// Only emit TurnEnded on error — the loop emits it with usage on success.
				emit(protocol.TurnEnded{})
			}
			cancel()
		}
	}()

	producerWg.Add(1)
	go func() {
		defer producerWg.Done()
		for focus := range compactCh {
			compactCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
			summary, err := lp.CompactNow(compactCtx, focus)
			cancel()
			if err != nil {
				emit(protocol.Notice{Level: "error", Text: fmt.Sprintf("/compact failed: %v", err)})
			} else if summary == "" {
				emit(protocol.Notice{Level: "info", Text: "nothing to compact"})
			} else {
				msg := fmt.Sprintf("compacted oldest chunk - summary: %s", summary)
				emit(protocol.Notice{Level: "info", Text: msg})
			}
		}
	}()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}

	close(inputCh)
	close(compactCh)
	producerWg.Wait()
	close(eventCh)
	bridgeWg.Wait()
	return nil
}
