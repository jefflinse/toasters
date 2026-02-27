// Message types: Bubble Tea message definitions, tick/timer commands, toast notifications, and shared type declarations.
package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/mcp"
	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
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

// pendingCompletion holds a buffered agent-completion notification that arrived
// while the operator stream was active. It is drained after the stream ends.
type pendingCompletion struct {
	notification string // the pre-built notification message to inject
}

// focusedPanel identifies which panel currently holds keyboard focus.
type focusedPanel int

const (
	focusChat     focusedPanel = iota
	focusJobs     focusedPanel = iota
	focusTeams    focusedPanel = iota
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

// progressPollMsg carries the latest progress data from SQLite, fired every 500ms.
type progressPollMsg struct {
	Jobs            []*db.Job
	Tasks           map[string][]*db.Task
	Progress        map[string][]*db.ProgressReport
	Sessions        []*db.AgentSession
	RuntimeSessions []runtime.SessionSnapshot // live snapshots with real token counts
	FeedEntries     []*db.FeedEntry           // recent activity feed entries
}

// progressPollTickMsg is an internal tick that triggers the next poll.
type progressPollTickMsg struct{}

// Message types for the Bubble Tea event loop.

type StreamChunkMsg struct {
	Content   string
	Reasoning string
}

type StreamDoneMsg struct {
	Model string
	Usage *provider.Usage
}

type StreamErrMsg struct {
	Err error
}

type ModelsMsg struct {
	Models []provider.ModelInfo
	Err    error
}

// AgentOutputMsg is sent by the gateway notify callback when any slot output changes.
type AgentOutputMsg struct{}

// RuntimeSessionStartedMsg is sent when a new runtime session begins.
type RuntimeSessionStartedMsg struct {
	SessionID      string
	AgentName      string
	TeamName       string // team this agent belongs to (may be empty)
	Task           string // short human-readable description of what this agent is doing
	JobID          string
	SystemPrompt   string
	InitialMessage string
}

// RuntimeSessionEventMsg carries a runtime session event to the TUI.
type RuntimeSessionEventMsg struct {
	Event runtime.SessionEvent
}

// RuntimeSessionDoneMsg is sent when a runtime session completes.
type RuntimeSessionDoneMsg struct {
	SessionID string
	AgentName string
	JobID     string
	FinalText string
	Status    string // "completed", "failed", "cancelled"
}

// TeamsReloadedMsg is sent by the hot-reload watcher when the teams directory changes.
type TeamsReloadedMsg struct {
	Teams     []agents.Team
	Awareness string
}

// JobsReloadedMsg is sent when jobs are reloaded (e.g. from SQLite polling).
type JobsReloadedMsg struct {
	Jobs []*db.Job
}

// AppReadyMsg is sent when the app has finished loading and is ready to start.
type AppReadyMsg struct {
	Awareness string
	Greeting  string // pre-fetched operator greeting; injected immediately on render
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

// SlotTimeoutPromptExpiredMsg fires when the 1-minute user-response window elapses.
type SlotTimeoutPromptExpiredMsg struct{ SlotID int }

// claudeMetaMsg carries model/mode info parsed from the claude CLI system/init event.
type claudeMetaMsg struct {
	Model          string
	PermissionMode string
	Version        string
	SessionID      string
}

// ToolCallMsg is emitted when the LLM requests one or more tool calls.
type ToolCallMsg struct {
	Calls []provider.ToolCall
}

// ToolResult holds the result of a single tool call execution.
type ToolResult struct {
	CallID string
	Name   string
	Result string
	Err    error
}

// ToolResultMsg is sent when async tool execution completes.
type ToolResultMsg struct {
	Results []ToolResult
}

// AskUserResponseMsg is dispatched when the user submits a response in prompt mode.
type AskUserResponseMsg struct {
	Call   provider.ToolCall
	Result string
}

// TeamsAutoDetectDoneMsg is sent when the LLM coordinator auto-detection finishes.
type TeamsAutoDetectDoneMsg struct {
	teamDir   string // team.Dir, used to match back
	agentName string // matched agent name; empty if no match or error
	err       error
}

// blockerAnswersSubmittedMsg is sent when the user has submitted answers for a blocker.
type blockerAnswersSubmittedMsg struct {
	jobID   string
	blocker *Blocker
}

// MCPStatusMsg is sent after MCP connection completes to trigger startup toasts.
type MCPStatusMsg struct {
	Servers []mcp.ServerStatus
}

// DefinitionsReloadedMsg is sent when definition files change and are reloaded.
type DefinitionsReloadedMsg struct{}

// OperatorTextMsg carries streamed text from the operator LLM.
type OperatorTextMsg struct {
	Text string
}

// OperatorEventMsg carries an operator event for TUI display.
type OperatorEventMsg struct {
	Event operator.Event
}

// ChatEntry consolidates the per-message data that was previously spread
// across four parallel slices (messages, timestamps, reasoning, claudeMeta).
// Reasoning and ClaudeMeta are only meaningful for assistant-role messages;
// for other roles they are empty strings.
type ChatEntry struct {
	Message    provider.Message
	Timestamp  time.Time
	Reasoning  string
	ClaudeMeta string
}

// waitForAgentUpdate blocks until the gateway signals an output update.
func waitForAgentUpdate(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-ch
		return AgentOutputMsg{}
	}
}

// slotTimeoutPromptCmd fires SlotTimeoutPromptExpiredMsg after 1 minute,
// giving the user a window to respond before auto-continuing.
func slotTimeoutPromptCmd(slotID int) tea.Cmd {
	return tea.Tick(time.Minute, func(time.Time) tea.Msg {
		return SlotTimeoutPromptExpiredMsg{SlotID: slotID}
	})
}
