package gateway

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/config"
)

// newTestGateway returns a Gateway wired with a no-op notify callback and a
// zero ClaudeConfig. It is suitable for tests that manipulate slot state
// directly without launching real subprocesses.
func newTestGateway() *Gateway {
	return New(config.ClaudeConfig{}, "", func() {})
}

// injectRunningSlot places a slot in SlotRunning state at the given index and
// returns the cancel function so callers can clean up if needed.
func injectRunningSlot(g *Gateway, idx int) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	s := &slot{
		agentName:  "test-agent",
		jobID:      "job-001",
		status:     SlotRunning,
		startTime:  time.Now(),
		cancel:     cancel,
		resetTimer: make(chan time.Duration, 1),
	}
	// Suppress the unused ctx lint warning — ctx is owned by cancel.
	_ = ctx

	g.mu.Lock()
	g.slots[idx] = s
	g.mu.Unlock()

	return cancel
}

// injectDoneSlot places a slot in SlotDone state at the given index with
// killed set to false (simulating a natural exit).
func injectDoneSlot(g *Gateway, idx int) {
	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx // context held by slot; cancel is stored on the slot for cleanup
	s := &slot{
		agentName:  "test-agent",
		jobID:      "job-002",
		status:     SlotDone,
		killed:     false,
		startTime:  time.Now().Add(-5 * time.Second),
		endTime:    time.Now(),
		cancel:     cancel,
		resetTimer: make(chan time.Duration, 1),
	}

	g.mu.Lock()
	g.slots[idx] = s
	g.mu.Unlock()
}

// --- TestKill_SetsKilledFlag ---

// TestKill_SetsKilledFlag verifies that calling Kill on a running slot sets
// Killed=true and Status=SlotDone in the resulting SlotSnapshot.
func TestKill_SetsKilledFlag(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	injectRunningSlot(g, 0)

	if err := g.Kill(0); err != nil {
		t.Fatalf("Kill(0) returned unexpected error: %v", err)
	}

	snapshots := g.Slots()
	snap := snapshots[0]

	if !snap.Active {
		t.Error("expected slot to be Active after Kill, got Active=false")
	}
	if !snap.Killed {
		t.Error("expected Killed=true after Kill(), got Killed=false")
	}
	if snap.Status != SlotDone {
		t.Errorf("expected Status=SlotDone after Kill(), got Status=%v", snap.Status)
	}
	if snap.EndTime.IsZero() {
		t.Error("expected EndTime to be set after Kill(), got zero value")
	}
	if !strings.Contains(snap.Output, "[killed]") {
		t.Errorf("expected Output to contain \"[killed]\", got: %q", snap.Output)
	}
}

// TestKill_SetsKilledFlag_MultipleSlots verifies that Kill only affects the
// targeted slot and leaves other slots untouched.
func TestKill_SetsKilledFlag_MultipleSlots(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	injectRunningSlot(g, 0)
	injectRunningSlot(g, 1)

	if err := g.Kill(0); err != nil {
		t.Fatalf("Kill(0) returned unexpected error: %v", err)
	}

	snapshots := g.Slots()

	// Slot 0: killed.
	if !snapshots[0].Killed {
		t.Error("slot 0: expected Killed=true, got false")
	}
	if snapshots[0].Status != SlotDone {
		t.Errorf("slot 0: expected Status=SlotDone, got %v", snapshots[0].Status)
	}

	// Slot 1: still running, not killed.
	if snapshots[1].Killed {
		t.Error("slot 1: expected Killed=false (not killed), got true")
	}
	if snapshots[1].Status != SlotRunning {
		t.Errorf("slot 1: expected Status=SlotRunning, got %v", snapshots[1].Status)
	}

	// Clean up slot 1's cancel to avoid goroutine leak.
	g.mu.Lock()
	if g.slots[1] != nil {
		g.slots[1].cancel()
	}
	g.mu.Unlock()
}

// --- TestNormalExit_KilledFalse ---

