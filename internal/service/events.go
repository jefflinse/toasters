package service

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// Event type discriminators
// ---------------------------------------------------------------------------

// EventType is a string discriminator for the unified service event stream.
// All SSE events carry one of these types in their envelope.
type EventType string

const (
	// EventTypeOperatorText carries streamed text tokens from the operator LLM.
	// Payload: OperatorTextPayload. Carries TurnID for correlation.
	EventTypeOperatorText EventType = "operator.text"

	// EventTypeOperatorDone signals that the operator has finished a turn.
	// Payload: OperatorDonePayload. Carries TurnID for correlation.
	EventTypeOperatorDone EventType = "operator.done"

	// EventTypeOperatorPrompt is sent when the operator calls ask_user and needs
	// a response from the human. Payload: OperatorPromptPayload.
	// The client must call Operator().RespondToPrompt() to unblock the operator.
	//
	// TODO(Phase 2): This event type is defined but never emitted by
	// LocalService. The prompt flow must be fully wired before Phase 2:
	// operator ask_user → LocalService emits this event → event consumer
	// enters prompt mode → user answers → RespondToPrompt sends answer.
	EventTypeOperatorPrompt EventType = "operator.prompt"

	// EventTypeJobCreated is sent when the operator creates a new job via
	// the create_job system tool. Payload: JobCreatedPayload.
	EventTypeJobCreated EventType = "job.created"

	// EventTypeTaskCreated is sent when the operator creates a new task via
	// the create_task system tool. Payload: TaskCreatedPayload.
	EventTypeTaskCreated EventType = "task.created"

	// EventTypeTaskAssigned is sent when the operator assigns a task to a team.
	// Payload: TaskAssignedPayload.
	EventTypeTaskAssigned EventType = "task.assigned"

	// EventTypeTaskStarted is sent when a team begins executing a task.
	// Payload: TaskStartedPayload.
	EventTypeTaskStarted EventType = "task.started"

	// EventTypeTaskCompleted is sent when a task finishes successfully.
	// Payload: TaskCompletedPayload.
	EventTypeTaskCompleted EventType = "task.completed"

	// EventTypeTaskFailed is sent when a task fails.
	// Payload: TaskFailedPayload.
	EventTypeTaskFailed EventType = "task.failed"

	// EventTypeBlockerReported is sent when an agent reports a blocker.
	// Payload: BlockerReportedPayload.
	EventTypeBlockerReported EventType = "blocker.reported"

	// EventTypeJobCompleted is sent when an entire job finishes.
	// Payload: JobCompletedPayload.
	EventTypeJobCompleted EventType = "job.completed"

	// EventTypeProgressUpdate replaces the 500ms SQLite polling loop.
	// Sent whenever orchestration state changes (task status, session start/end,
	// new feed entries). Payload: ProgressUpdatePayload.
	// The TUI updates its local ProgressState from this event.
	EventTypeProgressUpdate EventType = "progress.update"

	// EventTypeSessionStarted is sent when a new agent session begins.
	// Payload: SessionStartedPayload. Carries SessionID.
	EventTypeSessionStarted EventType = "session.started"

	// EventTypeSessionText carries streamed text from an agent session.
	// Payload: SessionTextPayload. Carries SessionID.
	EventTypeSessionText EventType = "session.text"

	// EventTypeSessionToolCall is sent when an agent invokes a tool.
	// Payload: SessionToolCallPayload. Carries SessionID.
	EventTypeSessionToolCall EventType = "session.tool_call"

	// EventTypeSessionToolResult is sent when a tool returns a result.
	// Payload: SessionToolResultPayload. Carries SessionID.
	EventTypeSessionToolResult EventType = "session.tool_result"

	// EventTypeSessionDone is sent when an agent session completes.
	// Payload: SessionDonePayload. Carries SessionID.
	EventTypeSessionDone EventType = "session.done"

	// EventTypeDefinitionsReloaded is sent when definition files change and are
	// reloaded by the fsnotify watcher. The TUI should refresh its local copies
	// of skills, agents, and teams. Payload: nil (no payload needed).
	EventTypeDefinitionsReloaded EventType = "definitions.reloaded"

	// EventTypeOperationCompleted is sent when an async operation finishes
	// successfully (e.g. GenerateSkill, GenerateTeam, PromoteTeam).
	// Payload: OperationCompletedPayload. Carries OperationID.
	EventTypeOperationCompleted EventType = "operation.completed"

	// EventTypeOperationFailed is sent when an async operation fails.
	// Payload: OperationFailedPayload. Carries OperationID.
	EventTypeOperationFailed EventType = "operation.failed"

	// EventTypeHeartbeat is a keepalive event sent every 15 seconds to prevent
	// SSE connections from timing out. Payload: HeartbeatPayload.
	EventTypeHeartbeat EventType = "heartbeat"

	// EventTypeConnectionLost is a client-only event emitted when the SSE
	// connection to the server drops. Not sent by the server — synthesized
	// by RemoteClient. Payload: ConnectionLostPayload.
	EventTypeConnectionLost EventType = "connection.lost"

	// EventTypeConnectionRestored is a client-only event emitted when the SSE
	// connection is successfully re-established after a disconnect.
	// Not sent by the server — synthesized by RemoteClient.
	// Payload: ConnectionRestoredPayload.
	EventTypeConnectionRestored EventType = "connection.restored"
)

