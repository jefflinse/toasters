package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/llm"
)

// --- waitForChunk tests ---

func TestWaitForChunk_ContentResponse(t *testing.T) {
	t.Parallel()

	ch := make(chan llm.StreamResponse, 1)
	ch <- llm.StreamResponse{Content: "hello", Reasoning: "thinking"}

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

	ch := make(chan llm.StreamResponse, 1)
	testErr := errors.New("stream failed")
	ch <- llm.StreamResponse{Error: testErr}

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

	ch := make(chan llm.StreamResponse, 1)
	usage := &llm.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}
	ch <- llm.StreamResponse{Done: true, Model: "test-model", Usage: usage}

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
	if done.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", done.Usage.PromptTokens)
	}
	if done.Usage.CompletionTokens != 20 {
		t.Errorf("CompletionTokens = %d, want 20", done.Usage.CompletionTokens)
	}
}

func TestWaitForChunk_DoneWithNoModelOrUsage(t *testing.T) {
	t.Parallel()

	ch := make(chan llm.StreamResponse, 1)
	ch <- llm.StreamResponse{Done: true}

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

	ch := make(chan llm.StreamResponse, 1)
	calls := []llm.ToolCall{
		{Index: 0, ID: "call-1", Type: "function", Function: llm.ToolCallFunction{Name: "read_file", Arguments: `{"path":"foo.go"}`}},
		{Index: 1, ID: "call-2", Type: "function", Function: llm.ToolCallFunction{Name: "write_file", Arguments: `{"path":"bar.go"}`}},
	}
	ch <- llm.StreamResponse{ToolCalls: calls}

	cmd := waitForChunk(ch)
	msg := cmd()

	toolMsg, ok := msg.(ToolCallMsg)
	if !ok {
		t.Fatalf("expected ToolCallMsg, got %T", msg)
	}
	if len(toolMsg.Calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(toolMsg.Calls))
	}
	if toolMsg.Calls[0].Function.Name != "read_file" {
		t.Errorf("first call name = %q, want %q", toolMsg.Calls[0].Function.Name, "read_file")
	}
	if toolMsg.Calls[1].Function.Name != "write_file" {
		t.Errorf("second call name = %q, want %q", toolMsg.Calls[1].Function.Name, "write_file")
	}
}

