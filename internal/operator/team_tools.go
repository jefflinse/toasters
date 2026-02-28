package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/runtime"
)

// TeamLeadTools provides tools for team lead agents.
// These are layered on top of worker tools (CoreTools + MCP).
type TeamLeadTools struct {
	store   db.Store
	eventCh chan<- Event
	taskID  string // the task this lead is working on
	jobID   string // the job this task belongs to
	teamID  string // the team this lead belongs to
}

// NewTeamLeadTools creates a new TeamLeadTools instance.
func NewTeamLeadTools(store db.Store, eventCh chan<- Event, taskID, jobID, teamID string) *TeamLeadTools {
	return &TeamLeadTools{
		store:   store,
		eventCh: eventCh,
		taskID:  taskID,
		jobID:   jobID,
		teamID:  teamID,
	}
}

// Definitions returns the tool definitions available to team lead agents.
func (tl *TeamLeadTools) Definitions() []runtime.ToolDef {
	return []runtime.ToolDef{
		{
			Name:        "complete_task",
			Description: "Mark the team's current task as done. Provide a summary of what was accomplished and optional follow-up recommendations.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"summary": {
						"type": "string",
						"description": "Summary of what was accomplished"
					},
					"recommendations": {
						"type": "string",
						"description": "Optional follow-up recommendations for future work"
					}
				},
				"required": ["summary"]
			}`),
		},
		{
			Name:        "request_new_task",
			Description: "Recommend that a new job task be created. Use this when you discover additional work that should be done.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"description": {
						"type": "string",
						"description": "Description of the new task to create"
					},
					"reason": {
						"type": "string",
						"description": "Why this new task is needed"
					}
				},
				"required": ["description", "reason"]
			}`),
		},
		{
			Name:        "report_blocker",
			Description: "Report a blocker that the team cannot resolve on its own. This escalates the issue to the operator.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"description": {
						"type": "string",
						"description": "Description of the blocker"
					}
				},
				"required": ["description"]
			}`),
		},
		{
			Name:        "report_progress",
			Description: "Report progress on the current task. Use this to provide status updates.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"message": {
						"type": "string",
						"description": "Progress update message"
					}
				},
				"required": ["message"]
			}`),
		},
		{
			Name:        "query_job_context",
			Description: "Get context about the broader job this task belongs to, including all tasks and their statuses.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {}
			}`),
		},
		{
			Name:        "query_team_context",
			Description: "Get the team's culture document and context.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {}
			}`),
		},
	}
}

// Execute dispatches a tool call by name.
func (tl *TeamLeadTools) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "complete_task":
		return tl.completeTask(ctx, args)
	case "request_new_task":
		return tl.requestNewTask(ctx, args)
	case "report_blocker":
		return tl.reportBlocker(ctx, args)
	case "report_progress":
		return tl.reportProgress(ctx, args)
	case "query_job_context":
		return tl.queryJobContext(ctx)
	case "query_team_context":
		return tl.queryTeamContext(ctx)
	default:
		return "", fmt.Errorf("%w: %s", runtime.ErrUnknownTool, name)
	}
}

func (tl *TeamLeadTools) completeTask(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Summary         string `json:"summary"`
		Recommendations string `json:"recommendations"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing complete_task args: %w", err)
	}

	if params.Summary == "" {
		return "", fmt.Errorf("summary is required")
	}

	// Update task status, summary, and recommendations atomically.
	if err := tl.store.CompleteTask(ctx, tl.taskID, db.TaskStatusCompleted, params.Summary, params.Recommendations); err != nil {
		return "", fmt.Errorf("completing task: %w", err)
	}

	// Check if there are more pending tasks for this job.
	readyTasks, err := tl.store.GetReadyTasks(ctx, tl.jobID)
	if err != nil {
		return "", fmt.Errorf("checking ready tasks: %w", err)
	}

	// Send EventTaskCompleted.
	trySendEvent(ctx, tl.eventCh, Event{
		Type: EventTaskCompleted,
		Payload: TaskCompletedPayload{
			TaskID:          tl.taskID,
			JobID:           tl.jobID,
			TeamID:          tl.teamID,
			Summary:         params.Summary,
			Recommendations: params.Recommendations,
			HasNextTask:     len(readyTasks) > 0,
		},
	})

	return "Task completed successfully", nil
}

func (tl *TeamLeadTools) requestNewTask(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Description string `json:"description"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing request_new_task args: %w", err)
	}

	if params.Description == "" {
		return "", fmt.Errorf("description is required")
	}
	if params.Reason == "" {
		return "", fmt.Errorf("reason is required")
	}

	trySendEvent(ctx, tl.eventCh, Event{
		Type: EventNewTaskRequest,
		Payload: NewTaskRequestPayload{
			JobID:       tl.jobID,
			TeamID:      tl.teamID,
			Description: params.Description,
			Reason:      params.Reason,
		},
	})

	return fmt.Sprintf("New task request submitted: %s", params.Description), nil
}

func (tl *TeamLeadTools) reportBlocker(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing report_blocker args: %w", err)
	}

	if params.Description == "" {
		return "", fmt.Errorf("description is required")
	}

	// 1. Update task status to blocked.
	if err := tl.store.UpdateTaskStatus(ctx, tl.taskID, db.TaskStatusBlocked, params.Description); err != nil {
		return "", fmt.Errorf("updating task status: %w", err)
	}

	// 2. Send EventBlockerReported.
	trySendEvent(ctx, tl.eventCh, Event{
		Type: EventBlockerReported,
		Payload: BlockerReportedPayload{
			TaskID:      tl.taskID,
			TeamID:      tl.teamID,
			Description: params.Description,
		},
	})

	return fmt.Sprintf("Blocker reported: %s", params.Description), nil
}

