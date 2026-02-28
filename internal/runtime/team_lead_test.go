package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/compose"
	"github.com/jefflinse/toasters/internal/provider"
)

// TestRuntimeImplementsTeamLeadSpawner is a compile-time interface check.
// If *Runtime ever stops implementing TeamLeadSpawner, this line will fail to compile.
func TestRuntimeImplementsTeamLeadSpawner(t *testing.T) {
	var _ TeamLeadSpawner = (*Runtime)(nil)
}

// TestSpawnTeamLead_CreatesSession verifies that SpawnTeamLead creates a session
// that is registered in the runtime and runs to completion.
func TestSpawnTeamLead_CreatesSession(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Team lead response"},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := New(nil, newTestRegistry(mp))

	// Track the session created by SpawnTeamLead via the OnSessionStarted hook.
	var startedSess *Session
	rt.OnSessionStarted = func(s *Session) {
		startedSess = s
	}

	composed := &compose.ComposedAgent{
		AgentID:      "lead-agent",
		Name:         "Lead Agent",
		SystemPrompt: "You are a team lead.",
		Provider:     "test",
		Model:        "test-model",
		TeamID:       "team-1",
	}

	err := rt.SpawnTeamLead(context.Background(), composed, "task-1", "job-1", t.TempDir(), "Initialize the project")
	assertNoError(t, err)

	// OnSessionStarted must have fired.
	if startedSess == nil {
		t.Fatal("OnSessionStarted was not called; no session was created")
	}

	// Session ID must be non-empty.
	if startedSess.ID() == "" {
		t.Fatal("session ID should not be empty")
	}

	// Wait for the session to complete so the test doesn't leak goroutines.
	select {
	case <-startedSess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete in time")
	}

	snap := startedSess.Snapshot()
	assertEqual(t, "completed", snap.Status)
}

// TestSpawnTeamLead_DepthIsZero verifies that the session spawned by
// SpawnTeamLead has Depth=0. This is the regression test for the off-by-one
// bug where team leads were spawned at depth 1 instead of depth 0, which
// prevented them from having spawn_agent available.
//
// We verify depth indirectly: a session at depth 0 with maxDepth=1 should
// have spawn_agent in its tool definitions. We capture the session via
// OnSessionStarted and inspect the tools it was given.
func TestSpawnTeamLead_DepthIsZero(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "done"},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := New(nil, newTestRegistry(mp))

	var startedSess *Session
	rt.OnSessionStarted = func(s *Session) {
		startedSess = s
	}

	composed := &compose.ComposedAgent{
		AgentID:      "lead-agent",
		Name:         "Lead Agent",
		SystemPrompt: "You are a team lead.",
		Provider:     "test",
		Model:        "test-model",
		TeamID:       "team-1",
		// No Tools filter — session gets the full CoreTools set.
	}

	err := rt.SpawnTeamLead(context.Background(), composed, "task-1", "job-1", t.TempDir(), "Check depth")
	assertNoError(t, err)

	if startedSess == nil {
		t.Fatal("OnSessionStarted was not called; no session was created")
	}

	// Inspect the tool definitions available to the session via toolExec.
	// At depth=0 with maxDepth=1 (defaultMaxDepth), spawn_agent MUST be present.
	defs := startedSess.toolExec.Definitions()
	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Name] = true
	}

	if !names["spawn_agent"] {
		t.Errorf("team lead session at depth=0 should have spawn_agent available; "+
			"got tools: %v (regression: was depth=1 before fix, which excluded spawn_agent)", toolNames(defs))
	}

	// Wait for session to complete to avoid goroutine leak.
	select {
	case <-startedSess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete in time")
	}
}

// TestSpawnTeamLead_WithToolFilter verifies that when ComposedAgent.Tools is
// non-empty, SpawnTeamLead applies a tool filter so the session only exposes
// the requested tools to the LLM.
func TestSpawnTeamLead_WithToolFilter(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "done"},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := New(nil, newTestRegistry(mp))

	var startedSess *Session
	rt.OnSessionStarted = func(s *Session) {
		startedSess = s
	}

	composed := &compose.ComposedAgent{
		AgentID:      "lead-agent",
		Name:         "Lead Agent",
		SystemPrompt: "You are a team lead.",
		Provider:     "test",
		Model:        "test-model",
		TeamID:       "team-1",
		// Request only read_file and spawn_agent.
		Tools: []string{"read_file", "spawn_agent"},
	}

	err := rt.SpawnTeamLead(context.Background(), composed, "task-1", "job-1", t.TempDir(), "Filter test")
	assertNoError(t, err)

	if startedSess == nil {
		t.Fatal("OnSessionStarted was not called; no session was created")
	}

	defs := startedSess.toolExec.Definitions()
	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Name] = true
	}

	if len(defs) != 2 {
		t.Errorf("expected exactly 2 tools in filtered set, got %d: %v", len(defs), toolNames(defs))
	}
	if !names["read_file"] {
		t.Error("expected read_file in filtered tool set")
	}
	if !names["spawn_agent"] {
		t.Error("expected spawn_agent in filtered tool set")
	}
	// write_file was not requested — it should be absent.
	if names["write_file"] {
		t.Error("write_file should NOT be in filtered tool set")
	}

	select {
	case <-startedSess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete in time")
	}
}

// TestSpawnTeamLead_ProviderNotFound verifies that SpawnTeamLead returns an
// error when the composed agent's provider is not registered.
func TestSpawnTeamLead_ProviderNotFound(t *testing.T) {
	rt := New(nil, provider.NewRegistry()) // empty registry

	composed := &compose.ComposedAgent{
		AgentID:      "lead-agent",
		Provider:     "nonexistent",
		Model:        "test-model",
		SystemPrompt: "You are a team lead.",
	}

	err := rt.SpawnTeamLead(context.Background(), composed, "task-1", "job-1", t.TempDir(), "Provider test")
	assertError(t, err)
	assertContains(t, err.Error(), "not found")
}
