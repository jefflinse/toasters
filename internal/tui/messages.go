// Message types: Bubble Tea message definitions, tick/timer commands, toast notifications, and shared type declarations.
package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// Toast notification types.
type toastLevel int

const (
	toastInfo toastLevel = iota
	toastSuccess
	toastWarning
)

type toast struct {
	message   string
	level     toastLevel
	createdAt time.Time
	id        int // unique ID for dismissal
}

// dismissToastMsg is sent after a delay to remove a specific toast.
type dismissToastMsg struct{ id int }

// dismissToast returns a tea.Cmd that fires dismissToastMsg after 3 seconds.
func dismissToast(id int) tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return dismissToastMsg{id: id}
	})
}

// addToast appends a new toast notification and returns a command to auto-dismiss it.
func (m *Model) addToast(message string, level toastLevel) tea.Cmd {
	t := toast{message: message, level: level, createdAt: time.Now(), id: m.nextToastID}
	m.nextToastID++
	m.toasts = append(m.toasts, t)
	// Limit to 5 visible toasts.
	if len(m.toasts) > 5 {
		m.toasts = m.toasts[len(m.toasts)-5:]
	}
	return dismissToast(t.id)
}

// focusedPanel identifies which panel currently holds keyboard focus.
type focusedPanel int

const (
	focusChat     focusedPanel = iota
	focusJobs     focusedPanel = iota
	focusAgents   focusedPanel = iota
	focusOperator focusedPanel = iota
	focusMCP      focusedPanel = iota
)

// SessionStats tracks session-level statistics displayed in the sidebar.
type SessionStats struct {
	ModelName            string
	Endpoint             string
	Connected            bool
	ContextLength        int // max context window in tokens (0 if unknown)
	MessageCount         int
	PromptTokens         int // current context size in tokens (latest API-reported value)
	CompletionTokens     int // total completion tokens generated across all responses
	ReasoningTokens      int // accumulated reasoning tokens across all turns
	CompletionTokensLive int // estimated completion tokens for the in-progress response
	ReasoningTokensLive  int // estimated reasoning tokens for the in-progress response
	SystemPromptTokens   int // estimated token count of the system prompt
	LastResponseTime     time.Duration
	ResponseStart        time.Time
	TotalResponses       int           // number of completed responses (for avg calc)
	TotalResponseTime    time.Duration // sum of all response times (for avg calc)
}

// estimateTokens returns a rough token count for a string (~4 chars per token).
func estimateTokens(s string) int {
	n := len(s)
	if n == 0 {
		return 0
	}
	return (n + 3) / 4 // ceiling division
}

// progressPollMsg carries the latest progress snapshot from the service event
// stream. It is produced by the event consumer in response to progress.update
// events. Note: it no longer carries runtime session info — those flow through
// dedicated session.* events into m.runtimeSessions.
type progressPollMsg struct {
	Jobs        []service.Job
	Tasks       map[string][]service.Task
	Progress    map[string][]service.ProgressReport
	Sessions    []service.WorkerSession
	FeedEntries []service.FeedEntry       // recent activity feed entries
	MCPServers  []service.MCPServerStatus // MCP server connection status
}

// progressPollTickMsg is an internal tick that triggers the next poll.
type progressPollTickMsg struct{}

// Message types for the Bubble Tea event loop.

type ModelsMsg struct {
	Models []service.ModelInfo
	Err    error
}

// GraphsMsg carries the current catalog of loaded graph definitions. Produced
// by fetchGraphs (called at startup and when a graph definition file changes).
// The TUI caches these by id to resolve task graph_id → topology when
// rendering the graph map.
type GraphsMsg struct {
	Graphs []service.GraphDefinition
	Err    error
}

// SessionStartedMsg is sent when a worker session starts on the server.
// Produced by the event consumer in response to a session.started event.
type SessionStartedMsg struct {
	SessionID      string
	WorkerName     string
	Task           string // short human-readable description of what this worker is doing
	JobID          string
	TaskID         string
	SystemPrompt   string
	InitialMessage string
}

// SessionTextMsg carries a chunk of streamed text from an agent session.
// Produced by the event consumer in response to a session.text event.
type SessionTextMsg struct {
	SessionID string
	Text      string
}

// SessionToolCallMsg is sent when an agent session invokes a tool.
// Produced by the event consumer in response to a session.tool_call event.
type SessionToolCallMsg struct {
	SessionID string
	ToolID    string
	ToolName  string
	ToolInput string // raw JSON arguments
}

// SessionToolResultMsg is sent when a tool result returns to an agent session.
// Produced by the event consumer in response to a session.tool_result event.
type SessionToolResultMsg struct {
	SessionID  string
	CallID     string
	ToolName   string
	ToolOutput string
	IsError    bool
}

// SessionDoneMsg is sent when a worker session terminates.
// Produced by the event consumer in response to a session.done event.
type SessionDoneMsg struct {
	SessionID  string
	WorkerName string
	JobID      string
	TaskID     string
	FinalText  string
	Status     string // "completed", "failed", "cancelled"
}

