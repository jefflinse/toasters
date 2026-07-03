package tui

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	xansi "github.com/charmbracelet/x/ansi"

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

// TestAttachWorkerStreamFileChange_MergesIntoPendingCall verifies a
// session.file_change event pairs with the matching in-flight write_file
// call by tool name + path (parsed from ToolArgs), and — critically — does
// not mark the item done. The later tool_result must still be able to
// complete the same item instead of finding it "already done" and
// synthesizing a duplicate.
func TestAttachWorkerStreamFileChange_MergesIntoPendingCall(t *testing.T) {
	m := newMinimalModel(t)
	s := &runtimeSlot{sessionID: "s", workerName: "graph:implement", jobID: "j"}

	args, _ := json.Marshal(map[string]string{"path": "main.go"})
	m.appendWorkerStreamToolCall(s, "call1", "write_file", args)
	m.attachWorkerStreamFileChange(s, SessionFileChangeMsg{
		SessionID: "s", ToolName: "write_file", Path: "main.go",
		Diff: "@@ -1,1 +1,1 @@\n-a\n+b", Added: 1, Removed: 1,
	})

	card := m.findWorkerStream("s")
	if card == nil || len(card.Items) != 1 {
		t.Fatalf("expected 1 item, card=%+v", card)
	}
	it := card.Items[0]
	if it.FileDiff == "" {
		t.Fatal("diff not attached")
	}
	if !it.EndedAt.IsZero() {
		t.Error("attaching the diff must not complete the item — tool_result still needs to merge into it")
	}

	// The result arrives after the diff and must merge into the same item.
	m.appendWorkerStreamToolResult(s, "call1", "write_file", "wrote 12 bytes", false)
	if len(card.Items) != 1 {
		t.Fatalf("expected the result to merge into the same item, got %d items", len(card.Items))
	}
	if card.Items[0].FileDiff == "" {
		t.Error("diff lost after the result merged in")
	}
	if card.Items[0].ToolResult != "wrote 12 bytes" {
		t.Errorf("result not merged: %+v", card.Items[0])
	}
}

// TestAttachWorkerStreamFileChange_OldestPendingFirst mirrors
// TestAttachFileChange_OldestPendingFirst in slot_output_test.go: two
// parallel calls to the same tool+path can both be pending at once (mycelium
// fires all tool_call events up front, then executes sequentially), and
// their file_change notifications arrive in execution (= insertion) order.
// The first notification must land on the first (oldest) pending item, the
// second on the second, not both on the newest.
func TestAttachWorkerStreamFileChange_OldestPendingFirst(t *testing.T) {
	m := newMinimalModel(t)
	s := &runtimeSlot{sessionID: "s", workerName: "graph:implement", jobID: "j"}

	args, _ := json.Marshal(map[string]string{"path": "main.go"})
	m.appendWorkerStreamToolCall(s, "call1", "write_file", args)
	m.appendWorkerStreamToolCall(s, "call2", "write_file", args)

	m.attachWorkerStreamFileChange(s, SessionFileChangeMsg{
		SessionID: "s", ToolName: "write_file", Path: "main.go", Diff: "diff-1", Added: 1,
	})
	m.attachWorkerStreamFileChange(s, SessionFileChangeMsg{
		SessionID: "s", ToolName: "write_file", Path: "main.go", Diff: "diff-2", Added: 2,
	})

	card := m.findWorkerStream("s")
	if card == nil || len(card.Items) != 2 {
		t.Fatalf("expected 2 items, card=%+v", card)
	}
	if card.Items[0].FileDiff != "diff-1" {
		t.Errorf("Items[0].FileDiff = %q, want %q (first notification -> oldest pending item)", card.Items[0].FileDiff, "diff-1")
	}
	if card.Items[1].FileDiff != "diff-2" {
		t.Errorf("Items[1].FileDiff = %q, want %q (second notification -> second item)", card.Items[1].FileDiff, "diff-2")
	}
}

