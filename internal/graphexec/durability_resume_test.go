package graphexec

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jefflinse/rhizome"

	"github.com/jefflinse/toasters/internal/db"
)

// TestDurability_ReconcileThenResume is an end-to-end test of the durability
// seam across Tier 0 (requeue) + Tier 1 (checkpoint/resume): an interrupted run
// leaves a checkpoint, the boot-time ReconcileInterrupted requeues the task
// WITHOUT dropping that checkpoint, and the next dispatch RESUMES from the
// interrupted node rather than restarting the graph — finishing the work and
// cleaning up the checkpoint.
//
// It wires the real components (Executor.Execute, db.ReconcileInterrupted, the
// SQLite CheckpointStore) with a hand-built rhizome graph standing in for the
// compiled RoleNode graph, so it exercises the persistence/recovery machinery
// without the LLM-node stack. Operator-side re-dispatch (assignNextTask) is
// covered separately by TestRun_RecoversInterruptedJobOnStart.
func TestDurability_ReconcileThenResume(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	cp := store.CheckpointStore()
	bg := context.Background()

	const jobID, taskID = "job-1", "task-1"
	if err := store.CreateJob(bg, &db.Job{ID: jobID, Title: "J", Type: "test", Status: db.JobStatusActive}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := store.CreateTask(bg, &db.Task{ID: taskID, JobID: jobID, Title: "T", Status: db.TaskStatusInProgress, GraphID: "g"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// A worker session left 'active', as an interruption would leave it.
	if err := store.CreateSession(bg, &db.WorkerSession{ID: "s-1", WorkerID: "graph:node-1", JobID: jobID, TaskID: taskID, Status: db.SessionStatusActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	var executed []string
	recordNode := func(id string) rhizome.NodeFunc[*TaskState] {
		return func(_ context.Context, s *TaskState) (*TaskState, error) {
			executed = append(executed, id)
			s.SetArtifact(id, "ran")
			return s, nil
		}
	}
	edges := func(g *rhizome.Graph[*TaskState]) {
		_ = g.AddEdge(rhizome.Start, "node-1")
		_ = g.AddEdge("node-1", "node-2")
		_ = g.AddEdge("node-2", "node-3")
		_ = g.AddEdge("node-3", rhizome.End)
	}
	compile := func(g *rhizome.Graph[*TaskState]) *rhizome.CompiledGraph[*TaskState] {
		cg, cErr := g.Compile(rhizome.WithCheckpointing(cp))
		if cErr != nil {
			t.Fatalf("compile: %v", cErr)
		}
		return cg
	}

	// --- Phase 1: run, interrupted mid-graph at node-2 (server killed) ---
	// node-1 completes (and checkpoints); node-2 signals then blocks until the
	// run is cancelled, standing in for the process dying mid-node.
	started := make(chan struct{})
	g1 := rhizome.New[*TaskState]()
	_ = g1.AddNode("node-1", recordNode("node-1"))
	_ = g1.AddNode("node-2", func(ctx context.Context, s *TaskState) (*TaskState, error) {
		executed = append(executed, "node-2")
		close(started)
		<-ctx.Done()
		return s, ctx.Err()
	})
	_ = g1.AddNode("node-3", recordNode("node-3"))
	edges(g1)
	cg1 := compile(g1)

	exec1 := &Executor{store: store, eventSink: &mockEventSink{}, checkpointStore: cp, retryAttempts: 1, draining: true}
	runCtx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() {
		done <- exec1.Execute(runCtx, cg1, NewTaskState(jobID, taskID, "/ws", "prov", "model"), "g")
	}()
	<-started // node-1 done + checkpointed; node-2 entered and blocking
	cancel()  // interruption
	if execErr := <-done; execErr == nil {
		t.Fatal("phase 1: want an error from the interrupted run")
	}
	if got := strings.Join(executed, ","); got != "node-1,node-2" {
		t.Fatalf("phase 1 executed = %q, want node-1,node-2", got)
	}
	// Interruption left the task in_progress with its checkpoint intact.
	if task, _ := store.GetTask(bg, taskID); task.Status != db.TaskStatusInProgress {
		t.Fatalf("phase 1: task status = %s, want in_progress", task.Status)
	}
	if node, _, lErr := cp.Load(bg, taskID); lErr != nil || node != "node-1" {
		t.Fatalf("phase 1: checkpoint = (%q, %v), want (node-1, nil)", node, lErr)
	}

	// --- Restart: reconcile requeues the task but must KEEP the checkpoint ---
	sessions, tasks, err := store.ReconcileInterrupted(bg)
	if err != nil {
		t.Fatalf("ReconcileInterrupted: %v", err)
	}
	if sessions != 1 || tasks != 1 {
		t.Fatalf("reconcile = (%d sessions, %d tasks), want (1, 1)", sessions, tasks)
	}
	if task, _ := store.GetTask(bg, taskID); task.Status != db.TaskStatusPending {
		t.Fatalf("after reconcile: task status = %s, want pending", task.Status)
	}
	if node, _, lErr := cp.Load(bg, taskID); lErr != nil || node != "node-1" {
		t.Fatalf("after reconcile: checkpoint = (%q, %v), want it preserved at node-1", node, lErr)
	}

	// --- Phase 2: re-dispatch (fresh executor) resumes from the checkpoint ---
	executed = nil
	g2 := rhizome.New[*TaskState]()
	_ = g2.AddNode("node-1", recordNode("node-1"))
	_ = g2.AddNode("node-2", recordNode("node-2"))
	_ = g2.AddNode("node-3", recordNode("node-3"))
	edges(g2)
	cg2 := compile(g2)

	exec2 := &Executor{store: store, eventSink: &mockEventSink{}, checkpointStore: cp, retryAttempts: 1}
	if err := exec2.Execute(bg, cg2, NewTaskState(jobID, taskID, "/ws", "prov", "model"), "g"); err != nil {
		t.Fatalf("phase 2 Execute: %v", err)
	}
	// node-1 must NOT re-run: resume continues from after the checkpoint.
	if got := strings.Join(executed, ","); got != "node-2,node-3" {
		t.Fatalf("phase 2 executed = %q, want node-2,node-3 (node-1 must not re-run)", got)
	}
	// Task finished, and the checkpoint was cleaned up at terminal status.
	if task, _ := store.GetTask(bg, taskID); task.Status != db.TaskStatusCompleted {
		t.Fatalf("phase 2: task status = %s, want completed", task.Status)
	}
	if _, _, lErr := cp.Load(bg, taskID); lErr == nil {
		t.Errorf("phase 2: checkpoint should be deleted after completion")
	}
}
