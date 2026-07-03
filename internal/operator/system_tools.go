package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/uuid/v5"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/graphexec"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/runtime"
)

// SystemEventBroadcaster is the surface SystemTools uses to publish state
// changes (job/task creation, task assignment) to the unified service event
// stream so subscribers see real-time activity instead of waiting for the
// next progress poll.
//
// This interface is satisfied by *service.LocalService. Defining it here in
// the operator package keeps the dependency direction one-way (service
// imports operator, never the reverse).
//
// Implementations are called synchronously from within tool execution, which
// runs on the operator event loop goroutine. Implementations MUST NOT block
// on the operator's event channel — broadcasting through a separate fan-out
// channel (as LocalService.broadcast does) is the expected pattern.
type SystemEventBroadcaster interface {
	BroadcastJobCreated(jobID, title, description string)
	BroadcastTaskCreated(taskID, jobID, title, teamID string)
	BroadcastTaskAssigned(taskID, jobID, teamID, title string)
}

// GraphTaskExecutor is an alias for graphexec.TaskExecutor, kept here so
// existing operator.Config consumers don't have to import graphexec directly.
type GraphTaskExecutor = graphexec.TaskExecutor

// GraphCatalog exposes the loaded graph Definitions to system tools so
// query_graphs can surface them to the decomposer and operator. Satisfied
// by *loader.Loader (via its Graphs() method); kept as an interface here
// to avoid an operator → loader import.
type GraphCatalog interface {
	Graphs() []*graphexec.Definition
}

// SystemTools provides orchestration tools for system workers (decomposer,
// scheduler, blocker-handler). These are distinct from the operator's own
// tools — system workers use them to create/query jobs and tasks, assign
// work to teams or graphs, and communicate with users.
type SystemTools struct {
	store           db.Store
	promptEngine    *prompt.Engine
	defaultProvider string
	defaultModel    string
	eventCh         chan<- Event
	graphExecutor   GraphTaskExecutor      // required for assign_task; tasks dispatch here
	graphCatalog    GraphCatalog           // optional; backs query_graphs
	workDir         string                 // global workspace directory; per-job subdirs are created under this
	broadcaster     SystemEventBroadcaster // optional; nil means no service event broadcast
	lifetimeCtx     context.Context        // outlives any single operator turn; used for detached graph dispatch
}

// SystemToolsConfig bundles SystemTools dependencies. Optional fields can be
// left zero — the corresponding capability (broadcaster, graph catalog) is
// then a no-op.
type SystemToolsConfig struct {
	Store           db.Store
	PromptEngine    *prompt.Engine
	DefaultProvider string
	DefaultModel    string
	EventCh         chan<- Event
	WorkDir         string
	Broadcaster     SystemEventBroadcaster
	GraphExecutor   GraphTaskExecutor
	GraphCatalog    GraphCatalog
	// LifetimeCtx is the context used for graph task dispatch goroutines, which
	// outlive the operator turn that triggered them. Should be the service-level
	// lifetime context so dispatched tasks are cancelled on Shutdown. If nil,
	// context.Background() is used (acceptable for tests but not production).
	LifetimeCtx context.Context
}

// NewSystemTools creates a new SystemTools instance from a config struct.
func NewSystemTools(cfg SystemToolsConfig) *SystemTools {
	lifetimeCtx := cfg.LifetimeCtx
	if lifetimeCtx == nil {
		lifetimeCtx = context.Background()
	}
	return &SystemTools{
		store:           cfg.Store,
		promptEngine:    cfg.PromptEngine,
		defaultProvider: cfg.DefaultProvider,
		defaultModel:    cfg.DefaultModel,
		eventCh:         cfg.EventCh,
		graphExecutor:   cfg.GraphExecutor,
		graphCatalog:    cfg.GraphCatalog,
		workDir:         cfg.WorkDir,
		broadcaster:     cfg.Broadcaster,
		lifetimeCtx:     lifetimeCtx,
	}
}

