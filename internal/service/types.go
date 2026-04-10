// Package service defines the use-case-level interface for the Toasters
// orchestration engine. It provides a clean boundary between the TUI (or any
// other client) and the underlying implementation, enabling the engine to run
// either in-process (LocalService) or over the network (RemoteClient).
//
// All types in this package are service-level DTOs. No raw db.*, provider.*,
// runtime.*, operator.*, mcp.*, or agentfmt.* types appear here. The TUI
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
	// JobStatusDecomposing means the planner is breaking the job into tasks.
	JobStatusDecomposing JobStatus = "decomposing"
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
	// TaskStatusInProgress means an agent is actively working on the task.
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
	AgentID         string // assigned agent (may be empty)
	TeamID          string // assigned team (may be empty)
	ParentID        string // DAG edge; empty for root tasks
	SortOrder       int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Summary         string          // completion summary or failure reason
	ResultSummary   string          // structured result summary
	Recommendations string          // structured recommendations from the team
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
	AgentID   string // may be empty
	Status    string // "in_progress", "blocked", "completed", "failed"
	Message   string
	CreatedAt time.Time
}

// ---------------------------------------------------------------------------
// Session types
// ---------------------------------------------------------------------------

// SessionStatus represents the lifecycle state of an agent session.
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

// AgentSession is the service-level representation of a persisted agent session
// record. It carries the DB-level metadata (token counts, timing, cost).
type AgentSession struct {
	ID        string
	AgentID   string
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

// SessionEventType identifies the kind of event emitted by a live agent session.
type SessionEventType string

const (
	// SessionEventTypeText means the agent produced a text token.
	SessionEventTypeText SessionEventType = "text"
	// SessionEventTypeToolCall means the agent invoked a tool.
	SessionEventTypeToolCall SessionEventType = "tool_call"
	// SessionEventTypeToolResult means a tool returned a result.
	SessionEventTypeToolResult SessionEventType = "tool_result"
	// SessionEventTypeDone means the session completed.
	SessionEventTypeDone SessionEventType = "done"
	// SessionEventTypeError means the session encountered an error.
	SessionEventTypeError SessionEventType = "error"
)

// SessionSnapshot is a live, read-only view of an in-process agent session.
// Unlike AgentSession (which is persisted), a SessionSnapshot carries real-time
// token counts from the runtime before the session completes and writes to DB.
type SessionSnapshot struct {
	ID        string
	AgentID   string
	TeamName  string // team this agent belongs to; may be empty
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
	AgentName      string         // human-readable agent name
	TeamName       string         // team name; may be empty
	Task           string         // short human-readable task description
}

// ActivityItem represents a single tool-call activity for display in an agent card.
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
	// FeedEntryTypeBlockerReported is emitted when an agent reports a blocker.
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
// Definition types (Skills, Agents, Teams)
// ---------------------------------------------------------------------------

// Skill is the service-level representation of a reusable agent capability.
type Skill struct {
	ID          string
	Name        string
	Description string
	Tools       []string // tool names granted by this skill
	Prompt      string   // the skill's markdown body (injected into agent system prompt)
	Source      string   // "system", "user", "builtin"
	SourcePath  string   `json:"-"` // absolute path to the .md file; empty for built-ins
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Agent is the service-level representation of a configured LLM agent.
type Agent struct {
	ID              string
	Name            string
	Description     string
	Mode            string // "lead", "worker", "primary"
	Model           string
	Provider        string
	Temperature     *float64
	SystemPrompt    string
	Tools           []string // tool names
	DisallowedTools []string // tool names blocked for this agent
	Skills          []string // skill name references
	PermissionMode  string
	MaxTurns        *int
	Color           string
	Hidden          bool
	Disabled        bool
	Source          string // "system", "user", "auto"
	SourcePath      string `json:"-"` // absolute path to the .md file
	TeamID          string // empty for shared agents
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Team is the service-level representation of a group of agents.
type Team struct {
	ID          string
	Name        string
	Description string
	LeadAgent   string   // agent ID reference
	Skills      []string // team-wide skill names
	Provider    string   // team default provider
	Model       string   // team default model
	Culture     string   // team culture document (markdown body)
	Source      string   // "system", "user", "auto"
	SourcePath  string   `json:"-"` // absolute path to the team directory
	IsAuto      bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TeamView bundles a Team with its resolved coordinator and worker agents.
// It replaces the tui.TeamView type and is the canonical view used everywhere
// a team is displayed with its members.
type TeamView struct {
	Team        Team
	Coordinator *Agent  // nil if no lead agent is set or found
	Workers     []Agent // all non-coordinator agents
	IsReadOnly  bool    // true if the team is read-only (e.g. Claude Code auto-discovered teams)
	IsSystem    bool    // true if the team is a system team (lives under the system config directory)
}

// Name returns the team name.
func (tv TeamView) Name() string { return tv.Team.Name }

// Description returns the team description.
func (tv TeamView) Description() string { return tv.Team.Description }

// Dir returns the team's source path (the team directory).
func (tv TeamView) Dir() string { return tv.Team.SourcePath }

// IsAuto returns true if the team was auto-detected rather than manually created.
func (tv TeamView) IsAuto() bool { return tv.Team.IsAuto }

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
// Used in promptModeState.pendingDispatch (the "confirm tool dispatch" flow) and
// in ChatMessage.ToolCalls.
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

// ChatEntry consolidates per-message data for the chat history display.
// It replaces the tui.ChatEntry type, which previously held a provider.Message.
type ChatEntry struct {
	Message    ChatMessage
	Timestamp  time.Time
	Reasoning  string // chain-of-thought reasoning text; empty for non-assistant messages
	ClaudeMeta string // byline / metadata string (e.g. "operator · claude-sonnet-4-6")
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
	ActiveSessions []AgentSession

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
	// For team generation, AgentNames holds the names of agents to assign.
	AgentNames []string
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

// ---------------------------------------------------------------------------
// Blocker types (used in the blocker modal)
// ---------------------------------------------------------------------------

// BlockerQuestion is a single question posed by a blocked agent, optionally
// with a set of predefined answer choices. If Options is empty the user
// provides a free-form text answer.
type BlockerQuestion struct {
	Text    string   // the question text
	Options []string // suggested answers; empty means free-form
	Answer  string   // the user's answer, populated after submission
}

// Blocker represents an active blocker reported by an agent that requires
// user input before work can continue. It is the canonical type used by both
// the service layer and the TUI blocker modal.
type Blocker struct {
	JobID          string
	TaskID         string
	TeamID         string
	AgentID        string
	Team           string // human-readable team name for display
	BlockerSummary string // short summary of what is blocked
	Context        string // additional context from the agent
	WhatWasTried   string // what the agent already attempted
	WhatIsNeeded   string // what the agent needs to proceed
	Questions      []BlockerQuestion
	Answered       bool
	RawBody        string // raw markdown body from the report_blocker tool call
}
