package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/orchestration"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// toolCall builds a ToolCall for job_set_status with the given job ID and status.
func jobSetStatusCall(jobID, status string) provider.ToolCall {
	args, _ := json.Marshal(map[string]string{"id": jobID, "status": status})
	return provider.ToolCall{
		Name:      "job_set_status",
		Arguments: args,
	}
}

// makeToolCall builds a generic ToolCall with the given name and arguments map.
func makeToolCall(name string, args any) provider.ToolCall {
	b, _ := json.Marshal(args)
	return provider.ToolCall{
		Name:      name,
		Arguments: b,
	}
}

// makeToolCallRaw builds a ToolCall with raw string arguments.
func makeToolCallRaw(name, rawArgs string) provider.ToolCall {
	return provider.ToolCall{
		Name:      name,
		Arguments: json.RawMessage(rawArgs),
	}
}

// openTestStore creates a SQLite store in a temp directory and registers cleanup.
func openTestStore(t *testing.T) db.Store {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// newTestExecutor creates a ToolExecutor wired to a temp directory with a real
// SQLite store. It returns the executor, the workspace dir, and the store.
func newTestExecutor(t *testing.T) (*ToolExecutor, string) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	store := openTestStore(t)
	te := NewToolExecutor(nil, nil, configDir, store, nil)
	return te, configDir
}

// newTestExecutorNilStore creates a ToolExecutor with a nil Store for testing
// the "database not available" error path.
func newTestExecutorNilStore(t *testing.T) *ToolExecutor {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	te := NewToolExecutor(nil, nil, configDir, nil, nil)
	return te
}

// createTestJob creates a job in the store and returns it.
func createTestJob(t *testing.T, store db.Store, id, title, description string, status db.JobStatus) *db.Job {
	t.Helper()
	j := &db.Job{
		ID:          id,
		Title:       title,
		Description: description,
		Type:        "general",
		Status:      status,
	}
	if err := store.CreateJob(context.Background(), j); err != nil {
		t.Fatalf("creating test job %q: %v", id, err)
	}
	return j
}

// createTestTask creates a task in the store and returns it.
func createTestTask(t *testing.T, store db.Store, jobID, title string, status db.TaskStatus) *db.Task {
	t.Helper()
	task := &db.Task{
		ID:     newTaskID(),
		JobID:  jobID,
		Title:  title,
		Status: status,
	}
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("creating test task %q: %v", title, err)
	}
	return task
}

// --- Mock AgentSpawner ---

type mockSpawner struct {
	spawnTeamFn   func(teamName, jobID, task string, team agents.Team) (int, bool, error)
	slotSummaries []orchestration.GatewaySlot
	killFn        func(slotID int) error
}

func (m *mockSpawner) SpawnTeam(teamName, jobID, task string, team agents.Team) (int, bool, error) {
	if m.spawnTeamFn != nil {
		return m.spawnTeamFn(teamName, jobID, task, team)
	}
	return 0, false, nil
}

func (m *mockSpawner) SlotSummaries() []orchestration.GatewaySlot {
	return m.slotSummaries
}

func (m *mockSpawner) Kill(slotID int) error {
	if m.killFn != nil {
		return m.killFn(slotID)
	}
	return nil
}

// ============================================================================
// job_set_status tests
// ============================================================================

func TestJobSetStatus_DoneUpdatesStatus(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "test-job-done", "Test Job", "A test job.", db.JobStatusActive)

	result, err := te.ExecuteTool(context.Background(), jobSetStatusCall("test-job-done", "done"))
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result == "" {
		t.Fatal("ExecuteTool returned empty result")
	}
	if !strings.Contains(result, "done") {
		t.Errorf("expected result to contain 'done', got %q", result)
	}

	// Verify the status in SQLite.
	j, err := te.Store.GetJob(context.Background(), "test-job-done")
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if j.Status != db.JobStatusCompleted {
		t.Errorf("status: got %q, want %q", j.Status, db.JobStatusCompleted)
	}
}

func TestJobSetStatus_ActiveUpdatesStatus(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "test-job-active", "Test Job", "A test job.", db.JobStatusCompleted)

	_, err := te.ExecuteTool(context.Background(), jobSetStatusCall("test-job-active", "active"))
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}

	j, err := te.Store.GetJob(context.Background(), "test-job-active")
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if j.Status != db.JobStatusActive {
		t.Errorf("status: got %q, want %q", j.Status, db.JobStatusActive)
	}
}

func TestJobSetStatus_PausedUpdatesStatus(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "test-job-paused", "Test Job", "A test job.", db.JobStatusCompleted)

	_, err := te.ExecuteTool(context.Background(), jobSetStatusCall("test-job-paused", "paused"))
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}

	j, err := te.Store.GetJob(context.Background(), "test-job-paused")
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if j.Status != db.JobStatusPaused {
		t.Errorf("status: got %q, want %q", j.Status, db.JobStatusPaused)
	}
}

// TestJobSetStatus_InvalidStatus verifies that an invalid status returns an
// error message (not an error) indicating the valid options.
func TestJobSetStatus_InvalidStatus(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "test-job", "Test Job", "A test job.", db.JobStatusActive)

	result, err := te.ExecuteTool(context.Background(), jobSetStatusCall("test-job", "invalid"))
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "invalid status") {
		t.Errorf("expected result to contain 'invalid status', got %q", result)
	}
}

// TestJobSetStatus_BadJSON verifies that malformed JSON arguments return an error.
func TestJobSetStatus_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("job_set_status", "not valid json")
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parsing job_set_status args") {
		t.Errorf("expected error about parsing args, got: %v", err)
	}
}

// TestJobSetStatus_NonexistentJob verifies that setting status on a nonexistent
// job returns an error.
func TestJobSetStatus_NonexistentJob(t *testing.T) {
	te, _ := newTestExecutor(t)

	_, err := te.ExecuteTool(context.Background(), jobSetStatusCall("nonexistent-job", "done"))
	if err == nil {
		t.Fatal("expected error for nonexistent job, got nil")
	}
}

