package tui

import (
	"strings"
	"testing"
)

func TestLeftPanelWidth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		termWidth int
		want      int
	}{
		{
			name:      "wide terminal",
			termWidth: 200,
			want:      50, // 200/4 = 50
		},
		{
			name:      "medium terminal",
			termWidth: 120,
			want:      30, // 120/4 = 30
		},
		{
			name:      "narrow terminal clamps to minimum",
			termWidth: 40,
			want:      minLeftPanelWidth, // 40/4 = 10 < 22
		},
		{
			name:      "very narrow terminal clamps to minimum",
			termWidth: 10,
			want:      minLeftPanelWidth, // 10/4 = 2 < 22
		},
		{
			name:      "zero terminal width clamps to minimum",
			termWidth: 0,
			want:      minLeftPanelWidth, // 0/4 = 0 < 22
		},
		{
			name:      "terminal width exactly at 4x minimum",
			termWidth: minLeftPanelWidth * 4,
			want:      minLeftPanelWidth, // 88/4 = 22 == minLeftPanelWidth
		},
		{
			name:      "terminal width just above 4x minimum",
			termWidth: minLeftPanelWidth*4 + 4,
			want:      minLeftPanelWidth + 1, // (88+4)/4 = 23
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := leftPanelWidth(tt.termWidth)
			if got != tt.want {
				t.Errorf("leftPanelWidth(%d) = %d, want %d", tt.termWidth, got, tt.want)
			}
		})
	}
}

func TestSidebarWidth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		termWidth int
		want      int
	}{
		{
			name:      "wide terminal",
			termWidth: 240,
			want:      40, // 240/6 = 40
		},
		{
			name:      "medium terminal",
			termWidth: 180,
			want:      30, // 180/6 = 30
		},
		{
			name:      "narrow terminal clamps to minimum",
			termWidth: 60,
			want:      minLeftPanelWidth, // 60/6 = 10 < 22
		},
		{
			name:      "very narrow terminal clamps to minimum",
			termWidth: 10,
			want:      minLeftPanelWidth, // 10/6 = 1 < 22
		},
		{
			name:      "zero terminal width clamps to minimum",
			termWidth: 0,
			want:      minLeftPanelWidth, // 0/6 = 0 < 22
		},
		{
			name:      "terminal width exactly at 6x minimum",
			termWidth: minLeftPanelWidth * 6,
			want:      minLeftPanelWidth, // 132/6 = 22 == minLeftPanelWidth
		},
		{
			name:      "terminal width just above 6x minimum",
			termWidth: minLeftPanelWidth*6 + 6,
			want:      minLeftPanelWidth + 1, // (132+6)/6 = 23
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sidebarWidth(tt.termWidth)
			if got != tt.want {
				t.Errorf("sidebarWidth(%d) = %d, want %d", tt.termWidth, got, tt.want)
			}
		})
	}
}

func TestRenderMiniContextBar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		used, tot  int
		width      int
		wantSub    string // substring that must appear
		wantNoZero bool   // must not render "100%" when under budget
	}{
		{name: "half full", used: 500, tot: 1000, width: 12, wantSub: "50%"},
		{name: "over budget clamps to 100", used: 3000, tot: 1000, width: 12, wantSub: "100%"},
		{name: "unknown total shows token count", used: 1500, tot: 0, width: 12, wantSub: "1.5k"},
		{name: "empty shows dash", used: 0, tot: 0, width: 12, wantSub: "—"},
		// No live occupancy but a known window (graph-node worker) must read as
		// unknown, not a misleading 0%.
		{name: "no usage with known window shows dash", used: 0, tot: 200000, width: 12, wantSub: "—"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renderMiniContextBar(tt.used, tt.tot, tt.width)
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("renderMiniContextBar(%d,%d,%d) = %q, want substring %q",
					tt.used, tt.tot, tt.width, got, tt.wantSub)
			}
		})
	}
}

func TestFleetTotals(t *testing.T) {
	t.Parallel()

	// Idle operator (has a since-start rate but not active), one active worker,
	// one completed worker (also has a rate). Live throughput must count only
	// the active worker; cost must sum across all.
	members := []fleetMember{
		{label: "operator", active: false, hasTPS: true, tps: 500, costUSD: 0},
		{label: "w-active", active: true, hasTPS: true, tps: 120, costUSD: 0.03},
		{label: "w-done", active: false, done: true, hasTPS: true, tps: 40, costUSD: 0.01},
	}
	live, tps, cost := fleetTotals(members)
	if live != 1 {
		t.Errorf("liveCount = %d, want 1", live)
	}
	if tps != 120 {
		t.Errorf("totalTPS = %.0f, want 120 (active worker only, not idle op or done worker)", tps)
	}
	if cost != 0.04 {
		t.Errorf("totalCost = %.2f, want 0.04 (sum of all members)", cost)
	}
}

func TestBuildFleet(t *testing.T) {
	t.Parallel()

	m := Model{
		runtimeSessions: map[string]*runtimeSlot{
			"s1": {
				sessionID:     "s1",
				workerName:    "graph:plan",
				jobID:         "abcdef1234567890",
				status:        "active",
				model:         "gpt-4o-mini",
				tokensOut:     2000,
				contextTokens: 4096,
			},
		},
		modelContext: map[string]int{"gpt-4o-mini": 128000},
	}
	m.stats.ModelName = "claude-opus"
	m.stats.ContextLength = 200000
	m.stats.PromptTokens = 5000

	fleet := m.buildFleet()
	if len(fleet) != 2 {
		t.Fatalf("buildFleet len = %d, want 2 (operator + 1 worker)", len(fleet))
	}
	if fleet[0].label != "operator" || fleet[0].icon != "⬡" {
		t.Errorf("first member = %+v, want operator pinned first", fleet[0])
	}
	if fleet[0].ctxUsed != 5000 || fleet[0].ctxMax != 200000 {
		t.Errorf("operator ctx = %d/%d, want 5000/200000", fleet[0].ctxUsed, fleet[0].ctxMax)
	}
	w := fleet[1]
	if w.label != "abcdef12:plan" {
		t.Errorf("worker label = %q, want %q", w.label, "abcdef12:plan")
	}
	if w.ctxUsed != 4096 || w.ctxMax != 128000 {
		t.Errorf("worker ctx = %d/%d, want 4096/128000 (joined via modelContext)", w.ctxUsed, w.ctxMax)
	}
	if !w.active {
		t.Errorf("worker should be active")
	}
}

