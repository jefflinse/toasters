package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/jefflinse/toasters/internal/service"
)

// TestOperatorDoneMsg_LiveTokensResetOnDone verifies that CompletionTokensLive
// is zeroed when an operator turn completes, so the context bar only reflects
// in-progress tokens during active streaming.
func TestOperatorDoneMsg_LiveTokensResetOnDone(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	// Simulate mid-stream live token estimates.
	m.stats.CompletionTokensLive = 250
	m.stream.streaming = true

	result, _ := m.Update(OperatorDoneMsg{})
	model := result.(*Model)

	if model.stats.CompletionTokensLive != 0 {
		t.Errorf("CompletionTokensLive = %d, want 0 (should be reset after operator done)", model.stats.CompletionTokensLive)
	}
}

// TestOperatorDoneMsg_CompletionTokensAccumulated verifies that CompletionTokens
// accumulates the live estimate from each turn.
func TestOperatorDoneMsg_CompletionTokensAccumulated(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	// Turn 1: 100 completion tokens reported by the operator.
	m.stream.streaming = true
	result1, _ := m.Update(OperatorDoneMsg{TokensOut: 100})
	model1 := result1.(*Model)
	if model1.stats.CompletionTokens != 100 {
		t.Errorf("after turn 1: CompletionTokens = %d, want 100", model1.stats.CompletionTokens)
	}

	// Turn 2: 150 more completion tokens.
	model1.stream.streaming = true
	result2, _ := model1.Update(OperatorDoneMsg{TokensOut: 150})
	model2 := result2.(*Model)

	// CompletionTokens should be cumulative: 100 + 150 = 250.
	if model2.stats.CompletionTokens != 250 {
		t.Errorf("after turn 2: CompletionTokens = %d, want 250 (cumulative)", model2.stats.CompletionTokens)
	}
}

// TestOperatorMemberContextOccupancy pins what actually feeds the operator's
// context bar (now on the input-box border): the provider-reported
// PromptTokens, verbatim.
func TestOperatorMemberContextOccupancy(t *testing.T) {
	t.Parallel()

	m := Model{runtimeSessions: map[string]*runtimeSlot{}}
	m.stats.ModelName = "gemma"
	m.stats.PromptTokens = 1234
	m.stats.ContextLength = 8192

	op := m.operatorMember()
	if op.ctxUsed != 1234 || op.ctxMax != 8192 {
		t.Errorf("operator ctx = %d/%d, want 1234/8192 (PromptTokens/ContextLength verbatim)",
			op.ctxUsed, op.ctxMax)
	}
}

// TestRenderMiniContextBar_ThresholdTick verifies the compaction-threshold
// tick renders at the right cell and only when it's meaningful.
func TestRenderMiniContextBar_ThresholdTick(t *testing.T) {
	t.Parallel()

	t.Run("tick at threshold position", func(t *testing.T) {
		t.Parallel()
		// width 24, label " 25%" (4 cells) → barW 20; threshold 0.5 → cell 10.
		got := stripANSI(renderMiniContextBar(250, 1000, 24, false, 0.5))
		runes := []rune(got)
		tickAt := -1
		for i, r := range runes {
			if r == '│' {
				tickAt = i
				break
			}
		}
		if tickAt != 10 {
			t.Errorf("tick at cell %d, want 10 (bar %q)", tickAt, got)
		}
	})

	t.Run("tick survives fill passing it", func(t *testing.T) {
		t.Parallel()
		got := stripANSI(renderMiniContextBar(800, 1000, 24, false, 0.5))
		if !strings.ContainsRune(got, '│') {
			t.Errorf("tick missing once fill passed threshold: %q", got)
		}
	})

	t.Run("no tick without threshold", func(t *testing.T) {
		t.Parallel()
		got := stripANSI(renderMiniContextBar(250, 1000, 24, false, 0))
		if strings.ContainsRune(got, '│') {
			t.Errorf("unexpected tick with threshold 0: %q", got)
		}
	})

	t.Run("no tick when total unknown", func(t *testing.T) {
		t.Parallel()
		got := stripANSI(renderMiniContextBar(1500, 0, 24, false, 0.5))
		if strings.ContainsRune(got, '│') {
			t.Errorf("unexpected tick with unknown total: %q", got)
		}
	})

	t.Run("threshold at bar edge clamps inside", func(t *testing.T) {
		t.Parallel()
		// threshold 1.0 makes tickIdx == barW, forcing the clamp to the last
		// cell. Not reachable via config (which caps at 90%), but the
		// function must tolerate the full [0,1] range defensively.
		got := stripANSI(renderMiniContextBar(100, 1000, 8, false, 1.0))
		if !strings.ContainsRune(got, '│') {
			t.Errorf("tick missing when threshold clamps to last cell: %q", got)
		}
	})

	t.Run("fill reaching exactly the tick cell keeps the tick", func(t *testing.T) {
		t.Parallel()
		// barW 20, threshold 0.5 → tickIdx 10; used 500/1000 → filled 10.
		// The tick cell is the first unfilled cell — it must render as a
		// tick, with exactly 10 fill cells before it.
		got := stripANSI(renderMiniContextBar(500, 1000, 24, false, 0.5))
		runes := []rune(got)
		if len(runes) < 12 || runes[10] != '│' {
			t.Fatalf("tick not at cell 10 when fill meets threshold: %q", got)
		}
		for i := range 10 {
			if runes[i] != '█' {
				t.Errorf("cell %d = %q, want fill before the tick (%q)", i, runes[i], got)
				break
			}
		}
	})
}