// TestAttachWorkerStreamFileChange_NameOnlyFallback verifies that when no
// tool item's path argument matches (e.g. args missing), the newest item
// with the same tool name is used instead.
func TestAttachWorkerStreamFileChange_NameOnlyFallback(t *testing.T) {
	m := newMinimalModel(t)
	s := &runtimeSlot{sessionID: "s", workerName: "graph:implement", jobID: "j"}

	m.appendWorkerStreamToolCall(s, "call1", "edit_file", nil)
	m.attachWorkerStreamFileChange(s, SessionFileChangeMsg{
		SessionID: "s", ToolName: "edit_file", Path: "unresolved/path.go",
		Diff: "diff body", Added: 2,
	})

	card := m.findWorkerStream("s")
	if card == nil || len(card.Items) != 1 {
		t.Fatalf("expected the fallback to reuse the existing item, got %+v", card)
	}
	if card.Items[0].FileDiff != "diff body" {
		t.Errorf("diff not attached via name-only fallback: %+v", card.Items[0])
	}
}

// TestAttachWorkerStreamFileChange_SynthesizesOnTotalMiss verifies that with
// no matching tool item at all, a completed item is synthesized so the diff
// still surfaces rather than being dropped silently.
func TestAttachWorkerStreamFileChange_SynthesizesOnTotalMiss(t *testing.T) {
	m := newMinimalModel(t)
	s := &runtimeSlot{sessionID: "s", workerName: "graph:implement", jobID: "j"}

	m.attachWorkerStreamFileChange(s, SessionFileChangeMsg{
		SessionID: "s", ToolName: "write_file", Path: "new.go",
		Diff: "diff body", Created: true, Truncated: true,
	})

	card := m.findWorkerStream("s")
	if card == nil || len(card.Items) != 1 {
		t.Fatalf("expected a synthesized item, got %+v", card)
	}
	it := card.Items[0]
	if it.Kind != service.WorkerStreamItemTool || it.ToolName != "write_file" {
		t.Errorf("synthesized item wrong: %+v", it)
	}
	if it.FileDiff != "diff body" || !it.DiffCreated || !it.DiffTruncated {
		t.Errorf("diff fields not set on synthesized item: %+v", it)
	}
	if it.EndedAt.IsZero() {
		t.Error("synthesized item should be marked complete")
	}
}

// TestAttachWorkerStreamShellExec_MergesIntoPendingCall mirrors
// TestAttachWorkerStreamFileChange_MergesIntoPendingCall: shell_exec fires
// before the tool_result, so attaching it must not mark the item done — the
// later result still needs to merge into the same item.
func TestAttachWorkerStreamShellExec_MergesIntoPendingCall(t *testing.T) {
	m := newMinimalModel(t)
	s := &runtimeSlot{sessionID: "s", workerName: "graph:implement", jobID: "j"}

	m.appendWorkerStreamToolCall(s, "call1", "shell", json.RawMessage(`{"command":"go test ./..."}`))
	m.attachWorkerStreamShellExec(s, SessionShellExecMsg{
		SessionID: "s", ExitCode: 1, DurationMs: 500, OutputBytes: 100,
	})

	card := m.findWorkerStream("s")
	if card == nil || len(card.Items) != 1 {
		t.Fatalf("expected 1 item, card=%+v", card)
	}
	it := card.Items[0]
	if !it.HasShellExec {
		t.Fatal("shell exec not attached")
	}
	if !it.EndedAt.IsZero() {
		t.Error("attaching shell exec must not complete the item — tool_result still needs to merge into it")
	}

	m.appendWorkerStreamToolResult(s, "call1", "shell", "FAIL", true)
	if len(card.Items) != 1 {
		t.Fatalf("expected the result to merge into the same item, got %d items", len(card.Items))
	}
	if !card.Items[0].HasShellExec {
		t.Error("shell exec lost after the result merged in")
	}
	if card.Items[0].ShellExitCode != 1 {
		t.Errorf("ShellExitCode = %d, want 1", card.Items[0].ShellExitCode)
	}
}