// TestNormalExit_KilledFalse verifies that a slot that completes naturally
// (without Kill being called) has Killed=false in its SlotSnapshot.
func TestNormalExit_KilledFalse(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	injectDoneSlot(g, 0)

	snapshots := g.Slots()
	snap := snapshots[0]

	if !snap.Active {
		t.Error("expected slot to be Active, got Active=false")
	}
	if snap.Killed {
		t.Errorf("expected Killed=false for naturally-exited slot, got Killed=true")
	}
	if snap.Status != SlotDone {
		t.Errorf("expected Status=SlotDone, got Status=%v", snap.Status)
	}
}

// --- Kill error cases ---

// TestKill_OutOfRange verifies that Kill returns an error for slot indices
// outside the valid range [0, MaxSlots).
func TestKill_OutOfRange(t *testing.T) {
	t.Parallel()

	g := newTestGateway()

	tests := []struct {
		name   string
		slotID int
	}{
		{"negative index", -1},
		{"index equal to MaxSlots", MaxSlots},
		{"index well above MaxSlots", MaxSlots + 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := g.Kill(tt.slotID)
			if err == nil {
				t.Errorf("Kill(%d): expected error for out-of-range index, got nil", tt.slotID)
			}
		})
	}
}

// TestKill_NilSlot verifies that Kill returns an error when the target slot
// has never been assigned (nil).
func TestKill_NilSlot(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	// Slot 0 is nil by default.
	err := g.Kill(0)
	if err == nil {
		t.Error("Kill(0) on nil slot: expected error, got nil")
	}
}

// TestKill_AlreadyDoneSlot verifies that Kill returns an error when the target
// slot has already completed (SlotDone).
func TestKill_AlreadyDoneSlot(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	injectDoneSlot(g, 0)

	err := g.Kill(0)
	if err == nil {
		t.Error("Kill(0) on done slot: expected error, got nil")
	}
}

// TestKill_IdempotentKilledFlag verifies that the Killed flag remains true
// after Kill is called and the slot is inspected multiple times via Slots().
func TestKill_IdempotentKilledFlag(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	injectRunningSlot(g, 0)

	if err := g.Kill(0); err != nil {
		t.Fatalf("Kill(0): %v", err)
	}

	// Call Slots() twice; the flag must be stable.
	for i := range 3 {
		snap := g.Slots()[0]
		if !snap.Killed {
			t.Errorf("call %d: expected Killed=true, got false", i+1)
		}
		if snap.Status != SlotDone {
			t.Errorf("call %d: expected Status=SlotDone, got %v", i+1, snap.Status)
		}
	}
}

// --- Slots snapshot correctness ---

// TestSlots_InactiveForNilSlot verifies that Slots() returns Active=false for
// slots that have never been assigned.
func TestSlots_InactiveForNilSlot(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	snapshots := g.Slots()

	for i, snap := range snapshots {
		if snap.Active {
			t.Errorf("slot %d: expected Active=false for nil slot, got true", i)
		}
		if snap.Killed {
			t.Errorf("slot %d: expected Killed=false for nil slot, got true", i)
		}
	}
}

// TestSlots_CopiesKilledField verifies that Slots() faithfully copies the
// killed field from the internal slot to SlotSnapshot.Killed for both true
// and false values.
func TestSlots_CopiesKilledField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		killedValue bool
	}{
		{"killed=false", false},
		{"killed=true", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := newTestGateway()
			_, cancel := context.WithCancel(context.Background())
			s := &slot{
				agentName:  "agent",
				jobID:      "job",
				status:     SlotDone,
				killed:     tt.killedValue,
				cancel:     cancel,
				resetTimer: make(chan time.Duration, 1),
			}
			g.mu.Lock()
			g.slots[0] = s
			g.mu.Unlock()

			snap := g.Slots()[0]
			if snap.Killed != tt.killedValue {
				t.Errorf("Slots()[0].Killed = %v, want %v", snap.Killed, tt.killedValue)
			}
		})
	}
}

// --- New ---