func TestWaitForChunk_SingleToolCall(t *testing.T) {
	t.Parallel()

	ch := make(chan llm.StreamResponse, 1)
	ch <- llm.StreamResponse{
		ToolCalls: []llm.ToolCall{
			{Index: 0, ID: "call-abc", Type: "function", Function: llm.ToolCallFunction{Name: "list_files", Arguments: `{}`}},
		},
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

func TestWaitForChunk_MetaResponse(t *testing.T) {
	t.Parallel()

	ch := make(chan llm.StreamResponse, 1)
	ch <- llm.StreamResponse{
		Meta: &llm.ClaudeMeta{
			Model:          "claude-sonnet-4-20250514",
			PermissionMode: "plan",
			Version:        "1.0.42",
			SessionID:      "sess-abcdef1234567890",
		},
	}

	cmd := waitForChunk(ch)
	msg := cmd()

	meta, ok := msg.(claudeMetaMsg)
	if !ok {
		t.Fatalf("expected claudeMetaMsg, got %T", msg)
	}
	if meta.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", meta.Model, "claude-sonnet-4-20250514")
	}
	if meta.PermissionMode != "plan" {
		t.Errorf("PermissionMode = %q, want %q", meta.PermissionMode, "plan")
	}
	if meta.Version != "1.0.42" {
		t.Errorf("Version = %q, want %q", meta.Version, "1.0.42")
	}
	if meta.SessionID != "sess-abcdef1234567890" {
		t.Errorf("SessionID = %q, want %q", meta.SessionID, "sess-abcdef1234567890")
	}
}

func TestWaitForChunk_ClosedChannel(t *testing.T) {
	t.Parallel()

	ch := make(chan llm.StreamResponse)
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

	ch := make(chan llm.StreamResponse)
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

	ch := make(chan llm.StreamResponse, 1)
	ch <- llm.StreamResponse{Content: "", Reasoning: ""}

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
	ch := make(chan llm.StreamResponse, 1)
	testErr := errors.New("error with content")
	ch <- llm.StreamResponse{Error: testErr, Content: "some content"}

	cmd := waitForChunk(ch)
	msg := cmd()

	_, ok := msg.(StreamErrMsg)
	if !ok {
		t.Fatalf("expected StreamErrMsg (error takes precedence), got %T", msg)
	}
}

func TestWaitForChunk_MetaTakesPrecedenceOverToolCalls(t *testing.T) {
	t.Parallel()

	// When both Meta and ToolCalls are set, Meta should take precedence
	// because the code checks resp.Meta before resp.ToolCalls.
	ch := make(chan llm.StreamResponse, 1)
	ch <- llm.StreamResponse{
		Meta: &llm.ClaudeMeta{Model: "test-model"},
		ToolCalls: []llm.ToolCall{
			{ID: "call-1"},
		},
	}

	cmd := waitForChunk(ch)
	msg := cmd()

	_, ok := msg.(claudeMetaMsg)
	if !ok {
		t.Fatalf("expected claudeMetaMsg (meta takes precedence over tool calls), got %T", msg)
	}
}

func TestWaitForChunk_ToolCallsTakePrecedenceOverDone(t *testing.T) {
	t.Parallel()

	// When both ToolCalls and Done are set, ToolCalls should take precedence.
	ch := make(chan llm.StreamResponse, 1)
	ch <- llm.StreamResponse{
		ToolCalls: []llm.ToolCall{{ID: "call-1"}},
		Done:      true,
	}

	cmd := waitForChunk(ch)
	msg := cmd()

	_, ok := msg.(ToolCallMsg)
	if !ok {
		t.Fatalf("expected ToolCallMsg (tool calls take precedence over done), got %T", msg)
	}
}

func TestWaitForChunk_MultipleChunksInSequence(t *testing.T) {
	t.Parallel()

	ch := make(chan llm.StreamResponse, 3)
	ch <- llm.StreamResponse{Content: "chunk1"}
	ch <- llm.StreamResponse{Content: "chunk2"}
	ch <- llm.StreamResponse{Done: true, Model: "final-model"}

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

// --- formatClaudeMeta tests ---

func TestFormatClaudeMeta(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		msg   claudeMetaMsg
		check func(t *testing.T, result string)
	}{
		{
			name: "all fields populated",
			msg: claudeMetaMsg{
				Model:          "claude-sonnet-4-20250514",
				PermissionMode: "plan",
				Version:        "1.0.42",
				SessionID:      "sess-abcdef1234567890",
			},
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "claude-sonnet-4-20250514") {
					t.Errorf("result should contain model name, got %q", result)
				}
				if !strings.Contains(result, "plan mode") {
					t.Errorf("result should contain 'plan mode', got %q", result)
				}
				if !strings.Contains(result, "claude v1.0.42") {
					t.Errorf("result should contain 'claude v1.0.42', got %q", result)
				}
				// SessionID is truncated to 8 chars.
				if !strings.Contains(result, "session: sess-abc") {
					t.Errorf("result should contain truncated session ID 'session: sess-abc', got %q", result)
				}
			},
		},
		{
			name: "no version or session",
			msg: claudeMetaMsg{
				Model:          "claude-opus-4-20250514",
				PermissionMode: "auto",
			},
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "claude-opus-4-20250514") {
					t.Errorf("result should contain model name, got %q", result)
				}
				if !strings.Contains(result, "auto mode") {
					t.Errorf("result should contain 'auto mode', got %q", result)
				}
				if strings.Contains(result, "claude v") {
					t.Errorf("result should not contain version when empty, got %q", result)
				}
				if strings.Contains(result, "session:") {
					t.Errorf("result should not contain session when empty, got %q", result)
				}
			},
		},
		{
			name: "all fields empty",
			msg:  claudeMetaMsg{},
			check: func(t *testing.T, result string) {
				// Should still produce " · mode" (model and permission mode are empty strings).
				if !strings.Contains(result, " · ") {
					t.Errorf("result should contain separator, got %q", result)
				}
				if !strings.Contains(result, "mode") {
					t.Errorf("result should contain 'mode', got %q", result)
				}
				if strings.Contains(result, "claude v") {
					t.Errorf("result should not contain version when empty, got %q", result)
				}
				if strings.Contains(result, "session:") {
					t.Errorf("result should not contain session when empty, got %q", result)
				}
			},
		},
		{
			name: "version only",
			msg: claudeMetaMsg{
				Model:          "test-model",
				PermissionMode: "plan",
				Version:        "2.0.0",
			},
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "claude v2.0.0") {
					t.Errorf("result should contain 'claude v2.0.0', got %q", result)
				}
				if strings.Contains(result, "session:") {
					t.Errorf("result should not contain session when empty, got %q", result)
				}
			},
		},
		{
			name: "session only",
			msg: claudeMetaMsg{
				Model:          "test-model",
				PermissionMode: "plan",
				SessionID:      "short",
			},
			check: func(t *testing.T, result string) {
				if strings.Contains(result, "claude v") {
					t.Errorf("result should not contain version when empty, got %q", result)
				}
				// SessionID "short" is <= 8 chars, so it should appear in full.
				if !strings.Contains(result, "session: short") {
					t.Errorf("result should contain 'session: short', got %q", result)
				}
			},
		},
		{
			name: "session ID exactly 8 chars",
			msg: claudeMetaMsg{
				Model:          "m",
				PermissionMode: "p",
				SessionID:      "12345678",
			},
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "session: 12345678") {
					t.Errorf("result should contain full 8-char session ID, got %q", result)
				}
			},
		},
		{
			name: "session ID longer than 8 chars is truncated",
			msg: claudeMetaMsg{
				Model:          "m",
				PermissionMode: "p",
				SessionID:      "123456789abcdef",
			},
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "session: 12345678") {
					t.Errorf("result should contain truncated session ID, got %q", result)
				}
				if strings.Contains(result, "9abcdef") {
					t.Errorf("result should not contain chars beyond 8, got %q", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := formatClaudeMeta(tt.msg)
			tt.check(t, result)
		})
	}
}

