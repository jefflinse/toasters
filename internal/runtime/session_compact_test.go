package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jefflinse/toasters/internal/contextwindow"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/provider"
)

// fixedWindow is a ContextWindowSource returning one value for everything.
type fixedWindow int

func (w fixedWindow) Window(_, _ string) int { return int(w) }

// toolTurnResponse scripts one assistant turn that calls the "dig" tool and
// reports the given occupancy.
func toolTurnResponse(callID string, inputTokens int) mockResponse {
	return mockResponse{events: []provider.StreamEvent{
		{Type: provider.EventText, Text: "digging"},
		{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
			ID: callID, Name: "dig", Arguments: []byte(`{}`),
		}},
		{Type: provider.EventUsage, Usage: &provider.Usage{InputTokens: inputTokens, OutputTokens: 10}},
		{Type: provider.EventDone},
	}}
}

// finalResponse ends the session with plain text.
func finalResponse(text string) mockResponse {
	return mockResponse{events: []provider.StreamEvent{
		{Type: provider.EventText, Text: text},
		{Type: provider.EventUsage, Usage: &provider.Usage{InputTokens: 100, OutputTokens: 5}},
		{Type: provider.EventDone},
	}}
}

// newCompactionSession builds a session wired for compaction: given window
// and threshold, a "dig" tool returning a large result, and event capture.
func newCompactionSession(t *testing.T, mp *mockProvider, window, threshold int) (*Session, <-chan SessionEvent) {
	t.Helper()
	tools := &mockToolExecutor{
		results: map[string]string{"dig": strings.Repeat("x", 3000)},
	}
	sess := newSession("sess-compact", mp, SpawnOpts{
		WorkerID:       "w1",
		Model:          "test-model",
		SystemPrompt:   "You are a test worker.",
		InitialMessage: "dig until done",
		MaxTurns:       20,
	}, tools)
	sess.providerName = "test"
	if window > 0 {
		sess.ctxWindows = fixedWindow(window)
	}
	var th atomic.Int32
	th.Store(int32(threshold))
	sess.compactionThreshold = &th
	return sess, sess.Subscribe()
}

// drainCompactions collects compaction events from a subscription.
func drainCompactions(events <-chan SessionEvent) []CompactionEvent {
	var out []CompactionEvent
	for ev := range events {
		if ev.Type == SessionEventCompaction && ev.Compaction != nil {
			out = append(out, *ev.Compaction)
		}
	}
	return out
}

func TestPreflightCompaction_ElidesAgedToolResults(t *testing.T) {
	// Six tool turns; the sixth reports 8000/10000 (over the 70% threshold),
	// so the pre-flight check before turn 7 must compact. With keepTurns=4,
	// the first two turns' tool results get elided — the usage numbers are
	// scaled consistently with the ~3KB tool results (bytes/4), so elision
	// alone frees enough and tier 1 suffices.
	mp := &mockProvider{name: "test", responses: []mockResponse{
		toolTurnResponse("c1", 1000),
		toolTurnResponse("c2", 2200),
		toolTurnResponse("c3", 3400),
		toolTurnResponse("c4", 4600),
		toolTurnResponse("c5", 5800),
		toolTurnResponse("c6", 8000),
		finalResponse("done"),
	}}
	sess, events := newCompactionSession(t, mp, 10000, 70)

	if err := sess.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	compactions := drainCompactions(events)
	if len(compactions) != 1 {
		t.Fatalf("compactions = %d, want 1", len(compactions))
	}
	if compactions[0].Tier != 1 {
		t.Errorf("Tier = %d, want 1 (elision alone frees plenty)", compactions[0].Tier)
	}
	if compactions[0].BeforeTokens != 8000 {
		t.Errorf("BeforeTokens = %d, want 8000", compactions[0].BeforeTokens)
	}

	// The 7th request (post-compaction) must carry elided stubs for old
	// tool results and intact recent ones.
	reqs := mp.getRequests()
	if len(reqs) != 7 {
		t.Fatalf("requests = %d, want 7", len(reqs))
	}
	final := reqs[6].Messages
	var stubs, intact int
	for _, msg := range final {
		if msg.Role != "tool" {
			continue
		}
		if strings.HasPrefix(msg.Content, elidedStubPrefix) {
			stubs++
			if !strings.Contains(msg.Content, "dig") {
				t.Errorf("stub does not name the tool: %q", msg.Content)
			}
		} else {
			intact++
		}
	}
	if stubs == 0 {
		t.Error("no tool results were elided")
	}
	if intact < compactKeepTurns {
		t.Errorf("intact recent tool results = %d, want >= %d", intact, compactKeepTurns)
	}
	// The task message survives verbatim at position 0.
	if final[0].Role != "user" || final[0].Content != "dig until done" {
		t.Errorf("first message = %+v, want the original task", final[0])
	}
	// Structure preserved: every tool message still follows its assistant.
	assertToolPairing(t, final)
}

