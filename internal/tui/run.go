package tui

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mintoleda/talos/internal/client"
	"github.com/mintoleda/talos/internal/pricing"
	"github.com/mintoleda/talos/internal/protocol"
)

func QuitOnSignal(p *tea.Program) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	go func() {
		<-ch
		p.Quit()
	}()
}

// RunTabs starts the TUI in multi-tab mode. initial is the fully-wired Config
// for the first tab. initialEventCh is that tab's engine event stream. newTab
// is the factory used to spawn additional tabs on ctrl+n.
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
// via the client-facing Engine interface.
func Run(
	ctx context.Context,
	engine client.Engine,
	sessionID string,
	providerName string,
	modelName string,
	pricingTable *pricing.Table,
	initialHistory []protocol.FrozenMessage,
) error {
	// Seed the TUI's cumulative counters from the loop's restored stats so
	// resumed sessions show historical token/cost data on the status bar.
	in, out, cacheMiss, seedCost, _ := engine.Stats()

	seedStats := struct {
		InputTokens  int
		OutputTokens int
		CacheMiss    int
		Cost         float64
	}{
		InputTokens:  in,
		OutputTokens: out,
		CacheMiss:    cacheMiss,
		Cost:         seedCost,
	}

	m := NewModel(Config{
		SessionID:      sessionID,
		Mode:           ModeSingleAgent,
		Engine:         engine,
		Provider:       providerName,
		Model:          modelName,
		Pricing:        pricingTable,
		InitialHistory: initialHistory,
		SeedStats:      seedStats,
	})
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	QuitOnSignal(p)

	var bridgeWg sync.WaitGroup

	bridgeWg.Add(1)
	go func() {
		defer bridgeWg.Done()
		for {
			select {
			case e, ok := <-engine.Events():
				if !ok {
					return
				}
				p.Send(EventMsg{E: e})
			case <-ctx.Done():
				return
			}
		}
	}()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}

	engine.Close()
	bridgeWg.Wait()
	return nil
}
