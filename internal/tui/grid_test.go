package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/gateway"
)

func TestCommaInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input int
		want  string
	}{
		{
			name:  "zero",
			input: 0,
			want:  "0",
		},
		{
			name:  "single digit",
			input: 5,
			want:  "5",
		},
		{
			name:  "two digits",
			input: 42,
			want:  "42",
		},
		{
			name:  "three digits",
			input: 999,
			want:  "999",
		},
		{
			name:  "four digits",
			input: 1234,
			want:  "1,234",
		},
		{
			name:  "thousands",
			input: 12345,
			want:  "12,345",
		},
		{
			name:  "hundred thousands",
			input: 200000,
			want:  "200,000",
		},
		{
			name:  "millions",
			input: 1234567,
			want:  "1,234,567",
		},
		{
			name:  "billions",
			input: 1234567890,
			want:  "1,234,567,890",
		},
		{
			name:  "negative single digit",
			input: -5,
			want:  "-5",
		},
		{
			name:  "negative thousands",
			input: -1234,
			want:  "-1,234",
		},
		{
			name:  "negative millions",
			input: -1234567,
			want:  "-1,234,567",
		},
		{
			name:  "exact thousand",
			input: 1000,
			want:  "1,000",
		},
		{
			name:  "exact million",
			input: 1000000,
			want:  "1,000,000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := commaInt(tt.input)
			if got != tt.want {
				t.Errorf("commaInt(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRenderContextBar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		used         int
		systemTokens int
		total        int
		width        int
		streaming    bool
		spinnerFrame int
		check        func(t *testing.T, result string)
	}{
		{
			name: "basic usage",
			used: 5000, systemTokens: 1000, total: 200000, width: 20,
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "5,000") {
					t.Errorf("result should contain '5,000', got %q", result)
				}
				if !strings.Contains(result, "200,000") {
					t.Errorf("result should contain '200,000', got %q", result)
				}
			},
		},
		{
			name: "zero total shows question mark",
			used: 100, systemTokens: 0, total: 0, width: 20,
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "?") {
					t.Errorf("result should contain '?', got %q", result)
				}
			},
		},
		{
			name: "very small width clamped to 4",
			used: 100, systemTokens: 0, total: 200000, width: 1,
			check: func(t *testing.T, result string) {
				// Should not panic.
				if result == "" {
					t.Error("expected non-empty result")
				}
			},
		},
		{
			name: "100 percent usage",
			used: 200000, systemTokens: 1000, total: 200000, width: 20,
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "100%") {
					t.Errorf("result should contain '100%%', got %q", result)
				}
			},
		},
		{
			name: "over 100 percent clamped",
			used: 300000, systemTokens: 1000, total: 200000, width: 20,
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "100%") {
					t.Errorf("result should contain '100%%', got %q", result)
				}
			},
		},
		{
			name: "streaming mode",
			used: 50000, systemTokens: 2000, total: 200000, width: 20,
			streaming: true, spinnerFrame: 3,
			check: func(t *testing.T, result string) {
				if result == "" {
					t.Error("expected non-empty result")
				}
			},
		},
		{
			name: "system tokens shown in detail",
			used: 10000, systemTokens: 3000, total: 200000, width: 20,
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "sys") {
					t.Errorf("result should contain 'sys' detail, got %q", result)
				}
				if !strings.Contains(result, "conv") {
					t.Errorf("result should contain 'conv' detail, got %q", result)
				}
			},
		},
		{
			name: "no system tokens omits detail line",
			used: 5000, systemTokens: 0, total: 200000, width: 20,
			check: func(t *testing.T, result string) {
				if strings.Contains(result, "sys") {
					t.Errorf("result should not contain 'sys' when systemTokens=0, got %q", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := renderContextBar(tt.used, tt.systemTokens, tt.total, tt.width, tt.streaming, tt.spinnerFrame)
			tt.check(t, result)
		})
	}
}

func TestRenderReasoningBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		reasoning    string
		contentWidth int
		check        func(t *testing.T, result string)
	}{
		{
			name:         "basic reasoning",
			reasoning:    "I need to think about this carefully.",
			contentWidth: 60,
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "thinking") {
					t.Errorf("result should contain 'thinking' header, got %q", result)
				}
				if !strings.Contains(result, "I need to think about this carefully.") {
					t.Errorf("result should contain reasoning text, got %q", result)
				}
			},
		},
		{
			name:         "very narrow width",
			reasoning:    "Short thought.",
			contentWidth: 5,
			check: func(t *testing.T, result string) {
				// Should not panic.
				if !strings.Contains(result, "thinking") {
					t.Errorf("result should contain 'thinking' header, got %q", result)
				}
			},
		},
		{
			name:         "multi-line reasoning",
			reasoning:    "First thought.\nSecond thought.\nThird thought.",
			contentWidth: 60,
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "First thought") {
					t.Errorf("result should contain reasoning text, got %q", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := renderReasoningBlock(tt.reasoning, tt.contentWidth)
			tt.check(t, result)
		})
	}
}

