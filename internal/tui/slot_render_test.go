package tui

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
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

// TestParseDiffHunks verifies hunk-header parsing tracks old/new line
// counters correctly, including the comma-less single-line hunk form
// ("@@ -5 +5 @@" means count 1, not "parse error").
func TestParseDiffHunks(t *testing.T) {
	diff := strings.Join([]string{
		"@@ -1,3 +1,4 @@",
		" package foo",
		"-var x = 1",
		"+var x = 2",
		"+var y = 3",
		" ",
		"@@ -10 +11 @@",
		"-old solo line",
	}, "\n")

	rows := parseDiffHunks(diff)

	var got []diffRenderLine
	for _, r := range rows {
		got = append(got, r)
	}
	want := []diffRenderLine{
		{marker: '@'},
		{marker: ' ', num: 1, code: "package foo"},
		{marker: '-', num: 2, code: "var x = 1"},
		{marker: '+', num: 2, code: "var x = 2"},
		{marker: '+', num: 3, code: "var y = 3"},
		{marker: ' ', num: 4, code: ""},
		{marker: '@'},
		{marker: '-', num: 10, code: "old solo line"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestRenderDiffLines verifies the rendered output carries the right line
// numbers (gutter tracks old-file numbers for removals, new-file numbers for
// context/additions), skips raw "@@" headers in favor of a separator, and
// truncates long lines to the given width instead of letting them overflow.
func TestRenderDiffLines(t *testing.T) {
	diff := strings.Join([]string{
		"@@ -1,2 +1,3 @@",
		" package foo",
		"-var x = 1",
		"+var x = 2",
		"+var y = 3",
	}, "\n")

	out := renderDiffLines(diff, 40)
	if out == "" {
		t.Fatal("expected non-empty diff render")
	}
	if strings.Contains(out, "@@") {
		t.Errorf("raw hunk header leaked into render: %q", out)
	}
	if !strings.Contains(out, "···") {
		t.Errorf("expected a dim hunk separator, got %q", out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 5 { // separator + 4 diff rows
		t.Fatalf("got %d rendered lines, want 5: %q", len(lines), lines)
	}
	// The leading context line is new-file line 1; the first "+" line comes
	// after the removed line consumed old-file line 2, so it's new-file
	// line 2. Spot-check the digits are present in the gutter.
	if !strings.Contains(lines[1], "1") {
		t.Errorf("context line missing new-file number 1: %q", lines[1])
	}
	if !strings.Contains(lines[3], "2") {
		t.Errorf("added line missing new-file number 2: %q", lines[3])
	}

	// A long code line must not exceed the requested width.
	longDiff := "@@ -1,1 +1,1 @@\n+" + strings.Repeat("x", 200)
	longOut := renderDiffLines(longDiff, 30)
	for _, ln := range strings.Split(longOut, "\n") {
		if n := len([]rune(xansi.Strip(ln))); n > 30 {
			t.Errorf("rendered line exceeds width 30: %d runes: %q", n, ln)
		}
	}
}

// TestRenderDiffLines_Empty verifies degenerate inputs render nothing rather
// than panicking.
func TestRenderDiffLines_Empty(t *testing.T) {
	if got := renderDiffLines("", 40); got != "" {
		t.Errorf("empty diff should render empty, got %q", got)
	}
	if got := renderDiffLines("@@ -1,1 +1,1 @@\n+x", 2); got != "" {
		t.Errorf("width too small should render empty, got %q", got)
	}
}