// Definitions returns the tool definitions available to system workers.
func (st *SystemTools) Definitions() []runtime.ToolDef {
	return []runtime.ToolDef{
		{
			Name:        "create_job",
			Description: "Create a new job. A job is a top-level unit of work that contains tasks. A per-job workspace directory is automatically created unless workspace_of_job is set.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"title": {
						"type": "string",
						"description": "Short title for the job"
					},
					"description": {
						"type": "string",
						"description": "Detailed description of what the job entails"
					},
					"workspace_of_job": {
						"type": "string",
						"description": "Optional ID of an existing job whose workspace this job should share. Use this when the work is a follow-up on a previous job's files (fixing, extending, or reviewing its output). Find the ID with list_jobs. Omit to create a fresh workspace."
					}
				},
				"required": ["title", "description"]
			}`),
		},
		{
			Name:        "create_task",
			Description: "Add a new task to an existing job. The task enters the job's queue as pending. If graph_id is omitted, the framework automatically selects a graph for the task; tasks run serially, so it starts when no sibling task is in progress. Use this when a graph requests follow-up work or completed work recommends a next step worth doing.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"job_id": {
						"type": "string",
						"description": "ID of the job the task belongs to"
					},
					"title": {
						"type": "string",
						"description": "Short title for the task"
					},
					"description": {
						"type": "string",
						"description": "What the task entails; passed to the worker as context"
					},
					"graph_id": {
						"type": "string",
						"description": "Optional graph to run the task on. Omit to let the framework choose one."
					}
				},
				"required": ["job_id", "title"]
			}`),
		},
		{
			Name:        "retry_task",
			Description: "Re-run a single failed task in place, on the same job, instead of creating a new job to redo the work. Use this when a task fails for a transient or fixable reason (an environment, dependency, or build issue, or something a clearer instruction would resolve). Optionally pass a different graph_id to retry on; by default it retries on the graph it failed on.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"task_id": {
						"type": "string",
						"description": "ID of the failed task to retry."
					},
					"graph_id": {
						"type": "string",
						"description": "Optional graph to retry on. Defaults to the graph the task previously failed on."
					}
				},
				"required": ["task_id"]
			}`),
		},
		{
			Name:        "query_graphs",
			Description: "List all available graphs with their ids, names, descriptions, and tags. Graphs are declarative, user-defined pipelines that execute a specific class of work — pick one before creating a task to target it (create_task graph_id, assign_task graph_id).",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {}
			}`),
		},
		{
			Name:        "query_job",
			Description: "Get the full current state of a job: title, status, description, workspace, and every task with its status, graph, IDs, and result summary. This is the ONE tool for inspecting a job — use it before retrying tasks, answering status questions, or deciding on follow-up work.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"job_id": {
						"type": "string",
						"description": "ID of the job to query"
					}
				},
				"required": ["job_id"]
			}`),
		},
		{
			Name:        "surface_to_user",
			Description: "Surface information to the user. Use this to relay important findings, summaries, questions, or status updates.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"text": {
						"type": "string",
						"description": "The text to show to the user"
					}
				},
				"required": ["text"]
			}`),
		},
	}
}

// Execute dispatches a tool call by name.
func (st *SystemTools) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "create_job":
		return st.createJob(ctx, args)
	case "create_task":
		return st.createTask(ctx, args)
	case "assign_task":
		// Retained for internal use by the operator event loop's
		// assignNextTask, which dispatches the next ready task via the
		// same graph-dispatch code path the retired LLM tool used. Not
		// exposed in Definitions() — no LLM surface for it.
		return st.assignTask(ctx, args)
	case "retry_task":
		return st.retryTask(ctx, args)
	case "query_graphs":
		return st.queryGraphs()
	case "query_job":
		return st.queryJob(ctx, args)
	case "query_job_context":
		// Retired alias — query_job now carries everything it returned.
		// Kept dispatchable so a stale persisted conversation replaying an
		// old tool call doesn't hard-error.
		return st.queryJob(ctx, args)
	case "surface_to_user":
		return st.surfaceToUser(ctx, args)
	default:
		return "", fmt.Errorf("%w: %s", runtime.ErrUnknownTool, name)
	}
}