func TestEffectiveLeftPanelWidth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		termWidth     int
		widthOverride int
		want          int
	}{
		{
			name:          "no override returns default computed width",
			termWidth:     200,
			widthOverride: 0,
			want:          50, // leftPanelWidth(200) = 200/4 = 50
		},
		{
			name:          "no override with narrow terminal clamps to minimum",
			termWidth:     40,
			widthOverride: 0,
			want:          minLeftPanelWidth, // 40/4 = 10 < 22
		},
		{
			name:          "override respected when within bounds",
			termWidth:     200,
			widthOverride: 40,
			want:          40,
		},
		{
			name:          "override clamped to minimum",
			termWidth:     200,
			widthOverride: 5,
			want:          minLeftPanelWidth,
		},
		{
			name:          "override clamped to half terminal width",
			termWidth:     60,
			widthOverride: 50,
			want:          30, // 60/2 = 30
		},
		{
			name:          "override exactly at minimum",
			termWidth:     200,
			widthOverride: minLeftPanelWidth,
			want:          minLeftPanelWidth,
		},
		{
			name:          "override exactly at max (half terminal)",
			termWidth:     100,
			widthOverride: 50,
			want:          50, // 100/2 = 50
		},
		{
			name:          "override above max clamped",
			termWidth:     100,
			widthOverride: 80,
			want:          50, // 100/2 = 50
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.width = tt.termWidth
			m.leftPanelWidthOverride = tt.widthOverride

			got := m.effectiveLeftPanelWidth()
			if got != tt.want {
				t.Errorf("effectiveLeftPanelWidth() = %d, want %d (termWidth=%d, override=%d)",
					got, tt.want, tt.termWidth, tt.widthOverride)
			}
		})
	}
}

func TestPromptWidgetInner(t *testing.T) {
	t.Parallel()

	t.Run("option selection mode shows numbered options", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.width = 120
		m.height = 40
		m.prompt.promptMode = true
		m.prompt.promptCustom = false
		m.prompt.promptQuestion = "Which team should handle this?"
		m.prompt.promptOptions = []string{"Team Alpha", "Team Beta"}
		m.prompt.promptSelected = 0

		result := m.promptWidgetInner()

		// Should contain the question.
		if !strings.Contains(result, "Which team should handle this?") {
			t.Error("expected prompt widget to contain the question")
		}
		// Should contain numbered options.
		if !strings.Contains(result, "1.") {
			t.Error("expected prompt widget to contain option number 1")
		}
		if !strings.Contains(result, "Team Alpha") {
			t.Error("expected prompt widget to contain 'Team Alpha'")
		}
		if !strings.Contains(result, "2.") {
			t.Error("expected prompt widget to contain option number 2")
		}
		if !strings.Contains(result, "Team Beta") {
			t.Error("expected prompt widget to contain 'Team Beta'")
		}
		// Should contain the "Custom response..." option.
		if !strings.Contains(result, "Custom response...") {
			t.Error("expected prompt widget to contain 'Custom response...'")
		}
		// Should contain navigation hint.
		if !strings.Contains(result, "navigate") {
			t.Error("expected prompt widget to contain navigation hint")
		}
	})

	t.Run("option selection mode with cursor on second option", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.width = 120
		m.height = 40
		m.prompt.promptMode = true
		m.prompt.promptCustom = false
		m.prompt.promptQuestion = "Pick one"
		m.prompt.promptOptions = []string{"Option A", "Option B"}
		m.prompt.promptSelected = 1

		result := m.promptWidgetInner()

		// Both options should be present.
		if !strings.Contains(result, "Option A") {
			t.Error("expected 'Option A' in output")
		}
		if !strings.Contains(result, "Option B") {
			t.Error("expected 'Option B' in output")
		}
	})

	t.Run("custom text mode shows question and submit hint", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.width = 120
		m.height = 40
		m.prompt.promptMode = true
		m.prompt.promptCustom = true
		m.prompt.promptQuestion = "Enter your custom response"

		result := m.promptWidgetInner()

		// Should contain the question.
		if !strings.Contains(result, "Enter your custom response") {
			t.Error("expected prompt widget to contain the question")
		}
		// Should contain submit hint.
		if !strings.Contains(result, "Enter to submit") {
			t.Error("expected prompt widget to contain submit hint")
		}
	})

	t.Run("option selection with no options shows only custom response", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.width = 120
		m.height = 40
		m.prompt.promptMode = true
		m.prompt.promptCustom = false
		m.prompt.promptQuestion = "No options"
		m.prompt.promptOptions = nil
		m.prompt.promptSelected = 0

		result := m.promptWidgetInner()

		// Should still contain "Custom response..." as the only option.
		if !strings.Contains(result, "Custom response...") {
			t.Error("expected 'Custom response...' even with no options")
		}
		if !strings.Contains(result, "1.") {
			t.Error("expected option number 1 for 'Custom response...'")
		}
	})
}
