package tui

import (
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

// streamingState holds all state related to the active operator stream.
type streamingState struct {
	streaming        bool
	currentResponse  string
	currentReasoning string
	operatorByline   string // formatted byline for the in-progress operator stream; cleared when done
}

// gridState holds all state for the dynamic NxM worker grid screen.
type gridState struct {
	showGrid      bool
	gridFocusCell int // 0-(cols*rows-1) within current page
	gridPage      int // current page index
	gridCols      int // computed from terminal width
	gridRows      int // computed from terminal height

	// confirmKill gates the destructive "kill worker" action behind an
	// Enter/Esc confirmation, mirroring the jobs modal's confirmCancel.
	confirmKill          bool
	confirmKillSessionID string

	// filterActive is true while "/" capture is on; filterQuery narrows the
	// displayed sessions by job id / role / status (case-insensitive substring).
	filterActive bool
	filterQuery  string
}

// promptModeState holds all state for the interactive prompt mode
// (active when the operator calls ask_user).
type promptModeState struct {
	promptMode     bool
	promptQuestion string
	promptOptions  []string // LLM-provided options; "Custom response..." appended at render time
	promptSelected int      // cursor index
	promptCustom   bool     // true when user selected "Custom response..." and is typing
	requestID      string   // correlates with ask_user request for RespondToPrompt

	// Multi-question round (operator ask_user with several questions). The
	// widget runs as a form: one question shown at a time, ←→ to move between
	// questions (answers persist), Enter to select and advance, Enter on the
	// last question submits a single combined string. promptQuestion/
	// promptOptions always reflect the current question; roundCursor remembers
	// each question's cursor so revisiting shows the prior selection.
	round        []service.PromptQuestion
	roundIndex   int
	roundAnswers []string // committed answer per question (len == len(round))
	roundCursor  []int    // remembered cursor per question (len == len(round))
	source       string   // "" = operator; "graph:<node>" = graph interrupt
	jobID        string   // job the blocker gates (for the byline's job title); "" for operator

	// fromBlocker is true when this round was opened from the Blockers panel
	// (the only entry point now). It distinguishes Esc-to-back-out (leave the
	// blocker pending) from an explicit dismiss: cancelPrompt must not resolve
	// the blocker, only exit prompt mode.
	fromBlocker bool
}

// blockersModalState holds the selection dialog shown when the user opens the
// Blockers panel: a list of pending blockers to choose one to answer.
type blockersModalState struct {
	show bool
	sel  int // cursor index into m.blockers
}

// cockpitTab identifies which pane of the worker cockpit is shown.
type cockpitTab int

const (
	cockpitTabOutput cockpitTab = iota
	cockpitTabPrompt
	cockpitTabStats
	cockpitTabCount // sentinel: number of tabs
)

// cockpitState holds the worker-detail cockpit: a tabbed, scrollable, near-
// fullscreen overlay showing one runtime session's live output, its prompt, and
// its stats. Opened from the grid drill-in; it replaces the separate output and
// prompt modals so a session is inspected on one surface.
type cockpitState struct {
	show      bool
	sessionID string     // runtime session ID being viewed
	tab       cockpitTab // active tab
	// scroll is the per-tab scroll offset so switching tabs preserves position.
	scroll [cockpitTabCount]int
	// userScrolled suppresses the Output tab's auto-tail once the user scrolls
	// up, so live events don't yank the view back to the bottom.
	userScrolled bool
}

// cmdPopupState holds all state for the slash command autocomplete popup.
type cmdPopupState struct {
	show         bool
	filteredCmds []SlashCommand
	selectedIdx  int
}

// scrollState holds all state related to chat viewport scrolling.
type scrollState struct {
	userScrolled     bool      // true when user has manually scrolled up; suppresses auto-scroll
	hasNewMessages   bool      // true when new content arrived while user was scrolled up
	scrollbarVisible bool      // true when scrollbar should be rendered (auto-hides after inactivity)
	lastScrollTime   time.Time // when the last scroll event occurred
}

// progressState holds all state populated by the progress update events.
type progressState struct {
	jobs           []service.Job
	tasks          map[string][]service.Task
	reports        map[string][]service.ProgressReport
	activeSessions []service.WorkerSession
	feedEntries    []service.FeedEntry       // recent activity feed entries
	mcpServers     []service.MCPServerStatus // MCP server connection status
}

// chatState holds all state related to the chat conversation history and
// collapsible message display.
type chatState struct {
	entries           []service.ChatEntry // consolidated chat history (messages, timestamps, reasoning, metadata)
	completionMsgIdx  map[int]bool        // indices of team-completion messages in entries
	expandedMsgs      map[int]bool        // which completion messages are currently expanded
	selectedMsgIdx    int                 // currently selected message index (-1 = none)
	expandedReasoning map[int]bool        // which entry indices have reasoning expanded
	collapsedTools    map[int]bool        // true = expanded; absent/false = collapsed (default)

	// queuedMessages holds user messages entered while the operator is
	// mid-turn. When OperatorDoneMsg arrives, the next queued message is
	// sent automatically.
	queuedMessages []string
}

// activityItem represents a single tool-call activity for display in a runtime worker card.
type activityItem struct {
	label    string // formatted display label, e.g. "write: main.go"
	toolName string // raw tool name
}
