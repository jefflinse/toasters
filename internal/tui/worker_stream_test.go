package tui

import (
	"testing"

	"github.com/jefflinse/toasters/internal/service"
)

func workerStreamCards(m *Model) []*service.WorkerStreamSnapshot {
	var out []*service.WorkerStreamSnapshot
	for i := range m.chat.entries {
		e := &m.chat.entries[i]
		if e.Kind == service.ChatEntryKindWorkerStream && e.WorkerStream != nil {
			out = append(out, e.WorkerStream)
		}
	}
	return out
}

// TestWorkerStreamCardsAreStablePerSession verifies that interleaved activity
// from concurrent fan-out sessions does not spawn duplicate cards or reorder
// them — each session keeps one card that updates in place.
func TestWorkerStreamCardsAreStablePerSession(t *testing.T) {
	m := newMinimalModel(t)
	a := &runtimeSlot{sessionID: "graph:t:impl#0", agentName: "graph:impl#0", jobID: "j1"}
	b := &runtimeSlot{sessionID: "graph:t:impl#1", agentName: "graph:impl#1", jobID: "j1"}

	// Interleave: A, B, A — the classic fan-out pattern that used to reorder.
	m.appendWorkerStreamText(a, "a1 ")
	m.appendWorkerStreamText(b, "b1 ")
	m.appendWorkerStreamText(a, "a2")

	cards := workerStreamCards(&m)
	if len(cards) != 2 {
		t.Fatalf("expected 2 stable cards, got %d", len(cards))
	}
	if cards[0].SessionID != a.sessionID {
		t.Errorf("card[0] session = %q, want %q (first-activity order, no reorder)", cards[0].SessionID, a.sessionID)
	}
	if cards[1].SessionID != b.sessionID {
		t.Errorf("card[1] session = %q, want %q", cards[1].SessionID, b.sessionID)
	}
	if got := cards[0].Items[0].Text; got != "a1 a2" {
		t.Errorf("A text = %q, want %q (coalesced in place across the interleave)", got, "a1 a2")
	}
}

// TestWorkerStreamToolCallResultMergePerSession verifies a tool result merges
// into its call within the same session's card even when another session's
// activity interleaves — the bug that caused tool calls to render twice.
func TestWorkerStreamToolCallResultMergePerSession(t *testing.T) {
	m := newMinimalModel(t)
	a := &runtimeSlot{sessionID: "sA", agentName: "A", jobID: "j"}
	b := &runtimeSlot{sessionID: "sB", agentName: "B", jobID: "j"}

	m.appendWorkerStreamToolCall(a, "call1", "write_file", nil)
	m.appendWorkerStreamToolCall(b, "call2", "shell", nil) // interleave a different session
	m.appendWorkerStreamToolResult(a, "call1", "write_file", "wrote 259 bytes", false)

	card := m.findWorkerStream("sA")
	if card == nil {
		t.Fatal("session A card missing")
	}
	tools := 0
	for _, it := range card.Items {
		if it.Kind == service.WorkerStreamItemTool {
			tools++
		}
	}
	if tools != 1 {
		t.Fatalf("expected 1 merged tool item, got %d (call+result rendered separately)", tools)
	}
	if card.Items[0].ToolResult != "wrote 259 bytes" {
		t.Errorf("result not merged into the call item: %q", card.Items[0].ToolResult)
	}
}

// TestWorkerStreamToolResultEmptyCallIDFallback verifies a result with no call
// ID (the graph-node case before the graphexec fix, and a belt-and-suspenders
// backstop) still merges into the most recent pending call of the same name
// rather than synthesizing a duplicate item.
func TestWorkerStreamToolResultEmptyCallIDFallback(t *testing.T) {
	m := newMinimalModel(t)
	s := &runtimeSlot{sessionID: "s", agentName: "graph:implement#0", jobID: "j"}

	m.appendWorkerStreamToolCall(s, "call-real-id", "write_file", nil)
	// Result arrives with an empty CallID (mycelium doesn't surface it).
	m.appendWorkerStreamToolResult(s, "", "write_file", "wrote 74 bytes to main.go", false)

	card := m.findWorkerStream("s")
	if card == nil {
		t.Fatal("card missing")
	}
	tools := 0
	for _, it := range card.Items {
		if it.Kind == service.WorkerStreamItemTool {
			tools++
		}
	}
	if tools != 1 {
		t.Fatalf("expected 1 merged tool item, got %d (empty-callID result duplicated the call)", tools)
	}
	if card.Items[0].ToolResult != "wrote 74 bytes to main.go" || card.Items[0].EndedAt.IsZero() {
		t.Errorf("result not merged into the pending call: %+v", card.Items[0])
	}
}