// TestContextBarFillColor pins the threshold-relative coloring semantics:
// crossing the threshold means "compaction pending" (yellow), 15 points past
// means "compaction overdue" (red); with no threshold the legacy fixed
// breakpoints apply.
func TestContextBarFillColor(t *testing.T) {
	t.Parallel()

	green, yellow, red, dimGreen := "#52c41a", "#faad14", "#f5222d", "#3f6b3f"
	tests := []struct {
		name      string
		pct       float64
		threshold float64
		dim       bool
		want      string
	}{
		{"below threshold is green", 0.49, 0.5, false, green},
		{"at threshold is yellow", 0.5, 0.5, false, yellow},
		{"legacy yellow zone stays green below threshold", 0.65, 0.7, false, green},
		{"threshold+15 is red", 0.65, 0.5, false, red},
		{"high threshold red zone clamps to 100%", 1.0, 0.9, false, red},
		{"just under threshold+15 is yellow", 0.64, 0.5, false, yellow},
		{"no threshold uses legacy 60%", 0.6, 0, false, yellow},
		{"no threshold uses legacy 85%", 0.85, 0, false, red},
		{"no threshold low is green", 0.3, 0, false, green},
		{"dim wins over everything", 0.99, 0.5, true, dimGreen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := contextBarFillColor(tt.pct, tt.threshold, tt.dim); got != lipgloss.Color(tt.want) {
				t.Errorf("contextBarFillColor(%v, %v, %v) = %v, want %v", tt.pct, tt.threshold, tt.dim, got, tt.want)
			}
		})
	}
}

// TestApplySettings_CompactionThresholds verifies the settings round-trip
// into the model fields that position the bar ticks.
func TestApplySettings_CompactionThresholds(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.applySettings(service.Settings{
		OperatorCompactionThreshold: 40,
		WorkerCompactionThreshold:   80,
	})
	if m.opCompactionThreshold != 40 || m.workerCompactionThreshold != 80 {
		t.Errorf("thresholds = %d/%d, want 40/80", m.opCompactionThreshold, m.workerCompactionThreshold)
	}

	// 0 means disabled — must pass through, not fall back to defaults.
	m.applySettings(service.Settings{})
	if m.opCompactionThreshold != 0 || m.workerCompactionThreshold != 0 {
		t.Errorf("disabled thresholds = %d/%d, want 0/0", m.opCompactionThreshold, m.workerCompactionThreshold)
	}
}

