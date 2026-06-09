package service

import (
	"context"
	"testing"
)

// TestActiveGraphNodes_TrackedInProgressState verifies that graph nodes are
// recorded as active when they start and dropped when they complete, and that
// they surface in the progress snapshot — the data a reconnecting client uses
// to rebuild the Workers panel for an in-flight graph job.
func TestActiveGraphNodes_TrackedInProgressState(t *testing.T) {
	svc := NewLocal(LocalConfig{ConfigDir: t.TempDir(), Store: &mockStore{}})

	svc.BroadcastGraphNodeStarted("job-1", "task-1", "implement")
	svc.BroadcastGraphNodeStarted("job-1", "task-1", "implement#0")

	ps, err := svc.GetProgressState(context.Background())
	if err != nil {
		t.Fatalf("GetProgressState: %v", err)
	}
	if len(ps.ActiveGraphNodes) != 2 {
		t.Fatalf("ActiveGraphNodes = %d, want 2", len(ps.ActiveGraphNodes))
	}
	seen := map[string]GraphNodeSnapshot{}
	for _, gn := range ps.ActiveGraphNodes {
		seen[gn.SessionID] = gn
	}
	gn, ok := seen["graph:task-1:implement"]
	if !ok {
		t.Fatalf("missing graph:task-1:implement; got %v", ps.ActiveGraphNodes)
	}
	if gn.JobID != "job-1" || gn.TaskID != "task-1" || gn.Node != "implement" {
		t.Errorf("node = %+v, want job-1/task-1/implement", gn)
	}
	if _, ok := seen["graph:task-1:implement#0"]; !ok {
		t.Error("missing fan-out branch graph:task-1:implement#0")
	}

	// Completing a node removes it from the active set.
	svc.BroadcastGraphNodeCompleted("job-1", "task-1", "implement", "")
	ps2, _ := svc.GetProgressState(context.Background())
	if len(ps2.ActiveGraphNodes) != 1 || ps2.ActiveGraphNodes[0].SessionID != "graph:task-1:implement#0" {
		t.Errorf("after completion ActiveGraphNodes = %v, want only the branch", ps2.ActiveGraphNodes)
	}
}
