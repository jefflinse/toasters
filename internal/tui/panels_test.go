package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestLeftPanelWidth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		termWidth int
		want      int
	}{
		{
			name:      "wide terminal",
			termWidth: 200,
			want:      50, // 200/4 = 50
		},
		{
			name:      "medium terminal",
			termWidth: 120,
			want:      30, // 120/4 = 30
		},
		{
			name:      "narrow terminal clamps to minimum",
			termWidth: 40,
			want:      minLeftPanelWidth, // 40/4 = 10 < 22
		},
		{
			name:      "very narrow terminal clamps to minimum",
			termWidth: 10,
			want:      minLeftPanelWidth, // 10/4 = 2 < 22
		},
		{
			name:      "zero terminal width clamps to minimum",
			termWidth: 0,
			want:      minLeftPanelWidth, // 0/4 = 0 < 22
		},
		{
			name:      "terminal width exactly at 4x minimum",
			termWidth: minLeftPanelWidth * 4,
			want:      minLeftPanelWidth, // 88/4 = 22 == minLeftPanelWidth
		},
		{
			name:      "terminal width just above 4x minimum",
			termWidth: minLeftPanelWidth*4 + 4,
			want:      minLeftPanelWidth + 1, // (88+4)/4 = 23
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := leftPanelWidth(tt.termWidth)
			if got != tt.want {
				t.Errorf("leftPanelWidth(%d) = %d, want %d", tt.termWidth, got, tt.want)
			}
		})
	}
}

func TestSidebarWidth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		termWidth int
		want      int
	}{
		{
			name:      "wide terminal",
			termWidth: 240,
			want:      40, // 240/6 = 40
		},
		{
			name:      "medium terminal",
			termWidth: 180,
			want:      30, // 180/6 = 30
		},
		{
			name:      "narrow terminal clamps to minimum",
			termWidth: 60,
			want:      minLeftPanelWidth, // 60/6 = 10 < 22
		},
		{
			name:      "very narrow terminal clamps to minimum",
			termWidth: 10,
			want:      minLeftPanelWidth, // 10/6 = 1 < 22
		},
		{
			name:      "zero terminal width clamps to minimum",
			termWidth: 0,
			want:      minLeftPanelWidth, // 0/6 = 0 < 22
		},
		{
			name:      "terminal width exactly at 6x minimum",
			termWidth: minLeftPanelWidth * 6,
			want:      minLeftPanelWidth, // 132/6 = 22 == minLeftPanelWidth
		},
		{
			name:      "terminal width just above 6x minimum",
			termWidth: minLeftPanelWidth*6 + 6,
			want:      minLeftPanelWidth + 1, // (132+6)/6 = 23
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sidebarWidth(tt.termWidth)
			if got != tt.want {
				t.Errorf("sidebarWidth(%d) = %d, want %d", tt.termWidth, got, tt.want)
			}
		})
	}
}

func TestSidebarRow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		label string
		value string
		check func(t *testing.T, result string)
	}{
		{
			name:  "basic label and value",
			label: "Messages",
			value: "42",
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "Messages") {
					t.Errorf("result should contain 'Messages', got %q", result)
				}
				if !strings.Contains(result, "42") {
					t.Errorf("result should contain '42', got %q", result)
				}
				if !strings.HasSuffix(result, "\n") {
					t.Errorf("result should end with newline, got %q", result)
				}
			},
		},
		{
			name:  "empty label and value",
			label: "",
			value: "",
			check: func(t *testing.T, result string) {
				// Should not panic and should end with newline.
				if !strings.HasSuffix(result, "\n") {
					t.Errorf("result should end with newline, got %q", result)
				}
			},
		},
		{
			name:  "long label",
			label: "Very Long Label",
			value: "100",
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "Very Long Label") {
					t.Errorf("result should contain label, got %q", result)
				}
				if !strings.Contains(result, "100") {
					t.Errorf("result should contain value, got %q", result)
				}
			},
		},
		{
			name:  "special characters in value",
			label: "Speed",
			value: "12.5 t/s",
			check: func(t *testing.T, result string) {
				if !strings.Contains(result, "Speed") {
					t.Errorf("result should contain 'Speed', got %q", result)
				}
				if !strings.Contains(result, "12.5 t/s") {
					t.Errorf("result should contain '12.5 t/s', got %q", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := sidebarRow(tt.label, tt.value)
			tt.check(t, result)
		})
	}
}

