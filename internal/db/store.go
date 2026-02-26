package db

import "context"

// Store defines all database operations for the toasters persistence layer.
type Store interface {
	// Jobs
	CreateJob(ctx context.Context, job *Job) error
	GetJob(ctx context.Context, id string) (*Job, error)
	ListJobs(ctx context.Context, filter JobFilter) ([]*Job, error)
	ListAllJobs(ctx context.Context) ([]*Job, error)
	UpdateJob(ctx context.Context, id string, update JobUpdate) error
	UpdateJobStatus(ctx context.Context, id string, status JobStatus) error

	// Tasks
	CreateTask(ctx context.Context, task *Task) error
	GetTask(ctx context.Context, id string) (*Task, error)
	ListTasksForJob(ctx context.Context, jobID string) ([]*Task, error)
	UpdateTaskStatus(ctx context.Context, id string, status TaskStatus, summary string) error
	UpdateTaskResult(ctx context.Context, id string, resultSummary, recommendations string) error
	AssignTask(ctx context.Context, id string, teamID string) error
	AddTaskDependency(ctx context.Context, taskID, dependsOn string) error
	GetReadyTasks(ctx context.Context, jobID string) ([]*Task, error)

	// Progress
	ReportProgress(ctx context.Context, report *ProgressReport) error
	GetRecentProgress(ctx context.Context, jobID string, limit int) ([]*ProgressReport, error)

	// Skills
	UpsertSkill(ctx context.Context, skill *Skill) error
	GetSkill(ctx context.Context, id string) (*Skill, error)
	ListSkills(ctx context.Context) ([]*Skill, error)
	DeleteAllSkills(ctx context.Context) error

	// Agents
	UpsertAgent(ctx context.Context, agent *Agent) error
	GetAgent(ctx context.Context, id string) (*Agent, error)
	ListAgents(ctx context.Context) ([]*Agent, error)
	DeleteAllAgents(ctx context.Context) error

	// Teams
	UpsertTeam(ctx context.Context, team *Team) error
	GetTeam(ctx context.Context, id string) (*Team, error)
	ListTeams(ctx context.Context) ([]*Team, error)
	DeleteAllTeams(ctx context.Context) error

	// Team Agents
	AddTeamAgent(ctx context.Context, ta *TeamAgent) error
	ListTeamAgents(ctx context.Context, teamID string) ([]*TeamAgent, error)
	DeleteAllTeamAgents(ctx context.Context) error

	// Feed
	CreateFeedEntry(ctx context.Context, entry *FeedEntry) error
	ListFeedEntries(ctx context.Context, jobID string, limit int) ([]*FeedEntry, error)
	ListRecentFeedEntries(ctx context.Context, limit int) ([]*FeedEntry, error)

	// Rebuild — wraps delete-all + insert-all in a transaction
	RebuildDefinitions(ctx context.Context, skills []*Skill, agents []*Agent, teams []*Team, teamAgents []*TeamAgent) error

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
