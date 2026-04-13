package progress

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
)

// openTestStore opens a fresh SQLite store in a temp directory.
func openTestStore(t *testing.T) db.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	t.Cleanup(func() { store.Close() }) //nolint:errcheck
	return store
}

// createTestJob creates a job in the store and returns its ID.
func createTestJob(t *testing.T, ctx context.Context, store db.Store, id string) {
	t.Helper()
	job := &db.Job{
		ID:     id,
		Title:  "Test Job",
		Type:   "test",
		Status: db.JobStatusActive,
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("creating test job: %v", err)
	}
}

// createTestTask creates a task in the store and returns its ID.
func createTestTask(t *testing.T, ctx context.Context, store db.Store, jobID, taskID string) {
	t.Helper()
	task := &db.Task{
		ID:     taskID,
		JobID:  jobID,
		Title:  "Test Task",
		Status: db.TaskStatusPending,
	}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("creating test task: %v", err)
	}
}

// --- mockStore for error-path testing ---

type mockStore struct {
	db.Store         // embed for unimplemented methods — panics if called unexpectedly
	reportProgressFn func(ctx context.Context, report *db.ProgressReport) error
	logArtifactFn    func(ctx context.Context, artifact *db.Artifact) error
	updateTaskFn     func(ctx context.Context, id string, status db.TaskStatus, summary string) error
	getJobFn         func(ctx context.Context, id string) (*db.Job, error)
	listTasksFn      func(ctx context.Context, jobID string) ([]*db.Task, error)
	getProgressFn    func(ctx context.Context, jobID string, limit int) ([]*db.ProgressReport, error)
	listArtifactsFn  func(ctx context.Context, jobID string) ([]*db.Artifact, error)
}

func (m *mockStore) ReportProgress(ctx context.Context, report *db.ProgressReport) error {
	if m.reportProgressFn != nil {
		return m.reportProgressFn(ctx, report)
	}
	return nil
}

func (m *mockStore) LogArtifact(ctx context.Context, artifact *db.Artifact) error {
	if m.logArtifactFn != nil {
		return m.logArtifactFn(ctx, artifact)
	}
	return nil
}

func (m *mockStore) UpdateTaskStatus(ctx context.Context, id string, status db.TaskStatus, summary string) error {
	if m.updateTaskFn != nil {
		return m.updateTaskFn(ctx, id, status, summary)
	}
	return nil
}

func (m *mockStore) CompleteTask(_ context.Context, _ string, _ db.TaskStatus, _, _ string) error {
	return nil
}

func (m *mockStore) GetJob(ctx context.Context, id string) (*db.Job, error) {
	if m.getJobFn != nil {
		return m.getJobFn(ctx, id)
	}
	return nil, nil
}

func (m *mockStore) ListTasksForJob(ctx context.Context, jobID string) ([]*db.Task, error) {
	if m.listTasksFn != nil {
		return m.listTasksFn(ctx, jobID)
	}
	return nil, nil
}

func (m *mockStore) GetRecentProgress(ctx context.Context, jobID string, limit int) ([]*db.ProgressReport, error) {
	if m.getProgressFn != nil {
		return m.getProgressFn(ctx, jobID, limit)
	}
	return nil, nil
}

func (m *mockStore) ListArtifactsForJob(ctx context.Context, jobID string) ([]*db.Artifact, error) {
	if m.listArtifactsFn != nil {
		return m.listArtifactsFn(ctx, jobID)
	}
	return nil, nil
}

func (m *mockStore) Close() error { return nil }

// --- ReportTaskProgress tests ---