// ---------------------------------------------------------------------------
// Unified event envelope
// ---------------------------------------------------------------------------

// Event is the unified event envelope for the service event stream. Every event
// emitted by Events().Subscribe() is wrapped in this type.
//
// Sequence numbers are monotonically increasing per-subscription and allow
// clients to detect gaps (e.g. after reconnection). On reconnect, clients
// should fetch full state via REST endpoints rather than replaying missed events.
//
// Correlation IDs (TurnID, SessionID, OperationID) are set only for events
// where they are meaningful; all others are empty strings.
type Event struct {
	// Seq is a monotonically increasing sequence number for this subscription.
	// Starts at 1. Gaps indicate missed events (e.g. after reconnection).
	Seq uint64

	// Type identifies the event kind and determines which Payload field is set.
	Type EventType

	// Timestamp is when the event was generated on the server.
	Timestamp time.Time

	// TurnID correlates operator.text and operator.done events back to the
	// SendMessage call that initiated the turn. Empty for non-operator events.
	TurnID string

	// SessionID correlates session.* events to a specific agent session.
	// Empty for non-session events.
	SessionID string

	// OperationID correlates operation.completed and operation.failed events
	// to the async operation that was started. Empty for non-operation events.
	OperationID string

	// Payload holds the typed event data. Use a type switch on Type to
	// determine which concrete type to assert. See the Payload* types below.
	// Payload is nil for EventTypeDefinitionsReloaded.
	Payload any
}

// ---------------------------------------------------------------------------
// Event payload types
// ---------------------------------------------------------------------------

// OperatorTextPayload is the payload for EventTypeOperatorText events.
// The TUI accumulates Text tokens into the in-progress response buffer.
// textBatcher on the server side batches tokens before emitting (~16ms window),
// so Text may contain multiple tokens concatenated.
type OperatorTextPayload struct {
	// Text contains one or more batched text tokens from the operator LLM.
	Text string
	// Reasoning contains chain-of-thought reasoning text, if the model supports
	// extended thinking. Empty for most models.
	Reasoning string
}

// OperatorDonePayload is the payload for EventTypeOperatorDone events.
// Signals that the operator has finished processing a turn. The TUI should
// commit the accumulated response buffer as a ChatEntry and re-enable input.
type OperatorDonePayload struct {
	// ModelName is the model that generated the response (for the byline).
	ModelName string
	// TokensIn is the number of prompt tokens consumed in this turn.
	TokensIn int
	// TokensOut is the number of completion tokens generated in this turn.
	TokensOut int
	// ReasoningTokens is the number of reasoning tokens generated (0 if none).
	ReasoningTokens int
}

// OperatorPromptPayload is the payload for EventTypeOperatorPrompt events.
// The operator has called ask_user and is waiting for a human response.
// The TUI should enter prompt mode and call Operator().RespondToPrompt() with
// the user's answer.
type OperatorPromptPayload struct {
	// RequestID uniquely identifies this prompt request. Must be passed back
	// to Operator().RespondToPrompt() to correlate the response.
	RequestID string
	// Question is the question the operator is asking the user.
	Question string
	// Options is an optional list of suggested answers. Empty means free-form.
	Options []string
	// ConfirmDispatch is true when the prompt is a tool-dispatch confirmation
	// (the "assign_team" confirm flow). The TUI should show the dispatch UI.
	ConfirmDispatch bool
	// PendingDispatch holds the tool call awaiting confirmation, populated only
	// when ConfirmDispatch is true. The TUI displays this for the user to review.
	PendingDispatch *ToolCall
}

// JobCreatedPayload is the payload for EventTypeJobCreated events.
type JobCreatedPayload struct {
	JobID       string
	Title       string
	Description string
}

// TaskCreatedPayload is the payload for EventTypeTaskCreated events.
type TaskCreatedPayload struct {
	TaskID string
	JobID  string
	Title  string
	TeamID string // may be empty if no team is pre-assigned
}

// TaskAssignedPayload is the payload for EventTypeTaskAssigned events.
type TaskAssignedPayload struct {
	TaskID string
	JobID  string
	TeamID string
	Title  string
}

// TaskStartedPayload is the payload for EventTypeTaskStarted events.
type TaskStartedPayload struct {
	TaskID string
	JobID  string
	TeamID string
	Title  string
}

