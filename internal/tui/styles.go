package tui

import "github.com/charmbracelet/lipgloss"

// Colors — dark theme palette.
var (
	ColorPrimary         = lipgloss.AdaptiveColor{Light: "63", Dark: "135"}
	ColorSecondary       = lipgloss.AdaptiveColor{Light: "241", Dark: "248"}
	ColorDim             = lipgloss.AdaptiveColor{Light: "250", Dark: "241"}
	ColorBorder          = lipgloss.AdaptiveColor{Light: "250", Dark: "237"}
	ColorError           = lipgloss.AdaptiveColor{Light: "196", Dark: "196"}
	ColorUser            = lipgloss.AdaptiveColor{Light: "33", Dark: "81"}
	ColorUserBg          = lipgloss.AdaptiveColor{Light: "254", Dark: "235"}
	ColorUserBorder      = lipgloss.AdaptiveColor{Light: "33", Dark: "81"}
	ColorAssistant       = lipgloss.AdaptiveColor{Light: "241", Dark: "252"}
	ColorStreaming       = lipgloss.AdaptiveColor{Light: "208", Dark: "214"}
	ColorConnected       = lipgloss.AdaptiveColor{Light: "34", Dark: "76"}
	ColorReasoning       = lipgloss.AdaptiveColor{Light: "240", Dark: "243"}
	ColorReasoningBorder = lipgloss.AdaptiveColor{Light: "245", Dark: "238"}
	ColorReasoningBg     = lipgloss.AdaptiveColor{Light: "253", Dark: "233"}
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
			Padding(1, 1, 0, 1)

	// InputAreaStyle is used for the message input region.
	InputAreaStyle = lipgloss.NewStyle().
			Border(lipgloss.ThickBorder()).
			BorderForeground(lipgloss.Color("51")).
			Padding(0, 1)

	// HeaderStyle is used for the top header / title bar.
	HeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary).
			Padding(0, 1)

	// DimStyle is used for secondary / hint text.
	DimStyle = lipgloss.NewStyle().
			Foreground(ColorDim)

	// UserMsgLabelStyle styles the "you" label above user messages.
	UserMsgLabelStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorUser)

	// UserMsgBlockStyle styles the user message content block.
	UserMsgBlockStyle = lipgloss.NewStyle().
				Background(ColorUserBg).
				Foreground(lipgloss.AdaptiveColor{Light: "232", Dark: "252"}).
				Border(lipgloss.ThickBorder(), false, false, false, true).
				BorderForeground(ColorUserBorder).
				Padding(1, 2)

	// AssistantMsgStyle styles assistant message text.
	AssistantMsgStyle = lipgloss.NewStyle().
				Foreground(ColorAssistant)

	// StreamingStyle styles the streaming indicator.
	StreamingStyle = lipgloss.NewStyle().
			Foreground(ColorStreaming).
			Italic(true)

	// ReasoningStyle styles the "Thinking..." label.
	ReasoningStyle = lipgloss.NewStyle().
			Foreground(ColorReasoning).
			Italic(true)

	// ReasoningHeaderStyle styles the "thinking" label inside the reasoning block.
	ReasoningHeaderStyle = lipgloss.NewStyle().
				Foreground(ColorReasoning).
				Italic(true).
				Bold(false)

	// ReasoningBlockStyle styles the reasoning trace block.
	ReasoningBlockStyle = lipgloss.NewStyle().
				Foreground(ColorReasoning).
				Background(ColorReasoningBg).
				Border(lipgloss.NormalBorder(), false, false, false, true).
				BorderForeground(ColorReasoningBorder).
				Padding(0, 1)

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