func TestJobSetStatus_NilStore(t *testing.T) {
	te := newTestExecutorNilStore(t)

	_, err := te.ExecuteTool(context.Background(), jobSetStatusCall("some-job", "done"))
	if err == nil {
		t.Fatal("expected error for nil store, got nil")
	}
	if !strings.Contains(err.Error(), "database not available") {
		t.Errorf("expected 'database not available' error, got: %v", err)
	}
}

// ============================================================================
// job_list tests
// ============================================================================

func TestJobList_EmptyWorkspace(t *testing.T) {
	te, _ := newTestExecutor(t)

	result, err := te.ExecuteTool(context.Background(), makeToolCall("job_list", map[string]any{}))
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}

	// Should return an empty JSON array.
	var items []map[string]string
	if err := json.Unmarshal([]byte(result), &items); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(items))
	}
}

func TestJobList_WithJobs(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "alpha", "Alpha Job", "First job", db.JobStatusActive)
	createTestJob(t, te.Store, "beta", "Beta Job", "Second job", db.JobStatusCompleted)

	result, err := te.ExecuteTool(context.Background(), makeToolCall("job_list", map[string]any{}))
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}

	var items []map[string]string
	if err := json.Unmarshal([]byte(result), &items); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(items))
	}

	// Verify the items contain expected fields.
	ids := map[string]bool{}
	for _, item := range items {
		ids[item["id"]] = true
		if item["name"] == "" {
			t.Errorf("expected non-empty name for job %q", item["id"])
		}
	}
	if !ids["alpha"] || !ids["beta"] {
		t.Errorf("expected jobs alpha and beta, got %v", ids)
	}
}

func TestJobList_StatusMapping(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "completed-job", "Done Job", "Completed", db.JobStatusCompleted)

	result, err := te.ExecuteTool(context.Background(), makeToolCall("job_list", map[string]any{}))
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}

	var items []map[string]string
	if err := json.Unmarshal([]byte(result), &items); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 job, got %d", len(items))
	}
	// db.JobStatusCompleted should map to "done" in tool output.
	if items[0]["status"] != "done" {
		t.Errorf("expected status 'done', got %q", items[0]["status"])
	}
}

func TestJobList_NilStore(t *testing.T) {
	te := newTestExecutorNilStore(t)

	_, err := te.ExecuteTool(context.Background(), makeToolCall("job_list", map[string]any{}))
	if err == nil {
		t.Fatal("expected error for nil store, got nil")
	}
	if !strings.Contains(err.Error(), "database not available") {
		t.Errorf("expected 'database not available' error, got: %v", err)
	}
}

// ============================================================================
// job_create tests
// ============================================================================

func TestJobCreate_Success(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("job_create", map[string]string{
		"id":          "new-job",
		"name":        "New Job",
		"description": "A brand new job.",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "new-job") {
		t.Errorf("expected result to contain job ID, got %q", result)
	}

	// Verify the job exists in SQLite with correct fields.
	ctx := context.Background()
	dbJob, err := te.Store.GetJob(ctx, "new-job")
	if err != nil {
		t.Fatalf("GetJob from SQLite failed: %v", err)
	}
	if dbJob.Title != "New Job" {
		t.Errorf("SQLite job title: got %q, want %q", dbJob.Title, "New Job")
	}
	if dbJob.Description != "A brand new job." {
		t.Errorf("SQLite job description: got %q, want %q", dbJob.Description, "A brand new job.")
	}
	if dbJob.Status != db.JobStatusPending {
		t.Errorf("SQLite job status: got %q, want %q", dbJob.Status, db.JobStatusPending)
	}
	if dbJob.Type != "general" {
		t.Errorf("SQLite job type: got %q, want %q", dbJob.Type, "general")
	}
}

func TestJobCreate_InvalidID(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("job_create", map[string]string{
		"id":          "INVALID ID!",
		"name":        "Bad Job",
		"description": "Should fail.",
	})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for invalid job ID, got nil")
	}
}

func TestJobCreate_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("job_create", "{bad json")
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parsing job_create args") {
		t.Errorf("expected error about parsing args, got: %v", err)
	}
}

func TestJobCreate_NilStore(t *testing.T) {
	te := newTestExecutorNilStore(t)

	call := makeToolCall("job_create", map[string]string{
		"id":          "nil-store-job",
		"name":        "Nil Store Job",
		"description": "Should fail without SQLite.",
	})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for nil store, got nil")
	}
	if !strings.Contains(err.Error(), "database not available") {
		t.Errorf("expected 'database not available' error, got: %v", err)
	}
}

// ============================================================================
// job_read_overview tests
// ============================================================================

func TestJobReadOverview_Success(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "read-test", "Read Test Job", "This is the description.", db.JobStatusActive)

	call := makeToolCall("job_read_overview", map[string]string{"id": "read-test"})
	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "Read Test Job") {
		t.Errorf("expected overview to contain title, got %q", result)
	}
	if !strings.Contains(result, "This is the description.") {
		t.Errorf("expected overview to contain description, got %q", result)
	}
	if !strings.Contains(result, "active") {
		t.Errorf("expected overview to contain status, got %q", result)
	}
}

func TestJobReadOverview_NonexistentJob(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("job_read_overview", map[string]string{"id": "nonexistent"})
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for nonexistent job, got nil")
	}
}

func TestJobReadOverview_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("job_read_overview", "not json")
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

func TestJobReadOverview_NilStore(t *testing.T) {
	te := newTestExecutorNilStore(t)

	call := makeToolCall("job_read_overview", map[string]string{"id": "some-job"})
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for nil store, got nil")
	}
	if !strings.Contains(err.Error(), "database not available") {
		t.Errorf("expected 'database not available' error, got: %v", err)
	}
}

// ============================================================================
// job_read_todos tests
// ============================================================================

