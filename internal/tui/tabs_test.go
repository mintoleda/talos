package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mintoleda/talos/internal/protocol"
)

// stubNewTab returns a NewTabFunc that creates a minimal chat-only tab.
func stubNewTab() NewTabFunc {
	return func(ctx context.Context, tabID int) (Config, <-chan protocol.Event, func(), error) {
		ch := make(chan protocol.Event)
		return Config{
			SessionID: "tab",
			Mode:      ModeSingleAgent,
			Provider:  "test",
			Model:     "test",
		}, ch, func() {}, nil
	}
}

func TestTabsModelStartsWithOneTab(t *testing.T) {
	ctx := context.Background()
	ch := make(chan protocol.Event)
	m := NewTabsModel(ctx, Config{SessionID: "test", Mode: ModeSingleAgent}, ch, nil)

	if len(m.tabs) != 1 {
		t.Fatalf("expected 1 tab, got %d", len(m.tabs))
	}
	if m.active != 0 {
		t.Fatalf("expected active=0, got %d", m.active)
	}
}

func TestTabsModelCreatesNewTab(t *testing.T) {
	ctx := context.Background()
	ch := make(chan protocol.Event)
	m := NewTabsModel(ctx, Config{SessionID: "test", Mode: ModeSingleAgent}, ch, stubNewTab())

	m2, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	tabs := m2.(TabsModel)

	if cmd == nil {
		t.Fatal("expected a command for new tab")
	}

	m3, _ := tabs.Update(newTabReadyMsg{
		id: 1,
		model: NewModel(Config{
			SessionID: "tab-1",
			Mode:      ModeSingleAgent,
		}),
		eventCh: make(chan protocol.Event),
	})
	tabs3 := m3.(TabsModel)

	if len(tabs3.tabs) != 2 {
		t.Fatalf("expected 2 tabs, got %d", len(tabs3.tabs))
	}
	if tabs3.active != 1 {
		t.Fatalf("expected active=1 for new tab, got %d", tabs3.active)
	}
}

func TestTabsModelClosesTab(t *testing.T) {
	ctx := context.Background()
	ch1 := make(chan protocol.Event)
	ch2 := make(chan protocol.Event)

	m := NewTabsModel(ctx, Config{SessionID: "test", Mode: ModeSingleAgent}, ch1, stubNewTab())

	m2, _ := m.Update(newTabReadyMsg{
		id:      1,
		model:   NewModel(Config{SessionID: "tab-1", Mode: ModeSingleAgent}),
		eventCh: ch2,
	})
	tabs := m2.(TabsModel)

	if len(tabs.tabs) != 2 {
		t.Fatalf("expected 2 tabs before close, got %d", len(tabs.tabs))
	}

	m3, _ := tabs.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	tabs2 := m3.(TabsModel)

	if len(tabs2.tabs) != 1 {
		t.Fatalf("expected 1 tab after close, got %d", len(tabs2.tabs))
	}
}

func TestTabsModelCloseLastTabQuits(t *testing.T) {
	ctx := context.Background()
	ch := make(chan protocol.Event)
	m := NewTabsModel(ctx, Config{SessionID: "test", Mode: ModeSingleAgent}, ch, nil)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	if cmd == nil {
		t.Fatal("expected quit cmd when closing last tab")
	}
}

func TestTabsModelSwitchesTabAltL(t *testing.T) {
	ctx := context.Background()
	ch1 := make(chan protocol.Event)
	ch2 := make(chan protocol.Event)

	m := NewTabsModel(ctx, Config{SessionID: "test", Mode: ModeSingleAgent}, ch1, stubNewTab())
	m2, _ := m.Update(newTabReadyMsg{id: 1, model: NewModel(Config{SessionID: "tab", Mode: ModeSingleAgent}), eventCh: ch2})
	tabs := m2.(TabsModel)
	tabs.active = 0

	m3, _ := tabs.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}, Alt: true})
	tabs2 := m3.(TabsModel)
	if tabs2.active != 1 {
		t.Fatalf("expected active=1 after alt+l, got %d", tabs2.active)
	}
}

func TestTabsModelSwitchesTabAltH(t *testing.T) {
	ctx := context.Background()
	ch1 := make(chan protocol.Event)
	ch2 := make(chan protocol.Event)

	m := NewTabsModel(ctx, Config{SessionID: "test", Mode: ModeSingleAgent}, ch1, stubNewTab())
	m2, _ := m.Update(newTabReadyMsg{id: 1, model: NewModel(Config{SessionID: "tab", Mode: ModeSingleAgent}), eventCh: ch2})
	tabs := m2.(TabsModel)
	tabs.active = 1

	m3, _ := tabs.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}, Alt: true})
	tabs2 := m3.(TabsModel)
	if tabs2.active != 0 {
		t.Fatalf("expected active=0 after alt+h, got %d", tabs2.active)
	}
}

