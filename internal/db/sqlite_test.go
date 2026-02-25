package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// openTestStore creates a new SQLiteStore in a temporary directory.
func openTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	t.Cleanup(func() { store.Close() }) //nolint:errcheck
	return store
}

// --- Open / lifecycle tests ---

func TestOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close() //nolint:errcheck

	// Verify all expected tables exist by querying sqlite_master.
	tables := []string{
		"schema_version",
		"jobs",
		"tasks",
		"task_dependencies",
		"progress_reports",
		"agents",
		"teams",
		"team_members",
		"agent_sessions",
		"artifacts",
	}
	for _, table := range tables {
		var name string
		err := store.db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestOpen_CreatesParentDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c", "test.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open with nested path: %v", err)
	}
	store.Close() //nolint:errcheck
}

func TestOpen_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	store1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	store1.Close() //nolint:errcheck

	store2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	store2.Close() //nolint:errcheck
}

func TestWALMode(t *testing.T) {
	store := openTestStore(t)

	var mode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("querying journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	store := openTestStore(t)

	var fk int
	if err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("querying foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

func TestClose_ThenReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	ctx := context.Background()

	// Open, create a job, close.
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	job := &Job{ID: "j-1", Title: "Test", Type: "test", Status: JobStatusPending}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	store.Close() //nolint:errcheck

	// Reopen and verify the job persisted.
	store2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close() //nolint:errcheck

	got, err := store2.GetJob(ctx, "j-1")
	if err != nil {
		t.Fatalf("GetJob after reopen: %v", err)
	}
	if got.Title != "Test" {
		t.Errorf("Title = %q, want %q", got.Title, "Test")
	}
}

// --- Jobs CRUD ---

func TestJobs_CRUD(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	meta := json.RawMessage(`{"priority":"high"}`)
	job := &Job{
		ID:       "job-1",
		Title:    "Fix the bug",
		Type:     "bug_fix",
		Status:   JobStatusPending,
		Metadata: meta,
	}

	// Create
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if job.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set after create")
	}

	// Get
	got, err := store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.ID != "job-1" {
		t.Errorf("ID = %q, want %q", got.ID, "job-1")
	}
	if got.Title != "Fix the bug" {
		t.Errorf("Title = %q, want %q", got.Title, "Fix the bug")
	}
	if got.Type != "bug_fix" {
		t.Errorf("Type = %q, want %q", got.Type, "bug_fix")
	}
	if got.Status != JobStatusPending {
		t.Errorf("Status = %q, want %q", got.Status, JobStatusPending)
	}
	if string(got.Metadata) != `{"priority":"high"}` {
		t.Errorf("Metadata = %q, want %q", string(got.Metadata), `{"priority":"high"}`)
	}

	// List (no filter)
	jobs, err := store.ListJobs(ctx, JobFilter{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("ListJobs returned %d jobs, want 1", len(jobs))
	}

	// Create a second job
	job2 := &Job{ID: "job-2", Title: "New feature", Type: "new_feature", Status: JobStatusActive}
	if err := store.CreateJob(ctx, job2); err != nil {
		t.Fatalf("CreateJob (2): %v", err)
	}

	// List with status filter
	activeStatus := JobStatusActive
	jobs, err = store.ListJobs(ctx, JobFilter{Status: &activeStatus})
	if err != nil {
		t.Fatalf("ListJobs with status filter: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("ListJobs(active) returned %d jobs, want 1", len(jobs))
	}
	if jobs[0].ID != "job-2" {
		t.Errorf("filtered job ID = %q, want %q", jobs[0].ID, "job-2")
	}

	// List with type filter
	bugType := "bug_fix"
	jobs, err = store.ListJobs(ctx, JobFilter{Type: &bugType})
	if err != nil {
		t.Fatalf("ListJobs with type filter: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("ListJobs(bug_fix) returned %d jobs, want 1", len(jobs))
	}
	if jobs[0].ID != "job-1" {
		t.Errorf("filtered job ID = %q, want %q", jobs[0].ID, "job-1")
	}

	// List with limit
	jobs, err = store.ListJobs(ctx, JobFilter{Limit: 1})
	if err != nil {
		t.Fatalf("ListJobs with limit: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("ListJobs(limit=1) returned %d jobs, want 1", len(jobs))
	}

	// Update status
	if err := store.UpdateJobStatus(ctx, "job-1", JobStatusCompleted); err != nil {
		t.Fatalf("UpdateJobStatus: %v", err)
	}
	got, err = store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob after update: %v", err)
	}
	if got.Status != JobStatusCompleted {
		t.Errorf("Status after update = %q, want %q", got.Status, JobStatusCompleted)
	}
	// updated_at should be later than created_at (or at least equal)
	if got.UpdatedAt.Before(got.CreatedAt) {
		t.Errorf("UpdatedAt (%v) should not be before CreatedAt (%v)", got.UpdatedAt, got.CreatedAt)
	}
}

func TestGetJob_NotFound(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	_, err := store.GetJob(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent job, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestUpdateJobStatus_NotFound(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	err := store.UpdateJobStatus(ctx, "nonexistent", JobStatusActive)
	if err == nil {
		t.Fatal("expected error for nonexistent job, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestJobs_NilMetadata(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	job := &Job{ID: "j-nil", Title: "No meta", Type: "test", Status: JobStatusPending}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	got, err := store.GetJob(ctx, "j-nil")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Metadata != nil {
		t.Errorf("Metadata = %q, want nil", string(got.Metadata))
	}
}

// --- Tasks CRUD ---

func TestTasks_CRUD(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Create parent job first.
	job := &Job{ID: "job-t", Title: "Task test", Type: "test", Status: JobStatusActive}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	task := &Task{
		ID:        "task-1",
		JobID:     "job-t",
		Title:     "Write tests",
		Status:    TaskStatusPending,
		AgentID:   "agent-1",
		SortOrder: 1,
	}

	// Create
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set after create")
	}

	// Get
	got, err := store.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ID != "task-1" {
		t.Errorf("ID = %q, want %q", got.ID, "task-1")
	}
	if got.JobID != "job-t" {
		t.Errorf("JobID = %q, want %q", got.JobID, "job-t")
	}
	if got.Title != "Write tests" {
		t.Errorf("Title = %q, want %q", got.Title, "Write tests")
	}
	if got.Status != TaskStatusPending {
		t.Errorf("Status = %q, want %q", got.Status, TaskStatusPending)
	}
	if got.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want %q", got.AgentID, "agent-1")
	}
	if got.SortOrder != 1 {
		t.Errorf("SortOrder = %d, want 1", got.SortOrder)
	}

	// Create a second task
	task2 := &Task{
		ID:        "task-2",
		JobID:     "job-t",
		Title:     "Review code",
		Status:    TaskStatusPending,
		SortOrder: 2,
	}
	if err := store.CreateTask(ctx, task2); err != nil {
		t.Fatalf("CreateTask (2): %v", err)
	}

	// List for job
	tasks, err := store.ListTasksForJob(ctx, "job-t")
	if err != nil {
		t.Fatalf("ListTasksForJob: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("ListTasksForJob returned %d tasks, want 2", len(tasks))
	}
	// Should be ordered by sort_order
	if tasks[0].ID != "task-1" {
		t.Errorf("first task ID = %q, want %q", tasks[0].ID, "task-1")
	}
	if tasks[1].ID != "task-2" {
		t.Errorf("second task ID = %q, want %q", tasks[1].ID, "task-2")
	}

	// Update status
	if err := store.UpdateTaskStatus(ctx, "task-1", TaskStatusCompleted, "All tests pass"); err != nil {
		t.Fatalf("UpdateTaskStatus: %v", err)
	}
	got, err = store.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetTask after update: %v", err)
	}
	if got.Status != TaskStatusCompleted {
		t.Errorf("Status after update = %q, want %q", got.Status, TaskStatusCompleted)
	}
	if got.Summary != "All tests pass" {
		t.Errorf("Summary = %q, want %q", got.Summary, "All tests pass")
	}
}

func TestGetTask_NotFound(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	_, err := store.GetTask(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent task, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestUpdateTaskStatus_NotFound(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	err := store.UpdateTaskStatus(ctx, "nonexistent", TaskStatusCompleted, "done")
	if err == nil {
		t.Fatal("expected error for nonexistent task, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

// --- Task Dependencies ---

func TestTaskDependencies(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Setup: job with 3 tasks.
	job := &Job{ID: "job-dep", Title: "Dep test", Type: "test", Status: JobStatusActive}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	taskA := &Task{ID: "dep-a", JobID: "job-dep", Title: "Task A", Status: TaskStatusPending, SortOrder: 1}
	taskB := &Task{ID: "dep-b", JobID: "job-dep", Title: "Task B", Status: TaskStatusPending, SortOrder: 2}
	taskC := &Task{ID: "dep-c", JobID: "job-dep", Title: "Task C", Status: TaskStatusPending, SortOrder: 3}

	for _, task := range []*Task{taskA, taskB, taskC} {
		if err := store.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask(%s): %v", task.ID, err)
		}
	}

	// B depends on A, C depends on both A and B.
	if err := store.AddTaskDependency(ctx, "dep-b", "dep-a"); err != nil {
		t.Fatalf("AddTaskDependency(B->A): %v", err)
	}
	if err := store.AddTaskDependency(ctx, "dep-c", "dep-a"); err != nil {
		t.Fatalf("AddTaskDependency(C->A): %v", err)
	}
	if err := store.AddTaskDependency(ctx, "dep-c", "dep-b"); err != nil {
		t.Fatalf("AddTaskDependency(C->B): %v", err)
	}

	// Initially, only A should be ready (no deps).
	ready, err := store.GetReadyTasks(ctx, "job-dep")
	if err != nil {
		t.Fatalf("GetReadyTasks: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("GetReadyTasks returned %d tasks, want 1", len(ready))
	}
	if ready[0].ID != "dep-a" {
		t.Errorf("ready task ID = %q, want %q", ready[0].ID, "dep-a")
	}

	// Complete A → B should become ready, C still blocked (B not done).
	if err := store.UpdateTaskStatus(ctx, "dep-a", TaskStatusCompleted, "done"); err != nil {
		t.Fatalf("UpdateTaskStatus(A): %v", err)
	}
	ready, err = store.GetReadyTasks(ctx, "job-dep")
	if err != nil {
		t.Fatalf("GetReadyTasks after A complete: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("GetReadyTasks returned %d tasks, want 1", len(ready))
	}
	if ready[0].ID != "dep-b" {
		t.Errorf("ready task ID = %q, want %q", ready[0].ID, "dep-b")
	}

	// Complete B → C should become ready.
	if err := store.UpdateTaskStatus(ctx, "dep-b", TaskStatusCompleted, "done"); err != nil {
		t.Fatalf("UpdateTaskStatus(B): %v", err)
	}
	ready, err = store.GetReadyTasks(ctx, "job-dep")
	if err != nil {
		t.Fatalf("GetReadyTasks after B complete: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("GetReadyTasks returned %d tasks, want 1", len(ready))
	}
	if ready[0].ID != "dep-c" {
		t.Errorf("ready task ID = %q, want %q", ready[0].ID, "dep-c")
	}
}

func TestTaskDependencies_NoDeps_AllReady(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	job := &Job{ID: "job-nodep", Title: "No deps", Type: "test", Status: JobStatusActive}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	for i, id := range []string{"nd-1", "nd-2", "nd-3"} {
		task := &Task{ID: id, JobID: "job-nodep", Title: "Task", Status: TaskStatusPending, SortOrder: i}
		if err := store.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask(%s): %v", id, err)
		}
	}

	ready, err := store.GetReadyTasks(ctx, "job-nodep")
	if err != nil {
		t.Fatalf("GetReadyTasks: %v", err)
	}
	if len(ready) != 3 {
		t.Errorf("GetReadyTasks returned %d tasks, want 3", len(ready))
	}
}

func TestTaskDependencies_InProgressNotReady(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	job := &Job{ID: "job-ip", Title: "In progress", Type: "test", Status: JobStatusActive}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// Task that's already in_progress should NOT appear in ready tasks.
	task := &Task{ID: "ip-1", JobID: "job-ip", Title: "Working", Status: TaskStatusInProgress}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	ready, err := store.GetReadyTasks(ctx, "job-ip")
	if err != nil {
		t.Fatalf("GetReadyTasks: %v", err)
	}
	if len(ready) != 0 {
		t.Errorf("GetReadyTasks returned %d tasks, want 0 (in_progress should not be ready)", len(ready))
	}
}

func TestTaskDependencies_CompletedNotReady(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	job := &Job{ID: "job-comp", Title: "Completed", Type: "test", Status: JobStatusActive}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// Already completed task should NOT appear in ready tasks.
	task := &Task{ID: "comp-1", JobID: "job-comp", Title: "Done", Status: TaskStatusCompleted}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	ready, err := store.GetReadyTasks(ctx, "job-comp")
	if err != nil {
		t.Fatalf("GetReadyTasks: %v", err)
	}
	if len(ready) != 0 {
		t.Errorf("GetReadyTasks returned %d tasks, want 0 (completed should not be ready)", len(ready))
	}
}

// --- Progress Reports ---

func TestProgressReports(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	job := &Job{ID: "job-pr", Title: "Progress test", Type: "test", Status: JobStatusActive}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// Report multiple progress entries with explicit timestamps to ensure ordering.
	baseTime := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	messages := []string{"Starting", "In progress", "Almost done", "Complete"}
	for i, msg := range messages {
		report := &ProgressReport{
			JobID:     "job-pr",
			TaskID:    "task-1",
			AgentID:   "agent-1",
			Status:    "in_progress",
			Message:   msg,
			CreatedAt: baseTime.Add(time.Duration(i) * time.Minute),
		}
		if err := store.ReportProgress(ctx, report); err != nil {
			t.Fatalf("ReportProgress(%d): %v", i, err)
		}
		if report.ID == 0 {
			t.Errorf("report %d: ID should be set after insert", i)
		}
	}

	// Get recent with limit.
	reports, err := store.GetRecentProgress(ctx, "job-pr", 2)
	if err != nil {
		t.Fatalf("GetRecentProgress: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("GetRecentProgress returned %d reports, want 2", len(reports))
	}
	// Should be ordered by created_at DESC, so most recent first.
	if reports[0].Message != "Complete" {
		t.Errorf("first report message = %q, want %q", reports[0].Message, "Complete")
	}
	if reports[1].Message != "Almost done" {
		t.Errorf("second report message = %q, want %q", reports[1].Message, "Almost done")
	}

	// Get all (default limit).
	all, err := store.GetRecentProgress(ctx, "job-pr", 0)
	if err != nil {
		t.Fatalf("GetRecentProgress(0): %v", err)
	}
	if len(all) != 4 {
		t.Errorf("GetRecentProgress(0) returned %d reports, want 4", len(all))
	}
}

// --- Agents CRUD ---

func TestAgents_CRUD(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	temp := 0.7
	tools := json.RawMessage(`["read","write"]`)
	agent := &Agent{
		ID:           "agent-1",
		Name:         "Code Writer",
		Description:  "Writes code",
		Mode:         "worker",
		Model:        "claude-opus-4-20250514",
		Provider:     "anthropic",
		Temperature:  &temp,
		SystemPrompt: "You are a code writer.",
		Tools:        tools,
		Source:       "file",
	}

	// Upsert (insert)
	if err := store.UpsertAgent(ctx, agent); err != nil {
		t.Fatalf("UpsertAgent (insert): %v", err)
	}

	// Get
	got, err := store.GetAgent(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.Name != "Code Writer" {
		t.Errorf("Name = %q, want %q", got.Name, "Code Writer")
	}
	if got.Temperature == nil || *got.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", got.Temperature)
	}
	if string(got.Tools) != `["read","write"]` {
		t.Errorf("Tools = %q, want %q", string(got.Tools), `["read","write"]`)
	}
	if got.Source != "file" {
		t.Errorf("Source = %q, want %q", got.Source, "file")
	}

	// Upsert (update)
	agent.Name = "Code Writer v2"
	agent.UpdatedAt = time.Time{} // reset so it gets set by UpsertAgent
	if err := store.UpsertAgent(ctx, agent); err != nil {
		t.Fatalf("UpsertAgent (update): %v", err)
	}
	got, err = store.GetAgent(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgent after upsert: %v", err)
	}
	if got.Name != "Code Writer v2" {
		t.Errorf("Name after upsert = %q, want %q", got.Name, "Code Writer v2")
	}

	// List
	agent2 := &Agent{ID: "agent-2", Name: "Reviewer", Mode: "coordinator", Source: "database"}
	if err := store.UpsertAgent(ctx, agent2); err != nil {
		t.Fatalf("UpsertAgent (2): %v", err)
	}
	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("ListAgents returned %d agents, want 2", len(agents))
	}
}

func TestGetAgent_NotFound(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	_, err := store.GetAgent(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent agent, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestAgents_NilTemperature(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	agent := &Agent{ID: "agent-nil-temp", Name: "No Temp", Source: "test"}
	if err := store.UpsertAgent(ctx, agent); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	got, err := store.GetAgent(ctx, "agent-nil-temp")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.Temperature != nil {
		t.Errorf("Temperature = %v, want nil", got.Temperature)
	}
}

// --- Teams CRUD ---

func TestTeams_CRUD(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Create agents first for team members.
	agent1 := &Agent{ID: "tm-agent-1", Name: "Coordinator", Mode: "coordinator", Source: "test"}
	agent2 := &Agent{ID: "tm-agent-2", Name: "Worker", Mode: "worker", Source: "test"}
	for _, a := range []*Agent{agent1, agent2} {
		if err := store.UpsertAgent(ctx, a); err != nil {
			t.Fatalf("UpsertAgent(%s): %v", a.ID, err)
		}
	}

	meta := json.RawMessage(`{"domain":"backend"}`)
	team := &Team{
		ID:          "team-1",
		Name:        "Backend Team",
		Description: "Handles backend work",
		Coordinator: "tm-agent-1",
		Metadata:    meta,
	}

	// Create
	if err := store.CreateTeam(ctx, team); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	// Get
	got, err := store.GetTeam(ctx, "team-1")
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if got.Name != "Backend Team" {
		t.Errorf("Name = %q, want %q", got.Name, "Backend Team")
	}
	if got.Coordinator != "tm-agent-1" {
		t.Errorf("Coordinator = %q, want %q", got.Coordinator, "tm-agent-1")
	}
	if string(got.Metadata) != `{"domain":"backend"}` {
		t.Errorf("Metadata = %q, want %q", string(got.Metadata), `{"domain":"backend"}`)
	}

	// List
	team2 := &Team{ID: "team-2", Name: "Frontend Team", Description: "Handles frontend"}
	if err := store.CreateTeam(ctx, team2); err != nil {
		t.Fatalf("CreateTeam (2): %v", err)
	}
	teams, err := store.ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 2 {
		t.Fatalf("ListTeams returned %d teams, want 2", len(teams))
	}

	// Add members
	member := &TeamMember{TeamID: "team-1", AgentID: "tm-agent-1", Role: "coordinator"}
	if err := store.AddTeamMember(ctx, member); err != nil {
		t.Fatalf("AddTeamMember: %v", err)
	}
	member2 := &TeamMember{TeamID: "team-1", AgentID: "tm-agent-2", Role: "worker"}
	if err := store.AddTeamMember(ctx, member2); err != nil {
		t.Fatalf("AddTeamMember (2): %v", err)
	}

	// Verify members exist by querying directly.
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM team_members WHERE team_id = ?", "team-1").Scan(&count); err != nil {
		t.Fatalf("counting team members: %v", err)
	}
	if count != 2 {
		t.Errorf("team member count = %d, want 2", count)
	}
}

func TestGetTeam_NotFound(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	_, err := store.GetTeam(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent team, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

// --- Sessions CRUD ---

func TestSessions_CRUD(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	session := &AgentSession{
		ID:        "sess-1",
		AgentID:   "agent-1",
		JobID:     "job-1",
		TaskID:    "task-1",
		Status:    SessionStatusActive,
		Model:     "claude-opus-4-20250514",
		Provider:  "anthropic",
		TokensIn:  100,
		TokensOut: 50,
	}

	// Create
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Get active
	active, err := store.GetActiveSessions(ctx)
	if err != nil {
		t.Fatalf("GetActiveSessions: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("GetActiveSessions returned %d sessions, want 1", len(active))
	}
	if active[0].ID != "sess-1" {
		t.Errorf("session ID = %q, want %q", active[0].ID, "sess-1")
	}
	if active[0].Model != "claude-opus-4-20250514" {
		t.Errorf("Model = %q, want %q", active[0].Model, "claude-opus-4-20250514")
	}
	if active[0].TokensIn != 100 {
		t.Errorf("TokensIn = %d, want 100", active[0].TokensIn)
	}
	if active[0].TokensOut != 50 {
		t.Errorf("TokensOut = %d, want 50", active[0].TokensOut)
	}

	// Update
	completedStatus := SessionStatusCompleted
	endTime := time.Now().UTC()
	cost := 0.05
	tokensIn := int64(500)
	tokensOut := int64(200)
	if err := store.UpdateSession(ctx, "sess-1", SessionUpdate{
		Status:    &completedStatus,
		TokensIn:  &tokensIn,
		TokensOut: &tokensOut,
		EndedAt:   &endTime,
		CostUSD:   &cost,
	}); err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}

	// After completing, GetActiveSessions should return empty.
	active, err = store.GetActiveSessions(ctx)
	if err != nil {
		t.Fatalf("GetActiveSessions after update: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("GetActiveSessions returned %d sessions, want 0", len(active))
	}

	// Verify the update by reading the session directly.
	var status string
	var gotTokensIn, gotTokensOut int64
	var endedAt sql.NullString
	var costUSD sql.NullFloat64
	err = store.db.QueryRow(
		"SELECT status, tokens_in, tokens_out, ended_at, cost_usd FROM agent_sessions WHERE id = ?",
		"sess-1",
	).Scan(&status, &gotTokensIn, &gotTokensOut, &endedAt, &costUSD)
	if err != nil {
		t.Fatalf("querying session: %v", err)
	}
	if status != "completed" {
		t.Errorf("status = %q, want %q", status, "completed")
	}
	if gotTokensIn != 500 {
		t.Errorf("tokens_in = %d, want 500", gotTokensIn)
	}
	if gotTokensOut != 200 {
		t.Errorf("tokens_out = %d, want 200", gotTokensOut)
	}
	if !endedAt.Valid {
		t.Error("ended_at should be set")
	}
	if !costUSD.Valid || costUSD.Float64 != 0.05 {
		t.Errorf("cost_usd = %v, want 0.05", costUSD)
	}
}

func TestUpdateSession_NotFound(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	status := SessionStatusCompleted
	err := store.UpdateSession(ctx, "nonexistent", SessionUpdate{Status: &status})
	if err == nil {
		t.Fatal("expected error for nonexistent session, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestUpdateSession_NoFields(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Updating with no fields should be a no-op, not an error.
	err := store.UpdateSession(ctx, "anything", SessionUpdate{})
	if err != nil {
		t.Fatalf("UpdateSession with no fields: %v", err)
	}
}

func TestSessions_WithEndedAtAndCost(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	endTime := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
	cost := 1.23
	session := &AgentSession{
		ID:        "sess-full",
		AgentID:   "agent-1",
		Status:    SessionStatusCompleted,
		Model:     "test-model",
		Provider:  "test",
		TokensIn:  1000,
		TokensOut: 500,
		EndedAt:   &endTime,
		CostUSD:   &cost,
	}

	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Should not appear in active sessions.
	active, err := store.GetActiveSessions(ctx)
	if err != nil {
		t.Fatalf("GetActiveSessions: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("GetActiveSessions returned %d, want 0", len(active))
	}
}

// --- Artifacts ---

func TestArtifacts(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	job := &Job{ID: "job-art", Title: "Artifact test", Type: "test", Status: JobStatusActive}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	art1 := &Artifact{
		JobID:   "job-art",
		TaskID:  "task-1",
		Type:    "code",
		Path:    "/src/main.go",
		Summary: "Main entry point",
	}
	if err := store.LogArtifact(ctx, art1); err != nil {
		t.Fatalf("LogArtifact: %v", err)
	}
	if art1.ID == 0 {
		t.Error("artifact ID should be set after insert")
	}

	art2 := &Artifact{
		JobID:   "job-art",
		Type:    "report",
		Path:    "/reports/summary.md",
		Summary: "Final report",
	}
	if err := store.LogArtifact(ctx, art2); err != nil {
		t.Fatalf("LogArtifact (2): %v", err)
	}

	// List for job
	artifacts, err := store.ListArtifactsForJob(ctx, "job-art")
	if err != nil {
		t.Fatalf("ListArtifactsForJob: %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("ListArtifactsForJob returned %d artifacts, want 2", len(artifacts))
	}
	if artifacts[0].Path != "/src/main.go" {
		t.Errorf("first artifact path = %q, want %q", artifacts[0].Path, "/src/main.go")
	}
	if artifacts[1].Path != "/reports/summary.md" {
		t.Errorf("second artifact path = %q, want %q", artifacts[1].Path, "/reports/summary.md")
	}

	// List for different job should be empty.
	empty, err := store.ListArtifactsForJob(ctx, "nonexistent-job")
	if err != nil {
		t.Fatalf("ListArtifactsForJob(nonexistent): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ListArtifactsForJob(nonexistent) returned %d, want 0", len(empty))
	}
}

// --- Migration tests ---

func TestMigrationVersion(t *testing.T) {
	store := openTestStore(t)

	var version int
	if err := store.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("querying schema version: %v", err)
	}
	if version < 1 {
		t.Errorf("schema version = %d, want >= 1", version)
	}
}

// --- Helper function tests ---

func TestParseTime_RFC3339(t *testing.T) {
	input := "2025-06-15T10:30:00Z"
	got := parseTime(input)
	want := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("parseTime(%q) = %v, want %v", input, got, want)
	}
}

func TestParseTime_SQLiteFormat(t *testing.T) {
	input := "2025-06-15 10:30:00"
	got := parseTime(input)
	want := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("parseTime(%q) = %v, want %v", input, got, want)
	}
}

func TestParseTime_Invalid(t *testing.T) {
	got := parseTime("not-a-time")
	if !got.IsZero() {
		t.Errorf("parseTime(invalid) = %v, want zero time", got)
	}
}

func TestNullableJSON_Nil(t *testing.T) {
	got := nullableJSON(nil)
	if got != nil {
		t.Errorf("nullableJSON(nil) = %v, want nil", got)
	}
}

func TestNullableJSON_Empty(t *testing.T) {
	got := nullableJSON(json.RawMessage{})
	if got != nil {
		t.Errorf("nullableJSON(empty) = %v, want nil", got)
	}
}

func TestNullableJSON_Valid(t *testing.T) {
	data := json.RawMessage(`{"key":"value"}`)
	got := nullableJSON(data)
	if got != `{"key":"value"}` {
		t.Errorf("nullableJSON(data) = %v, want %q", got, `{"key":"value"}`)
	}
}

// --- Concurrent access test ---

func TestConcurrentReads(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Create some data.
	for i := range 10 {
		job := &Job{
			ID:     fmt.Sprintf("conc-job-%d", i),
			Title:  fmt.Sprintf("Job %d", i),
			Type:   "test",
			Status: JobStatusPending,
		}
		if err := store.CreateJob(ctx, job); err != nil {
			t.Fatalf("CreateJob(%d): %v", i, err)
		}
	}

	// Read concurrently.
	errs := make(chan error, 20)
	for range 20 {
		go func() {
			_, err := store.ListJobs(ctx, JobFilter{})
			errs <- err
		}()
	}

	for range 20 {
		if err := <-errs; err != nil {
			t.Errorf("concurrent ListJobs: %v", err)
		}
	}
}