func TestJobReadTodos_Success(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "todo-read", "Todo Read Job", "Test", db.JobStatusActive)
	createTestTask(t, te.Store, "todo-read", "First task", db.TaskStatusPending)
	createTestTask(t, te.Store, "todo-read", "Second task", db.TaskStatusCompleted)

	call := makeToolCall("job_read_todos", map[string]string{"id": "todo-read"})
	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "First task") {
		t.Errorf("expected result to contain 'First task', got %q", result)
	}
	if !strings.Contains(result, "Second task") {
		t.Errorf("expected result to contain 'Second task', got %q", result)
	}
	if !strings.Contains(result, "[ ]") {
		t.Errorf("expected result to contain '[ ]' for pending task, got %q", result)
	}
	if !strings.Contains(result, "[x]") {
		t.Errorf("expected result to contain '[x]' for completed task, got %q", result)
	}
}

func TestJobReadTodos_NonexistentJob(t *testing.T) {
	te, _ := newTestExecutor(t)

	// ListTasksForJob returns empty list for nonexistent job (not an error),
	// so this should succeed with an empty task list.
	call := makeToolCall("job_read_todos", map[string]string{"id": "nonexistent"})
	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "# Tasks") {
		t.Errorf("expected result to contain header, got %q", result)
	}
}

func TestJobReadTodos_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("job_read_todos", "bad")
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

func TestJobReadTodos_NilStore(t *testing.T) {
	te := newTestExecutorNilStore(t)

	call := makeToolCall("job_read_todos", map[string]string{"id": "some-job"})
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for nil store, got nil")
	}
	if !strings.Contains(err.Error(), "database not available") {
		t.Errorf("expected 'database not available' error, got: %v", err)
	}
}

// ============================================================================
// job_update_overview tests
// ============================================================================

func TestJobUpdateOverview_Overwrite(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "update-ow", "Update Job", "Original description", db.JobStatusActive)

	call := makeToolCall("job_update_overview", map[string]string{
		"id":      "update-ow",
		"content": "New content here",
		"mode":    "overwrite",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %q", result)
	}

	// Verify the description was overwritten in SQLite.
	j, err := te.Store.GetJob(context.Background(), "update-ow")
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if j.Description != "New content here" {
		t.Errorf("expected description 'New content here', got %q", j.Description)
	}
}

func TestJobUpdateOverview_Append(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "update-ap", "Update Job", "Original", db.JobStatusActive)

	call := makeToolCall("job_update_overview", map[string]string{
		"id":      "update-ap",
		"content": "Appended text",
		"mode":    "append",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %q", result)
	}

	// Verify the description was appended in SQLite.
	j, err := te.Store.GetJob(context.Background(), "update-ap")
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if !strings.Contains(j.Description, "Original") {
		t.Errorf("expected description to contain 'Original', got %q", j.Description)
	}
	if !strings.Contains(j.Description, "Appended text") {
		t.Errorf("expected description to contain 'Appended text', got %q", j.Description)
	}
}

func TestJobUpdateOverview_InvalidMode(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "update-bad", "Update Job", "Desc", db.JobStatusActive)

	call := makeToolCall("job_update_overview", map[string]string{
		"id":      "update-bad",
		"content": "content",
		"mode":    "delete",
	})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for invalid mode, got nil")
	}
	if !strings.Contains(err.Error(), "invalid mode") {
		t.Errorf("expected error about invalid mode, got: %v", err)
	}
}

func TestJobUpdateOverview_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("job_update_overview", "bad")
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

func TestJobUpdateOverview_NonexistentJob(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("job_update_overview", map[string]string{
		"id":      "nonexistent",
		"content": "content",
		"mode":    "overwrite",
	})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for nonexistent job, got nil")
	}
}

// ============================================================================
// job_add_todo tests
// ============================================================================

func TestJobAddTodo_Success(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "add-todo", "Add Todo Job", "Test", db.JobStatusActive)

	call := makeToolCall("job_add_todo", map[string]string{
		"id":   "add-todo",
		"task": "Write more tests",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %q", result)
	}

	// Verify the task was created in SQLite.
	tasks, err := te.Store.ListTasksForJob(context.Background(), "add-todo")
	if err != nil {
		t.Fatalf("ListTasksForJob failed: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Title != "Write more tests" {
		t.Errorf("expected task title 'Write more tests', got %q", tasks[0].Title)
	}
	if tasks[0].Status != db.TaskStatusPending {
		t.Errorf("expected task status pending, got %q", tasks[0].Status)
	}
}

func TestJobAddTodo_NonexistentJob(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("job_add_todo", map[string]string{
		"id":   "nonexistent",
		"task": "Should fail",
	})

	// CreateTask may fail with a foreign key constraint or succeed depending
	// on the store implementation. Either way, we just verify it doesn't panic.
	_, _ = te.ExecuteTool(context.Background(), call)
}

func TestJobAddTodo_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("job_add_todo", "bad")
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

func TestJobAddTodo_NilStore(t *testing.T) {
	te := newTestExecutorNilStore(t)

	call := makeToolCall("job_add_todo", map[string]string{
		"id":   "some-job",
		"task": "Should fail",
	})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for nil store, got nil")
	}
	if !strings.Contains(err.Error(), "database not available") {
		t.Errorf("expected 'database not available' error, got: %v", err)
	}
}

// ============================================================================
// job_complete_todo tests
// ============================================================================

func TestJobCompleteTodo_ByIndex(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "complete-idx", "Complete Job", "Test", db.JobStatusActive)
	createTestTask(t, te.Store, "complete-idx", "First", db.TaskStatusPending)
	task2 := createTestTask(t, te.Store, "complete-idx", "Second", db.TaskStatusPending)
	createTestTask(t, te.Store, "complete-idx", "Third", db.TaskStatusPending)

	call := makeToolCall("job_complete_todo", map[string]string{
		"id":            "complete-idx",
		"index_or_text": "2",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %q", result)
	}

	// Verify the second task was completed.
	tasks, err := te.Store.ListTasksForJob(context.Background(), "complete-idx")
	if err != nil {
		t.Fatalf("ListTasksForJob failed: %v", err)
	}
	for _, task := range tasks {
		if task.ID == task2.ID {
			if task.Status != db.TaskStatusCompleted {
				t.Errorf("second task should be completed, got %q", task.Status)
			}
		} else {
			if task.Status == db.TaskStatusCompleted {
				t.Errorf("task %q should not be completed", task.Title)
			}
		}
	}
}

