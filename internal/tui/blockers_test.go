package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/service"
)

func TestBlockerSourceLabel(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                  "operator",
		"graph:investigate": "node investigate",
		"weird":             "weird",
	}
	for in, want := range cases {
		if got := blockerSourceLabel(in); got != want {
			t.Errorf("blockerSourceLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBlockerLabel_ResolvesJobContext(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.jobs = []service.Job{{ID: "job-1", Title: "To-Do web app"}}

	if got := m.blockerLabel(service.Blocker{Source: "graph:plan", JobID: "job-1"}); got != "node plan · To-Do web app" {
		t.Errorf("blockerLabel (known job) = %q", got)
	}
	if got := m.blockerLabel(service.Blocker{Source: "graph:plan", JobID: "missing"}); got != "node plan" {
		t.Errorf("blockerLabel (unknown job) = %q, want 'node plan'", got)
	}
	if got := m.blockerLabel(service.Blocker{}); got != "operator" {
		t.Errorf("blockerLabel (operator) = %q, want 'operator'", got)
	}
}

func TestBlockerAddedMsg_QueuesRecordsAndToasts(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	beforeEntries := len(m.chat.entries)

	b := service.Blocker{
		RequestID: "req-1",
		Source:    "graph:investigate",
		Questions: []service.PromptQuestion{{Question: "Which path?"}},
		CreatedAt: time.Unix(1, 0),
	}
	res, _ := m.Update(BlockerAddedMsg{Blocker: b})
	got := res.(*Model)

	if len(got.blockers) != 1 || got.blockers[0].RequestID != "req-1" {
		t.Fatalf("blockers = %v, want one req-1", got.blockers)
	}
	if len(got.chat.entries) != beforeEntries+1 {
		t.Errorf("chat entries = %d, want %d (blocker recorded)", len(got.chat.entries), beforeEntries+1)
	}
	if len(got.toasts) != 1 {
		t.Errorf("toasts = %d, want 1", len(got.toasts))
	}
	// It must NOT enter prompt mode — that's the whole point.
	if got.prompt.promptMode {
		t.Error("BlockerAddedMsg should not enter prompt mode")
	}

	// A duplicate (same RequestID) is ignored.
	res2, _ := got.Update(BlockerAddedMsg{Blocker: b})
	if len(res2.(*Model).blockers) != 1 {
		t.Errorf("duplicate blocker should be ignored; got %d", len(res2.(*Model).blockers))
	}
}

func TestBlockerResolvedMsg_Removes(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.blockers = []service.Blocker{
		{RequestID: "a"},
		{RequestID: "b"},
	}
	m.blockersSel = 1

	res, _ := m.Update(BlockerResolvedMsg{RequestID: "a"})
	got := res.(*Model)
	if len(got.blockers) != 1 || got.blockers[0].RequestID != "b" {
		t.Fatalf("blockers = %v, want [b]", got.blockers)
	}
	if got.blockersSel != 0 {
		t.Errorf("blockersSel = %d, want clamped to 0", got.blockersSel)
	}
}

func TestBlockerResolvedMsg_ExitsPromptIfAnswering(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.blockers = []service.Blocker{{RequestID: "a"}}
	m.prompt = promptModeState{promptMode: true, requestID: "a", fromBlocker: true}

	res, _ := m.Update(BlockerResolvedMsg{RequestID: "a"})
	got := res.(*Model)
	if got.prompt.promptMode {
		t.Error("prompt mode should exit when the answered blocker is resolved elsewhere")
	}
}

func TestUpdateBlockersModal_EnterOpensWizard(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.blockers = []service.Blocker{
		{RequestID: "a", Source: "graph:plan", Questions: []service.PromptQuestion{{Question: "Q1"}}},
	}
	m.blockersModal = blockersModalState{show: true, sel: 0}

	res, _ := m.updateBlockersModal(specialKey(tea.KeyEnter))
	got := res.(*Model)
	if got.blockersModal.show {
		t.Error("modal should close after selecting a blocker")
	}
	if !got.prompt.promptMode || !got.prompt.fromBlocker {
		t.Errorf("expected prompt mode opened fromBlocker; got promptMode=%v fromBlocker=%v", got.prompt.promptMode, got.prompt.fromBlocker)
	}
	if got.prompt.requestID != "a" {
		t.Errorf("requestID = %q, want a", got.prompt.requestID)
	}
}

func TestUpdateBlockersModal_EscCloses(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.blockers = []service.Blocker{{RequestID: "a"}}
	m.blockersModal = blockersModalState{show: true, sel: 0}

	res, _ := m.updateBlockersModal(specialKey(tea.KeyEscape))
	got := res.(*Model)
	if got.blockersModal.show {
		t.Error("Esc should close the modal")
	}
	if got.prompt.promptMode {
		t.Error("Esc should not open the wizard")
	}
}

func TestCancelPrompt_FromBlockerLeavesPending(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.blockers = []service.Blocker{{RequestID: "a"}}
	m.prompt = promptModeState{promptMode: true, requestID: "a", fromBlocker: true}

	cmd := m.cancelPrompt()
	if cmd != nil {
		t.Error("cancelPrompt fromBlocker should not call RespondToPrompt (nil cmd)")
	}
	if m.prompt.promptMode {
		t.Error("cancelPrompt should exit prompt mode")
	}
	if len(m.blockers) != 1 {
		t.Errorf("blocker should remain pending; got %d", len(m.blockers))
	}
}

func TestRenderPromptModal_RendersWizard(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	m.openBlocker(service.Blocker{
		RequestID: "a",
		Source:    "graph:plan",
		Questions: []service.PromptQuestion{{Question: "Which path?", Options: []string{"left", "right"}}},
	})

	out := m.renderPromptModal()
	if out == "" {
		t.Fatal("renderPromptModal returned empty")
	}
	for _, want := range []string{"Which path?", "left", "right", "plan asks"} {
		if !strings.Contains(out, want) {
			t.Errorf("modal output missing %q", want)
		}
	}
	// The textarea width must be restored so the normal input is unaffected.
	if got := m.input.Width(); got <= 1 {
		t.Errorf("input width = %d, expected restored to its prior value", got)
	}
}

func TestRenderBlockersModal_TwoPanel(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.width = 140
	m.height = 40
	m.blockers = []service.Blocker{
		{
			RequestID: "r1",
			Questions: []service.PromptQuestion{{Question: "What features should the system support?"}},
			CreatedAt: time.Now().Add(-2 * time.Minute),
		},
		{
			RequestID: "r2",
			Source:    "graph:plan",
			Questions: []service.PromptQuestion{
				{Question: "Which path?", Options: []string{"left", "right"}},
				{Question: "How deep?"},
			},
			CreatedAt: time.Now().Add(-30 * time.Second),
		},
	}
	m.blockersModal = blockersModalState{show: true, sel: 1}

	out := m.renderBlockersModal()
	for _, want := range []string{
		"2 waiting",                // count in the queue header
		"node plan",                // selected blocker's attribution header
		"Questions (2)",            // multi-question section header
		"Which path?", "How deep?", // both questions in the detail panel
		"○ left", "○ right", // options as bullets
		"[Enter] Answer", "[x] Dismiss", // footer hints
	} {
		if !strings.Contains(out, want) {
			t.Errorf("modal output missing %q", want)
		}
	}
}

func TestRenderBlockersModal_Empty(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	m.blockersModal = blockersModalState{show: true}

	out := m.renderBlockersModal()
	if !strings.Contains(out, "No pending blockers") {
		t.Error("empty modal should render the no-blockers state")
	}
}

func TestBuildBlockersLines_CountAndMultiQuestion(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.blockers = []service.Blocker{
		{
			RequestID: "r1",
			Questions: []service.PromptQuestion{{Question: "First?"}, {Question: "Second?"}, {Question: "Third?"}},
			CreatedAt: time.Now().Add(-time.Minute),
		},
	}

	joined := strings.Join(m.buildBlockersLines(60), "\n")
	if !strings.Contains(joined, "1 waiting") {
		t.Errorf("pane title missing pending count, got:\n%s", joined)
	}
	if !strings.Contains(joined, "First?") {
		t.Errorf("pane missing first question, got:\n%s", joined)
	}
	if !strings.Contains(joined, "(+2 more)") {
		t.Errorf("pane missing extra-question marker, got:\n%s", joined)
	}
}

func TestCompactAge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ago  time.Duration
		want string
	}{
		{5 * time.Second, "5s"},
		{90 * time.Second, "1m"},
		{45 * time.Minute, "45m"},
		{3 * time.Hour, "3h"},
		{50 * time.Hour, "2d"},
	}
	for _, tt := range tests {
		if got := compactAge(time.Now().Add(-tt.ago)); got != tt.want {
			t.Errorf("compactAge(-%v) = %q, want %q", tt.ago, got, tt.want)
		}
	}
}

