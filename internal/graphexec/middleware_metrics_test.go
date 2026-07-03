package graphexec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jefflinse/rhizome"
	"github.com/jefflinse/toasters/internal/db"
)

// openMetricsTestStore opens a real SQLite store, seeded with the job/task
// row PersistenceMiddleware's ReportProgress call references — node_executions
// aggregation is exercised through actual SQL, not a mock.
func openMetricsTestStore(t *testing.T) db.Store {
	t.Helper()
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
	return store
}

// PersistenceMiddleware sits outside rhizome.Retry in Executor's chain (see
// the chain comment in Execute), so it sees exactly one call per logical
// node execution even when Retry re-invokes the node body internally. This
// test wires that composition directly — Persistence wrapping Retry
// wrapping a node that fails twice before succeeding — and asserts a
// single node_executions row is written, recording the final outcome.
func TestPersistenceMiddleware_RetrySucceeds_WritesOneRow(t *testing.T) {
	store := openMetricsTestStore(t)
	persist := PersistenceMiddleware(store, "bug-fix")
	retry := rhizome.Retry[*TaskState](
		rhizome.WithMaxAttempts(3),
		rhizome.WithBackoff(func(int) time.Duration { return 0 }),
	)

	var attempts int
	node := rhizome.NodeFunc[*TaskState](func(_ context.Context, s *TaskState) (*TaskState, error) {
		attempts++
		if attempts < 3 {
			return s, errors.New("transient")
		}
		return s, nil
	})

	state := &TaskState{JobID: "job-1", TaskID: "task-1"}
	wrapped := rhizome.NodeFunc[*TaskState](func(ctx context.Context, s *TaskState) (*TaskState, error) {
		return retry(ctx, "implement", s, node)
	})

	if _, err := persist(context.Background(), "implement", state, wrapped); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (2 failures + 1 success)", attempts)
	}

	stats, err := store.NodeExecutionStats(context.Background())
	if err != nil {
		t.Fatalf("NodeExecutionStats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("got %d node stats, want 1 (one row despite 3 attempts): %+v", len(stats), stats)
	}
	if stats[0].Runs != 1 {
		t.Errorf("Runs = %d, want 1", stats[0].Runs)
	}
	if stats[0].Failures != 0 {
		t.Errorf("Failures = %d, want 0 — the logical execution ultimately succeeded", stats[0].Failures)
	}
}

// When every attempt fails, Retry exhausts and returns the last error to
// Persistence, which must still write exactly one row, marked failed.
func TestPersistenceMiddleware_RetryExhausted_WritesOneFailedRow(t *testing.T) {
	store := openMetricsTestStore(t)
	persist := PersistenceMiddleware(store, "bug-fix")
	retry := rhizome.Retry[*TaskState](
		rhizome.WithMaxAttempts(3),
		rhizome.WithBackoff(func(int) time.Duration { return 0 }),
	)

	var attempts int
	boom := errors.New("boom")
	node := rhizome.NodeFunc[*TaskState](func(_ context.Context, s *TaskState) (*TaskState, error) {
		attempts++
		return s, boom
	})

	state := &TaskState{JobID: "job-1", TaskID: "task-1"}
	wrapped := rhizome.NodeFunc[*TaskState](func(ctx context.Context, s *TaskState) (*TaskState, error) {
		return retry(ctx, "implement", s, node)
	})

	if _, err := persist(context.Background(), "implement", state, wrapped); !errors.Is(err, boom) {
		t.Fatalf("persist error = %v, want %v", err, boom)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}

	stats, err := store.NodeExecutionStats(context.Background())
	if err != nil {
		t.Fatalf("NodeExecutionStats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("got %d node stats, want 1 (one row despite 3 attempts): %+v", len(stats), stats)
	}
	if stats[0].Runs != 1 {
		t.Errorf("Runs = %d, want 1", stats[0].Runs)
	}
	if stats[0].Failures != 1 {
		t.Errorf("Failures = %d, want 1", stats[0].Failures)
	}
}

// A node that sets a routing status (not an error) is recorded with that
// status, not "failed" — matches EventMiddleware's success-status handling.
func TestPersistenceMiddleware_RoutingOutcomeRecorded(t *testing.T) {
	store := openMetricsTestStore(t)
	persist := PersistenceMiddleware(store, "bug-fix")

	node := rhizome.NodeFunc[*TaskState](func(_ context.Context, s *TaskState) (*TaskState, error) {
		s.Status = "tests_passed"
		return s, nil
	})

	state := &TaskState{JobID: "job-1", TaskID: "task-1"}
	if _, err := persist(context.Background(), "test", state, node); err != nil {
		t.Fatalf("persist: %v", err)
	}

	stats, err := store.NodeExecutionStats(context.Background())
	if err != nil {
		t.Fatalf("NodeExecutionStats: %v", err)
	}
	if len(stats) != 1 || stats[0].Node != "test" {
		t.Fatalf("stats = %+v, want one row for node=test", stats)
	}
	if stats[0].Failures != 0 {
		t.Errorf("Failures = %d, want 0 — a routing outcome is not a failure", stats[0].Failures)
	}
}