func TestJobCompleteTodo_ByText(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "complete-txt", "Complete Job", "Test", db.JobStatusActive)
	createTestTask(t, te.Store, "complete-txt", "Write tests", db.TaskStatusPending)
	createTestTask(t, te.Store, "complete-txt", "Fix bugs", db.TaskStatusPending)

	call := makeToolCall("job_complete_todo", map[string]string{
		"id":            "complete-txt",
		"index_or_text": "Fix bugs",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %q", result)
	}

	// Verify "Fix bugs" was completed and "Write tests" was not.
	tasks, err := te.Store.ListTasksForJob(context.Background(), "complete-txt")
	if err != nil {
		t.Fatalf("ListTasksForJob failed: %v", err)
	}
	for _, task := range tasks {
		if task.Title == "Fix bugs" && task.Status != db.TaskStatusCompleted {
			t.Errorf("'Fix bugs' should be completed, got %q", task.Status)
		}
		if task.Title == "Write tests" && task.Status == db.TaskStatusCompleted {
			t.Error("'Write tests' should not be completed")
		}
	}
}

func TestJobCompleteTodo_NoMatch(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "complete-nomatch", "Complete Job", "Test", db.JobStatusActive)
	createTestTask(t, te.Store, "complete-nomatch", "Only task", db.TaskStatusPending)

	call := makeToolCall("job_complete_todo", map[string]string{
		"id":            "complete-nomatch",
		"index_or_text": "nonexistent task",
	})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for no matching todo, got nil")
	}
}

func TestJobCompleteTodo_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("job_complete_todo", "bad")
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

func TestJobCompleteTodo_NilStore(t *testing.T) {
	te := newTestExecutorNilStore(t)

	call := makeToolCall("job_complete_todo", map[string]string{
		"id":            "some-job",
		"index_or_text": "1",
	})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for nil store, got nil")
	}
	if !strings.Contains(err.Error(), "database not available") {
		t.Errorf("expected 'database not available' error, got: %v", err)
	}
}

// ============================================================================
// list_directory tests
// ============================================================================

func TestListDirectory_Success(t *testing.T) {
	te, configDir := newTestExecutor(t)

	// Create some files and a subdirectory under the workspace dir.
	dir := filepath.Join(configDir, "testdir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("creating dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("creating file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("creating subdir: %v", err)
	}

	call := makeToolCall("list_directory", map[string]string{"path": dir})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "file.txt") {
		t.Errorf("expected result to contain 'file.txt', got %q", result)
	}
	if !strings.Contains(result, "[file]") {
		t.Errorf("expected result to contain '[file]' prefix, got %q", result)
	}
	if !strings.Contains(result, "subdir/") {
		t.Errorf("expected result to contain 'subdir/', got %q", result)
	}
	if !strings.Contains(result, "[dir]") {
		t.Errorf("expected result to contain '[dir]' prefix, got %q", result)
	}
	if !strings.Contains(result, "5 bytes") {
		t.Errorf("expected result to contain file size '5 bytes', got %q", result)
	}
}

func TestListDirectory_NonexistentPath(t *testing.T) {
	te, configDir := newTestExecutor(t)
	call := makeToolCall("list_directory", map[string]string{"path": filepath.Join(configDir, "nonexistent", "path", "xyz")})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for nonexistent directory, got nil")
	}
}

func TestListDirectory_EmptyDirectory(t *testing.T) {
	te, configDir := newTestExecutor(t)

	dir := filepath.Join(configDir, "emptydir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("creating dir: %v", err)
	}

	call := makeToolCall("list_directory", map[string]string{"path": dir})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result for empty directory, got %q", result)
	}
}

func TestListDirectory_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)
	call := makeToolCallRaw("list_directory", "bad")

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

func TestListDirectory_RejectsPathOutsideWorkspace(t *testing.T) {
	te, _ := newTestExecutor(t)
	call := makeToolCall("list_directory", map[string]string{"path": "/etc"})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for path outside workspace, got nil")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("expected 'access denied' error, got: %v", err)
	}
}

func TestListDirectory_RejectsTraversalAttack(t *testing.T) {
	te, _ := newTestExecutor(t)
	call := makeToolCall("list_directory", map[string]string{"path": "../../../../../../etc"})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for traversal attack, got nil")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("expected 'access denied' error, got: %v", err)
	}
}

func TestListDirectory_AllowsRelativePath(t *testing.T) {
	te, configDir := newTestExecutor(t)

	// Create a subdirectory under the workspace.
	subdir := filepath.Join(configDir, "mydir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("creating subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "test.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("creating file: %v", err)
	}

	call := makeToolCall("list_directory", map[string]string{"path": "mydir"})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "test.txt") {
		t.Errorf("expected result to contain 'test.txt', got %q", result)
	}
}

// ============================================================================
// list_slots tests
// ============================================================================

func TestListSlots_NilGateway(t *testing.T) {
	te, _ := newTestExecutor(t)
	// Gateway is nil by default from newTestExecutor.

	call := makeToolCall("list_slots", map[string]any{})
	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "gateway not initialized" {
		t.Errorf("expected 'gateway not initialized', got %q", result)
	}
}

func TestListSlots_NoActiveSlots(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{slotSummaries: nil}

	call := makeToolCall("list_slots", map[string]any{})
	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "no active slots" {
		t.Errorf("expected 'no active slots', got %q", result)
	}
}

