package tui

import (
	"testing"
)

// --- stripCodeFences tests ---

func TestStripCodeFences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no fences",
			input: "plain content",
			want:  "plain content",
		},
		{
			name:  "backtick fence no language",
			input: "```\ncontent\n```",
			want:  "content",
		},
		{
			name:  "backtick fence with yaml language",
			input: "```yaml\ncontent\n```",
			want:  "content",
		},
		{
			name:  "backtick fence with json language",
			input: "```json\n{\"key\": \"value\"}\n```",
			want:  "{\"key\": \"value\"}",
		},
		{
			name:  "leading and trailing whitespace stripped",
			input: "  \n  content  \n  ",
			want:  "content",
		},
		{
			name:  "opening fence only",
			input: "```yaml\ncontent without closing fence",
			want:  "content without closing fence",
		},
		{
			name:  "closing fence only",
			input: "content without opening fence\n```",
			want:  "content without opening fence",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "multiline content preserved",
			input: "```\nline1\nline2\nline3\n```",
			want:  "line1\nline2\nline3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stripCodeFences(tt.input)
			if got != tt.want {
				t.Errorf("stripCodeFences(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
