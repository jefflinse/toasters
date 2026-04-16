package graphexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jefflinse/rhizome"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
	"github.com/jefflinse/toasters/internal/tooldef"
)

// --- Mock provider ---

type mockProvider struct {
	// responses is a queue of responses. Each call to ChatStream pops
	// the first entry. Each entry is a slice of StreamEvents to send.
	responses [][]provider.StreamEvent
	calls     int
}

func (m *mockProvider) ChatStream(_ context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	if m.calls >= len(m.responses) {
		return nil, fmt.Errorf("mock provider: no more responses (call %d)", m.calls)
	}
	events := m.responses[m.calls]
	m.calls++

	ch := make(chan provider.StreamEvent, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func (m *mockProvider) Models(_ context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (m *mockProvider) Name() string { return "mock" }

// --- Mock tool executor ---

type mockToolExecutor struct {
	// handler is called for each tool execution.
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

// textResponse returns stream events for a simple text completion.
func textResponse(text string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventText, Text: text},
		{Type: provider.EventDone},
	}
}

// toolCallResponse returns stream events with a tool call.
func toolCallResponse(id, name, argsJSON string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
			ID:        id,
			Name:      name,
			Arguments: json.RawMessage(argsJSON),
		}},
		{Type: provider.EventDone},
	}
}

// decisionResponse returns stream events for a decision tool call.
// Used in tests to drive test/review node routing (these nodes terminate as
// soon as a decision tool fires, so no subsequent text response is needed).
func decisionResponse(tool, argField, msg string) []provider.StreamEvent {
	return toolCallResponse("dec-"+tool, tool, fmt.Sprintf(`{%q:%q}`, argField, msg))
}

// --- Tests ---

func TestLLMNode_SimpleCompletion(t *testing.T) {
	prov := &mockProvider{
		responses: [][]provider.StreamEvent{
			textResponse("Hello, world!"),
		},
	}

	node := LLMNode(NodeConfig{
		Provider:     prov,
		SystemPrompt: "You are a helpful assistant.",
		ArtifactKey:  "output",
	})

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "Say hello")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.FinalText != "Hello, world!" {
		t.Errorf("FinalText = %q, want %q", result.FinalText, "Hello, world!")
	}

	if got := result.GetArtifactString("output"); got != "Hello, world!" {
		t.Errorf("artifact 'output' = %q, want %q", got, "Hello, world!")
	}

	if prov.calls != 1 {
		t.Errorf("provider called %d times, want 1", prov.calls)
	}
}

func TestLLMNode_ToolCallThenCompletion(t *testing.T) {
	prov := &mockProvider{
		responses: [][]provider.StreamEvent{
			// Turn 1: LLM requests a tool call.
			toolCallResponse("call-1", "read_file", `{"path": "main.go"}`),
			// Turn 2: LLM responds with text after seeing tool result.
			textResponse("The file contains a main function."),
		},
	}

	toolExec := &mockToolExecutor{
		handler: func(_ context.Context, name string, _ json.RawMessage) (string, error) {
			if name == "read_file" {
				return "package main\n\nfunc main() {}", nil
			}
			return "", fmt.Errorf("unknown tool: %s", name)
		},
		defs: []tooldef.ToolDef{
			{Name: "read_file", Description: "Read a file"},
		},
	}

	node := LLMNode(NodeConfig{
		Provider:     prov,
		ToolExecutor: toolExec,
		SystemPrompt: "You are a code reader.",
		ArtifactKey:  "analysis",
	})

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "Read main.go")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.FinalText != "The file contains a main function." {
		t.Errorf("FinalText = %q, want %q", result.FinalText, "The file contains a main function.")
	}

	if prov.calls != 2 {
		t.Errorf("provider called %d times, want 2", prov.calls)
	}
}