// validateWorkDir checks that the workspace directory is under the user's home
// directory. This prevents an LLM from directing work to system directories
// like /tmp, /etc, or /var.
func validateWorkDir(workDir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("determining home directory: %w", err)
	}

	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Errorf("resolving workspace directory: %w", err)
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}

	// Resolve symlinks so that e.g. /var -> /private/var doesn't bypass the check.
	// Fall back to the absolute path if the directory doesn't exist yet (EvalSymlinks
	// requires the path to exist).
	resolvedWork, err := filepath.EvalSymlinks(absWork)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			resolvedWork = absWork
		} else {
			return fmt.Errorf("resolving workspace symlinks: %w", err)
		}
	}
	resolvedHome, err := filepath.EvalSymlinks(absHome)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			resolvedHome = absHome
		} else {
			return fmt.Errorf("resolving home directory symlinks: %w", err)
		}
	}

	// The workspace must be under the home directory (or equal to it).
	if resolvedWork != resolvedHome && !strings.HasPrefix(resolvedWork, resolvedHome+string(filepath.Separator)) {
		return fmt.Errorf("workspace directory %q is outside the user's home directory (%s); this is not allowed for safety", workDir, resolvedHome)
	}

	return nil
}

func (st *SystemTools) createJob(ctx context.Context, args json.RawMessage) (string, error) {
	if err := validateWorkDir(st.workDir); err != nil {
		return "", err
	}

	var params struct {
		Title          string `json:"title"`
		Description    string `json:"description"`
		WorkspaceOfJob string `json:"workspace_of_job"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing create_job args: %w", err)
	}

	if params.Title == "" {
		return "", fmt.Errorf("title is required")
	}
	if params.Description == "" {
		return "", fmt.Errorf("description is required")
	}

	jobID, err := uuid.NewV4()
	if err != nil {
		return "", fmt.Errorf("generating job ID: %w", err)
	}

	// The workspace is either shared with an existing job (follow-up work on
	// that job's files) or a fresh per-job subdirectory under the global
	// workspace. Reuse is keyed by job ID, never by path: a job ID is
	// something the model demonstrably handles correctly, while a re-typed
	// path is how a mangled UUID once split a job across two directories.
	var jobDir string
	reusedWorkspace := params.WorkspaceOfJob != ""
	if reusedWorkspace {
		src, err := st.store.GetJob(ctx, params.WorkspaceOfJob)
		if err != nil {
			return "", fmt.Errorf("workspace_of_job: looking up job %q: %w", params.WorkspaceOfJob, err)
		}
		if src.WorkspaceDir == "" {
			return "", fmt.Errorf("workspace_of_job: job %q has no workspace directory", params.WorkspaceOfJob)
		}
		jobDir = src.WorkspaceDir
	} else {
		jobDir = filepath.Join(st.workDir, jobID.String())
	}
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return "", fmt.Errorf("creating job workspace directory: %w", err)
	}

	job := &db.Job{
		ID:           jobID.String(),
		Title:        params.Title,
		Description:  params.Description,
		Status:       db.JobStatusPending,
		WorkspaceDir: jobDir,
	}

	if err := st.store.CreateJob(ctx, job); err != nil {
		if !reusedWorkspace {
			_ = os.Remove(jobDir) // best-effort cleanup of orphaned directory
		}
		return "", fmt.Errorf("creating job: %w", err)
	}

	if st.broadcaster != nil {
		st.broadcaster.BroadcastJobCreated(job.ID, job.Title, job.Description)
	}

	result, err := json.Marshal(map[string]string{"job_id": job.ID})
	if err != nil {
		return "", fmt.Errorf("marshaling result: %w", err)
	}
	return string(result), nil
}

// isTerminalJobStatus reports whether a job has finished running and will
// never resume on its own. Terminal jobs must not gain new tasks — the
// follow-up path is create_job with workspace_of_job, which starts a new job
// against the same workspace.
func isTerminalJobStatus(status db.JobStatus) bool {
	switch status {
	case db.JobStatusCompleted, db.JobStatusFailed, db.JobStatusCancelled:
		return true
	default:
		return false
	}
}

// createTask adds a pending task to an existing job. With a graph_id it
// dispatches immediately through the same path as assign_task (respecting
// serial execution); without one it broadcasts task-created with an empty
// graph so the service auto-runs fine-decompose to pick a graph and start
// the task when it becomes ready.
func (st *SystemTools) createTask(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		JobID       string `json:"job_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		GraphID     string `json:"graph_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing create_task args: %w", err)
	}
	if params.JobID == "" {
		return "", fmt.Errorf("job_id is required")
	}
	if params.Title == "" {
		return "", fmt.Errorf("title is required")
	}

	job, err := st.store.GetJob(ctx, params.JobID)
	if err != nil {
		return "", fmt.Errorf("getting job: %w", err)
	}
	if isTerminalJobStatus(job.Status) {
		return "", fmt.Errorf("job %s is %s; tasks cannot be added to a finished job. To continue this work, create a follow-up job with create_job and workspace_of_job: %q so it shares the same workspace", job.ID, job.Status, job.ID)
	}
	if params.GraphID != "" && st.graphCatalog != nil {
		known := false
		for _, g := range st.graphCatalog.Graphs() {
			if g.ID == params.GraphID {
				known = true
				break
			}
		}
		if !known {
			return "", fmt.Errorf("graph %q is not loaded (use query_graphs to list available graphs)", params.GraphID)
		}
	}

	siblings, err := st.store.ListTasksForJob(ctx, params.JobID)
	if err != nil {
		return "", fmt.Errorf("listing job tasks: %w", err)
	}

	taskID, err := uuid.NewV4()
	if err != nil {
		return "", fmt.Errorf("generating task ID: %w", err)
	}
	task := &db.Task{
		ID:    taskID.String(),
		JobID: params.JobID,
		Title: params.Title,
		// Description is the task's immutable contract; Summary starts as a
		// display copy of it but is overwritten by status updates.
		Description: params.Description,
		Status:      db.TaskStatusPending,
		Summary:     params.Description,
		SortOrder:   len(siblings),
	}
	if err := st.store.CreateTask(ctx, task); err != nil {
		return "", fmt.Errorf("creating task: %w", err)
	}

	// With an explicit graph, dispatch now (or queue behind an in-progress
	// sibling). Without one, the task-created broadcast triggers automatic
	// graph selection via fine-decompose.
	if params.GraphID != "" {
		if st.broadcaster != nil {
			st.broadcaster.BroadcastTaskCreated(task.ID, task.JobID, task.Title, params.GraphID)
		}
		return st.dispatchGraphTask(ctx, task, job, params.GraphID)
	}
	if st.broadcaster != nil {
		st.broadcaster.BroadcastTaskCreated(task.ID, task.JobID, task.Title, "")
	}

	result, err := json.Marshal(map[string]string{
		"task_id": task.ID,
		"job_id":  task.JobID,
		"status":  string(db.TaskStatusPending),
	})
	if err != nil {
		return "", fmt.Errorf("marshaling result: %w", err)
	}
	return string(result), nil
}