func TestPreflightCompaction_DisabledCases(t *testing.T) {
	cases := []struct {
		name      string
		window    int
		threshold int
	}{
		{"threshold zero", 1000, 0},
		{"window unknown", 0, 70},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mp := &mockProvider{name: "test", responses: []mockResponse{
				toolTurnResponse("c1", 900),
				finalResponse("done"),
			}}
			sess, events := newCompactionSession(t, mp, tc.window, tc.threshold)
			if err := sess.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if n := len(drainCompactions(events)); n != 0 {
				t.Errorf("compactions = %d, want 0", n)
			}
		})
	}
}

func TestOverflowBackstop_CompactsAndRetriesOnce(t *testing.T) {
	overflow := mockResponse{events: []provider.StreamEvent{
		{Type: provider.EventError, Error: &provider.APIError{
			Provider: "test", StatusCode: 400,
			Body: `{"error":{"code":"context_length_exceeded"}}`,
		}},
	}}
	summaryResponse := mockResponse{events: []provider.StreamEvent{
		{Type: provider.EventText, Text: "progress summary"},
		{Type: provider.EventDone},
	}}
	// Build history first (two tool turns), then the third request
	// overflows. With only two turns nothing is elidable, so the backstop
	// escalates to tier 2 (one summary call), then retries and succeeds.
	// Threshold disabled: the backstop must work on its own.
	mp := &mockProvider{name: "test", responses: []mockResponse{
		toolTurnResponse("c1", 100),
		toolTurnResponse("c2", 200),
		overflow,
		summaryResponse,
		finalResponse("recovered"),
	}}
	sess, events := newCompactionSession(t, mp, 0, 0)
	// MaxTurns exactly matches the real turns consumed (2 tool turns + the
	// final turn) — the overflow retry must re-use its turn's budget slot,
	// not burn an extra one.
	sess.maxTurns = 3

	if err := sess.Run(context.Background()); err != nil {
		t.Fatalf("Run after overflow retry: %v", err)
	}
	if got := sess.Snapshot().Status; got != "completed" {
		t.Errorf("status = %q, want completed", got)
	}
	compactions := drainCompactions(events)
	if len(compactions) != 1 {
		t.Fatalf("compactions = %d, want 1 (the backstop)", len(compactions))
	}
	// With nothing elidable (2 turns, all protected), the backstop must
	// escalate to tier 2.
	if compactions[0].Tier != 2 {
		t.Errorf("Tier = %d, want 2 on the backstop path", compactions[0].Tier)
	}
	// 5 provider calls: 2 turns + overflow + tier-2 summary + retry.
	reqs := mp.getRequests()
	if len(reqs) != 5 {
		t.Fatalf("requests = %d, want 5", len(reqs))
	}
	// The retried request must carry the COMPACTED history: the summary
	// message (which can only exist post-compaction) and the original task.
	retry := reqs[4].Messages
	if retry[0].Content != "dig until done" {
		t.Errorf("retry first message = %q, want the original task", retry[0].Content)
	}
	foundSummary := false
	for _, msg := range retry {
		if strings.Contains(msg.Content, "progress summary") {
			foundSummary = true
		}
	}
	if !foundSummary {
		t.Error("retried request does not contain the compaction summary — the retry did not use the compacted history")
	}
	assertToolPairing(t, retry)
}