// TestNew_DefaultTimeout verifies that New uses the 15-minute default timeout
// when SlotTimeoutMinutes is zero.
func TestNew_DefaultTimeout(t *testing.T) {
	t.Parallel()

	g := New(config.ClaudeConfig{}, "/tmp/workspace", func() {})

	if g.defaultTimeout != 15*time.Minute {
		t.Errorf("defaultTimeout = %v, want %v", g.defaultTimeout, 15*time.Minute)
	}
	if g.workspaceDir != "/tmp/workspace" {
		t.Errorf("workspaceDir = %q, want %q", g.workspaceDir, "/tmp/workspace")
	}
	if g.notify == nil {
		t.Error("notify callback is nil, expected non-nil")
	}
	if g.send == nil {
		t.Error("send callback is nil, expected non-nil")
	}
}

// TestNew_CustomTimeout verifies that New uses the configured timeout when
// SlotTimeoutMinutes is set to a positive value.
func TestNew_CustomTimeout(t *testing.T) {
	t.Parallel()

	g := New(config.ClaudeConfig{SlotTimeoutMinutes: 30}, "", func() {})

	if g.defaultTimeout != 30*time.Minute {
		t.Errorf("defaultTimeout = %v, want %v", g.defaultTimeout, 30*time.Minute)
	}
}

// TestNew_NegativeTimeout verifies that a negative SlotTimeoutMinutes falls
// back to the 15-minute default.
func TestNew_NegativeTimeout(t *testing.T) {
	t.Parallel()

	g := New(config.ClaudeConfig{SlotTimeoutMinutes: -5}, "", func() {})

	if g.defaultTimeout != 15*time.Minute {
		t.Errorf("defaultTimeout = %v, want %v", g.defaultTimeout, 15*time.Minute)
	}
}

// TestNew_AllSlotsNil verifies that a freshly created Gateway has all slots nil.
func TestNew_AllSlotsNil(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	for i, s := range g.slots {
		if s != nil {
			t.Errorf("slot %d: expected nil, got non-nil", i)
		}
	}
}

// --- Dismiss ---

// TestDismiss_Success verifies that Dismiss clears a done slot so it becomes nil.
func TestDismiss_Success(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	injectDoneSlot(g, 3)

	if err := g.Dismiss(3); err != nil {
		t.Fatalf("Dismiss(3) returned unexpected error: %v", err)
	}

	snap := g.Slots()[3]
	if snap.Active {
		t.Error("expected slot to be inactive after Dismiss, got Active=true")
	}
}

// TestDismiss_OutOfRange verifies that Dismiss returns an error for out-of-range indices.
func TestDismiss_OutOfRange(t *testing.T) {
	t.Parallel()

	g := newTestGateway()

	tests := []struct {
		name   string
		slotID int
	}{
		{"negative index", -1},
		{"index equal to MaxSlots", MaxSlots},
		{"index well above MaxSlots", MaxSlots + 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := g.Dismiss(tt.slotID)
			if err == nil {
				t.Errorf("Dismiss(%d): expected error for out-of-range index, got nil", tt.slotID)
			}
			if !strings.Contains(err.Error(), "out of range") {
				t.Errorf("Dismiss(%d): error %q should contain 'out of range'", tt.slotID, err.Error())
			}
		})
	}
}