func TestLLMNode_MaxTurnsExceeded(t *testing.T) {
	// Provider always returns a tool call — never completes.
	prov := &mockProvider{
		responses: [][]provider.StreamEvent{
			toolCallResponse("call-1", "read_file", `{}`),
			toolCallResponse("call-2", "read_file", `{}`),
			toolCallResponse("call-3", "read_file", `{}`),
		},
	}

	toolExec := &mockToolExecutor{
		defs: []tooldef.ToolDef{{Name: "read_file", Description: "Read a file"}},
	}

	node := LLMNode(NodeConfig{
		Provider:     prov,
		ToolExecutor: toolExec,
		MaxTurns:     3,
	})

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Node should set state.Err rather than returning an error.
	if result.Err == nil {
		t.Fatal("expected state.Err to be set on max turns")
	}

	if got := result.Err.Error(); got != "max turns (3) exceeded" {
		t.Errorf("state.Err = %q, want %q", got, "max turns (3) exceeded")
	}
}

func TestLLMNode_ToolResultCapping(t *testing.T) {
	largeResult := make([]byte, 16*1024) // 16KB
	for i := range largeResult {
		largeResult[i] = 'x'
	}

	prov := &mockProvider{
		responses: [][]provider.StreamEvent{
			toolCallResponse("call-1", "read_file", `{}`),
			textResponse("Done."),
		},
	}

	toolExec := &mockToolExecutor{
		handler: func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
			return string(largeResult), nil
		},
		defs: []tooldef.ToolDef{{Name: "read_file", Description: "Read a file"}},
	}

	node := LLMNode(NodeConfig{
		Provider:     prov,
		ToolExecutor: toolExec,
		ArtifactKey:  "output",
	})

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.FinalText != "Done." {
		t.Errorf("FinalText = %q, want %q", result.FinalText, "Done.")
	}
}

func TestLLMNode_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	prov := &mockProvider{
		responses: [][]provider.StreamEvent{
			textResponse("should not reach this"),
		},
	}

	node := LLMNode(NodeConfig{Provider: prov})

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	_, err := node(ctx, state)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestLLMNode_ToolExecutionError(t *testing.T) {
	prov := &mockProvider{
		responses: [][]provider.StreamEvent{
			toolCallResponse("call-1", "shell", `{"cmd": "false"}`),
			textResponse("The command failed."),
		},
	}

	toolExec := &mockToolExecutor{
		handler: func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
			return "", fmt.Errorf("exit code 1")
		},
		defs: []tooldef.ToolDef{{Name: "shell", Description: "Run a command"}},
	}

	node := LLMNode(NodeConfig{
		Provider:     prov,
		ToolExecutor: toolExec,
		ArtifactKey:  "output",
	})

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Tool errors are communicated to the LLM, not propagated as node errors.
	if result.FinalText != "The command failed." {
		t.Errorf("FinalText = %q, want %q", result.FinalText, "The command failed.")
	}
}

func TestLLMNode_NoToolExecutorWithToolCalls(t *testing.T) {
	prov := &mockProvider{
		responses: [][]provider.StreamEvent{
			toolCallResponse("call-1", "read_file", `{}`),
		},
	}

	// No ToolExecutor configured.
	node := LLMNode(NodeConfig{Provider: prov})

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	_, err := node(context.Background(), state)
	if err == nil {
		t.Fatal("expected error when LLM requests tools but no executor configured")
	}
}

func TestLLMNode_CustomInitialMessage(t *testing.T) {
	prov := &mockProvider{
		responses: [][]provider.StreamEvent{
			textResponse("Custom response"),
		},
	}

	node := LLMNode(NodeConfig{
		Provider:       prov,
		InitialMessage: "Do something specific",
		ArtifactKey:    "output",
	})

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.GetArtifactString("output") != "Custom response" {
		t.Errorf("artifact = %q, want %q", result.GetArtifactString("output"), "Custom response")
	}
}

func TestSingleWorkerGraph(t *testing.T) {
	prov := &mockProvider{
		responses: [][]provider.StreamEvent{
			textResponse("Task completed successfully."),
		},
	}

	toolExec := &mockToolExecutor{
		defs: []tooldef.ToolDef{{Name: "read_file", Description: "Read a file"}},
	}

	graph, err := SingleWorkerGraph(
		TemplateConfig{Provider: prov, ToolExecutor: toolExec, Model: "test-model"},
		"You are a worker.",
		"Do the work.",
	)
	if err != nil {
		t.Fatalf("failed to build graph: %v", err)
	}

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := graph.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("graph.Run error: %v", err)
	}

	if result.FinalText != "Task completed successfully." {
		t.Errorf("FinalText = %q, want %q", result.FinalText, "Task completed successfully.")
	}

	if got := result.GetArtifactString("work.output"); got != "Task completed successfully." {
		t.Errorf("artifact 'work.output' = %q, want %q", got, "Task completed successfully.")
	}
}

