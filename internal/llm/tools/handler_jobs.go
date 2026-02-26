package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gofrs/uuid/v5"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/provider"
)

// dbJobStatusToTool maps db.JobStatus to tool-facing status strings.
func dbJobStatusToTool(s db.JobStatus) string {
	switch s {
	case db.JobStatusPending:
		return "pending"
	case db.JobStatusActive:
		return "active"
	case db.JobStatusPaused:
		return "paused"
	case db.JobStatusCompleted:
		return "done"
	case db.JobStatusFailed:
		return "failed"
	case db.JobStatusCancelled:
		return "cancelled"
	default:
		return string(s)
	}
}

func handleJobList(ctx context.Context, te *ToolExecutor, _ provider.ToolCall) (string, error) {
	if te.Store == nil {
		return "", fmt.Errorf("database not available")
	}
	jobs, err := te.Store.ListAllJobs(ctx)
	if err != nil {
		return "", fmt.Errorf("listing jobs: %w", err)
	}
	type item struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Status      string `json:"status"`
	}
	items := make([]item, len(jobs))
	for i, j := range jobs {
		items[i] = item{
			ID:          j.ID,
			Name:        j.Title,
			Description: j.Description,
			Status:      dbJobStatusToTool(j.Status),
		}
	}
	b, _ := json.Marshal(items)
	return string(b), nil
}

func handleJobCreate(ctx context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	if te.Store == nil {
		return "", fmt.Errorf("database not available")
	}
	var args struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing job_create args: %w", err)
	}
	if args.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	if args.Description == "" {
		return "", fmt.Errorf("description is required")
	}

	jobID, err := uuid.NewV4()
	if err != nil {
		return "", fmt.Errorf("generating job ID: %w", err)
	}

	// Create per-job workspace subdirectory under the global workspace.
	jobDir := filepath.Join(te.WorkspaceDir, jobID.String())
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return "", fmt.Errorf("creating job workspace directory: %w", err)
	}

	dbJob := &db.Job{
		ID:           jobID.String(),
		Title:        args.Name,
		Description:  args.Description,
		Type:         "general",
		Status:       db.JobStatusPending,
		WorkspaceDir: jobDir,
	}
	if err := te.Store.CreateJob(ctx, dbJob); err != nil {
		_ = os.Remove(jobDir) // best-effort cleanup of orphaned directory
		return "", fmt.Errorf("creating job: %w", err)
	}
	return "created: " + dbJob.ID, nil
}