func TestMiniTokenBar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		totalTokens int
		check       func(t *testing.T, result string)
	}{
		{
			name:        "zero tokens",
			totalTokens: 0,
			check: func(t *testing.T, result string) {
				if !strings.HasPrefix(result, "[") {
					t.Errorf("expected bar to start with '[', got %q", result)
				}
				if !strings.Contains(result, "0") {
					t.Errorf("expected result to contain '0', got %q", result)
				}
			},
		},
		{
			name:        "small token count",
			totalTokens: 500,
			check: func(t *testing.T, result string) {
				if !strings.HasPrefix(result, "[") {
					t.Errorf("expected bar to start with '[', got %q", result)
				}
				if !strings.Contains(result, "500") {
					t.Errorf("expected result to contain '500', got %q", result)
				}
			},
		},
		{
			name:        "medium token count",
			totalTokens: 50000,
			check: func(t *testing.T, result string) {
				if !strings.HasPrefix(result, "[") {
					t.Errorf("expected bar to start with '[', got %q", result)
				}
				// 50000 should be formatted as "50k" by compactNum.
				if !strings.Contains(result, "50k") {
					t.Errorf("expected result to contain '50k', got %q", result)
				}
			},
		},
		{
			name:        "max tokens",
			totalTokens: 200000,
			check: func(t *testing.T, result string) {
				if !strings.HasPrefix(result, "[") {
					t.Errorf("expected bar to start with '[', got %q", result)
				}
				if !strings.Contains(result, "200k") {
					t.Errorf("expected result to contain '200k', got %q", result)
				}
			},
		},
		{
			name:        "over max tokens clamped",
			totalTokens: 400000,
			check: func(t *testing.T, result string) {
				// Should not panic. Bar should be fully filled.
				if !strings.HasPrefix(result, "[") {
					t.Errorf("expected bar to start with '[', got %q", result)
				}
			},
		},
		{
			name:        "negative tokens",
			totalTokens: -100,
			check: func(t *testing.T, result string) {
				// Should not panic.
				if !strings.HasPrefix(result, "[") {
					t.Errorf("expected bar to start with '[', got %q", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := miniTokenBar(tt.totalTokens)
			tt.check(t, result)
		})
	}
}

// --------------------------------------------------------------------------
// TestSlotPriority
// --------------------------------------------------------------------------

func TestSlotPriority(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		snap gateway.SlotSnapshot
		want int
	}{
		{
			name: "inactive slot returns 2",
			snap: gateway.SlotSnapshot{Active: false},
			want: 2,
		},
		{
			name: "running slot returns 0",
			snap: gateway.SlotSnapshot{Active: true, Status: gateway.SlotRunning},
			want: 0,
		},
		{
			name: "done slot returns 1",
			snap: gateway.SlotSnapshot{Active: true, Status: gateway.SlotDone},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := slotPriority(tt.snap)
			if got != tt.want {
				t.Errorf("slotPriority(%+v) = %d, want %d", tt.snap, got, tt.want)
			}
		})
	}
}

// --------------------------------------------------------------------------
// TestSortedSlotIndicesFrom
// --------------------------------------------------------------------------

func TestSortedSlotIndicesFrom(t *testing.T) {
	t.Parallel()

	// Build a snapshot array with a mix of running, done, and inactive slots.
	// Slots 0,3 = inactive; slot 1 = done; slot 2 = running.
	// Expected order: [2 (running), 1 (done), 0 (inactive), 3 (inactive), ...]
	var slots [gateway.MaxSlots]gateway.SlotSnapshot
	slots[0] = gateway.SlotSnapshot{Active: false}
	slots[1] = gateway.SlotSnapshot{Active: true, Status: gateway.SlotDone}
	slots[2] = gateway.SlotSnapshot{Active: true, Status: gateway.SlotRunning}
	slots[3] = gateway.SlotSnapshot{Active: false}
	// All other slots default to Active=false (inactive).

	indices := sortedSlotIndicesFrom(slots)

	if len(indices) != gateway.MaxSlots {
		t.Fatalf("expected %d indices, got %d", gateway.MaxSlots, len(indices))
	}

	// First index must be the running slot (2).
	if indices[0] != 2 {
		t.Errorf("indices[0] = %d, want 2 (running slot)", indices[0])
	}
	// Second index must be the done slot (1).
	if indices[1] != 1 {
		t.Errorf("indices[1] = %d, want 1 (done slot)", indices[1])
	}
	// Remaining indices must all be inactive slots (priority 2).
	for i := 2; i < len(indices); i++ {
		if slots[indices[i]].Active {
			t.Errorf("indices[%d] = %d is active, expected inactive", i, indices[i])
		}
	}
}

