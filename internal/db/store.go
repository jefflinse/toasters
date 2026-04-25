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
	CompleteTask(ctx context.Context, id string, status TaskStatus, summary, recommendations string) error
	AssignTaskToGraph(ctx context.Context, id string, graphID string) error
	PreAssignTaskGraph(ctx context.Context, id string, graphID string) error
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

	// Workers
	UpsertWorker(ctx context.Context, worker *Worker) error
	GetWorker(ctx context.Context, id string) (*Worker, error)
	ListWorkers(ctx context.Context) ([]*Worker, error)
	DeleteAllWorkers(ctx context.Context) error

	// Feed
	CreateFeedEntry(ctx context.Context, entry *FeedEntry) error
	ListFeedEntries(ctx context.Context, jobID string, limit int) ([]*FeedEntry, error)
	ListRecentFeedEntries(ctx context.Context, limit int) ([]*FeedEntry, error)

	// Rebuild — wraps delete-all + insert-all in a transaction
	RebuildDefinitions(ctx context.Context, skills []*Skill, workers []*Worker) error

	// Sessions
	CreateSession(ctx context.Context, session *WorkerSession) error
	UpdateSession(ctx context.Context, id string, update SessionUpdate) error
	GetActiveSessions(ctx context.Context) ([]*WorkerSession, error)
	ListSessionsForTask(ctx context.Context, taskID string) ([]*WorkerSession, error)
	ListSessionsForJob(ctx context.Context, jobID string) ([]*WorkerSession, error)

	// Session transcripts
	AppendSessionMessage(ctx context.Context, msg *SessionMessage) error
	ListSessionMessages(ctx context.Context, sessionID string) ([]*SessionMessage, error)

	// Artifacts
	LogArtifact(ctx context.Context, artifact *Artifact) error
	ListArtifactsForJob(ctx context.Context, jobID string) ([]*Artifact, error)

	// Chat history — survives server restart for reconnect hydration.
	AppendChatEntry(ctx context.Context, entry *ChatEntry) error
	ListRecentChatEntries(ctx context.Context, limit int) ([]*ChatEntry, error)

	// Lifecycle
	Close() error
}