// TestDismiss_RunningSlot verifies that Dismiss returns an error when the slot
// is still running.
func TestDismiss_RunningSlot(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	cancel := injectRunningSlot(g, 0)
	defer cancel()

	err := g.Dismiss(0)
	if err == nil {
		t.Error("Dismiss(0) on running slot: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cannot dismiss a running slot") {
		t.Errorf("Dismiss(0): error %q should mention 'cannot dismiss a running slot'", err.Error())
	}

	// Verify slot is still there.
	snap := g.Slots()[0]
	if !snap.Active {
		t.Error("expected slot to still be active after failed Dismiss")
	}
}

// TestDismiss_NilSlot verifies that Dismiss returns an error when the slot is nil.
func TestDismiss_NilSlot(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	err := g.Dismiss(0)
	if err == nil {
		t.Error("Dismiss(0) on nil slot: expected error, got nil")
	}
}

// --- SlotSummaries ---

// TestSlotSummaries_EmptyGateway verifies that SlotSummaries returns an empty
// slice when no slots are active.
func TestSlotSummaries_EmptyGateway(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	summaries := g.SlotSummaries()

	if len(summaries) != 0 {
		t.Errorf("expected 0 summaries for empty gateway, got %d", len(summaries))
	}
}

// TestSlotSummaries_MixedSlots verifies that SlotSummaries returns correct
// summaries for a mix of running, done, and nil slots.
func TestSlotSummaries_MixedSlots(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	cancel := injectRunningSlot(g, 0)
	defer cancel()
	injectDoneSlot(g, 2)
	// Slots 1, 3..MaxSlots-1 are nil.

	summaries := g.SlotSummaries()

	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(summaries))
	}

	// First summary should be slot 0 (running).
	if summaries[0].Index != 0 {
		t.Errorf("summaries[0].Index = %d, want 0", summaries[0].Index)
	}
	if summaries[0].Status != "running" {
		t.Errorf("summaries[0].Status = %q, want %q", summaries[0].Status, "running")
	}
	if summaries[0].Team != "test-agent" {
		t.Errorf("summaries[0].Team = %q, want %q", summaries[0].Team, "test-agent")
	}
	if summaries[0].JobID != "job-001" {
		t.Errorf("summaries[0].JobID = %q, want %q", summaries[0].JobID, "job-001")
	}
	if summaries[0].Elapsed == "" {
		t.Error("summaries[0].Elapsed should not be empty for a running slot")
	}

	// Second summary should be slot 2 (done).
	if summaries[1].Index != 2 {
		t.Errorf("summaries[1].Index = %d, want 2", summaries[1].Index)
	}
	if summaries[1].Status != "done" {
		t.Errorf("summaries[1].Status = %q, want %q", summaries[1].Status, "done")
	}
	if summaries[1].Team != "test-agent" {
		t.Errorf("summaries[1].Team = %q, want %q", summaries[1].Team, "test-agent")
	}
	if summaries[1].Elapsed == "" {
		t.Error("summaries[1].Elapsed should not be empty for a done slot")
	}
}

// TestSlotSummaries_SkipsNilSlots verifies that nil slots are excluded from summaries.
func TestSlotSummaries_SkipsNilSlots(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	injectDoneSlot(g, MaxSlots-1) // Only the last slot is active.

	summaries := g.SlotSummaries()
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].Index != MaxSlots-1 {
		t.Errorf("summaries[0].Index = %d, want %d", summaries[0].Index, MaxSlots-1)
	}
}

// --- ExtendSlot ---

// TestExtendSlot_Success verifies that ExtendSlot succeeds for a running slot
// and sends the duration on the resetTimer channel.
func TestExtendSlot_Success(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	cancel := injectRunningSlot(g, 0)
	defer cancel()

	err := g.ExtendSlot(0)
	if err != nil {
		t.Fatalf("ExtendSlot(0) returned unexpected error: %v", err)
	}

	// The resetTimer channel should have received the default timeout.
	g.mu.Lock()
	s := g.slots[0]
	g.mu.Unlock()

	select {
	case d := <-s.resetTimer:
		if d != g.defaultTimeout {
			t.Errorf("resetTimer received %v, want %v", d, g.defaultTimeout)
		}
	default:
		t.Error("expected a value on resetTimer channel, got none")
	}
}

// TestExtendSlot_OutOfRange verifies that ExtendSlot returns an error for
// out-of-range indices.
func TestExtendSlot_OutOfRange(t *testing.T) {
	t.Parallel()

	g := newTestGateway()

	tests := []struct {
		name   string
		slotID int
	}{
		{"negative index", -1},
		{"index equal to MaxSlots", MaxSlots},
		{"index well above MaxSlots", MaxSlots + 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := g.ExtendSlot(tt.slotID)
			if err == nil {
				t.Errorf("ExtendSlot(%d): expected error, got nil", tt.slotID)
			}
			if !strings.Contains(err.Error(), "out of range") {
				t.Errorf("ExtendSlot(%d): error %q should contain 'out of range'", tt.slotID, err.Error())
			}
		})
	}
}

