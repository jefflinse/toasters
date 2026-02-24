package tui

import (
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  int
	}{
		{
			name:  "empty string",
			input: "",
			want:  0,
		},
		{
			name:  "single character",
			input: "a",
			want:  1, // (1+3)/4 = 1
		},
		{
			name:  "four characters is one token",
			input: "abcd",
			want:  1, // (4+3)/4 = 1
		},
		{
			name:  "five characters rounds up",
			input: "abcde",
			want:  2, // (5+3)/4 = 2
		},
		{
			name:  "eight characters is two tokens",
			input: "abcdefgh",
			want:  2, // (8+3)/4 = 2
		},
		{
			name:  "short sentence",
			input: "hello world",
			want:  3, // (11+3)/4 = 3
		},
		{
			name:  "longer text",
			input: "The quick brown fox jumps over the lazy dog",
			want:  11, // (43+3)/4 = 11
		},
		{
			name:  "100 characters",
			input: strings.Repeat("a", 100),
			want:  25, // (100+3)/4 = 25
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := estimateTokens(tt.input)
			if got != tt.want {
				t.Errorf("estimateTokens(%q) = %d, want %d (len=%d)", tt.input, got, tt.want, len(tt.input))
			}
		})
	}
}

func TestEstimateTokens_CeilingDivision(t *testing.T) {
	t.Parallel()

	// Verify the ceiling division property: result * 4 >= len(s) for all non-empty strings.
	for length := 1; length <= 20; length++ {
		s := make([]byte, length)
		for i := range s {
			s[i] = 'x'
		}
		tokens := estimateTokens(string(s))
		if tokens*4 < length {
			t.Errorf("len=%d: tokens=%d, but tokens*4=%d < len", length, tokens, tokens*4)
		}
		// Also verify it's the ceiling: (tokens-1)*4 < length.
		if tokens > 0 && (tokens-1)*4 >= length {
			t.Errorf("len=%d: tokens=%d is not the ceiling, (tokens-1)*4=%d >= len", length, tokens, (tokens-1)*4)
		}
	}
}