// TestAttachWorkerStreamShellExec_SynthesizesOnTotalMiss mirrors
// TestAttachWorkerStreamFileChange_SynthesizesOnTotalMiss: with no matching
// tool item, a completed item is synthesized so the status still surfaces.
func TestAttachWorkerStreamShellExec_SynthesizesOnTotalMiss(t *testing.T) {
	m := newMinimalModel(t)
	s := &runtimeSlot{sessionID: "s", workerName: "graph:implement", jobID: "j"}

	m.attachWorkerStreamShellExec(s, SessionShellExecMsg{
		SessionID: "s", ExitCode: 137, TimedOut: true,
	})

	card := m.findWorkerStream("s")
	if card == nil || len(card.Items) != 1 {
		t.Fatalf("expected a synthesized item, got %+v", card)
	}
	it := card.Items[0]
	if it.Kind != service.WorkerStreamItemTool || it.ToolName != "shell" {
		t.Errorf("synthesized item wrong: %+v", it)
	}
	if !it.HasShellExec || it.ShellExitCode != 137 || !it.ShellTimedOut {
		t.Errorf("shell exec fields not set on synthesized item: %+v", it)
	}
	if it.EndedAt.IsZero() {
		t.Error("synthesized item should be marked complete")
	}
}

// TestAttachWorkerStreamWorkerSpawn_MergesIntoPendingCall mirrors
// TestAttachWorkerStreamShellExec_MergesIntoPendingCall: the notifier fires
// from within CoreTools.Execute (SpawnAndWait blocks until the child
// finishes), before the tool_result, so attaching it must not mark the item
// done — the later result still needs to merge into the same item.
func TestAttachWorkerStreamWorkerSpawn_MergesIntoPendingCall(t *testing.T) {
	m := newMinimalModel(t)
	s := &runtimeSlot{sessionID: "s", workerName: "graph:implement", jobID: "j"}

	m.appendWorkerStreamToolCall(s, "call1", "spawn_worker", json.RawMessage(`{"role":"coder"}`))
	m.attachWorkerStreamWorkerSpawn(s, SessionWorkerSpawnMsg{
		SessionID: "s", Role: "coder", Task: "implement the thing", JobID: "j-1", Depth: 1,
	})

	card := m.findWorkerStream("s")
	if card == nil || len(card.Items) != 1 {
		t.Fatalf("expected 1 item, card=%+v", card)
	}
	it := card.Items[0]
	if !it.HasWorkerSpawn {
		t.Fatal("worker spawn not attached")
	}
	if !it.EndedAt.IsZero() {
		t.Error("attaching worker spawn must not complete the item — tool_result still needs to merge into it")
	}

	m.appendWorkerStreamToolResult(s, "call1", "spawn_worker", "child done", false)
	if len(card.Items) != 1 {
		t.Fatalf("expected the result to merge into the same item, got %d items", len(card.Items))
	}
	if !card.Items[0].HasWorkerSpawn {
		t.Error("worker spawn lost after the result merged in")
	}
	if card.Items[0].SpawnRole != "coder" {
		t.Errorf("SpawnRole = %q, want %q", card.Items[0].SpawnRole, "coder")
	}
}

// TestAttachWorkerStreamWorkerSpawn_SynthesizesOnTotalMiss mirrors
// TestAttachWorkerStreamShellExec_SynthesizesOnTotalMiss: with no matching
// tool item, a completed item is synthesized so the status still surfaces.
func TestAttachWorkerStreamWorkerSpawn_SynthesizesOnTotalMiss(t *testing.T) {
	m := newMinimalModel(t)
	s := &runtimeSlot{sessionID: "s", workerName: "graph:implement", jobID: "j"}

	m.attachWorkerStreamWorkerSpawn(s, SessionWorkerSpawnMsg{
		SessionID: "s", Role: "coder", Failed: true, Error: "role not found",
	})

	card := m.findWorkerStream("s")
	if card == nil || len(card.Items) != 1 {
		t.Fatalf("expected a synthesized item, got %+v", card)
	}
	it := card.Items[0]
	if it.Kind != service.WorkerStreamItemTool || it.ToolName != "spawn_worker" {
		t.Errorf("synthesized item wrong: %+v", it)
	}
	if !it.HasWorkerSpawn || !it.SpawnFailed || it.SpawnError != "role not found" {
		t.Errorf("worker spawn fields not set on synthesized item: %+v", it)
	}
	if it.EndedAt.IsZero() {
		t.Error("synthesized item should be marked complete")
	}
}