func TestSingleWorkerGraph_WithToolCalls(t *testing.T) {
	prov := &mockProvider{
		responses: [][]provider.StreamEvent{
			toolCallResponse("call-1", "read_file", `{"path": "go.mod"}`),
			textResponse("Module is github.com/example/project"),
		},
	}

	toolExec := &mockToolExecutor{
		handler: func(_ context.Context, name string, _ json.RawMessage) (string, error) {
			return "module github.com/example/project\n\ngo 1.26.2", nil
		},
		defs: []tooldef.ToolDef{{Name: "read_file", Description: "Read a file"}},
	}

	graph, err := SingleWorkerGraph(
		TemplateConfig{Provider: prov, ToolExecutor: toolExec, Model: "test-model"},
		"Read go.mod and report the module name.",
		"What is the module name?",
	)
	if err != nil {
		t.Fatalf("failed to build graph: %v", err)
	}

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := graph.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("graph.Run error: %v", err)
	}

	if result.FinalText != "Module is github.com/example/project" {
		t.Errorf("FinalText = %q, want %q", result.FinalText, "Module is github.com/example/project")
	}
}

// --- Phase 2: Multi-node graph tests ---

// templateConfig builds a TemplateConfig with a mock provider that returns
// the given responses in order. Each node in the graph consumes responses
// from the shared queue (one response per LLM turn per node).
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

