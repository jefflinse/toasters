package gateway

import (
	"context"
	"strings"
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
