package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/provider"
)

// --- waitForChunk tests ---

func TestWaitForChunk_ContentResponse(t *testing.T) {
	t.Parallel()

	ch := make(chan provider.StreamEvent, 1)
	ch <- provider.StreamEvent{Type: provider.EventText, Text: "hello", Reasoning: "thinking"}

	cmd := waitForChunk(ch)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}

	msg := cmd()
	chunk, ok := msg.(StreamChunkMsg)
	if !ok {
		t.Fatalf("expected StreamChunkMsg, got %T", msg)
	}
	if chunk.Content != "hello" {
		t.Errorf("Content = %q, want %q", chunk.Content, "hello")
	}
	if chunk.Reasoning != "thinking" {
		t.Errorf("Reasoning = %q, want %q", chunk.Reasoning, "thinking")
	}
}

func TestWaitForChunk_ErrorResponse(t *testing.T) {
	t.Parallel()

	ch := make(chan provider.StreamEvent, 1)
	testErr := errors.New("stream failed")
	ch <- provider.StreamEvent{Type: provider.EventError, Error: testErr}

	cmd := waitForChunk(ch)
	msg := cmd()

	errMsg, ok := msg.(StreamErrMsg)
	if !ok {
		t.Fatalf("expected StreamErrMsg, got %T", msg)
	}
	if !errors.Is(errMsg.Err, testErr) {
		t.Errorf("Err = %v, want %v", errMsg.Err, testErr)
	}
}

func TestWaitForChunk_DoneResponse(t *testing.T) {
	t.Parallel()

	ch := make(chan provider.StreamEvent, 1)
	usage := &provider.Usage{InputTokens: 10, OutputTokens: 20}
	ch <- provider.StreamEvent{Type: provider.EventDone, Model: "test-model", Usage: usage}

	cmd := waitForChunk(ch)
	msg := cmd()

	done, ok := msg.(StreamDoneMsg)
	if !ok {
		t.Fatalf("expected StreamDoneMsg, got %T", msg)
	}
	if done.Model != "test-model" {
		t.Errorf("Model = %q, want %q", done.Model, "test-model")
	}
	if done.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	if done.Usage.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", done.Usage.InputTokens)
	}
	if done.Usage.OutputTokens != 20 {
		t.Errorf("OutputTokens = %d, want 20", done.Usage.OutputTokens)
	}
}

func TestWaitForChunk_DoneWithNoModelOrUsage(t *testing.T) {
	t.Parallel()

	ch := make(chan provider.StreamEvent, 1)
	ch <- provider.StreamEvent{Type: provider.EventDone}

	cmd := waitForChunk(ch)
	msg := cmd()

	done, ok := msg.(StreamDoneMsg)
	if !ok {
		t.Fatalf("expected StreamDoneMsg, got %T", msg)
	}
	if done.Model != "" {
		t.Errorf("Model = %q, want empty", done.Model)
	}
	if done.Usage != nil {
		t.Errorf("Usage = %v, want nil", done.Usage)
	}
}

func TestWaitForChunk_ToolCallResponse(t *testing.T) {
	t.Parallel()

	// With provider.StreamEvent, each tool call is a separate event.
	// Test that a single EventToolCall produces a ToolCallMsg.
	ch := make(chan provider.StreamEvent, 1)
	ch <- provider.StreamEvent{
		Type:     provider.EventToolCall,
		ToolCall: &provider.ToolCall{ID: "call-1", Name: "read_file", Arguments: []byte(`{"path":"foo.go"}`)},
	}

	cmd := waitForChunk(ch)
	msg := cmd()

	toolMsg, ok := msg.(ToolCallMsg)
	if !ok {
		t.Fatalf("expected ToolCallMsg, got %T", msg)
	}
	if len(toolMsg.Calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolMsg.Calls))
	}
	if toolMsg.Calls[0].Name != "read_file" {
		t.Errorf("first call name = %q, want %q", toolMsg.Calls[0].Name, "read_file")
	}
}