func (st *SystemTools) assignTask(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		TaskID  string `json:"task_id"`
		GraphID string `json:"graph_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing assign_task args: %w", err)
	}

	if params.TaskID == "" {
		return "", fmt.Errorf("task_id is required")
	}
	if params.GraphID == "" {
		return "", fmt.Errorf("graph_id is required")
	}

	// 1. Get task and verify it's pending.
	task, err := st.store.GetTask(ctx, params.TaskID)
	if err != nil {
		return "", fmt.Errorf("getting task: %w", err)
	}
	if task.Status != db.TaskStatusPending {
		return "", fmt.Errorf("task %q is %s, not pending", params.TaskID, task.Status)
	}

	// 2. Get the job to obtain the per-job workspace directory.
	job, err := st.store.GetJob(ctx, task.JobID)
	if err != nil {
		return "", fmt.Errorf("getting job for workspace: %w", err)
	}
	if isTerminalJobStatus(job.Status) {
		return "", fmt.Errorf("job %s is %s; tasks cannot be dispatched in a finished job. To continue this work, create a follow-up job with create_job and workspace_of_job: %q so it shares the same workspace", job.ID, job.Status, job.ID)
	}

	// 2a. Validate the job's workspace directory is under $HOME. This guards
	// against tampered database entries or jobs created before validation existed.
	if err := validateWorkDir(job.WorkspaceDir); err != nil {
		return "", fmt.Errorf("job workspace validation failed: %w", err)
	}

	return st.dispatchGraphTask(ctx, task, job, params.GraphID)
}

// dispatchGraphTask runs a task via a declarative graph. The graph_id is
// validated against the GraphCatalog so the operator sees a clear error
// before anything is dispatched. Provider/model fall back to the global
// defaults — graph-dispatched tasks don't carry per-team overrides yet.
func (st *SystemTools) dispatchGraphTask(ctx context.Context, task *db.Task, job *db.Job, graphID string) (string, error) {
	if st.graphExecutor == nil {
		return "", fmt.Errorf("cannot assign task: no graph executor configured")
	}
	if st.graphCatalog != nil {
		known := false
		for _, g := range st.graphCatalog.Graphs() {
			if g.ID == graphID {
				known = true
				break
			}
		}
		if !known {
			return "", fmt.Errorf("graph %q is not loaded (use query_graphs to list available graphs)", graphID)
		}
	}

	// Enforce serial execution: if another task in this job is already in
	// progress, persist the graph target but don't start now.
	allTasks, err := st.store.ListTasksForJob(ctx, task.JobID)
	if err != nil {
		return "", fmt.Errorf("checking job tasks: %w", err)
	}
	for _, t := range allTasks {
		if t.ID != task.ID && t.Status == db.TaskStatusInProgress {
			if err := st.store.PreAssignTaskGraph(ctx, task.ID, graphID); err != nil {
				return "", fmt.Errorf("pre-assigning graph: %w", err)
			}
			return fmt.Sprintf(
				"Task %q queued for graph %q — task %q is currently in progress. "+
					"This task will start automatically when the current task completes.",
				task.Title, graphID, t.Title,
			), nil
		}
	}

	// No in-progress siblings — mark in_progress and set the graph.
	if err := st.store.AssignTaskToGraph(ctx, task.ID, graphID); err != nil {
		return "", fmt.Errorf("assigning task to graph: %w", err)
	}

	req := graphexec.TaskRequest{
		JobID:           task.JobID,
		JobTitle:        job.Title,
		JobDescription:  job.Description,
		TaskID:          task.ID,
		TaskTitle:       task.Title,
		TaskDescription: task.Description,
		GraphID:         graphID,
		// Recovers the toolchain fine-decompose chose for this task (persisted
		// on task metadata) — this dispatch may be the serial-gate advance for
		// a task that was pre-assigned earlier, not the initial dispatch, so
		// the caller has no toolchain of its own to pass.
		Toolchain:    db.ParseTaskMetadata(task.Metadata).Toolchain,
		Siblings:     graphexec.FormatSiblingTitles(graphexec.SiblingTitles(allTasks, task.ID)),
		WorkspaceDir: job.WorkspaceDir,
		ProviderName: st.defaultProvider,
		Model:        st.defaultModel,
	}
	go func() {
		// Detach from the per-turn operator ctx, but stay scoped to the
		// service lifetime so Shutdown cancels in-flight graph tasks.
		if err := st.graphExecutor.ExecuteTask(st.lifetimeCtx, req); err != nil {
			slog.Error("graph task execution failed",
				"task_id", req.TaskID, "job_id", req.JobID, "graph_id", req.GraphID, "error", err)
		}
	}()

	// Broadcast assignment event.
	if st.broadcaster != nil {
		st.broadcaster.BroadcastTaskAssigned(task.ID, task.JobID, graphID, task.Title)
	}

	// Promote the job to active if still pending — without this, the TUI
	// Jobs panel never sees the job transition out of pending.
	if job.Status == db.JobStatusPending {
		if err := st.store.UpdateJobStatus(ctx, task.JobID, db.JobStatusActive); err != nil {
			slog.Warn("failed to mark job active", "job_id", task.JobID, "error", err)
		}
	}

	// Inline task-started feed entry — we must not send EventTaskStarted
	// through the operator channel because assignTask is itself called from
	// the event loop goroutine and a send-to-self could deadlock.
	content := fmt.Sprintf("⚡ %s started task: %s", graphID, task.Title)
	entry := &db.FeedEntry{
		EntryType: db.FeedEntryTaskStarted,
		Content:   content,
		JobID:     task.JobID,
	}
	if err := st.store.CreateFeedEntry(ctx, entry); err != nil {
		slog.Warn("failed to create task_started feed entry", "task_id", task.ID, "error", err)
	}
	slog.Info("task started", "task_id", task.ID, "job_id", task.JobID, "graph_id", graphID, "title", task.Title)

	result, err := json.Marshal(map[string]string{
		"task_id":  task.ID,
		"graph_id": graphID,
		"status":   "in_progress",
	})
	if err != nil {
		return "", fmt.Errorf("marshaling result: %w", err)
	}
	return string(result), nil
}

// retryTask re-runs a failed task in place: it clears the prior failure, resets
// the task to in_progress, and re-dispatches its graph — instead of the operator
// improvising a whole new job (which duplicates work and leaves the original job
// running). Mirrors the user-facing RetryTask path. graph_id is optional; it
// defaults to the graph the task previously failed on.
func (st *SystemTools) retryTask(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		TaskID  string `json:"task_id"`
		GraphID string `json:"graph_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing retry_task args: %w", err)
	}
	if params.TaskID == "" {
		return "", fmt.Errorf("task_id is required")
	}
	if st.graphExecutor == nil {
		return "", fmt.Errorf("cannot retry task: no graph executor configured")
	}

	task, err := st.store.GetTask(ctx, params.TaskID)
	if err != nil {
		return "", fmt.Errorf("getting task: %w", err)
	}
	if task.Status != db.TaskStatusFailed {
		return "", fmt.Errorf("task %q is %s, not failed — retry_task only re-runs failed tasks", task.Title, task.Status)
	}

	graphID := params.GraphID
	if graphID == "" {
		graphID = task.GraphID
	}
	if graphID == "" {
		return "", fmt.Errorf("task %q has no graph to retry; pass graph_id (use query_graphs to list)", task.Title)
	}
	if st.graphCatalog != nil {
		known := false
		for _, g := range st.graphCatalog.Graphs() {
			if g.ID == graphID {
				known = true
				break
			}
		}
		if !known {
			return "", fmt.Errorf("graph %q is not loaded (use query_graphs to list available graphs)", graphID)
		}
	}

	job, err := st.store.GetJob(ctx, task.JobID)
	if err != nil {
		return "", fmt.Errorf("getting job for workspace: %w", err)
	}
	if err := validateWorkDir(job.WorkspaceDir); err != nil {
		return "", fmt.Errorf("job workspace validation failed: %w", err)
	}

	allTasks, err := st.store.ListTasksForJob(ctx, task.JobID)
	if err != nil {
		return "", fmt.Errorf("listing job tasks: %w", err)
	}

	// Enforce serial execution, same as assign_task: retrying while a
	// sibling runs would put two graph executions in the same job workspace
	// concurrently. Returned as a message (not an error) so the model reads
	// it and retries after the in-progress task completes.
	for _, t := range allTasks {
		if t.ID != task.ID && t.Status == db.TaskStatusInProgress {
			return fmt.Sprintf(
				"Cannot retry task %q yet — task %q is currently in progress and tasks in a job run serially. "+
					"Retry after the current task completes.",
				task.Title, t.Title,
			), nil
		}
	}

	// Clear the failure and reset to in_progress on the (possibly new) graph.
	if err := st.store.RetryTask(ctx, task.ID, graphID); err != nil {
		return "", fmt.Errorf("resetting task for retry: %w", err)
	}
	req := graphexec.TaskRequest{
		JobID:           task.JobID,
		JobTitle:        job.Title,
		JobDescription:  job.Description,
		TaskID:          task.ID,
		TaskTitle:       task.Title,
		TaskDescription: task.Description,
		GraphID:         graphID,
		// task was fetched before RetryTask reset its status; RetryTask never
		// touches the metadata column, so the toolchain fine-decompose chose
		// at initial dispatch is still there — recover it rather than
		// dropping it, or slot-bound roles fail to compose on retry.
		Toolchain:    db.ParseTaskMetadata(task.Metadata).Toolchain,
		Siblings:     graphexec.FormatSiblingTitles(graphexec.SiblingTitles(allTasks, task.ID)),
		WorkspaceDir: job.WorkspaceDir,
		ProviderName: st.defaultProvider,
		Model:        st.defaultModel,
	}
	go func() {
		if err := st.graphExecutor.ExecuteTask(st.lifetimeCtx, req); err != nil {
			slog.Error("retry graph task execution failed",
				"task_id", req.TaskID, "job_id", req.JobID, "graph_id", req.GraphID, "error", err)
		}
	}()

	if st.broadcaster != nil {
		st.broadcaster.BroadcastTaskAssigned(task.ID, task.JobID, graphID, task.Title)
	}
	slog.Info("task retried", "task_id", task.ID, "job_id", task.JobID, "graph_id", graphID, "title", task.Title)

	return fmt.Sprintf("Retrying task %q on graph %q in place (job %q is unchanged).", task.Title, graphID, job.Title), nil
}

