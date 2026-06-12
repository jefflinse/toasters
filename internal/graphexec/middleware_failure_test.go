package graphexec

import (
	"context"
	"errors"
	"testing"

	"github.com/jefflinse/rhizome"
)

// A node error must broadcast status "failed" — the TUI keys PhaseFailed off
// it and failedGraphNode names the failing node from it. Before the fix,
// EventMiddleware read result.Status (which production nodes never set) and
// failed nodes broadcast as completed with status "".
func TestEventMiddleware_NodeErrorBroadcastsFailed(t *testing.T) {
	sink := &mockEventSink{}
	mw := EventMiddleware(sink)
	state := &TaskState{JobID: "j-1", TaskID: "t-1"}

	boom := errors.New("boom")
	failing := func(_ context.Context, s *TaskState) (*TaskState, error) {
		return s, boom
	}
	if _, err := mw(context.Background(), "implement", state, rhizome.NodeFunc[*TaskState](failing)); !errors.Is(err, boom) {
		t.Fatalf("middleware should propagate the node error, got %v", err)
	}

	events := sink.snapshot()
	want := []string{"node_started:implement", "node_completed:implement:failed"}
	if len(events) != 2 || events[0] != want[0] || events[1] != want[1] {
		t.Errorf("events = %v, want %v", events, want)
	}
}

// A successful node with no routing status broadcasts "completed"; one that
// sets a routing status broadcasts that status.
func TestEventMiddleware_SuccessStatuses(t *testing.T) {
	sink := &mockEventSink{}
	mw := EventMiddleware(sink)

	plain := func(_ context.Context, s *TaskState) (*TaskState, error) { return s, nil }
	if _, err := mw(context.Background(), "plan", &TaskState{}, rhizome.NodeFunc[*TaskState](plain)); err != nil {
		t.Fatalf("plain node: %v", err)
	}

	routed := func(_ context.Context, s *TaskState) (*TaskState, error) {
		s.Status = "tests_passed"
		return s, nil
	}
	if _, err := mw(context.Background(), "test", &TaskState{}, rhizome.NodeFunc[*TaskState](routed)); err != nil {
		t.Fatalf("routed node: %v", err)
	}

	events := sink.snapshot()
	if events[1] != "node_completed:plan:completed" {
		t.Errorf("plain node completion = %q, want node_completed:plan:completed", events[1])
	}
	if events[3] != "node_completed:test:tests_passed" {
		t.Errorf("routed node completion = %q, want node_completed:test:tests_passed", events[3])
	}
}
