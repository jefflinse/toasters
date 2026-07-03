package tui

import "testing"

// A fan-out root pseudo-session never streams output or progress — the LLM
// work happens in its "#i" branch (and ".judge") sessions — so displaying it
// reads as a stuck worker. displayRuntimeSessions hides it (like system
// nodes) unless --debug.
func TestDisplayRuntimeSessionsHidesFanoutRoots(t *testing.T) {
	m := newMinimalModel(t)
	m.runtimeSessions = map[string]*runtimeSlot{
		"graph:t1:review":       {sessionID: "graph:t1:review", status: "active"}, // fan-out root
		"graph:t1:review#0":     {sessionID: "graph:t1:review#0", status: "active"},
		"graph:t1:review#1":     {sessionID: "graph:t1:review#1", status: "completed"},
		"graph:t1:review.judge": {sessionID: "graph:t1:review.judge", status: "active"},
		"graph:t1:build":        {sessionID: "graph:t1:build", status: "active"}, // plain node
	}

	shown := map[string]bool{}
	for _, rs := range m.displayRuntimeSessions() {
		shown[rs.sessionID] = true
	}

	if shown["graph:t1:review"] {
		t.Error("fan-out root shown; want hidden")
	}
	for _, id := range []string{"graph:t1:review#0", "graph:t1:review#1", "graph:t1:review.judge", "graph:t1:build"} {
		if !shown[id] {
			t.Errorf("session %q hidden; want shown", id)
		}
	}

	// --debug shows everything, root included.
	m.debug = true
	shown = map[string]bool{}
	for _, rs := range m.displayRuntimeSessions() {
		shown[rs.sessionID] = true
	}
	if !shown["graph:t1:review"] {
		t.Error("fan-out root hidden under --debug; want shown")
	}
}

// Before any branch session exists (during the split phase) the root is
// indistinguishable from a plain node and is shown; it disappears once the
// first branch registers. This pins the intentionally-brief flicker window.
func TestFanoutRootShownUntilFirstBranch(t *testing.T) {
	m := newMinimalModel(t)
	m.runtimeSessions = map[string]*runtimeSlot{
		"graph:t1:review": {sessionID: "graph:t1:review", status: "active"},
	}
	if got := len(m.displayRuntimeSessions()); got != 1 {
		t.Fatalf("sessions shown = %d, want 1 (root alone is indistinguishable)", got)
	}

	m.runtimeSessions["graph:t1:review#0"] = &runtimeSlot{sessionID: "graph:t1:review#0", status: "active"}
	for _, rs := range m.displayRuntimeSessions() {
		if rs.sessionID == "graph:t1:review" {
			t.Error("fan-out root still shown after first branch registered")
		}
	}
}