func handleJobReadOverview(ctx context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	if te.Store == nil {
		return "", fmt.Errorf("database not available")
	}
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing job_read_overview args: %w", err)
	}
	j, err := te.Store.GetJob(ctx, args.ID)
	if err != nil {
		return "", fmt.Errorf("reading job %q: %w", args.ID, err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", j.Title)
	fmt.Fprintf(&sb, "**Status:** %s\n", dbJobStatusToTool(j.Status))
	fmt.Fprintf(&sb, "**Created:** %s\n", j.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	if j.Description != "" {
		fmt.Fprintf(&sb, "\n%s\n", j.Description)
	}
	return sb.String(), nil
}

func handleJobReadTodos(ctx context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	if te.Store == nil {
		return "", fmt.Errorf("database not available")
	}
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing job_read_todos args: %w", err)
	}
	tasks, err := te.Store.ListTasksForJob(ctx, args.ID)
	if err != nil {
		return "", fmt.Errorf("listing tasks for job %q: %w", args.ID, err)
	}
	var sb strings.Builder
	sb.WriteString("# Tasks\n\n")
	for _, t := range tasks {
		check := "[ ]"
		if t.Status == db.TaskStatusCompleted {
			check = "[x]"
		}
		fmt.Fprintf(&sb, "- %s %s (%s)\n", check, t.Title, string(t.Status))
	}
	return sb.String(), nil
}

func handleJobUpdateOverview(ctx context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	if te.Store == nil {
		return "", fmt.Errorf("database not available")
	}
	var args struct {
		ID      string `json:"id"`
		Content string `json:"content"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing job_update_overview args: %w", err)
	}
	if args.Mode != "overwrite" && args.Mode != "append" {
		return "", fmt.Errorf("invalid mode %q: must be 'overwrite' or 'append'", args.Mode)
	}

	newDesc := args.Content
	if args.Mode == "append" {
		j, err := te.Store.GetJob(ctx, args.ID)
		if err != nil {
			return "", fmt.Errorf("reading job %q for append: %w", args.ID, err)
		}
		newDesc = j.Description + "\n" + args.Content
	}

	update := db.JobUpdate{Description: &newDesc}
	if err := te.Store.UpdateJob(ctx, args.ID, update); err != nil {
		return "", fmt.Errorf("updating job %q: %w", args.ID, err)
	}
	return "ok", nil
}

func handleJobAddTodo(ctx context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	if te.Store == nil {
		return "", fmt.Errorf("database not available")
	}
	var args struct {
		ID   string `json:"id"`
		Task string `json:"task"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing job_add_todo args: %w", err)
	}
	taskID, err := uuid.NewV4()
	if err != nil {
		return "", fmt.Errorf("generating task ID: %w", err)
	}
	task := &db.Task{
		ID:     taskID.String(),
		JobID:  args.ID,
		Title:  args.Task,
		Status: db.TaskStatusPending,
	}
	if err := te.Store.CreateTask(ctx, task); err != nil {
		return "", fmt.Errorf("adding task to job %q: %w", args.ID, err)
	}
	return "ok", nil
}

func handleJobCompleteTodo(ctx context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	if te.Store == nil {
		return "", fmt.Errorf("database not available")
	}
	var args struct {
		ID          string `json:"id"`
		IndexOrText string `json:"index_or_text"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing job_complete_todo args: %w", err)
	}
	tasks, err := te.Store.ListTasksForJob(ctx, args.ID)
	if err != nil {
		return "", fmt.Errorf("listing tasks for job %q: %w", args.ID, err)
	}

	// Try to match by 1-based index first.
	if idx, parseErr := strconv.Atoi(args.IndexOrText); parseErr == nil {
		if idx < 1 || idx > len(tasks) {
			return "", fmt.Errorf("task index %d out of range (1-%d)", idx, len(tasks))
		}
		if err := te.Store.UpdateTaskStatus(ctx, tasks[idx-1].ID, db.TaskStatusCompleted, ""); err != nil {
			return "", fmt.Errorf("completing task: %w", err)
		}
		return "ok", nil
	}

	// Fall back to substring match on title.
	for _, t := range tasks {
		if strings.Contains(t.Title, args.IndexOrText) {
			if err := te.Store.UpdateTaskStatus(ctx, t.ID, db.TaskStatusCompleted, ""); err != nil {
				return "", fmt.Errorf("completing task: %w", err)
			}
			return "ok", nil
		}
	}
	return "", fmt.Errorf("no task matching %q in job %q", args.IndexOrText, args.ID)
}

func handleTaskSetStatus(ctx context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	if te.Store == nil {
		return "", fmt.Errorf("database not available")
	}
	var args struct {
		JobID  string `json:"job_id"`
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing task_set_status args: %w", err)
	}
	statusMap := map[string]db.TaskStatus{
		"active": db.TaskStatusInProgress,
		"done":   db.TaskStatusCompleted,
		"paused": db.TaskStatusPending,
	}
	dbStatus, ok := statusMap[args.Status]
	if !ok {
		return fmt.Sprintf("invalid status %q: must be one of active, done, paused", args.Status), nil
	}

	// Verify the task exists and belongs to the specified job.
	tasks, err := te.Store.ListTasksForJob(ctx, args.JobID)
	if err != nil {
		return "", fmt.Errorf("listing tasks: %w", err)
	}
	for _, t := range tasks {
		if t.ID == args.TaskID {
			if err := te.Store.UpdateTaskStatus(ctx, args.TaskID, dbStatus, ""); err != nil {
				return "", fmt.Errorf("setting task status: %w", err)
			}
			return fmt.Sprintf("task %s status set to %s", args.TaskID, args.Status), nil
		}
	}
	return fmt.Sprintf("task %q not found in job %q", args.TaskID, args.JobID), nil
}

func handleJobSetStatus(ctx context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	if te.Store == nil {
		return "", fmt.Errorf("database not available")
	}
	var args struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing job_set_status args: %w", err)
	}
	statusMap := map[string]db.JobStatus{
		"active":    db.JobStatusActive,
		"done":      db.JobStatusCompleted,
		"cancelled": db.JobStatusCancelled,
		"paused":    db.JobStatusPaused,
	}
	dbStatus, ok := statusMap[args.Status]
	if !ok {
		return fmt.Sprintf("invalid status %q: must be one of active, done, cancelled, paused", args.Status), nil
	}
	if err := te.Store.UpdateJobStatus(ctx, args.ID, dbStatus); err != nil {
		return "", fmt.Errorf("updating job status: %w", err)
	}
	return fmt.Sprintf("job %s status set to %s", args.ID, args.Status), nil
}
