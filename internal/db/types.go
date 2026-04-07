package db

import (
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned when a requested entity does not exist.
var ErrNotFound = errors.New("not found")

// JobStatus represents the lifecycle state of a job.
type JobStatus string

const (
	JobStatusPending     JobStatus = "pending"
	JobStatusSettingUp   JobStatus = "setting_up"
	JobStatusDecomposing JobStatus = "decomposing"
	JobStatusActive      JobStatus = "active"
	JobStatusPaused      JobStatus = "paused"
	JobStatusCompleted   JobStatus = "completed"
	JobStatusFailed      JobStatus = "failed"
	JobStatusCancelled   JobStatus = "cancelled"
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

// SessionStatus represents the lifecycle state of an agent session.
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
	AgentID         string // assigned agent (may be empty)
	TeamID          string // assigned team (may be empty)
	ParentID        string // DAG edge, empty for root tasks
	SortOrder       int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Summary         string          // completion summary or failure reason
	Metadata        json.RawMessage // extensible JSON blob
	ResultSummary   string          // structured result summary
	Recommendations string          // structured recommendations
}

// ProgressReport records a point-in-time status update for a job or task.
type ProgressReport struct {
	ID        int64
	JobID     string
	TaskID    string // may be empty
	AgentID   string // may be empty
	Status    string // in_progress, blocked, completed, failed
	Message   string
	CreatedAt time.Time
}

// Skill represents a reusable capability that can be assigned to agents.
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

// Agent represents a configured LLM agent.
type Agent struct {
	ID              string
	Name            string
	Description     string
	Mode            string // lead, worker
	Model           string
	Provider        string
	Temperature     *float64
	SystemPrompt    string
	Tools           json.RawMessage // JSON array of tool names
	DisallowedTools json.RawMessage // JSON array of disallowed tool names
	Skills          json.RawMessage // JSON array of skill name references
	PermissionMode  string
	Permissions     json.RawMessage // JSON blob
	MCPServers      json.RawMessage // JSON blob
	MaxTurns        *int
	Color           string
	Hidden          bool
	Disabled        bool
	Source          string // system, user, auto
	SourcePath      string
	TeamID          string // empty for shared agents
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Team represents a group of agents that work together.
type Team struct {
	ID          string
	Name        string
	Description string
	LeadAgent   string          // agent ID reference
	Skills      json.RawMessage // JSON array of team-wide skill names
	Provider    string          // team default
	Model       string          // team default
	Culture     string          // team culture document / markdown body
	Source      string          // system, user, auto
	SourcePath  string
	IsAuto      bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TeamAgent represents an agent's membership in a team.
type TeamAgent struct {
	TeamID  string
	AgentID string
	Role    string // lead, worker
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

// AgentSession tracks a single agent execution session.
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

// SessionUpdate specifies fields to update on an agent session.
type SessionUpdate struct {
	Status    *SessionStatus
	TokensIn  *int64
	TokensOut *int64
	EndedAt   *time.Time
	CostUSD   *float64
}
