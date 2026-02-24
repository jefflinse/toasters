package tui

import (
	"strings"
	"testing"
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
