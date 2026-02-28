package tui

import (
	"testing"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"

	llmtools "github.com/jefflinse/toasters/internal/llm/tools"
	"github.com/jefflinse/toasters/internal/provider"
)

// newMinimalModel returns a Model with only the fields needed to exercise the
// pending-completion buffer logic without panicking. It deliberately avoids
// calling NewModel so we don't need a real LLM client or config directory.
//
// Key design decision: llmClient is intentionally nil. startStream returns a
// tea.Cmd closure that only calls the client when the cmd is *executed*, not
// during Update. Our tests never execute the returned cmds, so this is safe.
func newMinimalModel(t *testing.T) Model {
	t.Helper()

	ta := textarea.New()
	ta.Focus()

	vp := viewport.New()

	return Model{
		llmClient:    nil,
		chatViewport: vp,
		input:        ta,
		toolExec:     llmtools.NewToolExecutor(nil, "", nil, nil),
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

// TestPendingCompletion_InjectedAfterStreamDone verifies that when StreamDoneMsg
// arrives and m.chat.pendingCompletions is non-empty, the buffered notifications are
// drained into m.chat.entries, the buffer is cleared, and a new stream is started.
func TestPendingCompletion_InjectedAfterStreamDone(t *testing.T) {
	m := newMinimalModel(t)
	m.stream.streaming = true

	// Pre-populate the pending buffer with one completion notification.
	const wantNotification = "Team 'alpha' in slot 0 has completed (job: job-42).\n\nExit Summary:\nAll done.\n\nOutput (last 2000 chars):\nsome output"
	m.chat.pendingCompletions = []pendingCompletion{
		{notification: wantNotification},
	}

	initialMsgCount := len(m.chat.entries)

	updatedModel, cmd := m.Update(StreamDoneMsg{})
	got := updatedModel.(*Model)

	// Buffer must be drained.
	if len(got.chat.pendingCompletions) != 0 {
		t.Errorf("pendingCompletions: got %d entries after StreamDoneMsg, want 0",
			len(got.chat.pendingCompletions))
	}

	// The notification must have been injected as a user entry.
	if len(got.chat.entries) != initialMsgCount+1 {
		t.Errorf("entries: got %d, want %d (one notification should be injected)",
			len(got.chat.entries), initialMsgCount+1)
	}
	if len(got.chat.entries) > initialMsgCount {
		injected := got.chat.entries[len(got.chat.entries)-1].Message
		if injected.Role != "user" {
			t.Errorf("injected message role: got %q, want %q", injected.Role, "user")
		}
		if injected.Content != wantNotification {
			t.Errorf("injected message content:\ngot:  %q\nwant: %q",
				injected.Content, wantNotification)
		}
	}

	// A new stream should have been started — Update must return a non-nil cmd.
	// (We don't execute the cmd because that would require a real LLM client.)
	if cmd == nil {
		t.Error("Update(StreamDoneMsg) should return a non-nil cmd to start a new stream after draining completions")
	}

	// startStream sets m.stream.streaming = true before returning the cmd.
	if !got.stream.streaming {
		t.Error("streaming should be true after draining pending completions (startStream was called)")
	}
}

// TestDrainPendingCompletions_Empty verifies that drainPendingCompletions is a
// no-op when the buffer is empty, returning the unchanged message slice and false.
func TestDrainPendingCompletions_Empty(t *testing.T) {
	m := newMinimalModel(t)
	m.appendEntry(ChatEntry{
		Message: provider.Message{Role: "system", Content: "sys"},
	})

	msgs, ok := m.drainPendingCompletions()

	if ok {
		t.Error("drainPendingCompletions: got ok=true for empty buffer, want false")
	}
	if len(msgs) != 1 {
		t.Errorf("drainPendingCompletions: messages length changed: got %d, want 1", len(msgs))
	}
	if len(m.chat.pendingCompletions) != 0 {
		t.Errorf("pendingCompletions should remain empty, got %d", len(m.chat.pendingCompletions))
	}
}

// TestDrainPendingCompletions_Multiple verifies that all buffered completions
// are injected in order and the buffer is cleared.
func TestDrainPendingCompletions_Multiple(t *testing.T) {
	m := newMinimalModel(t)
	m.appendEntry(ChatEntry{
		Message: provider.Message{Role: "system", Content: "sys"},
	})
	m.chat.pendingCompletions = []pendingCompletion{
		{notification: "first completion"},
		{notification: "second completion"},
		{notification: "third completion"},
	}

	msgs, ok := m.drainPendingCompletions()

	if !ok {
		t.Error("drainPendingCompletions: got ok=false for non-empty buffer, want true")
	}
	if len(m.chat.pendingCompletions) != 0 {
		t.Errorf("pendingCompletions should be nil after drain, got %d entries",
			len(m.chat.pendingCompletions))
	}

	// Original system message + 3 notifications = 4 total.
	if len(msgs) != 4 {
		t.Fatalf("messages: got %d, want 4", len(msgs))
	}
	wantContents := []string{"sys", "first completion", "second completion", "third completion"}
	for i, want := range wantContents {
		if msgs[i].Content != want {
			t.Errorf("messages[%d].Content = %q, want %q", i, msgs[i].Content, want)
		}
	}
	// All injected messages must be "user" role.
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role != "user" {
			t.Errorf("messages[%d].Role = %q, want %q", i, msgs[i].Role, "user")
		}
	}
}

// TestPendingCompletion_StreamDoneWithNoPending verifies that when StreamDoneMsg
// arrives with an empty pending buffer, no new stream is started and streaming
// is set to false.
func TestPendingCompletion_StreamDoneWithNoPending(t *testing.T) {
	m := newMinimalModel(t)
	m.stream.streaming = true
	// No pending completions.

	updatedModel, _ := m.Update(StreamDoneMsg{})
	got := updatedModel.(*Model)

	// streaming should be false — stream ended, no new one started.
	if got.stream.streaming {
		t.Error("streaming should be false after StreamDoneMsg with no pending completions")
	}

	// Buffer should remain empty.
	if len(got.chat.pendingCompletions) != 0 {
		t.Errorf("pendingCompletions: got %d, want 0", len(got.chat.pendingCompletions))
	}
}
