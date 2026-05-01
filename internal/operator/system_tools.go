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
}

// NewSystemTools creates a new SystemTools instance from a config struct.
func NewSystemTools(cfg SystemToolsConfig) *SystemTools {
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
	}
}

// Definitions returns the tool definitions available to system agents.
func (st *SystemTools) Definitions() []runtime.ToolDef {
	return []runtime.ToolDef{
		{
			Name:        "create_job",
			Description: "Create a new job. A job is a top-level unit of work that contains tasks. A per-job workspace directory is automatically created.",
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
					}
				},
				"required": ["title", "description"]
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
			Description: "Get the current state of a job including all its tasks and their statuses.",
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
			Name:        "query_job_context",
			Description: "Query the context of a job, including its tasks and their current status.",
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
	case "assign_task":
		// Retained for internal use by the operator event loop's
		// assignNextTask, which dispatches the next ready task via the
		// same graph-dispatch code path the retired LLM tool used. Not
		// exposed in Definitions() — no LLM surface for it.
		return st.assignTask(ctx, args)
	case "query_graphs":
		return st.queryGraphs()
	case "query_job":
		return st.queryJob(ctx, args)
	case "query_job_context":
		return st.queryJobContext(ctx, args)
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
		Title       string `json:"title"`
		Description string `json:"description"`
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

	// Create per-job workspace subdirectory under the global workspace.
	jobDir := filepath.Join(st.workDir, jobID.String())
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
		_ = os.Remove(jobDir) // best-effort cleanup of orphaned directory
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
		JobID:          task.JobID,
		JobTitle:       job.Title,
		JobDescription: job.Description,
		TaskID:         task.ID,
		TaskTitle:      task.Title,
		GraphID:        graphID,
		WorkspaceDir:   job.WorkspaceDir,
		ProviderName:   st.defaultProvider,
		Model:          st.defaultModel,
	}
	go func() {
		if err := st.graphExecutor.ExecuteTask(
			context.Background(), // detach from operator context
			req,
		); err != nil {
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
			b.WriteString("\n")
		}
	}

	return b.String(), nil
}

func (st *SystemTools) queryJobContext(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing query_job_context args: %w", err)
	}
	if params.JobID == "" {
		return "", fmt.Errorf("job_id is required")
	}
	return formatJobContext(ctx, st.store, params.JobID)
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

