package db

import (
	"context"
	"encoding/json"
	"time"
)

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
	AssignTaskToGraph(ctx context.Context, id string, graphID string) error
	PreAssignTaskGraph(ctx context.Context, id string, graphID string) error
	// RetryTask re-dispatches a failed task: it transitions the task from
	// failed back to in_progress, re-sets the graph_id, and clears the stale
	// result fields. Only tasks currently in failed status are affected.
	RetryTask(ctx context.Context, id string, graphID string) error
	// SetTaskMetadata overwrites a task's metadata column in place, independent
	// of status or graph assignment. Used to persist fine-decompose's
	// toolchain choice onto the task record so later dispatches (retry,
	// serial-gate advance) can recover it via ParseTaskMetadata — a
	// graphexec.TaskRequest only carries Toolchain from the dispatch call
	// that built it, not from the task row.
	SetTaskMetadata(ctx context.Context, id string, metadata json.RawMessage) error
	AddTaskDependency(ctx context.Context, taskID, dependsOn string) error
	GetReadyTasks(ctx context.Context, jobID string) ([]*Task, error)
	// ListTaskDependents returns the tasks that declare a dependency on the
	// given task. Used to surface work that can never become ready while its
	// dependency sits in a failed state.
	ListTaskDependents(ctx context.Context, taskID string) ([]*Task, error)

	// Progress
	ReportProgress(ctx context.Context, report *ProgressReport) error
	GetRecentProgress(ctx context.Context, jobID string, limit int) ([]*ProgressReport, error)

	// Metrics — per-node execution and per-session aggregate statistics.
	// Foundation for future auto-tuning; feeds the service layer's
	// Metrics() report.
	InsertNodeExecution(ctx context.Context, exec *NodeExecution) error
	NodeExecutionStats(ctx context.Context) ([]*NodeExecutionStat, error)
	SessionStats(ctx context.Context) ([]*SessionStat, error)

	// Skills
	UpsertSkill(ctx context.Context, skill *Skill) error
	GetSkill(ctx context.Context, id string) (*Skill, error)
	ListSkills(ctx context.Context) ([]*Skill, error)
	DeleteAllSkills(ctx context.Context) error

	// Feed
	CreateFeedEntry(ctx context.Context, entry *FeedEntry) error
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
	// MarkSessionMessagesSuperseded flags rows at or below maxSeq as removed
	// from the live conversation by a compaction (kept for debugging).
	MarkSessionMessagesSuperseded(ctx context.Context, sessionID string, maxSeq int) error

	// Artifacts
	LogArtifact(ctx context.Context, artifact *Artifact) error
	ListArtifactsForJob(ctx context.Context, jobID string) ([]*Artifact, error)

	// Chat history — survives server restart for reconnect hydration.
	AppendChatEntry(ctx context.Context, entry *ChatEntry) error
	ListRecentChatEntries(ctx context.Context, limit int) ([]*ChatEntry, error)

	// Blockers — HITL prompt history. A row is created when a blocker is
	// raised and updated in place when it resolves; resolved rows remain
	// as browsable history.
	CreateBlocker(ctx context.Context, rec *BlockerRecord) error
	ResolveBlockerRecord(ctx context.Context, requestID, disposition, answer string, resolvedAt time.Time) error
	// ListBlockerHistory returns resolved blockers, newest-first.
	ListBlockerHistory(ctx context.Context, limit int) ([]*BlockerRecord, error)
	// ListPendingBlockers returns unresolved blockers, oldest-first — the
	// questions still waiting on a human. Used by the operator's handoff
	// digest.
	ListPendingBlockers(ctx context.Context) ([]*BlockerRecord, error)
	// SweepUnresolvedBlockers marks still-pending rows cancelled. Called at
	// startup: a pending row from a previous process has no waiting caller
	// anymore, so it can never be answered. Returns the number swept.
	SweepUnresolvedBlockers(ctx context.Context) (int, error)

	// Recovery
	//
	// ReconcileInterrupted reclaims rows orphaned by a previous process:
	// worker sessions still 'active' are marked failed (their runtime no
	// longer exists), and tasks still 'in_progress' are reset to 'pending'
	// so they become re-dispatchable. Without this sweep, ghost sessions
	// pollute every progress snapshot and a phantom in_progress task wedges
	// its job's serial-dispatch gate forever. Call once at startup, before
	// the runtime and graph executor start; the operator's recovery sweep
	// (Operator.recoverInterrupted) then re-dispatches the requeued tasks
	// once its event loop is live. Returns the counts of sessions failed
	// and tasks requeued.
	ReconcileInterrupted(ctx context.Context) (sessions int, tasks int, err error)

	// Lifecycle
	Close() error
}
