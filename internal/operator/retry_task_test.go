package operator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/graphexec"
)

// failTask drives a seeded pending task into a failed state on the given graph,
// so retry_task has a real precondition to act on.
func failTask(t *testing.T, ctx context.Context, store db.Store, taskID, graphID string) {
	t.Helper()
	if err := store.AssignTaskToGraph(ctx, taskID, graphID); err != nil {
		t.Fatalf("AssignTaskToGraph: %v", err)
	}
	if err := store.UpdateTaskStatus(ctx, taskID, db.TaskStatusFailed, "build broke"); err != nil {
		t.Fatalf("UpdateTaskStatus(failed): %v", err)
	}
	if err := store.UpdateTaskResult(ctx, taskID, "build broke", "use modernc sqlite"); err != nil {
		t.Fatalf("UpdateTaskResult: %v", err)
	}
}

func TestRetryTask_RerunsFailedTaskInPlace(t *testing.T) {
	ctx := context.Background()
	st, store, gExec, workDir := newTestSystemToolsWithCatalog(t, []*graphexec.Definition{
		{ID: "bug-fix", Name: "Bug Fix"},
	})
	_, taskID := seedGraphJob(t, ctx, store, workDir)
	failTask(t, ctx, store, taskID, "bug-fix")

	// No graph_id → defaults to the graph it failed on.
	args, _ := json.Marshal(map[string]string{"task_id": taskID})
	if _, err := st.Execute(ctx, "retry_task", args); err != nil {
		t.Fatalf("retry_task: %v", err)
	}

	gExec.waitForGraphCall(t)
	calls := gExec.getCalls()
	if len(calls) != 1 {
		t.Fatalf("ExecuteTask called %d times, want 1", len(calls))
	}
	if calls[0].TaskID != taskID || calls[0].GraphID != "bug-fix" {
		t.Errorf("dispatched %+v, want task %q on bug-fix", calls[0], taskID)
	}

	task, err := store.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != db.TaskStatusInProgress {
		t.Errorf("status = %s, want in_progress", task.Status)
	}
	if task.Summary != "" || task.Recommendations != "" {
		t.Errorf("prior failure not cleared: summary=%q recommendations=%q", task.Summary, task.Recommendations)
	}
}

// TestRetryTask_PropagatesToolchainFromMetadata reproduces issue #31: a task
// dispatched by fine-decompose with a toolchain (e.g. "go") must not lose it
// on retry_task, or slot resolution for `slots: { toolchain: "{{
// task.toolchain }}" }` hard-errors with "has no value in task data" —
// retryTask rebuilds the TaskRequest from the task row, and the toolchain is
// otherwise only ever carried by the original dispatch call, never the row.
func TestRetryTask_PropagatesToolchainFromMetadata(t *testing.T) {
	ctx := context.Background()
	st, store, gExec, workDir := newTestSystemToolsWithCatalog(t, []*graphexec.Definition{
		{ID: "bug-fix", Name: "Bug Fix"},
	})
	_, taskID := seedGraphJob(t, ctx, store, workDir)
	failTask(t, ctx, store, taskID, "bug-fix")

	meta, err := db.MarshalTaskMetadata(db.TaskMetadata{Toolchain: "go"})
	if err != nil {
		t.Fatalf("MarshalTaskMetadata: %v", err)
	}
	if err := store.SetTaskMetadata(ctx, taskID, meta); err != nil {
		t.Fatalf("SetTaskMetadata: %v", err)
	}

	args, _ := json.Marshal(map[string]string{"task_id": taskID})
	if _, err := st.Execute(ctx, "retry_task", args); err != nil {
		t.Fatalf("retry_task: %v", err)
	}

	gExec.waitForGraphCall(t)
	calls := gExec.getCalls()
	if len(calls) != 1 {
		t.Fatalf("ExecuteTask called %d times, want 1", len(calls))
	}
	if calls[0].Toolchain != "go" {
		t.Errorf("dispatched Toolchain = %q, want %q (lost on retry)", calls[0].Toolchain, "go")
	}
}

func TestRetryTask_RejectsNonFailedTask(t *testing.T) {
	ctx := context.Background()
	st, store, gExec, workDir := newTestSystemToolsWithCatalog(t, []*graphexec.Definition{
		{ID: "bug-fix"},
	})
	_, taskID := seedGraphJob(t, ctx, store, workDir) // seeded pending

	args, _ := json.Marshal(map[string]string{"task_id": taskID, "graph_id": "bug-fix"})
	if _, err := st.Execute(ctx, "retry_task", args); err == nil {
		t.Fatal("expected an error retrying a non-failed task")
	}
	if n := len(gExec.getCalls()); n != 0 {
		t.Errorf("dispatched %d times, want 0 for a non-failed task", n)
	}
}

// TestRetryTask_ExposedToOperator verifies the operator LLM can see and route
// the retry_task tool (not just the system layer).
func TestRetryTask_ExposedToOperator(t *testing.T) {
	ot := newTestOperatorTools(t)
	found := false
	for _, d := range ot.Definitions() {
		if d.Name == "retry_task" {
			found = true
			break
		}
	}
	if !found {
		t.Error("retry_task missing from operator tool definitions")
	}
}

// retry_task must respect the same serial-execution gate as assign_task —
// retrying while a sibling runs would put two graph executions in the same
// job workspace concurrently.
func TestRetryTask_DefersWhileSiblingInProgress(t *testing.T) {
	ctx := context.Background()
	st, store, gExec, workDir := newTestSystemToolsWithCatalog(t, []*graphexec.Definition{
		{ID: "bug-fix", Name: "Bug Fix"},
	})
	jobID, taskID := seedGraphJob(t, ctx, store, workDir)
	failTask(t, ctx, store, taskID, "bug-fix")

	if err := store.CreateTask(ctx, &db.Task{
		ID: "t-sibling", JobID: jobID, Title: "active sibling", Status: db.TaskStatusInProgress,
	}); err != nil {
		t.Fatalf("CreateTask(sibling): %v", err)
	}

	args, _ := json.Marshal(map[string]string{"task_id": taskID})
	out, err := st.Execute(ctx, "retry_task", args)
	if err != nil {
		t.Fatalf("retry_task should defer with a message, not error: %v", err)
	}
	if !strings.Contains(out, "Cannot retry") {
		t.Errorf("output = %q, want a serial-execution deferral message", out)
	}

	if calls := gExec.getCalls(); len(calls) != 0 {
		t.Errorf("ExecuteTask dispatched %d times during deferral, want 0", len(calls))
	}
	task, err := store.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != db.TaskStatusFailed {
		t.Errorf("task status = %s, want still failed (retryable later)", task.Status)
	}
}