func TestWaitForChunk_SingleToolCall(t *testing.T) {
	t.Parallel()

	ch := make(chan provider.StreamEvent, 1)
	ch <- provider.StreamEvent{
		Type:     provider.EventToolCall,
		ToolCall: &provider.ToolCall{ID: "call-abc", Name: "list_files", Arguments: []byte(`{}`)},
	}

	cmd := waitForChunk(ch)
	msg := cmd()

	toolMsg, ok := msg.(ToolCallMsg)
	if !ok {
		t.Fatalf("expected ToolCallMsg, got %T", msg)
	}
	if len(toolMsg.Calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolMsg.Calls))
	}
	if toolMsg.Calls[0].ID != "call-abc" {
		t.Errorf("call ID = %q, want %q", toolMsg.Calls[0].ID, "call-abc")
	}
}

func TestWaitForChunk_ClosedChannel(t *testing.T) {
	t.Parallel()

	ch := make(chan provider.StreamEvent)
	close(ch)

	cmd := waitForChunk(ch)
	msg := cmd()

	_, ok := msg.(StreamDoneMsg)
	if !ok {
		t.Fatalf("expected StreamDoneMsg on closed channel, got %T", msg)
	}
}

func TestWaitForChunk_ClosedChannelReturnsDoneWithEmptyFields(t *testing.T) {
	t.Parallel()

	ch := make(chan provider.StreamEvent)
	close(ch)

	cmd := waitForChunk(ch)
	msg := cmd()

	done, ok := msg.(StreamDoneMsg)
	if !ok {
		t.Fatalf("expected StreamDoneMsg, got %T", msg)
	}
	// Closed channel returns the zero-value StreamDoneMsg (no model, no usage).
	if done.Model != "" {
		t.Errorf("Model = %q, want empty", done.Model)
	}
	if done.Usage != nil {
		t.Errorf("Usage = %v, want nil", done.Usage)
	}
}

func TestWaitForChunk_EmptyContentChunk(t *testing.T) {
	t.Parallel()

	ch := make(chan provider.StreamEvent, 1)
	ch <- provider.StreamEvent{Type: provider.EventText, Text: "", Reasoning: ""}

	cmd := waitForChunk(ch)
	msg := cmd()

	chunk, ok := msg.(StreamChunkMsg)
	if !ok {
		t.Fatalf("expected StreamChunkMsg, got %T", msg)
	}
	if chunk.Content != "" {
		t.Errorf("Content = %q, want empty", chunk.Content)
	}
	if chunk.Reasoning != "" {
		t.Errorf("Reasoning = %q, want empty", chunk.Reasoning)
	}
}

func TestWaitForChunk_ErrorTakesPrecedenceOverContent(t *testing.T) {
	t.Parallel()

	// When both Error and Content are set, Error should take precedence
	// because the code checks resp.Error first.
	ch := make(chan provider.StreamEvent, 1)
	testErr := errors.New("error with content")
	ch <- provider.StreamEvent{Type: provider.EventError, Error: testErr}

	cmd := waitForChunk(ch)
	msg := cmd()

	_, ok := msg.(StreamErrMsg)
	if !ok {
		t.Fatalf("expected StreamErrMsg (error takes precedence), got %T", msg)
	}
}

func TestWaitForChunk_MultipleChunksInSequence(t *testing.T) {
	t.Parallel()

	ch := make(chan provider.StreamEvent, 3)
	ch <- provider.StreamEvent{Type: provider.EventText, Text: "chunk1"}
	ch <- provider.StreamEvent{Type: provider.EventText, Text: "chunk2"}
	ch <- provider.StreamEvent{Type: provider.EventDone, Model: "final-model"}

	// First chunk.
	msg1 := waitForChunk(ch)()
	chunk1, ok := msg1.(StreamChunkMsg)
	if !ok {
		t.Fatalf("msg1: expected StreamChunkMsg, got %T", msg1)
	}
	if chunk1.Content != "chunk1" {
		t.Errorf("msg1 Content = %q, want %q", chunk1.Content, "chunk1")
	}

	// Second chunk.
	msg2 := waitForChunk(ch)()
	chunk2, ok := msg2.(StreamChunkMsg)
	if !ok {
		t.Fatalf("msg2: expected StreamChunkMsg, got %T", msg2)
	}
	if chunk2.Content != "chunk2" {
		t.Errorf("msg2 Content = %q, want %q", chunk2.Content, "chunk2")
	}

	// Done.
	msg3 := waitForChunk(ch)()
	done, ok := msg3.(StreamDoneMsg)
	if !ok {
		t.Fatalf("msg3: expected StreamDoneMsg, got %T", msg3)
	}
	if done.Model != "final-model" {
		t.Errorf("msg3 Model = %q, want %q", done.Model, "final-model")
	}
}

