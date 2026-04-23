package operator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/graphexec"
)

// newTestSystemToolsWithCatalog wires a SystemTools exactly like
// newTestSystemTools but also attaches a GraphCatalog so the graph-dispatch
// path has something to validate against. Returns the SystemTools, its
// real SQLite store, the mock graph executor, and the workspace dir.
func newTestSystemToolsWithCatalog(t *testing.T, graphs []*graphexec.Definition) (*SystemTools, db.Store, *mockGraphExecutor, string) {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	gExec := newMockGraphExecutor()
	workDir := t.TempDir()
	t.Setenv("HOME", workDir)

	st := NewSystemTools(SystemToolsConfig{
		Store:           store,
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
		EventCh:         make(chan Event, 64),
		WorkDir:         workDir,
		GraphExecutor:   gExec,
		GraphCatalog:    stubCatalog{graphs: graphs},
	})
	return st, store, gExec, workDir
}

// seedGraphJob creates a job + pending task, returning the task id. Used by
// graph-dispatch tests that don't care about team scaffolding.
func seedGraphJob(t *testing.T, ctx context.Context, store db.Store, workDir string) (jobID, taskID string) {
	t.Helper()
	jobID = "j-" + t.Name()
	taskID = "t-" + t.Name()
	jobDir := filepath.Join(workDir, jobID)
	// Create the directory so EvalSymlinks resolves it — otherwise the
	// home-directory check compares symlink-unresolved /var/... to
	// resolved /private/var/... and fails on macOS.
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatalf("mkdir jobDir: %v", err)
	}
	if err := store.CreateJob(ctx, &db.Job{
		ID:           jobID,
		Title:        "test job",
		Description:  "desc",
		Status:       db.JobStatusPending,
		WorkspaceDir: jobDir,
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := store.CreateTask(ctx, &db.Task{
		ID:     taskID,
		JobID:  jobID,
		Title:  "do the thing",
		Status: db.TaskStatusPending,
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return jobID, taskID
}

func TestAssignTask_GraphDispatch_HappyPath(t *testing.T) {
	ctx := context.Background()
	st, store, gExec, workDir := newTestSystemToolsWithCatalog(t, []*graphexec.Definition{
		{ID: "bug-fix", Name: "Bug Fix"},
	})
	_, taskID := seedGraphJob(t, ctx, store, workDir)

	args, _ := json.Marshal(map[string]string{"task_id": taskID, "graph_id": "bug-fix"})
	result, err := st.Execute(ctx, "assign_task", args)
	if err != nil {
		t.Fatalf("assign_task: %v", err)
	}

	gExec.waitForGraphCall(t)
	calls := gExec.getCalls()
	if len(calls) != 1 {
		t.Fatalf("ExecuteTask called %d times, want 1", len(calls))
	}
	if calls[0].GraphID != "bug-fix" {
		t.Errorf("GraphID = %q, want bug-fix", calls[0].GraphID)
	}
	if calls[0].TeamID != "" {
		t.Errorf("TeamID = %q, want empty (graph dispatch)", calls[0].TeamID)
	}

	// Task should be in_progress with graph_id set.
	task, err := store.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != db.TaskStatusInProgress {
		t.Errorf("task status = %q, want in_progress", task.Status)
	}
	if task.GraphID != "bug-fix" {
		t.Errorf("task.GraphID = %q, want bug-fix", task.GraphID)
	}

	if !strings.Contains(result, "bug-fix") {
		t.Errorf("result should mention the graph id; got %q", result)
	}
}

func TestAssignTask_GraphDispatch_RejectsUnknownGraph(t *testing.T) {
	ctx := context.Background()
	st, store, gExec, workDir := newTestSystemToolsWithCatalog(t, []*graphexec.Definition{
		{ID: "bug-fix"},
	})
	_, taskID := seedGraphJob(t, ctx, store, workDir)

	args, _ := json.Marshal(map[string]string{"task_id": taskID, "graph_id": "does-not-exist"})
	_, err := st.Execute(ctx, "assign_task", args)
	if err == nil {
		t.Fatal("expected error for unknown graph")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("err = %v, want to name the missing graph", err)
	}
	if len(gExec.getCalls()) != 0 {
		t.Error("executor should not have fired for unknown graph")
	}
}

func TestAssignTask_RejectsBothTeamAndGraph(t *testing.T) {
	ctx := context.Background()
	st, store, _, workDir := newTestSystemToolsWithCatalog(t, nil)
	_, taskID := seedGraphJob(t, ctx, store, workDir)

	args, _ := json.Marshal(map[string]string{
		"task_id":  taskID,
		"team_id":  "some-team",
		"graph_id": "some-graph",
	})
	_, err := st.Execute(ctx, "assign_task", args)
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("err = %v, want 'not both'", err)
	}
}

func TestAssignTask_RejectsMissingAssignmentTarget(t *testing.T) {
	ctx := context.Background()
	st, store, _, workDir := newTestSystemToolsWithCatalog(t, nil)
	_, taskID := seedGraphJob(t, ctx, store, workDir)

	args, _ := json.Marshal(map[string]string{"task_id": taskID})
	_, err := st.Execute(ctx, "assign_task", args)
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("err = %v, want a required-field error", err)
	}
}

func TestAssignTask_GraphDispatch_DeferredWhenSiblingInProgress(t *testing.T) {
	ctx := context.Background()
	st, store, gExec, workDir := newTestSystemToolsWithCatalog(t, []*graphexec.Definition{
		{ID: "bug-fix"},
	})
	jobID, taskID := seedGraphJob(t, ctx, store, workDir)

	// Seed an in-progress sibling task.
	sibling := &db.Task{
		ID:     "t-sibling",
		JobID:  jobID,
		Title:  "already running",
		Status: db.TaskStatusInProgress,
	}
	if err := store.CreateTask(ctx, sibling); err != nil {
		t.Fatalf("CreateTask sibling: %v", err)
	}

	args, _ := json.Marshal(map[string]string{"task_id": taskID, "graph_id": "bug-fix"})
	result, err := st.Execute(ctx, "assign_task", args)
	if err != nil {
		t.Fatalf("assign_task: %v", err)
	}
	if !strings.Contains(result, "queued") {
		t.Errorf("result should indicate queued state; got %q", result)
	}
	if len(gExec.getCalls()) != 0 {
		t.Error("executor should NOT have fired while sibling is in_progress")
	}

	// Task should still be pending but graph_id pre-assigned.
	task, err := store.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != db.TaskStatusPending {
		t.Errorf("task.Status = %q, want pending", task.Status)
	}
	if task.GraphID != "bug-fix" {
		t.Errorf("task.GraphID = %q, want bug-fix", task.GraphID)
	}
}

func TestCreateTask_RejectsBothTeamAndGraph(t *testing.T) {
	ctx := context.Background()
	st, store, _, workDir := newTestSystemToolsWithCatalog(t, nil)

	jobID := "j-create-reject"
	jobDir := filepath.Join(workDir, jobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatalf("mkdir jobDir: %v", err)
	}
	if err := store.CreateJob(ctx, &db.Job{ID: jobID, Title: "t", Description: "d", Status: db.JobStatusPending, WorkspaceDir: jobDir}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	args, _ := json.Marshal(map[string]string{
		"job_id":   jobID,
		"title":    "work",
		"team_id":  "some-team",
		"graph_id": "some-graph",
	})
	_, err := st.Execute(ctx, "create_task", args)
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("err = %v, want 'not both'", err)
	}
}

func TestCreateTask_PersistsGraphID(t *testing.T) {
	ctx := context.Background()
	st, store, _, workDir := newTestSystemToolsWithCatalog(t, nil)

	jobID := "j-create-graph"
	jobDir := filepath.Join(workDir, jobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatalf("mkdir jobDir: %v", err)
	}
	if err := store.CreateJob(ctx, &db.Job{ID: jobID, Title: "t", Description: "d", Status: db.JobStatusPending, WorkspaceDir: jobDir}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	args, _ := json.Marshal(map[string]string{
		"job_id":   jobID,
		"title":    "work",
		"graph_id": "bug-fix",
	})
	result, err := st.Execute(ctx, "create_task", args)
	if err != nil {
		t.Fatalf("create_task: %v", err)
	}

	var body struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(result), &body); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	task, err := store.GetTask(ctx, body.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.GraphID != "bug-fix" {
		t.Errorf("task.GraphID = %q, want bug-fix", task.GraphID)
	}
	if task.TeamID != "" {
		t.Errorf("task.TeamID = %q, want empty", task.TeamID)
	}
}
