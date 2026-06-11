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
	// RetryTask re-dispatches a failed task: it transitions the task from
	// failed back to in_progress, re-sets the graph_id, and clears the stale
	// result fields. Only tasks currently in failed status are affected.
	RetryTask(ctx context.Context, id string, graphID string) error
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

	// Feed
	CreateFeedEntry(ctx context.Context, entry *FeedEntry) error
	ListFeedEntries(ctx context.Context, jobID string, limit int) ([]*FeedEntry, error)
	ListRecentFeedEntries(ctx context.Context, limit int) ([]*FeedEntry, error)

	// Rebuild — wraps delete-all + insert-all in a transaction
	RebuildDefinitions(ctx context.Context, skills []*Skill) error

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

	// Recovery
	//
	// ReconcileInterrupted marks rows orphaned by a previous process as
	// failed: worker sessions still 'active' and tasks still 'in_progress'
	// belong to a runtime that no longer exists. Without this sweep, ghost
	// sessions pollute every progress snapshot and a phantom in_progress
	// task wedges its job's serial-dispatch gate forever. Call once at
	// startup, before the runtime and graph executor start.
	ReconcileInterrupted(ctx context.Context) (sessions int, tasks int, err error)

	// Lifecycle
	Close() error
}
