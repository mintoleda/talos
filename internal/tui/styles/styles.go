package styles

import "github.com/charmbracelet/lipgloss"

var (
	FilePickerSelected = lipgloss.NewStyle().
				Background(lipgloss.Color("236")).
				Foreground(lipgloss.Color("254")).
				Bold(true)
	FilePickerDir = lipgloss.NewStyle().
			Foreground(lipgloss.Color("111"))
	FilePickerFile = lipgloss.NewStyle().
			Foreground(lipgloss.Color("251"))
	FilePickerHint = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Italic(true)

	UserStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	AssistantStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	DimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	ErrorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	ThinkStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("248")).Italic(true)

	ToolRunningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("221"))
	ToolOKStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	ToolErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	// ToolNameStyle is the calm accent used for the tool's name (the verb).
	ToolNameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	// ToolArgStyle dims the call descriptor (path, command, query) so the name
	// and status icon stay the focal points.
	ToolArgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	// ToolHeadingStyle labels the expanded-output panel.
	ToolHeadingStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Bold(true)
	ToolSelectedStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("236")).
				Foreground(lipgloss.Color("254")).
				Bold(true)
	// ToolCursorStyle colors the ▌ gutter bar on the selected row.
	ToolCursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))

	StatusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("235")).
			Foreground(lipgloss.Color("251"))

	ThinkingLineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("146"))
	ThinkingMsgStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("183")).Italic(true)

	StatusSepStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	StatusDirStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	StatusModelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Bold(true)
	StatusLevelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("221"))
	StatusTimeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	StatusTokStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	StatusPctStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("221"))
	StatusCostStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	StatusOutStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("251"))
	StatusMissStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	StatusMCPStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("140"))
	StatusPermStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))

	DialogBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("39")).
			Padding(1, 2).
			Background(lipgloss.Color("235"))

	DialogTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("39"))

	InputStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("240"))

	InputStyleDim = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("237"))

	VimNormalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("220")).
			Bold(true)

	VimInsertStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("82")).
			Bold(true)

	BatchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))

	PaneStyle = lipgloss.NewStyle().
			Padding(0, 1)

	// PaneSepStyle colors the vertical rule between the chat and tools panes.
	PaneSepStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))

	// ToolPaneTitleStyle is the small header shown atop the tools pane.
	ToolPaneTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("246")).
				Bold(true)

	// ToolPaneFocusedTitleStyle highlights the pane title when that pane has focus.
	ToolPaneFocusedTitleStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("39")).
					Bold(true)

	TabActiveStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("235")).
			Background(lipgloss.Color("39")).
			Padding(0, 1)

	TabInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("251")).
				Padding(0, 1)
)
