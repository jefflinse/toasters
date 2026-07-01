package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/tui/dagmap"
)

// --------------------------------------------------------------------------
// TestComputeGridDimensions
// --------------------------------------------------------------------------

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

		// --- spawn_worker ---
		{
			name:     "spawn_worker with role uses it",
			toolName: "spawn_worker",
			args:     json.RawMessage(`{"role": "builder"}`),
			want:     "spawn: builder",
		},
		{
			name:     "spawn_worker without role falls back to worker",
			toolName: "spawn_worker",
			args:     json.RawMessage(`{}`),
			want:     "spawn: worker",
		},

		// --- report_progress / report_task_progress ---
		{
			name:     "report_progress short message not truncated",
			toolName: "report_progress",
			args:     json.RawMessage(`{"message": "done"}`),
			want:     "progress: done",
		},
		{
			name:     "report_task_progress maps to same label as report_progress",
			toolName: "report_task_progress",
			args:     json.RawMessage(`{"message": "halfway there"}`),
			want:     "progress: halfway there",
		},
		{
			name:     "report_task_progress with empty message returns sentinel",
			toolName: "report_task_progress",
			args:     json.RawMessage(`{}`),
			want:     "progress: (no message)",
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

		// --- log_artifact ---
		{
			name:     "log_artifact with name",
			toolName: "log_artifact",
			args:     json.RawMessage(`{"name": "output.json"}`),
			want:     "artifact: output.json",
		},

		// --- complete_task ---
		{
			name:     "complete_task with summary",
			toolName: "complete_task",
			args:     json.RawMessage(`{"summary": "all tests passing"}`),
			want:     "task: all tests passing",
		},
		{
			name:     "complete_task with empty args returns sentinel",
			toolName: "complete_task",
			args:     json.RawMessage(`{}`),
			want:     "task: completed",
		},
		{
			name:     "complete_task with long summary truncated",
			toolName: "complete_task",
			args:     json.RawMessage(`{"summary": "implemented all core data models and tests"}`),
			want:     "task: implemented all core data mo…",
		},

		// --- request_new_task ---
		{
			name:     "request_new_task with description",
			toolName: "request_new_task",
			args:     json.RawMessage(`{"description": "add caching layer"}`),
			want:     "request: add caching layer",
		},
		{
			name:     "request_new_task with empty args returns sentinel",
			toolName: "request_new_task",
			args:     json.RawMessage(`{}`),
			want:     "request: new task",
		},
		{
			name:     "request_new_task with long description truncated",
			toolName: "request_new_task",
			args:     json.RawMessage(`{"description": "refactor the entire authentication subsystem"}`),
			want:     "request: refactor the entire authenti…",
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

// --------------------------------------------------------------------------
// TestRenderWorkerCard
// --------------------------------------------------------------------------

// stripANSI removes ANSI escape sequences so we can do plain-text assertions.
func stripANSI(s string) string {
	// Walk rune-by-rune, skipping ESC sequences.
	var out strings.Builder
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func TestRenderWorkerCard(t *testing.T) {
	t.Parallel()

	base := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	t.Run("returns non-empty string for active session", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-1",
			workerName: "builder",
			teamName:   "backend",
			task:       "implement feature X",
			jobID:      "job-abc123",
			status:     "active",
			startTime:  base,
		}

		result := renderWorkerCard(rs, 40, 8, 0, false, 0)

		if result == "" {
			t.Error("expected non-empty result for active session")
		}
	})

	t.Run("returns non-empty string for completed session", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-2",
			workerName: "tester",
			teamName:   "qa",
			task:       "run test suite",
			jobID:      "job-xyz789",
			status:     "completed",
			startTime:  base,
			endTime:    base.Add(5 * time.Minute),
		}

		result := renderWorkerCard(rs, 40, 8, 0, false, 0)

		if result == "" {
			t.Error("expected non-empty result for completed session")
		}
	})

	t.Run("clamps output to innerH lines for small heights", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-3",
			workerName: "worker",
			jobID:      "job-short12345678",
			status:     "active",
			startTime:  base,
		}

		// Should not panic for any small innerH, and never exceed the budget.
		for _, h := range []int{0, 1, 2, 3} {
			t.Run(fmt.Sprintf("innerH=%d", h), func(t *testing.T) {
				result := renderWorkerCard(rs, 40, h, 0, false, 0)
				lines := strings.Split(result, "\n")
				if h > 0 && len(lines) > h {
					t.Errorf("innerH=%d: got %d lines, expected at most %d", h, len(lines), h)
				}
			})
		}
	})

	t.Run("handles zero innerW gracefully without panic", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-4",
			workerName: "worker",
			jobID:      "job-1",
			status:     "active",
			startTime:  base,
		}

		// Must not panic.
		_ = renderWorkerCard(rs, 0, 8, 0, false, 0)
	})

	t.Run("uses worker label as headline when task is empty", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-5",
			workerName: "my-special-worker",
			jobID:      "job-1",
			status:     "active",
			startTime:  base,
		}

		result := stripANSI(renderWorkerCard(rs, 60, 10, 0, false, 0))

		if !strings.Contains(result, "my-special-worker") {
			t.Errorf("expected worker name 'my-special-worker' in output, got:\n%s", result)
		}
	})

	t.Run("uses team-scoped worker label as headline when task is empty", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-6",
			workerName: "builder",
			teamName:   "backend",
			jobID:      "job-1",
			status:     "active",
			startTime:  base,
		}

		result := stripANSI(renderWorkerCard(rs, 60, 10, 0, false, 0))

		// Should contain "backend/builder" (team-scoped label).
		if !strings.Contains(result, "backend/builder") {
			t.Errorf("expected 'backend/builder' in output, got:\n%s", result)
		}
	})

	t.Run("does not double-prefix when workerName already has teamName prefix", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-7",
			workerName: "myteam/orchestrator",
			teamName:   "myteam",
			jobID:      "job-1",
			status:     "active",
			startTime:  base,
		}

		result := stripANSI(renderWorkerCard(rs, 60, 10, 0, false, 0))

		// Should contain "myteam/orchestrator" exactly once, not "myteam/myteam/orchestrator".
		if strings.Contains(result, "myteam/myteam/orchestrator") {
			t.Errorf("worker name was double-prefixed: %s", result)
		}
		if !strings.Contains(result, "myteam/orchestrator") {
			t.Errorf("expected 'myteam/orchestrator' in output, got:\n%s", result)
		}
	})

	t.Run("task description is the headline when present", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-8",
			workerName: "worker",
			jobID:      "job-1",
			task:       "implement auth module",
			status:     "active",
			startTime:  base,
		}

		result := stripANSI(renderWorkerCard(rs, 60, 10, 0, false, 0))

		if !strings.Contains(result, "implement auth module") {
			t.Errorf("expected task description in output, got:\n%s", result)
		}
	})

	t.Run("active session with no activities shows waiting placeholder", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-9",
			workerName: "worker",
			jobID:      "job-1",
			status:     "active",
			activities: nil,
			startTime:  base,
		}

		result := stripANSI(renderWorkerCard(rs, 60, 10, 0, false, 0))

		if !strings.Contains(result, "waiting for activity") {
			t.Errorf("expected 'waiting for activity' placeholder for active session with no activities, got:\n%s", result)
		}
	})

	t.Run("completed session with no activities shows no placeholder", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-10",
			workerName: "worker",
			jobID:      "job-1",
			status:     "completed",
			activities: nil,
			startTime:  base,
			endTime:    base.Add(time.Minute),
		}

		result := stripANSI(renderWorkerCard(rs, 60, 10, 0, false, 0))

		if strings.Contains(result, "waiting for activity") {
			t.Errorf("completed session should not show 'waiting for activity', got:\n%s", result)
		}
	})

	t.Run("shows activities newest-first with gear prefix", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-11",
			workerName: "coder",
			jobID:      "job-1",
			status:     "active",
			startTime:  base,
			activities: []activityItem{
				{label: "write: main.go", toolName: "write_file"},
				{label: "shell: go build ./...", toolName: "shell"},
				{label: "read: config.yaml", toolName: "read_file"},
			},
		}

		result := stripANSI(renderWorkerCard(rs, 60, 12, 0, false, 0))

		// Activities are shown newest-first; "read: config.yaml" is the newest.
		if !strings.Contains(result, "read: config.yaml") {
			t.Errorf("expected newest activity 'read: config.yaml' in output, got:\n%s", result)
		}
		if !strings.Contains(result, "⚙") {
			t.Errorf("expected gear prefix on activity lines, got:\n%s", result)
		}
	})

	t.Run("activity list is capped to available height", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-12",
			workerName: "coder",
			jobID:      "job-1",
			status:     "active",
			startTime:  base,
			activities: []activityItem{
				{label: "act-1", toolName: "shell"},
				{label: "act-2", toolName: "shell"},
				{label: "act-3", toolName: "shell"},
				{label: "act-4", toolName: "shell"},
				{label: "act-5", toolName: "shell"},
				{label: "act-6", toolName: "shell"},
			},
		}

		result := renderWorkerCard(rs, 60, 6, 0, false, 0)
		lines := strings.Split(result, "\n")

		if len(lines) > 6 {
			t.Errorf("expected at most 6 lines (innerH=6), got %d:\n%s", len(lines), result)
		}
	})

	t.Run("short jobID is shown in header", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-13",
			workerName: "worker",
			jobID:      "abcdef1234567890", // 16 chars — only first 8 shown
			status:     "active",
			startTime:  base,
		}

		result := stripANSI(renderWorkerCard(rs, 60, 8, 0, false, 0))

		// Only the first 8 chars of the job ID should appear.
		if !strings.Contains(result, "abcdef12") {
			t.Errorf("expected short job ID 'abcdef12' in header, got:\n%s", result)
		}
		if strings.Contains(result, "abcdef1234567890") {
			t.Errorf("full job ID should be truncated to 8 chars, got:\n%s", result)
		}
	})

	t.Run("active session shows the bread status glyph", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-14",
			workerName: "worker",
			jobID:      "job-1",
			status:     "active",
			startTime:  base,
		}

		result := stripANSI(renderWorkerCard(rs, 60, 8, 0, false, 0))

		if !strings.Contains(result, "🍞") {
			t.Errorf("expected '🍞' status glyph for active session, got:\n%s", result)
		}
	})

	t.Run("completed session shows checkmark status mark", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-15",
			workerName: "worker",
			jobID:      "job-1",
			status:     "completed",
			startTime:  base,
			endTime:    base.Add(time.Minute),
		}

		result := stripANSI(renderWorkerCard(rs, 60, 8, 0, false, 0))

		if !strings.Contains(result, "✓") {
			t.Errorf("expected '✓' status mark for completed session, got:\n%s", result)
		}
	})

	t.Run("failed session shows cross status mark", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-15b",
			workerName: "worker",
			jobID:      "job-1",
			status:     "failed",
			startTime:  base,
			endTime:    base.Add(time.Minute),
		}

		result := stripANSI(renderWorkerCard(rs, 60, 8, 0, false, 0))

		if !strings.Contains(result, "✗") {
			t.Errorf("expected '✗' status mark for failed session, got:\n%s", result)
		}
	})

	t.Run("uses endTime for elapsed duration in completed session", func(t *testing.T) {
		t.Parallel()
		// endTime is exactly 2 minutes after startTime.
		rs := &runtimeSlot{
			sessionID:  "sess-16",
			workerName: "worker",
			jobID:      "job-1",
			status:     "completed",
			startTime:  base,
			endTime:    base.Add(2 * time.Minute),
		}

		result := stripANSI(renderWorkerCard(rs, 60, 8, 0, false, 0))

		// Duration should be "2m0s".
		if !strings.Contains(result, "2m0s") {
			t.Errorf("expected '2m0s' elapsed duration for completed session, got:\n%s", result)
		}
	})

	t.Run("falls back to 'runtime' when workerName is empty", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:  "sess-17",
			workerName: "", // empty — should fall back to "runtime"
			jobID:      "job-1",
			status:     "active",
			startTime:  base,
		}

		result := stripANSI(renderWorkerCard(rs, 60, 8, 0, false, 0))

		if !strings.Contains(result, "runtime") {
			t.Errorf("expected 'runtime' fallback label when workerName is empty, got:\n%s", result)
		}
	})

	t.Run("renders a context bar showing occupancy percentage", func(t *testing.T) {
		t.Parallel()
		rs := &runtimeSlot{
			sessionID:     "sess-18",
			workerName:    "worker",
			jobID:         "job-1",
			status:        "active",
			startTime:     base,
			contextTokens: 40000,
		}

		// ctxMax = 200000 → 40000/200000 = 20%.
		result := stripANSI(renderWorkerCard(rs, 60, 8, 200000, false, 0))

		if !strings.Contains(result, "20%") {
			t.Errorf("expected context bar to show '20%%', got:\n%s", result)
		}
	})
}

