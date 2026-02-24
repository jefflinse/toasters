package tui

import "testing"

func TestCompactNum(t *testing.T) {
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
			name:  "small number",
			input: 42,
			want:  "42",
		},
		{
			name:  "just under 1000",
			input: 999,
			want:  "999",
		},
		{
			name:  "exactly 1000",
			input: 1000,
			want:  "1.0k",
		},
		{
			name:  "1500 shows decimal",
			input: 1500,
			want:  "1.5k",
		},
		{
			name:  "9999 shows decimal",
			input: 9999,
			want:  "10.0k",
		},
		{
			name:  "10000 drops decimal",
			input: 10000,
			want:  "10k",
		},
		{
			name:  "50000",
			input: 50000,
			want:  "50k",
		},
		{
			name:  "999999",
			input: 999999,
			want:  "999k",
		},
		{
			name:  "exactly 1 million",
			input: 1000000,
			want:  "1.0M",
		},
		{
			name:  "1.5 million",
			input: 1500000,
			want:  "1.5M",
		},
		{
			name:  "10 million",
			input: 10000000,
			want:  "10.0M",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := compactNum(tt.input)
			if got != tt.want {
				t.Errorf("compactNum(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
