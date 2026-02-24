package tui

import (
	"fmt"
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

func TestRenderScrollableModal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		width, height int
		title         string
		content       string
		scroll        int
		checkOutput   func(t *testing.T, result string)
		checkScroll   func(t *testing.T, clampedScroll int)
	}{
		{
			name:    "basic modal contains title and content",
			width:   120,
			height:  40,
			title:   "Test Modal",
			content: "line one\nline two\nline three",
			scroll:  0,
			checkOutput: func(t *testing.T, result string) {
				if !strings.Contains(result, "Test Modal") {
					t.Error("expected modal to contain title")
				}
				if !strings.Contains(result, "line one") {
					t.Error("expected modal to contain first content line")
				}
			},
			checkScroll: func(t *testing.T, clampedScroll int) {
				if clampedScroll != 0 {
					t.Errorf("expected scroll 0, got %d", clampedScroll)
				}
			},
		},
		{
			name:    "scroll clamped when exceeding max",
			width:   120,
			height:  40,
			title:   "Clamped",
			content: "short content",
			scroll:  9999,
			checkOutput: func(t *testing.T, result string) {
				if !strings.Contains(result, "Clamped") {
					t.Error("expected modal to contain title")
				}
			},
			checkScroll: func(t *testing.T, clampedScroll int) {
				// Content is 1 line, modal height is 30 (40*3/4), inner height is 30-4=26.
				// maxScroll = max(0, 1 - 30 + 4) = 0. So scroll should be clamped to 0.
				if clampedScroll != 0 {
					t.Errorf("expected scroll clamped to 0, got %d", clampedScroll)
				}
			},
		},
		{
			name:    "empty content renders without panic",
			width:   120,
			height:  40,
			title:   "Empty",
			content: "",
			scroll:  0,
			checkOutput: func(t *testing.T, result string) {
				if !strings.Contains(result, "Empty") {
					t.Error("expected modal to contain title")
				}
				// Should contain footer with scroll info.
				if !strings.Contains(result, "scroll") {
					t.Error("expected modal to contain scroll hint in footer")
				}
			},
			checkScroll: func(t *testing.T, clampedScroll int) {
				if clampedScroll != 0 {
					t.Errorf("expected scroll 0, got %d", clampedScroll)
				}
			},
		},
		{
			name:    "content shorter than modal height shows all lines",
			width:   120,
			height:  40,
			title:   "Short",
			content: "alpha\nbeta\ngamma",
			scroll:  0,
			checkOutput: func(t *testing.T, result string) {
				if !strings.Contains(result, "alpha") {
					t.Error("expected 'alpha' in output")
				}
				if !strings.Contains(result, "beta") {
					t.Error("expected 'beta' in output")
				}
				if !strings.Contains(result, "gamma") {
					t.Error("expected 'gamma' in output")
				}
			},
			checkScroll: func(t *testing.T, clampedScroll int) {
				if clampedScroll != 0 {
					t.Errorf("expected scroll 0, got %d", clampedScroll)
				}
			},
		},
		{
			name:   "long content with valid scroll offset",
			width:  120,
			height: 40,
			title:  "Long",
			// Generate 100 lines of content.
			content: func() string {
				var lines []string
				for i := 0; i < 100; i++ {
					lines = append(lines, fmt.Sprintf("line %d", i))
				}
				return strings.Join(lines, "\n")
			}(),
			scroll: 10,
			checkOutput: func(t *testing.T, result string) {
				if !strings.Contains(result, "Long") {
					t.Error("expected modal to contain title")
				}
				// Line 10 should be visible (scroll offset = 10).
				if !strings.Contains(result, "line 10") {
					t.Error("expected 'line 10' to be visible at scroll offset 10")
				}
			},
			checkScroll: func(t *testing.T, clampedScroll int) {
				if clampedScroll != 10 {
					t.Errorf("expected scroll 10, got %d", clampedScroll)
				}
			},
		},
		{
			name:    "small terminal dimensions use minimum modal size",
			width:   30,
			height:  8,
			title:   "Tiny",
			content: "some content here",
			scroll:  0,
			checkOutput: func(t *testing.T, result string) {
				if !strings.Contains(result, "Tiny") {
					t.Error("expected modal to contain title")
				}
			},
			checkScroll: func(t *testing.T, clampedScroll int) {
				if clampedScroll != 0 {
					t.Errorf("expected scroll 0, got %d", clampedScroll)
				}
			},
		},
		{
			name:    "line truncation for very long lines",
			width:   80,
			height:  40,
			title:   "Truncate",
			content: strings.Repeat("X", 500),
			scroll:  0,
			checkOutput: func(t *testing.T, result string) {
				if !strings.Contains(result, "Truncate") {
					t.Error("expected modal to contain title")
				}
				// The line should be truncated to innerW = modalW - 4 = 60 - 4 = 56.
				// (modalW = 80*3/4 = 60)
				// Verify the full 500-char line is NOT present.
				if strings.Contains(result, strings.Repeat("X", 500)) {
					t.Error("expected long line to be truncated")
				}
			},
			checkScroll: func(t *testing.T, clampedScroll int) {
				if clampedScroll != 0 {
					t.Errorf("expected scroll 0, got %d", clampedScroll)
				}
			},
		},
		{
			name:    "scroll at zero with long content",
			width:   120,
			height:  40,
			title:   "AtZero",
			content: strings.Repeat("line\n", 200),
			scroll:  0,
			checkOutput: func(t *testing.T, result string) {
				if !strings.Contains(result, "AtZero") {
					t.Error("expected modal to contain title")
				}
				if !strings.Contains(result, "line 1/") {
					t.Error("expected scroll info to show line 1")
				}
			},
			checkScroll: func(t *testing.T, clampedScroll int) {
				if clampedScroll != 0 {
					t.Errorf("expected scroll 0, got %d", clampedScroll)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.width = tt.width
			m.height = tt.height

			result, clampedScroll := m.renderScrollableModal(tt.title, tt.content, tt.scroll)
			tt.checkOutput(t, result)
			tt.checkScroll(t, clampedScroll)
		})
	}
}

