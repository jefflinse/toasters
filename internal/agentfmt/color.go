package agentfmt

import (
	"regexp"
	"strings"
)

// namedColors maps lowercase color names to hex values.
var namedColors = map[string]string{
	"red":     "#FF0000",
	"blue":    "#0000FF",
	"green":   "#00FF00",
	"yellow":  "#FFFF00",
	"orange":  "#FF8C00",
	"purple":  "#800080",
	"cyan":    "#00FFFF",
	"magenta": "#FF00FF",
	"white":   "#FFFFFF",
	"black":   "#000000",
	"pink":    "#FFC0CB",
	"gray":    "#808080",
	"grey":    "#808080",
}

// hexColorRe matches #RGB or #RRGGBB hex color strings.
var hexColorRe = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}){1,2}$`)

// NormalizeColor converts named colors to hex format.
// Supports: red, blue, green, yellow, orange, purple, cyan, magenta, white,
// black, pink, gray/grey. Hex colors (#RRGGBB or #RGB) are passed through.
// Returns empty string for unrecognized values.
func NormalizeColor(color string) string {
	color = strings.TrimSpace(color)
	if color == "" {
		return ""
	}

	// Check hex passthrough.
	if hexColorRe.MatchString(color) {
		return color
	}

	// Check named colors (case-insensitive).
	if hex, ok := namedColors[strings.ToLower(color)]; ok {
		return hex
	}

	return ""
}