func TestBugFixGraph_HappyPath(t *testing.T) {
	// Each investigative/planning/implementation node completes in one turn.
	// Test and review nodes terminate as soon as a decision tool is called.
	cfg, prov := templateConfig([][]provider.StreamEvent{
		textResponse("Found bug in parser.go: off-by-one error on line 42."),                // investigate
		textResponse("1. Fix the loop bound in parser.go\n2. Add test case."),               // plan
		textResponse("Applied fix to parser.go line 42."),                                   // implement
		decisionResponse("decide_tests_passed", "summary", "All 15 tests pass."),            // test → tests_passed
		decisionResponse("decide_approved", "reason", "Fix is correct and well-tested."),    // review → review_approved
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

	// Verify all artifacts were populated.
	if got := result.GetArtifactString("investigate.findings"); got == "" {
		t.Error("expected investigate.findings to be set")
	}
	if got := result.GetArtifactString("plan.steps"); got == "" {
		t.Error("expected plan.steps to be set")
	}
	if got := result.GetArtifactString("implement.summary"); got == "" {
		t.Error("expected implement.summary to be set")
	}
	if got := result.GetArtifactString("test.results"); got == "" {
		t.Error("expected test.results to be set")
	}
	if got := result.GetArtifactString("review.feedback"); got == "" {
		t.Error("expected review.feedback to be set")
	}

	if result.Status != "review_approved" {
		t.Errorf("Status = %q, want %q", result.Status, "review_approved")
	}

	if prov.calls != 5 {
		t.Errorf("provider called %d times, want 5", prov.calls)
	}
}

func TestBugFixGraph_TestFailureRetry(t *testing.T) {
	// Flow: investigate → plan → implement → test(FAIL) → implement → test(PASS) → review(APPROVED)
	cfg, prov := templateConfig([][]provider.StreamEvent{
		textResponse("Investigation findings."),                                              // investigate
		textResponse("Implementation plan."),                                                 // plan
		textResponse("First implementation attempt."),                                        // implement (1st)
		decisionResponse("decide_tests_failed", "summary", "test_parser_edge failed."),      // test (1st) → implement
		textResponse("Revised implementation with fix."),                                     // implement (2nd)
		decisionResponse("decide_tests_passed", "summary", "All tests pass."),               // test (2nd) → review
		decisionResponse("decide_approved", "reason", "Revision looks good."),               // review → End
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

	if result.Status != "review_approved" {
		t.Errorf("Status = %q, want %q", result.Status, "review_approved")
	}

	if prov.calls != 7 {
		t.Errorf("provider called %d times, want 7", prov.calls)
	}
}

func TestBugFixGraph_ReviewRejectionRetry(t *testing.T) {
	// Flow: investigate → plan → implement → test(PASS) → review(REJECTED) → implement → test(PASS) → review(APPROVED)
	cfg, prov := templateConfig([][]provider.StreamEvent{
		textResponse("Investigation findings."),                                                    // investigate
		textResponse("Implementation plan."),                                                       // plan
		textResponse("First implementation."),                                                      // implement (1st)
		decisionResponse("decide_tests_passed", "summary", "Tests pass."),                         // test (1st) → review
		decisionResponse("decide_rejected", "feedback", "Missing error handling in foo()."),       // review (1st) → implement
		textResponse("Revised with error handling."),                                               // implement (2nd)
		decisionResponse("decide_tests_passed", "summary", "Tests still pass."),                   // test (2nd) → review
		decisionResponse("decide_approved", "reason", "Error handling looks correct."),            // review (2nd) → End
	})

	graph, err := BugFixGraph(cfg)
	if err != nil {
		t.Fatalf("failed to build graph: %v", err)
	}

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "Fix the bug")

	result, err := graph.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("graph.Run error: %v", err)
	}

	if result.Status != "review_approved" {
		t.Errorf("Status = %q, want %q", result.Status, "review_approved")
	}

	if prov.calls != 8 {
		t.Errorf("provider called %d times, want 8", prov.calls)
	}
}

func TestPrototypeGraph_HappyPath(t *testing.T) {
	cfg, prov := templateConfig([][]provider.StreamEvent{
		textResponse("Implemented the prototype."),                                    // implement
		decisionResponse("decide_tests_passed", "summary", "Prototype tests pass."),   // test → End
	})

	graph, err := PrototypeGraph(cfg)
	if err != nil {
		t.Fatalf("failed to build graph: %v", err)
	}

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "Build a prototype")

	result, err := graph.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("graph.Run error: %v", err)
	}

	if result.Status != "tests_passed" {
		t.Errorf("Status = %q, want %q", result.Status, "tests_passed")
	}

	if prov.calls != 2 {
		t.Errorf("provider called %d times, want 2", prov.calls)
	}
}

func TestPrototypeGraph_CycleLimit(t *testing.T) {
	// Tests always fail → implement/test cycle hits maxNodeExecs(3).
	// implement runs 3 times, test runs 3 times, then implement exec 4 → ErrCycleLimit.
	cfg, _ := templateConfig([][]provider.StreamEvent{
		textResponse("Attempt 1."),                                                // implement (1)
		decisionResponse("decide_tests_failed", "summary", "Tests fail."),         // test (1) → implement
		textResponse("Attempt 2."),                                                // implement (2)
		decisionResponse("decide_tests_failed", "summary", "Tests still fail."),   // test (2) → implement
		textResponse("Attempt 3."),                                                // implement (3)
		decisionResponse("decide_tests_failed", "summary", "Tests fail again."),   // test (3) → implement
		// implement exec 4 → ErrCycleLimit (no LLM call)
	})

	graph, err := PrototypeGraph(cfg)
	if err != nil {
		t.Fatalf("failed to build graph: %v", err)
	}

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "Build something")

	_, err = graph.Run(context.Background(), state)
	if err == nil {
		t.Fatal("expected ErrCycleLimit error")
	}
	if !errors.Is(err, rhizome.ErrCycleLimit) {
		t.Errorf("error = %v, want ErrCycleLimit", err)
	}
}