// TestSessionFileChangeMsg_AccumulatesDiffStatAndPatchesActivity verifies
// that Model.Update accumulates diffAdded/diffRemoved on the runtime slot
// across successive session.file_change events for a session (grid.go's
// worker-card diff-stat is driven off these totals), and patches the
// matching activity label (tool name + filename) with a "+N −M" suffix
// exactly once — a second file_change for the same tool but a different
// path must not touch the already-patched label.
func TestSessionFileChangeMsg_AccumulatesDiffStatAndPatchesActivity(t *testing.T) {
	m := newMinimalModel(t)
	slot := &runtimeSlot{sessionID: "s", workerName: "graph:implement", jobID: "j"}
	m.runtimeSessions["s"] = slot

	args, _ := json.Marshal(map[string]string{"path": "main.go"})
	res, _ := m.Update(SessionToolCallMsg{SessionID: "s", ToolID: "call1", ToolName: "write_file", ToolInput: string(args)})
	got := res.(*Model)

	res, _ = got.Update(SessionFileChangeMsg{
		SessionID: "s", ToolName: "write_file", Path: "main.go",
		Diff: "diff-1", Added: 3, Removed: 1,
	})
	got = res.(*Model)

	if slot.diffAdded != 3 || slot.diffRemoved != 1 {
		t.Fatalf("after first file_change: diffAdded=%d diffRemoved=%d, want 3/1", slot.diffAdded, slot.diffRemoved)
	}
	if len(slot.activities) != 1 || slot.activities[0].label != "write: main.go +3 −1" {
		t.Fatalf("activity label = %+v, want %q", slot.activities, "write: main.go +3 −1")
	}

	// A second file_change for the same tool (another write_file call) must
	// still accumulate the cumulative totals but must not re-patch the
	// already-patched activity label.
	res, _ = got.Update(SessionFileChangeMsg{
		SessionID: "s", ToolName: "write_file", Path: "other.go",
		Diff: "diff-2", Added: 2, Removed: 0,
	})
	got = res.(*Model)

	if slot.diffAdded != 5 || slot.diffRemoved != 1 {
		t.Fatalf("after second file_change: diffAdded=%d diffRemoved=%d, want 5/1", slot.diffAdded, slot.diffRemoved)
	}
	if len(slot.activities) != 1 || slot.activities[0].label != "write: main.go +3 −1" {
		t.Fatalf("activity label should not be double-patched: %+v", slot.activities)
	}
}