func TestReportTaskProgress_Success(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	createTestJob(t, ctx, store, "job-1")

	params := ReportTaskProgressParams{
		JobID:   "job-1",
		TaskID:  "task-1",
		WorkerID: "agent-1",
		Status:  "in_progress",
		Message: "working on it",
	}

	result, err := ReportTaskProgress(ctx, store, params)
	if err != nil {
		t.Fatalf("ReportTaskProgress: %v", err)
	}
	if result != "progress reported" {
		t.Errorf("result = %q, want %q", result, "progress reported")
	}

	// Verify the report was persisted.
	reports, err := store.GetRecentProgress(ctx, "job-1", 10)
	if err != nil {
		t.Fatalf("GetRecentProgress: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	r := reports[0]
	if r.JobID != "job-1" {
		t.Errorf("JobID = %q, want %q", r.JobID, "job-1")
	}
	if r.TaskID != "task-1" {
		t.Errorf("TaskID = %q, want %q", r.TaskID, "task-1")
	}
	if r.WorkerID != "agent-1" {
		t.Errorf("WorkerID = %q, want %q", r.WorkerID, "agent-1")
	}
	if r.Status != "in_progress" {
		t.Errorf("Status = %q, want %q", r.Status, "in_progress")
	}
	if r.Message != "working on it" {
		t.Errorf("Message = %q, want %q", r.Message, "working on it")
	}
}

func TestReportTaskProgress_StoreError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeErr := errors.New("db exploded")
	store := &mockStore{
		reportProgressFn: func(_ context.Context, _ *db.ProgressReport) error {
			return storeErr
		},
	}

	_, err := ReportTaskProgress(ctx, store, ReportTaskProgressParams{
		JobID:   "job-1",
		Status:  "in_progress",
		Message: "hello",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, storeErr) {
		t.Errorf("expected wrapped storeErr, got: %v", err)
	}
}

func TestReportTaskProgress_EmptyOptionalFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	createTestJob(t, ctx, store, "job-empty")

	// TaskID and WorkerID are optional — should succeed with empty strings.
	params := ReportTaskProgressParams{
		JobID:   "job-empty",
		Status:  "completed",
		Message: "done",
	}

	result, err := ReportTaskProgress(ctx, store, params)
	if err != nil {
		t.Fatalf("ReportTaskProgress with empty optional fields: %v", err)
	}
	if result != "progress reported" {
		t.Errorf("result = %q, want %q", result, "progress reported")
	}
}

func TestReportTaskProgress_MissingJobID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &mockStore{}

	_, err := ReportTaskProgress(ctx, store, ReportTaskProgressParams{
		Status:  "in_progress",
		Message: "working",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "job_id is required") {
		t.Errorf("error should mention missing job_id, got: %v", err)
	}
}

// --- ReportBlocker tests ---

func TestReportBlocker_Success(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	createTestJob(t, ctx, store, "job-blocker")

	params := ReportBlockerParams{
		JobID:       "job-blocker",
		TaskID:      "task-1",
		WorkerID:     "agent-1",
		Description: "cannot access the database",
		Severity:    "high",
	}

	result, err := ReportBlocker(ctx, store, params)
	if err != nil {
		t.Fatalf("ReportBlocker: %v", err)
	}
	if result != "blocker reported" {
		t.Errorf("result = %q, want %q", result, "blocker reported")
	}

	// Verify status is "blocked" and message format is "[severity] description".
	reports, err := store.GetRecentProgress(ctx, "job-blocker", 10)
	if err != nil {
		t.Fatalf("GetRecentProgress: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	r := reports[0]
	if r.Status != "blocked" {
		t.Errorf("Status = %q, want %q", r.Status, "blocked")
	}
	wantMsg := "[high] cannot access the database"
	if r.Message != wantMsg {
		t.Errorf("Message = %q, want %q", r.Message, wantMsg)
	}
}

func TestReportBlocker_AllSeverities(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		severity string
	}{
		{"low"},
		{"medium"},
		{"high"},
	}

	for _, tt := range tests {
		t.Run(tt.severity, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t)
			createTestJob(t, ctx, store, "job-"+tt.severity)

			params := ReportBlockerParams{
				JobID:       "job-" + tt.severity,
				Description: "some blocker",
				Severity:    tt.severity,
			}

			result, err := ReportBlocker(ctx, store, params)
			if err != nil {
				t.Fatalf("ReportBlocker(%s): %v", tt.severity, err)
			}
			if result != "blocker reported" {
				t.Errorf("result = %q, want %q", result, "blocker reported")
			}

			reports, err := store.GetRecentProgress(ctx, "job-"+tt.severity, 10)
			if err != nil {
				t.Fatalf("GetRecentProgress: %v", err)
			}
			if len(reports) != 1 {
				t.Fatalf("expected 1 report, got %d", len(reports))
			}
			wantMsg := "[" + tt.severity + "] some blocker"
			if reports[0].Message != wantMsg {
				t.Errorf("Message = %q, want %q", reports[0].Message, wantMsg)
			}
		})
	}
}

