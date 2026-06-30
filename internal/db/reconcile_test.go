package db

import (
	"context"
	"testing"
)

func TestReconcileInterrupted(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	job := &Job{ID: "j-1", Title: "Test", Type: "test", Status: JobStatusActive}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	seedTasks := map[string]TaskStatus{
		"t-running": TaskStatusInProgress,
		"t-pending": TaskStatusPending,
		"t-done":    TaskStatusCompleted,
		"t-failed":  TaskStatusFailed,
	}
	for id, status := range seedTasks {
		if err := store.CreateTask(ctx, &Task{ID: id, JobID: "j-1", Title: id, Status: status}); err != nil {
			t.Fatalf("CreateTask(%s): %v", id, err)
		}
	}

	seedSessions := map[string]SessionStatus{
		"s-active": SessionStatusActive,
		"s-done":   SessionStatusCompleted,
	}
	for id, status := range seedSessions {
		sess := &WorkerSession{ID: id, WorkerID: "w-1", JobID: "j-1", TaskID: "t-running", Status: status}
		if err := store.CreateSession(ctx, sess); err != nil {
			t.Fatalf("CreateSession(%s): %v", id, err)
		}
	}

	sessions, tasks, err := store.ReconcileInterrupted(ctx)
	if err != nil {
		t.Fatalf("ReconcileInterrupted: %v", err)
	}
	if sessions != 1 {
		t.Errorf("reconciled sessions = %d, want 1", sessions)
	}
	if tasks != 1 {
		t.Errorf("reconciled tasks = %d, want 1", tasks)
	}

	// The in-progress task is failed with an explanatory summary; all other
	// statuses are untouched. The in-progress task is reset to pending (not
	// failed) so the operator's recovery sweep can re-dispatch it.
	want := map[string]TaskStatus{
		"t-running": TaskStatusPending,
		"t-pending": TaskStatusPending,
		"t-done":    TaskStatusCompleted,
		"t-failed":  TaskStatusFailed,
	}
	for id, ws := range want {
		task, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("GetTask(%s): %v", id, err)
		}
		if task.Status != ws {
			t.Errorf("task %s status = %s, want %s", id, task.Status, ws)
		}
	}
	running, err := store.GetTask(ctx, "t-running")
	if err != nil {
		t.Fatalf("GetTask(t-running): %v", err)
	}
	if running.Summary != "" {
		t.Errorf("requeued task summary = %q, want it cleared", running.Summary)
	}

	active, err := store.GetActiveSessions(ctx)
	if err != nil {
		t.Fatalf("GetActiveSessions: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("active sessions after reconcile = %d, want 0", len(active))
	}

	// Idempotent: a second sweep finds nothing.
	sessions, tasks, err = store.ReconcileInterrupted(ctx)
	if err != nil {
		t.Fatalf("second ReconcileInterrupted: %v", err)
	}
	if sessions != 0 || tasks != 0 {
		t.Errorf("second sweep = (%d sessions, %d tasks), want (0, 0)", sessions, tasks)
	}
}

func TestReconcileInterrupted_EmptyDB(t *testing.T) {
	store := openTestStore(t)
	sessions, tasks, err := store.ReconcileInterrupted(context.Background())
	if err != nil {
		t.Fatalf("ReconcileInterrupted on empty db: %v", err)
	}
	if sessions != 0 || tasks != 0 {
		t.Errorf("empty db sweep = (%d, %d), want (0, 0)", sessions, tasks)
	}
}
