package graphexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jefflinse/mycelium/agent"
	"github.com/jefflinse/rhizome"

	"github.com/jefflinse/toasters/internal/hitl"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
	"github.com/jefflinse/toasters/internal/tooldef"
)

// --- Mock provider ---

type mockProvider struct {
	mu        sync.Mutex
	responses [][]provider.StreamEvent
	calls     int
}

func (m *mockProvider) ChatStream(_ context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.calls >= len(m.responses) {
		return nil, fmt.Errorf("mock provider: no more responses (call %d)", m.calls)
	}
	events := m.responses[m.calls]
	m.calls++

	ch := make(chan provider.StreamEvent, len(events)+1)
	for _, ev := range events {
		ch <- ev
	}
	ch <- provider.StreamEvent{Type: provider.EventDone}
	close(ch)
	return ch, nil
}

func (m *mockProvider) Models(_ context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (m *mockProvider) Name() string { return "mock" }

// --- Mock tool executor ---

type mockToolExecutor struct {
	handler func(ctx context.Context, name string, args json.RawMessage) (string, error)
	defs    []tooldef.ToolDef
}

func (m *mockToolExecutor) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if m.handler != nil {
		return m.handler(ctx, name, args)
	}
	return fmt.Sprintf("result of %s", name), nil
}

func (m *mockToolExecutor) Definitions() []runtime.ToolDef {
	return m.defs
}

// --- Helpers ---

// completeResponse returns stream events for a terminal complete() tool call
// carrying the given output payload. Used to drive typed nodes.
func completeResponse(payload any) []provider.StreamEvent {
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return []provider.StreamEvent{
		{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
			ID:        "call-complete",
			Name:      "complete",
			Arguments: b,
		}},
	}
}

// toolCallResponse returns a non-terminal tool call (e.g. read_file).
func toolCallResponse(id, name, argsJSON string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
			ID:        id,
			Name:      name,
			Arguments: json.RawMessage(argsJSON),
		}},
	}
}

func templateConfig(responses [][]provider.StreamEvent) (TemplateConfig, *mockProvider) {
	prov := &mockProvider{responses: responses}
	toolExec := &mockToolExecutor{
		defs: []tooldef.ToolDef{
			{Name: "read_file", Description: "Read a file"},
			{Name: "write_file", Description: "Write a file"},
			{Name: "edit_file", Description: "Edit a file"},
			{Name: "glob", Description: "Find files"},
			{Name: "grep", Description: "Search files"},
			{Name: "shell", Description: "Run a command"},
		},
	}
	return TemplateConfig{Provider: prov, ToolExecutor: toolExec, Model: "test-model"}, prov
}

// --- Node-level tests ---

func TestSingleWorkerNode_Completion(t *testing.T) {
	cfg, _ := templateConfig([][]provider.StreamEvent{
		completeResponse(WorkOutput{Output: "task complete"}),
	})

	node := SingleWorkerNode(cfg, "You are a worker.", "Do the task.")
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want %q", result.Status, StatusCompleted)
	}
	if got := result.GetArtifactString("work.output"); got != "task complete" {
		t.Errorf("work.output = %q, want %q", got, "task complete")
	}
}

func TestTestNode_PassedRoutesToPassed(t *testing.T) {
	cfg, _ := templateConfig([][]provider.StreamEvent{
		completeResponse(TestOutput{Passed: true, Summary: "all tests pass"}),
	})

	node := TestNodeDynamic(cfg)
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusTestsPassed {
		t.Errorf("Status = %q, want %q", result.Status, StatusTestsPassed)
	}
	if got := result.GetArtifactString("test.results"); got != "all tests pass" {
		t.Errorf("test.results = %q, want %q", got, "all tests pass")
	}
}

func TestTestNode_FailedRoutesToFailed(t *testing.T) {
	cfg, _ := templateConfig([][]provider.StreamEvent{
		completeResponse(TestOutput{Passed: false, Summary: "test_parser failed"}),
	})

	node := TestNodeDynamic(cfg)
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusTestsFailed {
		t.Errorf("Status = %q, want %q", result.Status, StatusTestsFailed)
	}
}

