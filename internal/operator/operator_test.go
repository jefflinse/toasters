package operator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// --- Test helpers ---

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func assertEqual(t *testing.T, want, got string) {
	t.Helper()
	if want != got {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("expected %q to contain %q", s, substr)
	}
}

// --- Mock provider ---

// mockProvider implements provider.Provider for testing. It returns
// pre-configured responses in order, one per ChatStream call.
type mockProvider struct {
	name      string
	responses []mockResponse
	mu        sync.Mutex
	callIdx   int
	requests  []provider.ChatRequest // captured for inspection
}

type mockResponse struct {
	events []provider.StreamEvent
	err    error
	block  bool // if true, channel stays open until context is cancelled
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.requests = append(m.requests, req)

	if m.callIdx >= len(m.responses) {
		return nil, errors.New("no more mock responses")
	}

	resp := m.responses[m.callIdx]
	m.callIdx++

	if resp.err != nil {
		return nil, resp.err
	}

	ch := make(chan provider.StreamEvent, len(resp.events)+1)
	for _, ev := range resp.events {
		ch <- ev
	}

	if resp.block {
		go func() {
			<-ctx.Done()
			close(ch)
		}()
	} else {
		close(ch)
	}
	return ch, nil
}

func (m *mockProvider) Models(_ context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (m *mockProvider) getRequests() []provider.ChatRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]provider.ChatRequest, len(m.requests))
	copy(cp, m.requests)
	return cp
}

// --- Tests ---

func TestOperatorProcessesUserMessage(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Hello from operator"},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
	})
	op.OnText = func(text string) {
		mu.Lock()
		textBuf.WriteString(text)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	op.Send(Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Hello"},
	})

	// Wait for processing.
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return textBuf.Len() > 0
	}, 2*time.Second)

	mu.Lock()
	got := textBuf.String()
	mu.Unlock()

	assertEqual(t, "Hello from operator", got)
}

func TestOperatorLongLivedSession(t *testing.T) {
	// The operator should maintain conversation context across multiple user
	// messages. We verify by checking that the second ChatStream call receives
	// the full message history (user + assistant + user).
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			// Response to first user message.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "First response"},
				{Type: provider.EventDone},
			}},
			// Response to second user message.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Second response"},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
	})
	op.OnText = func(text string) {
		mu.Lock()
		textBuf.WriteString(text)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	// Send first message.
	op.Send(Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "First message"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "First response")
	}, 2*time.Second)

	// Send second message.
	op.Send(Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Second message"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "Second response")
	}, 2*time.Second)

	// Verify the second ChatStream call received the full history.
	reqs := mp.getRequests()
	if len(reqs) != 2 {
		t.Fatalf("want 2 ChatStream calls, got %d", len(reqs))
	}

	// First call: 1 user message.
	if len(reqs[0].Messages) != 1 {
		t.Fatalf("first call: want 1 message, got %d", len(reqs[0].Messages))
	}
	assertEqual(t, "user", reqs[0].Messages[0].Role)
	assertEqual(t, "First message", reqs[0].Messages[0].Content)

	// Second call: user + assistant + user = 3 messages.
	if len(reqs[1].Messages) != 3 {
		t.Fatalf("second call: want 3 messages, got %d", len(reqs[1].Messages))
	}
	assertEqual(t, "user", reqs[1].Messages[0].Role)
	assertEqual(t, "assistant", reqs[1].Messages[1].Role)
	assertEqual(t, "user", reqs[1].Messages[2].Role)
	assertEqual(t, "Second message", reqs[1].Messages[2].Content)

	// Verify message count reflects the full history.
	if op.MessageCount() != 4 { // user, assistant, user, assistant
		t.Fatalf("want 4 messages in history, got %d", op.MessageCount())
	}
}