func TestRenderSidebar_BlockerCounts(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, 1, 3} {
		m := newMinimalModel(t)
		m.width = 140
		m.blockers = make([]service.Blocker, n)
		for i := range m.blockers {
			m.blockers[i] = service.Blocker{RequestID: string(rune('a' + i)), Questions: []service.PromptQuestion{{Question: "Q"}}}
		}
		// Must not panic and must produce output for a reasonable panel size.
		out := m.renderSidebar(40, 30)
		if out == "" {
			t.Errorf("n=%d: renderSidebar returned empty", n)
		}
		_, _, blockersH := m.sidebarPaneHeights(40, 30)
		if blockersH < 1 {
			t.Errorf("n=%d: blockers pane height = %d, want >= 1", n, blockersH)
		}
	}
}

func TestRenderBlockersModal_ResolvedHistory(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.width = 140
	m.height = 40
	m.blockers = []service.Blocker{
		{RequestID: "p1", Questions: []service.PromptQuestion{{Question: "Pending Q?"}}, CreatedAt: time.Now()},
	}
	m.blockersModal = blockersModalState{
		show: true,
		sel:  1, // first history row (after the one pending row)
		history: []service.BlockerRecord{
			{
				Blocker: service.Blocker{
					RequestID: "r1",
					Source:    "graph:plan",
					Questions: []service.PromptQuestion{{Question: "Old question?"}},
					CreatedAt: time.Now().Add(-time.Hour),
				},
				ResolvedAt:  time.Now().Add(-30 * time.Minute),
				Disposition: service.BlockerDispositionAnswered,
				Answer:      "It was option two.",
			},
		},
	}

	out := m.renderBlockersModal()
	for _, want := range []string{
		"Resolved",           // history section header
		"Old question?",      // resolved blocker's question in detail
		"answered",           // disposition in detail
		"It was option two.", // the recorded answer
	} {
		if !strings.Contains(out, want) {
			t.Errorf("modal output missing %q", want)
		}
	}
	// A resolved row is browse-only: no Answer/Dismiss hints in the footer.
	if strings.Contains(out, "[Enter] Answer") {
		t.Error("footer should not offer Enter/answer on a resolved row")
	}
}