// TestSessionFileChangeMsg_ParallelSamePathCalls verifies stat attribution
// when a model issues two parallel tool calls to the same tool+path in one
// turn: all tool_call events (and thus activity entries) land before either
// call executes, and file_change notifications then arrive in execution
// (= insertion) order. Each stat must land on its own activity, oldest
// unpatched first — a newest-first walk would put call1's stat on call2's
// label.
func TestSessionFileChangeMsg_ParallelSamePathCalls(t *testing.T) {
	m := newMinimalModel(t)
	slot := &runtimeSlot{sessionID: "s", workerName: "graph:implement", jobID: "j"}
	m.runtimeSessions["s"] = slot

	args, _ := json.Marshal(map[string]string{"path": "main.go"})
	res, _ := m.Update(SessionToolCallMsg{SessionID: "s", ToolID: "call1", ToolName: "edit_file", ToolInput: string(args)})
	got := res.(*Model)
	res, _ = got.Update(SessionToolCallMsg{SessionID: "s", ToolID: "call2", ToolName: "edit_file", ToolInput: string(args)})
	got = res.(*Model)

	res, _ = got.Update(SessionFileChangeMsg{
		SessionID: "s", ToolName: "edit_file", Path: "main.go",
		Diff: "diff-1", Added: 3, Removed: 1,
	})
	got = res.(*Model)
	res, _ = got.Update(SessionFileChangeMsg{
		SessionID: "s", ToolName: "edit_file", Path: "main.go",
		Diff: "diff-2", Added: 2, Removed: 5,
	})
	_ = res.(*Model)

	if len(slot.activities) != 2 {
		t.Fatalf("expected 2 activities, got %+v", slot.activities)
	}
	if slot.activities[0].label != "edit: main.go +3 −1" {
		t.Errorf("first activity = %q, want %q", slot.activities[0].label, "edit: main.go +3 −1")
	}
	if slot.activities[1].label != "edit: main.go +2 −5" {
		t.Errorf("second activity = %q, want %q", slot.activities[1].label, "edit: main.go +2 −5")
	}
}

// TestWorkerStreamItemAsOutputItem_CarriesDiffFields verifies the adapter
// used by both the chat card and cockpit/jobs-modal renderers copies the
// diff fields across — easy to miss since it's a separate function from the
// appenders that set them.
func TestWorkerStreamItemAsOutputItem_CarriesDiffFields(t *testing.T) {
	src := &service.WorkerStreamItem{
		Kind:          service.WorkerStreamItemTool,
		ToolName:      "write_file",
		FileDiff:      "@@ -1,1 +1,1 @@\n-a\n+b",
		DiffAdded:     3,
		DiffRemoved:   1,
		DiffCreated:   true,
		DiffTruncated: true,
	}
	got := workerStreamItemAsOutputItem(src)
	if got.fileDiff != src.FileDiff {
		t.Errorf("fileDiff = %q, want %q", got.fileDiff, src.FileDiff)
	}
	if got.diffAdded != src.DiffAdded || got.diffRemoved != src.DiffRemoved {
		t.Errorf("diffAdded/diffRemoved = %d/%d, want %d/%d", got.diffAdded, got.diffRemoved, src.DiffAdded, src.DiffRemoved)
	}
	if got.diffCreated != src.DiffCreated || got.diffTruncated != src.DiffTruncated {
		t.Errorf("diffCreated/diffTruncated = %v/%v, want %v/%v", got.diffCreated, got.diffTruncated, src.DiffCreated, src.DiffTruncated)
	}
}

// TestWorkerStreamItemAsOutputItem_CarriesShellExecFields mirrors
// TestWorkerStreamItemAsOutputItem_CarriesDiffFields for the shell_exec
// fields.
func TestWorkerStreamItemAsOutputItem_CarriesShellExecFields(t *testing.T) {
	src := &service.WorkerStreamItem{
		Kind:             service.WorkerStreamItemTool,
		ToolName:         "shell",
		HasShellExec:     true,
		ShellExitCode:    2,
		ShellDurationMs:  1500,
		ShellOutputBytes: 4096,
		ShellTruncated:   true,
		ShellTimedOut:    false,
	}
	got := workerStreamItemAsOutputItem(src)
	if got.hasShellExec != src.HasShellExec {
		t.Errorf("hasShellExec = %v, want %v", got.hasShellExec, src.HasShellExec)
	}
	if got.shellExitCode != src.ShellExitCode {
		t.Errorf("shellExitCode = %d, want %d", got.shellExitCode, src.ShellExitCode)
	}
	if got.shellDurationMs != src.ShellDurationMs || got.shellOutputBytes != src.ShellOutputBytes {
		t.Errorf("shellDurationMs/shellOutputBytes = %d/%d, want %d/%d",
			got.shellDurationMs, got.shellOutputBytes, src.ShellDurationMs, src.ShellOutputBytes)
	}
	if got.shellTruncated != src.ShellTruncated || got.shellTimedOut != src.ShellTimedOut {
		t.Errorf("shellTruncated/shellTimedOut = %v/%v, want %v/%v",
			got.shellTruncated, got.shellTimedOut, src.ShellTruncated, src.ShellTimedOut)
	}
}