func TestEffectiveLeftPanelWidth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		termWidth     int
		widthOverride int
		want          int
	}{
		{
			name:          "no override returns default computed width",
			termWidth:     200,
			widthOverride: 0,
			want:          50, // leftPanelWidth(200) = 200/4 = 50
		},
		{
			name:          "no override with narrow terminal clamps to minimum",
			termWidth:     40,
			widthOverride: 0,
			want:          minLeftPanelWidth, // 40/4 = 10 < 22
		},
		{
			name:          "override respected when within bounds",
			termWidth:     200,
			widthOverride: 40,
			want:          40,
		},
		{
			name:          "override clamped to minimum",
			termWidth:     200,
			widthOverride: 5,
			want:          minLeftPanelWidth,
		},
		{
			name:          "override clamped to half terminal width",
			termWidth:     60,
			widthOverride: 50,
			want:          30, // 60/2 = 30
		},
		{
			name:          "override exactly at minimum",
			termWidth:     200,
			widthOverride: minLeftPanelWidth,
			want:          minLeftPanelWidth,
		},
		{
			name:          "override exactly at max (half terminal)",
			termWidth:     100,
			widthOverride: 50,
			want:          50, // 100/2 = 50
		},
		{
			name:          "override above max clamped",
			termWidth:     100,
			widthOverride: 80,
			want:          50, // 100/2 = 50
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.width = tt.termWidth
			m.leftPanelWidthOverride = tt.widthOverride

			got := m.effectiveLeftPanelWidth()
			if got != tt.want {
				t.Errorf("effectiveLeftPanelWidth() = %d, want %d (termWidth=%d, override=%d)",
					got, tt.want, tt.termWidth, tt.widthOverride)
			}
		})
	}
}

func TestRenderPromptWidget(t *testing.T) {
	t.Parallel()

	t.Run("option selection mode shows numbered options", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.width = 120
		m.height = 40
		m.promptMode = true
		m.promptCustom = false
		m.promptQuestion = "Which team should handle this?"
		m.promptOptions = []string{"Team Alpha", "Team Beta"}
		m.promptSelected = 0

		result := m.renderPromptWidget(80, lipgloss.NewStyle())

		// Should contain the question.
		if !strings.Contains(result, "Which team should handle this?") {
			t.Error("expected prompt widget to contain the question")
		}
		// Should contain numbered options.
		if !strings.Contains(result, "1.") {
			t.Error("expected prompt widget to contain option number 1")
		}
		if !strings.Contains(result, "Team Alpha") {
			t.Error("expected prompt widget to contain 'Team Alpha'")
		}
		if !strings.Contains(result, "2.") {
			t.Error("expected prompt widget to contain option number 2")
		}
		if !strings.Contains(result, "Team Beta") {
			t.Error("expected prompt widget to contain 'Team Beta'")
		}
		// Should contain the "Custom response..." option.
		if !strings.Contains(result, "Custom response...") {
			t.Error("expected prompt widget to contain 'Custom response...'")
		}
		// Should contain navigation hint.
		if !strings.Contains(result, "navigate") {
			t.Error("expected prompt widget to contain navigation hint")
		}
	})

	t.Run("option selection mode with cursor on second option", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.width = 120
		m.height = 40
		m.promptMode = true
		m.promptCustom = false
		m.promptQuestion = "Pick one"
		m.promptOptions = []string{"Option A", "Option B"}
		m.promptSelected = 1

		result := m.renderPromptWidget(80, lipgloss.NewStyle())

		// Both options should be present.
		if !strings.Contains(result, "Option A") {
			t.Error("expected 'Option A' in output")
		}
		if !strings.Contains(result, "Option B") {
			t.Error("expected 'Option B' in output")
		}
	})

	t.Run("custom text mode shows question and submit hint", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.width = 120
		m.height = 40
		m.promptMode = true
		m.promptCustom = true
		m.promptQuestion = "Enter your custom response"

		result := m.renderPromptWidget(80, InputAreaStyle)

		// Should contain the question.
		if !strings.Contains(result, "Enter your custom response") {
			t.Error("expected prompt widget to contain the question")
		}
		// Should contain submit hint.
		if !strings.Contains(result, "Enter to submit") {
			t.Error("expected prompt widget to contain submit hint")
		}
	})

	t.Run("option selection with no options shows only custom response", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.width = 120
		m.height = 40
		m.promptMode = true
		m.promptCustom = false
		m.promptQuestion = "No options"
		m.promptOptions = nil
		m.promptSelected = 0

		result := m.renderPromptWidget(80, lipgloss.NewStyle())

		// Should still contain "Custom response..." as the only option.
		if !strings.Contains(result, "Custom response...") {
			t.Error("expected 'Custom response...' even with no options")
		}
		if !strings.Contains(result, "1.") {
			t.Error("expected option number 1 for 'Custom response...'")
		}
	})
}