// queryGraphs renders the loaded graph catalog as markdown for the LLM.
// Each entry lists the graph id (what callers pass in graph_id), its name,
// description, and tags so the decomposer / operator can pick the right
// graph for a task.
func (st *SystemTools) queryGraphs() (string, error) {
	if st.graphCatalog == nil {
		return "No graphs are currently loaded.", nil
	}
	graphs := st.graphCatalog.Graphs()
	if len(graphs) == 0 {
		return "No graphs are currently loaded.", nil
	}

	var b strings.Builder
	b.WriteString("Available graphs:\n")
	for _, g := range graphs {
		fmt.Fprintf(&b, "\n- %s (id: %s)\n", displayName(g.Name, g.ID), g.ID)
		if g.Description != "" {
			fmt.Fprintf(&b, "  Description: %s\n", strings.TrimSpace(g.Description))
		}
		if len(g.Tags) > 0 {
			fmt.Fprintf(&b, "  Tags: %s\n", strings.Join(g.Tags, ", "))
		}
	}
	return b.String(), nil
}

// displayName returns name when set, else id. Keeps queryGraphs output
// readable for graphs authored without an explicit Name: field.
func displayName(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

func (st *SystemTools) queryJob(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing query_job args: %w", err)
	}

	if params.JobID == "" {
		return "", fmt.Errorf("job_id is required")
	}

	job, err := st.store.GetJob(ctx, params.JobID)
	if err != nil {
		return "", fmt.Errorf("getting job: %w", err)
	}

	tasks, err := st.store.ListTasksForJob(ctx, params.JobID)
	if err != nil {
		return "", fmt.Errorf("listing tasks: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Job: %s (id: %s)\n", job.Title, job.ID)
	fmt.Fprintf(&b, "Status: %s\n", job.Status)
	if job.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", job.Description)
	}
	if job.WorkspaceDir != "" {
		fmt.Fprintf(&b, "Workspace: %s\n", contractHome(job.WorkspaceDir))
	}

	if len(tasks) == 0 {
		b.WriteString("\nNo tasks.")
	} else {
		fmt.Fprintf(&b, "\nTasks (%d):\n", len(tasks))
		for _, task := range tasks {
			fmt.Fprintf(&b, "  - [%s] %s (id: %s)", task.Status, task.Title, task.ID)
			if task.GraphID != "" {
				fmt.Fprintf(&b, " → graph %s", task.GraphID)
			}
			if task.Description != "" {
				fmt.Fprintf(&b, "\n    Description: %s", task.Description)
			}
			// Summary is the latest status/result text; skip it while it
			// still just mirrors the description.
			if task.Summary != "" && task.Summary != task.Description {
				fmt.Fprintf(&b, "\n    Status: %s", task.Summary)
			}
			b.WriteString("\n")
		}
	}

	return b.String(), nil
}

func (st *SystemTools) surfaceToUser(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing surface_to_user args: %w", err)
	}

	if params.Text == "" {
		return "", fmt.Errorf("text is required")
	}

	entry := &db.FeedEntry{
		EntryType: db.FeedEntrySystemEvent,
		Content:   params.Text,
	}
	if err := st.store.CreateFeedEntry(ctx, entry); err != nil {
		return "", fmt.Errorf("creating feed entry: %w", err)
	}

	return fmt.Sprintf("Surfaced to user: %s", params.Text), nil
}