func TestReviewNode_ApprovedRoutesToApproved(t *testing.T) {
	cfg, _ := templateConfig([][]provider.StreamEvent{
		completeResponse(ReviewOutput{Approved: true, Feedback: "looks good"}),
	})

	node := ReviewNodeDynamic(cfg)
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusReviewApproved {
		t.Errorf("Status = %q, want %q", result.Status, StatusReviewApproved)
	}
}

func TestReviewNode_RejectedRoutesToRejected(t *testing.T) {
	cfg, _ := templateConfig([][]provider.StreamEvent{
		completeResponse(ReviewOutput{Approved: false, Feedback: "missing error handling"}),
	})

	node := ReviewNodeDynamic(cfg)
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusReviewRejected {
		t.Errorf("Status = %q, want %q", result.Status, StatusReviewRejected)
	}
	if got := result.GetArtifactString("review.feedback"); got != "missing error handling" {
		t.Errorf("review.feedback = %q, want feedback preserved", got)
	}
}

func TestImplementNode_CallsToolThenCompletes(t *testing.T) {
	cfg, _ := templateConfig([][]provider.StreamEvent{
		toolCallResponse("call-1", "read_file", `{"path":"main.go"}`),
		completeResponse(ImplementOutput{Summary: "applied fix"}),
	})
	cfg.ToolExecutor = &mockToolExecutor{
		handler: func(_ context.Context, name string, _ json.RawMessage) (string, error) {
			return "package main", nil
		},
		defs: []tooldef.ToolDef{
			{Name: "read_file", Description: "Read a file"},
			{Name: "write_file", Description: "Write a file"},
			{Name: "edit_file", Description: "Edit a file"},
			{Name: "glob", Description: "Find files"},
			{Name: "grep", Description: "Search files"},
			{Name: "shell", Description: "Run a command"},
		},
	}

	node := ImplementNodeDynamic(cfg)
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result.GetArtifactString("implement.summary"); got != "applied fix" {
		t.Errorf("implement.summary = %q, want %q", got, "applied fix")
	}
}

// --- Graph-level tests ---

func TestBugFixGraph_HappyPath(t *testing.T) {
	cfg, prov := templateConfig([][]provider.StreamEvent{
		completeResponse(FindingsOutput{Summary: "found bug on line 42"}),
		completeResponse(PlanOutput{Summary: "fix the loop bound"}),
		completeResponse(ImplementOutput{Summary: "applied fix"}),
		completeResponse(TestOutput{Passed: true, Summary: "all tests pass"}),
		completeResponse(ReviewOutput{Approved: true, Feedback: "correct fix"}),
	})

	graph, err := BugFixGraph(cfg)
	if err != nil {
		t.Fatalf("failed to build graph: %v", err)
	}

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "Fix the parser bug")

	result, err := graph.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("graph.Run error: %v", err)
	}

	for _, key := range []string{"investigate.findings", "plan.steps", "implement.summary", "test.results", "review.feedback"} {
		if got := result.GetArtifactString(key); got == "" {
			t.Errorf("expected artifact %q to be set", key)
		}
	}
	if result.Status != StatusReviewApproved {
		t.Errorf("Status = %q, want %q", result.Status, StatusReviewApproved)
	}
	if prov.calls != 5 {
		t.Errorf("provider called %d times, want 5", prov.calls)
	}
}

func TestBugFixGraph_TestFailureRetry(t *testing.T) {
	cfg, prov := templateConfig([][]provider.StreamEvent{
		completeResponse(FindingsOutput{Summary: "finding"}),
		completeResponse(PlanOutput{Summary: "plan"}),
		completeResponse(ImplementOutput{Summary: "impl 1"}),
		completeResponse(TestOutput{Passed: false, Summary: "test failed"}),
		completeResponse(ImplementOutput{Summary: "impl 2"}),
		completeResponse(TestOutput{Passed: true, Summary: "tests pass"}),
		completeResponse(ReviewOutput{Approved: true, Feedback: "good"}),
	})

	graph, err := BugFixGraph(cfg)
	if err != nil {
		t.Fatalf("failed to build graph: %v", err)
	}

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "fix it")

	result, err := graph.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("graph.Run error: %v", err)
	}
	if result.Status != StatusReviewApproved {
		t.Errorf("Status = %q, want %q", result.Status, StatusReviewApproved)
	}
	if prov.calls != 7 {
		t.Errorf("provider called %d times, want 7", prov.calls)
	}
}

