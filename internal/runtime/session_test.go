package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/provider"
)

// mockProvider implements provider.Provider for testing.
type mockProvider struct {
	name      string
	responses []mockResponse // one per ChatStream call, consumed in order
	mu        sync.Mutex
	callIdx   int
}

type mockResponse struct {
	events []provider.StreamEvent
	err    error // returned from ChatStream itself
	block  bool  // if true, channel stays open until context is cancelled
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) ChatStream(ctx context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

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
		// Keep channel open until context is cancelled.
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

// mockToolExecutor implements ToolExecutor for testing.
type mockToolExecutor struct {
	results map[string]string
	errors  map[string]error
	defs    []ToolDef
}

func (m *mockToolExecutor) Execute(_ context.Context, name string, _ json.RawMessage) (string, error) {
	if err, ok := m.errors[name]; ok {
		return "", err
	}
	if result, ok := m.results[name]; ok {
		return result, nil
	}
	return "ok", nil
}

func (m *mockToolExecutor) Definitions() []ToolDef {
	return m.defs
}

func TestSessionSimpleTextResponse(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Hello "},
				{Type: provider.EventText, Text: "world!"},
				{Type: provider.EventUsage, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 5}},
				{Type: provider.EventDone},
			}},
		},
	}

	opts := SpawnOpts{
		WorkerID:       "test-worker",
		Model:          "test-model",
		SystemPrompt:   "You are a test worker.",
		InitialMessage: "Say hello",
		MaxTurns:       10,
	}

	sess := newSession("sess-1", mp, opts, &mockToolExecutor{})

	err := sess.Run(context.Background())
	assertNoError(t, err)

	snap := sess.Snapshot()
	assertEqual(t, "completed", snap.Status)
	assertEqual(t, "sess-1", snap.ID)
	assertEqual(t, "test-worker", snap.WorkerID)
	assertEqual(t, "test-model", snap.Model)
	assertEqual(t, "test", snap.Provider)

	if snap.TokensIn != 10 {
		t.Errorf("want TokensIn=10, got %d", snap.TokensIn)
	}
	if snap.TokensOut != 5 {
		t.Errorf("want TokensOut=5, got %d", snap.TokensOut)
	}

	assertEqual(t, "Hello world!", sess.FinalText())
}