func TestOverflowBackstop_SummaryFailureDegradesToTaskAndTail(t *testing.T) {
	overflow := mockResponse{events: []provider.StreamEvent{
		{Type: provider.EventError, Error: &provider.APIError{
			Provider: "test", StatusCode: 400, Body: "prompt is too long",
		}},
	}}
	summaryFails := mockResponse{err: fmt.Errorf("provider exploded")}
	mp := &mockProvider{name: "test", responses: []mockResponse{
		toolTurnResponse("c1", 100),
		toolTurnResponse("c2", 200),
		overflow,
		summaryFails, // tier-2 one-shot fails → degrade to task + tail
		finalResponse("recovered"),
	}}
	sess, events := newCompactionSession(t, mp, 0, 0)

	if err := sess.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := len(drainCompactions(events)); n != 1 {
		t.Fatalf("compactions = %d, want 1", n)
	}
	reqs := mp.getRequests()
	retry := reqs[len(reqs)-1].Messages
	if retry[0].Content != "dig until done" {
		t.Errorf("retry first message = %q, want the original task", retry[0].Content)
	}
	for _, msg := range retry {
		if strings.Contains(msg.Content, "[Compaction summary") {
			t.Error("retry contains a summary message despite the summary call failing")
		}
	}
	assertToolPairing(t, retry)
}

func TestOverflowBackstop_SecondOverflowFails(t *testing.T) {
	overflow := mockResponse{events: []provider.StreamEvent{
		{Type: provider.EventError, Error: &provider.APIError{
			Provider: "test", StatusCode: 400, Body: "prompt is too long",
		}},
	}}
	summaryResponse := mockResponse{events: []provider.StreamEvent{
		{Type: provider.EventText, Text: "progress summary"},
		{Type: provider.EventDone},
	}}
	// Turn 1, then overflow → backstop (tier-2 summary succeeds) → the
	// retry overflows AGAIN → terminal failure with the overflow shape.
	mp := &mockProvider{name: "test", responses: []mockResponse{
		toolTurnResponse("c1", 100),
		overflow,
		summaryResponse,
		overflow,
	}}
	sess, _ := newCompactionSession(t, mp, 0, 0)

	err := sess.Run(context.Background())
	if err == nil {
		t.Fatal("want failure after second overflow, got nil")
	}
	if got := sess.Snapshot().Status; got != "failed" {
		t.Errorf("status = %q, want failed", got)
	}
	if !provider.IsContextOverflow(err) {
		t.Errorf("terminal error should preserve the overflow shape: %v", err)
	}
}