func TestTabsModelAltNumberSwitchesTab(t *testing.T) {
	ctx := context.Background()
	ch1 := make(chan protocol.Event)
	ch2 := make(chan protocol.Event)
	ch3 := make(chan protocol.Event)

	m := NewTabsModel(ctx, Config{SessionID: "test", Mode: ModeSingleAgent}, ch1, stubNewTab())
	m2, _ := m.Update(newTabReadyMsg{id: 1, model: NewModel(Config{SessionID: "tab-1", Mode: ModeSingleAgent}), eventCh: ch2})
	m2, _ = m2.Update(newTabReadyMsg{id: 2, model: NewModel(Config{SessionID: "tab-2", Mode: ModeSingleAgent}), eventCh: ch3})
	tabs := m2.(TabsModel)
	tabs.active = 0

	m3, _ := tabs.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}, Alt: true})
	tabs2 := m3.(TabsModel)
	if tabs2.active != 1 {
		t.Fatalf("expected active=1 after alt+2, got %d", tabs2.active)
	}
}

func TestTabsModelAltNumberOutOfRange(t *testing.T) {
	ctx := context.Background()
	ch := make(chan protocol.Event)
	m := NewTabsModel(ctx, Config{SessionID: "test", Mode: ModeSingleAgent}, ch, nil)

	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'9'}, Alt: true})
	tabs := m2.(TabsModel)
	if tabs.active != 0 {
		t.Fatalf("expected active=0, got %d", tabs.active)
	}
}

func TestTabsModelViewSingleTab(t *testing.T) {
	ctx := context.Background()
	ch := make(chan protocol.Event)
	m := NewTabsModel(ctx, Config{SessionID: "test", Mode: ModeSingleAgent}, ch, nil)

	view := m.View()
	if view == "" {
		t.Fatal("expected non-empty view")
	}
}

func TestTabsModelViewMultiTab(t *testing.T) {
	ctx := context.Background()
	ch1 := make(chan protocol.Event)
	ch2 := make(chan protocol.Event)

	m := NewTabsModel(ctx, Config{SessionID: "test", Mode: ModeSingleAgent}, ch1, stubNewTab())
	m2, _ := m.Update(newTabReadyMsg{id: 1, model: NewModel(Config{SessionID: "tab-1", Mode: ModeSingleAgent}), eventCh: ch2})
	tabs := m2.(TabsModel)

	view := tabs.View()
	if view == "" {
		t.Fatal("expected non-empty view with multiple tabs")
	}
}

func TestTabsModelWindowSizeResizesAll(t *testing.T) {
	ctx := context.Background()
	ch1 := make(chan protocol.Event)
	ch2 := make(chan protocol.Event)

	m := NewTabsModel(ctx, Config{SessionID: "test", Mode: ModeSingleAgent}, ch1, stubNewTab())
	m2, _ := m.Update(newTabReadyMsg{id: 1, model: NewModel(Config{SessionID: "tab-1", Mode: ModeSingleAgent}), eventCh: ch2})

	m3, _ := m2.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	tabs := m3.(TabsModel)

	for i, tab := range tabs.tabs {
		if tab.model.width != 120 {
			t.Fatalf("tab %d: expected width=120, got %d", i, tab.model.width)
		}
	}
}

func TestTabsModelTabBarHeight(t *testing.T) {
	ctx := context.Background()
	ch := make(chan protocol.Event)
	m := NewTabsModel(ctx, Config{SessionID: "test", Mode: ModeSingleAgent}, ch, nil)

	if h := m.tabBarH(); h != 0 {
		t.Fatalf("expected tabBarH=0 for single tab, got %d", h)
	}

	m2, _ := m.Update(newTabReadyMsg{id: 1, model: NewModel(Config{SessionID: "tab-1", Mode: ModeSingleAgent}), eventCh: make(chan protocol.Event)})
	tabs := m2.(TabsModel)
	if h := tabs.tabBarH(); h != 1 {
		t.Fatalf("expected tabBarH=1 for multiple tabs, got %d", h)
	}
}

func TestTabsModelNewTabOnSingleTabResizesOld(t *testing.T) {
	ctx := context.Background()
	ch := make(chan protocol.Event)
	m := NewTabsModel(ctx, Config{SessionID: "test", Mode: ModeSingleAgent}, ch, stubNewTab())
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	m3, _ := m2.Update(newTabReadyMsg{id: 1, model: NewModel(Config{SessionID: "tab-1", Mode: ModeSingleAgent}), eventCh: make(chan protocol.Event)})
	tabs := m3.(TabsModel)

	if tabs.tabs[0].model.height != 29 {
		t.Fatalf("expected height=29 for first tab after second appears, got %d", tabs.tabs[0].model.height)
	}
}

func TestTabsModelInit(t *testing.T) {
	ctx := context.Background()
	ch := make(chan protocol.Event)
	m := NewTabsModel(ctx, Config{SessionID: "test", Mode: ModeSingleAgent}, ch, nil)

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("expected Init to return a command")
	}
}

func contains(s, substr string) bool {
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