// TestExtendSlot_NilSlot verifies that ExtendSlot returns an error for a nil slot.
func TestExtendSlot_NilSlot(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	err := g.ExtendSlot(0)
	if err == nil {
		t.Error("ExtendSlot(0) on nil slot: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("ExtendSlot(0): error %q should contain 'not running'", err.Error())
	}
}

// TestExtendSlot_DoneSlot verifies that ExtendSlot returns an error for a done slot.
func TestExtendSlot_DoneSlot(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	injectDoneSlot(g, 0)

	err := g.ExtendSlot(0)
	if err == nil {
		t.Error("ExtendSlot(0) on done slot: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("ExtendSlot(0): error %q should contain 'not running'", err.Error())
	}
}

// --- SetNotify ---

// TestSetNotify_ReplacesCallback verifies that SetNotify replaces the notify
// callback and the new callback is invoked by Kill.
func TestSetNotify_ReplacesCallback(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	injectRunningSlot(g, 0)

	var called atomic.Bool
	g.SetNotify(func() {
		called.Store(true)
	})

	// Kill triggers notify.
	if err := g.Kill(0); err != nil {
		t.Fatalf("Kill(0): %v", err)
	}

	if !called.Load() {
		t.Error("expected new notify callback to be called after Kill, but it was not")
	}
}

// --- SetSend ---

// TestSetSend_ReplacesCallback verifies that SetSend replaces the send callback.
func TestSetSend_ReplacesCallback(t *testing.T) {
	t.Parallel()

	g := newTestGateway()

	var received atomic.Bool
	g.SetSend(func(msg SlotTimeoutMsg) {
		received.Store(true)
	})

	// Verify the callback was stored by reading it back under lock.
	g.mu.Lock()
	sendFn := g.send
	g.mu.Unlock()

	sendFn(SlotTimeoutMsg{SlotID: 0})
	if !received.Load() {
		t.Error("expected new send callback to be called, but it was not")
	}
}

// --- Kill (notify callback) ---

// TestKill_InvokesNotify verifies that Kill calls the notify callback.
func TestKill_InvokesNotify(t *testing.T) {
	t.Parallel()

	var notifyCalls atomic.Int32
	g := New(config.ClaudeConfig{}, "", func() {
		notifyCalls.Add(1)
	})
	injectRunningSlot(g, 0)

	if err := g.Kill(0); err != nil {
		t.Fatalf("Kill(0): %v", err)
	}

	if notifyCalls.Load() == 0 {
		t.Error("expected notify to be called at least once after Kill")
	}
}

// --- Slots snapshot field population ---

// TestSlots_PopulatesAllFields verifies that Slots() copies all fields from
// the internal slot to the SlotSnapshot.
func TestSlots_PopulatesAllFields(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	_, cancel := context.WithCancel(context.Background())
	now := time.Now()
	s := &slot{
		agentName:         "my-agent",
		jobID:             "job-42",
		status:            SlotDone,
		killed:            true,
		startTime:         now.Add(-10 * time.Second),
		endTime:           now,
		cancel:            cancel,
		summary:           "test summary",
		model:             "claude-4",
		sessionID:         "sess-abc",
		claudeVersion:     "1.2.3",
		prompt:            "do the thing",
		inputTokens:       100,
		outputTokens:      200,
		turnCount:         3,
		stopReason:        "end_turn",
		pendingTool:       "Bash",
		exitSummary:       "all done",
		subagentsSpawned:  2,
		subagentsInFlight: 1,
		resetTimer:        make(chan time.Duration, 1),
	}
	s.output.WriteString("hello output")
	s.thinkingOutput.WriteString("thinking...")
	s.subagentOutput.WriteString("subagent result")

	g.mu.Lock()
	g.slots[0] = s
	g.mu.Unlock()

	snap := g.Slots()[0]

	if !snap.Active {
		t.Error("Active: got false, want true")
	}
	if snap.AgentName != "my-agent" {
		t.Errorf("AgentName = %q, want %q", snap.AgentName, "my-agent")
	}
	if snap.JobID != "job-42" {
		t.Errorf("JobID = %q, want %q", snap.JobID, "job-42")
	}
	if snap.Status != SlotDone {
		t.Errorf("Status = %v, want SlotDone", snap.Status)
	}
	if !snap.Killed {
		t.Error("Killed: got false, want true")
	}
	if snap.StartTime != now.Add(-10*time.Second) {
		t.Errorf("StartTime = %v, want %v", snap.StartTime, now.Add(-10*time.Second))
	}
	if snap.EndTime != now {
		t.Errorf("EndTime = %v, want %v", snap.EndTime, now)
	}
	if snap.Output != "hello output" {
		t.Errorf("Output = %q, want %q", snap.Output, "hello output")
	}
	if snap.Summary != "test summary" {
		t.Errorf("Summary = %q, want %q", snap.Summary, "test summary")
	}
	if snap.Model != "claude-4" {
		t.Errorf("Model = %q, want %q", snap.Model, "claude-4")
	}
	if snap.Prompt != "do the thing" {
		t.Errorf("Prompt = %q, want %q", snap.Prompt, "do the thing")
	}
	if snap.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", snap.InputTokens)
	}
	if snap.OutputTokens != 200 {
		t.Errorf("OutputTokens = %d, want 200", snap.OutputTokens)
	}
	if snap.TurnCount != 3 {
		t.Errorf("TurnCount = %d, want 3", snap.TurnCount)
	}
	if snap.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", snap.StopReason, "end_turn")
	}
	if snap.PendingTool != "Bash" {
		t.Errorf("PendingTool = %q, want %q", snap.PendingTool, "Bash")
	}
	if snap.ExitSummary != "all done" {
		t.Errorf("ExitSummary = %q, want %q", snap.ExitSummary, "all done")
	}
	if snap.ThinkingOutput != "thinking..." {
		t.Errorf("ThinkingOutput = %q, want %q", snap.ThinkingOutput, "thinking...")
	}
	if snap.SubagentOutput != "subagent result" {
		t.Errorf("SubagentOutput = %q, want %q", snap.SubagentOutput, "subagent result")
	}
	if snap.SessionID != "sess-abc" {
		t.Errorf("SessionID = %q, want %q", snap.SessionID, "sess-abc")
	}
	if snap.SubagentsSpawned != 2 {
		t.Errorf("SubagentsSpawned = %d, want 2", snap.SubagentsSpawned)
	}
	if snap.SubagentsInFlight != 1 {
		t.Errorf("SubagentsInFlight = %d, want 1", snap.SubagentsInFlight)
	}
	if snap.ClaudeVersion != "1.2.3" {
		t.Errorf("ClaudeVersion = %q, want %q", snap.ClaudeVersion, "1.2.3")
	}
}