// --------------------------------------------------------------------------
// TestRuntimeSessionForGridCell
// --------------------------------------------------------------------------

func TestWorkerCardMeta(t *testing.T) {
	t.Parallel()

	cost := 0.0123
	cases := []struct {
		name     string
		rs       *runtimeSlot
		want     string
		contains []string
		empty    bool
	}{
		{name: "empty when no metrics", rs: &runtimeSlot{}, empty: true},
		{
			name:     "provider and model joined",
			rs:       &runtimeSlot{provider: "lmstudio", model: "qwen3"},
			contains: []string{"lmstudio/qwen3"},
		},
		{
			name:     "model only",
			rs:       &runtimeSlot{model: "qwen3"},
			contains: []string{"qwen3"},
		},
		{
			name:     "tokens and cost",
			rs:       &runtimeSlot{provider: "p", model: "m", tokensIn: 1200, tokensOut: 3400, costUSD: cost},
			contains: []string{"p/m", "↑", "↓", "~$0.01"},
		},
		{
			name:     "temperature shown when known",
			rs:       &runtimeSlot{model: "m", hasTemp: true, temperature: 0.5},
			contains: []string{"m", "t0.5"},
		},
		{
			name:     "thinking indicator with temperature",
			rs:       &runtimeSlot{model: "m", hasTemp: true, temperature: 0.2, thinking: true},
			contains: []string{"t0.2", "🧠"},
		},
		{
			name:  "zero cost omitted",
			rs:    &runtimeSlot{model: "m", costUSD: 0},
			want:  "m",
			empty: false,
		},
		{
			name:     "diff stat included when non-zero",
			rs:       &runtimeSlot{model: "m", diffAdded: 12, diffRemoved: 3},
			contains: []string{"m", "+12 −3"},
		},
		{
			name:  "diff stat omitted when zero",
			rs:    &runtimeSlot{model: "m", diffAdded: 0, diffRemoved: 0},
			want:  "m",
			empty: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := workerCardMeta(c.rs)
			if c.empty && got != "" {
				t.Fatalf("expected empty, got %q", got)
			}
			if c.want != "" && got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
			for _, sub := range c.contains {
				if !strings.Contains(got, sub) {
					t.Errorf("got %q, want it to contain %q", got, sub)
				}
			}
		})
	}
}