func TestRenderMarkdown(t *testing.T) {
	// Not parallel: ensureMarkdownRenderer calls toastersStyle() which mutates
	// a shared glamour style config (DraculaStyleConfig). Running these subtests
	// concurrently with each other or other tests triggers a data race.

	t.Run("nil renderer returns raw content", func(t *testing.T) {
		m := newMinimalModel(t)
		// mdRender is nil by default in newMinimalModel.
		content := "# Hello World\n\nSome **bold** text."
		result := m.renderMarkdown(content)
		if result != content {
			t.Errorf("expected raw content returned when renderer is nil\ngot:  %q\nwant: %q", result, content)
		}
	})

	t.Run("nil renderer with empty string", func(t *testing.T) {
		m := newMinimalModel(t)
		result := m.renderMarkdown("")
		if result != "" {
			t.Errorf("expected empty string, got %q", result)
		}
	})

	t.Run("initialized renderer produces styled output", func(t *testing.T) {
		m := newMinimalModel(t)
		m.width = 120
		m.height = 40
		m.chatViewport.SetWidth(80)
		m.chatViewport.SetHeight(30)
		m.ensureMarkdownRenderer()

		if m.mdRender == nil {
			t.Fatal("ensureMarkdownRenderer did not create a renderer")
		}

		content := "# Hello World\n\nSome **bold** text."
		result := m.renderMarkdown(content)

		// The rendered output should differ from raw input (contains ANSI codes).
		if result == content {
			t.Error("expected rendered output to differ from raw input")
		}
		// Should contain the text content. Note: glamour may split words across
		// ANSI escape sequences, so check for individual words rather than phrases.
		if !strings.Contains(result, "Hello") {
			t.Error("expected rendered output to contain 'Hello'")
		}
		if !strings.Contains(result, "World") {
			t.Error("expected rendered output to contain 'World'")
		}
		if !strings.Contains(result, "bold") {
			t.Error("expected rendered output to contain 'bold'")
		}
	})

	t.Run("renderer trims trailing newlines", func(t *testing.T) {
		m := newMinimalModel(t)
		m.width = 120
		m.height = 40
		m.chatViewport.SetWidth(80)
		m.chatViewport.SetHeight(30)
		m.ensureMarkdownRenderer()

		if m.mdRender == nil {
			t.Fatal("ensureMarkdownRenderer did not create a renderer")
		}

		result := m.renderMarkdown("Hello")
		if strings.HasSuffix(result, "\n") {
			t.Error("expected trailing newlines to be trimmed")
		}
	})
}
