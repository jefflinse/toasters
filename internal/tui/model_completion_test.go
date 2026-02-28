package tui

import (
	"testing"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
)

// newMinimalModel returns a Model with only the fields needed to exercise
// the TUI logic without panicking. It deliberately avoids calling NewModel
// so we don't need a real LLM client or config directory.
func newMinimalModel(t *testing.T) Model {
	t.Helper()

	ta := textarea.New()
	ta.Focus()

	vp := viewport.New()

	return Model{
		llmClient:    nil,
		chatViewport: vp,
		input:        ta,
		chat: chatState{
			selectedMsgIdx:    -1,
			completionMsgIdx:  make(map[int]bool),
			expandedMsgs:      make(map[int]bool),
			expandedReasoning: make(map[int]bool),
			collapsedTools:    make(map[int]bool),
		},
		blockers:        make(map[string]*Blocker),
		runtimeSessions: make(map[string]*runtimeSlot),
	}
}

// TestOperatorDoneMsg_CommitsCurrentResponse verifies that when OperatorDoneMsg
// arrives with a non-empty currentResponse, it is committed as a chat entry.
func TestOperatorDoneMsg_CommitsCurrentResponse(t *testing.T) {
	m := newMinimalModel(t)
	m.stream.streaming = true
	m.stream.currentResponse = "Hello from the operator"
	m.stats.ModelName = "test-model"

	initialCount := len(m.chat.entries)

	result, _ := m.Update(OperatorDoneMsg{})
	got := result.(*Model)

	// streaming should be false.
	if got.stream.streaming {
		t.Error("streaming should be false after OperatorDoneMsg")
	}

	// The response should have been committed as an entry.
	if len(got.chat.entries) != initialCount+1 {
		t.Errorf("entries: got %d, want %d", len(got.chat.entries), initialCount+1)
	}
	if len(got.chat.entries) > initialCount {
		entry := got.chat.entries[len(got.chat.entries)-1]
		if entry.Message.Role != "assistant" {
			t.Errorf("committed entry role = %q, want %q", entry.Message.Role, "assistant")
		}
		if entry.Message.Content != "Hello from the operator" {
			t.Errorf("committed entry content = %q, want %q", entry.Message.Content, "Hello from the operator")
		}
	}

	// currentResponse should be cleared.
	if got.stream.currentResponse != "" {
		t.Errorf("currentResponse should be empty after OperatorDoneMsg, got %q", got.stream.currentResponse)
	}
}

// TestOperatorDoneMsg_EmptyResponse verifies that when OperatorDoneMsg arrives
// with an empty currentResponse, no entry is committed.
func TestOperatorDoneMsg_EmptyResponse(t *testing.T) {
	m := newMinimalModel(t)
	m.stream.streaming = true
	m.stream.currentResponse = ""

	initialCount := len(m.chat.entries)

	result, _ := m.Update(OperatorDoneMsg{})
	got := result.(*Model)

	if got.stream.streaming {
		t.Error("streaming should be false after OperatorDoneMsg")
	}

	// No entry should have been added.
	if len(got.chat.entries) != initialCount {
		t.Errorf("entries: got %d, want %d (no entry for empty response)", len(got.chat.entries), initialCount)
	}
}

// TestOperatorTextMsg_AccumulatesResponse verifies that OperatorTextMsg
// accumulates text into currentResponse.
func TestOperatorTextMsg_AccumulatesResponse(t *testing.T) {
	m := newMinimalModel(t)
	m.stream.streaming = true

	m.Update(OperatorTextMsg{Text: "Hello "})
	if m.stream.currentResponse != "Hello " {
		t.Errorf("after first text: currentResponse = %q, want %q", m.stream.currentResponse, "Hello ")
	}

	m.Update(OperatorTextMsg{Text: "world"})
	if m.stream.currentResponse != "Hello world" {
		t.Errorf("after second text: currentResponse = %q, want %q", m.stream.currentResponse, "Hello world")
	}
}

// TestSendMessage_NilOperator verifies that sendMessage returns nil when
// the operator is not set.
func TestSendMessage_NilOperator(t *testing.T) {
	m := newMinimalModel(t)
	m.operator = nil
	m.input.SetValue("hello")

	cmd := m.sendMessage()
	if cmd != nil {
		t.Error("sendMessage should return nil when operator is nil")
	}

	// The message should still be appended to entries.
	found := false
	for _, e := range m.chat.entries {
		if e.Message.Role == "user" && e.Message.Content == "hello" {
			found = true
			break
		}
	}
	if !found {
		t.Error("user message should be appended to entries even without operator")
	}
}

// TestSendMessage_EmptyInput verifies that sendMessage returns nil for empty input.
func TestSendMessage_EmptyInput(t *testing.T) {
	m := newMinimalModel(t)
	m.input.SetValue("   ")

	cmd := m.sendMessage()
	if cmd != nil {
		t.Error("sendMessage should return nil for whitespace-only input")
	}
}
