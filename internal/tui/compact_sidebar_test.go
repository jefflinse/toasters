package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// TestSpliceTopBorderLabel verifies the top border row is replaced by a rule
// that embeds the label while preserving the box's total width and its other
// rows.
func TestSpliceTopBorderLabel(t *testing.T) {
	t.Parallel()

	const w = 30
	box := InputAreaStyle.Width(w).Render("hello")
	label := SidebarValueStyle.Render("⬡ gemma")
	out := spliceTopBorderLabel(box, w, label, lipgloss.Color("30"))

	inLines := strings.Split(box, "\n")
	outLines := strings.Split(out, "\n")
	if len(outLines) != len(inLines) {
		t.Fatalf("line count changed: %d → %d", len(inLines), len(outLines))
	}
	top := stripANSI(outLines[0])
	if lipgloss.Width(outLines[0]) != w {
		t.Errorf("top row width = %d, want %d (%q)", lipgloss.Width(outLines[0]), w, top)
	}
	if !strings.HasPrefix(top, "┌─ ⬡ gemma ") || !strings.HasSuffix(top, "┐") {
		t.Errorf("top row = %q, want embedded label between corners", top)
	}
	// Body rows must be untouched.
	for i := 1; i < len(inLines); i++ {
		if inLines[i] != outLines[i] {
			t.Errorf("row %d changed: %q → %q", i, inLines[i], outLines[i])
		}
	}
}

// TestSpliceTopBorderLabel_TooNarrow leaves the box unchanged when the label
// can't fit, rather than overflowing the line.
func TestSpliceTopBorderLabel_TooNarrow(t *testing.T) {
	t.Parallel()

	const w = 12
	box := InputAreaStyle.Width(w).Render("x")
	// Label wider than the width budget (w-5 = 7).
	label := strings.Repeat("Z", 20)
	out := spliceTopBorderLabel(box, w, label, lipgloss.Color("30"))
	if out != box {
		t.Errorf("expected unchanged box when label overflows")
	}
}

// TestRenderOperatorBorderLabel_FitsAndPrioritizes checks the strip stays
// within budget and drops the least-essential stats (cost, then tokens) first
// while model + context% survive.
func TestRenderOperatorBorderLabel_FitsAndPrioritizes(t *testing.T) {
	t.Parallel()

	m := Model{runtimeSessions: map[string]*runtimeSlot{}}
	m.stats.ModelName = "gemma-3-27b"
	m.stats.PromptTokens = 3400
	m.stats.ContextLength = 10000 // 34%
	m.stats.CompletionTokens = 1200
	m.stats.TotalResponses = 1
	m.stats.TotalResponseTime = 15 * time.Second // ~80 t/s

	// Roomy: everything fits, in display order.
	full := stripANSI(m.renderOperatorBorderLabel(80))
	for _, want := range []string{"gemma-3-27b", "34%", "t/s", "↓"} {
		if !strings.Contains(full, want) {
			t.Errorf("wide label missing %q: %q", want, full)
		}
	}
	if lipgloss.Width(m.renderOperatorBorderLabel(80)) > 80 {
		t.Errorf("label exceeds budget 80: %q", full)
	}

	// Tight: context% must survive even as trailing stats fall away.
	tight := stripANSI(m.renderOperatorBorderLabel(20))
	if lipgloss.Width(m.renderOperatorBorderLabel(20)) > 20 {
		t.Errorf("tight label exceeds budget 20: %q", tight)
	}
	if !strings.Contains(tight, "34%") {
		t.Errorf("tight label dropped context%%: %q", tight)
	}
}

// TestRenderOperatorBorderLabel_NoModel falls back to a bare operator label
// before the first response lands.
func TestRenderOperatorBorderLabel_NoModel(t *testing.T) {
	t.Parallel()

	m := Model{runtimeSessions: map[string]*runtimeSlot{}}
	got := stripANSI(m.renderOperatorBorderLabel(40))
	if !strings.Contains(got, "operator") {
		t.Errorf("label = %q, want fallback 'operator'", got)
	}
}

// TestRenderFleetMemberLine renders a worker on one line with occupancy and
// throughput folded in, and never exceeds the content width.
func TestRenderFleetMemberLine(t *testing.T) {
	t.Parallel()

	m := Model{}
	mem := fleetMember{
		label:   "abcdef12:plan",
		icon:    "⚡",
		active:  true,
		ctxUsed: 4000,
		ctxMax:  8000, // 50%
		tps:     120,
		hasTPS:  true,
	}
	line := m.renderFleetMemberLine(mem, 40)
	if strings.Contains(line, "\n") {
		t.Errorf("fleet line has a newline: %q", line)
	}
	if lipgloss.Width(line) > 40 {
		t.Errorf("fleet line width = %d, want <= 40", lipgloss.Width(line))
	}
	plain := stripANSI(line)
	for _, want := range []string{"plan", "50%", "120 t/s"} {
		if !strings.Contains(plain, want) {
			t.Errorf("fleet line missing %q: %q", want, plain)
		}
	}
}

// TestRenderJobLine renders a job on one line with glyph, title, and status,
// and shows the selection marker when selected.
func TestRenderJobLine(t *testing.T) {
	t.Parallel()

	m := Model{}
	snap := &service.JobSnapshot{
		JobID:  "abcdef1234",
		Title:  "Build the thing",
		Status: service.JobStatusCompleted,
	}
	line := stripANSI(m.renderJobLine(snap, 40, false))
	if strings.Contains(line, "\n") {
		t.Errorf("job line has a newline: %q", line)
	}
	if lipgloss.Width(m.renderJobLine(snap, 40, false)) > 40 {
		t.Errorf("job line width = %d, want <= 40", lipgloss.Width(m.renderJobLine(snap, 40, false)))
	}
	if !strings.Contains(line, "Build the thing") || !strings.Contains(line, "done") {
		t.Errorf("job line = %q, want title + status word", line)
	}
	sel := stripANSI(m.renderJobLine(snap, 40, true))
	if !strings.HasPrefix(sel, "▶ ") {
		t.Errorf("selected job line = %q, want ▶ marker", sel)
	}
}

// TestBuildBlockersLines_Compact collapses each blocker to a single line
// (attribution only, question folded away) when compact mode is on.
func TestBuildBlockersLines_Compact(t *testing.T) {
	t.Parallel()

	m := Model{compactSidebar: true}
	m.blockers = []service.Blocker{
		{
			RequestID: "a",
			Source:    "graph:plan",
			Questions: []service.PromptQuestion{{Question: "Which path should we take here?"}},
			CreatedAt: time.Now().Add(-90 * time.Second),
		},
	}
	lines := m.buildBlockersLines(30)
	// Title (with count) + exactly one blocker row; no question line.
	for _, l := range lines {
		if strings.Contains(l, "Which path") {
			t.Errorf("compact blocker leaked the question line: %q", l)
		}
	}
	var rows int
	for _, l := range lines {
		if strings.Contains(stripANSI(l), "⛔") {
			rows++
		}
	}
	if rows != 1 {
		t.Errorf("compact blocker rows = %d, want 1", rows)
	}
}