func TestListSlots_WithActiveSlots(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{
		slotSummaries: []orchestration.GatewaySlot{
			{Index: 0, Team: "coding", JobID: "job-1", Status: "running", Elapsed: "2m30s"},
			{Index: 1, Team: "testing", JobID: "job-2", Status: "running", Elapsed: "1m15s"},
		},
	}

	call := makeToolCall("list_slots", map[string]any{})
	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "slot 0: coding on job-1") {
		t.Errorf("expected slot 0 info, got %q", result)
	}
	if !strings.Contains(result, "slot 1: testing on job-2") {
		t.Errorf("expected slot 1 info, got %q", result)
	}
	if !strings.Contains(result, "2m30s") {
		t.Errorf("expected elapsed time, got %q", result)
	}
}

// ============================================================================
// kill_slot tests
// ============================================================================

func TestKillSlot_NilGateway(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("kill_slot", map[string]any{"slot_id": 0})
	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "gateway not initialized" {
		t.Errorf("expected 'gateway not initialized', got %q", result)
	}
}

func TestKillSlot_Success(t *testing.T) {
	killedSlot := -1
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{
		killFn: func(slotID int) error {
			killedSlot = slotID
			return nil
		},
	}

	call := makeToolCall("kill_slot", map[string]any{"slot_id": 2})
	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "killed slot 2" {
		t.Errorf("expected 'killed slot 2', got %q", result)
	}
	if killedSlot != 2 {
		t.Errorf("expected Kill called with slot 2, got %d", killedSlot)
	}
}

func TestKillSlot_Error(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{
		killFn: func(slotID int) error {
			return fmt.Errorf("slot %d not found", slotID)
		},
	}

	call := makeToolCall("kill_slot", map[string]any{"slot_id": 99})
	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	// kill_slot returns the error as a result string, not as an error.
	if !strings.Contains(result, "error killing slot 99") {
		t.Errorf("expected error message in result, got %q", result)
	}
}

func TestKillSlot_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{}

	call := makeToolCallRaw("kill_slot", "bad")
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

// ============================================================================
// ask_user tests
// ============================================================================

func TestAskUser_ReturnsFallbackMessage(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("ask_user", map[string]any{
		"question": "Which option?",
		"options":  []string{"A", "B", "C"},
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "ask_user was handled by the TUI" {
		t.Errorf("expected fallback message, got %q", result)
	}
}

// ============================================================================
// escalate_to_user tests
// ============================================================================

func TestEscalateToUser_Success(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("escalate_to_user", map[string]string{
		"question": "What should I do?",
		"context":  "The build is failing.",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.HasPrefix(result, "__escalate__:") {
		t.Errorf("expected result to start with '__escalate__:', got %q", result)
	}
	if !strings.Contains(result, "What should I do?") {
		t.Errorf("expected result to contain question, got %q", result)
	}
	if !strings.Contains(result, "The build is failing.") {
		t.Errorf("expected result to contain context, got %q", result)
	}
}

func TestEscalateToUser_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("escalate_to_user", "bad")
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parsing escalate_to_user args") {
		t.Errorf("expected error about parsing args, got: %v", err)
	}
}

// ============================================================================
// task_set_status tests
// ============================================================================

func TestTaskSetStatus_Success(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "task-status-job", "Task Status Job", "Testing task status", db.JobStatusActive)
	task := createTestTask(t, te.Store, "task-status-job", "Main task", db.TaskStatusPending)

	call := makeToolCall("task_set_status", map[string]string{
		"job_id":  "task-status-job",
		"task_id": task.ID,
		"status":  "done",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "status set to done") {
		t.Errorf("expected success message, got %q", result)
	}

	// Verify the task status was updated in SQLite.
	updatedTask, err := te.Store.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	if updatedTask.Status != db.TaskStatusCompleted {
		t.Errorf("task status: got %q, want %q", updatedTask.Status, db.TaskStatusCompleted)
	}
}

func TestTaskSetStatus_InvalidStatus(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "task-invalid", "Task Invalid", "Testing invalid status", db.JobStatusActive)
	task := createTestTask(t, te.Store, "task-invalid", "Main task", db.TaskStatusPending)

	call := makeToolCall("task_set_status", map[string]string{
		"job_id":  "task-invalid",
		"task_id": task.ID,
		"status":  "bogus",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "invalid status") {
		t.Errorf("expected invalid status message, got %q", result)
	}
}

func TestTaskSetStatus_TaskNotFound(t *testing.T) {
	te, _ := newTestExecutor(t)
	createTestJob(t, te.Store, "task-notfound", "Task Not Found", "Testing task not found", db.JobStatusActive)

	call := makeToolCall("task_set_status", map[string]string{
		"job_id":  "task-notfound",
		"task_id": "nonexistent-uuid",
		"status":  "done",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "not found") {
		t.Errorf("expected 'not found' message, got %q", result)
	}
}

func TestTaskSetStatus_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("task_set_status", "bad")
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

func TestTaskSetStatus_NilStore(t *testing.T) {
	te := newTestExecutorNilStore(t)

	call := makeToolCall("task_set_status", map[string]string{
		"job_id":  "some-job",
		"task_id": "some-task",
		"status":  "done",
	})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for nil store, got nil")
	}
	if !strings.Contains(err.Error(), "database not available") {
		t.Errorf("expected 'database not available' error, got: %v", err)
	}
}

// ============================================================================
// fetch_webpage tests
// ============================================================================

// withTestFetchClient temporarily replaces the operatorFetchClient with a plain
// HTTP client (no SSRF protection) for testing against httptest.NewServer, which
// binds to 127.0.0.1. Restores the original client when the test completes.
func withTestFetchClient(t *testing.T) {
	t.Helper()
	orig := operatorFetchClient
	operatorFetchClient = &http.Client{Timeout: 10 * time.Second}
	t.Cleanup(func() { operatorFetchClient = orig })
}

func TestFetchWebpage_Success(t *testing.T) {
	withTestFetchClient(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
<h1>Hello World</h1>
<p>This is a test paragraph.</p>
</body>
</html>`)
	}))
	defer srv.Close()

	te, _ := newTestExecutor(t)
	call := makeToolCall("fetch_webpage", map[string]string{"url": srv.URL})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "Hello World") {
		t.Errorf("expected result to contain 'Hello World', got %q", result)
	}
	if !strings.Contains(result, "This is a test paragraph.") {
		t.Errorf("expected result to contain paragraph text, got %q", result)
	}
}

func TestFetchWebpage_StripsScriptAndStyle(t *testing.T) {
	withTestFetchClient(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html>
<head>
<title>Test</title>
<style>body { color: red; }</style>
<script>alert('xss');</script>
</head>
<body>
<p>Visible text</p>
<script>console.log('hidden');</script>
<style>.hidden { display: none; }</style>
</body>
</html>`)
	}))
	defer srv.Close()

	te, _ := newTestExecutor(t)
	call := makeToolCall("fetch_webpage", map[string]string{"url": srv.URL})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "Visible text") {
		t.Errorf("expected result to contain 'Visible text', got %q", result)
	}
	if strings.Contains(result, "alert") {
		t.Errorf("expected script content to be stripped, got %q", result)
	}
	if strings.Contains(result, "color: red") {
		t.Errorf("expected style content to be stripped, got %q", result)
	}
	if strings.Contains(result, "console.log") {
		t.Errorf("expected inline script to be stripped, got %q", result)
	}
}