func TestBugFixGraph_ReviewRejectionRetry(t *testing.T) {
	cfg, prov := templateConfig([][]provider.StreamEvent{
		completeResponse(FindingsOutput{Summary: "finding"}),
		completeResponse(PlanOutput{Summary: "plan"}),
		completeResponse(ImplementOutput{Summary: "impl 1"}),
		completeResponse(TestOutput{Passed: true, Summary: "pass"}),
		completeResponse(ReviewOutput{Approved: false, Feedback: "missing error handling"}),
		completeResponse(ImplementOutput{Summary: "impl 2"}),
		completeResponse(TestOutput{Passed: true, Summary: "still passes"}),
		completeResponse(ReviewOutput{Approved: true, Feedback: "now good"}),
	})

	graph, err := BugFixGraph(cfg)
	if err != nil {
		t.Fatalf("failed to build graph: %v", err)
	}

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "fix it")

	result, err := graph.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("graph.Run error: %v", err)
	}
	if result.Status != StatusReviewApproved {
		t.Errorf("Status = %q, want %q", result.Status, StatusReviewApproved)
	}
	if prov.calls != 8 {
		t.Errorf("provider called %d times, want 8", prov.calls)
	}
}

func TestPrototypeGraph_CycleLimit(t *testing.T) {
	cfg, _ := templateConfig([][]provider.StreamEvent{
		completeResponse(ImplementOutput{Summary: "1"}),
		completeResponse(TestOutput{Passed: false, Summary: "fail"}),
		completeResponse(ImplementOutput{Summary: "2"}),
		completeResponse(TestOutput{Passed: false, Summary: "fail"}),
		completeResponse(ImplementOutput{Summary: "3"}),
		completeResponse(TestOutput{Passed: false, Summary: "fail"}),
	})

	graph, err := PrototypeGraph(cfg)
	if err != nil {
		t.Fatalf("failed to build graph: %v", err)
	}

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "build it")

	_, err = graph.Run(context.Background(), state)
	if err == nil {
		t.Fatal("expected ErrCycleLimit")
	}
	if !errors.Is(err, rhizome.ErrCycleLimit) {
		t.Errorf("error = %v, want ErrCycleLimit", err)
	}
}

func TestSingleWorkerGraph(t *testing.T) {
	cfg, _ := templateConfig([][]provider.StreamEvent{
		completeResponse(WorkOutput{Output: "done"}),
	})

	graph, err := SingleWorkerGraph(cfg, "You are a worker.", "Do the work.")
	if err != nil {
		t.Fatalf("failed to build graph: %v", err)
	}

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	result, err := graph.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("graph.Run error: %v", err)
	}
	if got := result.GetArtifactString("work.output"); got != "done" {
		t.Errorf("work.output = %q, want %q", got, "done")
	}
}

// --- Adapters & tools ---

func TestAdaptTools_Filtering(t *testing.T) {
	inner := &mockToolExecutor{
		defs: []tooldef.ToolDef{
			{Name: "read_file", Description: "r"},
			{Name: "write_file", Description: "w"},
			{Name: "shell", Description: "s"},
		},
	}
	tools := AdaptTools(inner, ReadOnlyTools)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool after filtering, got %d", len(tools))
	}
	if tools[0].Name != "read_file" {
		t.Errorf("tool[0].Name = %q, want read_file", tools[0].Name)
	}
}

func TestAdaptTools_NilAllowedMeansAll(t *testing.T) {
	inner := &mockToolExecutor{
		defs: []tooldef.ToolDef{
			{Name: "a", Description: ""},
			{Name: "b", Description: ""},
		},
	}
	tools := AdaptTools(inner, nil)
	if len(tools) != 2 {
		t.Errorf("expected 2 tools (nil allowed = all), got %d", len(tools))
	}
}

