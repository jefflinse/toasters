package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

// --------------------------------------------------------------------------
// TestRuntimeSessionsForTask
// --------------------------------------------------------------------------

func TestRuntimeSessionsForTask(t *testing.T) {
	t.Parallel()

	base := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	makeSlot := func(sessionID, taskID, status string, startOffset time.Duration) *runtimeSlot {
		return &runtimeSlot{
			sessionID: sessionID,
			taskID:    taskID,
			status:    status,
			startTime: base.Add(startOffset),
		}
	}

	t.Run("returns matching sessions for task", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.runtimeSessions = map[string]*runtimeSlot{
			"s1": makeSlot("s1", "task-A", "active", 0),
			"s2": makeSlot("s2", "task-B", "active", time.Second),
			"s3": makeSlot("s3", "task-A", "completed", 2*time.Second),
		}

		got := m.runtimeSessionsForTask("task-A")

		if len(got) != 2 {
			t.Fatalf("expected 2 sessions for task-A, got %d", len(got))
		}
		// Verify both returned sessions belong to task-A.
		for _, rs := range got {
			if rs.taskID != "task-A" {
				t.Errorf("unexpected taskID %q in result", rs.taskID)
			}
		}
	})

	t.Run("returns empty non-nil slice when no matches", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.runtimeSessions = map[string]*runtimeSlot{
			"s1": makeSlot("s1", "task-A", "active", 0),
		}

		got := m.runtimeSessionsForTask("task-Z")

		if got == nil {
			t.Error("expected non-nil empty slice, got nil")
		}
		if len(got) != 0 {
			t.Errorf("expected 0 sessions, got %d", len(got))
		}
	})

	t.Run("returns empty non-nil slice for empty taskID", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.runtimeSessions = map[string]*runtimeSlot{
			"s1": makeSlot("s1", "task-A", "active", 0),
		}

		got := m.runtimeSessionsForTask("")

		if got == nil {
			t.Error("expected non-nil empty slice, got nil")
		}
		if len(got) != 0 {
			t.Errorf("expected 0 sessions for empty taskID, got %d", len(got))
		}
	})

	t.Run("returns empty non-nil slice when runtimeSessions is empty", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		// runtimeSessions is already an empty map from newMinimalModel.

		got := m.runtimeSessionsForTask("task-A")

		if got == nil {
			t.Error("expected non-nil empty slice, got nil")
		}
		if len(got) != 0 {
			t.Errorf("expected 0 sessions, got %d", len(got))
		}
	})

	t.Run("sorts active sessions before completed", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.runtimeSessions = map[string]*runtimeSlot{
			// completed starts earlier but should sort after active.
			"s-completed": makeSlot("s-completed", "task-X", "completed", 0),
			"s-active":    makeSlot("s-active", "task-X", "active", 5*time.Second),
		}

		got := m.runtimeSessionsForTask("task-X")

		if len(got) != 2 {
			t.Fatalf("expected 2 sessions, got %d", len(got))
		}
		if got[0].status != "active" {
			t.Errorf("first session should be active, got status %q", got[0].status)
		}
		if got[1].status != "completed" {
			t.Errorf("second session should be completed, got status %q", got[1].status)
		}
	})

	t.Run("sorts by startTime ascending within same status group", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.runtimeSessions = map[string]*runtimeSlot{
			// Three active sessions with different start times.
			"s-late":   makeSlot("s-late", "task-Y", "active", 10*time.Second),
			"s-early":  makeSlot("s-early", "task-Y", "active", 0),
			"s-middle": makeSlot("s-middle", "task-Y", "active", 5*time.Second),
		}

		got := m.runtimeSessionsForTask("task-Y")

		if len(got) != 3 {
			t.Fatalf("expected 3 sessions, got %d", len(got))
		}
		if got[0].sessionID != "s-early" {
			t.Errorf("first session should be s-early (earliest start), got %q", got[0].sessionID)
		}
		if got[1].sessionID != "s-middle" {
			t.Errorf("second session should be s-middle, got %q", got[1].sessionID)
		}
		if got[2].sessionID != "s-late" {
			t.Errorf("third session should be s-late (latest start), got %q", got[2].sessionID)
		}
	})

	t.Run("sorts completed sessions by startTime ascending", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.runtimeSessions = map[string]*runtimeSlot{
			"c-second": makeSlot("c-second", "task-Z", "completed", 2*time.Second),
			"c-first":  makeSlot("c-first", "task-Z", "completed", 0),
		}

		got := m.runtimeSessionsForTask("task-Z")

		if len(got) != 2 {
			t.Fatalf("expected 2 sessions, got %d", len(got))
		}
		if got[0].sessionID != "c-first" {
			t.Errorf("first completed session should be c-first, got %q", got[0].sessionID)
		}
		if got[1].sessionID != "c-second" {
			t.Errorf("second completed session should be c-second, got %q", got[1].sessionID)
		}
	})

	t.Run("handles multiple sessions for same task with mixed statuses", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.runtimeSessions = map[string]*runtimeSlot{
			"a1": makeSlot("a1", "task-M", "active", 3*time.Second),
			"a2": makeSlot("a2", "task-M", "active", 1*time.Second),
			"c1": makeSlot("c1", "task-M", "completed", 0),
			"c2": makeSlot("c2", "task-M", "completed", 2*time.Second),
			// Different task — should not appear.
			"other": makeSlot("other", "task-OTHER", "active", 0),
		}

		got := m.runtimeSessionsForTask("task-M")

		if len(got) != 4 {
			t.Fatalf("expected 4 sessions for task-M, got %d", len(got))
		}
		// First two should be active (sorted by startTime).
		if got[0].status != "active" || got[0].sessionID != "a2" {
			t.Errorf("got[0] = {status:%q, id:%q}, want {active, a2}", got[0].status, got[0].sessionID)
		}
		if got[1].status != "active" || got[1].sessionID != "a1" {
			t.Errorf("got[1] = {status:%q, id:%q}, want {active, a1}", got[1].status, got[1].sessionID)
		}
		// Last two should be completed (sorted by startTime).
		if got[2].status != "completed" || got[2].sessionID != "c1" {
			t.Errorf("got[2] = {status:%q, id:%q}, want {completed, c1}", got[2].status, got[2].sessionID)
		}
		if got[3].status != "completed" || got[3].sessionID != "c2" {
			t.Errorf("got[3] = {status:%q, id:%q}, want {completed, c2}", got[3].status, got[3].sessionID)
		}
	})

	t.Run("stable tiebreaker by sessionID when startTimes are equal", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		// Both sessions have identical startTime — sessionID is the tiebreaker.
		sameTime := base
		m.runtimeSessions = map[string]*runtimeSlot{
			"z-session": {sessionID: "z-session", taskID: "task-T", status: "active", startTime: sameTime},
			"a-session": {sessionID: "a-session", taskID: "task-T", status: "active", startTime: sameTime},
		}

		got := m.runtimeSessionsForTask("task-T")

		if len(got) != 2 {
			t.Fatalf("expected 2 sessions, got %d", len(got))
		}
		// "a-session" < "z-session" lexicographically.
		if got[0].sessionID != "a-session" {
			t.Errorf("first session should be a-session (lexicographic tiebreaker), got %q", got[0].sessionID)
		}
		if got[1].sessionID != "z-session" {
			t.Errorf("second session should be z-session, got %q", got[1].sessionID)
		}
	})
}

