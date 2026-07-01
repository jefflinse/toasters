package service

import (
	"context"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/graphexec"
)

// newToolchainTestService wires a LocalService against a real SQLite store,
// mirroring newRetryTestService in retry_test.go. Used by the assignment
// tests below, which need real GetTask/ListTasksForJob/AssignTaskToGraph
// behavior — a hand-rolled fake store would just re-implement the bug we're
// testing for.
func newToolchainTestService(t *testing.T) (*LocalService, *db.SQLiteStore) {
	t.Helper()
	store, err := db.Open(t.TempDir() + "/toolchain.db")
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

func seedToolchainJob(t *testing.T, store *db.SQLiteStore, jobID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateJob(ctx, &db.Job{ID: jobID, Title: "J", Type: "bug_fix", Status: db.JobStatusActive, WorkspaceDir: t.TempDir()}); err != nil {
		t.Fatalf("create job: %v", err)
	}
}

// TestAssignGraphToParent_PersistsToolchain covers the plain assign branch
// (no in-progress sibling): fine-decompose's chosen toolchain must land on
// the task's metadata AND be carried on the immediate dispatch, so both the
// initial run and any later re-dispatch (retry, serial-gate advance) see it.
func TestAssignGraphToParent_PersistsToolchain(t *testing.T) {
	svc, store := newToolchainTestService(t)
	ctx := context.Background()
	seedToolchainJob(t, store, "job-1")

	parent := &db.Task{ID: "task-1", JobID: "job-1", Title: "fix the parser", Status: db.TaskStatusPending}
	if err := store.CreateTask(ctx, parent); err != nil {
		t.Fatalf("create task: %v", err)
	}

	exec := &captureExecutor{got: make(chan graphexec.TaskRequest, 1)}
	svc.SetGraphExecutor(exec)

	svc.assignGraphToParent(ctx, parent, "bug-fix", "go", "picked bug-fix")

	// Metadata persisted on the task row.
	got, err := store.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if tc := db.ParseTaskMetadata(got.Metadata).Toolchain; tc != "go" {
		t.Errorf("persisted Toolchain = %q, want %q", tc, "go")
	}
	if got.Status != db.TaskStatusInProgress {
		t.Errorf("status = %q, want in_progress", got.Status)
	}

	// Immediate dispatch also carries it.
	select {
	case req := <-exec.got:
		if req.Toolchain != "go" {
			t.Errorf("dispatched Toolchain = %q, want %q", req.Toolchain, "go")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ExecuteTask")
	}
}

// TestAssignGraphToParent_PersistsToolchain_NoToolchain covers graphs with no
// slot-bearing roles: fine-decompose passes an empty toolchain and no
// metadata write should happen (nil, not "{}").
func TestAssignGraphToParent_PersistsToolchain_NoToolchain(t *testing.T) {
	svc, store := newToolchainTestService(t)
	ctx := context.Background()
	seedToolchainJob(t, store, "job-1")

	parent := &db.Task{ID: "task-1", JobID: "job-1", Title: "write docs", Status: db.TaskStatusPending}
	if err := store.CreateTask(ctx, parent); err != nil {
		t.Fatalf("create task: %v", err)
	}
	svc.SetGraphExecutor(&captureExecutor{got: make(chan graphexec.TaskRequest, 1)})

	svc.assignGraphToParent(ctx, parent, "docs", "", "picked docs")

	got, err := store.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(got.Metadata) != 0 {
		t.Errorf("Metadata = %q, want empty for graphs with no toolchain", got.Metadata)
	}
}

// TestAssignGraphToParent_PreAssignPersistsToolchain covers the deferred
// (serial-gate) branch: when a sibling is already in progress,
// assignGraphToParent pre-assigns the graph without dispatching, but must
// still persist the toolchain — otherwise the later dispatch that advances
// this task (assign_task from the operator's assignNextTask) has nothing to
// recover it from, reproducing issue #31.
func TestAssignGraphToParent_PreAssignPersistsToolchain(t *testing.T) {
	svc, store := newToolchainTestService(t)
	ctx := context.Background()
	seedToolchainJob(t, store, "job-1")

	parent := &db.Task{ID: "task-1", JobID: "job-1", Title: "fix the parser", Status: db.TaskStatusPending}
	if err := store.CreateTask(ctx, parent); err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	sibling := &db.Task{ID: "task-sibling", JobID: "job-1", Title: "already running", Status: db.TaskStatusInProgress, GraphID: "other-graph"}
	if err := store.CreateTask(ctx, sibling); err != nil {
		t.Fatalf("create sibling task: %v", err)
	}

	exec := &captureExecutor{got: make(chan graphexec.TaskRequest, 1)}
	svc.SetGraphExecutor(exec)

	svc.assignGraphToParent(ctx, parent, "bug-fix", "python", "picked bug-fix")

	// Pre-assigned, not dispatched: status stays pending, graph_id set.
	got, err := store.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != db.TaskStatusPending {
		t.Errorf("status = %q, want pending (deferred by serial gate)", got.Status)
	}
	if got.GraphID != "bug-fix" {
		t.Errorf("GraphID = %q, want bug-fix", got.GraphID)
	}
	if tc := db.ParseTaskMetadata(got.Metadata).Toolchain; tc != "python" {
		t.Errorf("persisted Toolchain = %q, want %q (lost on pre-assign)", tc, "python")
	}
	select {
	case req := <-exec.got:
		t.Fatalf("executor should not have fired while sibling in progress, got %+v", req)
	default:
	}
}

// TestRedispatchTaskGraph_PropagatesToolchainFromMetadata covers the
// user-facing RetryTask path (service.Jobs().RetryTask, distinct from the
// operator's retry_task tool): redispatchTaskGraph rebuilds the TaskRequest
// from the task row and must recover the toolchain fine-decompose persisted,
// not just the graph id.
func TestRedispatchTaskGraph_PropagatesToolchainFromMetadata(t *testing.T) {
	svc, store := newToolchainTestService(t)
	ctx := context.Background()
	seedToolchainJob(t, store, "job-1")

	task := &db.Task{ID: "task-1", JobID: "job-1", Title: "fix it", Status: db.TaskStatusFailed, GraphID: "bug-fix"}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	meta, err := db.MarshalTaskMetadata(db.TaskMetadata{Toolchain: "typescript"})
	if err != nil {
		t.Fatalf("MarshalTaskMetadata: %v", err)
	}
	if err := store.SetTaskMetadata(ctx, "task-1", meta); err != nil {
		t.Fatalf("SetTaskMetadata: %v", err)
	}

	exec := &captureExecutor{got: make(chan graphexec.TaskRequest, 1)}
	svc.SetGraphExecutor(exec)

	if err := svc.Jobs().RetryTask(ctx, "task-1"); err != nil {
		t.Fatalf("RetryTask: %v", err)
	}

	select {
	case req := <-exec.got:
		if req.Toolchain != "typescript" {
			t.Errorf("dispatched Toolchain = %q, want %q (lost on retry re-dispatch)", req.Toolchain, "typescript")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ExecuteTask")
	}
}
