package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/uuid/v5"

	"github.com/jefflinse/toasters/internal/compose"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/runtime"
)

// TeamLeadSpawner is the interface for spawning team lead sessions.
// This is an alias for runtime.TeamLeadSpawner, re-exported here for
// convenience so callers don't need to import both packages.
type TeamLeadSpawner = runtime.TeamLeadSpawner

// SystemTools provides orchestration tools for system agents (planner,
// scheduler, blocker-handler). These are distinct from the operator's own
// tools — system agents use them to create/query jobs and tasks, assign
// work to teams, and communicate with users.
type SystemTools struct {
	store    db.Store
	composer *compose.Composer
	eventCh  chan<- Event
	spawner  TeamLeadSpawner
	workDir  string // global workspace directory; per-job subdirs are created under this
}

// NewSystemTools creates a new SystemTools instance.
func NewSystemTools(store db.Store, composer *compose.Composer, eventCh chan<- Event, spawner TeamLeadSpawner, workDir string) *SystemTools {
	return &SystemTools{
		store:    store,
		composer: composer,
		eventCh:  eventCh,
		spawner:  spawner,
		workDir:  workDir,
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
			Name:        "create_task",
			Description: "Create a new task on a job. Tasks are individual steps within a job that can be assigned to teams.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"job_id": {
						"type": "string",
						"description": "ID of the job this task belongs to"
					},
					"title": {
						"type": "string",
						"description": "Short title for the task"
					},
					"team_id": {
						"type": "string",
						"description": "Team to pre-assign the task to (optional)"
					}
				},
				"required": ["job_id", "title"]
			}`),
		},
		{
			Name:        "assign_task",
			Description: "Assign a pending task to a team. This spawns the team lead and starts execution. The task must be in pending status.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"task_id": {
						"type": "string",
						"description": "ID of the task to assign"
					},
					"team_id": {
						"type": "string",
						"description": "ID of the team to assign the task to"
					}
				},
				"required": ["task_id", "team_id"]
			}`),
		},
		{
			Name:        "query_teams",
			Description: "List all available teams with their descriptions, lead agents, and member counts.",
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
	case "create_task":
		return st.createTask(ctx, args)
	case "assign_task":
		return st.assignTask(ctx, args)
	case "query_teams":
		return st.queryTeams(ctx)
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

func (st *SystemTools) createJob(ctx context.Context, args json.RawMessage) (string, error) {
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

	result, err := json.Marshal(map[string]string{"job_id": job.ID})
	if err != nil {
		return "", fmt.Errorf("marshaling result: %w", err)
	}
	return string(result), nil
}

func (st *SystemTools) createTask(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		JobID  string `json:"job_id"`
		Title  string `json:"title"`
		TeamID string `json:"team_id"`
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

	taskID, err := uuid.NewV4()
	if err != nil {
		return "", fmt.Errorf("generating task ID: %w", err)
	}

	task := &db.Task{
		ID:     taskID.String(),
		JobID:  params.JobID,
		Title:  params.Title,
		Status: db.TaskStatusPending,
		TeamID: params.TeamID,
	}

	if err := st.store.CreateTask(ctx, task); err != nil {
		return "", fmt.Errorf("creating task: %w", err)
	}

	result, err := json.Marshal(map[string]string{"task_id": task.ID})
	if err != nil {
		return "", fmt.Errorf("marshaling result: %w", err)
	}
	return string(result), nil
}

func (st *SystemTools) assignTask(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		TaskID string `json:"task_id"`
		TeamID string `json:"team_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing assign_task args: %w", err)
	}

	if params.TaskID == "" {
		return "", fmt.Errorf("task_id is required")
	}
	if params.TeamID == "" {
		return "", fmt.Errorf("team_id is required")
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

	// 3. Get team to verify it exists and get its name.
	team, err := st.store.GetTeam(ctx, params.TeamID)
	if err != nil {
		return "", fmt.Errorf("getting team: %w", err)
	}

	// 3a. Reject assignments to the system team — it handles orchestration only.
	if team.Source == "system" {
		return "", fmt.Errorf("cannot assign job tasks to system team %q; use a user or auto team", team.Name)
	}

	// 3b. Validate that the team has a lead agent before we modify the task.
	if team.LeadAgent == "" {
		return "", fmt.Errorf("team %q has no lead agent configured; cannot assign task", team.Name)
	}

	// 4. Enforce serial execution: if another task in this job is already in progress,
	// pre-assign the team (so assignNextTask can pick it up later) but don't start it.
	allTasks, err := st.store.ListTasksForJob(ctx, task.JobID)
	if err != nil {
		return "", fmt.Errorf("checking job tasks: %w", err)
	}
	for _, t := range allTasks {
		if t.ID != params.TaskID && t.Status == db.TaskStatusInProgress {
			// Pre-assign the team so assignNextTask can start this task when it's ready.
			if err := st.store.PreAssignTaskTeam(ctx, params.TaskID, params.TeamID); err != nil {
				return "", fmt.Errorf("pre-assigning team: %w", err)
			}
			return fmt.Sprintf(
				"Task %q queued for team %s — task %q is currently in progress. "+
					"This task will start automatically when the current task completes.",
				task.Title, team.Name, t.Title,
			), nil
		}
	}

	// 5. No task in progress — start this one. Set status to in_progress and assign team.
	if err := st.store.AssignTask(ctx, params.TaskID, params.TeamID); err != nil {
		return "", fmt.Errorf("assigning task: %w", err)
	}

	// 6. Compose the team lead agent.
	composed, err := st.composer.Compose(ctx, team.LeadAgent, params.TeamID)
	if err != nil {
		return "", fmt.Errorf("composing team lead: %w", err)
	}

	// 7. Spawn team lead goroutine (fire-and-forget) with the job's workspace dir.
	if st.spawner == nil {
		return "", fmt.Errorf("cannot assign task: no agent spawner configured")
	}
	// Build the initial message for the team lead from the task and job context.
	initialMsg := fmt.Sprintf("Task: %s\n\nJob: %s\n%s", task.Title, job.Title, job.Description)

	// Create team lead tools that send events through the operator's event channel.
	teamLeadTools := NewTeamLeadTools(st.store, st.eventCh, params.TaskID, task.JobID, params.TeamID)

	if err := st.spawner.SpawnTeamLead(ctx, composed, params.TaskID, task.JobID, job.WorkspaceDir, initialMsg, teamLeadTools); err != nil {
		return "", fmt.Errorf("spawning team lead: %w", err)
	}

	// After SpawnTeamLead succeeds, promote the job to active (only if still pending).
	if job.Status == db.JobStatusPending {
		if err := st.store.UpdateJobStatus(ctx, task.JobID, db.JobStatusActive); err != nil {
			slog.Warn("failed to mark job active", "job_id", task.JobID, "error", err)
			// non-fatal: the task is already assigned and the team lead is already spawned.
			// The job will not appear in the TUI Jobs panel until the next status transition.
		}
	}

	// 8. Send EventTaskStarted to the event channel.
	trySendEvent(ctx, st.eventCh, Event{
		Type: EventTaskStarted,
		Payload: TaskStartedPayload{
			TaskID: params.TaskID,
			JobID:  task.JobID,
			TeamID: params.TeamID,
			Title:  task.Title,
		},
	})

	return fmt.Sprintf("Task assigned to team %s", team.Name), nil
}

func (st *SystemTools) queryTeams(ctx context.Context) (string, error) {
	allTeams, err := st.store.ListTeams(ctx)
	if err != nil {
		return "", fmt.Errorf("listing teams: %w", err)
	}

	// Filter out the system team — it handles orchestration, not job tasks.
	var teams []*db.Team
	for _, t := range allTeams {
		if t.Source != "system" {
			teams = append(teams, t)
		}
	}

	if len(teams) == 0 {
		return "No teams available.", nil
	}

	// Batch-load all team agents in one query per team. For small team counts
	// (<10) this is fine; a single JOIN query would be an optimization for
	// larger scales but isn't needed yet.
	var b strings.Builder
	b.WriteString("Available teams:\n")

	for _, team := range teams {
		fmt.Fprintf(&b, "\n- %s (id: %s)\n", team.Name, team.ID)
		if team.Description != "" {
			fmt.Fprintf(&b, "  Description: %s\n", team.Description)
		}
		if team.LeadAgent != "" {
			fmt.Fprintf(&b, "  Lead: %s\n", team.LeadAgent)
		}
		members, err := st.store.ListTeamAgents(ctx, team.ID)
		if err != nil {
			slog.Warn("failed to list team agents", "team_id", team.ID, "error", err)
		} else {
			fmt.Fprintf(&b, "  Members: %d\n", len(members))
		}
	}

	return b.String(), nil
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
			if task.TeamID != "" {
				fmt.Fprintf(&b, " → team %s", task.TeamID)
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