// --------------------------------------------------------------------------
// TestRuntimeSessionForGridCell
// --------------------------------------------------------------------------

func TestRuntimeSessionForGridCell(t *testing.T) {
	t.Parallel()

	// Helper: build a snapshot array where the first n slots are active (running)
	// and the rest are inactive.
	makeSlots := func(activeCount int) [gateway.MaxSlots]gateway.SlotSnapshot {
		var s [gateway.MaxSlots]gateway.SlotSnapshot
		for i := range activeCount {
			s[i] = gateway.SlotSnapshot{Active: true, Status: gateway.SlotRunning}
		}
		return s
	}

	// Helper: build a sorted runtime session list with the given IDs.
	// Sessions are given distinct start times so their order is deterministic.
	makeRT := func(ids ...string) map[string]*runtimeSlot {
		m := make(map[string]*runtimeSlot, len(ids))
		base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		for i, id := range ids {
			m[id] = &runtimeSlot{
				sessionID: id,
				status:    "active",
				startTime: base.Add(time.Duration(i) * time.Second),
			}
		}
		return m
	}

	tests := []struct {
		name          string
		gridPage      int
		slots         [gateway.MaxSlots]gateway.SlotSnapshot
		runtimeSess   map[string]*runtimeSlot
		cellIdx       int
		wantSessionID string // empty string means expect nil
	}{
		{
			name:          "page 0, no runtime sessions, returns nil",
			gridPage:      0,
			slots:         makeSlots(0),
			runtimeSess:   map[string]*runtimeSlot{},
			cellIdx:       0,
			wantSessionID: "",
		},
		{
			name:          "page 0, 2 inactive gateway slots, 3 runtime sessions — cell 0 maps to rt[0]",
			gridPage:      0,
			slots:         makeSlots(0), // all 4 visible cells are inactive
			runtimeSess:   makeRT("rt0", "rt1", "rt2"),
			cellIdx:       0,
			wantSessionID: "rt0",
		},
		{
			name:          "page 0, 2 inactive gateway slots, 3 runtime sessions — cell 1 maps to rt[1]",
			gridPage:      0,
			slots:         makeSlots(0),
			runtimeSess:   makeRT("rt0", "rt1", "rt2"),
			cellIdx:       1,
			wantSessionID: "rt1",
		},
		{
			name:          "page 0, 2 inactive gateway slots, 3 runtime sessions — cell 2 maps to rt[2]",
			gridPage:      0,
			slots:         makeSlots(0),
			runtimeSess:   makeRT("rt0", "rt1", "rt2"),
			cellIdx:       2,
			wantSessionID: "rt2",
		},
		{
			name:        "page 1, 4 active slots on page 0 — cell 0 on page 1 maps to rt[0]",
			gridPage:    1,
			slots:       makeSlots(4), // first 4 slots are active (sorted to page 0)
			runtimeSess: makeRT("rt0", "rt1"),
			cellIdx:     0,
			// Page 0 has 4 active slots → 0 runtime sessions consumed.
			// Page 1 cell 0 is the first inactive slot → maps to rt[0].
			wantSessionID: "rt0",
		},
		{
			name:        "page 1, 2 inactive slots on page 0 — cell 0 on page 1 maps to rt[2]",
			gridPage:    1,
			slots:       makeSlots(2), // first 2 slots are active (sorted to page 0 positions 0,1)
			runtimeSess: makeRT("rt0", "rt1", "rt2", "rt3"),
			cellIdx:     0,
			// Page 0: positions 0,1 are active; positions 2,3 are inactive → consume rt0, rt1.
			// Page 1 cell 0 is the next inactive slot → maps to rt[2].
			wantSessionID: "rt2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := newMinimalModel(t)
			m.grid.gridPage = tt.gridPage
			m.runtimeSessions = tt.runtimeSess

			sortedIndices := sortedSlotIndicesFrom(tt.slots)
			rs := m.runtimeSessionForGridCell(tt.cellIdx, tt.slots, sortedIndices)

			if tt.wantSessionID == "" {
				if rs != nil {
					t.Errorf("expected nil, got session %q", rs.sessionID)
				}
			} else {
				if rs == nil {
					t.Fatalf("expected session %q, got nil", tt.wantSessionID)
				}
				if rs.sessionID != tt.wantSessionID {
					t.Errorf("sessionID = %q, want %q", rs.sessionID, tt.wantSessionID)
				}
			}
		})
	}
}
