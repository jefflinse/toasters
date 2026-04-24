package graphexec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jefflinse/mycelium/agent"
	"github.com/jefflinse/rhizome"

	"github.com/jefflinse/toasters/internal/db"
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

// --- Test fixtures ---

// completeJSON returns stream events for a terminal complete() tool call
// with the given JSON payload.
func completeJSON(payload string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
			ID:        "call-complete",
			Name:      "complete",
			Arguments: json.RawMessage(payload),
		}},
	}
}

// summaryResp returns a complete() response for the default "summary" schema.
func summaryResp(summary string) []provider.StreamEvent {
	b, _ := json.Marshal(map[string]any{"summary": summary})
	return completeJSON(string(b))
}

// testResultResp returns a complete() response for the "test-result" schema.
func testResultResp(passed bool, summary string) []provider.StreamEvent {
	b, _ := json.Marshal(map[string]any{"passed": passed, "summary": summary})
	return completeJSON(string(b))
}

// reviewResp returns a complete() response for the "review-decision" schema.
func reviewResp(approved bool, feedback string) []provider.StreamEvent {
	b, _ := json.Marshal(map[string]any{"approved": approved, "feedback": feedback})
	return completeJSON(string(b))
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

// testEngine returns a prompt engine loaded with the standard system and
// user defaults (roles + schemas + instructions + toolchains). Tests call
// this to get a ready-to-use engine without hand-rolling role files in
// every test.
func testEngine(t testing.TB) *prompt.Engine {
	t.Helper()
	e := prompt.NewEngine()
	if err := e.LoadDir("../../defaults/system", "system"); err != nil {
		t.Fatalf("LoadDir system defaults: %v", err)
	}
	if err := e.LoadDir("../../defaults/user", "user"); err != nil {
		t.Fatalf("LoadDir user defaults: %v", err)
	}
	return e
}

func templateConfig(t testing.TB, responses [][]provider.StreamEvent) (TemplateConfig, *mockProvider) {
	t.Helper()
	prov := &mockProvider{responses: responses}
	toolExec := &mockToolExecutor{
		defs: []tooldef.ToolDef{
			{Name: "read_file", Description: "Read a file"},
			{Name: "write_file", Description: "Write a file"},
			{Name: "edit_file", Description: "Edit a file"},
			{Name: "glob", Description: "Find files"},
			{Name: "grep", Description: "Search files"},
			{Name: "shell", Description: "Run a command"},
			{Name: "query_graphs", Description: "List available graphs"},
		},
	}
	return TemplateConfig{
		Provider:     prov,
		ToolExecutor: toolExec,
		Model:        "test-model",
		PromptEngine: testEngine(t),
	}, prov
}

// --- Node-level tests ---

func TestRoleNode_TesterPassedRoutesViaOutput(t *testing.T) {
	cfg, _ := templateConfig(t, [][]provider.StreamEvent{
		testResultResp(true, "all tests pass"),
	})

	role := cfg.PromptEngine.Role("tester")
	if role == nil {
		t.Fatal("tester role not loaded")
	}
	node := RoleNode(cfg, role, "test")
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result.GetArtifactString("test.summary"); got != "all tests pass" {
		t.Errorf("test.summary = %q, want %q", got, "all tests pass")
	}
	var out map[string]any
	if err := result.UnmarshalNodeOutput("test", &out); err != nil {
		t.Fatalf("UnmarshalNodeOutput: %v", err)
	}
	if passed, _ := out["passed"].(bool); !passed {
		t.Errorf("node output passed = %v, want true", out["passed"])
	}
}

func TestRoleNode_ReviewerRejectedCarriesFeedback(t *testing.T) {
	cfg, _ := templateConfig(t, [][]provider.StreamEvent{
		reviewResp(false, "missing error handling"),
	})

	role := cfg.PromptEngine.Role("reviewer")
	node := RoleNode(cfg, role, "review")
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result.GetArtifactString("review.feedback"); got != "missing error handling" {
		t.Errorf("review.feedback = %q, want feedback preserved", got)
	}
	var out map[string]any
	_ = result.UnmarshalNodeOutput("review", &out)
	if approved, _ := out["approved"].(bool); approved {
		t.Error("approved = true, want false")
	}
}

func TestRoleNode_ImplementerToolCallThenComplete(t *testing.T) {
	cfg, _ := templateConfig(t, [][]provider.StreamEvent{
		toolCallResponse("call-1", "read_file", `{"path":"main.go"}`),
		summaryResp("applied fix"),
	})
	cfg.ToolExecutor = &mockToolExecutor{
		handler: func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
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

	role := cfg.PromptEngine.Role("implementer")
	node := RoleNode(cfg, role, "implement")
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result.GetArtifactString("implement.summary"); got != "applied fix" {
		t.Errorf("implement.summary = %q, want %q", got, "applied fix")
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
func (m *mockEventSink) BroadcastTaskCompleted(_, _, graphID, _ string, _ json.RawMessage, hasNextTask bool) {
	m.record(fmt.Sprintf("task_completed:%s:%v", graphID, hasNextTask))
}
func (m *mockEventSink) BroadcastTaskFailed(_, _, graphID, errMsg string) {
	m.record(fmt.Sprintf("task_failed:%s:%s", graphID, errMsg))
}
func (m *mockEventSink) BroadcastPrompt(requestID, question string, _ []string, source string) {
	m.record(fmt.Sprintf("prompt:%s:%s:%s", source, requestID, question))
}
func (m *mockEventSink) BroadcastSessionText(sessionID, text string) {
	m.record(fmt.Sprintf("session_text:%s:%s", sessionID, text))
}
func (m *mockEventSink) BroadcastSessionReasoning(sessionID, text string) {
	m.record(fmt.Sprintf("session_reasoning:%s:%s", sessionID, text))
}
func (m *mockEventSink) BroadcastSessionToolCall(sessionID, _, name string, _ json.RawMessage) {
	m.record(fmt.Sprintf("session_tool_call:%s:%s", sessionID, name))
}
func (m *mockEventSink) BroadcastSessionToolResult(sessionID, _, name, _, _ string) {
	m.record(fmt.Sprintf("session_tool_result:%s:%s", sessionID, name))
}

func TestExecutor_Execute(t *testing.T) {
	cfg, _ := templateConfig(t, [][]provider.StreamEvent{
		summaryResp("done"),
	})

	graph, err := Compile(&Definition{
		ID:    "solo",
		Entry: "work",
		Nodes: []Node{{ID: "work", Role: "investigator"}},
		Edges: []Edge{{From: "work", To: EndNode}},
	}, cfg, nil)
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

func TestExecutor_Execute_ForwardsSessionToolEvents(t *testing.T) {
	// Model issues one read_file call then terminates via complete.
	cfg, _ := templateConfig(t, [][]provider.StreamEvent{
		toolCallResponse("call-1", "read_file", `{"path":"main.go"}`),
		summaryResp("done"),
	})
	cfg.ToolExecutor = &mockToolExecutor{
		handler: func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
			return "package main", nil
		},
		defs: []tooldef.ToolDef{{Name: "read_file", Description: "r"}},
	}

	graph, err := Compile(&Definition{
		ID:    "solo",
		Entry: "work",
		Nodes: []Node{{ID: "work", Role: "investigator"}},
		Edges: []Edge{{From: "work", To: EndNode}},
	}, cfg, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	sink := &mockEventSink{}
	executor := NewExecutor(ExecutorConfig{EventSink: sink})
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	if err := executor.Execute(context.Background(), graph, state, "test-team"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	events := sink.snapshot()
	hasPrefix := func(prefix string) bool {
		for _, e := range events {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}
	if !hasPrefix("session_tool_call:graph:task-1:work:read_file") {
		t.Errorf("missing session_tool_call event; got %v", events)
	}
	if !hasPrefix("session_tool_result:graph:task-1:work:read_file") {
		t.Errorf("missing session_tool_result event; got %v", events)
	}
}

func TestExecutor_Execute_ForwardsSessionReasoning(t *testing.T) {
	// Provider emits a reasoning chunk before the terminal complete.
	reasoning := []provider.StreamEvent{
		{Type: provider.EventReasoning, Text: "pondering the task..."},
	}
	// Glue the reasoning chunk to the same turn as the complete call.
	firstTurn := append(reasoning, []provider.StreamEvent{
		{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
			ID:        "call-complete",
			Name:      "complete",
			Arguments: json.RawMessage(`{"summary":"done"}`),
		}},
	}...)
	cfg, _ := templateConfig(t, [][]provider.StreamEvent{firstTurn})

	graph, err := Compile(&Definition{
		ID:    "solo",
		Entry: "work",
		Nodes: []Node{{ID: "work", Role: "investigator"}},
		Edges: []Edge{{From: "work", To: EndNode}},
	}, cfg, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	sink := &mockEventSink{}
	executor := NewExecutor(ExecutorConfig{EventSink: sink})
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	if err := executor.Execute(context.Background(), graph, state, "test-team"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	events := sink.snapshot()
	want := "session_reasoning:graph:task-1:work:pondering the task..."
	found := false
	for _, e := range events {
		if e == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("missing session_reasoning event; got %v", events)
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
output: summary
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

	cfg := TemplateConfig{PromptEngine: engine}
	role := engine.Role("test-investigator")
	if role == nil {
		t.Fatal("role not loaded")
	}
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "Find the bug")
	state.SetArtifact("job.title", "Parser reliability")

	got, err := composePrompt(cfg, role, state)
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

func TestBundledRolesCompose(t *testing.T) {
	engine := testEngine(t)
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "stub task")
	state.SetArtifact("job.title", "stub job")
	state.SetArtifact("job.description", "stub desc")
	state.SetArtifact("investigate.summary", "stub findings")
	state.SetArtifact("plan.summary", "stub plan")
	state.SetArtifact("implement.summary", "stub impl")
	state.SetArtifact("test.summary", "stub tests")
	state.SetArtifact("review.feedback", "")

	cfg := TemplateConfig{PromptEngine: engine}
	for _, name := range []string{"investigator", "planner", "implementer", "tester", "reviewer"} {
		role := engine.Role(name)
		if role == nil {
			t.Errorf("role %q not loaded", name)
			continue
		}
		got, err := composePrompt(cfg, role, state)
		if err != nil {
			t.Errorf("compose %q: %v", name, err)
			continue
		}
		if got == "" {
			t.Errorf("compose %q: empty prompt", name)
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

	if err := s.SetNodeOutput("triage", map[string]any{"summary": "bug on line 42"}); err != nil {
		t.Fatalf("SetNodeOutput: %v", err)
	}

	raw := s.GetNodeOutput("triage")
	if len(raw) == 0 {
		t.Fatal("GetNodeOutput returned empty")
	}

	var got map[string]any
	if err := s.UnmarshalNodeOutput("triage", &got); err != nil {
		t.Fatalf("UnmarshalNodeOutput: %v", err)
	}
	if got["summary"] != "bug on line 42" {
		t.Errorf("Summary = %q, want %q", got["summary"], "bug on line 42")
	}
}

func TestTaskState_UnmarshalNodeOutputMissingNode(t *testing.T) {
	s := NewTaskState("j", "t", "/w", "mock", "m")
	var got map[string]any
	if err := s.UnmarshalNodeOutput("missing", &got); err == nil {
		t.Fatal("expected error for missing node output")
	}
}

func TestBugFixGraph_PopulatesTypedEnvelope(t *testing.T) {
	cfg, _ := templateConfig(t, [][]provider.StreamEvent{
		summaryResp("finding"),
		summaryResp("plan"),
		summaryResp("impl"),
		testResultResp(true, "pass"),
		reviewResp(true, "ok"),
	})

	graph, err := Compile(bugFixDef(), cfg, nil)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	state := NewTaskState("j", "t", "/w", "mock", "m")
	state.SetArtifact("task.description", "fix it")

	result, err := graph.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("graph.Run: %v", err)
	}

	var findings map[string]any
	if err := result.UnmarshalNodeOutput("investigate", &findings); err != nil {
		t.Errorf("investigate output: %v", err)
	} else if findings["summary"] != "finding" {
		t.Errorf("investigate.summary = %q, want %q", findings["summary"], "finding")
	}

	var tr map[string]any
	if err := result.UnmarshalNodeOutput("test", &tr); err != nil {
		t.Errorf("test output: %v", err)
	} else if passed, _ := tr["passed"].(bool); !passed {
		t.Errorf("test.passed = false, want true")
	}

	var rv map[string]any
	if err := result.UnmarshalNodeOutput("review", &rv); err != nil {
		t.Errorf("review output: %v", err)
	} else if approved, _ := rv["approved"].(bool); !approved {
		t.Errorf("review.approved = false, want true")
	}

	if got := result.GetArtifactString("investigate.summary"); got != "finding" {
		t.Errorf("artifact investigate.summary = %q, want %q", got, "finding")
	}
}

// --- Session persistence ---

func TestRoleNode_PersistsSessionTranscript(t *testing.T) {
	store := openInMemoryStore(t)

	cfg, _ := templateConfig(t, [][]provider.StreamEvent{
		summaryResp("investigation complete"),
	})
	cfg.Store = store

	role := cfg.PromptEngine.Role("investigator")
	if role == nil {
		t.Fatal("investigator not loaded")
	}
	node := RoleNode(cfg, role, "investigate")
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	if _, err := node(context.Background(), state); err != nil {
		t.Fatalf("node run: %v", err)
	}

	// Look up the worker_sessions row by task_id.
	sessions, err := store.ListSessionsForTask(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("querying sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 session row, got %d", len(sessions))
	}
	sess := sessions[0]
	if sess.WorkerID != "graph:investigate" {
		t.Errorf("worker_id = %q, want graph:investigate", sess.WorkerID)
	}
	if sess.Status != "completed" {
		t.Errorf("status = %q, want completed", sess.Status)
	}

	// Transcript must contain the user message and the assistant tool call.
	msgs, err := store.ListSessionMessages(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	if len(msgs) < 2 {
		t.Fatalf("want >=2 session messages, got %d", len(msgs))
	}
	hasAssistantCall := false
	for _, m := range msgs {
		if m.Role == "assistant" && m.ToolCalls != "" {
			hasAssistantCall = true
		}
	}
	if !hasAssistantCall {
		t.Error("transcript missing assistant message with tool call")
	}
}

func TestRoleNode_PersistsSessionOnMissingTerminal(t *testing.T) {
	store := openInMemoryStore(t)

	// Assistant replies with plain text, no tool calls → ErrNoTerminalTool.
	textOnly := []provider.StreamEvent{
		{Type: provider.EventText, Text: "here is the plan as prose — forgot to call complete"},
	}
	cfg, _ := templateConfig(t, [][]provider.StreamEvent{textOnly})
	cfg.Store = store

	role := cfg.PromptEngine.Role("planner")
	node := RoleNode(cfg, role, "plan")
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	_, err := node(context.Background(), state)
	if err == nil {
		t.Fatal("expected ErrNoTerminalTool propagation")
	}
	if !strings.Contains(err.Error(), "terminal") && !strings.Contains(err.Error(), "tool") {
		t.Logf("err = %v", err) // informational
	}

	// Transcript must still land in the DB so the failure is debuggable.
	sessions, err := store.ListSessionsForTask(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("querying sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 session row even on failure, got %d", len(sessions))
	}
	if sessions[0].Status != "failed" {
		t.Errorf("status = %q, want failed", sessions[0].Status)
	}
	msgs, err := store.ListSessionMessages(context.Background(), sessions[0].ID)
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected transcript messages even on failure")
	}
}

func openInMemoryStore(t *testing.T) db.Store {
	t.Helper()
	path := t.TempDir() + "/sessions.db"
	store, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// Compile-time interface checks.
var (
	_ EventSink  = (*mockEventSink)(nil)
	_ agent.Tool = AskUserTool()
)
