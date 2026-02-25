package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/gateway"
	"github.com/jefflinse/toasters/internal/job"
	"github.com/jefflinse/toasters/internal/llm"
	llmtools "github.com/jefflinse/toasters/internal/llm/tools"
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
	agentVP := viewport.New()

	return Model{
		llmClient:     nil,
		chatViewport:  vp,
		agentViewport: agentVP,
		input:         ta,
		toolExec:      llmtools.NewToolExecutor(nil, nil, "", nil, nil),
		attachedSlot:  -1,
		chat: chatState{
			selectedMsgIdx:    -1,
			completionMsgIdx:  make(map[int]bool),
			expandedMsgs:      make(map[int]bool),
			expandedReasoning: make(map[int]bool),
			collapsedTools:    make(map[int]bool),
		},
		blockers:        make(map[string]*job.Blocker),
		runtimeSessions: make(map[string]*runtimeSlot),
	}
}

// newGatewayWithDoneSlot creates a real gateway, spawns a team using
// /usr/bin/true as the claude binary (exits immediately with no output), and
// waits until slot 0 transitions to SlotDone. It returns the gateway and the
// slot index used.
//
// This is the only reliable way to get a gateway with a Done slot without
// refactoring Gateway to accept an interface, since Gateway's slots field is
// unexported.
func newGatewayWithDoneSlot(t *testing.T) (*gateway.Gateway, int) {
	t.Helper()

	claudeCfg := config.ClaudeConfig{
		Path: "/usr/bin/true", // exits immediately with no output
	}
	gw := gateway.New(claudeCfg, t.TempDir(), func() {})

	slotID, _, err := gw.SpawnTeam("test-team", "job-001", "do something", agents.Team{})
	if err != nil {
		t.Fatalf("SpawnTeam: %v", err)
	}

	// Poll until the slot is done (/usr/bin/true exits immediately).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		slots := gw.Slots()
		if slots[slotID].Status == gateway.SlotDone {
			return gw, slotID
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for slot to reach SlotDone")
	return nil, -1
}

// TestPendingCompletion_BufferedWhenStreaming verifies that when a Running→Done
// slot transition is detected while m.stream.streaming == true, the completion
// notification is buffered in m.chat.pendingCompletions rather than injected
// immediately into m.chat.entries.
func TestPendingCompletion_BufferedWhenStreaming(t *testing.T) {
	gw, slotID := newGatewayWithDoneSlot(t)

	m := newMinimalModel(t)
	m.gateway = gw
	m.stream.streaming = true

	// Simulate that slotID was previously Running so the handler detects the
	// Running→Done transition.
	m.prevSlotActive[slotID] = true
	m.prevSlotStatus[slotID] = gateway.SlotRunning

	// Provide a notify channel so the handler can re-arm the poller.
	m.agentNotifyCh = make(chan struct{}, 8)

	initialMsgCount := len(m.chat.entries)

	updatedModel, _ := m.Update(AgentOutputMsg{})
	got := updatedModel.(*Model)

	// The notification must be buffered, not injected immediately.
	if len(got.chat.pendingCompletions) != 1 {
		t.Errorf("pendingCompletions: got %d entries, want 1", len(got.chat.pendingCompletions))
	}

	// No new entries should have been appended while streaming.
	if len(got.chat.entries) != initialMsgCount {
		t.Errorf("entries: got %d, want %d (no entries should be injected while streaming)",
			len(got.chat.entries), initialMsgCount)
	}

	// The buffered notification should reference the team and job.
	if len(got.chat.pendingCompletions) > 0 {
		notif := got.chat.pendingCompletions[0].notification
		if !strings.Contains(notif, "test-team") {
			t.Errorf("notification %q does not contain team name %q", notif, "test-team")
		}
		if !strings.Contains(notif, "job-001") {
			t.Errorf("notification %q does not contain job ID %q", notif, "job-001")
		}
	}

	// streaming flag must still be true — no new stream was started.
	if !got.stream.streaming {
		t.Error("streaming should still be true after buffering a completion")
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

// TestPendingCompletion_NotBufferedWhenNotStreaming verifies that when a
// Running→Done slot transition is detected while m.stream.streaming == false, the
// completion notification is injected immediately into m.chat.entries (not buffered).
func TestPendingCompletion_NotBufferedWhenNotStreaming(t *testing.T) {
	gw, slotID := newGatewayWithDoneSlot(t)

	m := newMinimalModel(t)
	m.gateway = gw
	m.stream.streaming = false

	// Simulate that slotID was previously Running.
	m.prevSlotActive[slotID] = true
	m.prevSlotStatus[slotID] = gateway.SlotRunning

	m.agentNotifyCh = make(chan struct{}, 8)

	updatedModel, _ := m.Update(AgentOutputMsg{})
	got := updatedModel.(*Model)

	// Nothing should be buffered — notification is injected immediately.
	if len(got.chat.pendingCompletions) != 0 {
		t.Errorf("pendingCompletions: got %d entries, want 0 (should inject immediately when not streaming)",
			len(got.chat.pendingCompletions))
	}

	// The notification must have been injected as a user entry.
	found := false
	for _, entry := range got.chat.entries {
		if entry.Message.Role == "user" &&
			strings.Contains(entry.Message.Content, "test-team") &&
			strings.Contains(entry.Message.Content, "job-001") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a user entry containing team/job notification in m.chat.entries, got: %v",
			got.chat.entries)
	}
}

// TestDrainPendingCompletions_Empty verifies that drainPendingCompletions is a
// no-op when the buffer is empty, returning the unchanged message slice and false.
func TestDrainPendingCompletions_Empty(t *testing.T) {
	m := newMinimalModel(t)
	m.appendEntry(ChatEntry{
		Message: llm.Message{Role: "system", Content: "sys"},
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
		Message: llm.Message{Role: "system", Content: "sys"},
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
