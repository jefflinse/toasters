package tui

import (
	"strings"
	"testing"
	"time"

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

// TestParseDiffHunks_SkipsNoNewlineMarker verifies go-udiff's "\ No newline
// at end of file" marker line is skipped entirely — not rendered, and
// critically not counted against the old/new line counters. A buggy
// implementation that treats it as a context line would skew the line number
// of every row that follows it in the hunk.
func TestParseDiffHunks_SkipsNoNewlineMarker(t *testing.T) {
	diff := strings.Join([]string{
		"@@ -1,2 +1,2 @@",
		" context",
		"-old",
		`\ No newline at end of file`,
		"+new",
	}, "\n")

	rows := parseDiffHunks(diff)

	for _, r := range rows {
		if strings.Contains(r.code, "No newline") {
			t.Fatalf("marker line leaked into rows: %+v", rows)
		}
	}
	want := []diffRenderLine{
		{marker: '@'},
		{marker: ' ', num: 1, code: "context"},
		{marker: '-', num: 2, code: "old"},
		{marker: '+', num: 2, code: "new"},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row[%d] = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

// TestRenderDiffLines_SanitizesCodeLines verifies a diff code line carrying
// an embedded ANSI escape, a bare CR (CRLF file — go-udiff splits hunks on
// "\n" only, leaving "\r" attached), and a tab renders with the escape gone,
// no stray CR, and the tab expanded to 4 spaces.
func TestRenderDiffLines_SanitizesCodeLines(t *testing.T) {
	diff := "@@ -1,1 +1,1 @@\n+\x1b[31mred\r\ttext"

	out := renderDiffLines(diff, 60)

	// The render legitimately carries its own lipgloss/ANSI styling escapes,
	// so check for the specific injected escape rather than any ESC byte, and
	// strip styling before checking for the bare CR and tab expansion.
	if strings.Contains(out, "\x1b[31m") {
		t.Errorf("injected ANSI escape leaked into rendered diff: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Errorf("bare CR leaked into rendered diff: %q", out)
	}
	stripped := xansi.Strip(out)
	if !strings.Contains(stripped, "red    text") {
		t.Errorf("expected tab expanded to 4 spaces: %q", stripped)
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

func TestFormatShellDuration(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "0s"},
		{340, "340ms"},
		{999, "999ms"},
		{1200, "1.2s"},
		{65000, "1m5s"},
	}
	for _, c := range cases {
		if got := formatShellDuration(c.ms); got != c.want {
			t.Errorf("formatShellDuration(%d) = %q, want %q", c.ms, got, c.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{4300, "4.2 KB"},
		{1024 * 1024, "1.0 MB"},
	}
	for _, c := range cases {
		if got := formatBytes(c.n); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestRenderShellExecStatusLine_Success verifies a clean exit renders a
// checkmark with exit code, duration, and size.
func TestRenderShellExecStatusLine_Success(t *testing.T) {
	it := &outputItem{shellExitCode: 0, shellDurationMs: 1200, shellOutputBytes: 4300}
	out := xansi.Strip(renderShellExecStatusLine(it))
	if !strings.Contains(out, "✓ exit 0") {
		t.Errorf("expected success mark, got %q", out)
	}
	if !strings.Contains(out, "1.2s") || !strings.Contains(out, "4.2 KB") {
		t.Errorf("expected duration and size, got %q", out)
	}
}

// TestRenderShellExecStatusLine_TimedOut verifies the timeout case renders
// its own marker instead of an exit code (there isn't a meaningful one).
func TestRenderShellExecStatusLine_TimedOut(t *testing.T) {
	it := &outputItem{shellTimedOut: true, shellExitCode: -1}
	out := xansi.Strip(renderShellExecStatusLine(it))
	if !strings.Contains(out, "timed out") {
		t.Errorf("expected timeout marker, got %q", out)
	}
}

// TestRenderToolBlock_ShellExecFailure_NoContradictoryOkMark is the
// regression test for the bug where CoreTools.shell folds a nonzero exit
// into the result with a nil error: it.toolError is always false for a
// failed shell command, so the generic status line would render "✓ ok"
// directly above a "✗ exit 2" shell_exec line. renderToolBlock must use the
// shell_exec status in place of the generic one, not alongside it.
func TestRenderToolBlock_ShellExecFailure_NoContradictoryOkMark(t *testing.T) {
	now := time.Now()
	it := &outputItem{
		kind:             outputItemTool,
		toolName:         "shell",
		toolError:        false, // as CoreTools.shell actually reports it for a nonzero exit
		toolResult:       "boom\nexit status: exit status 2",
		startedAt:        now.Add(-1200 * time.Millisecond),
		endedAt:          now,
		hasShellExec:     true,
		shellExitCode:    2,
		shellDurationMs:  1200,
		shellOutputBytes: 4,
	}
	out := xansi.Strip(renderToolBlock(it, 80))

	if !strings.Contains(out, "✗ exit 2") {
		t.Errorf("expected the real exit code to be shown, got %q", out)
	}
	if strings.Contains(out, "✓ ok") {
		t.Errorf("rendered block contradicts itself (shows both success and failure): %q", out)
	}
}

// TestRenderToolBlock_ShellExecSuccess verifies a clean shell exit renders
// the exit/duration/size line instead of the generic "✓ ok · dur" line.
func TestRenderToolBlock_ShellExecSuccess(t *testing.T) {
	now := time.Now()
	it := &outputItem{
		kind:             outputItemTool,
		toolName:         "shell",
		startedAt:        now.Add(-100 * time.Millisecond),
		endedAt:          now,
		hasShellExec:     true,
		shellExitCode:    0,
		shellDurationMs:  100,
		shellOutputBytes: 5,
	}
	out := xansi.Strip(renderToolBlock(it, 80))

	if !strings.Contains(out, "✓ exit 0") {
		t.Errorf("expected the exit-code status line, got %q", out)
	}
}