// TestWorkerStreamItemAsOutputItem_CarriesWorkerSpawnFields mirrors
// TestWorkerStreamItemAsOutputItem_CarriesShellExecFields for the
// worker_spawn fields.
func TestWorkerStreamItemAsOutputItem_CarriesWorkerSpawnFields(t *testing.T) {
	src := &service.WorkerStreamItem{
		Kind:           service.WorkerStreamItemTool,
		ToolName:       "spawn_worker",
		HasWorkerSpawn: true,
		SpawnRole:      "coder",
		SpawnTask:      "implement the thing",
		SpawnJobID:     "job-1",
		SpawnDepth:     2,
		SpawnFailed:    true,
		SpawnError:     "role not found",
	}
	got := workerStreamItemAsOutputItem(src)
	if got.hasWorkerSpawn != src.HasWorkerSpawn {
		t.Errorf("hasWorkerSpawn = %v, want %v", got.hasWorkerSpawn, src.HasWorkerSpawn)
	}
	if got.spawnRole != src.SpawnRole || got.spawnTask != src.SpawnTask {
		t.Errorf("spawnRole/spawnTask = %q/%q, want %q/%q", got.spawnRole, got.spawnTask, src.SpawnRole, src.SpawnTask)
	}
	if got.spawnJobID != src.SpawnJobID || got.spawnDepth != src.SpawnDepth {
		t.Errorf("spawnJobID/spawnDepth = %q/%d, want %q/%d", got.spawnJobID, got.spawnDepth, src.SpawnJobID, src.SpawnDepth)
	}
	if got.spawnFailed != src.SpawnFailed || got.spawnError != src.SpawnError {
		t.Errorf("spawnFailed/spawnError = %v/%q, want %v/%q", got.spawnFailed, got.spawnError, src.SpawnFailed, src.SpawnError)
	}
}

// TestSessionWorkerSpawnMsg_AttachesToSlotAndWorkerStream verifies
// Model.Update wires a SessionWorkerSpawnMsg into both the runtime slot
// (jobs pane) and the worker-stream (chat card) copies, mirroring the shell
// and file-change message handlers.
func TestSessionWorkerSpawnMsg_AttachesToSlotAndWorkerStream(t *testing.T) {
	m := newMinimalModel(t)
	slot := &runtimeSlot{sessionID: "s", workerName: "graph:implement", jobID: "j"}
	m.runtimeSessions["s"] = slot

	args, _ := json.Marshal(map[string]string{"role": "coder"})
	res, _ := m.Update(SessionToolCallMsg{SessionID: "s", ToolID: "call1", ToolName: "spawn_worker", ToolInput: string(args)})
	got := res.(*Model)

	res, _ = got.Update(SessionWorkerSpawnMsg{
		SessionID: "s", Role: "coder", Task: "implement the thing", JobID: "j-1", Depth: 1,
	})
	got = res.(*Model)

	if len(slot.items) != 1 || !slot.items[0].hasWorkerSpawn {
		t.Fatalf("expected the runtime slot's tool item to carry the spawn metadata, got %+v", slot.items)
	}
	if slot.items[0].spawnRole != "coder" {
		t.Errorf("spawnRole = %q, want %q", slot.items[0].spawnRole, "coder")
	}

	card := got.findWorkerStream("s")
	if card == nil || len(card.Items) != 1 || !card.Items[0].HasWorkerSpawn {
		t.Fatalf("expected the worker-stream card's item to carry the spawn metadata, card=%+v", card)
	}
}

