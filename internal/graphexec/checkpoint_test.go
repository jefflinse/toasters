package graphexec

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jefflinse/rhizome"

	"github.com/jefflinse/toasters/internal/db"
)

func TestTaskState_SnapshotRoundTrip(t *testing.T) {
	s := NewTaskState("job-1", "task-1", "/ws", "prov", "model-x")
	s.WorkspaceBase = "/ws-base"
	s.SetArtifact("plan.steps", "step one\nstep two")
	s.NodeOutputs["investigate"] = json.RawMessage(`{"findings":"root cause X"}`)
	s.Inputs = json.RawMessage(`{"goal":"fix it"}`)
	s.Status = "ok"
	s.FinalText = "done"
	s.ExitNode = "review"

	data, err := s.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	var got TaskState
	if err := got.UnmarshalBinary(data); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}

	if got.JobID != s.JobID || got.TaskID != s.TaskID || got.WorkspaceDir != s.WorkspaceDir ||
		got.WorkspaceBase != s.WorkspaceBase || got.ProviderName != s.ProviderName ||
		got.Model != s.Model || got.Status != s.Status || got.FinalText != s.FinalText ||
		got.ExitNode != s.ExitNode {
		t.Fatalf("scalar fields not preserved: got %+v", got)
	}
	if got.Artifacts["plan.steps"] != "step one\nstep two" {
		t.Errorf("artifact not preserved: %v", got.Artifacts["plan.steps"])
	}
	if string(got.NodeOutputs["investigate"]) != `{"findings":"root cause X"}` {
		t.Errorf("node output not preserved: %s", got.NodeOutputs["investigate"])
	}
	if string(got.Inputs) != `{"goal":"fix it"}` {
		t.Errorf("inputs not preserved: %s", got.Inputs)
	}
}

// TestSQLiteCheckpoint_ResumeSkipsCompletedNodes proves the SQLite checkpoint
// store and TaskState's Snapshotter integrate with rhizome's resume machinery:
// a graph that "crashes" mid-run (a node errors before its own checkpoint is
// written) resumes from the last completed node, not from the entry node, and
// the state accumulated before the crash is restored.
func TestSQLiteCheckpoint_ResumeSkipsCompletedNodes(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	cp := store.CheckpointStore()

	var executed []string
	node := func(id string, fail bool) rhizome.NodeFunc[*TaskState] {
		return func(_ context.Context, s *TaskState) (*TaskState, error) {
			executed = append(executed, id)
			if fail {
				return s, fmt.Errorf("simulated crash at %s", id)
			}
			s.SetArtifact(id, "ran")
			return s, nil
		}
	}

	build := func(failAtN2 bool) *rhizome.CompiledGraph[*TaskState] {
		g := rhizome.New[*TaskState]()
		_ = g.AddNode("n1", node("n1", false))
		_ = g.AddNode("n2", node("n2", failAtN2))
		_ = g.AddNode("n3", node("n3", false))
		_ = g.AddEdge(rhizome.Start, "n1")
		_ = g.AddEdge("n1", "n2")
		_ = g.AddEdge("n2", "n3")
		_ = g.AddEdge("n3", rhizome.End)
		cg, cErr := g.Compile(rhizome.WithCheckpointing(cp))
		if cErr != nil {
			t.Fatalf("compile: %v", cErr)
		}
		return cg
	}

	st := NewTaskState("job-1", "task-1", "/ws", "prov", "model")

	// First run: n2 errors after n1's checkpoint is written. Only n1 is saved.
	_, err = build(true).Run(context.Background(), st, rhizome.WithThreadID[*TaskState]("task-1"))
	if err == nil {
		t.Fatal("expected first run to fail at n2")
	}
	if got := strings.Join(executed, ","); got != "n1,n2" {
		t.Fatalf("first run executed = %q, want n1,n2", got)
	}

	// A checkpoint survived the crash, recorded at the last completed node.
	node1, _, loadErr := cp.Load(context.Background(), "task-1")
	if loadErr != nil {
		t.Fatalf("expected a checkpoint after crash: %v", loadErr)
	}
	if node1 != "n1" {
		t.Fatalf("checkpoint node = %q, want n1", node1)
	}

	// Resume on a fresh graph (n2 now succeeds): n1 must NOT re-run.
	executed = nil
	final, err := build(false).Resume(context.Background(), "task-1", &TaskState{})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := strings.Join(executed, ","); got != "n2,n3" {
		t.Fatalf("after resume executed = %q, want n2,n3 (n1 must not re-run)", got)
	}
	// State carried across the crash: n1's artifact, set before the crash, is
	// restored from the checkpoint into the resumed run.
	if final.Artifacts["n1"] != "ran" {
		t.Errorf("resumed state lost n1's artifact: %+v", final.Artifacts)
	}
}

// TestExecute_ShutdownLeavesTaskResumable verifies that when the executor is
// draining (graceful shutdown / deploy) and a run is cancelled mid-graph,
// Execute leaves the task in_progress with its checkpoint intact instead of
// writing a terminal 'cancelled' status. That is what lets the next boot's
// ReconcileInterrupted + recovery resume it — making a deploy resumable on the
// same machinery as a hard crash. A user CancelJob (not draining) still falls
// through to the cancelled path.
func TestExecute_ShutdownLeavesTaskResumable(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "drain.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	cp := store.CheckpointStore()
	bg := context.Background()

	if err := store.CreateJob(bg, &db.Job{ID: "j", Title: "J", Type: "test", Status: db.JobStatusActive}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := store.CreateTask(bg, &db.Task{ID: "t", JobID: "j", Title: "T", Status: db.TaskStatusInProgress, GraphID: "g"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	e := &Executor{
		store:           store,
		eventSink:       &mockEventSink{},
		checkpointStore: cp,
		retryAttempts:   1,
		draining:        true, // simulate Drain() having flipped the flag
	}

	nodeStarted := make(chan struct{})
	g := rhizome.New[*TaskState]()
	_ = g.AddNode("node-1", func(_ context.Context, s *TaskState) (*TaskState, error) { return s, nil })
	_ = g.AddNode("node-2", func(ctx context.Context, s *TaskState) (*TaskState, error) {
		close(nodeStarted)
		<-ctx.Done() // block until the run is cancelled (the "deploy" SIGTERM)
		return s, ctx.Err()
	})
	_ = g.AddEdge(rhizome.Start, "node-1")
	_ = g.AddEdge("node-1", "node-2")
	_ = g.AddEdge("node-2", rhizome.End)
	cg, err := g.Compile(rhizome.WithCheckpointing(cp))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	st := NewTaskState("j", "t", "/ws", "prov", "model")
	runCtx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() { done <- e.Execute(runCtx, cg, st, "g") }()

	<-nodeStarted // node-1 completed and checkpointed; node-2 now blocking
	cancel()      // Drain cancels the in-flight run
	if execErr := <-done; execErr == nil {
		t.Fatal("want error from a shutdown-interrupted run")
	}

	// Task left in_progress (not cancelled/failed) so recovery re-dispatches it.
	task, err := store.GetTask(bg, "t")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != db.TaskStatusInProgress {
		t.Errorf("task status = %s, want in_progress (left resumable on shutdown)", task.Status)
	}
	// Checkpoint kept at the last completed node so Resume continues from there.
	node, _, err := cp.Load(bg, "t")
	if err != nil {
		t.Fatalf("checkpoint should be kept on shutdown: %v", err)
	}
	if node != "node-1" {
		t.Errorf("kept checkpoint node = %q, want node-1", node)
	}
}