// --- formatToolUse ---

// TestFormatToolUse exercises every branch in the formatToolUse switch statement.
func TestFormatToolUse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		tool  string
		input any
		want  string
	}{
		// Read
		{
			name:  "Read with file_path",
			tool:  "Read",
			input: map[string]any{"file_path": "/src/main.go"},
			want:  "[tool: Read] /src/main.go",
		},
		{
			name:  "Read with empty file_path",
			tool:  "Read",
			input: map[string]any{"file_path": ""},
			want:  "[tool: Read]",
		},
		{
			name:  "Read with nil input",
			tool:  "Read",
			input: nil,
			want:  "[tool: Read]",
		},

		// Write
		{
			name:  "Write with file_path",
			tool:  "Write",
			input: map[string]any{"file_path": "/src/output.txt"},
			want:  "[tool: Write] /src/output.txt",
		},
		{
			name:  "Write with empty file_path",
			tool:  "Write",
			input: map[string]any{"file_path": ""},
			want:  "[tool: Write]",
		},

		// Edit
		{
			name:  "Edit with file_path",
			tool:  "Edit",
			input: map[string]any{"file_path": "/src/edit.go"},
			want:  "[tool: Edit] /src/edit.go",
		},
		{
			name:  "Edit with empty file_path",
			tool:  "Edit",
			input: map[string]any{},
			want:  "[tool: Edit]",
		},

		// MultiEdit (shares the Edit case)
		{
			name:  "MultiEdit with file_path",
			tool:  "MultiEdit",
			input: map[string]any{"file_path": "/src/multi.go"},
			want:  "[tool: Edit] /src/multi.go",
		},

		// Bash
		{
			name:  "Bash with short command",
			tool:  "Bash",
			input: map[string]any{"command": "ls -la"},
			want:  "[tool: Bash] ls -la",
		},
		{
			name:  "Bash with long command truncated at 72 chars",
			tool:  "Bash",
			input: map[string]any{"command": strings.Repeat("x", 100)},
			want:  "[tool: Bash] " + strings.Repeat("x", 72) + "…",
		},
		{
			name:  "Bash with exactly 72 char command",
			tool:  "Bash",
			input: map[string]any{"command": strings.Repeat("y", 72)},
			want:  "[tool: Bash] " + strings.Repeat("y", 72),
		},
		{
			name:  "Bash with 73 char command truncated",
			tool:  "Bash",
			input: map[string]any{"command": strings.Repeat("z", 73)},
			want:  "[tool: Bash] " + strings.Repeat("z", 72) + "…",
		},
		{
			name:  "Bash with empty command",
			tool:  "Bash",
			input: map[string]any{"command": ""},
			want:  "[tool: Bash]",
		},

		// Task
		{
			name:  "Task with description",
			tool:  "Task",
			input: map[string]any{"description": "refactor the parser"},
			want:  "[tool: Task] refactor the parser",
		},
		{
			name:  "Task with empty description",
			tool:  "Task",
			input: map[string]any{"description": ""},
			want:  "[tool: Task]",
		},

		// Glob
		{
			name:  "Glob with pattern",
			tool:  "Glob",
			input: map[string]any{"pattern": "**/*.go"},
			want:  "[tool: Glob] **/*.go",
		},
		{
			name:  "Glob with empty pattern",
			tool:  "Glob",
			input: map[string]any{"pattern": ""},
			want:  "[tool: Glob]",
		},

		// Grep
		{
			name:  "Grep with pattern",
			tool:  "Grep",
			input: map[string]any{"pattern": "TODO"},
			want:  "[tool: Grep] TODO",
		},
		{
			name:  "Grep with empty pattern",
			tool:  "Grep",
			input: map[string]any{"pattern": ""},
			want:  "[tool: Grep]",
		},

		// WebFetch
		{
			name:  "WebFetch with url",
			tool:  "WebFetch",
			input: map[string]any{"url": "https://example.com"},
			want:  "[tool: WebFetch] https://example.com",
		},
		{
			name:  "WebFetch with empty url",
			tool:  "WebFetch",
			input: map[string]any{"url": ""},
			want:  "[tool: WebFetch]",
		},

		// TodoWrite (no parameters extracted)
		{
			name:  "TodoWrite",
			tool:  "TodoWrite",
			input: map[string]any{"todos": []any{"item1"}},
			want:  "[tool: TodoWrite]",
		},
		{
			name:  "TodoWrite with nil input",
			tool:  "TodoWrite",
			input: nil,
			want:  "[tool: TodoWrite]",
		},

		// TodoRead (no parameters extracted)
		{
			name:  "TodoRead",
			tool:  "TodoRead",
			input: nil,
			want:  "[tool: TodoRead]",
		},
		{
			name:  "TodoRead with empty map",
			tool:  "TodoRead",
			input: map[string]any{},
			want:  "[tool: TodoRead]",
		},

		// LS
		{
			name:  "LS with path",
			tool:  "LS",
			input: map[string]any{"path": "/usr/local"},
			want:  "[tool: LS] /usr/local",
		},
		{
			name:  "LS with empty path",
			tool:  "LS",
			input: map[string]any{"path": ""},
			want:  "[tool: LS]",
		},

		// Unknown/default tool
		{
			name:  "unknown tool name",
			tool:  "SomeCustomTool",
			input: map[string]any{"foo": "bar"},
			want:  "[tool: SomeCustomTool]",
		},
		{
			name:  "empty tool name",
			tool:  "",
			input: nil,
			want:  "[tool: ]",
		},

		// Non-map input (type assertion to map fails)
		{
			name:  "Read with non-map input falls to default",
			tool:  "Read",
			input: "not a map",
			want:  "[tool: Read]",
		},
		{
			name:  "Bash with non-map input falls to default",
			tool:  "Bash",
			input: 42,
			want:  "[tool: Bash]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatToolUse(tt.tool, tt.input)
			if got != tt.want {
				t.Errorf("formatToolUse(%q, %v) = %q, want %q", tt.tool, tt.input, got, tt.want)
			}
		})
	}
}

