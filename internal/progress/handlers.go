package progress

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jefflinse/toasters/internal/db"
)

// ReportProgressParams holds parameters for the report_progress tool.
type ReportProgressParams struct {
	JobID   string `json:"job_id"`
	TaskID  string `json:"task_id"`
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// ReportBlockerParams holds parameters for the report_blocker tool.
type ReportBlockerParams struct {
	JobID       string `json:"job_id"`
	TaskID      string `json:"task_id"`
	AgentID     string `json:"agent_id"`
	Description string `json:"description"`
	Severity    string `json:"severity"` // "low", "medium", "high"
}

// UpdateTaskStatusParams holds parameters for the update_task_status tool.
type UpdateTaskStatusParams struct {
	JobID   string `json:"job_id"`
	TaskID  string `json:"task_id"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

// RequestReviewParams holds parameters for the request_review tool.
type RequestReviewParams struct {
	JobID        string `json:"job_id"`
	TaskID       string `json:"task_id"`
	AgentID      string `json:"agent_id"`
	ArtifactPath string `json:"artifact_path"`
	Notes        string `json:"notes"`
}

// QueryJobContextParams holds parameters for the query_job_context tool.
type QueryJobContextParams struct {
	JobID string `json:"job_id"`
}

// LogArtifactParams holds parameters for the log_artifact tool.
type LogArtifactParams struct {
	JobID   string `json:"job_id"`
	TaskID  string `json:"task_id"`
	Type    string `json:"type"`
	Path    string `json:"path"`
	Summary string `json:"summary"`
}

// validProgressStatuses is the set of accepted status values for report_progress.
var validProgressStatuses = map[string]bool{
	"in_progress":      true,
	"completed":        true,
	"failed":           true,
	"blocked":          true,
	"review_requested": true,
}

// ReportProgress records a progress update for a job or task.
func ReportProgress(ctx context.Context, store db.Store, params ReportProgressParams) (string, error) {
	if !validProgressStatuses[params.Status] {
		return "", fmt.Errorf("invalid status %q: must be one of in_progress, completed, failed, blocked", params.Status)
	}
	report := &db.ProgressReport{
		JobID:   params.JobID,
		TaskID:  params.TaskID,
		AgentID: params.AgentID,
		Status:  params.Status,
		Message: params.Message,
	}
	if err := store.ReportProgress(ctx, report); err != nil {
		return "", fmt.Errorf("reporting progress: %w", err)
	}
	return "progress reported", nil
}

// validSeverities is the set of accepted severity values for report_blocker.
var validSeverities = map[string]bool{"low": true, "medium": true, "high": true}

// ReportBlocker records a blocker that prevents an agent from proceeding.
func ReportBlocker(ctx context.Context, store db.Store, params ReportBlockerParams) (string, error) {
	if !validSeverities[params.Severity] {
		return "", fmt.Errorf("invalid severity %q: must be one of low, medium, high", params.Severity)
	}
	report := &db.ProgressReport{
		JobID:   params.JobID,
		TaskID:  params.TaskID,
		AgentID: params.AgentID,
		Status:  "blocked",
		Message: fmt.Sprintf("[%s] %s", params.Severity, params.Description),
	}
	if err := store.ReportProgress(ctx, report); err != nil {
		return "", fmt.Errorf("reporting blocker: %w", err)
	}
	return "blocker reported", nil
}

// validTaskStatuses is the set of accepted task status values.
var validTaskStatuses = map[string]bool{
	"pending":     true,
	"in_progress": true,
	"completed":   true,
	"failed":      true,
	"blocked":     true,
	"cancelled":   true,
}

// UpdateTaskStatus updates the status of a task in the job tracker.
func UpdateTaskStatus(ctx context.Context, store db.Store, params UpdateTaskStatusParams) (string, error) {
	if !validTaskStatuses[params.Status] {
		return "", fmt.Errorf("invalid status %q: must be one of pending, in_progress, completed, failed, blocked, cancelled", params.Status)
	}
	if err := store.UpdateTaskStatus(ctx, params.TaskID, db.TaskStatus(params.Status), params.Summary); err != nil {
		return "", fmt.Errorf("updating task status: %w", err)
	}
	return "task status updated", nil
}

// RequestReview logs a review request artifact and reports progress.
func RequestReview(ctx context.Context, store db.Store, params RequestReviewParams) (string, error) {
	artifact := &db.Artifact{
		JobID:   params.JobID,
		TaskID:  params.TaskID,
		Type:    "review_request",
		Path:    params.ArtifactPath,
		Summary: params.Notes,
	}
	if err := store.LogArtifact(ctx, artifact); err != nil {
		return "", fmt.Errorf("logging review artifact: %w", err)
	}

	report := &db.ProgressReport{
		JobID:   params.JobID,
		TaskID:  params.TaskID,
		AgentID: params.AgentID,
		Status:  "review_requested",
		Message: "Review requested for " + params.ArtifactPath,
	}
	if err := store.ReportProgress(ctx, report); err != nil {
		return "", fmt.Errorf("reporting review progress: %w", err)
	}

	return "review requested", nil
}

// jobContextResult is the JSON structure returned by QueryJobContext.
type jobContextResult struct {
	Job       *db.Job              `json:"job"`
	Tasks     []*db.Task           `json:"tasks"`
	Progress  []*db.ProgressReport `json:"recent_progress"`
	Artifacts []*db.Artifact       `json:"artifacts"`
}

// QueryJobContext returns the current state of a job as a JSON string.
func QueryJobContext(ctx context.Context, store db.Store, params QueryJobContextParams) (string, error) {
	job, err := store.GetJob(ctx, params.JobID)
	if err != nil {
		return "", fmt.Errorf("getting job %s: %w", params.JobID, err)
	}

	tasks, err := store.ListTasksForJob(ctx, params.JobID)
	if err != nil {
		return "", fmt.Errorf("listing tasks for job %s: %w", params.JobID, err)
	}

	recentProgress, err := store.GetRecentProgress(ctx, params.JobID, 10)
	if err != nil {
		return "", fmt.Errorf("getting recent progress for job %s: %w", params.JobID, err)
	}

	artifacts, err := store.ListArtifactsForJob(ctx, params.JobID)
	if err != nil {
		return "", fmt.Errorf("listing artifacts for job %s: %w", params.JobID, err)
	}

	result := jobContextResult{
		Job:       job,
		Tasks:     tasks,
		Progress:  recentProgress,
		Artifacts: artifacts,
	}

	data, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshaling job context: %w", err)
	}

	return string(data), nil
}

// validArtifactTypes is the set of accepted type values for log_artifact.
var validArtifactTypes = map[string]bool{
	"code":          true,
	"report":        true,
	"investigation": true,
	"test_results":  true,
	"other":         true,
}

// LogArtifact records an artifact produced during a job.
func LogArtifact(ctx context.Context, store db.Store, params LogArtifactParams) (string, error) {
	if !validArtifactTypes[params.Type] {
		return "", fmt.Errorf("invalid artifact type %q: must be one of code, report, investigation, test_results, other", params.Type)
	}
	artifact := &db.Artifact{
		JobID:   params.JobID,
		TaskID:  params.TaskID,
		Type:    params.Type,
		Path:    params.Path,
		Summary: params.Summary,
	}
	if err := store.LogArtifact(ctx, artifact); err != nil {
		return "", fmt.Errorf("logging artifact: %w", err)
	}
	return "artifact logged", nil
}
