package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a requested entity does not exist.
var ErrNotFound = errors.New("not found")

// JobStatus represents the lifecycle state of a job.
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusSettingUp JobStatus = "setting_up"
	JobStatusActive    JobStatus = "active"
	JobStatusPaused    JobStatus = "paused"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
)

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
	TaskStatusBlocked    TaskStatus = "blocked"
	TaskStatusCancelled  TaskStatus = "cancelled"
)

// SessionStatus represents the lifecycle state of a worker session.
type SessionStatus string

const (
	SessionStatusActive    SessionStatus = "active"
	SessionStatusCompleted SessionStatus = "completed"
	SessionStatusFailed    SessionStatus = "failed"
	SessionStatusCancelled SessionStatus = "cancelled"
)

// FeedEntryType represents the kind of activity feed entry.
type FeedEntryType string

const (
	FeedEntryUserMessage       FeedEntryType = "user_message"
	FeedEntryOperatorMessage   FeedEntryType = "operator_message"
	FeedEntrySystemEvent       FeedEntryType = "system_event"
	FeedEntryConsultationTrace FeedEntryType = "consultation_trace"
	FeedEntryTaskStarted       FeedEntryType = "task_started"
	FeedEntryTaskCompleted     FeedEntryType = "task_completed"
	FeedEntryTaskFailed        FeedEntryType = "task_failed"
	FeedEntryBlockerReported   FeedEntryType = "blocker_reported"
	FeedEntryJobComplete       FeedEntryType = "job_complete"
)

// Job represents a unit of work managed by the orchestrator.
type Job struct {
	ID           string
	Title        string
	Description  string
	Type         string // bug_fix, new_feature, prototype, review
	Status       JobStatus
	WorkspaceDir string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Metadata     json.RawMessage // extensible JSON blob
}

// Task represents a single step within a job.
type Task struct {
	ID              string
	JobID           string
	Title           string
	Status          TaskStatus
	WorkerID        string // assigned worker (may be empty)
	GraphID         string // assigned graph definition id (may be empty)
	ParentID        string // DAG edge, empty for root tasks
	SortOrder       int
	DecomposeDepth  int // times this task (or its ancestors) has been split by fine-decompose
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Description     string          // what the task entails — the task's contract; immutable for the task's lifetime
	Summary         string          // completion summary or failure reason; overwritten by status updates
	Metadata        json.RawMessage // extensible JSON blob
	ResultSummary   string          // structured result summary
	Recommendations string          // structured recommendations
}

// TaskMetadata is the JSON shape stored in Task.Metadata for real
// (non-bootstrap) tasks. It currently carries only the toolchain chosen by
// fine-decompose, which slot-bound roles need via the `task.toolchain`
// artifact. Dispatch sites that rebuild a graphexec.TaskRequest after the
// task's initial dispatch — retry, serial-gate advance, and pre-assignment —
// read it back from here rather than losing it.
type TaskMetadata struct {
	Toolchain string `json:"toolchain,omitempty"`
}

// MarshalTaskMetadata encodes m for storage on Task.Metadata. Returns nil
// (not "{}") for the zero value so tasks that don't need metadata keep a
// NULL column.
func MarshalTaskMetadata(m TaskMetadata) (json.RawMessage, error) {
	if m == (TaskMetadata{}) {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshaling task metadata: %w", err)
	}
	return json.RawMessage(b), nil
}

// ParseTaskMetadata decodes a task's Metadata column. Empty or malformed
// metadata yields the zero value — callers treat "no toolchain recorded" as
// a normal, if degraded, case rather than an error, since not every task
// (or every graph) needs one.
func ParseTaskMetadata(raw json.RawMessage) TaskMetadata {
	var m TaskMetadata
	if len(raw) == 0 {
		return m
	}
	_ = json.Unmarshal(raw, &m)
	return m
}

// ProgressReport records a point-in-time status update for a job or task.
type ProgressReport struct {
	ID        int64
	JobID     string
	TaskID    string // may be empty
	WorkerID  string // may be empty
	Status    string // in_progress, blocked, completed, failed
	Message   string
	CreatedAt time.Time
}

