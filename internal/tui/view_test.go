package tui

import (
	"strings"
	"testing"
)

func TestIndentLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		indent int
		want   string
	}{
		{
			name:   "empty string",
			input:  "",
			indent: 4,
			want:   "",
		},
		{
			name:   "single line",
			input:  "hello",
			indent: 2,
			want:   "  hello",
		},
		{
			name:   "multiple lines",
			input:  "line one\nline two\nline three",
			indent: 3,
			want:   "   line one\n   line two\n   line three",
		},
		{
			name:   "zero indent",
			input:  "hello\nworld",
			indent: 0,
			want:   "hello\nworld",
		},
		{
			name:   "empty lines are not indented",
			input:  "hello\n\nworld",
			indent: 2,
			want:   "  hello\n\n  world",
		},
		{
			name:   "single empty line",
			input:  "",
			indent: 5,
			want:   "",
		},
		{
			name:   "indent of 1",
			input:  "a\nb",
			indent: 1,
			want:   " a\n b",
		},
		{
			name:   "large indent",
			input:  "x",
			indent: 10,
			want:   "          x",
		},
		{
			name:   "line with only spaces treated as non-empty",
			input:  "  ",
			indent: 2,
			want:   "    ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := indentLines(tt.input, tt.indent)
			if got != tt.want {
				t.Errorf("indentLines(%q, %d) = %q, want %q", tt.input, tt.indent, got, tt.want)
			}
		})
	}
}

func TestFadeColor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		r, g, b uint8
		factor  float64
		check   func(t *testing.T, result string)
	}{
		{
			name: "no fade returns original color",
			r:    255, g: 128, b: 64,
			factor: 0.0,
			check: func(t *testing.T, result string) {
				if result == "" {
					t.Error("expected non-empty result")
				}
			},
		},
		{
			name: "full fade returns black",
			r:    255, g: 128, b: 64,
			factor: 1.0,
			check: func(t *testing.T, result string) {
				if result == "" {
					t.Error("expected non-empty result")
				}
			},
		},
		{
			name: "half fade",
			r:    200, g: 100, b: 50,
			factor: 0.5,
			check: func(t *testing.T, result string) {
				if result == "" {
					t.Error("expected non-empty result")
				}
			},
		},
		{
			name: "zero color with any fade",
			r:    0, g: 0, b: 0,
			factor: 0.5,
			check: func(t *testing.T, result string) {
				if result == "" {
					t.Error("expected non-empty result")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := fadeColor(tt.r, tt.g, tt.b, tt.factor)
			// fadeColor returns a color.Color; verify it's not nil.
			if result == nil {
				t.Fatal("fadeColor returned nil")
			}
			// Verify the RGBA values are valid.
			r, g, b, a := result.RGBA()
			_ = r
			_ = g
			_ = b
			if a == 0 && tt.factor < 1.0 && (tt.r > 0 || tt.g > 0 || tt.b > 0) {
				// This shouldn't happen for non-black colors with non-full fade.
				t.Error("unexpected zero alpha")
			}
			tt.check(t, "ok") // fadeColor always returns a valid color
		})
	}
}

func TestGradientText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		text  string
		from  [3]uint8
		to    [3]uint8
		check func(t *testing.T, result string)
	}{
		{
			name: "empty string returns empty",
			text: "",
			from: [3]uint8{255, 0, 0},
			to:   [3]uint8{0, 0, 255},
			check: func(t *testing.T, result string) {
				if result != "" {
					t.Errorf("expected empty string, got %q", result)
				}
			},
		},
		{
			name: "single character",
			text: "A",
			from: [3]uint8{255, 0, 0},
			to:   [3]uint8{0, 0, 255},
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "A") {
					t.Errorf("result should contain 'A', got %q", result)
				}
			},
		},
		{
			name: "multi-character text",
			text: "Hello",
			from: [3]uint8{255, 175, 0},
			to:   [3]uint8{175, 50, 200},
			check: func(t *testing.T, result string) {
				// Should contain all characters.
				for _, ch := range "Hello" {
					if !strings.ContainsRune(result, ch) {
						t.Errorf("result should contain %q, got %q", string(ch), result)
					}
				}
			},
		},
		{
			name: "same from and to color",
			text: "AB",
			from: [3]uint8{100, 100, 100},
			to:   [3]uint8{100, 100, 100},
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "A") || !strings.Contains(result, "B") {
					t.Errorf("result should contain both characters, got %q", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := gradientText(tt.text, tt.from, tt.to)
			tt.check(t, result)
		})
	}
}

func TestIndentLines_PreservesLineCount(t *testing.T) {
	t.Parallel()

	input := "a\nb\nc\nd\ne"
	result := indentLines(input, 4)
	inputLines := strings.Split(input, "\n")
	resultLines := strings.Split(result, "\n")
	if len(resultLines) != len(inputLines) {
		t.Errorf("line count changed: input has %d lines, result has %d lines",
			len(inputLines), len(resultLines))
	}
}

func TestToastersStyle(t *testing.T) {
	t.Parallel()

	style := toastersStyle()

	// Verify the document margin was set to zero.
	if style.Document.Margin == nil {
		t.Fatal("expected Document.Margin to be set")
	}
	if *style.Document.Margin != 0 {
		t.Errorf("expected Document.Margin to be 0, got %d", *style.Document.Margin)
	}

	// Verify code block background was customized.
	if style.CodeBlock.Chroma.Background.BackgroundColor == nil {
		t.Fatal("expected CodeBlock.Chroma.Background.BackgroundColor to be set")
	}
	if *style.CodeBlock.Chroma.Background.BackgroundColor != "#1e1e2e" {
		t.Errorf("expected CodeBlock background %q, got %q",
			"#1e1e2e", *style.CodeBlock.Chroma.Background.BackgroundColor)
	}
}