func TestUpdateBlockersModal_CursorSpansHistory(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.blockers = []service.Blocker{{RequestID: "p1", Questions: []service.PromptQuestion{{Question: "Q"}}}}
	m.blockersModal = blockersModalState{
		show:    true,
		sel:     0,
		history: []service.BlockerRecord{{Blocker: service.Blocker{RequestID: "r1"}}},
	}

	// Down moves onto the history row; Enter there must NOT open the wizard.
	res, _ := m.updateBlockersModal(specialKey(tea.KeyDown))
	got := res.(*Model)
	if got.blockersModal.sel != 1 {
		t.Fatalf("sel = %d, want 1 (history row)", got.blockersModal.sel)
	}
	res, _ = got.updateBlockersModal(specialKey(tea.KeyEnter))
	got = res.(*Model)
	if got.prompt.promptMode {
		t.Error("Enter on a resolved row must not open the answer wizard")
	}
	if !got.blockersModal.show {
		t.Error("modal should stay open after Enter on a resolved row")
	}
	// Down at the end of the combined list stays clamped.
	res, _ = got.updateBlockersModal(specialKey(tea.KeyDown))
	if res.(*Model).blockersModal.sel != 1 {
		t.Errorf("sel = %d, want clamped at 1", res.(*Model).blockersModal.sel)
	}
}
