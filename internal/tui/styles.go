package tui

import "github.com/charmbracelet/lipgloss"

// Colors — dark theme palette.
var (
	ColorPrimary   = lipgloss.AdaptiveColor{Light: "63", Dark: "135"}
	ColorSecondary = lipgloss.AdaptiveColor{Light: "241", Dark: "248"}
	ColorDim       = lipgloss.AdaptiveColor{Light: "250", Dark: "241"}
	ColorBorder    = lipgloss.AdaptiveColor{Light: "250", Dark: "237"}
	ColorError     = lipgloss.AdaptiveColor{Light: "196", Dark: "196"}
	ColorUser      = lipgloss.AdaptiveColor{Light: "33", Dark: "81"}
	ColorAssistant = lipgloss.AdaptiveColor{Light: "241", Dark: "252"}
	ColorStreaming = lipgloss.AdaptiveColor{Light: "208", Dark: "214"}
	ColorConnected = lipgloss.AdaptiveColor{Light: "34", Dark: "76"}
)

// Layout styles.
var (
	// SidebarStyle is used for the right info sidebar.
	SidebarStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), false, false, false, true).
			BorderForeground(ColorBorder).
			Padding(1, 1)

	// ChatAreaStyle is used for the main chat message area.
	ChatAreaStyle = lipgloss.NewStyle().
			Padding(0, 1)

	// InputAreaStyle is used for the message input region.
	InputAreaStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), true, false, false, false).
			BorderForeground(ColorBorder).
			Padding(0, 0)

	// HeaderStyle is used for the top header / title bar.
	HeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary).
			Padding(0, 1)

	// DimStyle is used for secondary / hint text.
	DimStyle = lipgloss.NewStyle().
			Foreground(ColorDim)

	// UserMsgStyle styles the "you >" prefix for user messages.
	UserMsgStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorUser)

	// AssistantMsgStyle styles assistant message text.
	AssistantMsgStyle = lipgloss.NewStyle().
				Foreground(ColorAssistant)

	// StreamingStyle styles the streaming indicator.
	StreamingStyle = lipgloss.NewStyle().
			Foreground(ColorStreaming).
			Italic(true)

	// ErrorStyle styles error messages.
	ErrorStyle = lipgloss.NewStyle().
			Foreground(ColorError).
			Bold(true)

	// SidebarHeaderStyle styles section headers in the sidebar.
	SidebarHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorPrimary)

	// SidebarLabelStyle styles stat labels in the sidebar.
	SidebarLabelStyle = lipgloss.NewStyle().
				Foreground(ColorDim)

	// SidebarValueStyle styles stat values in the sidebar.
	SidebarValueStyle = lipgloss.NewStyle().
				Foreground(ColorSecondary)

	// ConnectedStyle styles the "Connected" status.
	ConnectedStyle = lipgloss.NewStyle().
			Foreground(ColorConnected)
)