// --- Kill (additional edge cases) ---

// TestKill_NotifyCalledExactlyOnce verifies Kill calls notify exactly once.
func TestKill_NotifyCalledExactlyOnce(t *testing.T) {
	t.Parallel()

	var count atomic.Int32
	g := New(config.ClaudeConfig{}, "", func() {
		count.Add(1)
	})
	injectRunningSlot(g, 0)

	if err := g.Kill(0); err != nil {
		t.Fatalf("Kill(0): %v", err)
	}

	if got := count.Load(); got != 1 {
		t.Errorf("notify called %d times, want 1", got)
	}
}

// --- Dismiss after Kill ---

// TestDismiss_AfterKill verifies that a killed slot can be dismissed.
func TestDismiss_AfterKill(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	injectRunningSlot(g, 0)

	if err := g.Kill(0); err != nil {
		t.Fatalf("Kill(0): %v", err)
	}

	if err := g.Dismiss(0); err != nil {
		t.Fatalf("Dismiss(0) after Kill: unexpected error: %v", err)
	}

	snap := g.Slots()[0]
	if snap.Active {
		t.Error("expected slot to be inactive after Dismiss, got Active=true")
	}
}

// --- SlotSummaries elapsed time ---

// TestSlotSummaries_ElapsedForRunningSlot verifies that a running slot has a
// non-empty elapsed time string.
func TestSlotSummaries_ElapsedForRunningSlot(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	// Inject a running slot with a start time in the past.
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &slot{
		agentName:  "elapsed-agent",
		jobID:      "job-elapsed",
		status:     SlotRunning,
		startTime:  time.Now().Add(-2 * time.Minute),
		cancel:     cancel,
		resetTimer: make(chan time.Duration, 1),
	}
	g.mu.Lock()
	g.slots[0] = s
	g.mu.Unlock()

	summaries := g.SlotSummaries()
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].Elapsed == "" {
		t.Error("expected non-empty Elapsed for running slot")
	}
	// The elapsed time should contain "2m" since we set start 2 minutes ago.
	if !strings.Contains(summaries[0].Elapsed, "2m") {
		t.Errorf("Elapsed = %q, expected to contain '2m'", summaries[0].Elapsed)
	}
}

