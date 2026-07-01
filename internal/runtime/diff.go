package runtime

import (
	"strings"
	"unicode/utf8"

	"github.com/aymanbagabas/go-udiff"
)

// Diff size limits: the diff is a display side-channel, not something the
// LLM pays context for, but it still rides SSE frames and the TUI's event
// buffer, so it's capped defensively.
const (
	maxDiffLines = 120
	maxDiffBytes = 16 * 1024
)

// computeFileChange builds the FileChange for a single write_file/edit_file
// mutation. It returns the zero FileChange when oldContent == newContent,
// signaling the caller to skip notification entirely (no-op writes are not
// display-worthy).
func computeFileChange(toolName, path, oldContent, newContent string, created bool) FileChange {
	if oldContent == newContent {
		return FileChange{}
	}

	full := udiff.Unified(path, path, oldContent, newContent)
	body := stripUnifiedFileHeader(full)
	added, removed := countDiffLines(body)
	diff, truncated := capDiff(body, maxDiffLines, maxDiffBytes)

	return FileChange{
		ToolName:  toolName,
		Path:      path,
		Diff:      diff,
		Added:     added,
		Removed:   removed,
		Created:   created,
		Truncated: truncated,
	}
}

// stripUnifiedFileHeader removes the "--- from" / "+++ to" file-header lines
// that udiff.Unified prepends, leaving only hunk headers ("@@ ... @@") and
// hunk bodies.
func stripUnifiedFileHeader(diff string) string {
	lines := strings.SplitAfter(diff, "\n")
	if len(lines) >= 2 && strings.HasPrefix(lines[0], "--- ") && strings.HasPrefix(lines[1], "+++ ") {
		lines = lines[2:]
	}
	return strings.Join(lines, "")
}

// countDiffLines counts added/removed lines across the full (uncapped) diff
// body. Hunk headers and the "\ No newline at end of file" marker start with
// neither '+' nor '-' and are excluded naturally.
func countDiffLines(body string) (added, removed int) {
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

// capDiff truncates body to at most maxLines lines or maxBytes bytes,
// whichever limit is hit first, cutting at a line boundary.
func capDiff(body string, maxLines, maxBytes int) (diff string, truncated bool) {
	lines := strings.SplitAfter(body, "\n")
	// SplitAfter leaves a trailing "" element when body ends in "\n" (the
	// normal case here); drop it so it isn't mistaken for a skipped line.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	var b strings.Builder
	for i, line := range lines {
		if i >= maxLines || b.Len()+len(line) > maxBytes {
			if b.Len() == 0 && line != "" {
				// The very first line alone exceeds maxBytes. Returning ""
				// here would render as "diff truncated" with nothing to show;
				// hard-truncate the line instead so the UI displays something.
				return truncateBytesRuneSafe(line, maxBytes), true
			}
			return b.String(), true
		}
		b.WriteString(line)
	}
	return b.String(), false
}

// truncateBytesRuneSafe cuts s to at most maxBytes bytes, backing off to the
// nearest earlier rune boundary so a multi-byte UTF-8 sequence is never split.
func truncateBytesRuneSafe(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