func TestReportBlocker_StoreError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeErr := errors.New("write failed")
	store := &mockStore{
		reportProgressFn: func(_ context.Context, _ *db.ProgressReport) error {
			return storeErr
		},
	}

	_, err := ReportBlocker(ctx, store, ReportBlockerParams{
		JobID:       "job-1",
		Description: "blocked",
		Severity:    "medium",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, storeErr) {
		t.Errorf("expected wrapped storeErr, got: %v", err)
	}
}

func TestReportBlocker_MissingJobID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &mockStore{}

	_, err := ReportBlocker(ctx, store, ReportBlockerParams{
		Description: "blocked",
		Severity:    "low",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "job_id is required") {
		t.Errorf("error should mention missing job_id, got: %v", err)
	}
}

// --- UpdateTaskStatus tests ---

func TestUpdateTaskStatus_Success(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	createTestJob(t, ctx, store, "job-uts")
	createTestTask(t, ctx, store, "job-uts", "task-uts")

	params := UpdateTaskStatusParams{
		JobID:   "job-uts",
		TaskID:  "task-uts",
		Status:  "completed",
		Summary: "all done",
	}

	result, err := UpdateTaskStatus(ctx, store, params)
	if err != nil {
		t.Fatalf("UpdateTaskStatus: %v", err)
	}
	if result != "task status updated" {
		t.Errorf("result = %q, want %q", result, "task status updated")
	}

	// Verify via GetTask.
	task, err := store.GetTask(ctx, "task-uts")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != db.TaskStatusCompleted {
		t.Errorf("Status = %q, want %q", task.Status, db.TaskStatusCompleted)
	}
	if task.Summary != "all done" {
		t.Errorf("Summary = %q, want %q", task.Summary, "all done")
	}
}

func TestUpdateTaskStatus_InvalidStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &mockStore{}

	_, err := UpdateTaskStatus(ctx, store, UpdateTaskStatusParams{
		JobID:  "job-1",
		TaskID: "task-1",
		Status: "not_a_real_status",
	})
	if err == nil {
		t.Fatal("expected error for invalid status, got nil")
	}
	if !strings.Contains(err.Error(), "invalid status") {
		t.Errorf("error should mention 'invalid status', got: %v", err)
	}
}

func TestUpdateTaskStatus_ValidStatuses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	validStatuses := []string{
		"pending",
		"in_progress",
		"completed",
		"failed",
		"blocked",
		"cancelled",
	}

	for _, status := range validStatuses {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t)
			jobID := "job-vs-" + status
			taskID := "task-vs-" + status

			createTestJob(t, ctx, store, jobID)
			createTestTask(t, ctx, store, jobID, taskID)

			params := UpdateTaskStatusParams{
				JobID:  jobID,
				TaskID: taskID,
				Status: status,
			}

			result, err := UpdateTaskStatus(ctx, store, params)
			if err != nil {
				t.Fatalf("UpdateTaskStatus(%q): %v", status, err)
			}
			if result != "task status updated" {
				t.Errorf("result = %q, want %q", result, "task status updated")
			}
		})
	}
}

func TestUpdateTaskStatus_StoreError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeErr := errors.New("update failed")
	store := &mockStore{
		updateTaskFn: func(_ context.Context, _ string, _ db.TaskStatus, _ string) error {
			return storeErr
		},
	}

	_, err := UpdateTaskStatus(ctx, store, UpdateTaskStatusParams{
		JobID:  "job-1",
		TaskID: "task-1",
		Status: "completed",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, storeErr) {
		t.Errorf("expected wrapped storeErr, got: %v", err)
	}
}

