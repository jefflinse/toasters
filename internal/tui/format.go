package tui

import "fmt"

// compactNum formats an integer as a compact human-readable string.
// e.g. 1500 → "1.5k", 12000 → "12k", 1500000 → "1.5M"
func compactNum(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
