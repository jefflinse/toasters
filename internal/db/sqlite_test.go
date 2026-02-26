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
		"skills",
		"team_agents",
		"feed_entries",
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

	// Create agents first for team agents.
	agent1 := &Agent{ID: "tm-agent-1", Name: "Lead", Mode: "lead", Source: "test"}
	agent2 := &Agent{ID: "tm-agent-2", Name: "Worker", Mode: "worker", Source: "test"}
	for _, a := range []*Agent{agent1, agent2} {
		if err := store.UpsertAgent(ctx, a); err != nil {
			t.Fatalf("UpsertAgent(%s): %v", a.ID, err)
		}
	}

	skills := json.RawMessage(`["go","testing"]`)
	team := &Team{
		ID:          "team-1",
		Name:        "Backend Team",
		Description: "Handles backend work",
		LeadAgent:   "tm-agent-1",
		Skills:      skills,
		Provider:    "anthropic",
		Model:       "claude-opus-4-20250514",
		Culture:     "We write clean code.",
		Source:      "user",
		SourcePath:  "/teams/backend.md",
	}

	// Upsert (insert)
	if err := store.UpsertTeam(ctx, team); err != nil {
		t.Fatalf("UpsertTeam: %v", err)
	}

	// Get
	got, err := store.GetTeam(ctx, "team-1")
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if got.Name != "Backend Team" {
		t.Errorf("Name = %q, want %q", got.Name, "Backend Team")
	}
	if got.LeadAgent != "tm-agent-1" {
		t.Errorf("LeadAgent = %q, want %q", got.LeadAgent, "tm-agent-1")
	}
	if string(got.Skills) != `["go","testing"]` {
		t.Errorf("Skills = %q, want %q", string(got.Skills), `["go","testing"]`)
	}
	if got.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", got.Provider, "anthropic")
	}
	if got.Culture != "We write clean code." {
		t.Errorf("Culture = %q, want %q", got.Culture, "We write clean code.")
	}
	if got.Source != "user" {
		t.Errorf("Source = %q, want %q", got.Source, "user")
	}
	if got.SourcePath != "/teams/backend.md" {
		t.Errorf("SourcePath = %q, want %q", got.SourcePath, "/teams/backend.md")
	}

	// Upsert (update)
	team.Name = "Backend Team v2"
	team.UpdatedAt = time.Time{} // reset so it gets set
	if err := store.UpsertTeam(ctx, team); err != nil {
		t.Fatalf("UpsertTeam (update): %v", err)
	}
	got, err = store.GetTeam(ctx, "team-1")
	if err != nil {
		t.Fatalf("GetTeam after upsert: %v", err)
	}
	if got.Name != "Backend Team v2" {
		t.Errorf("Name after upsert = %q, want %q", got.Name, "Backend Team v2")
	}

	// List
	team2 := &Team{ID: "team-2", Name: "Frontend Team", Description: "Handles frontend", Source: "user"}
	if err := store.UpsertTeam(ctx, team2); err != nil {
		t.Fatalf("UpsertTeam (2): %v", err)
	}
	teams, err := store.ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 2 {
		t.Fatalf("ListTeams returned %d teams, want 2", len(teams))
	}

	// Add team agents
	ta1 := &TeamAgent{TeamID: "team-1", AgentID: "tm-agent-1", Role: "lead"}
	if err := store.AddTeamAgent(ctx, ta1); err != nil {
		t.Fatalf("AddTeamAgent: %v", err)
	}
	ta2 := &TeamAgent{TeamID: "team-1", AgentID: "tm-agent-2", Role: "worker"}
	if err := store.AddTeamAgent(ctx, ta2); err != nil {
		t.Fatalf("AddTeamAgent (2): %v", err)
	}

	// List team agents
	teamAgents, err := store.ListTeamAgents(ctx, "team-1")
	if err != nil {
		t.Fatalf("ListTeamAgents: %v", err)
	}
	if len(teamAgents) != 2 {
		t.Errorf("team agent count = %d, want 2", len(teamAgents))
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

// --- Job Description and WorkspaceDir ---

func TestJobs_DescriptionAndWorkspaceDir(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	job := &Job{
		ID:           "job-desc",
		Title:        "Described job",
		Description:  "This is a detailed description",
		Type:         "new_feature",
		Status:       JobStatusPending,
		WorkspaceDir: "/home/user/project",
	}

	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	got, err := store.GetJob(ctx, "job-desc")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Description != "This is a detailed description" {
		t.Errorf("Description = %q, want %q", got.Description, "This is a detailed description")
	}
	if got.WorkspaceDir != "/home/user/project" {
		t.Errorf("WorkspaceDir = %q, want %q", got.WorkspaceDir, "/home/user/project")
	}
}

func TestJobs_DefaultDescriptionAndWorkspaceDir(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Create a job without setting Description or WorkspaceDir — they should default to "".
	job := &Job{ID: "job-nofields", Title: "Minimal", Type: "test", Status: JobStatusPending}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	got, err := store.GetJob(ctx, "job-nofields")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Description != "" {
		t.Errorf("Description = %q, want empty", got.Description)
	}
	if got.WorkspaceDir != "" {
		t.Errorf("WorkspaceDir = %q, want empty", got.WorkspaceDir)
	}
}

func TestJobs_ListIncludesNewFields(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	job := &Job{
		ID:           "job-list-new",
		Title:        "Listed job",
		Description:  "Listed description",
		Type:         "bug_fix",
		Status:       JobStatusActive,
		WorkspaceDir: "/tmp/workspace",
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	jobs, err := store.ListJobs(ctx, JobFilter{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("ListJobs returned %d jobs, want 1", len(jobs))
	}
	if jobs[0].Description != "Listed description" {
		t.Errorf("Description = %q, want %q", jobs[0].Description, "Listed description")
	}
	if jobs[0].WorkspaceDir != "/tmp/workspace" {
		t.Errorf("WorkspaceDir = %q, want %q", jobs[0].WorkspaceDir, "/tmp/workspace")
	}
}

// --- ListAllJobs ---

func TestListAllJobs(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Create multiple jobs with different statuses.
	for i, status := range []JobStatus{JobStatusPending, JobStatusActive, JobStatusCompleted, JobStatusPaused} {
		job := &Job{
			ID:     fmt.Sprintf("all-job-%d", i),
			Title:  fmt.Sprintf("Job %d", i),
			Type:   "test",
			Status: status,
		}
		if err := store.CreateJob(ctx, job); err != nil {
			t.Fatalf("CreateJob(%d): %v", i, err)
		}
	}

	jobs, err := store.ListAllJobs(ctx)
	if err != nil {
		t.Fatalf("ListAllJobs: %v", err)
	}
	if len(jobs) != 4 {
		t.Fatalf("ListAllJobs returned %d jobs, want 4", len(jobs))
	}
}

func TestListAllJobs_Empty(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	jobs, err := store.ListAllJobs(ctx)
	if err != nil {
		t.Fatalf("ListAllJobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("ListAllJobs returned %d jobs, want 0", len(jobs))
	}
}

// --- UpdateJob ---

func TestUpdateJob_AllFields(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	job := &Job{
		ID:           "job-upd",
		Title:        "Original",
		Description:  "Original desc",
		Type:         "test",
		Status:       JobStatusPending,
		WorkspaceDir: "/original",
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	newTitle := "Updated Title"
	newDesc := "Updated description"
	newStatus := JobStatusActive
	newDir := "/updated/workspace"

	if err := store.UpdateJob(ctx, "job-upd", JobUpdate{
		Title:        &newTitle,
		Description:  &newDesc,
		Status:       &newStatus,
		WorkspaceDir: &newDir,
	}); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}

	got, err := store.GetJob(ctx, "job-upd")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Title != "Updated Title" {
		t.Errorf("Title = %q, want %q", got.Title, "Updated Title")
	}
	if got.Description != "Updated description" {
		t.Errorf("Description = %q, want %q", got.Description, "Updated description")
	}
	if got.Status != JobStatusActive {
		t.Errorf("Status = %q, want %q", got.Status, JobStatusActive)
	}
	if got.WorkspaceDir != "/updated/workspace" {
		t.Errorf("WorkspaceDir = %q, want %q", got.WorkspaceDir, "/updated/workspace")
	}
	if got.UpdatedAt.Before(got.CreatedAt) {
		t.Errorf("UpdatedAt (%v) should not be before CreatedAt (%v)", got.UpdatedAt, got.CreatedAt)
	}
}

func TestUpdateJob_PartialUpdate(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	job := &Job{
		ID:           "job-partial",
		Title:        "Original",
		Description:  "Original desc",
		Type:         "test",
		Status:       JobStatusPending,
		WorkspaceDir: "/original",
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// Only update description — other fields should remain unchanged.
	newDesc := "Only this changed"
	if err := store.UpdateJob(ctx, "job-partial", JobUpdate{
		Description: &newDesc,
	}); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}

	got, err := store.GetJob(ctx, "job-partial")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Title != "Original" {
		t.Errorf("Title = %q, want %q (should be unchanged)", got.Title, "Original")
	}
	if got.Description != "Only this changed" {
		t.Errorf("Description = %q, want %q", got.Description, "Only this changed")
	}
	if got.Status != JobStatusPending {
		t.Errorf("Status = %q, want %q (should be unchanged)", got.Status, JobStatusPending)
	}
	if got.WorkspaceDir != "/original" {
		t.Errorf("WorkspaceDir = %q, want %q (should be unchanged)", got.WorkspaceDir, "/original")
	}
}

func TestUpdateJob_NoFields(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Updating with no fields should be a no-op, not an error.
	err := store.UpdateJob(ctx, "anything", JobUpdate{})
	if err != nil {
		t.Fatalf("UpdateJob with no fields: %v", err)
	}
}

func TestUpdateJob_NotFound(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	newTitle := "Nope"
	err := store.UpdateJob(ctx, "nonexistent", JobUpdate{Title: &newTitle})
	if err == nil {
		t.Fatal("expected error for nonexistent job, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

// --- JobStatusPaused ---

func TestJobStatusPaused(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	job := &Job{ID: "job-paused", Title: "Paused job", Type: "test", Status: JobStatusPaused}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	got, err := store.GetJob(ctx, "job-paused")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != JobStatusPaused {
		t.Errorf("Status = %q, want %q", got.Status, JobStatusPaused)
	}

	// Filter by paused status.
	pausedStatus := JobStatusPaused
	jobs, err := store.ListJobs(ctx, JobFilter{Status: &pausedStatus})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("ListJobs(paused) returned %d jobs, want 1", len(jobs))
	}
}

// --- Task TeamID ---

func TestTasks_TeamID(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	job := &Job{ID: "job-team", Title: "Team test", Type: "test", Status: JobStatusActive}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	task := &Task{
		ID:        "task-team-1",
		JobID:     "job-team",
		Title:     "Team task",
		Status:    TaskStatusPending,
		AgentID:   "agent-1",
		TeamID:    "backend-team",
		SortOrder: 1,
	}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// GetTask
	got, err := store.GetTask(ctx, "task-team-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.TeamID != "backend-team" {
		t.Errorf("TeamID = %q, want %q", got.TeamID, "backend-team")
	}

	// ListTasksForJob
	tasks, err := store.ListTasksForJob(ctx, "job-team")
	if err != nil {
		t.Fatalf("ListTasksForJob: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("ListTasksForJob returned %d tasks, want 1", len(tasks))
	}
	if tasks[0].TeamID != "backend-team" {
		t.Errorf("TeamID = %q, want %q", tasks[0].TeamID, "backend-team")
	}

	// GetReadyTasks
	ready, err := store.GetReadyTasks(ctx, "job-team")
	if err != nil {
		t.Fatalf("GetReadyTasks: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("GetReadyTasks returned %d tasks, want 1", len(ready))
	}
	if ready[0].TeamID != "backend-team" {
		t.Errorf("TeamID = %q, want %q", ready[0].TeamID, "backend-team")
	}
}

func TestTasks_DefaultTeamID(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	job := &Job{ID: "job-noteam", Title: "No team", Type: "test", Status: JobStatusActive}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// Create task without TeamID — should default to "".
	task := &Task{ID: "task-noteam", JobID: "job-noteam", Title: "No team task", Status: TaskStatusPending}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := store.GetTask(ctx, "task-noteam")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.TeamID != "" {
		t.Errorf("TeamID = %q, want empty", got.TeamID)
	}
}

// --- Migration 002 ---

func TestMigration002_NewColumns(t *testing.T) {
	store := openTestStore(t)

	// Verify the new columns exist by querying table_info.
	checkColumn := func(table, column string) {
		t.Helper()
		var found bool
		rows, err := store.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
		if err != nil {
			t.Fatalf("PRAGMA table_info(%s): %v", table, err)
		}
		defer rows.Close() //nolint:errcheck
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull int
			var dflt sql.NullString
			var pk int
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				t.Fatalf("scanning column info: %v", err)
			}
			if name == column {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("column %s.%s not found", table, column)
		}
	}

	checkColumn("jobs", "description")
	checkColumn("jobs", "workspace_dir")
	checkColumn("tasks", "team_id")
}

// --- Migration tests ---

func TestMigrationVersion(t *testing.T) {
	store := openTestStore(t)

	var version int
	if err := store.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("querying schema version: %v", err)
	}
	if version < 3 {
		t.Errorf("schema version = %d, want >= 3", version)
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

// --- Skills CRUD ---

func TestSkills_CRUD(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	tools := json.RawMessage(`["read","write","glob"]`)
	skill := &Skill{
		ID:          "skill-go",
		Name:        "Go Development",
		Description: "Write and test Go code",
		Tools:       tools,
		Prompt:      "You are a Go expert.",
		Source:      "builtin",
		SourcePath:  "/skills/go.md",
	}

	// Upsert (insert)
	if err := store.UpsertSkill(ctx, skill); err != nil {
		t.Fatalf("UpsertSkill (insert): %v", err)
	}

	// Get
	got, err := store.GetSkill(ctx, "skill-go")
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if got.Name != "Go Development" {
		t.Errorf("Name = %q, want %q", got.Name, "Go Development")
	}
	if got.Description != "Write and test Go code" {
		t.Errorf("Description = %q, want %q", got.Description, "Write and test Go code")
	}
	if string(got.Tools) != `["read","write","glob"]` {
		t.Errorf("Tools = %q, want %q", string(got.Tools), `["read","write","glob"]`)
	}
	if got.Prompt != "You are a Go expert." {
		t.Errorf("Prompt = %q, want %q", got.Prompt, "You are a Go expert.")
	}
	if got.Source != "builtin" {
		t.Errorf("Source = %q, want %q", got.Source, "builtin")
	}
	if got.SourcePath != "/skills/go.md" {
		t.Errorf("SourcePath = %q, want %q", got.SourcePath, "/skills/go.md")
	}

	// Upsert (update)
	skill.Name = "Go Development v2"
	skill.UpdatedAt = time.Time{} // reset so it gets set
	if err := store.UpsertSkill(ctx, skill); err != nil {
		t.Fatalf("UpsertSkill (update): %v", err)
	}
	got, err = store.GetSkill(ctx, "skill-go")
	if err != nil {
		t.Fatalf("GetSkill after upsert: %v", err)
	}
	if got.Name != "Go Development v2" {
		t.Errorf("Name after upsert = %q, want %q", got.Name, "Go Development v2")
	}

	// List
	skill2 := &Skill{ID: "skill-ts", Name: "TypeScript", Source: "user"}
	if err := store.UpsertSkill(ctx, skill2); err != nil {
		t.Fatalf("UpsertSkill (2): %v", err)
	}
	skills, err := store.ListSkills(ctx)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("ListSkills returned %d skills, want 2", len(skills))
	}

	// DeleteAll
	if err := store.DeleteAllSkills(ctx); err != nil {
		t.Fatalf("DeleteAllSkills: %v", err)
	}
	skills, err = store.ListSkills(ctx)
	if err != nil {
		t.Fatalf("ListSkills after delete: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("ListSkills after delete returned %d skills, want 0", len(skills))
	}
}

func TestGetSkill_NotFound(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	_, err := store.GetSkill(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent skill, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

// --- Agent new fields ---

func TestAgents_NewFields(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	temp := 0.7
	maxTurns := 10
	tools := json.RawMessage(`["read","write"]`)
	disallowed := json.RawMessage(`["bash"]`)
	skills := json.RawMessage(`["go","testing"]`)
	permissions := json.RawMessage(`{"allow":["read"]}`)
	mcpServers := json.RawMessage(`{"github":{"enabled":true}}`)

	agent := &Agent{
		ID:              "agent-full",
		Name:            "Full Agent",
		Description:     "Agent with all fields",
		Mode:            "lead",
		Model:           "claude-opus-4-20250514",
		Provider:        "anthropic",
		Temperature:     &temp,
		SystemPrompt:    "You are a lead agent.",
		Tools:           tools,
		DisallowedTools: disallowed,
		Skills:          skills,
		PermissionMode:  "plan",
		Permissions:     permissions,
		MCPServers:      mcpServers,
		MaxTurns:        &maxTurns,
		Color:           "#ff0000",
		Hidden:          true,
		Disabled:        false,
		Source:          "user",
		SourcePath:      "/agents/full.md",
		TeamID:          "team-backend",
	}

	if err := store.UpsertAgent(ctx, agent); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	got, err := store.GetAgent(ctx, "agent-full")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}

	if got.Mode != "lead" {
		t.Errorf("Mode = %q, want %q", got.Mode, "lead")
	}
	if string(got.DisallowedTools) != `["bash"]` {
		t.Errorf("DisallowedTools = %q, want %q", string(got.DisallowedTools), `["bash"]`)
	}
	if string(got.Skills) != `["go","testing"]` {
		t.Errorf("Skills = %q, want %q", string(got.Skills), `["go","testing"]`)
	}
	if got.PermissionMode != "plan" {
		t.Errorf("PermissionMode = %q, want %q", got.PermissionMode, "plan")
	}
	if string(got.Permissions) != `{"allow":["read"]}` {
		t.Errorf("Permissions = %q, want %q", string(got.Permissions), `{"allow":["read"]}`)
	}
	if string(got.MCPServers) != `{"github":{"enabled":true}}` {
		t.Errorf("MCPServers = %q, want %q", string(got.MCPServers), `{"github":{"enabled":true}}`)
	}
	if got.MaxTurns == nil || *got.MaxTurns != 10 {
		t.Errorf("MaxTurns = %v, want 10", got.MaxTurns)
	}
	if got.Color != "#ff0000" {
		t.Errorf("Color = %q, want %q", got.Color, "#ff0000")
	}
	if !got.Hidden {
		t.Error("Hidden = false, want true")
	}
	if got.Disabled {
		t.Error("Disabled = true, want false")
	}
	if got.SourcePath != "/agents/full.md" {
		t.Errorf("SourcePath = %q, want %q", got.SourcePath, "/agents/full.md")
	}
	if got.TeamID != "team-backend" {
		t.Errorf("TeamID = %q, want %q", got.TeamID, "team-backend")
	}
}

func TestAgents_DeleteAll(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"a1", "a2", "a3"} {
		if err := store.UpsertAgent(ctx, &Agent{ID: id, Name: id, Source: "test"}); err != nil {
			t.Fatalf("UpsertAgent(%s): %v", id, err)
		}
	}

	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 3 {
		t.Fatalf("ListAgents returned %d, want 3", len(agents))
	}

	if err := store.DeleteAllAgents(ctx); err != nil {
		t.Fatalf("DeleteAllAgents: %v", err)
	}

	agents, err = store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents after delete: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("ListAgents after delete returned %d, want 0", len(agents))
	}
}

// --- Team new fields ---

func TestTeams_IsAuto(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	team := &Team{
		ID:     "team-auto",
		Name:   "Auto Team",
		IsAuto: true,
		Source: "auto",
	}
	if err := store.UpsertTeam(ctx, team); err != nil {
		t.Fatalf("UpsertTeam: %v", err)
	}

	got, err := store.GetTeam(ctx, "team-auto")
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if !got.IsAuto {
		t.Error("IsAuto = false, want true")
	}
}

func TestTeams_DeleteAll(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"t1", "t2"} {
		if err := store.UpsertTeam(ctx, &Team{ID: id, Name: id, Source: "test"}); err != nil {
			t.Fatalf("UpsertTeam(%s): %v", id, err)
		}
	}

	if err := store.DeleteAllTeams(ctx); err != nil {
		t.Fatalf("DeleteAllTeams: %v", err)
	}

	teams, err := store.ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams after delete: %v", err)
	}
	if len(teams) != 0 {
		t.Errorf("ListTeams after delete returned %d, want 0", len(teams))
	}
}

// --- Team Agents ---

func TestTeamAgents_CRUD(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Create prerequisite data.
	if err := store.UpsertTeam(ctx, &Team{ID: "ta-team", Name: "Test Team", Source: "test"}); err != nil {
		t.Fatalf("UpsertTeam: %v", err)
	}
	for _, id := range []string{"ta-agent-1", "ta-agent-2", "ta-agent-3"} {
		if err := store.UpsertAgent(ctx, &Agent{ID: id, Name: id, Source: "test"}); err != nil {
			t.Fatalf("UpsertAgent(%s): %v", id, err)
		}
	}

	// Add team agents.
	for _, ta := range []*TeamAgent{
		{TeamID: "ta-team", AgentID: "ta-agent-1", Role: "lead"},
		{TeamID: "ta-team", AgentID: "ta-agent-2", Role: "worker"},
		{TeamID: "ta-team", AgentID: "ta-agent-3", Role: "worker"},
	} {
		if err := store.AddTeamAgent(ctx, ta); err != nil {
			t.Fatalf("AddTeamAgent(%s): %v", ta.AgentID, err)
		}
	}

	// List.
	teamAgents, err := store.ListTeamAgents(ctx, "ta-team")
	if err != nil {
		t.Fatalf("ListTeamAgents: %v", err)
	}
	if len(teamAgents) != 3 {
		t.Fatalf("ListTeamAgents returned %d, want 3", len(teamAgents))
	}

	// Verify lead comes first (ordered by role, agent_id).
	if teamAgents[0].Role != "lead" {
		t.Errorf("first team agent role = %q, want %q", teamAgents[0].Role, "lead")
	}

	// List for nonexistent team should be empty.
	empty, err := store.ListTeamAgents(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("ListTeamAgents(nonexistent): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ListTeamAgents(nonexistent) returned %d, want 0", len(empty))
	}

	// DeleteAll.
	if err := store.DeleteAllTeamAgents(ctx); err != nil {
		t.Fatalf("DeleteAllTeamAgents: %v", err)
	}
	teamAgents, err = store.ListTeamAgents(ctx, "ta-team")
	if err != nil {
		t.Fatalf("ListTeamAgents after delete: %v", err)
	}
	if len(teamAgents) != 0 {
		t.Errorf("ListTeamAgents after delete returned %d, want 0", len(teamAgents))
	}
}

// --- Feed Entries ---

func TestFeedEntries_CRUD(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Create entries with explicit timestamps for deterministic ordering.
	baseTime := time.Date(2026, 2, 26, 10, 0, 0, 0, time.UTC)

	meta := json.RawMessage(`{"agent":"builder"}`)
	entry := &FeedEntry{
		JobID:     "job-1",
		EntryType: FeedEntryTaskStarted,
		Content:   "Task started: implement feature",
		Metadata:  meta,
		CreatedAt: baseTime,
	}

	// Create
	if err := store.CreateFeedEntry(ctx, entry); err != nil {
		t.Fatalf("CreateFeedEntry: %v", err)
	}
	if entry.ID == 0 {
		t.Error("entry ID should be set after insert")
	}

	// Create more entries.
	for i, et := range []FeedEntryType{FeedEntryUserMessage, FeedEntryOperatorMessage, FeedEntryTaskCompleted} {
		e := &FeedEntry{
			JobID:     "job-1",
			EntryType: et,
			Content:   fmt.Sprintf("Entry %d", i),
			CreatedAt: baseTime.Add(time.Duration(i+1) * time.Minute),
		}
		if err := store.CreateFeedEntry(ctx, e); err != nil {
			t.Fatalf("CreateFeedEntry(%d): %v", i, err)
		}
	}

	// List for job with limit.
	entries, err := store.ListFeedEntries(ctx, "job-1", 2)
	if err != nil {
		t.Fatalf("ListFeedEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ListFeedEntries returned %d entries, want 2", len(entries))
	}
	// Most recent first.
	if entries[0].EntryType != FeedEntryTaskCompleted {
		t.Errorf("first entry type = %q, want %q", entries[0].EntryType, FeedEntryTaskCompleted)
	}

	// List all for job (default limit).
	all, err := store.ListFeedEntries(ctx, "job-1", 0)
	if err != nil {
		t.Fatalf("ListFeedEntries(0): %v", err)
	}
	if len(all) != 4 {
		t.Errorf("ListFeedEntries(0) returned %d entries, want 4", len(all))
	}

	// List for different job should be empty.
	empty, err := store.ListFeedEntries(ctx, "nonexistent", 0)
	if err != nil {
		t.Fatalf("ListFeedEntries(nonexistent): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ListFeedEntries(nonexistent) returned %d, want 0", len(empty))
	}
}

func TestFeedEntries_ListRecent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Create entries across different jobs.
	baseTime := time.Date(2026, 2, 26, 10, 0, 0, 0, time.UTC)
	for i := range 5 {
		e := &FeedEntry{
			JobID:     fmt.Sprintf("job-%d", i%2),
			EntryType: FeedEntrySystemEvent,
			Content:   fmt.Sprintf("Event %d", i),
			CreatedAt: baseTime.Add(time.Duration(i) * time.Minute),
		}
		if err := store.CreateFeedEntry(ctx, e); err != nil {
			t.Fatalf("CreateFeedEntry(%d): %v", i, err)
		}
	}

	// List recent across all jobs.
	entries, err := store.ListRecentFeedEntries(ctx, 3)
	if err != nil {
		t.Fatalf("ListRecentFeedEntries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("ListRecentFeedEntries returned %d entries, want 3", len(entries))
	}
	// Most recent first.
	if entries[0].Content != "Event 4" {
		t.Errorf("first entry content = %q, want %q", entries[0].Content, "Event 4")
	}
}

func TestFeedEntries_NilMetadata(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	entry := &FeedEntry{
		EntryType: FeedEntrySystemEvent,
		Content:   "No metadata",
	}
	if err := store.CreateFeedEntry(ctx, entry); err != nil {
		t.Fatalf("CreateFeedEntry: %v", err)
	}

	entries, err := store.ListRecentFeedEntries(ctx, 1)
	if err != nil {
		t.Fatalf("ListRecentFeedEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ListRecentFeedEntries returned %d, want 1", len(entries))
	}
	if entries[0].Metadata != nil {
		t.Errorf("Metadata = %q, want nil", string(entries[0].Metadata))
	}
}

// --- RebuildDefinitions ---

func TestRebuildDefinitions(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Initial data.
	skills1 := []*Skill{
		{ID: "sk-1", Name: "Go", Source: "builtin", Prompt: "Go expert"},
		{ID: "sk-2", Name: "TypeScript", Source: "builtin", Prompt: "TS expert"},
	}
	agents1 := []*Agent{
		{ID: "ag-1", Name: "Builder", Mode: "worker", Source: "user"},
		{ID: "ag-2", Name: "Reviewer", Mode: "lead", Source: "user"},
	}
	teams1 := []*Team{
		{ID: "tm-1", Name: "Backend", LeadAgent: "ag-2", Source: "user"},
	}
	teamAgents1 := []*TeamAgent{
		{TeamID: "tm-1", AgentID: "ag-1", Role: "worker"},
		{TeamID: "tm-1", AgentID: "ag-2", Role: "lead"},
	}

	// First rebuild.
	if err := store.RebuildDefinitions(ctx, skills1, agents1, teams1, teamAgents1); err != nil {
		t.Fatalf("RebuildDefinitions (first): %v", err)
	}

	// Verify initial data.
	skillList, err := store.ListSkills(ctx)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skillList) != 2 {
		t.Errorf("ListSkills returned %d, want 2", len(skillList))
	}

	agentList, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agentList) != 2 {
		t.Errorf("ListAgents returned %d, want 2", len(agentList))
	}

	teamList, err := store.ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teamList) != 1 {
		t.Errorf("ListTeams returned %d, want 1", len(teamList))
	}

	taList, err := store.ListTeamAgents(ctx, "tm-1")
	if err != nil {
		t.Fatalf("ListTeamAgents: %v", err)
	}
	if len(taList) != 2 {
		t.Errorf("ListTeamAgents returned %d, want 2", len(taList))
	}

	// Second rebuild with different data — old data should be gone.
	skills2 := []*Skill{
		{ID: "sk-3", Name: "Python", Source: "user", Prompt: "Python expert"},
	}
	agents2 := []*Agent{
		{ID: "ag-3", Name: "Tester", Mode: "worker", Source: "system"},
	}
	teams2 := []*Team{
		{ID: "tm-2", Name: "QA", LeadAgent: "ag-3", Source: "system"},
	}
	teamAgents2 := []*TeamAgent{
		{TeamID: "tm-2", AgentID: "ag-3", Role: "lead"},
	}

	if err := store.RebuildDefinitions(ctx, skills2, agents2, teams2, teamAgents2); err != nil {
		t.Fatalf("RebuildDefinitions (second): %v", err)
	}

	// Verify old data is gone.
	skillList, err = store.ListSkills(ctx)
	if err != nil {
		t.Fatalf("ListSkills after rebuild: %v", err)
	}
	if len(skillList) != 1 {
		t.Fatalf("ListSkills after rebuild returned %d, want 1", len(skillList))
	}
	if skillList[0].ID != "sk-3" {
		t.Errorf("skill ID = %q, want %q", skillList[0].ID, "sk-3")
	}

	agentList, err = store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents after rebuild: %v", err)
	}
	if len(agentList) != 1 {
		t.Fatalf("ListAgents after rebuild returned %d, want 1", len(agentList))
	}
	if agentList[0].ID != "ag-3" {
		t.Errorf("agent ID = %q, want %q", agentList[0].ID, "ag-3")
	}

	teamList, err = store.ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams after rebuild: %v", err)
	}
	if len(teamList) != 1 {
		t.Fatalf("ListTeams after rebuild returned %d, want 1", len(teamList))
	}
	if teamList[0].ID != "tm-2" {
		t.Errorf("team ID = %q, want %q", teamList[0].ID, "tm-2")
	}

	// Old team agents should be gone.
	taList, err = store.ListTeamAgents(ctx, "tm-1")
	if err != nil {
		t.Fatalf("ListTeamAgents(tm-1) after rebuild: %v", err)
	}
	if len(taList) != 0 {
		t.Errorf("ListTeamAgents(tm-1) after rebuild returned %d, want 0", len(taList))
	}

	// New team agents should exist.
	taList, err = store.ListTeamAgents(ctx, "tm-2")
	if err != nil {
		t.Fatalf("ListTeamAgents(tm-2) after rebuild: %v", err)
	}
	if len(taList) != 1 {
		t.Errorf("ListTeamAgents(tm-2) after rebuild returned %d, want 1", len(taList))
	}
}