func TestUpdateTaskStatus_EmptyStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &mockStore{}

	_, err := UpdateTaskStatus(ctx, store, UpdateTaskStatusParams{
		JobID:  "job-1",
		TaskID: "task-1",
		Status: "",
	})
	if err == nil {
		t.Fatal("expected error for empty status, got nil")
	}
	if !strings.Contains(err.Error(), "invalid status") {
		t.Errorf("error should mention 'invalid status', got: %v", err)
	}
}

// --- RequestReview tests ---

func TestRequestReview_Success(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	createTestJob(t, ctx, store, "job-rr")

	params := RequestReviewParams{
		JobID:        "job-rr",
		TaskID:       "task-rr",
		WorkerID:      "agent-rr",
		ArtifactPath: "/path/to/artifact.go",
		Notes:        "please review this carefully",
	}

	result, err := RequestReview(ctx, store, params)
	if err != nil {
		t.Fatalf("RequestReview: %v", err)
	}
	if result != "review requested" {
		t.Errorf("result = %q, want %q", result, "review requested")
	}

	// Verify artifact was logged.
	artifacts, err := store.ListArtifactsForJob(ctx, "job-rr")
	if err != nil {
		t.Fatalf("ListArtifactsForJob: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
	art := artifacts[0]
	if art.Type != "review_request" {
		t.Errorf("artifact Type = %q, want %q", art.Type, "review_request")
	}
	if art.Path != "/path/to/artifact.go" {
		t.Errorf("artifact Path = %q, want %q", art.Path, "/path/to/artifact.go")
	}
	if art.Summary != "please review this carefully" {
		t.Errorf("artifact Summary = %q, want %q", art.Summary, "please review this carefully")
	}

	// Verify progress report was inserted with status "review_requested".
	reports, err := store.GetRecentProgress(ctx, "job-rr", 10)
	if err != nil {
		t.Fatalf("GetRecentProgress: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 progress report, got %d", len(reports))
	}
	r := reports[0]
	if r.Status != "review_requested" {
		t.Errorf("Status = %q, want %q", r.Status, "review_requested")
	}
	if !strings.Contains(r.Message, "/path/to/artifact.go") {
		t.Errorf("Message %q should contain artifact path", r.Message)
	}
}

func TestRequestReview_ArtifactLogError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeErr := errors.New("artifact insert failed")
	store := &mockStore{
		logArtifactFn: func(_ context.Context, _ *db.Artifact) error {
			return storeErr
		},
	}

	_, err := RequestReview(ctx, store, RequestReviewParams{
		JobID:        "job-1",
		ArtifactPath: "/some/path",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, storeErr) {
		t.Errorf("expected wrapped storeErr, got: %v", err)
	}
}

func TestRequestReview_ProgressReportError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeErr := errors.New("progress insert failed")
	store := &mockStore{
		logArtifactFn: func(_ context.Context, _ *db.Artifact) error {
			return nil // artifact succeeds
		},
		reportProgressFn: func(_ context.Context, _ *db.ProgressReport) error {
			return storeErr // progress fails
		},
	}

	_, err := RequestReview(ctx, store, RequestReviewParams{
		JobID:        "job-1",
		ArtifactPath: "/some/path",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, storeErr) {
		t.Errorf("expected wrapped storeErr, got: %v", err)
	}
}

func TestRequestReview_MissingJobID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &mockStore{}

	_, err := RequestReview(ctx, store, RequestReviewParams{
		ArtifactPath: "/some/path",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "job_id is required") {
		t.Errorf("error should mention missing job_id, got: %v", err)
	}
}

// --- QueryJobContext tests ---

func TestQueryJobContext_Success(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	createTestJob(t, ctx, store, "job-qjc")
	createTestTask(t, ctx, store, "job-qjc", "task-qjc-1")
	createTestTask(t, ctx, store, "job-qjc", "task-qjc-2")

	// Add a progress report.
	if err := store.ReportProgress(ctx, &db.ProgressReport{
		JobID:   "job-qjc",
		TaskID:  "task-qjc-1",
		Status:  "in_progress",
		Message: "working",
	}); err != nil {
		t.Fatalf("ReportProgress: %v", err)
	}

	// Add an artifact.
	if err := store.LogArtifact(ctx, &db.Artifact{
		JobID:   "job-qjc",
		TaskID:  "task-qjc-1",
		Type:    "code",
		Path:    "/src/main.go",
		Summary: "main file",
	}); err != nil {
		t.Fatalf("LogArtifact: %v", err)
	}

	result, err := QueryJobContext(ctx, store, QueryJobContextParams{JobID: "job-qjc"})
	if err != nil {
		t.Fatalf("QueryJobContext: %v", err)
	}

	// Result must be valid JSON.
	var parsed jobContextResult
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v\nresult: %s", err, result)
	}

	if parsed.Job == nil {
		t.Fatal("job is nil in result")
	}
	if parsed.Job.ID != "job-qjc" {
		t.Errorf("job ID = %q, want %q", parsed.Job.ID, "job-qjc")
	}
	if len(parsed.Tasks) != 2 {
		t.Errorf("tasks count = %d, want 2", len(parsed.Tasks))
	}
	if len(parsed.Progress) != 1 {
		t.Errorf("progress count = %d, want 1", len(parsed.Progress))
	}
	if len(parsed.Artifacts) != 1 {
		t.Errorf("artifacts count = %d, want 1", len(parsed.Artifacts))
	}
}

func TestQueryJobContext_JobNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	_, err := QueryJobContext(ctx, store, QueryJobContextParams{JobID: "nonexistent-job"})
	if err == nil {
		t.Fatal("expected error for nonexistent job, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent-job") {
		t.Errorf("error should mention job ID, got: %v", err)
	}
}

func TestQueryJobContext_EmptyTasks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	createTestJob(t, ctx, store, "job-empty-tasks")

	result, err := QueryJobContext(ctx, store, QueryJobContextParams{JobID: "job-empty-tasks"})
	if err != nil {
		t.Fatalf("QueryJobContext: %v", err)
	}

	var parsed jobContextResult
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if parsed.Job == nil {
		t.Fatal("job is nil in result")
	}
	// Tasks, Progress, and Artifacts may be nil (JSON null) or empty slice — both are acceptable.
	// The important thing is no error and valid JSON.
}