func TestSessionToolCallLoop(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			// Turn 1: LLM requests a tool call.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Let me check..."},
				{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:        "call-1",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"path": "test.txt"}`),
				}},
				{Type: provider.EventUsage, Usage: &provider.Usage{InputTokens: 20, OutputTokens: 15}},
				{Type: provider.EventDone},
			}},
			// Turn 2: LLM responds with final text after seeing tool result.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "The file contains: test content"},
				{Type: provider.EventUsage, Usage: &provider.Usage{InputTokens: 30, OutputTokens: 10}},
				{Type: provider.EventDone},
			}},
		},
	}

	toolExec := &mockToolExecutor{
		results: map[string]string{
			"read_file": "1: test content\n",
		},
		defs: []ToolDef{
			{Name: "read_file", Description: "Read a file"},
		},
	}

	opts := SpawnOpts{
		WorkerID:       "test-worker",
		Model:          "test-model",
		InitialMessage: "Read test.txt",
		MaxTurns:       10,
	}

	sess := newSession("sess-2", mp, opts, toolExec)

	err := sess.Run(context.Background())
	assertNoError(t, err)

	snap := sess.Snapshot()
	assertEqual(t, "completed", snap.Status)

	// Should have accumulated tokens from both turns.
	if snap.TokensIn != 50 {
		t.Errorf("want TokensIn=50, got %d", snap.TokensIn)
	}
	if snap.TokensOut != 25 {
		t.Errorf("want TokensOut=25, got %d", snap.TokensOut)
	}

	assertEqual(t, "The file contains: test content", sess.FinalText())

	// Verify message history: user, assistant (with tool call), tool result, assistant (final).
	if len(sess.messages) != 4 {
		t.Fatalf("want 4 messages, got %d", len(sess.messages))
	}
	assertEqual(t, "user", sess.messages[0].Role)
	assertEqual(t, "assistant", sess.messages[1].Role)
	assertEqual(t, "tool", sess.messages[2].Role)
	assertEqual(t, "assistant", sess.messages[3].Role)
}

func TestSessionMultiToolCalls(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			// Turn 1: Two tool calls.
			{events: []provider.StreamEvent{
				{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:        "call-1",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"path": "a.txt"}`),
				}},
				{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:        "call-2",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"path": "b.txt"}`),
				}},
				{Type: provider.EventDone},
			}},
			// Turn 2: Final response.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Done"},
				{Type: provider.EventDone},
			}},
		},
	}

	toolExec := &mockToolExecutor{
		results: map[string]string{
			"read_file": "content",
		},
		defs: []ToolDef{
			{Name: "read_file", Description: "Read a file"},
		},
	}

	opts := SpawnOpts{
		InitialMessage: "Read both files",
		MaxTurns:       10,
	}

	sess := newSession("sess-3", mp, opts, toolExec)
	err := sess.Run(context.Background())
	assertNoError(t, err)

	// user, assistant (2 tool calls), tool result 1, tool result 2, assistant (final).
	if len(sess.messages) != 5 {
		t.Fatalf("want 5 messages, got %d", len(sess.messages))
	}
	assertEqual(t, "tool", sess.messages[2].Role)
	assertEqual(t, "call-1", sess.messages[2].ToolCallID)
	assertEqual(t, "tool", sess.messages[3].Role)
	assertEqual(t, "call-2", sess.messages[3].ToolCallID)
}

func TestSessionToolCallError(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:        "call-1",
					Name:      "bad_tool",
					Arguments: json.RawMessage(`{}`),
				}},
				{Type: provider.EventDone},
			}},
			// LLM sees the error and responds.
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Tool failed, sorry"},
				{Type: provider.EventDone},
			}},
		},
	}

	toolExec := &mockToolExecutor{
		errors: map[string]error{
			"bad_tool": errors.New("tool broke"),
		},
		defs: []ToolDef{
			{Name: "bad_tool", Description: "A broken tool"},
		},
	}

	opts := SpawnOpts{
		InitialMessage: "Use the bad tool",
		MaxTurns:       10,
	}

	sess := newSession("sess-4", mp, opts, toolExec)
	err := sess.Run(context.Background())
	assertNoError(t, err)

	// The tool result message should contain the error.
	toolMsg := sess.messages[2]
	assertEqual(t, "tool", toolMsg.Role)
	assertContains(t, toolMsg.Content, "error: tool broke")
}

func TestSessionContextCancellation(t *testing.T) {
	// Provider that blocks until context is cancelled.
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{
				events: []provider.StreamEvent{
					{Type: provider.EventText, Text: "Starting..."},
				},
				block: true,
			},
		},
	}

	opts := SpawnOpts{
		InitialMessage: "Do something slow",
		MaxTurns:       10,
	}

	sess := newSession("sess-5", mp, opts, &mockToolExecutor{})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- sess.Run(ctx)
	}()

	// Give the session time to start.
	time.Sleep(50 * time.Millisecond)
	cancel()

	err := <-done
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}

	snap := sess.Snapshot()
	if snap.Status != "cancelled" && snap.Status != "failed" {
		t.Errorf("want status cancelled or failed, got %q", snap.Status)
	}
}

func TestSessionMaxTurns(t *testing.T) {
	// Provider always returns a tool call, never a final response.
	responses := make([]mockResponse, 5)
	for i := range responses {
		responses[i] = mockResponse{
			events: []provider.StreamEvent{
				{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:        "call-" + string(rune('a'+i)),
					Name:      "read_file",
					Arguments: json.RawMessage(`{"path": "test.txt"}`),
				}},
				{Type: provider.EventDone},
			},
		}
	}

	mp := &mockProvider{name: "test", responses: responses}

	toolExec := &mockToolExecutor{
		results: map[string]string{"read_file": "content"},
		defs:    []ToolDef{{Name: "read_file", Description: "Read a file"}},
	}

	opts := SpawnOpts{
		InitialMessage: "Keep going",
		MaxTurns:       3,
	}

	sess := newSession("sess-6", mp, opts, toolExec)
	err := sess.Run(context.Background())
	assertError(t, err)
	assertContains(t, err.Error(), "max turns (3) exceeded")

	snap := sess.Snapshot()
	assertEqual(t, "failed", snap.Status)
}

func TestSessionSubscriber(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Hello"},
				{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:        "call-1",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"path": "test.txt"}`),
				}},
				{Type: provider.EventDone},
			}},
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Done"},
				{Type: provider.EventDone},
			}},
		},
	}

	toolExec := &mockToolExecutor{
		results: map[string]string{"read_file": "content"},
		defs:    []ToolDef{{Name: "read_file", Description: "Read a file"}},
	}

	opts := SpawnOpts{
		InitialMessage: "Test",
		MaxTurns:       10,
	}

	sess := newSession("sess-7", mp, opts, toolExec)
	sub := sess.Subscribe()

	go func() {
		_ = sess.Run(context.Background())
	}()

	var events []SessionEvent
	for ev := range sub {
		events = append(events, ev)
	}

	// Should have: text("Hello"), tool_call, tool_result, text("Done"), done.
	if len(events) < 4 {
		t.Fatalf("want at least 4 events, got %d", len(events))
	}

	// Verify we got the expected event types.
	types := make(map[SessionEventType]int)
	for _, ev := range events {
		types[ev.Type]++
	}

	if types[SessionEventText] < 2 {
		t.Errorf("want at least 2 text events, got %d", types[SessionEventText])
	}
	if types[SessionEventToolCall] != 1 {
		t.Errorf("want 1 tool_call event, got %d", types[SessionEventToolCall])
	}
	if types[SessionEventToolResult] != 1 {
		t.Errorf("want 1 tool_result event, got %d", types[SessionEventToolResult])
	}
	if types[SessionEventDone] != 1 {
		t.Errorf("want 1 done event, got %d", types[SessionEventDone])
	}
}

