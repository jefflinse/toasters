package agentfmt_test

import (
	"testing"

	"github.com/jefflinse/toasters/internal/agentfmt"
)

func TestNormalizeColor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Named colors
		{name: "red", input: "red", want: "#FF0000"},
		{name: "blue", input: "blue", want: "#0000FF"},
		{name: "green", input: "green", want: "#00FF00"},
		{name: "yellow", input: "yellow", want: "#FFFF00"},
		{name: "orange", input: "orange", want: "#FF8C00"},
		{name: "purple", input: "purple", want: "#800080"},
		{name: "cyan", input: "cyan", want: "#00FFFF"},
		{name: "magenta", input: "magenta", want: "#FF00FF"},
		{name: "white", input: "white", want: "#FFFFFF"},
		{name: "black", input: "black", want: "#000000"},
		{name: "pink", input: "pink", want: "#FFC0CB"},
		{name: "gray", input: "gray", want: "#808080"},
		{name: "grey", input: "grey", want: "#808080"},

		// Case insensitive
		{name: "uppercase RED", input: "RED", want: "#FF0000"},
		{name: "mixed case Blue", input: "Blue", want: "#0000FF"},

		// Hex passthrough
		{name: "hex 6-digit", input: "#FF9800", want: "#FF9800"},
		{name: "hex 3-digit", input: "#F00", want: "#F00"},
		{name: "hex lowercase", input: "#abcdef", want: "#abcdef"},
		{name: "hex mixed case", input: "#AbCdEf", want: "#AbCdEf"},

		// Invalid
		{name: "empty string", input: "", want: ""},
		{name: "whitespace only", input: "   ", want: ""},
		{name: "unknown color name", input: "chartreuse", want: ""},
		{name: "invalid hex too short", input: "#FF", want: ""},
		{name: "invalid hex too long", input: "#FF00FF00", want: ""},
		{name: "invalid hex no hash", input: "FF9800", want: ""},
		{name: "invalid hex bad chars", input: "#GGHHII", want: ""},

		// Whitespace trimming
		{name: "leading space", input: "  red", want: "#FF0000"},
		{name: "trailing space", input: "red  ", want: "#FF0000"},
		{name: "hex with spaces", input: "  #FF9800  ", want: "#FF9800"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := agentfmt.NormalizeColor(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeColor(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