// --- drainPendingCompletions tests ---

func TestDrainPendingCompletions_NoPending(t *testing.T) {
	t.Parallel()

	m := &Model{
		chat: chatState{
			entries: []ChatEntry{
				{Message: provider.Message{Role: "system", Content: "system prompt"}},
				{Message: provider.Message{Role: "user", Content: "hello"}},
			},
			pendingCompletions: nil,
		},
	}

	msgs, drained := m.drainPendingCompletions()
	if drained {
		t.Error("expected drained=false when no pending completions")
	}
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("first message role = %q, want %q", msgs[0].Role, "system")
	}
	if msgs[1].Role != "user" {
		t.Errorf("second message role = %q, want %q", msgs[1].Role, "user")
	}
}

func TestDrainPendingCompletions_OnePending(t *testing.T) {
	t.Parallel()

	m := &Model{
		chat: chatState{
			entries: []ChatEntry{
				{Message: provider.Message{Role: "system", Content: "system prompt"}},
			},
			pendingCompletions: []pendingCompletion{
				{notification: "Agent completed task X"},
			},
		},
	}

	msgs, drained := m.drainPendingCompletions()
	if !drained {
		t.Error("expected drained=true when pending completions exist")
	}
	// Should have original entry + the drained notification.
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[1].Role != "user" {
		t.Errorf("drained message role = %q, want %q", msgs[1].Role, "user")
	}
	if msgs[1].Content != "Agent completed task X" {
		t.Errorf("drained message content = %q, want %q", msgs[1].Content, "Agent completed task X")
	}
	// pendingCompletions should be cleared.
	if len(m.chat.pendingCompletions) != 0 {
		t.Errorf("pendingCompletions should be nil after drain, got %d", len(m.chat.pendingCompletions))
	}
}

func TestDrainPendingCompletions_MultiplePending(t *testing.T) {
	t.Parallel()

	m := &Model{
		chat: chatState{
			entries: []ChatEntry{
				{Message: provider.Message{Role: "system", Content: "system prompt"}},
			},
			pendingCompletions: []pendingCompletion{
				{notification: "Agent A done"},
				{notification: "Agent B done"},
				{notification: "Agent C done"},
			},
		},
	}

	msgs, drained := m.drainPendingCompletions()
	if !drained {
		t.Error("expected drained=true")
	}
	// 1 original + 3 drained.
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	// Verify all drained messages are user-role with correct content.
	for i, want := range []string{"Agent A done", "Agent B done", "Agent C done"} {
		msg := msgs[i+1]
		if msg.Role != "user" {
			t.Errorf("drained msg[%d] role = %q, want %q", i, msg.Role, "user")
		}
		if msg.Content != want {
			t.Errorf("drained msg[%d] content = %q, want %q", i, msg.Content, want)
		}
	}
	if len(m.chat.pendingCompletions) != 0 {
		t.Errorf("pendingCompletions should be nil after drain, got %d", len(m.chat.pendingCompletions))
	}
}

