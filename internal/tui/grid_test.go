package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// --------------------------------------------------------------------------
// TestComputeGridDimensions
// --------------------------------------------------------------------------

func TestComputeGridDimensions(t *testing.T) {
	t.Parallel()

	// minCellW = minGridCellInnerW + gridCellBorderW = 40 + 4 = 44
	// minCellH = minGridCellInnerH + gridCellBorderH = 8  + 2 = 10
	// availH   = termH - gridHotkeyBarH               = termH - 1
	// cols     = termW / minCellW  (floor, min 1)
	// rows     = availH / minCellH (floor, min 1)
	minCellW := minGridCellInnerW + gridCellBorderW // 44
	minCellH := minGridCellInnerH + gridCellBorderH // 10

	tests := []struct {
		name     string
		termW    int
		termH    int
		wantCols int
		wantRows int
	}{
		{
			// 20/44=0 → clamped to 1; (10-1)/10=0 → clamped to 1
			name:     "very small terminal (20x10) yields 1x1",
			termW:    20,
			termH:    10,
			wantCols: 1,
			wantRows: 1,
		},
		{
			// 44/44=1 col; (21-1)/10=2 rows
			name:     "exactly min cell width (44 wide), tall enough for 2 rows",
			termW:    minCellW,
			termH:    2*minCellH + gridHotkeyBarH, // 21
			wantCols: 1,
			wantRows: 2,
		},
		{
			// 88/44=2 cols; (11-1)/10=1 row
			name:     "wide enough for 2 columns (88 wide), tall enough for 1 row",
			termW:    2 * minCellW,
			termH:    minCellH + gridHotkeyBarH, // 11
			wantCols: 2,
			wantRows: 1,
		},
		{
			// 132/44=3 cols; (31-1)/10=3 rows
			name:     "wide enough for 3 columns (132 wide), tall enough for 3 rows",
			termW:    3 * minCellW,
			termH:    3*minCellH + gridHotkeyBarH, // 31
			wantCols: 3,
			wantRows: 3,
		},
		{
			// 220/44=5 cols; (50-1)/10=4 rows
			name:     "large terminal (220x50) yields 5x4",
			termW:    220,
			termH:    50,
			wantCols: 220 / minCellW,                   // 5
			wantRows: (50 - gridHotkeyBarH) / minCellH, // 4
		},
		{
			// 0/44=0 → clamped to 1; (0-1)/10=-1/10=0 → clamped to 1
			name:     "zero width yields 1x1",
			termW:    0,
			termH:    0,
			wantCols: 1,
			wantRows: 1,
		},
		{
			// minCellW/44=1 col; (0-1)/10=-1/10=0 → clamped to 1 — tests the height-zero path
			name:     "zero height yields 1x1",
			termW:    minCellW,
			termH:    0,
			wantCols: 1,
			wantRows: 1,
		},
		{
			// (2*minCellW-1)/44=1 col — one pixel short of fitting 2 columns
			name:     "one pixel short of 2 columns yields 1 col",
			termW:    2*minCellW - 1,
			termH:    minCellH + gridHotkeyBarH,
			wantCols: 1,
			wantRows: 1,
		},
		{
			// 44/44=1 col; (11-1)/10=1 row — exactly one cell fits
			name:     "exactly one cell fits (44x11) yields 1x1",
			termW:    minCellW,
			termH:    minCellH + gridHotkeyBarH, // 11
			wantCols: 1,
			wantRows: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotCols, gotRows := computeGridDimensions(tt.termW, tt.termH)
			if gotCols != tt.wantCols {
				t.Errorf("computeGridDimensions(%d, %d) cols = %d, want %d",
					tt.termW, tt.termH, gotCols, tt.wantCols)
			}
			if gotRows != tt.wantRows {
				t.Errorf("computeGridDimensions(%d, %d) rows = %d, want %d",
					tt.termW, tt.termH, gotRows, tt.wantRows)
			}
		})
	}
}

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
// TestActivityLabel
// --------------------------------------------------------------------------

func TestActivityLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		toolName string
		args     json.RawMessage
		want     string
	}{
		// --- file tools ---
		{
			name:     "write_file extracts basename from path",
			toolName: "write_file",
			args:     json.RawMessage(`{"path": "/some/dir/main.go"}`),
			want:     "write: main.go",
		},
		{
			name:     "edit_file extracts basename from path",
			toolName: "edit_file",
			args:     json.RawMessage(`{"path": "/some/dir/config.yaml"}`),
			want:     "edit: config.yaml",
		},
		{
			name:     "read_file extracts basename from path",
			toolName: "read_file",
			args:     json.RawMessage(`{"path": "/some/dir/README.md"}`),
			want:     "read: README.md",
		},

		// --- shell ---
		{
			name:     "shell short command not truncated",
			toolName: "shell",
			args:     json.RawMessage(`{"command": "go build ./..."}`),
			want:     "shell: go build ./...",
		},
		{
			name:     "shell long command truncated with ellipsis",
			toolName: "shell",
			// 31 runes — over the 28-rune limit; first 28 = "go test -v -race -count=1 ./"
			args: json.RawMessage(`{"command": "go test -v -race -count=1 ./..."}`),
			want: "shell: go test -v -race -count=1 ./…",
		},

		// --- spawn_agent ---
		{
			name:     "spawn_agent with agent_name uses it",
			toolName: "spawn_agent",
			args:     json.RawMessage(`{"agent_name": "builder"}`),
			want:     "spawn: builder",
		},
		{
			name:     "spawn_agent without agent_name falls back to worker",
			toolName: "spawn_agent",
			args:     json.RawMessage(`{}`),
			want:     "spawn: worker",
		},

		// --- report_progress ---
		{
			name:     "report_progress short message not truncated",
			toolName: "report_progress",
			args:     json.RawMessage(`{"message": "done"}`),
			want:     "progress: done",
		},
		{
			name:     "report_progress long message truncated with ellipsis",
			toolName: "report_progress",
			// 38 runes — over the 28-rune limit; first 28 = "finished building all core d"
			args: json.RawMessage(`{"message": "finished building all core data models"}`),
			want: "progress: finished building all core d…",
		},

		// --- web_fetch ---
		{
			name:     "web_fetch extracts host from valid URL",
			toolName: "web_fetch",
			args:     json.RawMessage(`{"url": "https://pkg.go.dev/net/http"}`),
			want:     "fetch: pkg.go.dev",
		},
		{
			name:     "web_fetch with malformed URL falls back gracefully without panic",
			toolName: "web_fetch",
			args:     json.RawMessage(`{"url": "://not-a-valid-url"}`),
			// url.Parse on "://not-a-valid-url" returns an error, so falls back to trunc(u, 28)
			want: "fetch: ://not-a-valid-url",
		},

		// --- glob / grep ---
		{
			name:     "glob returns pattern",
			toolName: "glob",
			args:     json.RawMessage(`{"pattern": "**/*.go"}`),
			want:     "glob: **/*.go",
		},
		{
			name:     "grep returns pattern",
			toolName: "grep",
			args:     json.RawMessage(`{"pattern": "func.*Error"}`),
			want:     "grep: func.*Error",
		},

		// --- task / review / query ---
		{
			name:     "update_task_status returns status value",
			toolName: "update_task_status",
			args:     json.RawMessage(`{"status": "completed"}`),
			want:     "task: completed",
		},
		{
			name:     "request_review returns fixed label",
			toolName: "request_review",
			args:     json.RawMessage(`{}`),
			want:     "review: requested",
		},
		{
			name:     "query_job_context returns fixed label",
			toolName: "query_job_context",
			args:     json.RawMessage(`{}`),
			want:     "query: job context",
		},

		// --- report_blocker ---
		{
			name:     "report_blocker with description",
			toolName: "report_blocker",
			args:     json.RawMessage(`{"description": "dependency missing"}`),
			want:     "blocker: dependency missing",
		},
		{
			name:     "report_blocker with empty args returns sentinel",
			toolName: "report_blocker",
			args:     json.RawMessage(`{}`),
			want:     "blocker: (no description)",
		},

		// --- log_artifact ---
		{
			name:     "log_artifact with name",
			toolName: "log_artifact",
			args:     json.RawMessage(`{"name": "output.json"}`),
			want:     "artifact: output.json",
		},

		// --- MCP-namespaced tools ---
		{
			name:     "MCP-namespaced tool formatted as server: tool_name",
			toolName: "github__create_pull_request",
			args:     json.RawMessage(`{}`),
			want:     "github: create_pull_request",
		},

		// --- unknown tools ---
		{
			name:     "unknown tool name passes through unchanged",
			toolName: "my_custom_tool",
			args:     json.RawMessage(`{}`),
			want:     "my_custom_tool",
		},
		{
			name: "unknown tool with very long name truncated at 35 runes",
			// 36 runes — one over the 35-rune limit
			toolName: "this_is_a_very_long_custom_tool_name_x",
			args:     json.RawMessage(`{}`),
			want:     "this_is_a_very_long_custom_tool_nam…",
		},

		// --- nil / malformed args ---
		{
			name:     "nil args does not panic and returns tool name for unknown tool",
			toolName: "my_tool",
			args:     json.RawMessage(nil),
			want:     "my_tool",
		},
		{
			name:     "malformed JSON args does not panic and returns tool name for unknown tool",
			toolName: "my_tool",
			args:     json.RawMessage("not json"),
			want:     "my_tool",
		},
		{
			name:     "nil args does not panic for known tool",
			toolName: "request_review",
			args:     json.RawMessage(nil),
			want:     "review: requested",
		},
		{
			name:     "malformed JSON args does not panic for known tool",
			toolName: "request_review",
			args:     json.RawMessage("not json"),
			want:     "review: requested",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := activityLabel(tt.toolName, tt.args)
			if got != tt.want {
				t.Errorf("activityLabel(%q, %s) = %q, want %q", tt.toolName, tt.args, got, tt.want)
			}
		})
	}
}