func TestFormatDiffStat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		added, removed int
		want           string
	}{
		{name: "both added and removed", added: 12, removed: 3, want: "+12 −3"},
		{name: "add only", added: 5, removed: 0, want: "+5"},
		{name: "remove only", added: 0, removed: 7, want: "−7"},
		{name: "both zero", added: 0, removed: 0, want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := formatDiffStat(c.added, c.removed); got != c.want {
				t.Errorf("formatDiffStat(%d, %d) = %q, want %q", c.added, c.removed, got, c.want)
			}
		})
	}
}

func TestFailedGraphNode(t *testing.T) {
	t.Parallel()

	m := &Model{}
	if got := m.failedGraphNode("nope"); got != "" {
		t.Errorf("unknown task: got %q, want empty", got)
	}

	m.graphTasks = map[string]*graphTaskState{
		"task-1": {
			taskID: "task-1",
			nodes: dagmap.NodeStates{
				"investigate": {Phase: dagmap.PhaseCompleted},
				"implement":   {Phase: dagmap.PhaseFailed},
				"plan":        {Phase: dagmap.PhaseCompleted},
			},
		},
	}
	if got := m.failedGraphNode("task-1"); got != "implement" {
		t.Errorf("got %q, want %q", got, "implement")
	}

	m.graphTasks["task-2"] = &graphTaskState{
		taskID: "task-2",
		nodes:  dagmap.NodeStates{"a": {Phase: dagmap.PhaseCompleted}},
	}
	if got := m.failedGraphNode("task-2"); got != "" {
		t.Errorf("no failed node: got %q, want empty", got)
	}
}
