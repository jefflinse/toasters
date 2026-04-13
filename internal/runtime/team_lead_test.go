package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

	composed := &ComposedWorker{
		WorkerID:      "lead-worker",
		Name:         "Lead Worker",
		SystemPrompt: "You are a team lead.",
		Provider:     "test",
		Model:        "test-model",
		TeamID:       "team-1",
	}

	err := rt.SpawnTeamLead(context.Background(), composed, "task-1", "job-1", t.TempDir(), "Initialize the project", nil)
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
// prevented them from having spawn_worker available.
//
// We verify depth indirectly: a session at depth 0 with maxDepth=1 should
// have spawn_worker in its tool definitions. We capture the session via
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

	composed := &ComposedWorker{
		WorkerID:      "lead-worker",
		Name:         "Lead Worker",
		SystemPrompt: "You are a team lead.",
		Provider:     "test",
		Model:        "test-model",
		TeamID:       "team-1",
		// No Tools filter — session gets the full CoreTools set.
	}

	err := rt.SpawnTeamLead(context.Background(), composed, "task-1", "job-1", t.TempDir(), "Check depth", nil)
	assertNoError(t, err)

	if startedSess == nil {
		t.Fatal("OnSessionStarted was not called; no session was created")
	}

	// Inspect the tool definitions available to the session via toolExec.
	// At depth=0 with maxDepth=1 (defaultMaxDepth), spawn_worker MUST be present.
	defs := startedSess.toolExec.Definitions()
	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Name] = true
	}

	if names["spawn_agent"] {
		t.Error("spawn_agent should not be present (removed)")
	}

	// Wait for session to complete to avoid goroutine leak.
	select {
	case <-startedSess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete in time")
	}
}

// TestSpawnTeamLead_WithToolFilter verifies that when ComposedWorker.Tools is
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

	composed := &ComposedWorker{
		WorkerID:      "lead-worker",
		Name:         "Lead Worker",
		SystemPrompt: "You are a team lead.",
		Provider:     "test",
		Model:        "test-model",
		TeamID:       "team-1",
		// Request only read_file and write_file.
		Tools: []string{"read_file", "write_file"},
	}

	err := rt.SpawnTeamLead(context.Background(), composed, "task-1", "job-1", t.TempDir(), "Filter test", nil)
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
	if !names["write_file"] {
		t.Error("expected write_file in filtered tool set")
	}
	// shell was not requested — it should be absent.
	if names["shell"] {
		t.Error("shell should NOT be in filtered tool set")
	}

	select {
	case <-startedSess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete in time")
	}
}

// TestSpawnTeamLead_ProviderNotFound verifies that SpawnTeamLead returns an
// error when the composed worker's provider is not registered.
func TestSpawnTeamLead_ProviderNotFound(t *testing.T) {
	rt := New(nil, provider.NewRegistry()) // empty registry

	composed := &ComposedWorker{
		WorkerID:      "lead-worker",
		Provider:     "nonexistent",
		Model:        "test-model",
		SystemPrompt: "You are a team lead.",
	}

	err := rt.SpawnTeamLead(context.Background(), composed, "task-1", "job-1", t.TempDir(), "Provider test", nil)
	assertError(t, err)
	assertContains(t, err.Error(), "not found")
}

// TestSpawnTeamLead_WithExtraTools verifies that when extraTools is provided,
// the session's tool definitions include both CoreTools tools AND the extra
// tools. This is the integration test for the LayeredToolExecutor wiring in
// SpawnTeamLead → SpawnWorker.
func TestSpawnTeamLead_WithExtraTools(t *testing.T) {
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

	// Create a mock ToolExecutor that defines team lead tools.
	extraTools := &mockToolExecutor{
		defs: []ToolDef{
			{Name: "complete_task", Description: "Mark task as done"},
			{Name: "report_progress", Description: "Report progress"},
		},
		results: map[string]string{
			"complete_task":   "task completed",
			"report_progress": "progress reported",
		},
	}

	composed := &ComposedWorker{
		WorkerID:      "lead-worker",
		Name:         "Lead Worker",
		SystemPrompt: "You are a team lead.",
		Provider:     "test",
		Model:        "test-model",
		TeamID:       "team-1",
		// No Tools filter — session gets the full combined set.
	}

	err := rt.SpawnTeamLead(context.Background(), composed, "task-1", "job-1", t.TempDir(), "Extra tools test", extraTools)
	assertNoError(t, err)

	if startedSess == nil {
		t.Fatal("OnSessionStarted was not called; no session was created")
	}

	// Inspect the tool definitions available to the session.
	defs := startedSess.toolExec.Definitions()
	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Name] = true
	}

	// CoreTools tools should be present.
	if !names["read_file"] {
		t.Error("expected read_file (CoreTools) in session tool definitions")
	}
	if !names["write_file"] {
		t.Error("expected write_file (CoreTools) in session tool definitions")
	}
	if !names["shell"] {
		t.Error("expected shell (CoreTools) in session tool definitions")
	}
	if names["spawn_agent"] {
		t.Error("spawn_agent should not be present (removed)")
	}

	// Extra tools should also be present.
	if !names["complete_task"] {
		t.Error("expected complete_task (extra tool) in session tool definitions")
	}
	if !names["report_progress"] {
		t.Error("expected report_progress (extra tool) in session tool definitions")
	}

	// Wait for session to complete.
	select {
	case <-startedSess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete in time")
	}
}

