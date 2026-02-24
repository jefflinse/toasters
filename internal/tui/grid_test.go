package tui

import (
	"strings"
	"testing"
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
