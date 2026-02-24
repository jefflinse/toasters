package tui

import (
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
)

// Colors — dark theme palette.
// compat.AdaptiveColor selects light/dark at runtime based on terminal background.
var (
	ColorPrimary         = compat.AdaptiveColor{Light: lipgloss.Color("63"), Dark: lipgloss.Color("135")}
	ColorSecondary       = compat.AdaptiveColor{Light: lipgloss.Color("241"), Dark: lipgloss.Color("248")}
	ColorDim             = compat.AdaptiveColor{Light: lipgloss.Color("250"), Dark: lipgloss.Color("241")}
	ColorBorder          = compat.AdaptiveColor{Light: lipgloss.Color("250"), Dark: lipgloss.Color("237")}
	ColorError           = compat.AdaptiveColor{Light: lipgloss.Color("196"), Dark: lipgloss.Color("196")}
	ColorUser            = compat.AdaptiveColor{Light: lipgloss.Color("33"), Dark: lipgloss.Color("81")}
	ColorUserBg          = compat.AdaptiveColor{Light: lipgloss.Color("254"), Dark: lipgloss.Color("235")}
	ColorUserBorder      = compat.AdaptiveColor{Light: lipgloss.Color("33"), Dark: lipgloss.Color("81")}
	ColorAssistant       = compat.AdaptiveColor{Light: lipgloss.Color("241"), Dark: lipgloss.Color("252")}
	ColorStreaming       = compat.AdaptiveColor{Light: lipgloss.Color("208"), Dark: lipgloss.Color("214")}
	ColorConnected       = compat.AdaptiveColor{Light: lipgloss.Color("34"), Dark: lipgloss.Color("76")}
	ColorReasoning       = compat.AdaptiveColor{Light: lipgloss.Color("240"), Dark: lipgloss.Color("243")}
	ColorReasoningBorder = compat.AdaptiveColor{Light: lipgloss.Color("245"), Dark: lipgloss.Color("238")}
	ColorReasoningBg     = compat.AdaptiveColor{Light: lipgloss.Color("253"), Dark: lipgloss.Color("233")}
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
	// Thick left accent border (┃), thin top/bottom/right (─/│), proper corners.
	InputAreaStyle = lipgloss.NewStyle().
			Border(lipgloss.Border{
			Top:         "─",
			Bottom:      "─",
			Left:        "┃",
			Right:       "│",
			TopLeft:     "┌",
			TopRight:    "┐",
			BottomLeft:  "└",
			BottomRight: "┘",
		}).
		BorderLeftForeground(lipgloss.Color("51")).
		BorderTopForeground(lipgloss.Color("30")).
		BorderRightForeground(lipgloss.Color("30")).
		BorderBottomForeground(lipgloss.Color("30")).
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
				Foreground(compat.AdaptiveColor{Light: lipgloss.Color("232"), Dark: lipgloss.Color("252")}).
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

	// CmdPopupContainerStyle wraps the entire slash command popup.
	CmdPopupContainerStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("235"))

	// CmdPopupRowStyle styles an unselected row in the command popup.
	CmdPopupRowStyle = lipgloss.NewStyle().
				Padding(0, 1).
				Background(lipgloss.Color("235"))

	// CmdPopupSelectedStyle styles the currently selected row in the command popup.
	CmdPopupSelectedStyle = lipgloss.NewStyle().
				Padding(0, 1).
				Background(lipgloss.Color("238")).
				Bold(true)

	// CmdPopupNameStyle styles the command name in an unselected row.
	CmdPopupNameStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("51")).
				Width(12).
				Background(lipgloss.Color("235"))

	// CmdPopupNameSelectedStyle styles the command name in the selected row.
	CmdPopupNameSelectedStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("51")).
					Width(12).
					Background(lipgloss.Color("238")).
					Bold(true)

	// CmdPopupDescStyle styles the description in an unselected row.
	CmdPopupDescStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245")).
				Background(lipgloss.Color("235"))

	// CmdPopupDescSelectedStyle styles the description in the selected row.
	CmdPopupDescSelectedStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("252")).
					Background(lipgloss.Color("238"))

	// ClaudeMetaStyle styles the status strip shown near the input during an active claude stream.
	ClaudeMetaStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250")).
			Italic(true)

	// ClaudeBylineStyle styles the dim byline shown above completed claude responses.
	ClaudeBylineStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("250")).
				Italic(true)

	// LeftPanelStyle is the outer container for the left panel.
	// No border — individual panes have their own borders.
	LeftPanelStyle = lipgloss.NewStyle()

	// LeftPanelHeaderStyle styles section headers within the left panel.
	LeftPanelHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("51"))

	// FocusedPaneStyle wraps a left-panel pane that currently has keyboard focus.
	FocusedPaneStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorAccent).
				PaddingLeft(1).PaddingRight(1)

	// UnfocusedPaneStyle wraps a left-panel pane that does not have keyboard focus.
	UnfocusedPaneStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorBorder).
				PaddingLeft(1).PaddingRight(1)

	// JobSelectedStyle styles the currently selected job.
	JobSelectedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("51")).
				Bold(true)

	// JobItemStyle styles unselected job items.
	JobItemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	// PlaceholderPaneStyle styles placeholder text in unimplemented panes.
	PlaceholderPaneStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("237")).
				Italic(true)

	// TaskUpdatesPaneStyle styles the task updates section at the bottom of the right sidebar.
	TaskUpdatesPaneStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("237")).
				Italic(true)

	// ColorAccent is the accent color used for focused borders in modals.
	ColorAccent = compat.AdaptiveColor{Light: lipgloss.Color("51"), Dark: lipgloss.Color("51")}

	// TeamsModalStyle wraps the entire teams modal.
	TeamsModalStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorAccent).
			Padding(0, 1)

	// TeamsPanelStyle styles an unfocused panel within the teams modal.
	TeamsPanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder)

	// TeamsFocusedPanel styles the focused panel within the teams modal.
	TeamsFocusedPanel = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorAccent)

	// TeamsSelectedStyle highlights the selected item in a teams panel.
	TeamsSelectedStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#333333")).
				Bold(true)

	// TeamsReadOnlyStyle dims read-only team entries.
	TeamsReadOnlyStyle = lipgloss.NewStyle().
				Foreground(ColorDim)

	// TeamsWarningStyle styles delete-confirmation warnings.
	TeamsWarningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("196")).
				Bold(true)

	// Toast notification styles.
	ToastBaseStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("255")).
			Background(lipgloss.Color("235")).
			Padding(0, 1).
			MaxWidth(40)

	ToastInfoStyle = ToastBaseStyle.
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorPrimary).
			BorderBackground(lipgloss.Color("235"))

	ToastSuccessStyle = ToastBaseStyle.
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorConnected).
				BorderBackground(lipgloss.Color("235"))

	ToastWarningStyle = ToastBaseStyle.
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorStreaming).
				BorderBackground(lipgloss.Color("235"))

	// TaskPendingStyle styles a pending task subitem under a job.
	TaskPendingStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245"))

	// TaskDoneStyle styles a completed task subitem under a job (dimmed).
	TaskDoneStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("237"))

	// TaskBlockedStyle styles the BLOCKED indicator under a job.
	TaskBlockedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("214")).
				Bold(true)
)