func TestAdaptTools_ExecuteForwards(t *testing.T) {
	called := ""
	inner := &mockToolExecutor{
		handler: func(_ context.Context, name string, _ json.RawMessage) (string, error) {
			called = name
			return "ok", nil
		},
		defs: []tooldef.ToolDef{{Name: "read_file", Description: ""}},
	}
	tools := AdaptTools(inner, nil)
	result, err := tools[0].Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" || called != "read_file" {
		t.Errorf("handler dispatch failed: result=%q called=%q", result, called)
	}
}

func TestAskUserTool_Shape(t *testing.T) {
	tool := AskUserTool()
	if tool.Name != InterruptKindAskUser {
		t.Errorf("Name = %q, want %q", tool.Name, InterruptKindAskUser)
	}
	if !strings.Contains(string(tool.Parameters), `"question"`) {
		t.Errorf("parameters missing question field: %s", tool.Parameters)
	}
}

// --- Executor + middleware ---

type mockEventSink struct {
	mu     sync.Mutex
	events []string
}

func (m *mockEventSink) record(ev string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, ev)
}
func (m *mockEventSink) snapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.events))
	copy(out, m.events)
	return out
}

func (m *mockEventSink) BroadcastGraphNodeStarted(_, _, node string) {
	m.record(fmt.Sprintf("node_started:%s", node))
}
func (m *mockEventSink) BroadcastGraphNodeCompleted(_, _, node, status string) {
	m.record(fmt.Sprintf("node_completed:%s:%s", node, status))
}
func (m *mockEventSink) BroadcastGraphCompleted(_, _, summary string) {
	m.record(fmt.Sprintf("graph_completed:%s", summary))
}
func (m *mockEventSink) BroadcastGraphFailed(_, _, errMsg string) {
	m.record(fmt.Sprintf("graph_failed:%s", errMsg))
}
func (m *mockEventSink) BroadcastTaskCompleted(_, _, teamID, _ string, hasNextTask bool) {
	m.record(fmt.Sprintf("task_completed:%s:%v", teamID, hasNextTask))
}
func (m *mockEventSink) BroadcastTaskFailed(_, _, teamID, errMsg string) {
	m.record(fmt.Sprintf("task_failed:%s:%s", teamID, errMsg))
}
func (m *mockEventSink) BroadcastPrompt(requestID, question string, _ []string, source string) {
	m.record(fmt.Sprintf("prompt:%s:%s:%s", source, requestID, question))
}
func (m *mockEventSink) BroadcastSessionText(sessionID, text string) {
	m.record(fmt.Sprintf("session_text:%s:%s", sessionID, text))
}