func TestSessionStreamError(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "partial"},
				{Type: provider.EventError, Error: errors.New("stream broke")},
			}},
		},
	}

	opts := SpawnOpts{
		InitialMessage: "Test",
		MaxTurns:       10,
	}

	sess := newSession("sess-8", mp, opts, &mockToolExecutor{})
	err := sess.Run(context.Background())
	assertError(t, err)
	assertContains(t, err.Error(), "stream broke")

	snap := sess.Snapshot()
	assertEqual(t, "failed", snap.Status)
}

func TestSessionChatStreamError(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{err: errors.New("connection refused")},
		},
	}

	opts := SpawnOpts{
		InitialMessage: "Test",
		MaxTurns:       10,
	}

	sess := newSession("sess-9", mp, opts, &mockToolExecutor{})
	err := sess.Run(context.Background())
	assertError(t, err)
	assertContains(t, err.Error(), "starting stream")

	snap := sess.Snapshot()
	assertEqual(t, "failed", snap.Status)
}

func TestSessionFinalTextEmpty(t *testing.T) {
	sess := &Session{messages: []provider.Message{
		{Role: "user", Content: "hello"},
	}}
	assertEqual(t, "", sess.FinalText())
}

func TestSessionFinalTextMultipleAssistant(t *testing.T) {
	sess := &Session{messages: []provider.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "first"},
		{Role: "user", Content: "again"},
		{Role: "assistant", Content: "second"},
	}}
	assertEqual(t, "second", sess.FinalText())
}

func TestSessionCancel(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{
				events: []provider.StreamEvent{
					{Type: provider.EventText, Text: "start"},
				},
				block: true,
			},
		},
	}

	opts := SpawnOpts{
		InitialMessage: "Test",
		MaxTurns:       10,
	}

	sess := newSession("sess-cancel", mp, opts, &mockToolExecutor{})

	go func() {
		_ = sess.Run(context.Background())
	}()

	// Give session time to start.
	time.Sleep(50 * time.Millisecond)
	sess.Cancel()

	select {
	case <-sess.Done():
		// Session exited.
	case <-time.After(2 * time.Second):
		t.Fatal("session did not exit after cancel")
	}
}

func TestSessionNoInitialMessage(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Hello"},
				{Type: provider.EventDone},
			}},
		},
	}

	opts := SpawnOpts{
		MaxTurns: 10,
		// No InitialMessage.
	}

	sess := newSession("sess-no-msg", mp, opts, &mockToolExecutor{})

	// Should have no initial messages.
	if len(sess.messages) != 0 {
		t.Fatalf("want 0 initial messages, got %d", len(sess.messages))
	}

	err := sess.Run(context.Background())
	assertNoError(t, err)
}

func TestSessionDoneChannel(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Done"},
				{Type: provider.EventDone},
			}},
		},
	}

	opts := SpawnOpts{
		InitialMessage: "Test",
		MaxTurns:       10,
	}

	sess := newSession("sess-done", mp, opts, &mockToolExecutor{})

	go func() {
		_ = sess.Run(context.Background())
	}()

	select {
	case <-sess.Done():
		// Good — session completed.
	case <-time.After(2 * time.Second):
		t.Fatal("Done channel not closed after session completed")
	}
}

func TestSessionID(t *testing.T) {
	sess := &Session{id: "test-id"}
	if sess.ID() != "test-id" {
		t.Errorf("want ID test-id, got %s", sess.ID())
	}
}

// Test that subscriber channel is closed when session ends.
func TestSessionSubscriberClosed(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Done"},
				{Type: provider.EventDone},
			}},
		},
	}

	opts := SpawnOpts{
		InitialMessage: "Test",
		MaxTurns:       10,
	}

	sess := newSession("sess-sub-close", mp, opts, &mockToolExecutor{})
	sub := sess.Subscribe()

	go func() {
		_ = sess.Run(context.Background())
	}()

	// Drain all events — channel should close.
	for range sub {
	}

	// If we get here, the channel was closed. Verify by checking it's closed.
	_, ok := <-sub
	if ok {
		t.Fatal("subscriber channel should be closed")
	}
}

// Test that slow subscribers don't block the session.
func TestSessionSlowSubscriber(t *testing.T) {
	// Create a provider with many text events to overflow the subscriber buffer.
	events := make([]provider.StreamEvent, 0, subscriberBufSize+20)
	for i := 0; i < subscriberBufSize+10; i++ {
		events = append(events, provider.StreamEvent{
			Type: provider.EventText,
			Text: strings.Repeat("x", 100),
		})
	}
	events = append(events, provider.StreamEvent{Type: provider.EventDone})

	mp := &mockProvider{
		name:      "test",
		responses: []mockResponse{{events: events}},
	}

	opts := SpawnOpts{
		InitialMessage: "Test",
		MaxTurns:       10,
	}

	sess := newSession("sess-slow", mp, opts, &mockToolExecutor{})
	_ = sess.Subscribe() // Subscribe but never read.

	done := make(chan error, 1)
	go func() {
		done <- sess.Run(context.Background())
	}()

	select {
	case err := <-done:
		assertNoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("session blocked on slow subscriber")
	}
}