func (tl *TeamLeadTools) reportProgress(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing report_progress args: %w", err)
	}

	if params.Message == "" {
		return "", fmt.Errorf("message is required")
	}

	// 1. Create progress report in DB.
	report := &db.ProgressReport{
		JobID:   tl.jobID,
		TaskID:  tl.taskID,
		Status:  "in_progress",
		Message: params.Message,
	}
	if err := tl.store.ReportProgress(ctx, report); err != nil {
		return "", fmt.Errorf("reporting progress: %w", err)
	}

	// 2. Send EventProgressUpdate.
	trySendEvent(ctx, tl.eventCh, Event{
		Type: EventProgressUpdate,
		Payload: ProgressUpdatePayload{
			TaskID:  tl.taskID,
			Message: params.Message,
		},
	})

	return "Progress reported", nil
}

func (tl *TeamLeadTools) queryJobContext(ctx context.Context) (string, error) {
	return formatJobContext(ctx, tl.store, tl.jobID)
}

func (tl *TeamLeadTools) queryTeamContext(ctx context.Context) (string, error) {
	return formatTeamContext(ctx, tl.store, tl.teamID)
}

// WorkerTools provides additional tools for team worker agents.
// Workers already have CoreTools (read_file, write_file, etc.) — these
// are the extra coordination tools layered on top.
type WorkerTools struct {
	store   db.Store
	eventCh chan<- Event
	taskID  string
	jobID   string
	teamID  string
}

// NewWorkerTools creates a new WorkerTools instance.
func NewWorkerTools(store db.Store, eventCh chan<- Event, taskID, jobID, teamID string) *WorkerTools {
	return &WorkerTools{
		store:   store,
		eventCh: eventCh,
		taskID:  taskID,
		jobID:   jobID,
		teamID:  teamID,
	}
}

// Definitions returns the tool definitions available to team worker agents.
func (wt *WorkerTools) Definitions() []runtime.ToolDef {
	return []runtime.ToolDef{
		{
			Name:        "report_progress",
			Description: "Report progress on the current task. Use this to provide status updates.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"message": {
						"type": "string",
						"description": "Progress update message"
					}
				},
				"required": ["message"]
			}`),
		},
		{
			Name:        "query_team_context",
			Description: "Get the team's culture document and context.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {}
			}`),
		},
	}
}

// Execute dispatches a tool call by name.
func (wt *WorkerTools) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "report_progress":
		return wt.reportProgress(ctx, args)
	case "query_team_context":
		return wt.queryTeamContext(ctx)
	default:
		return "", fmt.Errorf("%w: %s", runtime.ErrUnknownTool, name)
	}
}

func (wt *WorkerTools) reportProgress(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing report_progress args: %w", err)
	}

	if params.Message == "" {
		return "", fmt.Errorf("message is required")
	}

	report := &db.ProgressReport{
		JobID:   wt.jobID,
		TaskID:  wt.taskID,
		Status:  "in_progress",
		Message: params.Message,
	}
	if err := wt.store.ReportProgress(ctx, report); err != nil {
		return "", fmt.Errorf("reporting progress: %w", err)
	}

	trySendEvent(ctx, wt.eventCh, Event{
		Type: EventProgressUpdate,
		Payload: ProgressUpdatePayload{
			TaskID:  wt.taskID,
			Message: params.Message,
		},
	})

	return "Progress reported", nil
}

func (wt *WorkerTools) queryTeamContext(ctx context.Context) (string, error) {
	return formatTeamContext(ctx, wt.store, wt.teamID)
}

// --- Shared helpers ---

// formatJobContext formats job and task information as readable context.
func formatJobContext(ctx context.Context, store db.Store, jobID string) (string, error) {
	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		return "", fmt.Errorf("getting job: %w", err)
	}

	tasks, err := store.ListTasksForJob(ctx, jobID)
	if err != nil {
		return "", fmt.Errorf("listing tasks: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Job: %s\n", job.Title)
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
			fmt.Fprintf(&b, "  - [%s] %s", task.Status, task.Title)
			if task.TeamID != "" {
				fmt.Fprintf(&b, " (team: %s)", task.TeamID)
			}
			if task.Summary != "" {
				fmt.Fprintf(&b, " — %s", task.Summary)
			}
			b.WriteString("\n")
		}
	}

	return b.String(), nil
}

// formatTeamContext returns the team's culture document.
func formatTeamContext(ctx context.Context, store db.Store, teamID string) (string, error) {
	team, err := store.GetTeam(ctx, teamID)
	if err != nil {
		return "", fmt.Errorf("getting team: %w", err)
	}

	if team.Culture == "" {
		return "No team culture document available", nil
	}

	return team.Culture, nil
}

// contractHome replaces the user's home directory prefix with "~/" for
// shorter, more readable paths in tool output. If the home directory
// cannot be determined or the path is not under it, the path is returned
// unchanged.
func contractHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if strings.HasPrefix(path, home+"/") {
		return "~/" + path[len(home)+1:]
	}
	if path == home {
		return "~"
	}
	return path
}
