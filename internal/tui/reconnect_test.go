package tui

import (
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

// TestProgressPollMsg_SeedsRuntimeSlots verifies the reconnect rehydration:
// a progress snapshot carrying active graph nodes and live worker sessions
// rebuilds runtime slots that the (non-replayed) live event stream would
// otherwise be the only source of — so the Workers panel isn't empty after a
// reconnect mid-job.
func TestProgressPollMsg_SeedsRuntimeSlots(t *testing.T) {
	m := newMinimalModel(t)
	start := time.Unix(1000, 0)

	res, _ := m.Update(progressPollMsg{
		GraphNodes: []service.GraphNodeSnapshot{
			{SessionID: "graph:t1:plan", JobID: "j1", TaskID: "t1", Node: "plan", StartedAt: start},
		},
		LiveSnapshots: []service.SessionSnapshot{
			{ID: "sess-1", WorkerID: "coder", JobID: "j1", TaskID: "t1", Status: "active", Model: "qwen", StartTime: start},
		},
	})
	got := res.(*Model)

	gslot, ok := got.runtimeSessions["graph:t1:plan"]
	if !ok {
		t.Fatal("graph node slot not seeded from snapshot")
	}
	if gslot.agentName != "graph:plan" || gslot.status != "active" {
		t.Errorf("graph slot = %+v, want agentName graph:plan / active", gslot)
	}

	wslot, ok := got.runtimeSessions["sess-1"]
	if !ok {
		t.Fatal("worker slot not seeded from snapshot")
	}
	if wslot.agentName != "coder" || wslot.model != "qwen" {
		t.Errorf("worker slot = %+v, want agentName coder / model qwen", wslot)
	}
}

// TestProgressPollMsg_SeedIsIdempotent verifies a re-poll doesn't duplicate or
// reset an existing slot — e.g. a slot already marked completed by a live event
// must not be resurrected as active by a stale snapshot.
func TestProgressPollMsg_SeedIsIdempotent(t *testing.T) {
	m := newMinimalModel(t)
	m.runtimeSessions["graph:t1:plan"] = &runtimeSlot{
		sessionID: "graph:t1:plan", agentName: "graph:plan", status: "completed",
	}

	res, _ := m.Update(progressPollMsg{
		GraphNodes: []service.GraphNodeSnapshot{
			{SessionID: "graph:t1:plan", JobID: "j1", TaskID: "t1", Node: "plan"},
		},
	})
	got := res.(*Model)

	if n := len(got.runtimeSessions); n != 1 {
		t.Errorf("runtimeSessions = %d, want 1 (no duplicate)", n)
	}
	if got.runtimeSessions["graph:t1:plan"].status != "completed" {
		t.Error("existing completed slot was reset by re-seed")
	}
}
