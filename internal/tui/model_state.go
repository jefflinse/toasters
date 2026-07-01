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

// nodesState holds all state for the master-detail nodes screen: a scrollable
// list of runtime worker sessions on the left and a tabbed detail pane (Output /
// Prompt / Stats) for the selected node on the right.
type nodesState struct {
	show bool

	// List (master) state.
	sel        int // selected index into the filtered node list
	listScroll int // row offset of the list viewport (rows, not items)

	// focusDetail moves keyboard focus between the list (false) and the detail
	// pane (true). Selection still tracks in the list while the detail is
	// focused; the detail always shows the selected node.
	focusDetail bool

	// Detail (cockpit) state, applied to the selected node.
	tab          cockpitTab
	tabScroll    [cockpitTabCount]int // per-tab scroll offset
	userScrolled bool                 // Output tab: suppresses auto-tail after a manual scroll

	// confirmKill gates the destructive "kill worker" action behind an
	// Enter/Esc confirmation. It kills the selected node.
	confirmKill bool

	// filterActive is true while "/" capture is on; filterQuery narrows the
	// listed sessions by job id / role / status (case-insensitive substring).
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

// cockpitTab identifies which tab of the node detail pane is shown.
type cockpitTab int

const (
	cockpitTabOutput cockpitTab = iota
	cockpitTabPrompt
	cockpitTabStats
	cockpitTabCount // sentinel: number of tabs
)

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