func TestDrainPendingCompletions_EmptySlice(t *testing.T) {
	t.Parallel()

	m := &Model{
		chat: chatState{
			entries:            []ChatEntry{},
			pendingCompletions: []pendingCompletion{},
		},
	}

	msgs, drained := m.drainPendingCompletions()
	if drained {
		t.Error("expected drained=false for empty (non-nil) slice")
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestDrainPendingCompletions_EntriesUpdated(t *testing.T) {
	t.Parallel()

	m := &Model{
		chat: chatState{
			entries: []ChatEntry{
				{Message: provider.Message{Role: "system", Content: "sys"}},
			},
			pendingCompletions: []pendingCompletion{
				{notification: "notification 1"},
			},
		},
	}

	before := len(m.chat.entries)
	_, _ = m.drainPendingCompletions()
	after := len(m.chat.entries)

	if after != before+1 {
		t.Errorf("expected entries to grow by 1 (from %d to %d), got %d", before, before+1, after)
	}
	// Verify the appended entry has a reasonable timestamp.
	lastEntry := m.chat.entries[len(m.chat.entries)-1]
	if lastEntry.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp on drained entry")
	}
	if time.Since(lastEntry.Timestamp) > 5*time.Second {
		t.Error("timestamp seems too old")
	}
}

// --- fetchModels tests ---

// mockProvider implements provider.Provider for testing fetchModels.
type mockProvider struct {
	models []provider.ModelInfo
	err    error
}

func (m *mockProvider) ChatStream(_ context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent)
	close(ch)
	return ch, nil
}

func (m *mockProvider) Models(_ context.Context) ([]provider.ModelInfo, error) {
	return m.models, m.err
}

func (m *mockProvider) Name() string {
	return "mock"
}

func TestFetchModels_ReturnsNonNilCmd(t *testing.T) {
	t.Parallel()

	m := Model{
		llmClient: &mockProvider{},
	}

	cmd := m.fetchModels()
	if cmd == nil {
		t.Fatal("expected non-nil cmd from fetchModels")
	}
}

func TestFetchModels_SuccessReturnsModelsMsg(t *testing.T) {
	t.Parallel()

	models := []provider.ModelInfo{
		{ID: "model-1", State: "loaded", MaxContextLength: 8192},
		{ID: "model-2", State: "not-loaded", MaxContextLength: 4096},
	}
	m := Model{
		llmClient: &mockProvider{models: models},
	}

	cmd := m.fetchModels()
	msg := cmd()

	modelsMsg, ok := msg.(ModelsMsg)
	if !ok {
		t.Fatalf("expected ModelsMsg, got %T", msg)
	}
	if modelsMsg.Err != nil {
		t.Fatalf("unexpected error: %v", modelsMsg.Err)
	}
	if len(modelsMsg.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(modelsMsg.Models))
	}
	if modelsMsg.Models[0].ID != "model-1" {
		t.Errorf("first model ID = %q, want %q", modelsMsg.Models[0].ID, "model-1")
	}
	if modelsMsg.Models[1].ID != "model-2" {
		t.Errorf("second model ID = %q, want %q", modelsMsg.Models[1].ID, "model-2")
	}
}

func TestFetchModels_ErrorReturnsModelsMsg(t *testing.T) {
	t.Parallel()

	testErr := errors.New("connection refused")
	m := Model{
		llmClient: &mockProvider{err: testErr},
	}

	cmd := m.fetchModels()
	msg := cmd()

	modelsMsg, ok := msg.(ModelsMsg)
	if !ok {
		t.Fatalf("expected ModelsMsg, got %T", msg)
	}
	if !errors.Is(modelsMsg.Err, testErr) {
		t.Errorf("Err = %v, want %v", modelsMsg.Err, testErr)
	}
	if modelsMsg.Models != nil {
		t.Errorf("Models = %v, want nil", modelsMsg.Models)
	}
}

func TestFetchModels_EmptyModelsReturnsEmptySlice(t *testing.T) {
	t.Parallel()

	m := Model{
		llmClient: &mockProvider{models: []provider.ModelInfo{}},
	}

	cmd := m.fetchModels()
	msg := cmd()

	modelsMsg, ok := msg.(ModelsMsg)
	if !ok {
		t.Fatalf("expected ModelsMsg, got %T", msg)
	}
	if modelsMsg.Err != nil {
		t.Fatalf("unexpected error: %v", modelsMsg.Err)
	}
	if len(modelsMsg.Models) != 0 {
		t.Errorf("expected 0 models, got %d", len(modelsMsg.Models))
	}
}
