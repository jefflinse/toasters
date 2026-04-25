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
	// No left padding: user-message block borders and assistant indents
	// render from col 0 so the block's left border (▌) lines up with the
	// input area's thick left border (┃).
	ChatAreaStyle = lipgloss.NewStyle().
			Padding(1, 1, 0, 0)

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
	// Left padding of 1 puts text inside the block at col 2 (border ▌ at col
	// 0, space at col 1, text at col 2) so it aligns with where the user's
	// input cursor sits in the input area.
	UserMsgBlockStyle = lipgloss.NewStyle().
				Background(ColorUserBg).
				Foreground(compat.AdaptiveColor{Light: lipgloss.Color("232"), Dark: lipgloss.Color("252")}).
				Border(lipgloss.ThickBorder(), false, false, false, true).
				BorderForeground(ColorUserBorder).
				Padding(1, 2, 1, 1)

	// AssistantMsgStyle styles assistant message text.
	AssistantMsgStyle = lipgloss.NewStyle().
				Foreground(ColorAssistant)

	// AssistantMsgIndent is the left padding (in columns) applied to assistant
	// messages so their text lands at the same column as the input area's
	// text cursor (thick border + one space of padding = col 2).
	AssistantMsgIndent = 2

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

	// OperatorMetaStyle styles the status strip shown near the input during an active operator stream.
	OperatorMetaStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("250")).
				Italic(true)

	// OperatorBylineStyle styles the dim byline shown above completed operator responses.
	OperatorBylineStyle = lipgloss.NewStyle().
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
	// Uses a hidden border so layout metrics (border size + padding) match
	// FocusedPaneStyle and focus changes don't shift surrounding content.
	UnfocusedPaneStyle = lipgloss.NewStyle().
				Border(lipgloss.HiddenBorder()).
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

	// ModalStyle wraps the entire modal overlay.
	ModalStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorAccent).
			Padding(0, 1)

	// ModalPanelStyle styles an unfocused panel within a modal. Pure
	// padding (no border) whose frame dimensions match ModalFocusedPanel
	// so focus flips don't shift surrounding content. Plain padding
	// plays nicely with .Background() when screens tint themselves —
	// the padding cells reliably paint with the style's bg, which
	// HiddenBorder glyphs do not in lipgloss v2.
	//
	// Horizontal frame: 2+2 = 4. Vertical frame: 1+1 = 2. Matches the
	// focused style below (1 border + 1 padding on each side).
	ModalPanelStyle = lipgloss.NewStyle().
			Padding(1, 2)

	// ModalFocusedPanel styles the focused panel within a modal.
	ModalFocusedPanel = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorAccent).
				Padding(0, 1)

	// ModalSelectedStyle highlights the selected item in a modal panel.
	ModalSelectedStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#333333")).
				Bold(true)

	// JobsScreenBgStyle tints the entire Jobs screen a subtle navy so it
	// reads as visually distinct from the main screen. Kept low-saturation
	// so syntax/status colors remain legible over it.
	JobsScreenBgStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#0d1b2a"))

	// ModalReadOnlyStyle dims read-only entries in a modal.
	ModalReadOnlyStyle = lipgloss.NewStyle().
				Foreground(ColorDim)

	// ModalWarningStyle styles delete-confirmation warnings in a modal.
	ModalWarningStyle = lipgloss.NewStyle().
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

	// DB task status indicator styles (for SQLite-backed progress display).
	dbTaskPendingStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	dbTaskInProgressStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")) // cyan
	dbTaskCompletedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	dbTaskFailedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // red
	dbTaskBlockedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // yellow
	dbTaskCancelledStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// JobBlockStyle is the base style for a job-update chat block. The
	// border color is swapped per job status at render time.
	JobBlockStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(0, 1)

	// Status-accented border colors for the job block.
	JobBlockBorderActive    = compat.AdaptiveColor{Light: lipgloss.Color("39"), Dark: lipgloss.Color("39")}   // cyan
	JobBlockBorderDone      = compat.AdaptiveColor{Light: lipgloss.Color("34"), Dark: lipgloss.Color("76")}   // green
	JobBlockBorderFailed    = compat.AdaptiveColor{Light: lipgloss.Color("196"), Dark: lipgloss.Color("196")} // red
	JobBlockBorderBlocked   = compat.AdaptiveColor{Light: lipgloss.Color("208"), Dark: lipgloss.Color("214")} // amber
	JobBlockBorderPaused    = compat.AdaptiveColor{Light: lipgloss.Color("245"), Dark: lipgloss.Color("245")} // grey
	JobBlockBorderCancelled = compat.AdaptiveColor{Light: lipgloss.Color("240"), Dark: lipgloss.Color("240")} // dim

	// JobBlockTitleStyle emphasizes the job title.
	JobBlockTitleStyle = lipgloss.NewStyle().Bold(true)
	// JobBlockMetaStyle dims the second-line metadata.
	JobBlockMetaStyle = lipgloss.NewStyle().Foreground(ColorDim)
	// JobBlockStatusDoneStyle styles the word "done" / "complete".
	JobBlockStatusDoneStyle = lipgloss.NewStyle().Foreground(JobBlockBorderDone).Bold(true)
	// JobBlockStatusFailedStyle styles failure words in the header.
	JobBlockStatusFailedStyle = lipgloss.NewStyle().Foreground(JobBlockBorderFailed).Bold(true)
	// JobBlockStatusBlockedStyle styles the word "blocked".
	JobBlockStatusBlockedStyle = lipgloss.NewStyle().Foreground(JobBlockBorderBlocked).Bold(true)
	// JobBlockStatusActiveStyle styles the word "running".
	JobBlockStatusActiveStyle = lipgloss.NewStyle().Foreground(JobBlockBorderActive)

	// Activity feed entry styles.
	FeedSystemEventStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Italic(true)
	FeedConsultationTraceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Italic(true)
	FeedTaskStartedStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("14")) // cyan
	FeedTaskCompletedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	FeedTaskFailedStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	FeedBlockerReportedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true) // yellow/bold
	FeedJobCompleteStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true) // green/bold
)