// TestSlotSummaries_ElapsedForDoneSlot verifies that a done slot's elapsed
// time is computed from StartTime to EndTime (not current time).
func TestSlotSummaries_ElapsedForDoneSlot(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	_, cancel := context.WithCancel(context.Background())
	start := time.Now().Add(-10 * time.Second)
	end := start.Add(5 * time.Second)
	s := &slot{
		agentName:  "done-agent",
		jobID:      "job-done",
		status:     SlotDone,
		startTime:  start,
		endTime:    end,
		cancel:     cancel,
		resetTimer: make(chan time.Duration, 1),
	}
	g.mu.Lock()
	g.slots[0] = s
	g.mu.Unlock()

	summaries := g.SlotSummaries()
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].Elapsed != "5s" {
		t.Errorf("Elapsed = %q, want %q", summaries[0].Elapsed, "5s")
	}
}

// --- ExtendSlot channel full ---

// TestExtendSlot_ChannelAlreadyFull verifies that ExtendSlot does not block
// when the resetTimer channel already has a pending value (non-blocking send).
func TestExtendSlot_ChannelAlreadyFull(t *testing.T) {
	t.Parallel()

	g := newTestGateway()
	cancel := injectRunningSlot(g, 0)
	defer cancel()

	// Fill the channel first.
	g.mu.Lock()
	s := g.slots[0]
	g.mu.Unlock()
	s.resetTimer <- 1 * time.Minute

	// This should not block even though the channel is full.
	done := make(chan error, 1)
	go func() {
		done <- g.ExtendSlot(0)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ExtendSlot(0) returned unexpected error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("ExtendSlot(0) blocked — should be non-blocking when channel is full")
	}
}