// TestSessionShellExecMsg_PatchesActivityOnlyOnAnomaly verifies Model.Update
// only appends a "· exit N"/"· timeout" suffix to the matching shell
// activity label for a non-clean outcome — a successful command (exit 0)
// leaves the label untouched, unlike a file-change diff stat which always
// annotates.
func TestSessionShellExecMsg_PatchesActivityOnlyOnAnomaly(t *testing.T) {
	m := newMinimalModel(t)
	slot := &runtimeSlot{sessionID: "s", workerName: "graph:implement", jobID: "j"}
	m.runtimeSessions["s"] = slot

	args, _ := json.Marshal(map[string]string{"command": "echo ok"})
	res, _ := m.Update(SessionToolCallMsg{SessionID: "s", ToolID: "call1", ToolName: "shell", ToolInput: string(args)})
	got := res.(*Model)

	res, _ = got.Update(SessionShellExecMsg{SessionID: "s", ExitCode: 0})
	got = res.(*Model)

	if len(slot.activities) != 1 || strings.Contains(slot.activities[0].label, "exit") {
		t.Fatalf("clean exit should not patch the label, got %+v", slot.activities)
	}

	args2, _ := json.Marshal(map[string]string{"command": "false"})
	res, _ = got.Update(SessionToolCallMsg{SessionID: "s", ToolID: "call2", ToolName: "shell", ToolInput: string(args2)})
	got = res.(*Model)
	res, _ = got.Update(SessionShellExecMsg{SessionID: "s", ExitCode: 1})
	_ = res.(*Model)

	if len(slot.activities) != 2 || !strings.HasSuffix(slot.activities[1].label, "· exit 1") {
		t.Fatalf("failed exit should patch the label with exit code, got %+v", slot.activities)
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

// TestWorkerStreamBlock_HeaderLayout pins the card header shape: the status
// glyph + elapsed sits at the end of line 1 (after the node name, space-
// separated — no dot before it), and line 2 is the short job id prefixing
// the job title.
func TestWorkerStreamBlock_HeaderLayout(t *testing.T) {
	m := newMinimalModel(t)
	m.jobs = []service.Job{{ID: "0123456789abcdef", Title: "Fix Docker Build"}}
	m.progress.tasks = map[string][]service.Task{
		"0123456789abcdef": {{ID: "t1", Title: "Replace go-sqlite3"}},
	}

	snap := &service.WorkerStreamSnapshot{
		SessionID:  "graph:t1:review#1",
		WorkerName: "graph:review#1",
		JobID:      "0123456789abcdef",
		TaskID:     "t1",
		StartedAt:  time.Now().Add(-time.Minute),
		Done:       false,
	}
	out := xansi.Strip(m.renderWorkerStreamBlock(snap, 80, false))
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("card rendered %d lines, want >= 2:\n%s", len(lines), out)
	}

	// Line 1: headline · node, then the glyph + elapsed at the end, no dot
	// between the node name and the status.
	l1 := strings.TrimRight(lines[0], " ")
	if !strings.Contains(l1, "Replace go-sqlite3") || !strings.Contains(l1, "review#1") {
		t.Errorf("line 1 = %q, want headline and node name", l1)
	}
	if !regexp.MustCompile(`● 1m0s$`).MatchString(l1) {
		t.Errorf("line 1 = %q, want it to end with the status glyph + elapsed", l1)
	}
	if strings.Contains(l1, "· ● ") || strings.Contains(l1, "· ● ") {
		t.Errorf("line 1 = %q, status must not be preceded by a dot", l1)
	}

	// Line 2: short job id prefixes the job title.
	if !strings.Contains(lines[1], "01234567 · Fix Docker Build") {
		t.Errorf("line 2 = %q, want short job id prefixing the title", lines[1])
	}
	if strings.Contains(lines[1], "●") || strings.Contains(lines[1], "1m0s") {
		t.Errorf("line 2 = %q, status/elapsed must not be on line 2 anymore", lines[1])
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