// GraphNodeStartedMsg is sent when a rhizome graph node begins executing.
// The TUI renders it as a pseudo-worker in the Workers panel — graph nodes
// don't create runtime.Session instances (they're stateless), so the
// existing session.started pathway sees nothing for graph-based tasks.
type GraphNodeStartedMsg struct {
	SessionID string // synthesized as "graph:<TaskID>:<Node>"
	Node      string // e.g. "investigate", "plan"
	JobID     string
	TaskID    string
}

// GraphNodeDoneMsg is sent when a rhizome graph node finishes executing.
// Status comes from TaskState.Status — "tests_passed", "review_approved",
// "review_rejected", or empty for nodes that don't set one.
type GraphNodeDoneMsg struct {
	SessionID string
	Node      string
	JobID     string
	TaskID    string
	Status    string
}

// JobsReloadedMsg is sent when jobs are reloaded (e.g. from SQLite polling).
type JobsReloadedMsg struct {
	Jobs []service.Job
}

// AppReadyMsg is sent when the app has finished loading and is ready to start.
// It carries the operator metadata fetched from the server so the sidebar
// (model name, endpoint URL) can be populated before any LLM calls happen.
type AppReadyMsg struct {
	Greeting         string              // pre-fetched operator greeting; injected immediately on render
	ModelName        string              // canonical operator model name from the server config
	Endpoint         string              // operator's LLM provider endpoint URL
	History          []service.ChatEntry // persisted chat history from server (oldest first)
	OperatorDisabled bool                // true if no operator provider is configured
}

// loadingTickMsg drives the loading screen animation.
type loadingTickMsg struct{}

// loadingTick returns a command that fires loadingTickMsg after 150ms.
func loadingTick() tea.Cmd {
	return tea.Tick(30*time.Millisecond, func(time.Time) tea.Msg {
		return loadingTickMsg{}
	})
}

// spinnerTickMsg drives the animated braille spinners (streaming cursor + agent heartbeat).
type spinnerTickMsg struct{}

// spinnerChars are the braille frames used for animated spinners.
var spinnerChars = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// spinnerTick returns a command that fires spinnerTickMsg after 80ms.
func spinnerTick() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

// clearFlashMsg is sent after a delay to clear the transient flash status line.
type clearFlashMsg struct{}

// clearFlash returns a command that fires clearFlashMsg once after 1500ms.
func clearFlash() tea.Cmd {
	return tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg {
		return clearFlashMsg{}
	})
}

// scrollbarHideMsg is sent after a delay to hide the scrollbar.
type scrollbarHideMsg struct{}

// scrollbarHideDuration is how long the scrollbar stays visible after scrolling.
const scrollbarHideDuration = 1 * time.Second

// scrollbarHide returns a command that fires scrollbarHideMsg after the hide duration.
func scrollbarHide() tea.Cmd {
	return tea.Tick(scrollbarHideDuration, func(time.Time) tea.Msg {
		return scrollbarHideMsg{}
	})
}

// showScrollbar marks the scrollbar as visible and returns a command to hide it
// after the configured duration. Call this from every scroll-event handler.
func (m *Model) showScrollbar() tea.Cmd {
	m.scroll.scrollbarVisible = true
	m.scroll.lastScrollTime = time.Now()
	return scrollbarHide()
}

// blockerAnswersSubmittedMsg is sent when the user has submitted answers for a blocker.
type blockerAnswersSubmittedMsg struct {
	jobID   string
	blocker *service.Blocker
}

// MCPStatusMsg is sent after MCP connection completes to trigger startup toasts.
type MCPStatusMsg struct {
	Servers []service.MCPServerStatus
}

// DefinitionsReloadedMsg is sent when definition files change and are reloaded.
type DefinitionsReloadedMsg struct{}

// ConnectionLostMsg is sent when the SSE connection to the server drops.
type ConnectionLostMsg struct {
	Error string
}

// ConnectionRestoredMsg is sent when the SSE connection is re-established.
type ConnectionRestoredMsg struct{}

// OperatorTextMsg carries streamed text from the operator LLM.
type OperatorTextMsg struct {
	Text string
}

// OperatorDoneMsg is sent when the operator finishes processing a turn.
type OperatorDoneMsg struct {
	ModelName       string
	TokensIn        int
	TokensOut       int
	ReasoningTokens int
	Err             error // non-nil if the turn failed (e.g. SendMessage error)
}

// OperatorEventMsg carries an operator event for TUI display.
type OperatorEventMsg struct {
	Event service.Event
}

// OperatorPromptMsg is sent when the operator calls ask_user and needs input.
type OperatorPromptMsg struct {
	RequestID string
	Question  string
	Options   []string
}

// EditorFinishedMsg is sent when an external $EDITOR process completes.
type EditorFinishedMsg struct {
	Err error
}