// --------------------------------------------------------------------------
// TestRuntimeSessionForGridCell
// --------------------------------------------------------------------------

func TestRuntimeSessionForGridCell(t *testing.T) {
	t.Parallel()

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
		runtimeSess   map[string]*runtimeSlot
		cellIdx       int
		wantSessionID string // empty string means expect nil
	}{
		{
			name:          "page 0, no runtime sessions, returns nil",
			gridPage:      0,
			runtimeSess:   map[string]*runtimeSlot{},
			cellIdx:       0,
			wantSessionID: "",
		},
		{
			name:          "page 0, 3 runtime sessions — cell 0 maps to rt[0]",
			gridPage:      0,
			runtimeSess:   makeRT("rt0", "rt1", "rt2"),
			cellIdx:       0,
			wantSessionID: "rt0",
		},
		{
			name:          "page 0, 3 runtime sessions — cell 1 maps to rt[1]",
			gridPage:      0,
			runtimeSess:   makeRT("rt0", "rt1", "rt2"),
			cellIdx:       1,
			wantSessionID: "rt1",
		},
		{
			name:          "page 0, 3 runtime sessions — cell 2 maps to rt[2]",
			gridPage:      0,
			runtimeSess:   makeRT("rt0", "rt1", "rt2"),
			cellIdx:       2,
			wantSessionID: "rt2",
		},
		{
			name:        "page 1, 4 sessions — cell 0 on page 1 maps to rt[4]",
			gridPage:    1,
			runtimeSess: makeRT("rt0", "rt1", "rt2", "rt3", "rt4", "rt5"),
			cellIdx:     0,
			// Page 0 has cells 0-3 (4 cells for 2x2 grid) → rt0..rt3 consumed.
			// Page 1 cell 0 → rt4.
			wantSessionID: "rt4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := newMinimalModel(t)
			m.grid.gridPage = tt.gridPage
			m.grid.gridCols = 2
			m.grid.gridRows = 2
			m.runtimeSessions = tt.runtimeSess

			rs := m.runtimeSessionForGridCell(tt.cellIdx)

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
