package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

func TestFanoutGroupKey(t *testing.T) {
	t.Parallel()

	mk := func(name, taskID string) service.ChatEntry {
		return service.ChatEntry{
			Kind:         service.ChatEntryKindWorkerStream,
			WorkerStream: &service.WorkerStreamSnapshot{WorkerName: name, TaskID: taskID},
		}
	}

	if k, ok := fanoutGroupKey(mk("graph:implement#0", "t1")); !ok || k != "t1\x00implement" {
		t.Errorf("branch key = %q ok=%v, want t1/implement", k, ok)
	}
	if k, ok := fanoutGroupKey(mk("graph:implement.judge", "t1")); !ok || k != "t1\x00implement" {
		t.Errorf("judge key = %q ok=%v, want t1/implement", k, ok)
	}
	// Same parent, different task → different key (don't merge across tasks).
	k1, _ := fanoutGroupKey(mk("graph:implement#0", "t1"))
	k2, _ := fanoutGroupKey(mk("graph:implement#0", "t2"))
	if k1 == k2 {
		t.Error("branches in different tasks must not share a group key")
	}
	// Ordinary node and non-worker-stream entries never group.
	if _, ok := fanoutGroupKey(mk("graph:test", "t1")); ok {
		t.Error("ordinary node should not produce a group key")
	}
	if _, ok := fanoutGroupKey(service.ChatEntry{Message: service.ChatMessage{Role: service.MessageRoleUser}}); ok {
		t.Error("non-worker-stream entry should not produce a group key")
	}
}

func TestRenderFanoutGroupSummary(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.progress.tasks = map[string][]service.Task{"job-1": {{ID: "task-1", Title: "DB layer"}}}
	start := time.Unix(1000, 0)
	mk := func(node string) service.ChatEntry {
		return service.ChatEntry{
			Kind: service.ChatEntryKindWorkerStream,
			WorkerStream: &service.WorkerStreamSnapshot{
				WorkerName: "graph:" + node, JobID: "job-1", TaskID: "task-1",
				Done: true, StartedAt: start, LastActivity: start.Add(90 * time.Second),
			},
		}
	}
	m.chat.entries = []service.ChatEntry{mk("implement#0"), mk("implement#1"), mk("implement.judge")}

	out := m.renderFanoutGroupSummary([]int{0, 1, 2}, 90)
	for _, want := range []string{"📦", "DB layer", "implement", "2 branches + judge", "✓", "1m30s"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q; got:\n%s", want, out)
		}
	}
}

func TestUpdateViewportContent_CollapsesCompletedFanout(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.width = 100
	m.height = 40
	m.chatViewport.SetWidth(90)
	m.chatViewport.SetHeight(40)
	m.progress.tasks = map[string][]service.Task{"job-1": {{ID: "task-1", Title: "DB layer"}}}

	// A user message so the welcome screen is skipped and entries render.
	m.chat.entries = append(m.chat.entries, service.ChatEntry{
		Message: service.ChatMessage{Role: service.MessageRoleUser, Content: "go"},
	})
	start := time.Unix(1000, 0)
	addCard := func(node string, done bool) {
		m.chat.entries = append(m.chat.entries, service.ChatEntry{
			Kind: service.ChatEntryKindWorkerStream,
			WorkerStream: &service.WorkerStreamSnapshot{
				WorkerName: "graph:" + node, JobID: "job-1", TaskID: "task-1",
				SessionID: "graph:task-1:" + node, Done: done,
				StartedAt: start, LastActivity: start.Add(time.Minute),
			},
		})
	}
	// Fan-out group is done, but the task isn't (verify still running) — so the
	// whole-task roll-up doesn't fire and the fan-out group roll-up is exercised.
	addCard("implement#0", true)
	addCard("implement#1", true)
	addCard("implement.judge", true)
	addCard("verify", false)

	// Nothing selected → group collapses to one summary, branch ids hidden,
	// the still-running card stays visible.
	m.chat.selectedMsgIdx = -1
	m.updateViewportContent()
	out := m.chatViewport.View()
	if !strings.Contains(out, "📦") {
		t.Errorf("expected collapsed fan-out summary; got:\n%s", out)
	}
	if strings.Contains(out, "implement#0") {
		t.Errorf("branch cards should be hidden when group is collapsed; got:\n%s", out)
	}
	if !strings.Contains(out, "verify") {
		t.Errorf("still-running card should remain visible; got:\n%s", out)
	}

	// Selecting a member expands the group so its branches are reachable again.
	m.chat.selectedMsgIdx = 1 // first worker-stream entry (index 0 is the user msg)
	m.updateViewportContent()
	out2 := m.chatViewport.View()
	if !strings.Contains(out2, "implement#0") {
		t.Errorf("expected expanded branch cards when a member is selected; got:\n%s", out2)
	}
}

