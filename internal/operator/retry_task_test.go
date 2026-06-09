package operator

import (
	"context"
	"encoding/json"
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
	if err := store.CompleteTask(ctx, taskID, db.TaskStatusFailed, "build broke", "use modernc sqlite"); err != nil {
		t.Fatalf("CompleteTask(failed): %v", err)
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