func TestFormatClaudeMeta_ExactOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  claudeMetaMsg
		want string
	}{
		{
			name: "all fields",
			msg: claudeMetaMsg{
				Model:          "claude-sonnet-4-20250514",
				PermissionMode: "plan",
				Version:        "1.0.42",
				SessionID:      "sess-abcdef1234567890",
			},
			want: "claude-sonnet-4-20250514 · plan mode · claude v1.0.42 · session: sess-abc",
		},
		{
			name: "model and mode only",
			msg: claudeMetaMsg{
				Model:          "claude-opus-4-20250514",
				PermissionMode: "auto",
			},
			want: "claude-opus-4-20250514 · auto mode",
		},
		{
			name: "empty fields",
			msg:  claudeMetaMsg{},
			want: " ·  mode",
		},
		{
			name: "short session ID not truncated",
			msg: claudeMetaMsg{
				Model:          "m",
				PermissionMode: "p",
				SessionID:      "abc",
			},
			want: "m · p mode · session: abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatClaudeMeta(tt.msg)
			if got != tt.want {
				t.Errorf("formatClaudeMeta() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- drainPendingCompletions tests ---

func TestDrainPendingCompletions_NoPending(t *testing.T) {
	t.Parallel()

	m := &Model{
		chat: chatState{
			entries: []ChatEntry{
				{Message: llm.Message{Role: "system", Content: "system prompt"}},
				{Message: llm.Message{Role: "user", Content: "hello"}},
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
				{Message: llm.Message{Role: "system", Content: "system prompt"}},
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
				{Message: llm.Message{Role: "system", Content: "system prompt"}},
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
				{Message: llm.Message{Role: "system", Content: "sys"}},
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

// mockProvider implements llm.Provider for testing fetchModels.
type mockProvider struct {
	models []llm.ModelInfo
	err    error
}

func (m *mockProvider) ChatCompletionStream(_ context.Context, _ []llm.Message, _ float64) <-chan llm.StreamResponse {
	ch := make(chan llm.StreamResponse)
	close(ch)
	return ch
}

func (m *mockProvider) ChatCompletionStreamWithTools(_ context.Context, _ []llm.Message, _ []llm.Tool, _ float64) <-chan llm.StreamResponse {
	ch := make(chan llm.StreamResponse)
	close(ch)
	return ch
}

func (m *mockProvider) ChatCompletion(_ context.Context, _ []llm.Message) (string, error) {
	return "", nil
}

func (m *mockProvider) FetchModels(_ context.Context) ([]llm.ModelInfo, error) {
	return m.models, m.err
}

func (m *mockProvider) BaseURL() string {
	return "http://test:1234"
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

	models := []llm.ModelInfo{
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
		llmClient: &mockProvider{models: []llm.ModelInfo{}},
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