func TestWrapText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		maxWidth int
		check    func(t *testing.T, result string)
	}{
		{
			name:     "empty string",
			input:    "",
			maxWidth: 40,
			check: func(t *testing.T, result string) {
				if result != "" {
					t.Errorf("got %q, want empty string", result)
				}
			},
		},
		{
			name:     "single word shorter than width",
			input:    "hello",
			maxWidth: 40,
			check: func(t *testing.T, result string) {
				if result != "hello" {
					t.Errorf("got %q, want %q", result, "hello")
				}
			},
		},
		{
			name:     "text shorter than width stays on one line",
			input:    "hello world",
			maxWidth: 40,
			check: func(t *testing.T, result string) {
				if strings.Contains(result, "\n") {
					t.Errorf("expected single line, got %q", result)
				}
				if result != "hello world" {
					t.Errorf("got %q, want %q", result, "hello world")
				}
			},
		},
		{
			name:     "text wraps at word boundary",
			input:    "hello world foo bar",
			maxWidth: 11,
			check: func(t *testing.T, result string) {
				lines := strings.Split(result, "\n")
				if len(lines) < 2 {
					t.Errorf("expected wrapping, got single line: %q", result)
				}
				// Each line should be at most 11 chars wide.
				for i, line := range lines {
					if len(line) > 11 {
						t.Errorf("line %d %q exceeds maxWidth 11", i, line)
					}
				}
			},
		},
		{
			name:     "preserves existing newlines",
			input:    "line one\nline two",
			maxWidth: 40,
			check: func(t *testing.T, result string) {
				lines := strings.Split(result, "\n")
				if len(lines) != 2 {
					t.Errorf("expected 2 lines, got %d: %q", len(lines), result)
				}
				if lines[0] != "line one" {
					t.Errorf("line 0: got %q, want %q", lines[0], "line one")
				}
				if lines[1] != "line two" {
					t.Errorf("line 1: got %q, want %q", lines[1], "line two")
				}
			},
		},
		{
			name:     "zero maxWidth uses default 40",
			input:    "short",
			maxWidth: 0,
			check: func(t *testing.T, result string) {
				// Should not panic and should return the text.
				if result != "short" {
					t.Errorf("got %q, want %q", result, "short")
				}
			},
		},
		{
			name:     "negative maxWidth uses default 40",
			input:    "short",
			maxWidth: -5,
			check: func(t *testing.T, result string) {
				if result != "short" {
					t.Errorf("got %q, want %q", result, "short")
				}
			},
		},
		{
			name:     "very long word gets broken",
			input:    "abcdefghijklmnopqrstuvwxyz",
			maxWidth: 10,
			check: func(t *testing.T, result string) {
				lines := strings.Split(result, "\n")
				if len(lines) < 2 {
					t.Errorf("expected long word to be broken, got single line: %q", result)
				}
				for i, line := range lines {
					if len(line) > 10 {
						t.Errorf("line %d %q exceeds maxWidth 10", i, line)
					}
				}
				// Reassembled should equal original.
				reassembled := strings.Join(lines, "")
				if reassembled != "abcdefghijklmnopqrstuvwxyz" {
					t.Errorf("reassembled %q != original", reassembled)
				}
			},
		},
		{
			name:     "short line with multiple spaces preserved",
			input:    "hello   world",
			maxWidth: 40,
			check: func(t *testing.T, result string) {
				// When the line fits within maxWidth, it's returned as-is (no word-wrapping path).
				if result != "hello   world" {
					t.Errorf("got %q, want %q", result, "hello   world")
				}
			},
		},
		{
			name:     "long line with multiple spaces collapses whitespace",
			input:    "hello   world   foo   bar",
			maxWidth: 15,
			check: func(t *testing.T, result string) {
				// When wrapping is triggered, strings.Fields collapses whitespace.
				lines := strings.Split(result, "\n")
				if len(lines) < 2 {
					t.Errorf("expected wrapping, got single line: %q", result)
				}
				// No line should have multiple consecutive spaces (Fields collapses them).
				for i, line := range lines {
					if strings.Contains(line, "  ") {
						t.Errorf("line %d %q contains double spaces", i, line)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := wrapText(tt.input, tt.maxWidth)
			tt.check(t, result)
		})
	}
}

func TestTruncateStr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "empty string",
			input:  "",
			maxLen: 10,
			want:   "",
		},
		{
			name:   "string shorter than max",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "string exactly at max",
			input:  "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "string longer than max gets ellipsis",
			input:  "hello world",
			maxLen: 8,
			want:   "hello...",
		},
		{
			name:   "maxLen of 3 is minimum",
			input:  "hello",
			maxLen: 3,
			want:   "...",
		},
		{
			name:   "maxLen less than 3 is clamped to 3",
			input:  "hello",
			maxLen: 1,
			want:   "...",
		},
		{
			name:   "maxLen of 0 is clamped to 3",
			input:  "hello",
			maxLen: 0,
			want:   "...",
		},
		{
			name:   "negative maxLen is clamped to 3",
			input:  "hello",
			maxLen: -5,
			want:   "...",
		},
		{
			name:   "maxLen of 4 truncates to 1 char plus ellipsis",
			input:  "hello",
			maxLen: 4,
			want:   "h...",
		},
		{
			name:   "string with exactly maxLen chars not truncated",
			input:  "abcdefghij",
			maxLen: 10,
			want:   "abcdefghij",
		},
		{
			name:   "string one char over maxLen",
			input:  "abcdefghijk",
			maxLen: 10,
			want:   "abcdefg...",
		},
		// --- Unicode / multi-byte rune cases ---
		{
			// "é" is 2 bytes (U+00E9) but 1 rune.
			// "héllo" = 5 runes; maxLen=5 → no truncation.
			name:   "multi-byte unicode string at exact maxLen not truncated",
			input:  "héllo",
			maxLen: 5,
			want:   "héllo",
		},
		{
			// "héllo" = 5 runes; maxLen=4 → keep first 1 rune + "..."
			name:   "multi-byte unicode string truncated at rune boundary",
			input:  "héllo",
			maxLen: 4,
			want:   "h...",
		},
		{
			// "héllo" = 5 runes; maxLen=10 → returned unchanged (shorter than max).
			name:   "multi-byte unicode string shorter than maxLen returned unchanged",
			input:  "héllo",
			maxLen: 10,
			want:   "héllo",
		},
		{
			// "hi 🎉 world" rune breakdown:
			//   h i   (space) 🎉 (space) w o r l d  = 11 runes
			//   🎉 is 4 bytes but 1 rune.
			// maxLen=7 → keep first 4 runes ("hi 🎉") + "..."
			name:   "emoji string truncated at correct rune boundary",
			input:  "hi 🎉 world",
			maxLen: 7,
			want:   "hi 🎉...",
		},
		{
			// "hi 🎉 world" = 11 runes; maxLen=11 → returned unchanged.
			name:   "emoji string at exact maxLen not truncated",
			input:  "hi 🎉 world",
			maxLen: 11,
			want:   "hi 🎉 world",
		},
		{
			// "hi 🎉 world" = 11 runes; maxLen=20 → returned unchanged.
			name:   "emoji string shorter than maxLen returned unchanged",
			input:  "hi 🎉 world",
			maxLen: 20,
			want:   "hi 🎉 world",
		},
		{
			// maxLen <= 3 is clamped to 3; "héllo" (5 runes) > 3 → "..."
			name:   "maxLen 2 clamped to 3 with unicode input yields only ellipsis",
			input:  "héllo",
			maxLen: 2,
			want:   "...",
		},
		{
			// maxLen <= 3 is clamped to 3; "🎉" (1 rune) <= 3 → returned unchanged.
			name:   "maxLen 1 clamped to 3 with single emoji shorter than clamped max returned unchanged",
			input:  "🎉",
			maxLen: 1,
			want:   "🎉",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateStr(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateStr(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestFirstLineOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty string returns empty",
			input: "",
			want:  "",
		},
		{
			name:  "single line",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "multi-line returns first",
			input: "first line\nsecond line\nthird line",
			want:  "first line",
		},
		{
			name:  "skips leading empty lines",
			input: "\n\n  \nhello\nworld",
			want:  "hello",
		},
		{
			name:  "trims whitespace from first line",
			input: "  hello  \nworld",
			want:  "hello",
		},
		{
			name:  "all empty lines returns original",
			input: "\n\n\n",
			want:  "\n\n\n",
		},
		{
			name:  "whitespace only lines returns original",
			input: "   \n   \n   ",
			want:  "   \n   \n   ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := firstLineOf(tt.input)
			if got != tt.want {
				t.Errorf("firstLineOf(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractToolName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "standard tool call format",
			content: "calling `function_name`...",
			want:    "function_name",
		},
		{
			name:    "tool call with emoji prefix",
			content: "calling `read_file`...",
			want:    "read_file",
		},
		{
			name:    "no backticks returns fallback",
			content: "some random content",
			want:    "tool call",
		},
		{
			name:    "empty string returns fallback",
			content: "",
			want:    "tool call",
		},
		{
			name:    "single backtick no closing returns fallback",
			content: "calling `function_name without closing",
			want:    "tool call",
		},
		{
			name:    "empty backtick pair",
			content: "calling ``...",
			want:    "",
		},
		{
			name:    "backticks with complex name",
			content: "calling `my.namespace.tool_v2`...",
			want:    "my.namespace.tool_v2",
		},
		{
			name:    "multiple backtick pairs returns first",
			content: "calling `first` then `second`",
			want:    "first",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractToolName(tt.content)
			if got != tt.want {
				t.Errorf("extractToolName(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestFormatToolName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "built-in tool unchanged",
			in:   "read_file",
			want: "read_file",
		},
		{
			name: "MCP namespaced tool formatted",
			in:   "github__search_repositories",
			want: "search_repositories (via github)",
		},
		{
			name: "MCP tool with multiple underscores in tool name",
			in:   "linear__list_my_issues",
			want: "list_my_issues (via linear)",
		},
		{
			name: "multiple double underscores uses first split",
			in:   "server__ns__tool",
			want: "ns__tool (via server)",
		},
		{
			name: "empty string unchanged",
			in:   "",
			want: "",
		},
		{
			name: "single underscore unchanged",
			in:   "read_file",
			want: "read_file",
		},
		{
			name: "double underscore at start",
			in:   "__tool_name",
			want: "tool_name (via )",
		},
		{
			name: "double underscore at end",
			in:   "server__",
			want: " (via server)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatToolName(tt.in)
			if got != tt.want {
				t.Errorf("formatToolName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatToolCallContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "MCP tool in standard format",
			content: "⚙ calling `github__search_repositories`…",
			want:    "⚙ calling `search_repositories (via github)`…",
		},
		{
			name:    "built-in tool unchanged",
			content: "⚙ calling `read_file`…",
			want:    "⚙ calling `read_file`…",
		},
		{
			name:    "no backticks unchanged",
			content: "some random content",
			want:    "some random content",
		},
		{
			name:    "single backtick no closing unchanged",
			content: "calling `github__tool without closing",
			want:    "calling `github__tool without closing",
		},
		{
			name:    "empty content unchanged",
			content: "",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatToolCallContent(tt.content)
			if got != tt.want {
				t.Errorf("formatToolCallContent(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestRenderCompletionBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		check   func(t *testing.T, result string)
	}{
		{
			name:    "single line content",
			content: "hello world",
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "hello world") {
					t.Errorf("result should contain 'hello world', got %q", result)
				}
				// Should end with newline.
				if !strings.HasSuffix(result, "\n") {
					t.Errorf("result should end with newline, got %q", result)
				}
			},
		},
		{
			name:    "multi-line content",
			content: "line one\nline two\nline three",
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "line one") {
					t.Errorf("result should contain 'line one', got %q", result)
				}
				if !strings.Contains(result, "line two") {
					t.Errorf("result should contain 'line two', got %q", result)
				}
				if !strings.Contains(result, "line three") {
					t.Errorf("result should contain 'line three', got %q", result)
				}
			},
		},
		{
			name:    "content with leading/trailing whitespace is trimmed",
			content: "  \n  hello  \n  ",
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "hello") {
					t.Errorf("result should contain 'hello', got %q", result)
				}
			},
		},
		{
			name:    "empty content",
			content: "",
			check: func(t *testing.T, result string) {
				// Should not panic and should produce some output.
				if result == "" {
					t.Error("expected non-empty result even for empty content")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := renderCompletionBlock(tt.content)
			tt.check(t, result)
		})
	}
}

func TestOverlayToasts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		screen      string
		toastBlock  string
		screenWidth int
		check       func(t *testing.T, result string)
	}{
		{
			name:        "empty toast returns screen unchanged",
			screen:      "hello\nworld",
			toastBlock:  "",
			screenWidth: 20,
			check: func(t *testing.T, result string) {
				if result != "hello\nworld" {
					t.Errorf("got %q, want %q", result, "hello\nworld")
				}
			},
		},
		{
			name:        "toast overlaid on screen",
			screen:      "aaaaaaaaaa\nbbbbbbbbbb",
			toastBlock:  "XX",
			screenWidth: 10,
			check: func(t *testing.T, result string) {
				lines := strings.Split(result, "\n")
				if len(lines) != 2 {
					t.Fatalf("expected 2 lines, got %d", len(lines))
				}
				// First line should end with the toast text.
				if !strings.HasSuffix(lines[0], "XX") {
					t.Errorf("first line should end with toast, got %q", lines[0])
				}
				// Second line should be unchanged.
				if lines[1] != "bbbbbbbbbb" {
					t.Errorf("second line should be unchanged, got %q", lines[1])
				}
			},
		},
		{
			name:        "toast wider than screen replaces line",
			screen:      "short\nline2",
			toastBlock:  "this is a very long toast message",
			screenWidth: 5,
			check: func(t *testing.T, result string) {
				lines := strings.Split(result, "\n")
				if len(lines) < 1 {
					t.Fatal("expected at least 1 line")
				}
				// When toast is wider than screen, it replaces the line.
				if lines[0] != "this is a very long toast message" {
					t.Errorf("expected toast to replace line, got %q", lines[0])
				}
			},
		},
		{
			name:        "toast with more lines than screen",
			screen:      "only one line",
			toastBlock:  "toast1\ntoast2\ntoast3",
			screenWidth: 20,
			check: func(t *testing.T, result string) {
				// Should not panic. Only the first toast line should be overlaid.
				lines := strings.Split(result, "\n")
				if len(lines) != 1 {
					t.Errorf("expected 1 line, got %d", len(lines))
				}
			},
		},
		{
			name:        "screen line shorter than screen width gets padded",
			screen:      "hi\nbye",
			toastBlock:  "TT",
			screenWidth: 10,
			check: func(t *testing.T, result string) {
				lines := strings.Split(result, "\n")
				if !strings.HasSuffix(lines[0], "TT") {
					t.Errorf("first line should end with toast, got %q", lines[0])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := overlayToasts(tt.screen, tt.toastBlock, tt.screenWidth)
			tt.check(t, result)
		})
	}
}

func TestRenderScrollbar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		viewportHeight int
		totalLines     int
		scrollPercent  float64
		check          func(t *testing.T, result string)
	}{
		{
			name:           "basic scrollbar at top",
			viewportHeight: 10,
			totalLines:     100,
			scrollPercent:  0.0,
			check: func(t *testing.T, result string) {
				lines := strings.Split(result, "\n")
				if len(lines) != 10 {
					t.Errorf("expected 10 lines, got %d", len(lines))
				}
			},
		},
		{
			name:           "scrollbar at bottom",
			viewportHeight: 10,
			totalLines:     100,
			scrollPercent:  1.0,
			check: func(t *testing.T, result string) {
				lines := strings.Split(result, "\n")
				if len(lines) != 10 {
					t.Errorf("expected 10 lines, got %d", len(lines))
				}
			},
		},
		{
			name:           "scrollbar at middle",
			viewportHeight: 10,
			totalLines:     100,
			scrollPercent:  0.5,
			check: func(t *testing.T, result string) {
				lines := strings.Split(result, "\n")
				if len(lines) != 10 {
					t.Errorf("expected 10 lines, got %d", len(lines))
				}
			},
		},
		{
			name:           "thumb fills entire viewport when content equals viewport",
			viewportHeight: 10,
			totalLines:     10,
			scrollPercent:  0.0,
			check: func(t *testing.T, result string) {
				lines := strings.Split(result, "\n")
				if len(lines) != 10 {
					t.Errorf("expected 10 lines, got %d", len(lines))
				}
			},
		},
		{
			name:           "negative scroll percent clamped",
			viewportHeight: 10,
			totalLines:     100,
			scrollPercent:  -0.5,
			check: func(t *testing.T, result string) {
				lines := strings.Split(result, "\n")
				if len(lines) != 10 {
					t.Errorf("expected 10 lines, got %d", len(lines))
				}
			},
		},
		{
			name:           "scroll percent over 1.0 clamped",
			viewportHeight: 10,
			totalLines:     100,
			scrollPercent:  2.0,
			check: func(t *testing.T, result string) {
				lines := strings.Split(result, "\n")
				if len(lines) != 10 {
					t.Errorf("expected 10 lines, got %d", len(lines))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := renderScrollbar(tt.viewportHeight, tt.totalLines, tt.scrollPercent)
			tt.check(t, result)
		})
	}
}

func TestDisplayJobs(t *testing.T) {
	t.Parallel()

	makeJob := func(id string, status service.JobStatus, updatedAt time.Time) service.Job {
		return service.Job{
			ID:        id,
			Title:     id,
			Status:    status,
			UpdatedAt: updatedAt,
		}
	}

	t.Run("empty jobs list", func(t *testing.T) {
		t.Parallel()
		m := Model{jobs: nil}
		result := m.displayJobs()
		if len(result) != 0 {
			t.Errorf("expected empty result, got %d jobs", len(result))
		}
	})

	t.Run("active jobs come first", func(t *testing.T) {
		t.Parallel()
		m := Model{
			jobs: []service.Job{
				makeJob("done-1", service.JobStatusCompleted, time.Now()),
				makeJob("active-1", service.JobStatusActive, time.Time{}),
				makeJob("paused-1", service.JobStatusPaused, time.Time{}),
			},
		}
		result := m.displayJobs()
		if len(result) != 3 {
			t.Fatalf("expected 3 jobs, got %d", len(result))
		}
		if result[0].ID != "active-1" {
			t.Errorf("first job should be active, got %q", result[0].ID)
		}
		if result[1].ID != "paused-1" {
			t.Errorf("second job should be paused, got %q", result[1].ID)
		}
		if result[2].ID != "done-1" {
			t.Errorf("third job should be done, got %q", result[2].ID)
		}
	})

	t.Run("stale done jobs are hidden", func(t *testing.T) {
		t.Parallel()
		staleTime := time.Now().Add(-48 * time.Hour)
		recentTime := time.Now().Add(-1 * time.Hour)
		m := Model{
			jobs: []service.Job{
				makeJob("stale", service.JobStatusCompleted, staleTime),
				makeJob("recent", service.JobStatusCompleted, recentTime),
				makeJob("active", service.JobStatusActive, time.Time{}),
			},
		}
		result := m.displayJobs()
		if len(result) != 2 {
			t.Fatalf("expected 2 jobs (stale hidden), got %d", len(result))
		}
		for _, j := range result {
			if j.ID == "stale" {
				t.Error("stale done job should be hidden")
			}
		}
	})

	t.Run("done job without updated timestamp is shown", func(t *testing.T) {
		t.Parallel()
		m := Model{
			jobs: []service.Job{
				makeJob("done-no-ts", service.JobStatusCompleted, time.Time{}),
			},
		}
		result := m.displayJobs()
		if len(result) != 1 {
			t.Fatalf("expected 1 job, got %d", len(result))
		}
	})
}

func TestHasBlocker(t *testing.T) {
	t.Parallel()

	t.Run("no blockers map entry", func(t *testing.T) {
		t.Parallel()
		m := Model{
			blockers: make(map[string]*service.Blocker),
		}
		j := service.Job{ID: "job-1"}
		if m.hasBlocker(j) {
			t.Error("expected no blocker for job without entry")
		}
	})

	t.Run("nil blocker entry", func(t *testing.T) {
		t.Parallel()
		m := Model{
			blockers: map[string]*service.Blocker{
				"job-1": nil,
			},
		}
		j := service.Job{ID: "job-1"}
		if m.hasBlocker(j) {
			t.Error("expected no blocker for nil entry")
		}
	})

	t.Run("answered blocker", func(t *testing.T) {
		t.Parallel()
		m := Model{
			blockers: map[string]*service.Blocker{
				"job-1": {Answered: true},
			},
		}
		j := service.Job{ID: "job-1"}
		if m.hasBlocker(j) {
			t.Error("expected no blocker for answered blocker")
		}
	})

	t.Run("unanswered blocker", func(t *testing.T) {
		t.Parallel()
		m := Model{
			blockers: map[string]*service.Blocker{
				"job-1": {Answered: false},
			},
		}
		j := service.Job{ID: "job-1"}
		if !m.hasBlocker(j) {
			t.Error("expected blocker for unanswered blocker")
		}
	})
}

func TestJobByID(t *testing.T) {
	t.Parallel()

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		m := Model{
			jobs: []service.Job{
				{ID: "job-1", Title: "First"},
				{ID: "job-2", Title: "Second"},
			},
		}
		j, ok := m.jobByID("job-2")
		if !ok {
			t.Fatal("expected to find job-2")
		}
		if j.Title != "Second" {
			t.Errorf("got title %q, want %q", j.Title, "Second")
		}
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		m := Model{
			jobs: []service.Job{
				{ID: "job-1", Title: "First"},
			},
		}
		_, ok := m.jobByID("nonexistent")
		if ok {
			t.Error("expected not to find nonexistent job")
		}
	})

	t.Run("empty jobs list", func(t *testing.T) {
		t.Parallel()
		m := Model{}
		_, ok := m.jobByID("any")
		if ok {
			t.Error("expected not to find job in empty list")
		}
	})
}