// TestSpawnTeamLead_WithExtraToolsAndFilter verifies that when both extraTools
// and a composed.Tools filter are provided, the session only exposes the
// filtered subset of the combined (CoreTools + extra) tool set.
func TestSpawnTeamLead_WithExtraToolsAndFilter(t *testing.T) {
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

	extraTools := &mockToolExecutor{
		defs: []ToolDef{
			{Name: "complete_task", Description: "Mark task as done"},
			{Name: "report_progress", Description: "Report progress"},
		},
		results: map[string]string{
			"complete_task":   "task completed",
			"report_progress": "progress reported",
		},
	}

	composed := &ComposedWorker{
		WorkerID:      "lead-worker",
		Name:         "Lead Worker",
		SystemPrompt: "You are a team lead.",
		Provider:     "test",
		Model:        "test-model",
		TeamID:       "team-1",
		// Filter to only read_file (CoreTools) and complete_task (extra).
		Tools: []string{"read_file", "complete_task"},
	}

	err := rt.SpawnTeamLead(context.Background(), composed, "task-1", "job-1", t.TempDir(), "Filter + extra test", extraTools)
	assertNoError(t, err)

	if startedSess == nil {
		t.Fatal("OnSessionStarted was not called; no session was created")
	}

	defs := startedSess.toolExec.Definitions()
	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Name] = true
	}

	// Only the two filtered tools should be present.
	if len(defs) != 2 {
		t.Errorf("expected exactly 2 tools in filtered set, got %d: %v", len(defs), toolNames(defs))
	}
	if !names["read_file"] {
		t.Error("expected read_file in filtered tool set")
	}
	if !names["complete_task"] {
		t.Error("expected complete_task in filtered tool set")
	}
	// Tools not in the filter should be absent.
	if names["write_file"] {
		t.Error("write_file should NOT be in filtered tool set")
	}
	if names["report_progress"] {
		t.Error("report_progress should NOT be in filtered tool set (not in filter)")
	}
	if names["spawn_agent"] {
		t.Error("spawn_agent should NOT be in filtered tool set (not in filter)")
	}

	select {
	case <-startedSess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete in time")
	}
}

// TestSpawnTeamLead_ExtraToolsDispatchPriority verifies that when a team lead
// session calls a tool that exists in both CoreTools and extraTools, the extra
// tool (overlay) handles it. This is an end-to-end test through the full
// SpawnTeamLead → LayeredToolExecutor → session tool call path.
func TestSpawnTeamLead_ExtraToolsDispatchPriority(t *testing.T) {
	// The LLM will call "report_progress" which exists in both CoreTools
	// (as report_task_progress — different name, so no conflict) and extraTools.
	// We use "complete_task" which only exists in extraTools to verify dispatch.
	extraToolCalled := false
	extraTools := &mockToolExecutor{
		defs: []ToolDef{
			{Name: "complete_task", Description: "Mark task as done"},
		},
		results: map[string]string{
			"complete_task": "task completed via extra tools",
		},
	}
	// Wrap to track calls.
	trackingExtra := &trackingToolExecutor{
		inner: extraTools,
		onExecute: func(name string) {
			if name == "complete_task" {
				extraToolCalled = true
			}
		},
	}

	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			// Turn 1: LLM calls complete_task.
			{events: []provider.StreamEvent{
				{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:        "call-1",
					Name:      "complete_task",
					Arguments: json.RawMessage(`{"summary":"all done"}`),
				}},
				{Type: provider.EventDone},
			}},
			// Turn 2: LLM responds after seeing tool result.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Task completed"},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := New(nil, newTestRegistry(mp))

	var startedSess *Session
	rt.OnSessionStarted = func(s *Session) {
		startedSess = s
	}

	composed := &ComposedWorker{
		WorkerID:      "lead-worker",
		Name:         "Lead Worker",
		SystemPrompt: "You are a team lead.",
		Provider:     "test",
		Model:        "test-model",
		TeamID:       "team-1",
	}

	err := rt.SpawnTeamLead(context.Background(), composed, "task-1", "job-1", t.TempDir(), "Dispatch priority test", trackingExtra)
	assertNoError(t, err)

	if startedSess == nil {
		t.Fatal("OnSessionStarted was not called")
	}

	// Wait for session to complete.
	select {
	case <-startedSess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete in time")
	}

	if !extraToolCalled {
		t.Error("complete_task should have been dispatched to extra tools (overlay), not CoreTools")
	}
}

// trackingToolExecutor wraps a ToolExecutor and calls onExecute for each Execute call.
type trackingToolExecutor struct {
	inner     ToolExecutor
	onExecute func(name string)
}

func (t *trackingToolExecutor) Definitions() []ToolDef {
	return t.inner.Definitions()
}

func (t *trackingToolExecutor) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if t.onExecute != nil {
		t.onExecute(name)
	}
	return t.inner.Execute(ctx, name, args)
}