func TestNewFeatureGraph_HappyPath(t *testing.T) {
	// No investigation phase.
	cfg, prov := templateConfig([][]provider.StreamEvent{
		textResponse("Plan: add new endpoint."),                                                     // plan
		textResponse("Implemented the endpoint."),                                                   // implement
		decisionResponse("decide_tests_passed", "summary", "Endpoint tests pass."),                 // test → review
		decisionResponse("decide_approved", "reason", "Endpoint is well-implemented."),             // review → End
	})

	graph, err := NewFeatureGraph(cfg)
	if err != nil {
		t.Fatalf("failed to build graph: %v", err)
	}

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "Add /health endpoint")

	result, err := graph.Run(context.Background(), state)
	if err != nil {
		t.Fatalf("graph.Run error: %v", err)
	}

	if result.Status != "review_approved" {
		t.Errorf("Status = %q, want %q", result.Status, "review_approved")
	}

	// No investigation artifact should exist.
	if got := result.GetArtifactString("investigate.findings"); got != "" {
		t.Errorf("investigate.findings should be empty, got %q", got)
	}

	if prov.calls != 4 {
		t.Errorf("provider called %d times, want 4", prov.calls)
	}
}

func TestFilterTools(t *testing.T) {
	inner := &mockToolExecutor{
		handler: func(_ context.Context, name string, _ json.RawMessage) (string, error) {
			return "executed " + name, nil
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

	filtered := FilterTools(inner, ReadOnlyTools)

	// Definitions should only include allowed tools.
	defs := filtered.Definitions()
	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, name := range ReadOnlyTools {
		if !names[name] {
			t.Errorf("expected %q in filtered definitions", name)
		}
	}
	if names["write_file"] {
		t.Error("write_file should not be in read-only filtered definitions")
	}
	if names["shell"] {
		t.Error("shell should not be in read-only filtered definitions")
	}

	// Execute should work for allowed tools.
	result, err := filtered.Execute(context.Background(), "read_file", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "executed read_file" {
		t.Errorf("result = %q, want %q", result, "executed read_file")
	}

	// Execute should reject disallowed tools.
	_, err = filtered.Execute(context.Background(), "write_file", nil)
	if err == nil {
		t.Fatal("expected error for disallowed tool")
	}
}

// --- Phase 3: Middleware and executor tests ---

type mockEventSink struct {
	events []string // collected event descriptions
}

func (m *mockEventSink) BroadcastGraphNodeStarted(jobID, taskID, node string) {
	m.events = append(m.events, fmt.Sprintf("node_started:%s", node))
}

func (m *mockEventSink) BroadcastGraphNodeCompleted(jobID, taskID, node, status string) {
	m.events = append(m.events, fmt.Sprintf("node_completed:%s:%s", node, status))
}

func (m *mockEventSink) BroadcastGraphCompleted(jobID, taskID, summary string) {
	m.events = append(m.events, fmt.Sprintf("graph_completed:%s", summary))
}

func (m *mockEventSink) BroadcastGraphFailed(jobID, taskID, errMsg string) {
	m.events = append(m.events, fmt.Sprintf("graph_failed:%s", errMsg))
}

func TestEventMiddleware(t *testing.T) {
	sink := &mockEventSink{}
	mw := EventMiddleware(sink)

	// Create a simple node that sets status.
	node := func(_ context.Context, state *TaskState) (*TaskState, error) {
		state.Status = "ok"
		return state, nil
	}

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	result, err := mw(context.Background(), "test_node", state, node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "ok" {
		t.Errorf("Status = %q, want %q", result.Status, "ok")
	}

	if len(sink.events) != 2 {
		t.Fatalf("expected 2 events, got %d: %v", len(sink.events), sink.events)
	}
	if sink.events[0] != "node_started:test_node" {
		t.Errorf("event[0] = %q, want %q", sink.events[0], "node_started:test_node")
	}
	if sink.events[1] != "node_completed:test_node:ok" {
		t.Errorf("event[1] = %q, want %q", sink.events[1], "node_completed:test_node:ok")
	}
}

func TestLoggingMiddleware(t *testing.T) {
	mw := LoggingMiddleware()

	node := func(_ context.Context, state *TaskState) (*TaskState, error) {
		state.FinalText = "done"
		return state, nil
	}

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	result, err := mw(context.Background(), "test_node", state, node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalText != "done" {
		t.Errorf("FinalText = %q, want %q", result.FinalText, "done")
	}
}

func TestExecutor_Execute(t *testing.T) {
	prov := &mockProvider{
		responses: [][]provider.StreamEvent{
			textResponse("Task completed."),
		},
	}

	toolExec := &mockToolExecutor{
		defs: []tooldef.ToolDef{{Name: "read_file", Description: "Read a file"}},
	}

	graph, err := SingleWorkerGraph(
		TemplateConfig{Provider: prov, ToolExecutor: toolExec, Model: "test-model"},
		"You are a worker.",
		"Do the task.",
	)
	if err != nil {
		t.Fatalf("failed to build graph: %v", err)
	}

	sink := &mockEventSink{}
	executor := NewExecutor(ExecutorConfig{
		EventSink: sink,
	})

	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	err = executor.Execute(context.Background(), graph, state)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Should have node events + graph completed event.
	hasNodeStarted := false
	hasNodeCompleted := false
	hasGraphCompleted := false
	for _, ev := range sink.events {
		if ev == "node_started:work" {
			hasNodeStarted = true
		}
		if strings.Contains(ev, "node_completed:work") {
			hasNodeCompleted = true
		}
		if strings.Contains(ev, "graph_completed:") {
			hasGraphCompleted = true
		}
	}
	if !hasNodeStarted {
		t.Error("expected node_started:work event")
	}
	if !hasNodeCompleted {
		t.Error("expected node_completed:work event")
	}
	if !hasGraphCompleted {
		t.Error("expected graph_completed event")
	}
}

func TestGraphExecution_WithMiddleware(t *testing.T) {
	// End-to-end: BugFixGraph with all middleware applied.
	cfg, _ := templateConfig([][]provider.StreamEvent{
		textResponse("Found the bug."),                                              // investigate
		textResponse("Plan to fix."),                                                // plan
		textResponse("Applied fix."),                                                // implement
		decisionResponse("decide_tests_passed", "summary", "All tests pass."),      // test
		decisionResponse("decide_approved", "reason", "Looks good."),               // review
	})

	graph, err := BugFixGraph(cfg)
	if err != nil {
		t.Fatalf("failed to build graph: %v", err)
	}

	sink := &mockEventSink{}
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "Fix the bug")

	result, err := graph.Run(context.Background(), state,
		rhizome.WithMiddleware[*TaskState](
			LoggingMiddleware(),
			EventMiddleware(sink),
		),
	)
	if err != nil {
		t.Fatalf("graph.Run error: %v", err)
	}

	if result.Status != "review_approved" {
		t.Errorf("Status = %q, want %q", result.Status, "review_approved")
	}

	// Should have 5 node_started + 5 node_completed = 10 events.
	startedCount := 0
	completedCount := 0
	for _, ev := range sink.events {
		if strings.Contains(ev, "node_started:") {
			startedCount++
		}
		if strings.Contains(ev, "node_completed:") {
			completedCount++
		}
	}
	if startedCount != 5 {
		t.Errorf("node_started events = %d, want 5", startedCount)
	}
	if completedCount != 5 {
		t.Errorf("node_completed events = %d, want 5", completedCount)
	}
}

// TestComposePrompt_UsesPromptEngine verifies that node builders compose
// prompts via the prompt engine using role-based markdown, not hardcoded
// text. Regression guard for the bug where graphexec bypassed the engine
// and shipped one-paragraph prompts while the team-lead path used rich
// composed roles.
func TestComposePrompt_UsesPromptEngine(t *testing.T) {
	engine := prompt.NewEngine()
	// Write a minimal role file to a temp dir and load it.
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

// TestDefaultRolesLoadFromBundledDefaults verifies that the 5 phase roles
// (investigator/planner/implementer/tester/reviewer) actually exist and
// compose cleanly against a prompt engine loading defaults/user/roles/.
// Without this, the graph would ship with silently-missing roles —
// prompt.Engine.Compose returns "role not found" errors.
func TestDefaultRolesLoadFromBundledDefaults(t *testing.T) {
	engine := prompt.NewEngine()
	// Load from the real repo defaults. Test file lives in
	// internal/graphexec/node_test.go, so ../../defaults is the repo root's
	// defaults directory.
	if err := engine.LoadDir("../../defaults/user", "user"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if err := engine.LoadDir("../../defaults/system", "system"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	roles := DefaultRoles()
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")
	state.SetArtifact("task.description", "stub task")
	state.SetArtifact("job.title", "stub job")
	state.SetArtifact("job.description", "stub description")
	state.SetArtifact("investigate.findings", "stub findings")
	state.SetArtifact("plan.steps", "stub plan")
	state.SetArtifact("implement.summary", "stub impl")
	state.SetArtifact("test.results", "stub tests")
	state.SetArtifact("review.feedback", "")

	cfg := TemplateConfig{PromptEngine: engine, Roles: roles}
	for _, roleName := range []string{roles.Investigate, roles.Plan, roles.Implement, roles.Test, roles.Review} {
		prompt, err := composePrompt(cfg, roleName, state)
		if err != nil {
			t.Errorf("compose %q: %v", roleName, err)
			continue
		}
		if prompt == "" {
			t.Errorf("compose %q: empty prompt", roleName)
		}
	}
}

// TestReviewNode_NoDecisionIsError verifies the force-fail safety net:
// if the LLM ends the review node without calling a decision tool, the
// node must return an error so the graph halts instead of silently routing
// with stale state. Mirrors runtime.watchTeamLeadForCompletion's pattern.
func TestReviewNode_NoDecisionIsError(t *testing.T) {
	cfg, _ := templateConfig([][]provider.StreamEvent{
		textResponse("I reviewed the code but forgot to call a decision tool."),
	})

	node := ReviewNodeDynamic(cfg)
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	_, err := node(context.Background(), state)
	if err == nil {
		t.Fatal("expected error when review node ends without a decision tool call")
	}
	if !strings.Contains(err.Error(), "decide_approved") {
		t.Errorf("error = %v, want message mentioning decide_approved", err)
	}
	if state.Status != "" {
		t.Errorf("state.Status = %q, want empty (no decision was recorded)", state.Status)
	}
}

// TestTestNode_NoDecisionIsError — same safety net for the test node.
func TestTestNode_NoDecisionIsError(t *testing.T) {
	cfg, _ := templateConfig([][]provider.StreamEvent{
		textResponse("Ran tests but didn't record the outcome."),
	})

	node := TestNodeDynamic(cfg)
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	_, err := node(context.Background(), state)
	if err == nil {
		t.Fatal("expected error when test node ends without a decision tool call")
	}
}

// TestReviewNode_RejectedRoutesCorrectly verifies the regression-prone case:
// a "not approved" decision should NOT route to approval. Previous substring
// parsing matched "not approved" as "approved" and silently routed rejected
// work to End.
func TestReviewNode_RejectedRoutesCorrectly(t *testing.T) {
	cfg, _ := templateConfig([][]provider.StreamEvent{
		decisionResponse("decide_rejected", "feedback", "This work is not approved — it needs revision."),
	})

	node := ReviewNodeDynamic(cfg)
	state := NewTaskState("job-1", "task-1", "/workspace", "mock", "test-model")

	result, err := node(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "review_rejected" {
		t.Errorf("Status = %q, want %q — decision tool was decide_rejected", result.Status, "review_rejected")
	}
	if feedback := result.GetArtifactString("review.feedback"); !strings.Contains(feedback, "not approved") {
		t.Errorf("review.feedback = %q, want feedback text preserved", feedback)
	}
}

// TestExecutor_BuildToolExecutor_ScopedPerTask verifies that each task gets
// its own tool executor scoped to its workspace directory, not a shared
// server-wide executor. Regression test for the bug where cmd/serve.go built
// one CoreTools at startup and every graph task used it, causing file
// operations to hit the global workspace instead of the task's subdir.
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
		t.Errorf("execA read %q, want content containing %q", resultA, "A")
	}
	if !strings.Contains(resultB, "B") {
		t.Errorf("execB read %q, want content containing %q", resultB, "B")
	}
	if resultA == resultB {
		t.Errorf("execA and execB returned identical content (%q) — tool executors are not scoped per-task", resultA)
	}
}

// Verify compile-time type compatibility.
var _ rhizome.NodeFunc[*TaskState] = LLMNode(NodeConfig{})
var _ EventSink = (*mockEventSink)(nil)
