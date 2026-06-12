package service

import (
	"context"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
)

// Cancelling a job must leave no task in a re-dispatchable or wedged state:
// pending/blocked/in_progress all sweep to cancelled, terminal tasks are
// untouched, and the job flips to cancelled.
func TestJobCancel_SweepsNonTerminalTasks(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/cancel.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := newTestService(t)
	svc.cfg.Store = store

	ctx := context.Background()
	if err := store.CreateJob(ctx, &db.Job{ID: "job-1", Title: "J", Type: "test", Status: db.JobStatusActive}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	seed := map[string]db.TaskStatus{
		"t-pending": db.TaskStatusPending,
		"t-blocked": db.TaskStatusBlocked,
		"t-running": db.TaskStatusInProgress,
		"t-done":    db.TaskStatusCompleted,
		"t-failed":  db.TaskStatusFailed,
	}
	for id, status := range seed {
		if err := store.CreateTask(ctx, &db.Task{ID: id, JobID: "job-1", Title: id, Status: status}); err != nil {
			t.Fatalf("CreateTask(%s): %v", id, err)
		}
	}

	if err := svc.Jobs().Cancel(ctx, "job-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	job, err := store.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Status != db.JobStatusCancelled {
		t.Errorf("job status = %s, want cancelled", job.Status)
	}

	want := map[string]db.TaskStatus{
		"t-pending": db.TaskStatusCancelled,
		"t-blocked": db.TaskStatusCancelled,
		"t-running": db.TaskStatusCancelled,
		"t-done":    db.TaskStatusCompleted,
		"t-failed":  db.TaskStatusFailed,
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

	// Terminal job: a second cancel is rejected.
	if err := svc.Jobs().Cancel(ctx, "job-1"); err == nil {
		t.Error("second Cancel succeeded, want conflict error")
	}
}
