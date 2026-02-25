package db

import "context"

// Store defines all database operations for the toasters persistence layer.
type Store interface {
	// Jobs
	CreateJob(ctx context.Context, job *Job) error
	GetJob(ctx context.Context, id string) (*Job, error)
	ListJobs(ctx context.Context, filter JobFilter) ([]*Job, error)
	UpdateJobStatus(ctx context.Context, id string, status JobStatus) error

	// Tasks
	CreateTask(ctx context.Context, task *Task) error
	GetTask(ctx context.Context, id string) (*Task, error)
	ListTasksForJob(ctx context.Context, jobID string) ([]*Task, error)
	UpdateTaskStatus(ctx context.Context, id string, status TaskStatus, summary string) error
	AddTaskDependency(ctx context.Context, taskID, dependsOn string) error
	GetReadyTasks(ctx context.Context, jobID string) ([]*Task, error)

	// Progress
	ReportProgress(ctx context.Context, report *ProgressReport) error
	GetRecentProgress(ctx context.Context, jobID string, limit int) ([]*ProgressReport, error)

	// Agents
	UpsertAgent(ctx context.Context, agent *Agent) error
	GetAgent(ctx context.Context, id string) (*Agent, error)
	ListAgents(ctx context.Context) ([]*Agent, error)

	// Teams
	CreateTeam(ctx context.Context, team *Team) error
	GetTeam(ctx context.Context, id string) (*Team, error)
	ListTeams(ctx context.Context) ([]*Team, error)
	AddTeamMember(ctx context.Context, member *TeamMember) error

	// Sessions
	CreateSession(ctx context.Context, session *AgentSession) error
	UpdateSession(ctx context.Context, id string, update SessionUpdate) error
	GetActiveSessions(ctx context.Context) ([]*AgentSession, error)

	// Artifacts
	LogArtifact(ctx context.Context, artifact *Artifact) error
	ListArtifactsForJob(ctx context.Context, jobID string) ([]*Artifact, error)

	// Lifecycle
	Close() error
}