// TestUpdateViewportContent_CollapsesCompletedTask verifies the whole-task
// roll-up: when every card of a task is done, the task collapses to a single
// "✓ <task>" line (superseding the per-fan-out-group roll-up), and expands when
// a member is selected.
func TestUpdateViewportContent_CollapsesCompletedTask(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.width = 100
	m.height = 40
	m.chatViewport.SetWidth(90)
	m.chatViewport.SetHeight(40)
	m.progress.tasks = map[string][]service.Task{"job-1": {{ID: "task-1", Title: "DB layer"}}}

	m.chat.entries = append(m.chat.entries, service.ChatEntry{
		Message: service.ChatMessage{Role: service.MessageRoleUser, Content: "go"},
	})
	start := time.Unix(1000, 0)
	for _, node := range []string{"plan", "implement#0", "implement#1", "test"} {
		m.chat.entries = append(m.chat.entries, service.ChatEntry{
			Kind: service.ChatEntryKindWorkerStream,
			WorkerStream: &service.WorkerStreamSnapshot{
				WorkerName: "graph:" + node, JobID: "job-1", TaskID: "task-1",
				SessionID: "graph:task-1:" + node, Done: true,
				StartedAt: start, LastActivity: start.Add(time.Minute),
			},
		})
	}

	// Nothing selected → the whole task collapses to one ✓ line; no fan-out
	// summary, no individual node ids.
	m.chat.selectedMsgIdx = -1
	m.updateViewportContent()
	out := m.chatViewport.View()
	if !strings.Contains(out, "✓ DB layer") {
		t.Errorf("expected whole-task summary '✓ DB layer'; got:\n%s", out)
	}
	if strings.Contains(out, "📦") {
		t.Errorf("whole-task roll-up should supersede the fan-out summary; got:\n%s", out)
	}
	if strings.Contains(out, "implement#0") || strings.Contains(out, "· plan") {
		t.Errorf("node cards should be hidden when the task is collapsed; got:\n%s", out)
	}

	// Selecting a member expands the task back to its nodes.
	m.chat.selectedMsgIdx = 1
	m.updateViewportContent()
	out2 := m.chatViewport.View()
	if !strings.Contains(out2, "implement#0") {
		t.Errorf("expected expanded node cards when a member is selected; got:\n%s", out2)
	}
}

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
	a := &runtimeSlot{sessionID: "graph:t:impl#0", workerName: "graph:impl#0", jobID: "j1"}
	b := &runtimeSlot{sessionID: "graph:t:impl#1", workerName: "graph:impl#1", jobID: "j1"}

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
	a := &runtimeSlot{sessionID: "sA", workerName: "A", jobID: "j"}
	b := &runtimeSlot{sessionID: "sB", workerName: "B", jobID: "j"}

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
	s := &runtimeSlot{sessionID: "s", workerName: "graph:implement#0", jobID: "j"}

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

func TestCollapseBlankLines(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"one line", "one line"},
		{"a\n\n\n\nb", "a\n\nb"},       // run collapsed to one blank
		{"\n\n\nLeading", "Leading"},   // leading blanks dropped
		{"Trailing\n\n\n", "Trailing"}, // trailing blanks dropped
		{"a\n  \n\t\nb", "a\n\nb"},     // whitespace-only lines are blank
		{"verify the file.\n\n\n\nread", "verify the file.\n\nread"}, // the motivating case
		{"p1\n\np2\n\np3", "p1\n\np2\n\np3"},                         // single blanks preserved
	}
	for _, c := range cases {
		if got := collapseBlankLines(c.in); got != c.want {
			t.Errorf("collapseBlankLines(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
