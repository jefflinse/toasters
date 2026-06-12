package graphexec

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/provider"
)

// seedInProgressTask creates a job with one task already transitioned to
// in_progress — the state every ExecuteTask caller establishes before
// dispatching.
func seedInProgressTask(t *testing.T, store db.Store) (jobID, taskID string) {
	t.Helper()
	ctx := context.Background()
	jobID, taskID = "job-resilience", "task-resilience"
	if err := store.CreateJob(ctx, &db.Job{ID: jobID, Title: "j", Type: "test", Status: db.JobStatusActive}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := store.CreateTask(ctx, &db.Task{ID: taskID, JobID: jobID, Title: "t", Status: db.TaskStatusInProgress}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return jobID, taskID
}

func buildResilienceExecutor(t *testing.T) (*Executor, *mockEventSink, db.Store) {
	t.Helper()
	reg := provider.NewRegistry()
	reg.Register("mock", &mockProvider{})
	store := openTestStoreForDispatch(t)
	sink := &mockEventSink{}
	exec := NewExecutor(ExecutorConfig{
		Registry:     reg,
		Store:        store,
		EventSink:    sink,
		Graphs:       mapGraphSource{}, // no graphs loaded
		DefaultModel: "test-model",
	})
	return exec, sink, store
}

// A dispatch error before the graph runs must mark the task failed and
// notify the operator — callers run ExecuteTask in detached goroutines that
// only log the error, and the task is already in_progress.
func TestExecuteTask_DispatchFailureMarksTaskFailed(t *testing.T) {
	exec, sink, store := buildResilienceExecutor(t)
	jobID, taskID := seedInProgressTask(t, store)

	err := exec.ExecuteTask(context.Background(), TaskRequest{
		JobID:        jobID,
		TaskID:       taskID,
		GraphID:      "no-such-graph",
		ProviderName: "mock",
	})
	if err == nil {
		t.Fatal("expected error for unknown graph, got nil")
	}

	task, getErr := store.GetTask(context.Background(), taskID)
	if getErr != nil {
		t.Fatalf("GetTask: %v", getErr)
	}
	if task.Status != db.TaskStatusFailed {
		t.Errorf("task status = %s, want failed", task.Status)
	}
	if !strings.Contains(task.Summary, "Dispatch failed") {
		t.Errorf("task summary = %q, want a dispatch-failure summary", task.Summary)
	}

	var sawGraphFailed, sawTaskFailed bool
	for _, ev := range sink.snapshot() {
		if strings.HasPrefix(ev, "graph_failed:") {
			sawGraphFailed = true
		}
		if strings.HasPrefix(ev, "task_failed:no-such-graph:") {
			sawTaskFailed = true
		}
	}
	if !sawGraphFailed {
		t.Error("expected a graph_failed broadcast")
	}
	if !sawTaskFailed {
		t.Error("expected a task_failed broadcast so the operator can react")
	}
}

func TestExecuteTask_UnknownProviderMarksTaskFailed(t *testing.T) {
	exec, _, store := buildResilienceExecutor(t)
	jobID, taskID := seedInProgressTask(t, store)

	err := exec.ExecuteTask(context.Background(), TaskRequest{
		JobID:        jobID,
		TaskID:       taskID,
		GraphID:      "any",
		ProviderName: "no-such-provider",
	})
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}

	task, getErr := store.GetTask(context.Background(), taskID)
	if getErr != nil {
		t.Fatalf("GetTask: %v", getErr)
	}
	if task.Status != db.TaskStatusFailed {
		t.Errorf("task status = %s, want failed", task.Status)
	}
}

// After Drain, new dispatches are refused and still fail the task so it
// doesn't strand in_progress across the restart.
func TestDrain_RejectsNewDispatches(t *testing.T) {
	exec, _, store := buildResilienceExecutor(t)

	if !exec.Drain(time.Second) {
		t.Fatal("Drain with no in-flight tasks should return true immediately")
	}

	jobID, taskID := seedInProgressTask(t, store)
	err := exec.ExecuteTask(context.Background(), TaskRequest{
		JobID:        jobID,
		TaskID:       taskID,
		GraphID:      "any",
		ProviderName: "mock",
	})
	if err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("ExecuteTask after Drain = %v, want shutting-down error", err)
	}

	task, getErr := store.GetTask(context.Background(), taskID)
	if getErr != nil {
		t.Fatalf("GetTask: %v", getErr)
	}
	if task.Status != db.TaskStatusFailed {
		t.Errorf("task status = %s, want failed", task.Status)
	}
}

func TestCancelJobRegistry(t *testing.T) {
	exec, _, _ := buildResilienceExecutor(t)

	runCtx, err := exec.beginTask(context.Background(), TaskRequest{JobID: "j1", TaskID: "t1"})
	if err != nil {
		t.Fatalf("beginTask: %v", err)
	}

	if n := exec.CancelJob("other-job"); n != 0 {
		t.Errorf("CancelJob(other-job) = %d, want 0", n)
	}
	select {
	case <-runCtx.Done():
		t.Fatal("run context cancelled by an unrelated job's cancellation")
	default:
	}

	if n := exec.CancelJob("j1"); n != 1 {
		t.Errorf("CancelJob(j1) = %d, want 1", n)
	}
	select {
	case <-runCtx.Done():
	default:
		t.Error("run context not cancelled by CancelJob")
	}

	exec.endTask("t1")
	if n := exec.CancelJob("j1"); n != 0 {
		t.Errorf("CancelJob after endTask = %d, want 0", n)
	}
}

// A run that dies because its context was cancelled is a deliberate stop:
// the task persists as cancelled (not failed) and the operator is NOT sent
// task_failed — that would prompt it to retry work the user just cancelled.
func TestExecuteTask_CancelledRunMarksTaskCancelled(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Register("mock", &mockProvider{})
	store := openTestStoreForDispatch(t)
	sink := &mockEventSink{}
	exec := NewExecutor(ExecutorConfig{
		Registry:     reg,
		Store:        store,
		EventSink:    sink,
		Graphs:       mapGraphSource{"bug-fix": bugFixDef()},
		PromptEngine: testEngine(t),
		DefaultModel: "test-model",
	})

	jobID, taskID := seedInProgressTask(t, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // job cancelled before/while the run executes

	err := exec.ExecuteTask(ctx, TaskRequest{
		JobID:        jobID,
		TaskID:       taskID,
		GraphID:      "bug-fix",
		WorkspaceDir: t.TempDir(),
		ProviderName: "mock",
	})
	if err == nil {
		t.Fatal("expected error from cancelled run")
	}

	task, getErr := store.GetTask(context.Background(), taskID)
	if getErr != nil {
		t.Fatalf("GetTask: %v", getErr)
	}
	if task.Status != db.TaskStatusCancelled {
		t.Errorf("task status = %s, want cancelled", task.Status)
	}

	for _, ev := range sink.snapshot() {
		if strings.HasPrefix(ev, "task_failed:") {
			t.Errorf("cancelled run broadcast task_failed (%s) — operator would retry cancelled work", ev)
		}
	}
}