func TestQueryJobContext_ListTasksError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeErr := errors.New("tasks query failed")
	store := &mockStore{
		getJobFn: func(_ context.Context, _ string) (*db.Job, error) {
			return &db.Job{ID: "job-1"}, nil
		},
		listTasksFn: func(_ context.Context, _ string) ([]*db.Task, error) {
			return nil, storeErr
		},
	}

	_, err := QueryJobContext(ctx, store, QueryJobContextParams{JobID: "job-1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, storeErr) {
		t.Errorf("expected wrapped storeErr, got: %v", err)
	}
}

func TestQueryJobContext_GetProgressError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeErr := errors.New("progress query failed")
	store := &mockStore{
		getJobFn: func(_ context.Context, _ string) (*db.Job, error) {
			return &db.Job{ID: "job-1"}, nil
		},
		listTasksFn: func(_ context.Context, _ string) ([]*db.Task, error) {
			return nil, nil
		},
		getProgressFn: func(_ context.Context, _ string, _ int) ([]*db.ProgressReport, error) {
			return nil, storeErr
		},
	}

	_, err := QueryJobContext(ctx, store, QueryJobContextParams{JobID: "job-1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, storeErr) {
		t.Errorf("expected wrapped storeErr, got: %v", err)
	}
}

func TestQueryJobContext_ListArtifactsError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeErr := errors.New("artifacts query failed")
	store := &mockStore{
		getJobFn: func(_ context.Context, _ string) (*db.Job, error) {
			return &db.Job{ID: "job-1"}, nil
		},
		listTasksFn: func(_ context.Context, _ string) ([]*db.Task, error) {
			return nil, nil
		},
		getProgressFn: func(_ context.Context, _ string, _ int) ([]*db.ProgressReport, error) {
			return nil, nil
		},
		listArtifactsFn: func(_ context.Context, _ string) ([]*db.Artifact, error) {
			return nil, storeErr
		},
	}

	_, err := QueryJobContext(ctx, store, QueryJobContextParams{JobID: "job-1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, storeErr) {
		t.Errorf("expected wrapped storeErr, got: %v", err)
	}
}

