// Package service defines the use-case-level interface for the Toasters
// orchestration engine. It provides a clean boundary between the TUI (or any
// other client) and the underlying implementation, enabling the engine to run
// either in-process (LocalService) or over the network (RemoteClient).
//
// All types in this package are service-level DTOs. No raw db.*, provider.*,
// runtime.*, operator.*, mcp.*, or mdfmt.* types appear here. The TUI
// imports only this package plus its rendering libraries.
package service

import (
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// Job types
// ---------------------------------------------------------------------------

// JobStatus represents the lifecycle state of a job.
type JobStatus string

const (
	// JobStatusPending means the job has been created but work has not started.
	JobStatusPending JobStatus = "pending"
	// JobStatusSettingUp means the job is being initialised (workspace, context).
	JobStatusSettingUp JobStatus = "setting_up"
	// JobStatusActive means at least one task is currently being worked on.
	JobStatusActive JobStatus = "active"
	// JobStatusPaused means the job is temporarily suspended.
	JobStatusPaused JobStatus = "paused"
	// JobStatusCompleted means all tasks finished successfully.
	JobStatusCompleted JobStatus = "completed"
	// JobStatusFailed means the job ended with an unrecoverable error.
	JobStatusFailed JobStatus = "failed"
	// JobStatusCancelled means the job was cancelled by the user.
	JobStatusCancelled JobStatus = "cancelled"
)

// Job is the service-level representation of a unit of orchestrated work.
type Job struct {
	ID           string
	Title        string
	Description  string
	Type         string // "bug_fix", "new_feature", "prototype", "review"
	Status       JobStatus
	WorkspaceDir string `json:"-"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Metadata     json.RawMessage // extensible JSON blob; may be nil
}

// JobDetail bundles a Job with its tasks and recent progress reports, used
// for the jobs modal detail view. Fetched via Jobs().Get(id).
type JobDetail struct {
	Job      Job
	Tasks    []Task
	Progress []ProgressReport // most recent progress reports, newest first
}

// JobListFilter specifies criteria for listing jobs.
type JobListFilter struct {
	Status *JobStatus
	Type   *string
	Limit  int
	Offset int
}

// ---------------------------------------------------------------------------
// Task types
// ---------------------------------------------------------------------------

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	// TaskStatusPending means the task has been created but not started.
	TaskStatusPending TaskStatus = "pending"
	// TaskStatusInProgress means a worker is actively working on the task.
	TaskStatusInProgress TaskStatus = "in_progress"
	// TaskStatusCompleted means the task finished successfully.
	TaskStatusCompleted TaskStatus = "completed"
	// TaskStatusFailed means the task ended with an error.
	TaskStatusFailed TaskStatus = "failed"
	// TaskStatusBlocked means the task is waiting on a blocker to be resolved.
	TaskStatusBlocked TaskStatus = "blocked"
	// TaskStatusCancelled means the task was cancelled.
	TaskStatusCancelled TaskStatus = "cancelled"
)

// Task is the service-level representation of a single step within a job.
type Task struct {
	ID              string
	JobID           string
	Title           string
	Status          TaskStatus
	WorkerID        string // assigned worker (may be empty)
	GraphID         string // assigned graph definition id (may be empty)
	ParentID        string // DAG edge; empty for root tasks
	SortOrder       int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Summary         string          // completion summary or failure reason
	ResultSummary   string          // structured result summary
	Recommendations string          // structured recommendations
	Metadata        json.RawMessage // extensible JSON blob; may be nil
}

// ---------------------------------------------------------------------------
// Progress types
// ---------------------------------------------------------------------------

// ProgressReport records a point-in-time status update for a job or task.
type ProgressReport struct {
	ID        int64
	JobID     string
	TaskID    string // may be empty (job-level report)
	WorkerID  string // may be empty
	Status    string // "in_progress", "blocked", "completed", "failed"
	Message   string
	CreatedAt time.Time
}

// ---------------------------------------------------------------------------
// Session types
// ---------------------------------------------------------------------------

// SessionStatus represents the lifecycle state of a worker session.
type SessionStatus string

const (
	// SessionStatusActive means the session is currently running.
	SessionStatusActive SessionStatus = "active"
	// SessionStatusCompleted means the session finished successfully.
	SessionStatusCompleted SessionStatus = "completed"
	// SessionStatusFailed means the session ended with an error.
	SessionStatusFailed SessionStatus = "failed"
	// SessionStatusCancelled means the session was cancelled.
	SessionStatusCancelled SessionStatus = "cancelled"
)

// WorkerSession is the service-level representation of a persisted worker session
// record. It carries the DB-level metadata (token counts, timing, cost).
type WorkerSession struct {
	ID        string
	WorkerID  string
	JobID     string // may be empty
	TaskID    string // may be empty
	Status    SessionStatus
	Model     string
	Provider  string
	TokensIn  int64
	TokensOut int64
	StartedAt time.Time
	EndedAt   *time.Time
	CostUSD   *float64
}

// SessionEventType identifies the kind of event emitted by a live worker session.
type SessionEventType string

const (
	// SessionEventTypeText means the worker produced a text token.
	SessionEventTypeText SessionEventType = "text"
	// SessionEventTypeToolCall means the worker invoked a tool.
	SessionEventTypeToolCall SessionEventType = "tool_call"
	// SessionEventTypeToolResult means a tool returned a result.
	SessionEventTypeToolResult SessionEventType = "tool_result"
	// SessionEventTypeDone means the session completed.
	SessionEventTypeDone SessionEventType = "done"
	// SessionEventTypeError means the session encountered an error.
	SessionEventTypeError SessionEventType = "error"
)

// SessionSnapshot is a live, read-only view of an in-process worker session.
// Unlike WorkerSession (which is persisted), a SessionSnapshot carries real-time
// token counts from the runtime before the session completes and writes to DB.
type SessionSnapshot struct {
	ID        string
	WorkerID  string
	JobID     string
	TaskID    string
	Status    string // "active", "completed", "failed", "cancelled"
	Model     string
	Provider  string
	StartTime time.Time
	TokensIn  int64
	TokensOut int64
}

// SessionDetail is a full view of a session including its accumulated output
// and activity history. Used for the output modal and for reconnect hydration
// (B4 concern: clients call Sessions().Get(id) on reconnect to rebuild state).
type SessionDetail struct {
	Snapshot       SessionSnapshot
	SystemPrompt   string         // the system prompt given to the LLM
	InitialMessage string         // the initial user message / task description
	Output         string         // accumulated text output from the session
	Activities     []ActivityItem // recent tool-call activities; newest last
	WorkerName     string         // human-readable worker name
	Task           string         // short human-readable task description
}

// ActivityItem represents a single tool-call activity for display in a worker card.
type ActivityItem struct {
	Label    string // formatted display label, e.g. "write: main.go"
	ToolName string // raw tool name
}

// ---------------------------------------------------------------------------
// Feed types
// ---------------------------------------------------------------------------

// FeedEntryType represents the kind of activity feed entry.
type FeedEntryType string

const (
	// FeedEntryTypeUserMessage is a message sent by the user.
	FeedEntryTypeUserMessage FeedEntryType = "user_message"
	// FeedEntryTypeOperatorMessage is a message from the operator LLM.
	FeedEntryTypeOperatorMessage FeedEntryType = "operator_message"
	// FeedEntryTypeSystemEvent is an internal system event.
	FeedEntryTypeSystemEvent FeedEntryType = "system_event"
	// FeedEntryTypeConsultationTrace is a trace from a system agent consultation.
	FeedEntryTypeConsultationTrace FeedEntryType = "consultation_trace"
	// FeedEntryTypeTaskStarted is emitted when a task begins execution.
	FeedEntryTypeTaskStarted FeedEntryType = "task_started"
	// FeedEntryTypeTaskCompleted is emitted when a task finishes successfully.
	FeedEntryTypeTaskCompleted FeedEntryType = "task_completed"
	// FeedEntryTypeTaskFailed is emitted when a task fails.
	FeedEntryTypeTaskFailed FeedEntryType = "task_failed"
	// FeedEntryTypeBlockerReported is emitted when a worker reports a blocker.
	FeedEntryTypeBlockerReported FeedEntryType = "blocker_reported"
	// FeedEntryTypeJobComplete is emitted when an entire job finishes.
	FeedEntryTypeJobComplete FeedEntryType = "job_complete"
)

// FeedEntry is a single entry in the activity feed.
type FeedEntry struct {
	ID        int64
	JobID     string // may be empty
	EntryType FeedEntryType
	Content   string
	Metadata  json.RawMessage // extensible JSON blob; may be nil
	CreatedAt time.Time
}

// ---------------------------------------------------------------------------
// Definition types (Skills, Workers)
// ---------------------------------------------------------------------------

// Skill is the service-level representation of a reusable worker capability.
type Skill struct {
	ID          string
	Name        string
	Description string
	Tools       []string // tool names granted by this skill
	Prompt      string   // the skill's markdown body (injected into worker system prompt)
	Source      string   // "system", "user", "builtin"
	SourcePath  string   `json:"-"` // absolute path to the .md file; empty for built-ins
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ---------------------------------------------------------------------------
// Graph definition types
// ---------------------------------------------------------------------------

// GraphEdgeKind classifies a graph edge: a plain forward edge or a conditional
// edge emitted from a router. Used by dagmap renderers to style branch edges
// distinctly from linear edges.
type GraphEdgeKind string

const (
	// GraphEdgeStatic is an unconditional forward edge (plain `to:`).
	GraphEdgeStatic GraphEdgeKind = "static"

	// GraphEdgeConditional is a router-branch edge. Label carries a short
	// human-readable description of the match (e.g. "true", "approved",
	// "passed"), derived from Branch.When.
	GraphEdgeConditional GraphEdgeKind = "conditional"
)

// GraphEdge describes one edge in a graph topology.
type GraphEdge struct {
	From  string        // source node id, or "" for the start edge
	To    string        // destination node id, or "" for the end edge
	Kind  GraphEdgeKind // static or conditional
	Label string        // optional branch label for conditional edges
}

// GraphDefinition is the service-level projection of a compiled graph. It's
// the minimum the TUI needs to render a topology map for a task — node ids
// plus edge shape — without taking a dependency on internal/graphexec.
type GraphDefinition struct {
	ID          string
	Name        string
	Description string
	Tags        []string
	Entry       string
	Exit        string
	Nodes       []string
	Edges       []GraphEdge
}

// ---------------------------------------------------------------------------
// Chat / conversation types
// ---------------------------------------------------------------------------

// MessageRole identifies the role of a chat message participant.
type MessageRole string

const (
	// MessageRoleUser is a message from the human user.
	MessageRoleUser MessageRole = "user"
	// MessageRoleAssistant is a message from the LLM.
	MessageRoleAssistant MessageRole = "assistant"
	// MessageRoleTool is a tool result message.
	MessageRoleTool MessageRole = "tool"
	// MessageRoleSystem is a system prompt message.
	MessageRoleSystem MessageRole = "system"
)

// ToolCall is the service-level representation of a tool invocation by the LLM.
// Used in ChatMessage.ToolCalls.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage // raw JSON arguments
}

// ToolCallResult is the service-level representation of a tool execution result.
type ToolCallResult struct {
	CallID string
	Name   string
	Result string
	Error  string // non-empty if the tool call failed
}

// ChatMessage is the service-level replacement for provider.Message. It carries
// all fields needed by the TUI to render a conversation turn.
type ChatMessage struct {
	Role       MessageRole
	Content    string
	ToolCalls  []ToolCall // populated for assistant messages that invoke tools
	ToolCallID string     // populated for tool result messages
}

// ChatEntryKind distinguishes rendering modes for a ChatEntry. The default
// empty value means a plain text message (user/assistant/system/tool);
// additional kinds are structured entries that the TUI renders from a
// typed payload instead of free-form content.
type ChatEntryKind string

const (
	// ChatEntryKindMessage is the default: a conventional chat message
	// rendered from ChatEntry.Message.
	ChatEntryKindMessage ChatEntryKind = ""
	// ChatEntryKindJobUpdate is a live, mutating block summarizing a
	// single job's progress. Payload lives in ChatEntry.JobUpdate.
	ChatEntryKindJobUpdate ChatEntryKind = "job_update"

	// ChatEntryKindJobResult is a terminal completion summary that lands
	// in chat the moment a job finishes — separate from the in-progress
	// JobUpdate block so the conversation history reflects "this is the
	// completion event" rather than retroactively rewriting prior state.
	// Payload lives in ChatEntry.JobResult.
	ChatEntryKindJobResult ChatEntryKind = "job_result"

	// ChatEntryKindWorkerStream is a live block accumulating one worker's
	// streamed output (text + tool calls) interleaved into the chat. New
	// streamed events for the same worker+job append to the existing
	// block until the block closes (60s idle, worker done, or any other
	// chat entry supersedes it from below). Payload lives in
	// ChatEntry.WorkerStream.
	ChatEntryKindWorkerStream ChatEntryKind = "worker_stream"
)

// JobSnapshot is the payload for a ChatEntryKindJobUpdate entry. It
// captures the bits of a Job needed to render the job-update block so
// the renderer doesn't need to look up live state.
type JobSnapshot struct {
	JobID          string
	Title          string
	Status         JobStatus
	TasksCompleted int
	TasksTotal     int
	TasksFailed    int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ChatEntry consolidates per-message data for the chat history display.
// It replaces the tui.ChatEntry type, which previously held a provider.Message.
type ChatEntry struct {
	Message    ChatMessage
	Timestamp  time.Time
	Reasoning  string // chain-of-thought reasoning text; empty for non-assistant messages
	ClaudeMeta string // byline / metadata string (e.g. "operator · claude-sonnet-4-6")

	// Kind selects how the entry is rendered. Defaults to ChatEntryKindMessage.
	// Non-message kinds are currently TUI-ephemeral (not persisted or
	// round-tripped over the wire); see internal/tui for consumers.
	Kind ChatEntryKind
	// JobUpdate carries the snapshot for Kind == ChatEntryKindJobUpdate.
	// Nil for other kinds.
	JobUpdate *JobSnapshot

	// JobResult carries the completion summary for
	// Kind == ChatEntryKindJobResult. Nil for other kinds.
	JobResult *JobResultSnapshot

	// WorkerStream carries the streamed-output snapshot for
	// Kind == ChatEntryKindWorkerStream. Nil for other kinds.
	WorkerStream *WorkerStreamSnapshot
}

// WorkerStreamItemKind discriminates the elements stored in a
// WorkerStreamSnapshot.Items slice.
type WorkerStreamItemKind int

const (
	// WorkerStreamItemText is a coalesced run of streamed text tokens.
	WorkerStreamItemText WorkerStreamItemKind = iota
	// WorkerStreamItemTool wraps a tool call's lifecycle: start (with
	// args) and result (with status, duration, and a truncated preview).
	WorkerStreamItemTool
)

// WorkerStreamItem is one renderable element inside a worker stream
// chat block — either a coalesced text run or a single tool
// call/result pair.
type WorkerStreamItem struct {
	Kind WorkerStreamItemKind

	// Text run.
	Text string

	// Tool call lifecycle.
	ToolID     string
	ToolName   string
	ToolArgs   json.RawMessage
	ToolResult string
	ToolError  bool
	StartedAt  time.Time
	EndedAt    time.Time // zero while in flight
}

// WorkerStreamSnapshot is the payload for a ChatEntryKindWorkerStream
// entry. The block accumulates items as long as it stays "open" — same
// (WorkerName, JobID), most-recent chat entry, less than 60s since
// LastActivity, and Done==false. Once any of those flips, the next
// streamed event from the same worker starts a fresh snapshot.
type WorkerStreamSnapshot struct {
	WorkerName   string
	JobID        string
	TaskID       string
	SessionID    string // most recent contributing session
	Items        []WorkerStreamItem
	StartedAt    time.Time
	LastActivity time.Time
	Done         bool // session terminated normally; render shows ✓ instead of streaming/idle
}

// JobResultSnapshot is the payload for a ChatEntryKindJobResult entry. It
// mirrors JobCompletedPayload one-for-one so the TUI can stash it on the
// chat entry, render the result block from cached state, and survive a
// progress refresh without re-fetching server state.
type JobResultSnapshot struct {
	JobID     string
	Title     string
	Summary   string
	Status    JobStatus
	Workspace string
	StartedAt time.Time
	EndedAt   time.Time

	TasksTotal     int
	TasksCompleted int
	TasksFailed    int

	TokensIn  int64
	TokensOut int64
	CostUSD   float64

	FilesTouched      []FileTouch
	FilesTouchedExtra int
}

// ---------------------------------------------------------------------------
// MCP types
// ---------------------------------------------------------------------------

// MCPServerState represents the connection state of an MCP server.
type MCPServerState string

const (
	// MCPServerStateConnected means the server connected and tools were discovered.
	MCPServerStateConnected MCPServerState = "connected"
	// MCPServerStateFailed means the server failed to connect.
	MCPServerStateFailed MCPServerState = "failed"
)

// MCPToolInfo holds metadata about a single tool discovered from an MCP server.
type MCPToolInfo struct {
	NamespacedName string // "{server_name}__{tool_name}"
	OriginalName   string // original tool name from the MCP server
	ServerName     string
	Description    string
	InputSchema    json.RawMessage // JSON Schema for tool parameters
}

// MCPServerStatus holds the connection status and metadata for a configured
// MCP server. Replaces mcp.ServerStatus in the TUI.
type MCPServerStatus struct {
	Name      string
	Transport string // "stdio", "sse", "http"
	State     MCPServerState
	Error     string // non-empty if State == MCPServerStateFailed
	ToolCount int
	Tools     []MCPToolInfo
}

// ---------------------------------------------------------------------------
// Model / provider types
// ---------------------------------------------------------------------------

// ModelInfo describes an available LLM model. Replaces provider.ModelInfo.
type ModelInfo struct {
	ID                  string
	Name                string
	Provider            string
	State               string // "loaded", "not-loaded", "available", etc.
	MaxContextLength    int    // max context window the model supports (0 if unknown)
	LoadedContextLength int    // actual context length for the loaded model (0 if not loaded)
}

// ContextLength returns the effective context length — loaded if available,
// otherwise max.
func (m ModelInfo) ContextLength() int {
	if m.LoadedContextLength > 0 {
		return m.LoadedContextLength
	}
	return m.MaxContextLength
}

// ---------------------------------------------------------------------------
// Catalog types (models.dev)
// ---------------------------------------------------------------------------

// CatalogProvider is a provider entry from the models.dev catalog.
type CatalogProvider struct {
	ID     string         // provider ID (e.g. "anthropic", "openai")
	Name   string         // display name (e.g. "Anthropic")
	API    string         // base API URL, if known
	Doc    string         // documentation URL
	Env    []string       // environment variable names for API keys
	Models []CatalogModel // sorted by name
}

// CatalogModel is a model entry from the models.dev catalog.
type CatalogModel struct {
	ID               string  // model ID (e.g. "claude-sonnet-4-6")
	Name             string  // display name (e.g. "Claude Sonnet 4.6")
	Family           string  // model family
	ToolCall         bool    // supports tool/function calling
	Reasoning        bool    // supports chain-of-thought reasoning
	StructuredOutput bool    // supports structured/JSON output
	OpenWeights      bool    // open-weight model
	ContextLimit     int     // max context window (tokens)
	OutputLimit      int     // max output tokens
	InputCost        float64 // cost per 1M input tokens (0 for free/local)
	OutputCost       float64 // cost per 1M output tokens (0 for free/local)
}

// ---------------------------------------------------------------------------
// Provider configuration types
// ---------------------------------------------------------------------------

// AddProviderRequest holds the data needed to add a new provider to config.yaml.
type AddProviderRequest struct {
	ID       string // unique identifier (e.g. "my-openai")
	Name     string // display name (e.g. "My OpenAI")
	Type     string // "openai", "local", or "anthropic"
	Endpoint string // API endpoint URL (optional for some types)
	APIKey   string // API key value or ${ENV_VAR} reference (optional)
}

// Settings is the user-editable subset of runtime configuration exposed
// through the /settings surface. All fields are flat scalars so the TUI can
// render them as simple rows.
type Settings struct {
	// CoarseGranularity controls how large the tasks emitted by
	// coarse-decompose are. One of: "xcoarse", "coarse", "medium", "fine",
	// "xfine" (coarsest → finest).
	CoarseGranularity string `json:"coarse_granularity"`

	// FineGranularity controls how finely fine-decompose slices a task into
	// subtasks (and, in the future, dynamically generated graph nodes).
	// Same enum as CoarseGranularity.
	FineGranularity string `json:"fine_granularity"`

	// WorkerThinkingEnabled is the default value of the per-request thinking
	// toggle for worker (graph) nodes. Roles may override this in their
	// frontmatter.
	WorkerThinkingEnabled bool `json:"worker_thinking_enabled"`

	// WorkerTemperature is the default sampling temperature for worker
	// (graph) nodes. Roles may override this in their frontmatter.
	WorkerTemperature float64 `json:"worker_temperature"`

	// ShowJobsPanelByDefault keeps the Jobs/Workers left panel visible
	// even when no jobs or runtime sessions exist. When false (default),
	// the panel only appears once there's something to show.
	ShowJobsPanelByDefault bool `json:"show_jobs_panel_by_default"`

	// ShowOperatorPanelByDefault keeps the Operator/sidebar right panel
	// visible by default. When false, the panel is hidden until the user
	// reveals it via Ctrl+O.
	ShowOperatorPanelByDefault bool `json:"show_operator_panel_by_default"`
}

// ---------------------------------------------------------------------------
// Progress state (replaces progressPollMsg)
// ---------------------------------------------------------------------------

// ProgressState is the complete snapshot of orchestration progress delivered
// via SSE progress.update events. It replaces the tui.progressPollMsg type
// and the 500ms SQLite polling loop. The TUI maintains a local copy of this
// state, updated whenever a progress.update event arrives.
type ProgressState struct {
	// Jobs is the full list of jobs (display layer filters by status).
	Jobs []Job

	// Tasks maps job ID → tasks for that job.
	Tasks map[string][]Task

	// Reports maps job ID → recent progress reports for that job (newest first).
	Reports map[string][]ProgressReport

	// ActiveSessions is the list of persisted session records with active status.
	ActiveSessions []WorkerSession

	// LiveSnapshots is the list of live session snapshots from the in-process
	// runtime. These carry real-time token counts before the session writes to DB.
	LiveSnapshots []SessionSnapshot

	// FeedEntries is the most recent activity feed entries (newest first).
	FeedEntries []FeedEntry

	// MCPServers is the current MCP server connection status.
	MCPServers []MCPServerStatus
}

// ---------------------------------------------------------------------------
// Operator status
// ---------------------------------------------------------------------------

// OperatorState represents the current state of the operator event loop.
type OperatorState string

const (
	// OperatorStateDisabled means no operator provider is configured.
	OperatorStateDisabled OperatorState = "disabled"
	// OperatorStateIdle means the operator is waiting for input.
	OperatorStateIdle OperatorState = "idle"
	// OperatorStateStreaming means the operator is generating a response.
	OperatorStateStreaming OperatorState = "streaming"
	// OperatorStateProcessing means the operator is processing an event (tool call, etc.).
	OperatorStateProcessing OperatorState = "processing"
)

// OperatorStatus is the current status of the operator, returned by
// Operator().Status(). Used by clients to rebuild state on reconnect and to
// populate the sidebar (model name, endpoint URL).
type OperatorStatus struct {
	State         OperatorState
	CurrentTurnID string // non-empty while a turn is in progress
	ModelName     string // the model the operator is using (canonical, from server config)
	Endpoint      string // the LLM provider endpoint URL the operator is using
}

// ---------------------------------------------------------------------------
// Async operation types
// ---------------------------------------------------------------------------

// OperationResult carries the outcome of an async operation, delivered via
// operation.completed or operation.failed SSE events.
type OperationResult struct {
	OperationID string
	// For generation operations, Content holds the generated definition content.
	Content string
	// Error is non-empty for operation.failed events.
	Error string
}

// ---------------------------------------------------------------------------
// System / health types
// ---------------------------------------------------------------------------

// HealthStatus is returned by System().Health().
type HealthStatus struct {
	Status  string        // "ok", "degraded"
	Version string        // application version
	Uptime  time.Duration // time since the service started
}