func TestHasConversation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries []ChatEntry
		want    bool
	}{
		{
			name:    "no entries returns false",
			entries: nil,
			want:    false,
		},
		{
			name:    "empty entries slice returns false",
			entries: []ChatEntry{},
			want:    false,
		},
		{
			name: "system-only entries returns false",
			entries: []ChatEntry{
				{Message: service.ChatMessage{Role: "system", Content: "You are a helpful assistant."}},
			},
			want: false,
		},
		{
			name: "assistant-only entries returns false",
			entries: []ChatEntry{
				{Message: service.ChatMessage{Role: "assistant", Content: "Hello! How can I help?"}},
			},
			want: false,
		},
		{
			name: "system and assistant entries returns false",
			entries: []ChatEntry{
				{Message: service.ChatMessage{Role: "system", Content: "You are a helpful assistant."}},
				{Message: service.ChatMessage{Role: "assistant", Content: "Hello!"}},
			},
			want: false,
		},
		{
			name: "single user message returns true",
			entries: []ChatEntry{
				{Message: service.ChatMessage{Role: "user", Content: "Hi there"}},
			},
			want: true,
		},
		{
			name: "user message among other roles returns true",
			entries: []ChatEntry{
				{Message: service.ChatMessage{Role: "system", Content: "You are a helpful assistant."}},
				{Message: service.ChatMessage{Role: "assistant", Content: "Hello!"}},
				{Message: service.ChatMessage{Role: "user", Content: "What is Go?"}},
				{Message: service.ChatMessage{Role: "assistant", Content: "Go is a programming language."}},
			},
			want: true,
		},
		{
			name: "tool role entries without user returns false",
			entries: []ChatEntry{
				{Message: service.ChatMessage{Role: "system", Content: "system prompt"}},
				{Message: service.ChatMessage{Role: "assistant", Content: "calling tool"}},
				{Message: service.ChatMessage{Role: "tool", Content: "tool result"}},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.chat.entries = tt.entries

			got := m.hasConversation()
			if got != tt.want {
				t.Errorf("hasConversation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMessagesFromEntries(t *testing.T) {
	t.Parallel()

	t.Run("empty entries returns empty slice", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.chat.entries = nil

		msgs := m.messagesFromEntries()
		if len(msgs) != 0 {
			t.Errorf("expected empty slice, got %d messages", len(msgs))
		}
	})

	t.Run("single entry returns single message", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.chat.entries = []ChatEntry{
			{
				Message:   service.ChatMessage{Role: "user", Content: "hello"},
				Timestamp: time.Now(),
			},
		}

		msgs := m.messagesFromEntries()
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message, got %d", len(msgs))
		}
		if msgs[0].Role != "user" {
			t.Errorf("expected role 'user', got %q", msgs[0].Role)
		}
		if msgs[0].Content != "hello" {
			t.Errorf("expected content 'hello', got %q", msgs[0].Content)
		}
	})

	t.Run("multiple entries preserve order and content", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.chat.entries = []ChatEntry{
			{Message: service.ChatMessage{Role: "system", Content: "system prompt"}},
			{Message: service.ChatMessage{Role: "user", Content: "question"}},
			{Message: service.ChatMessage{Role: "assistant", Content: "answer"}},
		}

		msgs := m.messagesFromEntries()
		if len(msgs) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(msgs))
		}

		expectedRoles := []service.MessageRole{"system", "user", "assistant"}
		expectedContents := []string{"system prompt", "question", "answer"}
		for i, msg := range msgs {
			if msg.Role != expectedRoles[i] {
				t.Errorf("message %d: expected role %q, got %q", i, expectedRoles[i], msg.Role)
			}
			if msg.Content != expectedContents[i] {
				t.Errorf("message %d: expected content %q, got %q", i, expectedContents[i], msg.Content)
			}
		}
	})

	t.Run("extra ChatEntry fields are not included in messages", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.chat.entries = []ChatEntry{
			{
				Message:    service.ChatMessage{Role: "assistant", Content: "response"},
				Timestamp:  time.Now(),
				Reasoning:  "I thought about it",
				ClaudeMeta: "operator · model-name",
			},
		}

		msgs := m.messagesFromEntries()
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message, got %d", len(msgs))
		}
		// The returned message should be the provider.Message from the entry.
		if msgs[0].Role != "assistant" {
			t.Errorf("expected role 'assistant', got %q", msgs[0].Role)
		}
		if msgs[0].Content != "response" {
			t.Errorf("expected content 'response', got %q", msgs[0].Content)
		}
	})

	t.Run("tool call messages preserve tool call ID", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.chat.entries = []ChatEntry{
			{Message: service.ChatMessage{Role: "tool", Content: "result", ToolCallID: "call_123"}},
		}

		msgs := m.messagesFromEntries()
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message, got %d", len(msgs))
		}
		if msgs[0].ToolCallID != "call_123" {
			t.Errorf("expected ToolCallID 'call_123', got %q", msgs[0].ToolCallID)
		}
	})
}

