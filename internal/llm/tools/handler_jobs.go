package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/job"
	"github.com/jefflinse/toasters/internal/provider"
)

func handleJobList(_ context.Context, te *ToolExecutor, _ provider.ToolCall) (string, error) {
	jobs, err := job.List(te.WorkspaceDir)
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
		items[i] = item{ID: j.ID, Name: j.Name, Description: j.Description, Status: string(j.Status)}
	}
	b, _ := json.Marshal(items)
	return string(b), nil
}

func handleJobCreate(_ context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	var args struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing job_create args: %w", err)
	}
	j, err := job.Create(te.WorkspaceDir, args.ID, args.Name, args.Description)
	if err != nil {
		return "", fmt.Errorf("creating job: %w", err)
	}
	// Dual-write to SQLite if available.
	if te.Store != nil {
		ctx := context.Background()
		dbJob := &db.Job{
			ID:     j.ID,
			Title:  args.Name,
			Type:   "general",
			Status: db.JobStatusPending,
		}
		if dbErr := te.Store.CreateJob(ctx, dbJob); dbErr != nil {
			slog.Warn("failed to persist job to SQLite", "job", j.ID, "error", dbErr)
		}
	}
	return "created: " + j.ID, nil
}

func handleJobReadOverview(_ context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing job_read_overview args: %w", err)
	}
	dir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.ID)
	return job.ReadOverview(dir)
}

func handleJobReadTodos(_ context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing job_read_todos args: %w", err)
	}
	dir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.ID)
	return job.ReadTodos(dir)
}

func handleJobUpdateOverview(_ context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
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
	dir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.ID)
	var overviewErr error
	if args.Mode == "overwrite" {
		overviewErr = job.WriteOverview(dir, args.Content)
	} else {
		overviewErr = job.AppendOverview(dir, args.Content)
	}
	if overviewErr != nil {
		return "", overviewErr
	}
	return "ok", nil
}

func handleJobAddTodo(_ context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	var args struct {
		ID   string `json:"id"`
		Task string `json:"task"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing job_add_todo args: %w", err)
	}
	dir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.ID)
	if err := job.AddTodo(dir, args.Task); err != nil {
		return "", err
	}
	return "ok", nil
}

func handleJobCompleteTodo(_ context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	var args struct {
		ID          string `json:"id"`
		IndexOrText string `json:"index_or_text"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing job_complete_todo args: %w", err)
	}
	dir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.ID)
	if err := job.CompleteTodo(dir, args.IndexOrText); err != nil {
		return "", err
	}
	return "ok", nil
}

func handleTaskSetStatus(_ context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	var args struct {
		JobID  string `json:"job_id"`
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing task_set_status args: %w", err)
	}
	validStatuses := map[string]bool{"active": true, "done": true, "paused": true}
	if !validStatuses[args.Status] {
		return fmt.Sprintf("invalid status %q: must be one of active, done, paused", args.Status), nil
	}
	jobDir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.JobID)
	tasks, err := job.ListTasks(jobDir)
	if err != nil {
		return "", fmt.Errorf("listing tasks: %w", err)
	}
	for _, t := range tasks {
		if t.ID == args.TaskID {
			if err := job.SetTaskStatus(t.Dir, job.Status(args.Status)); err != nil {
				return "", fmt.Errorf("setting task status: %w", err)
			}
			return fmt.Sprintf("task %s status set to %s", args.TaskID, args.Status), nil
		}
	}
	return fmt.Sprintf("task %q not found in job %q", args.TaskID, args.JobID), nil
}

func handleJobSetStatus(_ context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	var args struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing job_set_status args: %w", err)
	}
	validStatuses := map[string]bool{"active": true, "done": true, "cancelled": true, "paused": true}
	if !validStatuses[args.Status] {
		return fmt.Sprintf("invalid status %q: must be one of active, done, cancelled, paused", args.Status), nil
	}
	dir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.ID)
	updates := map[string]string{"status": args.Status}
	if args.Status == "done" {
		updates["completed"] = time.Now().UTC().Format(time.RFC3339)
	}
	if err := job.UpdateFrontmatter(dir, updates); err != nil {
		return "", fmt.Errorf("updating job status: %w", err)
	}
	// Dual-write to SQLite if available.
	if te.Store != nil {
		ctx := context.Background()
		dbStatus := mapJobStatus(args.Status)
		if dbErr := te.Store.UpdateJobStatus(ctx, args.ID, dbStatus); dbErr != nil {
			slog.Warn("failed to update job status in SQLite", "job", args.ID, "error", dbErr)
		}
	}
	return fmt.Sprintf("job %s status set to %s", args.ID, args.Status), nil
}
