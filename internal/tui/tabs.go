package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mintoleda/talos/internal/protocol"
	"github.com/mintoleda/talos/internal/tui/styles"
)

// TabEventMsg routes a protocol.Event to a specific tab by ID.
type TabEventMsg struct {
	TabID int
	E     protocol.Event
}

// newTabReadyMsg is returned by the new-tab Cmd once setup completes.
type newTabReadyMsg struct {
	id      int
	model   Model
	eventCh <-chan protocol.Event
	err     error
}

// tabState holds one tab's runtime model and event source.
type tabState struct {
	id      int
	model   Model
	title   string
	eventCh <-chan protocol.Event
}

// NewTabFunc creates the engine and channels for a new tab.
// ctx is a lifecycle context; tabID is the unique tab identifier.
// Returns the Config for the tab's Model, a read-only event channel, and any error.
type NewTabFunc func(ctx context.Context, tabID int) (Config, <-chan protocol.Event, error)

// TabsModel is the root BubbleTea model when tabs are enabled.
type TabsModel struct {
	tabs   []tabState
	active int
	nextID int
	width  int
	height int
	ctx    context.Context
	newTab NewTabFunc
}

// NewTabsModel creates a TabsModel with one initial tab.
// initialEventCh is the event channel for the initial tab's engine goroutines.
func NewTabsModel(ctx context.Context, initialCfg Config, initialEventCh <-chan protocol.Event, newTab NewTabFunc) TabsModel {
	tab := tabState{
		id:      0,
		model:   NewModel(initialCfg),
		title:   "1",
		eventCh: initialEventCh,
	}
	return TabsModel{
		tabs:   []tabState{tab},
		active: 0,
		nextID: 1,
		ctx:    ctx,
		newTab: newTab,
	}
}

// waitForTabEvent is an idiomatic BubbleTea Cmd that blocks until the next
// event arrives from a tab's engine goroutine, then delivers it as TabEventMsg.
func waitForTabEvent(eventCh <-chan protocol.Event, tabID int) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-eventCh
		if !ok {
			return nil
		}
		return TabEventMsg{TabID: tabID, E: e}
	}
}

func (m TabsModel) tabBarH() int {
	if len(m.tabs) <= 1 {
		return 0
	}
	return 1
}

func (m TabsModel) Init() tea.Cmd {
	var cmds []tea.Cmd
	for _, tab := range m.tabs {
		cmds = append(cmds, tab.model.Init())
		cmds = append(cmds, waitForTabEvent(tab.eventCh, tab.id))
	}
	return tea.Batch(cmds...)
}

func (m TabsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		sub := tea.WindowSizeMsg{Width: msg.Width, Height: msg.Height - m.tabBarH()}
		for i := range m.tabs {
			updated, _ := m.tabs[i].model.Update(sub)
			m.tabs[i].model = updated.(Model)
		}
		return m, nil

	case TabEventMsg:
		for i := range m.tabs {
			if m.tabs[i].id == msg.TabID {
				updated, cmd := m.tabs[i].model.Update(EventMsg{E: msg.E})
				m.tabs[i].model = updated.(Model)
				return m, tea.Batch(cmd, waitForTabEvent(m.tabs[i].eventCh, msg.TabID))
			}
		}
		return m, nil

	case newTabReadyMsg:
		if msg.err != nil {
			m.tabs[m.active].model.chat = m.tabs[m.active].model.chat.AppendNotice("error", "new tab: "+msg.err.Error())
			return m, nil
		}
		wasOne := len(m.tabs) == 1
		// Size the new model.
		tbH := 1 // will have 2+ tabs after this add
		sub := tea.WindowSizeMsg{Width: m.width, Height: m.height - tbH}
		sized, _ := msg.model.Update(sub)
		sizedModel := sized.(Model)
		m.tabs = append(m.tabs, tabState{
			id:      msg.id,
			model:   sizedModel,
			title:   fmt.Sprintf("%d", len(m.tabs)+1),
			eventCh: msg.eventCh,
		})
		m.active = len(m.tabs) - 1
		// Resize existing tabs now that the tab bar has appeared.
		if wasOne {
			for i := range m.tabs[:len(m.tabs)-1] {
				updated, _ := m.tabs[i].model.Update(sub)
				m.tabs[i].model = updated.(Model)
			}
		}
		return m, tea.Batch(sizedModel.Init(), waitForTabEvent(msg.eventCh, msg.id))

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+n":
			if m.newTab == nil {
				return m, nil
			}
			tabID := m.nextID
			m.nextID++
			return m, m.spawnTab(tabID)
		case "ctrl+w":
			if len(m.tabs) == 1 {
				return m, tea.Quit
			}
			m.tabs = append(m.tabs[:m.active], m.tabs[m.active+1:]...)
			if m.active >= len(m.tabs) {
				m.active = len(m.tabs) - 1
			}
			// If we're back to one tab, reclaim the tab bar row.
			if len(m.tabs) == 1 {
				sub := tea.WindowSizeMsg{Width: m.width, Height: m.height}
				updated, _ := m.tabs[0].model.Update(sub)
				m.tabs[0].model = updated.(Model)
			}
			return m, nil
		case "alt+l":
			if len(m.tabs) > 1 {
				m.active = (m.active + 1) % len(m.tabs)
			}
			return m, nil
		case "alt+h":
			if len(m.tabs) > 1 {
				m.active = (m.active - 1 + len(m.tabs)) % len(m.tabs)
			}
			return m, nil
		case "alt+1", "alt+2", "alt+3", "alt+4", "alt+5",
			"alt+6", "alt+7", "alt+8", "alt+9":
			digit := int(msg.String()[len(msg.String())-1] - '0')
			idx := digit - 1
			if idx >= 0 && idx < len(m.tabs) {
				m.active = idx
			}
			return m, nil
		}
	}

	// Forward all other messages to the active tab.
	if len(m.tabs) > 0 {
		updated, cmd := m.tabs[m.active].model.Update(msg)
		m.tabs[m.active].model = updated.(Model)
		return m, cmd
	}
	return m, nil
}

func (m TabsModel) spawnTab(tabID int) tea.Cmd {
	ctx := m.ctx
	fn := m.newTab
	return func() tea.Msg {
		cfg, eventCh, err := fn(ctx, tabID)
		if err != nil {
			return newTabReadyMsg{id: tabID, err: err}
		}
		return newTabReadyMsg{
			id:      tabID,
			model:   NewModel(cfg),
			eventCh: eventCh,
		}
	}
}

func (m TabsModel) View() string {
	if len(m.tabs) == 0 {
		return ""
	}
	if len(m.tabs) == 1 {
		return m.tabs[0].model.View()
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.renderTabBar(), m.tabs[m.active].model.View())
}

func (m TabsModel) renderTabBar() string {
	var b strings.Builder
	for i, tab := range m.tabs {
		label := tab.title
		if m.tabs[i].model.busy {
			label += " ●"
		}
		if i == m.active {
			b.WriteString(styles.TabActiveStyle.Render(label))
		} else {
			b.WriteString(styles.TabInactiveStyle.Render(label))
		}
	}
	// Right-align the keybinding hint.
	hint := styles.DimStyle.Render("ctrl+n:new  ctrl+w:close  alt+h/l:switch")
	content := b.String()
	contentW := lipgloss.Width(content)
	hintW := lipgloss.Width(hint)
	gap := m.width - contentW - hintW
	if gap > 0 {
		content += strings.Repeat(" ", gap) + hint
	}
	return content
}