func TestOperatorConsultAgent(t *testing.T) {
	// The operator calls consult_agent("planner", "..."), which spawns a
	// fresh session via runtime.SpawnAndWait. We need two providers:
	// one for the operator and one for the planner agent.
	//
	// Since both use the same registry, we use a single mock provider that
	// handles multiple ChatStream calls in order:
	//   1. Operator's first response: tool call to consult_agent
	//   2. Planner agent's response: "Here is the plan: ..."
	//   3. Operator's second response (after seeing tool result): final text
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			// 1. Operator calls consult_agent.
			{events: []provider.StreamEvent{
				{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:        "call-1",
					Name:      "consult_agent",
					Arguments: json.RawMessage(`{"agent_name": "planner", "message": "Plan a web app"}`),
				}},
				{Type: provider.EventDone},
			}},
			// 2. Planner agent responds.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Plan: 1) Setup 2) Build 3) Deploy"},
				{Type: provider.EventDone},
			}},
			// 3. Operator responds after seeing planner's result.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "The planner suggests: Setup, Build, Deploy"},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
	})
	op.OnText = func(text string) {
		mu.Lock()
		textBuf.WriteString(text)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	op.Send(Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Build me a web app"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "planner suggests")
	}, 5*time.Second)

	mu.Lock()
	got := textBuf.String()
	mu.Unlock()

	assertContains(t, got, "planner suggests")

	// Verify the operator's message history includes the tool call round-trip.
	// Expected: user, assistant (tool call), tool (result), assistant (final).
	if op.MessageCount() != 4 {
		t.Fatalf("want 4 messages in history, got %d", op.MessageCount())
	}
}

func TestOperatorEventCallbackFires(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "ok"},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	var events []Event
	var mu sync.Mutex

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
	})
	op.OnEvent = func(ev Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	op.Send(Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Hello"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) > 0
	}, 2*time.Second)

	mu.Lock()
	defer mu.Unlock()

	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if events[0].Type != EventUserMessage {
		t.Fatalf("want EventUserMessage, got %s", events[0].Type)
	}
}

func TestOperatorMechanicalEvents(t *testing.T) {
	// Mechanical events (task_completed, task_failed, blocker_reported) should
	// be handled without calling the LLM. We verify by checking that no
	// ChatStream calls are made.
	mp := &mockProvider{
		name:      "test",
		responses: []mockResponse{}, // no responses — LLM should not be called
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	var events []Event
	var mu sync.Mutex

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
	})
	op.OnEvent = func(ev Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	// Send mechanical events.
	op.Send(Event{
		Type:    EventTaskCompleted,
		Payload: TaskCompletedPayload{TaskID: "task-1", Summary: "Done"},
	})
	op.Send(Event{
		Type:    EventTaskFailed,
		Payload: TaskFailedPayload{TaskID: "task-2", Error: "timeout"},
	})
	op.Send(Event{
		Type:    EventBlockerReported,
		Payload: BlockerReportedPayload{AgentID: "agent-1", Description: "stuck"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) >= 3
	}, 2*time.Second)

	mu.Lock()
	defer mu.Unlock()

	if len(events) != 3 {
		t.Fatalf("want 3 events, got %d", len(events))
	}

	// Verify no ChatStream calls were made.
	reqs := mp.getRequests()
	if len(reqs) != 0 {
		t.Fatalf("want 0 ChatStream calls for mechanical events, got %d", len(reqs))
	}
}

func TestOperatorCleanShutdown(t *testing.T) {
	mp := &mockProvider{
		name:      "test",
		responses: []mockResponse{},
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	op.Start(ctx)

	// Cancel should cause the event loop to exit cleanly.
	cancel()

	// Give the goroutine time to exit. If it doesn't, the test will pass
	// but the goroutine leak detector (if any) would catch it.
	time.Sleep(50 * time.Millisecond)
}

func TestOperatorChatStreamError(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{err: errors.New("connection refused")},
		},
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
	})
	op.OnText = func(text string) {
		mu.Lock()
		textBuf.WriteString(text)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	op.Send(Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Hello"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return textBuf.Len() > 0
	}, 2*time.Second)

	mu.Lock()
	got := textBuf.String()
	mu.Unlock()

	assertContains(t, got, "operator error")
	assertContains(t, got, "connection refused")
}

