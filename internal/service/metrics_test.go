package service

import (
	"context"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
)

// Metrics with no store configured returns a zero-value report rather than
// an error — tests and standalone TUI previews shouldn't need a database
// wired up just to call it.
func TestMetrics_NoStore(t *testing.T) {
	svc := newTestService(t)

	report, err := svc.Metrics(context.Background())
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if len(report.Nodes) != 0 || len(report.Sessions) != 0 {
		t.Errorf("report = %+v, want empty", report)
	}
}

// Metrics aggregates node_executions and worker_sessions into the service
// DTOs, computing failure rates and excluding usage-unavailable sessions
// from the token/context averages.
func TestMetrics_HappyPath(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/metrics.db")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	if err := store.CreateJob(ctx, &db.Job{ID: "job-1", Title: "j", Type: "test", Status: db.JobStatusActive}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := store.CreateTask(ctx, &db.Task{ID: "task-1", JobID: "job-1", Title: "t", Status: db.TaskStatusInProgress}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Two "implement" executions: one completed, one failed.
	if err := store.InsertNodeExecution(ctx, &db.NodeExecution{
		ID: "e1", JobID: "job-1", TaskID: "task-1", Node: "implement", Status: "completed", ElapsedMS: 1000,
	}); err != nil {
		t.Fatalf("InsertNodeExecution: %v", err)
	}
	if err := store.InsertNodeExecution(ctx, &db.NodeExecution{
		ID: "e2", JobID: "job-1", TaskID: "task-1", Node: "implement", Status: "failed", ElapsedMS: 2000,
	}); err != nil {
		t.Fatalf("InsertNodeExecution: %v", err)
	}

	// Two "coder" sessions: one with usage reported, one without (local
	// server omitted it) — the unavailable one must not drag the token
	// average toward zero.
	seedMetricsSession(t, store, "s1", "coder", db.SessionStatusCompleted, 1000, 200)
	seedMetricsSession(t, store, "s2", "coder", db.SessionStatusFailed, 0, 0)

	svc := newTestService(t)
	svc.cfg.Store = store

	report, err := svc.Metrics(ctx)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}

	if len(report.Nodes) != 1 {
		t.Fatalf("got %d node metrics, want 1: %+v", len(report.Nodes), report.Nodes)
	}
	node := report.Nodes[0]
	if node.Node != "implement" || node.Runs != 2 || node.Failures != 1 {
		t.Errorf("node = %+v, want implement/2 runs/1 failure", node)
	}
	if node.FailureRate != 0.5 {
		t.Errorf("FailureRate = %v, want 0.5", node.FailureRate)
	}
	if node.AvgElapsedMS != 1500 {
		t.Errorf("AvgElapsedMS = %v, want 1500", node.AvgElapsedMS)
	}

	if len(report.Sessions) != 1 {
		t.Fatalf("got %d session metrics, want 1: %+v", len(report.Sessions), report.Sessions)
	}
	sess := report.Sessions[0]
	if sess.WorkerID != "coder" || sess.Sessions != 2 || sess.Failures != 1 {
		t.Errorf("session = %+v, want coder/2 sessions/1 failure", sess)
	}
	if sess.FailureRate != 0.5 {
		t.Errorf("FailureRate = %v, want 0.5", sess.FailureRate)
	}
	if sess.UsageUnavailable != 1 {
		t.Errorf("UsageUnavailable = %d, want 1", sess.UsageUnavailable)
	}
	// avg(1000) = 1000 — s2's tokens_in=0 excluded, not averaged as a real zero.
	if sess.AvgTokensIn != 1000 {
		t.Errorf("AvgTokensIn = %v, want 1000 (s2's zero excluded)", sess.AvgTokensIn)
	}
}

func seedMetricsSession(t *testing.T, store *db.SQLiteStore, id, workerID string, status db.SessionStatus, tokensIn, tokensOut int64) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateSession(ctx, &db.WorkerSession{
		ID: id, WorkerID: workerID, JobID: "job-1", Status: db.SessionStatusActive, Model: "m", Provider: "p",
	}); err != nil {
		t.Fatalf("CreateSession(%s): %v", id, err)
	}
	if err := store.UpdateSession(ctx, id, db.SessionUpdate{
		Status: &status, TokensIn: &tokensIn, TokensOut: &tokensOut,
	}); err != nil {
		t.Fatalf("UpdateSession(%s): %v", id, err)
	}
}
