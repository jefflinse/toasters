package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/graphexec"
)

// captureExecutor records the TaskRequest passed to ExecuteTask and signals
// when it has been entered, so a detached dispatch goroutine can be awaited.
type captureExecutor struct {
	got   chan graphexec.TaskRequest
	errOf error
}

func (c *captureExecutor) ExecuteTask(_ context.Context, req graphexec.TaskRequest) error {
	c.got <- req
	return c.errOf
}

func newRetryTestService(t *testing.T) (*LocalService, *db.SQLiteStore) {
	t.Helper()
	store, err := db.Open(t.TempDir() + "/retry.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := newTestService(t)
	svc.cfg.Store = store
	svc.opMu.Lock()
	svc.defaultProvider = "lmstudio"
	svc.defaultModel = "qwen3"
	svc.opMu.Unlock()
	return svc, store
}

func seedFailedTask(t *testing.T, store *db.SQLiteStore) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateJob(ctx, &db.Job{ID: "job-1", Title: "J", Type: "bug_fix", Status: db.JobStatusActive, WorkspaceDir: t.TempDir()}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := store.CreateTask(ctx, &db.Task{ID: "task-1", JobID: "job-1", Title: "fix it", Status: db.TaskStatusFailed, GraphID: "bug-fix"}); err != nil {
		t.Fatalf("create task: %v", err)
	}
}

func TestRetryTask_DispatchesBoundGraph(t *testing.T) {
	svc, store := newRetryTestService(t)
	exec := &captureExecutor{got: make(chan graphexec.TaskRequest, 1)}
	svc.SetGraphExecutor(exec)
	seedFailedTask(t, store)

	if err := svc.Jobs().RetryTask(context.Background(), "task-1"); err != nil {
		t.Fatalf("RetryTask: %v", err)
	}

	// Task should be back in progress with its result fields cleared.
	got, err := store.GetTask(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != db.TaskStatusInProgress {
		t.Errorf("status = %q, want in_progress", got.Status)
	}

	// The bound graph should have been dispatched.
	select {
	case req := <-exec.got:
		if req.TaskID != "task-1" || req.GraphID != "bug-fix" {
			t.Errorf("dispatched %+v, want task-1 / bug-fix", req)
		}
		if req.ProviderName != "lmstudio" || req.Model != "qwen3" {
			t.Errorf("dispatched provider/model = %q/%q, want lmstudio/qwen3", req.ProviderName, req.Model)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ExecuteTask")
	}
}

func TestRetryTask_Errors(t *testing.T) {
	svc, store := newRetryTestService(t)
	svc.SetGraphExecutor(&captureExecutor{got: make(chan graphexec.TaskRequest, 1)})
	ctx := context.Background()

	// Missing task -> ErrNotFound.
	if err := svc.Jobs().RetryTask(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing task: got %v, want ErrNotFound", err)
	}

	// Non-failed task cannot be retried.
	if err := store.CreateJob(ctx, &db.Job{ID: "job-1", Title: "J", Type: "t", Status: db.JobStatusActive}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := store.CreateTask(ctx, &db.Task{ID: "task-ok", JobID: "job-1", Title: "ok", Status: db.TaskStatusCompleted, GraphID: "g"}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := svc.Jobs().RetryTask(ctx, "task-ok"); err == nil {
		t.Error("expected error retrying completed task")
	}

	// Failed task with no graph binding cannot be retried.
	if err := store.CreateTask(ctx, &db.Task{ID: "task-nograph", JobID: "job-1", Title: "x", Status: db.TaskStatusFailed}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := svc.Jobs().RetryTask(ctx, "task-nograph"); err == nil {
		t.Error("expected error retrying task with no graph")
	}
}

func TestBroadcastTaskFailed_DecompositionMarksJobFailed(t *testing.T) {
	svc, store := newRetryTestService(t)
	ctx := context.Background()
	if err := store.CreateJob(ctx, &db.Job{ID: "job-d", Title: "J", Type: "t", Status: db.JobStatusActive}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	// A failed coarse-decompose bootstrap (no operator wired) must still flip
	// the job to failed rather than leaving it stranded at running.
	svc.BroadcastTaskFailed("job-d", "bootstrap-task", graphCoarseDecompose, "node crashed")

	got, err := store.GetJob(ctx, "job-d")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != db.JobStatusFailed {
		t.Errorf("job status = %q, want failed", got.Status)
	}
}

func TestBroadcastTaskFailed_NonDecompositionLeavesJobStatus(t *testing.T) {
	svc, store := newRetryTestService(t)
	ctx := context.Background()
	if err := store.CreateJob(ctx, &db.Job{ID: "job-n", Title: "J", Type: "t", Status: db.JobStatusActive}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	// A regular (non-decomposition) task failure must not touch job status here;
	// the operator's blocker-handler owns that decision.
	svc.BroadcastTaskFailed("job-n", "task-1", "bug-fix", "boom")

	got, err := store.GetJob(ctx, "job-n")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != db.JobStatusActive {
		t.Errorf("job status = %q, want active (unchanged)", got.Status)
	}
}