func TestOperatorStreamError(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "partial"},
				{Type: provider.EventError, Error: errors.New("stream broke")},
			}},
		},
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
	})
	op.OnText = func(text string) {
		mu.Lock()
		textBuf.WriteString(text)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	op.Send(Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Hello"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "operator error")
	}, 2*time.Second)

	mu.Lock()
	got := textBuf.String()
	mu.Unlock()

	// Should have received partial text before the error.
	assertContains(t, got, "partial")
	assertContains(t, got, "stream broke")
}

func TestOperatorSurfaceToUser(t *testing.T) {
	// Operator calls surface_to_user, gets the result back, then responds.
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			// Operator calls surface_to_user.
			{events: []provider.StreamEvent{
				{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:        "call-1",
					Name:      "surface_to_user",
					Arguments: json.RawMessage(`{"text": "Important update"}`),
				}},
				{Type: provider.EventDone},
			}},
			// Operator responds after seeing tool result.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "I've surfaced the update."},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := runtime.New(nil, newTestRegistry(mp))

	var textBuf strings.Builder
	var mu sync.Mutex

	op := New(Config{
		Runtime:  rt,
		Provider: mp,
		Model:    "test-model",
		WorkDir:  t.TempDir(),
	})
	op.OnText = func(text string) {
		mu.Lock()
		textBuf.WriteString(text)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op.Start(ctx)

	op.Send(Event{
		Type:    EventUserMessage,
		Payload: UserMessagePayload{Text: "Give me an update"},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(textBuf.String(), "surfaced the update")
	}, 2*time.Second)
}

func TestConsultAgentUnknownAgent(t *testing.T) {
	tools := newOperatorTools(nil, "test", "model", t.TempDir())

	_, err := tools.Execute(context.Background(), "consult_agent",
		json.RawMessage(`{"agent_name": "nonexistent", "message": "hello"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "unknown agent")
}

func TestConsultAgentMissingParams(t *testing.T) {
	tools := newOperatorTools(nil, "test", "model", t.TempDir())

	// Missing agent_name.
	_, err := tools.Execute(context.Background(), "consult_agent",
		json.RawMessage(`{"message": "hello"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "agent_name is required")

	// Missing message.
	_, err = tools.Execute(context.Background(), "consult_agent",
		json.RawMessage(`{"agent_name": "planner"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "message is required")
}

func TestSurfaceToUserMissingText(t *testing.T) {
	tools := newOperatorTools(nil, "test", "model", t.TempDir())

	_, err := tools.Execute(context.Background(), "surface_to_user",
		json.RawMessage(`{}`))
	assertError(t, err)
	assertContains(t, err.Error(), "text is required")
}

func TestOperatorToolsUnknownTool(t *testing.T) {
	tools := newOperatorTools(nil, "test", "model", t.TempDir())

	_, err := tools.Execute(context.Background(), "nonexistent", json.RawMessage(`{}`))
	assertError(t, err)
	if !errors.Is(err, runtime.ErrUnknownTool) {
		t.Fatalf("want ErrUnknownTool, got %v", err)
	}
}

func TestOperatorToolDefinitions(t *testing.T) {
	tools := newOperatorTools(nil, "test", "model", t.TempDir())
	defs := tools.Definitions()

	if len(defs) != 2 {
		t.Fatalf("want 2 tool definitions, got %d", len(defs))
	}

	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}

	if !names["consult_agent"] {
		t.Error("expected consult_agent in definitions")
	}
	if !names["surface_to_user"] {
		t.Error("expected surface_to_user in definitions")
	}
}

// --- Helpers ---

func newTestRegistry(mp *mockProvider) *provider.Registry {
	reg := provider.NewRegistry()
	reg.Register(mp.Name(), mp)
	return reg
}

// waitFor polls a condition until it returns true or the timeout expires.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}
