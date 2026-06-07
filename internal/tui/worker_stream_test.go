package tui

import (
	"strings"
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

func TestWorkerStreamDisplayOrder(t *testing.T) {
	card := func(done bool) service.ChatEntry {
		return service.ChatEntry{
			Kind:         service.ChatEntryKindWorkerStream,
			WorkerStream: &service.WorkerStreamSnapshot{Done: done},
		}
	}
	other := service.ChatEntry{Message: service.ChatMessage{Role: service.MessageRoleAssistant, Content: "hi"}}

	// [other, done, active, done, other, active]
	entries := []service.ChatEntry{other, card(true), card(false), card(true), other, card(false)}
	got := workerStreamDisplayOrder(entries)

	// Run at 1..3 reorders to done-first (1,3) then active (2); singles unchanged.
	want := []int{0, 1, 3, 2, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("order = %v, want %v", got, want)
			break
		}
	}
}

// TestWorkerStreamBlock_FinishedCardsCollapse verifies a finished card renders
// only its two header rows (body hidden) unless it's selected, while a running
// card always shows its body.
func TestWorkerStreamBlock_FinishedCardsCollapse(t *testing.T) {
	m := newMinimalModel(t)
	const marker = "UNIQUE_BODY_MARKER_TEXT"

	mk := func(done bool) *service.WorkerStreamSnapshot {
		return &service.WorkerStreamSnapshot{
			SessionID:  "graph:t:plan",
			WorkerName: "graph:plan",
			JobID:      "j1",
			Done:       done,
			Items:      []service.WorkerStreamItem{{Kind: service.WorkerStreamItemText, Text: marker}},
		}
	}

	cases := []struct {
		name     string
		done     bool
		selected bool
		wantBody bool
	}{
		{"running shows body", false, false, true},
		{"finished unselected collapses", true, false, false},
		{"finished selected peeks inline", true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := m.renderWorkerStreamBlock(mk(c.done), 80, c.selected)
			if got := strings.Contains(out, marker); got != c.wantBody {
				t.Errorf("body present = %v, want %v", got, c.wantBody)
			}
		})
	}
}
