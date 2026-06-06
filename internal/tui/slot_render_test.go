package tui

import (
	"strings"
	"testing"
)

func TestTruncateMiddle(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},           // fits, unchanged
		{"abcdefghij", 10, "abcdefghij"}, // exactly fits
		{"abcdefghij", 5, "ab…ij"},       // middle elided, ends preserved
		{"", 5, ""},                      // empty
		{"abcdef", 0, ""},                // no room
		{"abcdef", 1, "…"},               // room only for the ellipsis
	}
	for _, c := range cases {
		got := truncateMiddle(c.in, c.max)
		if got != c.want {
			t.Errorf("truncateMiddle(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
		if c.max > 0 {
			if n := len([]rune(got)); n > c.max {
				t.Errorf("truncateMiddle(%q, %d) length %d exceeds max", c.in, c.max, n)
			}
		}
	}
}

// TestTruncateMiddle_PreservesFilename is the motivating case: a long temp path
// must keep its leading dirs and its filename rather than dropping the tail.
func TestTruncateMiddle_PreservesFilename(t *testing.T) {
	p := "/var/folders/gy/zd7sszzn5z182rmlhksvwj6m0000gn/T/toasters-fanout-2985464372/1/main.go"
	got := truncateMiddle(p, 40)
	if n := len([]rune(got)); n > 40 {
		t.Fatalf("length %d exceeds 40: %q", n, got)
	}
	if !strings.HasPrefix(got, "/var/") {
		t.Errorf("lost leading path context: %q", got)
	}
	if !strings.HasSuffix(got, "main.go") {
		t.Errorf("lost filename tail: %q", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("expected middle ellipsis: %q", got)
	}
}
