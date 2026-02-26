package operator

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jefflinse/toasters/internal/compose"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/runtime"
)

// AgentSpawner is the interface for spawning team lead sessions.
type AgentSpawner interface {
	SpawnTeamLead(ctx context.Context, composed *compose.ComposedAgent, taskID string, jobID string) error
}

// SystemTools provides orchestration tools for system agents (planner,
// scheduler, blocker-handler). These are distinct from the operator's own
// tools — system agents use them to create/query jobs and tasks, assign
// work to teams, and communicate with users.
type SystemTools struct {
	store    db.Store
	composer *compose.Composer
	eventCh  chan<- Event
	spawner  AgentSpawner
}

// NewSystemTools creates a new SystemTools instance.
func NewSystemTools(store db.Store, composer *compose.Composer, eventCh chan<- Event, spawner AgentSpawner) *SystemTools {
	return &SystemTools{
		store:    store,
		composer: composer,
		eventCh:  eventCh,
		spawner:  spawner,
	}
}

// Definitions returns the tool definitions available to system agents.
func (st *SystemTools) Definitions() []runtime.ToolDef {
	return []runtime.ToolDef{
		{
			Name:        "create_job",
			Description: "Create a new job. A job is a top-level unit of work that contains tasks.",
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
					"workspace_dir": {
						"type": "string",
						"description": "Working directory for the job (optional)"
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
		{
			Name:        "relay_to_team",
			Description: "Send a message to a team. Use this to relay instructions, feedback, or context to a team that is working on a task.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"team_id": {
						"type": "string",
						"description": "ID of the team to relay the message to"
					},
					"message": {
						"type": "string",
						"description": "The message to send to the team"
					}
				},
				"required": ["team_id", "message"]
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
	case "surface_to_user":
		return st.surfaceToUser(ctx, args)
	case "relay_to_team":
		return st.relayToTeam(ctx, args)
	default:
		return "", fmt.Errorf("%w: %s", runtime.ErrUnknownTool, name)
	}
}

func (st *SystemTools) createJob(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Title        string `json:"title"`
		Description  string `json:"description"`
		WorkspaceDir string `json:"workspace_dir"`
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

	job := &db.Job{
		ID:           newID(),
		Title:        params.Title,
		Description:  params.Description,
		Status:       db.JobStatusPending,
		WorkspaceDir: params.WorkspaceDir,
	}

	if err := st.store.CreateJob(ctx, job); err != nil {
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

	task := &db.Task{
		ID:     newID(),
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

	// 2. Get team to verify it exists and get its name.
	team, err := st.store.GetTeam(ctx, params.TeamID)
	if err != nil {
		return "", fmt.Errorf("getting team: %w", err)
	}

	// 3. Update task: set status to in_progress and assign team.
	if err := st.store.AssignTask(ctx, params.TaskID, params.TeamID); err != nil {
		return "", fmt.Errorf("assigning task: %w", err)
	}

	// 4. Compose the team lead agent.
	composed, err := st.composer.Compose(ctx, team.LeadAgent, params.TeamID)
	if err != nil {
		return "", fmt.Errorf("composing team lead: %w", err)
	}

	// 5. Spawn team lead goroutine (fire-and-forget).
	if err := st.spawner.SpawnTeamLead(ctx, composed, params.TaskID, task.JobID); err != nil {
		return "", fmt.Errorf("spawning team lead: %w", err)
	}

	// 6. Send EventTaskStarted to the event channel.
	st.eventCh <- Event{
		Type: EventTaskStarted,
		Payload: TaskStartedPayload{
			TaskID: params.TaskID,
			JobID:  task.JobID,
			TeamID: params.TeamID,
			Title:  task.Title,
		},
	}

	return fmt.Sprintf("Task assigned to team %s", team.Name), nil
}

func (st *SystemTools) queryTeams(ctx context.Context) (string, error) {
	teams, err := st.store.ListTeams(ctx)
	if err != nil {
		return "", fmt.Errorf("listing teams: %w", err)
	}

	if len(teams) == 0 {
		return "No teams available.", nil
	}

	var b strings.Builder
	b.WriteString("Available teams:\n")

	for _, team := range teams {
		members, err := st.store.ListTeamAgents(ctx, team.ID)
		if err != nil {
			slog.Warn("failed to list team agents", "team_id", team.ID, "error", err)
			members = nil
		}

		b.WriteString(fmt.Sprintf("\n- %s (id: %s)\n", team.Name, team.ID))
		if team.Description != "" {
			b.WriteString(fmt.Sprintf("  Description: %s\n", team.Description))
		}
		if team.LeadAgent != "" {
			b.WriteString(fmt.Sprintf("  Lead: %s\n", team.LeadAgent))
		}
		b.WriteString(fmt.Sprintf("  Members: %d\n", len(members)))
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
	b.WriteString(fmt.Sprintf("Job: %s (id: %s)\n", job.Title, job.ID))
	b.WriteString(fmt.Sprintf("Status: %s\n", job.Status))
	if job.Description != "" {
		b.WriteString(fmt.Sprintf("Description: %s\n", job.Description))
	}

	if len(tasks) == 0 {
		b.WriteString("\nNo tasks.")
	} else {
		b.WriteString(fmt.Sprintf("\nTasks (%d):\n", len(tasks)))
		for _, task := range tasks {
			line := fmt.Sprintf("  - [%s] %s (id: %s)", task.Status, task.Title, task.ID)
			if task.TeamID != "" {
				line += fmt.Sprintf(" → team %s", task.TeamID)
			}
			b.WriteString(line + "\n")
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

func (st *SystemTools) relayToTeam(_ context.Context, args json.RawMessage) (string, error) {
	var params struct {
		TeamID  string `json:"team_id"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing relay_to_team args: %w", err)
	}

	if params.TeamID == "" {
		return "", fmt.Errorf("team_id is required")
	}
	if params.Message == "" {
		return "", fmt.Errorf("message is required")
	}

	// Placeholder: log the relay. Actual implementation needs team session
	// tracking, which comes in a later phase.
	slog.Info("relay to team",
		"team_id", params.TeamID,
		"message", params.Message,
	)

	return fmt.Sprintf("Relayed to team %s: %s", params.TeamID, params.Message), nil
}

// newID generates a random UUID-like identifier using crypto/rand.
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
