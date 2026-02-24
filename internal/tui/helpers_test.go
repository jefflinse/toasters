package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/job"
)

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

	makeJob := func(id string, status job.Status, completed string) job.Job {
		return job.Job{
			Frontmatter: job.Frontmatter{
				ID:        id,
				Status:    status,
				Completed: completed,
			},
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
			jobs: []job.Job{
				makeJob("done-1", job.StatusDone, time.Now().Format(time.RFC3339)),
				makeJob("active-1", job.StatusActive, ""),
				makeJob("paused-1", job.StatusPaused, ""),
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
		staleTime := time.Now().Add(-48 * time.Hour).Format(time.RFC3339)
		recentTime := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
		m := Model{
			jobs: []job.Job{
				makeJob("stale", job.StatusDone, staleTime),
				makeJob("recent", job.StatusDone, recentTime),
				makeJob("active", job.StatusActive, ""),
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

	t.Run("done job without completed timestamp is shown", func(t *testing.T) {
		t.Parallel()
		m := Model{
			jobs: []job.Job{
				makeJob("done-no-ts", job.StatusDone, ""),
			},
		}
		result := m.displayJobs()
		if len(result) != 1 {
			t.Fatalf("expected 1 job, got %d", len(result))
		}
	})

	t.Run("done job with invalid timestamp is shown", func(t *testing.T) {
		t.Parallel()
		m := Model{
			jobs: []job.Job{
				makeJob("done-bad-ts", job.StatusDone, "not-a-date"),
			},
		}
		result := m.displayJobs()
		if len(result) != 1 {
			t.Fatalf("expected 1 job (invalid timestamp not filtered), got %d", len(result))
		}
	})
}

func TestHasBlocker(t *testing.T) {
	t.Parallel()

	t.Run("no blockers map entry", func(t *testing.T) {
		t.Parallel()
		m := Model{
			blockers: make(map[string]*job.Blocker),
		}
		j := job.Job{Frontmatter: job.Frontmatter{ID: "job-1"}}
		if m.hasBlocker(j) {
			t.Error("expected no blocker for job without entry")
		}
	})

	t.Run("nil blocker entry", func(t *testing.T) {
		t.Parallel()
		m := Model{
			blockers: map[string]*job.Blocker{
				"job-1": nil,
			},
		}
		j := job.Job{Frontmatter: job.Frontmatter{ID: "job-1"}}
		if m.hasBlocker(j) {
			t.Error("expected no blocker for nil entry")
		}
	})

	t.Run("answered blocker", func(t *testing.T) {
		t.Parallel()
		m := Model{
			blockers: map[string]*job.Blocker{
				"job-1": {Answered: true},
			},
		}
		j := job.Job{Frontmatter: job.Frontmatter{ID: "job-1"}}
		if m.hasBlocker(j) {
			t.Error("expected no blocker for answered blocker")
		}
	})

	t.Run("unanswered blocker", func(t *testing.T) {
		t.Parallel()
		m := Model{
			blockers: map[string]*job.Blocker{
				"job-1": {Answered: false},
			},
		}
		j := job.Job{Frontmatter: job.Frontmatter{ID: "job-1"}}
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
			jobs: []job.Job{
				{Frontmatter: job.Frontmatter{ID: "job-1", Name: "First"}},
				{Frontmatter: job.Frontmatter{ID: "job-2", Name: "Second"}},
			},
		}
		j, ok := m.jobByID("job-2")
		if !ok {
			t.Fatal("expected to find job-2")
		}
		if j.Name != "Second" {
			t.Errorf("got name %q, want %q", j.Name, "Second")
		}
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		m := Model{
			jobs: []job.Job{
				{Frontmatter: job.Frontmatter{ID: "job-1", Name: "First"}},
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