// TestHandleOperatorCompaction verifies the compaction trace: count
// increments, the activity line describes the drop, and the bar's occupancy
// falls to the estimate immediately.
func TestHandleOperatorCompaction(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.stats.ContextLength = 10000
	m.stats.PromptTokens = 5200

	res, _ := m.Update(OperatorCompactionMsg{
		BeforeTokens:         5200,
		EstimatedAfterTokens: 1800,
		ArchiveFile:          "operator-2026-07-02T12-00-00Z.json",
	})
	got := res.(*Model)

	if got.opCompactionCount != 1 {
		t.Errorf("opCompactionCount = %d, want 1", got.opCompactionCount)
	}
	if got.opLastCompaction != "compacted 52% → ~18%" {
		t.Errorf("opLastCompaction = %q, want %q", got.opLastCompaction, "compacted 52% → ~18%")
	}
	if got.stats.PromptTokens != 1800 {
		t.Errorf("PromptTokens = %d, want 1800 (bar drops immediately)", got.stats.PromptTokens)
	}

	// Unknown window falls back to raw token counts.
	m2 := newMinimalModel(t)
	res2, _ := m2.Update(OperatorCompactionMsg{BeforeTokens: 5200, EstimatedAfterTokens: 1800})
	got2 := res2.(*Model)
	if got2.opLastCompaction != "compacted 5.2k → ~1.8k" {
		t.Errorf("opLastCompaction (no window) = %q, want token counts", got2.opLastCompaction)
	}
}

// TestOperatorMemberCompactionTrace verifies the operator member carries the
// compaction activity line and count, and that the border strip shows the ↺
// badge.
func TestOperatorMemberCompactionTrace(t *testing.T) {
	t.Parallel()

	m := Model{runtimeSessions: map[string]*runtimeSlot{}}
	m.stats.ModelName = "gemma"
	m.opCompactionCount = 2
	m.opLastCompaction = "compacted 52% → ~18%"

	op := m.operatorMember()
	if op.label != "operator" {
		t.Fatalf("operatorMember = %+v, want operator", op)
	}
	if op.compactions != 2 {
		t.Errorf("compactions = %d, want 2", op.compactions)
	}
	if op.activity != "compacted 52% → ~18%" {
		t.Errorf("activity = %q, want the compaction trace", op.activity)
	}

	// The border strip shows the ↺ badge (given room).
	label := stripANSI(m.renderOperatorBorderLabel(80))
	if !strings.Contains(label, "↺2") {
		t.Errorf("border label missing ↺2:\n%s", label)
	}
}

// TestHandleSessionCompaction verifies a worker compaction lands on its slot:
// count bumps, activity line explains the drop, occupancy falls immediately,
// and buildFleet surfaces the ↺ badge on the worker row.
func TestHandleSessionCompaction(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.runtimeSessions = map[string]*runtimeSlot{
		"s1": {sessionID: "s1", workerName: "graph:plan", status: "active",
			model: "gemma", contextTokens: 8200, ctxWindow: 10000},
	}

	res, _ := m.Update(SessionCompactionMsg{
		SessionID: "s1", Tier: 1, BeforeTokens: 8200, EstimatedAfterTokens: 3000,
	})
	got := res.(*Model)
	slot := got.runtimeSessions["s1"]
	if slot.compactions != 1 {
		t.Errorf("compactions = %d, want 1", slot.compactions)
	}
	if slot.contextTokens != 3000 {
		t.Errorf("contextTokens = %d, want 3000 (bar drops immediately)", slot.contextTokens)
	}
	if n := len(slot.activities); n == 0 || slot.activities[n-1].label != "compacted 82% → ~30%" {
		t.Errorf("activity = %v, want compaction trace", slot.activities)
	}

	fleet := got.buildFleet()
	if len(fleet) < 1 {
		t.Fatalf("fleet = %d members, want the worker", len(fleet))
	}
	if fleet[0].compactions != 1 {
		t.Errorf("worker fleet compactions = %d, want 1", fleet[0].compactions)
	}

	// An unknown session must not panic.
	if res, _ := got.Update(SessionCompactionMsg{SessionID: "nope"}); res == nil {
		t.Error("unknown session should be a no-op, not nil model")
	}
}