// Skill represents a reusable capability that can be assigned to workers.
type Skill struct {
	ID          string
	Name        string
	Description string
	Tools       json.RawMessage // JSON array of tool names
	Prompt      string          // the skill's markdown body
	Source      string          // system, user, builtin
	SourcePath  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// FeedEntry represents a single entry in the activity feed.
type FeedEntry struct {
	ID        int64
	JobID     string // may be empty
	EntryType FeedEntryType
	Content   string
	Metadata  json.RawMessage // JSON blob for extra data
	CreatedAt time.Time
}

// WorkerSession tracks a single worker execution session.
type WorkerSession struct {
	ID           string
	WorkerID     string
	JobID        string // may be empty
	TaskID       string // may be empty
	Status       SessionStatus
	Model        string
	Provider     string
	TokensIn     int64
	TokensOut    int64
	StartedAt    time.Time
	EndedAt      *time.Time
	CostUSD      *float64
	SystemPrompt string // full system prompt sent to LLM
	ToolsJSON    string // JSON array of tool names available to the session
	// ContextTokens is the prompt size of the session's final model
	// round-trip (its context occupancy at completion). 0 means
	// unavailable, not "empty" — see SessionStat's tokens==0 handling.
	ContextTokens int64
	// ContextWindow is the resolved context window for the session's
	// provider/model at completion time. 0 means unresolved.
	ContextWindow int
}

// SessionMessage records a single message in a session's conversation transcript.
type SessionMessage struct {
	ID         int64
	SessionID  string
	Seq        int
	Role       string // "user", "assistant", "tool", "system" (compaction markers)
	Content    string
	ToolCalls  string // JSON-serialized []ToolCall for assistant messages
	ToolCallID string // for tool result messages
	// Superseded marks rows removed from the live conversation by a tier-2
	// compaction. The transcript keeps them for debugging; the model no
	// longer sees them.
	Superseded bool
	CreatedAt  time.Time
}

// ChatEntry records a single message in the operator/user conversation.
// Persisted so that a server restart can rehydrate the chat view.
type ChatEntry struct {
	ID        int64
	Timestamp time.Time
	Role      string // "user", "assistant", "tool", "system"
	Content   string
	Reasoning string // chain-of-thought; assistant only
	Meta      string // byline / model name string
	TurnID    string // operator turn correlation
}

// BlockerRecord persists one HITL prompt round (an ask_user from the operator
// or a graph-node interrupt) so resolved blockers stay browsable as history.
type BlockerRecord struct {
	RequestID   string
	Source      string // "" = operator; "graph:<node>" = node interrupt
	JobID       string
	TaskID      string
	Questions   string // JSON array of {question, options}
	CreatedAt   time.Time
	ResolvedAt  time.Time // zero while pending
	Disposition string    // "" pending | "answered" | "dismissed" | "cancelled"
	Answer      string
}

// Artifact records a file or output produced during a job.
type Artifact struct {
	ID        int64
	JobID     string
	TaskID    string // may be empty
	Type      string // code, report, investigation, test_results, other
	Path      string
	Summary   string
	CreatedAt time.Time
}

// JobFilter specifies criteria for listing jobs.
type JobFilter struct {
	Status *JobStatus
	Type   *string
	Limit  int
	Offset int
}

// JobUpdate specifies fields to update on a job.
type JobUpdate struct {
	Title        *string
	Description  *string
	Status       *JobStatus
	WorkspaceDir *string
}

// SessionUpdate specifies fields to update on a worker session.
type SessionUpdate struct {
	Status    *SessionStatus
	TokensIn  *int64
	TokensOut *int64
	EndedAt   *time.Time
	CostUSD   *float64
	// ContextTokens/ContextWindow, when set, record the session's final
	// context occupancy — see WorkerSession's doc comment on the same
	// fields. Populated once, at completion, alongside TokensIn/TokensOut.
	ContextTokens *int64
	ContextWindow *int
}

// NodeExecution records one logical execution of a graph node — a single
// row per outer middleware call. Rhizome retries of the same node inside
// that call are not separate rows (see the node_executions migration).
// Foundation for future auto-tuning: aggregated by NodeExecutionStats.
type NodeExecution struct {
	ID        string
	JobID     string
	TaskID    string
	GraphID   string // may be empty when not cheaply available at the call site
	Node      string
	Status    string // "completed", "failed", or a routing outcome (state.Status)
	ElapsedMS int64
	CreatedAt time.Time
}

// NodeExecutionStat aggregates node_executions rows by node name. Failure
// counts only status == "failed" — routing outcomes (e.g. "tests_passed",
// "needs_revision") are successful executions, just not the default one.
type NodeExecutionStat struct {
	Node         string
	Runs         int
	Failures     int
	AvgElapsedMS float64
	MinElapsedMS int64
	MaxElapsedMS int64
}

// SessionStat aggregates worker_sessions rows by worker id — the spawned
// worker's role name, or "graph:<node>" for graph-node sessions (see
// graphSession.WorkerID in internal/graphexec/sessions.go). Token and
// context-window averages exclude sessions with unavailable usage rather
// than treating a missing value as a real zero: many local
// OpenAI-compatible inference servers omit usage entirely, which would
// otherwise silently drag the averages toward zero.
type SessionStat struct {
	WorkerID string
	Sessions int
	Failures int
	// AvgDurationSeconds averages over sessions that have completed
	// (EndedAt set); in-flight sessions are excluded rather than
	// contributing a partial duration.
	AvgDurationSeconds float64
	// AvgTokensIn/AvgTokensOut exclude rows where that specific counter is
	// 0 (see UsageUnavailable for the both-zero case these overlap with).
	AvgTokensIn  float64
	AvgTokensOut float64
	// UsageUnavailable counts sessions where tokens_in == 0 AND
	// tokens_out == 0 — the signature of a provider that didn't report
	// usage, per the caveat in local_broadcast_operator.go.
	UsageUnavailable int
	// AvgContextPercent is context_tokens/context_window (0..1), averaged
	// only over sessions where both columns are > 0.
	AvgContextPercent float64
}