func TestTier2_SummarizeAndContinue(t *testing.T) {
	// Assistant turns carry huge text so tier-1 elision can't get under a
	// tiny budget — tier 2 must summarize. The summarize one-shot consumes
	// a mock response between the trigger turn and the retried turn.
	bigText := strings.Repeat("y", 4000)
	bigTurn := func(callID string, inputTokens int) mockResponse {
		return mockResponse{events: []provider.StreamEvent{
			{Type: provider.EventText, Text: bigText},
			{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
				ID: callID, Name: "dig", Arguments: []byte(`{}`),
			}},
			{Type: provider.EventUsage, Usage: &provider.Usage{InputTokens: inputTokens, OutputTokens: 10}},
			{Type: provider.EventDone},
		}}
	}
	summaryResponse := mockResponse{events: []provider.StreamEvent{
		{Type: provider.EventText, Text: "Dug three holes; two remain."},
		{Type: provider.EventDone},
	}}
	mp := &mockProvider{name: "test", responses: []mockResponse{
		bigTurn("c1", 1000),
		bigTurn("c2", 2000),
		bigTurn("c3", 3000),
		bigTurn("c4", 4000),
		bigTurn("c5", 5000),
		bigTurn("c6", 9000), // over 70% of 10000
		summaryResponse,     // consumed by the tier-2 one-shot
		finalResponse("done"),
	}}
	sess, events := newCompactionSession(t, mp, 10000, 70)

	// Real store so the superseded flag is observable.
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer store.Close() //nolint:errcheck
	sess.store = store

	if err := sess.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	compactions := drainCompactions(events)
	if len(compactions) != 1 {
		t.Fatalf("compactions = %d, want 1", len(compactions))
	}
	if compactions[0].Tier != 2 {
		t.Fatalf("Tier = %d, want 2", compactions[0].Tier)
	}

	// The post-compaction request: task first, then the summary, then the
	// recent tail at a pair-safe boundary.
	reqs := mp.getRequests()
	final := reqs[len(reqs)-1].Messages
	if final[0].Content != "dig until done" {
		t.Errorf("first message = %q, want the original task", final[0].Content)
	}
	if !strings.Contains(final[1].Content, "Dug three holes") {
		t.Errorf("second message = %q, want the compaction summary", final[1].Content)
	}
	assertToolPairing(t, final)

	// The summarize call itself must be a bounded, toolless one-shot.
	summaryReq := reqs[6]
	if summaryReq.MaxTokens != summaryMaxTokens || len(summaryReq.Tools) != 0 {
		t.Errorf("summary request = MaxTokens %d Tools %d, want %d/0",
			summaryReq.MaxTokens, len(summaryReq.Tools), summaryMaxTokens)
	}

	// Every pre-compaction row is flagged superseded, and the live
	// (non-superseded) set mirrors exactly what the model sees: the marker,
	// then the re-persisted task + summary + tail, then post-compaction
	// turns.
	rows, err := store.ListSessionMessages(context.Background(), sess.id)
	if err != nil {
		t.Fatalf("ListSessionMessages: %v", err)
	}
	var live []*db.SessionMessage
	sawMarker := false
	supersededCount := 0
	for _, r := range rows {
		if r.Superseded {
			supersededCount++
			continue
		}
		live = append(live, r)
		if strings.HasPrefix(r.Content, "[compacted (tier 2)") {
			sawMarker = true
		}
	}
	if supersededCount == 0 {
		t.Error("no transcript rows flagged superseded after tier-2 compaction")
	}
	if !sawMarker {
		t.Error("compaction marker row missing or flagged superseded")
	}
	// The live set must contain the re-persisted task and summary — the
	// exact conversation the model still sees — with no superseded flag.
	var liveTask, liveSummary bool
	for _, r := range live {
		if r.Content == "dig until done" {
			liveTask = true
		}
		if strings.Contains(r.Content, "Dug three holes") {
			liveSummary = true
		}
	}
	if !liveTask {
		t.Error("live transcript rows missing the re-persisted task message")
	}
	if !liveSummary {
		t.Error("live transcript rows missing the re-persisted summary")
	}
	// And the pre-compaction original of the task is flagged: the same
	// content exists on both sides of the boundary.
	var supersededTask bool
	for _, r := range rows {
		if r.Superseded && r.Content == "dig until done" {
			supersededTask = true
		}
	}
	if !supersededTask {
		t.Error("original task row not flagged superseded (live set would be ambiguous)")
	}
}

func TestCompactionSuppressed_WhenNothingHelps(t *testing.T) {
	// A tiny budget that even the compacted history exceeds: compaction
	// runs once (tier 2), then suppresses instead of thrashing.
	bigText := strings.Repeat("z", 8000)
	bigTurn := func(callID string, inputTokens int) mockResponse {
		return mockResponse{events: []provider.StreamEvent{
			{Type: provider.EventText, Text: bigText},
			{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
				ID: callID, Name: "dig", Arguments: []byte(`{}`),
			}},
			{Type: provider.EventUsage, Usage: &provider.Usage{InputTokens: inputTokens, OutputTokens: 10}},
			{Type: provider.EventDone},
		}}
	}
	summaryResponse := mockResponse{events: []provider.StreamEvent{
		{Type: provider.EventText, Text: "summary"},
		{Type: provider.EventDone},
	}}
	mp := &mockProvider{name: "test", responses: []mockResponse{
		bigTurn("c1", 900), // over 70% of 1000 immediately
		summaryResponse,    // tier-2 one-shot for the single compaction
		bigTurn("c2", 900), // still over threshold — must NOT re-compact
		finalResponse("done"),
	}}
	sess, events := newCompactionSession(t, mp, 1000, 70)

	if err := sess.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := len(drainCompactions(events)); n != 1 {
		t.Errorf("compactions = %d, want 1 (suppressed after the floor guard)", n)
	}
}