func TestFetchWebpage_NonOKStatus(t *testing.T) {
	withTestFetchClient(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	te, _ := newTestExecutor(t)
	call := makeToolCall("fetch_webpage", map[string]string{"url": srv.URL})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected status 404") {
		t.Errorf("expected error about status 404, got: %v", err)
	}
}

func TestFetchWebpage_InvalidURL(t *testing.T) {
	withTestFetchClient(t)
	te, _ := newTestExecutor(t)
	call := makeToolCall("fetch_webpage", map[string]string{"url": "http://localhost:1"})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for unreachable URL, got nil")
	}
}

func TestFetchWebpage_SSRFBlocksPrivateIP(t *testing.T) {
	// Do NOT call withTestFetchClient — use the real SSRF-protected client.
	te, _ := newTestExecutor(t)
	call := makeToolCall("fetch_webpage", map[string]string{"url": "http://127.0.0.1:9999"})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for private IP, got nil")
	}
	if !strings.Contains(err.Error(), "private/reserved IP") {
		t.Errorf("expected SSRF block error, got: %v", err)
	}
}

func TestFetchWebpage_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)
	call := makeToolCallRaw("fetch_webpage", "bad")

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

func TestFetchWebpage_CollapsesWhitespace(t *testing.T) {
	withTestFetchClient(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><body>
<p>Word1</p>


<p>Word2</p>
<p>  Word3  </p>
</body></html>`)
	}))
	defer srv.Close()

	te, _ := newTestExecutor(t)
	call := makeToolCall("fetch_webpage", map[string]string{"url": srv.URL})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	// Whitespace should be collapsed to single spaces.
	if strings.Contains(result, "  ") {
		t.Errorf("expected whitespace to be collapsed, got %q", result)
	}
	if !strings.Contains(result, "Word1") || !strings.Contains(result, "Word2") || !strings.Contains(result, "Word3") {
		t.Errorf("expected all words present, got %q", result)
	}
}

func TestFetchWebpage_TruncatesLongContent(t *testing.T) {
	withTestFetchClient(t)
	// Generate content longer than 8000 chars.
	longText := strings.Repeat("A", 9000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, "<html><body><p>%s</p></body></html>", longText)
	}))
	defer srv.Close()

	te, _ := newTestExecutor(t)
	call := makeToolCall("fetch_webpage", map[string]string{"url": srv.URL})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.HasSuffix(result, "...[truncated]") {
		t.Errorf("expected result to end with '...[truncated]', got suffix %q", result[len(result)-20:])
	}
}

// ============================================================================
// assign_team tests
// ============================================================================

func TestAssignTeam_NilGateway(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    "some-job",
		"task":      "Do something",
	})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for nil gateway, got nil")
	}
	if !strings.Contains(err.Error(), "gateway not initialized") {
		t.Errorf("expected 'gateway not initialized' error, got: %v", err)
	}
}

func TestAssignTeam_JobDoesNotExist(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{}
	te.SetTeams([]agents.Team{{Name: "coding"}})

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    "nonexistent-job",
		"task":      "Do something",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	// Should return a message (not error) telling the operator to create the job first.
	if !strings.Contains(result, "does not exist") {
		t.Errorf("expected 'does not exist' message, got %q", result)
	}
	if !strings.Contains(result, "job_create") {
		t.Errorf("expected 'job_create' hint, got %q", result)
	}
}

func TestAssignTeam_TeamNotFound(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{}
	te.SetTeams([]agents.Team{{Name: "coding"}})

	createTestJob(t, te.Store, "team-test", "Team Test", "Test", db.JobStatusActive)

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "nonexistent-team",
		"job_id":    "team-test",
		"task":      "Do something",
	})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for nonexistent team, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestAssignTeam_SuccessfulDispatch(t *testing.T) {
	te, _ := newTestExecutor(t)

	spawnCalled := false
	te.Gateway = &mockSpawner{
		spawnTeamFn: func(teamName, jobID, task string, team agents.Team) (int, bool, error) {
			spawnCalled = true
			if teamName != "coding" {
				t.Errorf("expected team 'coding', got %q", teamName)
			}
			if jobID != "dispatch-job" {
				t.Errorf("expected job 'dispatch-job', got %q", jobID)
			}
			return 1, false, nil
		},
	}
	te.SetTeams([]agents.Team{{Name: "coding"}})

	createTestJob(t, te.Store, "dispatch-job", "Dispatch Job", "Test", db.JobStatusActive)

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    "dispatch-job",
		"task":      "Implement feature X",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !spawnCalled {
		t.Error("expected SpawnTeam to be called")
	}
	if !strings.Contains(result, "started: slot 1") {
		t.Errorf("expected 'started: slot 1', got %q", result)
	}
}

func TestAssignTeam_AlreadyRunning(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{
		spawnTeamFn: func(_, _, _ string, _ agents.Team) (int, bool, error) {
			return 3, true, nil
		},
	}
	te.SetTeams([]agents.Team{{Name: "coding"}})

	createTestJob(t, te.Store, "running-job", "Running Job", "Test", db.JobStatusActive)

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    "running-job",
		"task":      "Continue work",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "already running") {
		t.Errorf("expected 'already running' message, got %q", result)
	}
	if !strings.Contains(result, "slot 3") {
		t.Errorf("expected slot 3 in result, got %q", result)
	}
}

func TestAssignTeam_SpawnError(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{
		spawnTeamFn: func(_, _, _ string, _ agents.Team) (int, bool, error) {
			return 0, false, fmt.Errorf("no available slots")
		},
	}
	te.SetTeams([]agents.Team{{Name: "coding"}})

	createTestJob(t, te.Store, "spawn-err-job", "Spawn Error Job", "Test", db.JobStatusActive)

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    "spawn-err-job",
		"task":      "Do work",
	})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error from spawn failure, got nil")
	}
	if !strings.Contains(err.Error(), "spawning team") {
		t.Errorf("expected 'spawning team' error, got: %v", err)
	}
}

func TestAssignTeam_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{}

	call := makeToolCallRaw("assign_team", "bad")
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

func TestAssignTeam_NilStore(t *testing.T) {
	te := newTestExecutorNilStore(t)
	te.Gateway = &mockSpawner{}
	te.SetTeams([]agents.Team{{Name: "coding"}})

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    "some-job",
		"task":      "Do something",
	})

	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for nil store, got nil")
	}
	if !strings.Contains(err.Error(), "database not available") {
		t.Errorf("expected 'database not available' error, got: %v", err)
	}
}

// ============================================================================
// Unknown tool tests
// ============================================================================

func TestUnknownTool_ReturnsError(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("nonexistent_tool", map[string]any{})
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for unknown tool, got nil")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected 'unknown tool' error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "nonexistent_tool") {
		t.Errorf("expected tool name in error, got: %v", err)
	}
}

// ============================================================================
// NewToolExecutor tests
// ============================================================================

func TestNewToolExecutor_SetsFields(t *testing.T) {
	spawner := &mockSpawner{}
	teams := []agents.Team{{Name: "test-team"}}

	te := NewToolExecutor(spawner, teams, "/tmp/workspace", nil, nil)

	if te.Gateway != spawner {
		t.Error("expected Gateway to be set")
	}
	if teams := te.getTeams(); len(teams) != 1 || teams[0].Name != "test-team" {
		t.Error("expected teams to be set")
	}
	if te.WorkspaceDir != "/tmp/workspace" {
		t.Errorf("expected WorkspaceDir '/tmp/workspace', got %q", te.WorkspaceDir)
	}
	if len(te.Tools) == 0 {
		t.Error("expected Tools to be populated with static tools")
	}
}

// ============================================================================
// Runtime integration tests
// ============================================================================

// --- Mock provider for runtime tests ---

type mockProvider struct {
	name string
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Models(_ context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (m *mockProvider) ChatStream(_ context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 3)
	ch <- provider.StreamEvent{Type: provider.EventText, Text: "Hello from mock"}
	ch <- provider.StreamEvent{Type: provider.EventDone}
	close(ch)
	return ch, nil
}

// ============================================================================
// Test: assign_team uses runtime when provider configured
// ============================================================================

func TestAssignTeam_UsesRuntimeWhenProviderConfigured(t *testing.T) {
	te, _ := newTestExecutor(t)

	// Set up a mock provider in a registry.
	mock := &mockProvider{name: "test-provider"}
	registry := provider.NewRegistry()
	registry.Register("test-provider", mock)

	// Create a runtime with the registry.
	rt := runtime.New(nil, registry)
	te.Runtime = rt
	te.DefaultProvider = "test-provider"
	te.DefaultModel = "test-model"

	// Track session starts via the runtime callback (the canonical notification path).
	var sessionStarted bool
	var mu sync.Mutex
	rt.OnSessionStarted = func(sess *runtime.Session) {
		mu.Lock()
		sessionStarted = true
		mu.Unlock()
	}

	// Set up a gateway mock that should NOT be called.
	gatewayCalled := false
	te.Gateway = &mockSpawner{
		spawnTeamFn: func(_, _, _ string, _ agents.Team) (int, bool, error) {
			gatewayCalled = true
			return 0, false, nil
		},
	}
	te.SetTeams([]agents.Team{{Name: "coding"}})

	// Create a job in SQLite.
	createTestJob(t, te.Store, "runtime-job", "Runtime Job", "Test", db.JobStatusActive)

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    "runtime-job",
		"task":      "Implement feature via runtime",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}

	// Verify runtime path was used.
	if !strings.Contains(result, "started runtime session") {
		t.Errorf("expected 'started runtime session' in result, got %q", result)
	}

	mu.Lock()
	started := sessionStarted
	mu.Unlock()
	if !started {
		t.Error("expected OnSessionStarted callback to be called")
	}

	// Verify gateway was NOT called.
	if gatewayCalled {
		t.Error("expected gateway NOT to be called when runtime path succeeds")
	}
}

// ============================================================================
// Test: assign_team falls back to gateway when no provider configured
// ============================================================================

func TestAssignTeam_FallsBackToGatewayWhenNoProvider(t *testing.T) {
	te, _ := newTestExecutor(t)

	// Set up runtime but leave DefaultProvider empty.
	registry := provider.NewRegistry()
	rt := runtime.New(nil, registry)
	te.Runtime = rt
	// te.DefaultProvider is "" — runtime path should be skipped.

	gatewayCalled := false
	te.Gateway = &mockSpawner{
		spawnTeamFn: func(teamName, jobID, task string, _ agents.Team) (int, bool, error) {
			gatewayCalled = true
			if teamName != "coding" {
				t.Errorf("expected team 'coding', got %q", teamName)
			}
			return 2, false, nil
		},
	}
	te.SetTeams([]agents.Team{{Name: "coding"}})

	createTestJob(t, te.Store, "gateway-fallback-job", "Gateway Fallback Job", "Test", db.JobStatusActive)

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    "gateway-fallback-job",
		"task":      "Do work via gateway",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}

	if !gatewayCalled {
		t.Error("expected gateway SpawnTeam to be called")
	}
	if !strings.Contains(result, "started: slot 2") {
		t.Errorf("expected 'started: slot 2', got %q", result)
	}
}

// ============================================================================
// Test: assign_team falls back to gateway when runtime spawn fails
// ============================================================================

func TestAssignTeam_FallsBackToGatewayOnRuntimeError(t *testing.T) {
	te, _ := newTestExecutor(t)

	// Set up runtime with a provider that doesn't exist in the registry.
	registry := provider.NewRegistry()
	// Do NOT register "nonexistent" — SpawnAgent will fail with "provider not found".
	rt := runtime.New(nil, registry)
	te.Runtime = rt
	te.DefaultProvider = "nonexistent"
	te.DefaultModel = "some-model"

	gatewayCalled := false
	te.Gateway = &mockSpawner{
		spawnTeamFn: func(_, _, _ string, _ agents.Team) (int, bool, error) {
			gatewayCalled = true
			return 5, false, nil
		},
	}
	te.SetTeams([]agents.Team{{Name: "coding"}})

	createTestJob(t, te.Store, "runtime-fail-job", "Runtime Fail Job", "Test", db.JobStatusActive)

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    "runtime-fail-job",
		"task":      "Do work with fallback",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}

	if !gatewayCalled {
		t.Error("expected gateway SpawnTeam to be called as fallback")
	}
	if !strings.Contains(result, "started: slot 5") {
		t.Errorf("expected 'started: slot 5', got %q", result)
	}
}

// ============================================================================
// list_sessions tests
// ============================================================================

func TestListSessions_NilRuntime(t *testing.T) {
	te, _ := newTestExecutor(t)
	// te.Runtime is nil by default.

	call := makeToolCall("list_sessions", map[string]any{})
	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "runtime not initialized" {
		t.Errorf("expected 'runtime not initialized', got %q", result)
	}
}

func TestListSessions_NoActiveSessions(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Runtime = runtime.New(nil, provider.NewRegistry())

	call := makeToolCall("list_sessions", map[string]any{})
	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "no active runtime sessions" {
		t.Errorf("expected 'no active runtime sessions', got %q", result)
	}
}

// ============================================================================
// cancel_session tests
// ============================================================================

func TestCancelSession_NilRuntime(t *testing.T) {
	te, _ := newTestExecutor(t)
	// te.Runtime is nil by default.

	call := makeToolCall("cancel_session", map[string]string{"session_id": "abc"})
	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "runtime not initialized" {
		t.Errorf("expected 'runtime not initialized', got %q", result)
	}
}

func TestCancelSession_NonexistentSession(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Runtime = runtime.New(nil, provider.NewRegistry())

	call := makeToolCall("cancel_session", map[string]string{"session_id": "nonexistent"})
	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "not found") {
		t.Errorf("expected 'not found' in result, got %q", result)
	}
}

func TestCancelSession_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Runtime = runtime.New(nil, provider.NewRegistry())

	call := makeToolCallRaw("cancel_session", "bad")
	_, err := te.ExecuteTool(context.Background(), call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parsing cancel_session args") {
		t.Errorf("expected error about parsing args, got: %v", err)
	}
}

// ============================================================================
// Regression test: assign_team WorkDir and prompt use workspace dir
// ============================================================================

func TestAssignTeam_RuntimePath_WorkDirIsWorkspaceDir(t *testing.T) {
	te, configDir := newTestExecutor(t)

	// Register a mock provider so SpawnAgent succeeds.
	mock := &mockProvider{name: "test-provider"}
	registry := provider.NewRegistry()
	registry.Register("test-provider", mock)

	rt := runtime.New(nil, registry)
	te.Runtime = rt
	te.DefaultProvider = "test-provider"
	te.DefaultModel = "test-model"

	// Capture the session spawned by assign_team.
	var capturedSession *runtime.Session
	var sessionMu sync.Mutex
	rt.OnSessionStarted = func(sess *runtime.Session) {
		sessionMu.Lock()
		capturedSession = sess
		sessionMu.Unlock()
	}

	// Gateway must be non-nil (assign_team checks te.Gateway == nil first),
	// but it should NOT be called when the runtime path succeeds.
	gatewayCalled := false
	te.Gateway = &mockSpawner{
		spawnTeamFn: func(_, _, _ string, _ agents.Team) (int, bool, error) {
			gatewayCalled = true
			return 0, false, nil
		},
	}
	te.SetTeams([]agents.Team{{Name: "coding"}})

	// Create the job in SQLite.
	const jobID = "workdir-regression-job"
	createTestJob(t, te.Store, jobID, "WorkDir Regression Job", "Test", db.JobStatusActive)

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    jobID,
		"task":      "Verify WorkDir is set to workspace dir",
	})

	result, err := te.ExecuteTool(context.Background(), call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}

	// Confirm the runtime path was taken (not the gateway fallback).
	if !strings.Contains(result, "started runtime session") {
		t.Errorf("expected runtime path result, got %q", result)
	}
	if gatewayCalled {
		t.Error("gateway should not be called when runtime path succeeds")
	}

	// Retrieve the captured session.
	sessionMu.Lock()
	sess := capturedSession
	sessionMu.Unlock()
	if sess == nil {
		t.Fatal("OnSessionStarted was not called — no session was captured")
	}

	// The system prompt is built with agents.BuildTeamCoordinatorPrompt(team, workspaceDir).
	// It contains the workspace dir path. Verify it's the configDir (our workspace).
	prompt := sess.SystemPrompt()
	if !strings.Contains(prompt, configDir) {
		t.Errorf("session SystemPrompt does not contain expected workspace dir %q\n"+
			"Prompt excerpt: %q",
			configDir,
			truncateForError(prompt, 200),
		)
	}
}

// truncateForError returns up to maxLen characters of s for use in error messages.
func truncateForError(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