func TestRebuildDefinitions_Empty(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Rebuild with empty slices should succeed.
	if err := store.RebuildDefinitions(ctx, nil, nil, nil, nil); err != nil {
		t.Fatalf("RebuildDefinitions (empty): %v", err)
	}

	// All definition tables should be empty.
	skills, _ := store.ListSkills(ctx)
	agents, _ := store.ListAgents(ctx)
	teams, _ := store.ListTeams(ctx)
	if len(skills) != 0 || len(agents) != 0 || len(teams) != 0 {
		t.Errorf("expected all empty after empty rebuild: skills=%d agents=%d teams=%d",
			len(skills), len(agents), len(teams))
	}
}

func TestRebuildDefinitions_PreservesOperationalData(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Create operational data (jobs, tasks, sessions).
	job := &Job{ID: "job-persist", Title: "Persistent", Type: "test", Status: JobStatusActive}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	task := &Task{ID: "task-persist", JobID: "job-persist", Title: "Persistent task", Status: TaskStatusPending}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Rebuild definitions.
	if err := store.RebuildDefinitions(ctx, nil, nil, nil, nil); err != nil {
		t.Fatalf("RebuildDefinitions: %v", err)
	}

	// Operational data should still exist.
	gotJob, err := store.GetJob(ctx, "job-persist")
	if err != nil {
		t.Fatalf("GetJob after rebuild: %v", err)
	}
	if gotJob.Title != "Persistent" {
		t.Errorf("Job title = %q, want %q", gotJob.Title, "Persistent")
	}

	gotTask, err := store.GetTask(ctx, "task-persist")
	if err != nil {
		t.Fatalf("GetTask after rebuild: %v", err)
	}
	if gotTask.Title != "Persistent task" {
		t.Errorf("Task title = %q, want %q", gotTask.Title, "Persistent task")
	}
}

// --- Migration 003 ---

func TestMigration003_NewTables(t *testing.T) {
	store := openTestStore(t)

	// Verify new tables exist.
	for _, table := range []string{"skills", "team_agents", "feed_entries"} {
		var name string
		err := store.db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}

	// Verify team_members was dropped.
	var name string
	err := store.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='team_members'",
	).Scan(&name)
	if err == nil {
		t.Error("table team_members should have been dropped but still exists")
	}
}

func TestMigration003_TaskNewColumns(t *testing.T) {
	store := openTestStore(t)

	checkColumn := func(table, column string) {
		t.Helper()
		var found bool
		rows, err := store.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
		if err != nil {
			t.Fatalf("PRAGMA table_info(%s): %v", table, err)
		}
		defer rows.Close() //nolint:errcheck
		for rows.Next() {
			var cid int
			var colName, typ string
			var notnull int
			var dflt sql.NullString
			var pk int
			if err := rows.Scan(&cid, &colName, &typ, &notnull, &dflt, &pk); err != nil {
				t.Fatalf("scanning column info: %v", err)
			}
			if colName == column {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("column %s.%s not found", table, column)
		}
	}

	checkColumn("tasks", "result_summary")
	checkColumn("tasks", "recommendations")
}