// TaskCompletedPayload is the payload for EventTypeTaskCompleted events.
type TaskCompletedPayload struct {
	TaskID          string
	JobID           string
	TeamID          string
	Summary         string
	Recommendations string // follow-up recommendations from the team
	HasNextTask     bool   // whether there is a queued next task
}

// TaskFailedPayload is the payload for EventTypeTaskFailed events.
type TaskFailedPayload struct {
	TaskID string
	JobID  string
	TeamID string
	Error  string
}

// BlockerReportedPayload is the payload for EventTypeBlockerReported events.
type BlockerReportedPayload struct {
	TaskID      string
	TeamID      string
	AgentID     string
	Description string
	Questions   []string // clarifying questions for the user
}

// JobCompletedPayload is the payload for EventTypeJobCompleted events.
type JobCompletedPayload struct {
	JobID   string
	Title   string
	Summary string
}

// ProgressUpdatePayload is the payload for EventTypeProgressUpdate events.
// It carries the complete current ProgressState so the TUI can replace its
// local copy atomically. This replaces the 500ms SQLite polling loop.
type ProgressUpdatePayload struct {
	State ProgressState
}

// SessionStartedPayload is the payload for EventTypeSessionStarted events.
type SessionStartedPayload struct {
	SessionID      string
	AgentName      string
	TeamName       string // may be empty
	Task           string // short human-readable task description
	JobID          string
	TaskID         string
	SystemPrompt   string
	InitialMessage string
}

// SessionTextPayload is the payload for EventTypeSessionText events.
// Text tokens from an agent session are delivered here (not batched — the TUI
// accumulates them into the session's output buffer).
type SessionTextPayload struct {
	Text string
}

// SessionToolCallPayload is the payload for EventTypeSessionToolCall events.
type SessionToolCallPayload struct {
	ToolCall ToolCall
}

// SessionToolResultPayload is the payload for EventTypeSessionToolResult events.
type SessionToolResultPayload struct {
	Result ToolCallResult
}

// SessionDonePayload is the payload for EventTypeSessionDone events.
type SessionDonePayload struct {
	AgentName string
	JobID     string
	TaskID    string
	Status    string // "completed", "failed", "cancelled"
	FinalText string // last text output from the session (may be empty)
}

// OperationCompletedPayload is the payload for EventTypeOperationCompleted events.
// The OperationID matches the value returned by the async service method that
// started the operation (e.g. Definitions().GenerateSkill()).
type OperationCompletedPayload struct {
	// Kind identifies what kind of operation completed (e.g. "generate_skill",
	// "generate_agent", "generate_team", "promote_team", "detect_coordinator").
	Kind string
	// Result carries the operation output. Fields populated depend on Kind.
	Result OperationResult
}

// OperationFailedPayload is the payload for EventTypeOperationFailed events.
type OperationFailedPayload struct {
	// Kind identifies what kind of operation failed.
	Kind string
	// Error is the human-readable error message.
	Error string
}

// HeartbeatPayload is the payload for EventTypeHeartbeat events.
// Sent every 15 seconds to keep SSE connections alive through proxies and
// load balancers that close idle connections.
type HeartbeatPayload struct {
	// ServerTime is the server's current time, useful for clock-skew detection.
	ServerTime time.Time
}

// ConnectionLostPayload is the payload for EventTypeConnectionLost events.
// Client-only — synthesized by RemoteClient when the SSE connection drops.
type ConnectionLostPayload struct {
	// Error is the human-readable reason the connection was lost.
	Error string
}

// ConnectionRestoredPayload is the payload for EventTypeConnectionRestored events.
// Client-only — synthesized by RemoteClient when the SSE connection is re-established.
type ConnectionRestoredPayload struct{}

// ---------------------------------------------------------------------------
// EventService interface
// ---------------------------------------------------------------------------

// EventService provides access to the unified server-push event stream.
// It unifies three current mechanisms:
//   - Operator callbacks (onText, onEvent, onTurnDone)
//   - Session subscriptions (session.Subscribe())
//   - SQLite progress polling (500ms progressPollCmd)
//
// All three sources are multiplexed into a single channel by LocalService.
// RemoteClient implements this via SSE.
type EventService interface {
	// Subscribe returns a channel that delivers all service events in order.
	// The channel is closed when ctx is cancelled. Multiple concurrent
	// subscribers are supported — each receives all events independently.
	//
	// The caller must drain the channel promptly; a slow consumer may cause
	// events to be dropped (the implementation uses a bounded buffer and
	// drops on overflow rather than blocking the server).
	//
	// On reconnect, the caller should fetch full state via REST endpoints
	// (Operator().Status(), Jobs().List(), Sessions().List()) before calling
	// Subscribe again, since missed events are not replayed.
	Subscribe(ctx context.Context) <-chan Event
}