func TestQueryJobContext_RecentProgressLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	createTestJob(t, ctx, store, "job-limit")

	// Insert 15 progress reports — QueryJobContext should only return 10.
	for i := range 15 {
		if err := store.ReportProgress(ctx, &db.ProgressReport{
			JobID:   "job-limit",
			Status:  "in_progress",
			Message: "step",
			WorkerID: string(rune('a' + i)),
		}); err != nil {
			t.Fatalf("ReportProgress(%d): %v", i, err)
		}
	}

	result, err := QueryJobContext(ctx, store, QueryJobContextParams{JobID: "job-limit"})
	if err != nil {
		t.Fatalf("QueryJobContext: %v", err)
	}

	var parsed jobContextResult
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if len(parsed.Progress) > 10 {
		t.Errorf("expected at most 10 progress reports, got %d", len(parsed.Progress))
	}
}

// --- LogArtifact tests ---

func TestLogArtifact_Success(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	createTestJob(t, ctx, store, "job-la")

	params := LogArtifactParams{
		JobID:   "job-la",
		TaskID:  "task-la",
		Type:    "code",
		Path:    "/src/handler.go",
		Summary: "HTTP handler implementation",
	}

	result, err := LogArtifact(ctx, store, params)
	if err != nil {
		t.Fatalf("LogArtifact: %v", err)
	}
	if result != "artifact logged" {
		t.Errorf("result = %q, want %q", result, "artifact logged")
	}

	// Verify artifact appears in the store.
	artifacts, err := store.ListArtifactsForJob(ctx, "job-la")
	if err != nil {
		t.Fatalf("ListArtifactsForJob: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
	art := artifacts[0]
	if art.JobID != "job-la" {
		t.Errorf("JobID = %q, want %q", art.JobID, "job-la")
	}
	if art.TaskID != "task-la" {
		t.Errorf("TaskID = %q, want %q", art.TaskID, "task-la")
	}
	if art.Type != "code" {
		t.Errorf("Type = %q, want %q", art.Type, "code")
	}
	if art.Path != "/src/handler.go" {
		t.Errorf("Path = %q, want %q", art.Path, "/src/handler.go")
	}
	if art.Summary != "HTTP handler implementation" {
		t.Errorf("Summary = %q, want %q", art.Summary, "HTTP handler implementation")
	}
}

func TestLogArtifact_StoreError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeErr := errors.New("artifact insert failed")
	store := &mockStore{
		logArtifactFn: func(_ context.Context, _ *db.Artifact) error {
			return storeErr
		},
	}

	_, err := LogArtifact(ctx, store, LogArtifactParams{
		JobID: "job-1",
		Type:  "code",
		Path:  "/some/path",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, storeErr) {
		t.Errorf("expected wrapped storeErr, got: %v", err)
	}
}

func TestLogArtifact_MultipleArtifacts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	createTestJob(t, ctx, store, "job-multi-art")

	types := []string{"code", "report", "test_results"}
	for i, typ := range types {
		params := LogArtifactParams{
			JobID:   "job-multi-art",
			Type:    typ,
			Path:    "/path/" + typ,
			Summary: "artifact " + string(rune('0'+i)),
		}
		if _, err := LogArtifact(ctx, store, params); err != nil {
			t.Fatalf("LogArtifact(%s): %v", typ, err)
		}
	}

	artifacts, err := store.ListArtifactsForJob(ctx, "job-multi-art")
	if err != nil {
		t.Fatalf("ListArtifactsForJob: %v", err)
	}
	if len(artifacts) != 3 {
		t.Errorf("expected 3 artifacts, got %d", len(artifacts))
	}
}

func TestLogArtifact_MissingJobID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &mockStore{}

	_, err := LogArtifact(ctx, store, LogArtifactParams{
		Type: "code",
		Path: "/some/path",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "job_id is required") {
		t.Errorf("error should mention missing job_id, got: %v", err)
	}
}