func TestExecutor_Execute(t *testing.T) {
	cfg, _ := templateConfig([][]provider.StreamEvent{
		completeResponse(WorkOutput{Output: "done"}),
	})

	graph, err := SingleWorkerGraph(cfg, "You are a worker.", "Do it.")
	if err != nil {
		t.Fatalf("failed to build graph: %v", err)
	}

	sink := &mockEventSink{}
	executor := NewExecutor(ExecutorConfig{EventSink: sink})

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	if err := executor.Execute(context.Background(), graph, state, "test-team"); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	events := sink.snapshot()
	has := func(prefix string) bool {
		for _, e := range events {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}
	if !has("node_started:work") {
		t.Error("expected node_started:work event")
	}
	if !has("node_completed:work") {
		t.Error("expected node_completed:work event")
	}
	if !has("graph_completed:") {
		t.Error("expected graph_completed event")
	}
}

// --- HITL interrupt handler ---

type capturingSink struct {
	mockEventSink
	promptMu sync.Mutex
	promptID string
}

func (c *capturingSink) BroadcastPrompt(requestID, question string, options []string, source string) {
	c.mockEventSink.BroadcastPrompt(requestID, question, options, source)
	c.promptMu.Lock()
	c.promptID = requestID
	c.promptMu.Unlock()
}
func (c *capturingSink) lastPromptID() string {
	c.promptMu.Lock()
	defer c.promptMu.Unlock()
	return c.promptID
}

func TestInterruptHandler_AskUser_RoutesThroughBroker(t *testing.T) {
	broker := hitl.New()
	sink := &capturingSink{}

	executor := NewExecutor(ExecutorConfig{EventSink: sink, Broker: broker})

	go func() {
		for i := 0; i < 100; i++ {
			if id := sink.lastPromptID(); id != "" {
				_ = broker.Respond(id, "42")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := executor.interruptHandler(ctx, rhizome.InterruptRequest{
		Node:    "investigate",
		Kind:    InterruptKindAskUser,
		Payload: AskUserPayload{Question: "?", Options: []string{"42", "other"}},
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if val, _ := resp.Value.(string); val != "42" {
		t.Errorf("resp.Value = %q, want %q", val, "42")
	}
}

// --- Prompt engine composition ---

func TestComposePrompt_UsesPromptEngine(t *testing.T) {
	engine := prompt.NewEngine()
	dir := t.TempDir()
	rolesDir := filepath.Join(dir, "roles")
	if err := os.Mkdir(rolesDir, 0755); err != nil {
		t.Fatal(err)
	}
	roleContent := `---
name: Test Investigator
description: Test role
mode: worker
---

TASK_MARKER: {{ globals.task.description }}
JOB_MARKER: {{ globals.job.title }}
`
	if err := os.WriteFile(filepath.Join(rolesDir, "test-investigator.md"), []byte(roleContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := engine.LoadDir(dir, "test"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	cfg := TemplateConfig{
		PromptEngine: engine,
		Roles:        RoleMap{Investigate: "test-investigator"},
	}
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "Find the bug")
	state.SetArtifact("job.title", "Parser reliability")

	got, err := composePrompt(cfg, cfg.Roles.Investigate, state)
	if err != nil {
		t.Fatalf("composePrompt: %v", err)
	}
	if !strings.Contains(got, "TASK_MARKER: Find the bug") {
		t.Errorf("prompt missing task override: %q", got)
	}
	if !strings.Contains(got, "JOB_MARKER: Parser reliability") {
		t.Errorf("prompt missing job override: %q", got)
	}
}

func TestDefaultRolesLoadFromBundledDefaults(t *testing.T) {
	engine := prompt.NewEngine()
	if err := engine.LoadDir("../../defaults/user", "user"); err != nil {
		t.Fatalf("LoadDir user: %v", err)
	}
	if err := engine.LoadDir("../../defaults/system", "system"); err != nil {
		t.Fatalf("LoadDir system: %v", err)
	}

	roles := DefaultRoles()
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "stub task")
	state.SetArtifact("job.title", "stub job")
	state.SetArtifact("job.description", "stub desc")
	state.SetArtifact("investigate.findings", "stub findings")
	state.SetArtifact("plan.steps", "stub plan")
	state.SetArtifact("implement.summary", "stub impl")
	state.SetArtifact("test.results", "stub tests")
	state.SetArtifact("review.feedback", "")

	cfg := TemplateConfig{PromptEngine: engine, Roles: roles}
	for _, roleName := range []string{roles.Investigate, roles.Plan, roles.Implement, roles.Test, roles.Review} {
		got, err := composePrompt(cfg, roleName, state)
		if err != nil {
			t.Errorf("compose %q: %v", roleName, err)
			continue
		}
		if got == "" {
			t.Errorf("compose %q: empty prompt", roleName)
		}
	}
}

// --- Per-workspace tool executor isolation ---

func TestExecutor_BuildToolExecutor_ScopedPerTask(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	if err := os.WriteFile(filepath.Join(dirA, "marker.txt"), []byte("A"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "marker.txt"), []byte("B"), 0644); err != nil {
		t.Fatal(err)
	}

	executor := NewExecutor(ExecutorConfig{})

	execA := executor.buildToolExecutor(dirA)
	execB := executor.buildToolExecutor(dirB)

	readArgs := json.RawMessage(`{"path":"marker.txt"}`)
	resultA, err := execA.Execute(context.Background(), "read_file", readArgs)
	if err != nil {
		t.Fatalf("execA.Execute: %v", err)
	}
	resultB, err := execB.Execute(context.Background(), "read_file", readArgs)
	if err != nil {
		t.Fatalf("execB.Execute: %v", err)
	}
	if !strings.Contains(resultA, "A") {
		t.Errorf("execA read %q, want A", resultA)
	}
	if !strings.Contains(resultB, "B") {
		t.Errorf("execB read %q, want B", resultB)
	}
	if resultA == resultB {
		t.Errorf("executors not scoped per-task (both read %q)", resultA)
	}
}

// --- Typed-output envelope ---

func TestTaskState_SetNodeOutputRoundTrip(t *testing.T) {
	s := NewTaskState("j", "t", "/w", "mock", "m")

	if err := s.SetNodeOutput("triage", FindingsOutput{Summary: "bug on line 42"}); err != nil {
		t.Fatalf("SetNodeOutput: %v", err)
	}

	raw := s.GetNodeOutput("triage")
	if len(raw) == 0 {
		t.Fatal("GetNodeOutput returned empty")
	}

	var got FindingsOutput
	if err := s.UnmarshalNodeOutput("triage", &got); err != nil {
		t.Fatalf("UnmarshalNodeOutput: %v", err)
	}
	if got.Summary != "bug on line 42" {
		t.Errorf("Summary = %q, want %q", got.Summary, "bug on line 42")
	}
}

func TestTaskState_UnmarshalNodeOutputMissingNode(t *testing.T) {
	s := NewTaskState("j", "t", "/w", "mock", "m")
	var got FindingsOutput
	if err := s.UnmarshalNodeOutput("missing", &got); err == nil {
		t.Fatal("expected error for missing node output")
	}
}

func TestBugFixGraph_PopulatesTypedEnvelope(t *testing.T) {
	cfg, _ := templateConfig([][]provider.StreamEvent{
		completeResponse(FindingsOutput{Summary: "finding"}),
		completeResponse(PlanOutput{Summary: "plan"}),
		completeResponse(ImplementOutput{Summary: "impl"}),
		completeResponse(TestOutput{Passed: true, Summary: "pass"}),
		completeResponse(ReviewOutput{Approved: true, Feedback: "ok"}),
	})

	graph, err := BugFixGraph(cfg)
	if err != nil {
		t.Fatalf("BugFixGraph: %v", err)
	}
	state := NewTaskState("j", "t", "/w", "mock", "m")
	state.SetArtifact("task.description", "fix it")

	result, err := graph.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("graph.Run: %v", err)
	}

	// Every role node should have recorded its typed output in the envelope
	// under its rhizome node ID. Executed here without middleware, so the
	// NodeContext fallback uses the role name.
	var findings FindingsOutput
	if err := result.UnmarshalNodeOutput("investigator", &findings); err != nil {
		t.Errorf("investigator output: %v", err)
	} else if findings.Summary != "finding" {
		t.Errorf("findings.Summary = %q, want %q", findings.Summary, "finding")
	}

	var test TestOutput
	if err := result.UnmarshalNodeOutput("tester", &test); err != nil {
		t.Errorf("tester output: %v", err)
	} else if !test.Passed {
		t.Errorf("test.Passed = false, want true")
	}

	var review ReviewOutput
	if err := result.UnmarshalNodeOutput("reviewer", &review); err != nil {
		t.Errorf("reviewer output: %v", err)
	} else if !review.Approved {
		t.Errorf("review.Approved = false, want true")
	}

	// Old artifacts path still works.
	if got := result.GetArtifactString("investigate.findings"); got != "finding" {
		t.Errorf("artifact investigate.findings = %q, want %q", got, "finding")
	}
}

// Compile-time interface checks.
var (
	_ rhizome.NodeFunc[*TaskState] = SingleWorkerNode(TemplateConfig{}, "", "")
	_ EventSink                    = (*mockEventSink)(nil)
	_ agent.Tool                   = AskUserTool()
)