// assertToolPairing fails if any tool message doesn't immediately follow its
// assistant tool-call group.
func assertToolPairing(t *testing.T, msgs []provider.Message) {
	t.Helper()
	pending := map[string]bool{}
	for i, m := range msgs {
		switch m.Role {
		case "assistant":
			pending = map[string]bool{}
			for _, tc := range m.ToolCalls {
				pending[tc.ID] = true
			}
		case "tool":
			if !pending[m.ToolCallID] {
				t.Fatalf("message %d: orphaned tool result (call %s):\n%s", i, m.ToolCallID, dumpRoles(msgs))
			}
		default:
			pending = map[string]bool{}
		}
	}
}

func dumpRoles(msgs []provider.Message) string {
	var b strings.Builder
	for i, m := range msgs {
		fmt.Fprintf(&b, "%d: %s (%d tool calls, callID %q)\n", i, m.Role, len(m.ToolCalls), m.ToolCallID)
	}
	return b.String()
}

func TestElideToolResults_Unit(t *testing.T) {
	t.Parallel()

	msgs := []provider.Message{
		{Role: "user", Content: "task"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read_file"}}},
		{Role: "tool", ToolCallID: "c1", Content: "old contents"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c2", Name: "spawn_worker"}}},
		{Role: "tool", ToolCallID: "c2", Content: "child synthesis"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c3", Name: "shell"}}},
		{Role: "tool", ToolCallID: "c3", Content: "recent output"},
		{Role: "assistant", Content: "thinking"},
	}
	// keepTurns=2 protects from the 2nd-from-last assistant turn onward.
	out, n := elideToolResults(msgs, 2)

	if n != 1 {
		t.Fatalf("elided = %d, want 1 (read_file only)", n)
	}
	if !strings.HasPrefix(out[2].Content, elidedStubPrefix) || !strings.Contains(out[2].Content, "read_file") {
		t.Errorf("read_file result = %q, want elided stub naming the tool", out[2].Content)
	}
	if out[4].Content != "child synthesis" {
		t.Errorf("spawn_worker result = %q, want untouched", out[4].Content)
	}
	if out[6].Content != "recent output" {
		t.Errorf("protected-tail result = %q, want untouched", out[6].Content)
	}
	// Idempotent: a second pass elides nothing new.
	_, n2 := elideToolResults(out, 2)
	if n2 != 0 {
		t.Errorf("second elision pass = %d, want 0", n2)
	}
	// The input is never mutated.
	if msgs[2].Content != "old contents" {
		t.Error("elideToolResults mutated its input")
	}
	assertToolPairing(t, out)
}

func TestRebuildHistory_ShortHistoryNoDuplication(t *testing.T) {
	t.Parallel()

	msgs := []provider.Message{
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "reply"},
	}
	out := rebuildHistory(msgs, "the summary", 4)
	if len(out) != 3 {
		t.Fatalf("rebuilt = %d messages, want 3 (task, summary, reply)", len(out))
	}
	if out[0].Content != "task" || !strings.Contains(out[1].Content, "the summary") || out[2].Content != "reply" {
		t.Errorf("rebuilt history wrong: %+v", out)
	}
}

func TestSetCompactionThreshold_LiveApply(t *testing.T) {
	t.Parallel()

	rt := New(nil, provider.NewRegistry())
	rt.SetCompactionThreshold(70)

	var th *atomic.Int32 = &rt.compactionThreshold
	if got := int(th.Load()); got != 70 {
		t.Fatalf("threshold = %d, want 70", got)
	}
	rt.SetCompactionThreshold(0)
	if got := int(th.Load()); got != 0 {
		t.Errorf("threshold after disable = %d, want 0 — sessions holding the pointer must see it", got)
	}
	_ = contextwindow.EstimateTokens(nil) // keep the shared-helper import honest
}