func TestIsToolCallIndicatorIdx(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries []ChatEntry
		idx     int
		want    bool
	}{
		{
			name:    "negative index returns false",
			entries: []ChatEntry{{Message: service.ChatMessage{Role: "assistant"}, ClaudeMeta: "tool-call-indicator"}},
			idx:     -1,
			want:    false,
		},
		{
			name:    "index out of bounds returns false",
			entries: []ChatEntry{{Message: service.ChatMessage{Role: "assistant"}, ClaudeMeta: "tool-call-indicator"}},
			idx:     5,
			want:    false,
		},
		{
			name:    "valid index with tool-call-indicator returns true",
			entries: []ChatEntry{{Message: service.ChatMessage{Role: "assistant"}, ClaudeMeta: "tool-call-indicator"}},
			idx:     0,
			want:    true,
		},
		{
			name:    "valid index without tool-call-indicator returns false",
			entries: []ChatEntry{{Message: service.ChatMessage{Role: "assistant"}, ClaudeMeta: "operator"}},
			idx:     0,
			want:    false,
		},
		{
			name:    "empty entries with index 0 returns false",
			entries: nil,
			idx:     0,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.chat.entries = tt.entries

			got := m.isToolCallIndicatorIdx(tt.idx)
			if got != tt.want {
				t.Errorf("isToolCallIndicatorIdx(%d) = %v, want %v", tt.idx, got, tt.want)
			}
		})
	}
}
