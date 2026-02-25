package db

import (
	"encoding/json"
	"time"
)

// JobStatus represents the lifecycle state of a job.
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusActive    JobStatus = "active"
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

// SessionStatus represents the lifecycle state of an agent session.
type SessionStatus string

const (
	SessionStatusActive    SessionStatus = "active"
	SessionStatusCompleted SessionStatus = "completed"
	SessionStatusFailed    SessionStatus = "failed"
	SessionStatusCancelled SessionStatus = "cancelled"
)

// Job represents a unit of work managed by the orchestrator.
type Job struct {
	ID        string
	Title     string
	Type      string // bug_fix, new_feature, prototype, review
	Status    JobStatus
	CreatedAt time.Time
	UpdatedAt time.Time
	Metadata  json.RawMessage // extensible JSON blob
}

// Task represents a single step within a job.
type Task struct {
	ID        string
	JobID     string
	Title     string
	Status    TaskStatus
	AgentID   string // assigned agent (may be empty)
	ParentID  string // DAG edge, empty for root tasks
	SortOrder int
	CreatedAt time.Time
	UpdatedAt time.Time
	Summary   string          // completion summary or failure reason
	Metadata  json.RawMessage // extensible JSON blob
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

// Agent represents a configured LLM agent.
type Agent struct {
	ID           string
	Name         string
	Description  string
	Mode         string // coordinator, worker
	Model        string
	Provider     string
	Temperature  *float64
	SystemPrompt string
	Tools        json.RawMessage // JSON array of tool names
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Source       string // file, database, template
}

// Team represents a group of agents that work together.
type Team struct {
	ID          string
	Name        string
	Description string
	Coordinator string // agent ID
	CreatedAt   time.Time
	Metadata    json.RawMessage
}

// TeamMember represents an agent's membership in a team.
type TeamMember struct {
	TeamID  string
	AgentID string
	Role    string // coordinator, worker
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

// SessionUpdate specifies fields to update on an agent session.
type SessionUpdate struct {
	Status    *SessionStatus
	TokensIn  *int64
	TokensOut *int64
	EndedAt   *time.Time
	CostUSD   *float64
}
