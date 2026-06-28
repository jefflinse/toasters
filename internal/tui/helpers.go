// Helpers: text utilities, scrollbar rendering, toast overlay, session management, and chat entry operations.
package tui

import (
	"fmt"
	"os"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// ChatEntry is a package-level alias for service.ChatEntry so that files not
// yet migrated to the service layer can continue to use the unqualified name.
type ChatEntry = service.ChatEntry

// wrapText wraps s to fit within maxWidth columns.
func wrapText(s string, maxWidth int) string {
	if maxWidth <= 0 {
		maxWidth = 40
	}

	var result strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if lipgloss.Width(line) <= maxWidth {
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(line)
			continue
		}

		// Word-wrap long lines.
		words := strings.Fields(line)
		var currentLine strings.Builder
		for _, word := range words {
			wordW := lipgloss.Width(word)
			currentW := lipgloss.Width(currentLine.String())

			if currentW == 0 {
				// Break very long words.
				for wordW > maxWidth {
					if result.Len() > 0 || currentLine.Len() > 0 {
						result.WriteString("\n")
					}
					// Take as many runes as fit.
					runes := []rune(word)
					cut := maxWidth
					if cut > len(runes) {
						cut = len(runes)
					}
					result.WriteString(string(runes[:cut]))
					word = string(runes[cut:])
					wordW = lipgloss.Width(word)
				}
				currentLine.WriteString(word)
			} else if currentW+1+wordW <= maxWidth {
				currentLine.WriteString(" ")
				currentLine.WriteString(word)
			} else {
				if result.Len() > 0 {
					result.WriteString("\n")
				}
				result.WriteString(currentLine.String())
				currentLine.Reset()
				currentLine.WriteString(word)
			}
		}
		if currentLine.Len() > 0 {
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(currentLine.String())
		}
	}

	return result.String()
}

// truncateStr truncates s to maxLen runes, adding "..." if truncated.
// Note: truncation is rune-based, not display-width-based. Characters with
// display width > 1 (e.g. CJK, emoji) may still overflow the visual budget.
func truncateStr(s string, maxLen int) string {
	if maxLen <= 3 {
		maxLen = 3
	}
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen-3]) + "..."
}

// firstLineOf returns the first non-empty line of s.
func firstLineOf(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return s
}

// renderCompletionBlock renders the full content of a completion message as a
// dimmed indented block, skipping the first line (already shown in the header).
func renderCompletionBlock(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(DimStyle.Render("  "+line) + "\n")
	}
	return sb.String()
}

// isToolCallIndicatorIdx reports whether the entry at index i is a tool-call indicator.
func (m *Model) isToolCallIndicatorIdx(i int) bool {
	if i < 0 || i >= len(m.chat.entries) {
		return false
	}
	return m.chat.entries[i].ClaudeMeta == "tool-call-indicator"
}

// extractToolName extracts the tool/function name from a tool-call indicator
// message like "⚙ calling `function_name`…". Returns the name or a fallback.
func extractToolName(content string) string {
	// Look for backtick-delimited name.
	start := strings.Index(content, "`")
	if start >= 0 {
		end := strings.Index(content[start+1:], "`")
		if end >= 0 {
			return content[start+1 : start+1+end]
		}
	}
	return "tool call"
}

// formatToolName formats a tool name for display. MCP-namespaced tools
// (containing "__") are rendered as "tool_name (via server)" instead of
// "server__tool_name". Built-in tool names are returned unchanged.
func formatToolName(name string) string {
	if parts := strings.SplitN(name, "__", 2); len(parts) == 2 {
		return fmt.Sprintf("%s (via %s)", parts[1], parts[0])
	}
	return name
}

// formatToolCallContent transforms the content of a tool-call indicator message
// so that any MCP-namespaced tool name inside backticks is displayed in the
// "tool_name (via server)" format. Non-MCP content is returned unchanged.
func formatToolCallContent(content string) string {
	start := strings.Index(content, "`")
	if start < 0 {
		return content
	}
	end := strings.Index(content[start+1:], "`")
	if end < 0 {
		return content
	}
	name := content[start+1 : start+1+end]
	formatted := formatToolName(name)
	if formatted == name {
		return content
	}
	return content[:start+1] + formatted + content[start+1+end:]
}

// renderScrollbar builds a vertical scrollbar string of the given height.
// The thumb position is determined by scrollPercent (0.0–1.0). This is meant
// to be placed alongside the viewport content via lipgloss.JoinHorizontal.
func renderScrollbar(viewportHeight int, totalLines int, scrollPercent float64) string {
	// Calculate thumb size: proportional to visible/total, minimum 2 lines.
	thumbSize := viewportHeight * viewportHeight / totalLines
	if thumbSize < 2 {
		thumbSize = 2
	}
	if thumbSize > viewportHeight {
		thumbSize = viewportHeight
	}

	// Calculate thumb position.
	trackSpace := viewportHeight - thumbSize
	thumbStart := int(scrollPercent * float64(trackSpace))
	if thumbStart < 0 {
		thumbStart = 0
	}
	if thumbStart > trackSpace {
		thumbStart = trackSpace
	}
	thumbEnd := thumbStart + thumbSize

	thumbStyle := lipgloss.NewStyle().Foreground(ColorPrimary)
	trackStyle := lipgloss.NewStyle().Foreground(ColorBorder)

	lines := make([]string, viewportHeight)
	for i := range viewportHeight {
		if i >= thumbStart && i < thumbEnd {
			lines[i] = thumbStyle.Render("█")
		} else {
			lines[i] = trackStyle.Render("░")
		}
	}
	return strings.Join(lines, "\n")
}

// renderToasts renders the toast notification stack as a single string block.
// Newest toasts appear at the top. maxOuterWidth caps the total rendered
// width (border + padding + content); messages shorter than that produce
// proportionally narrower toasts so the bubble grows with its content
// instead of always padding out to a fixed 40-cell box.
func (m *Model) renderToasts(maxOuterWidth int) string {
	if len(m.toasts) == 0 || maxOuterWidth <= 0 {
		return ""
	}
	// Outer = border (2) + padding (2) + inner content. Reserve the chrome
	// before deciding how much room the message itself gets.
	const chrome = 4
	innerBudget := maxOuterWidth - chrome
	if innerBudget < 1 {
		innerBudget = 1
	}
	var lines []string
	for i := len(m.toasts) - 1; i >= 0; i-- {
		t := m.toasts[i]
		msg := truncateToWidth(t.message, innerBudget)
		// Override MaxWidth per-render so wider terminals can show the full
		// message; the base style's MaxWidth was a hard 40 which clipped
		// every toast at the same point regardless of available room.
		var rendered string
		switch t.level {
		case toastSuccess:
			rendered = ToastSuccessStyle.MaxWidth(maxOuterWidth).Render(msg)
		case toastWarning:
			rendered = ToastWarningStyle.MaxWidth(maxOuterWidth).Render(msg)
		case toastError:
			rendered = ToastErrorStyle.MaxWidth(maxOuterWidth).Render(msg)
		default:
			rendered = ToastInfoStyle.MaxWidth(maxOuterWidth).Render(msg)
		}
		lines = append(lines, rendered)
	}
	return strings.Join(lines, "\n")
}

// truncateToWidth truncates s to fit within maxWidth display cells (using
// lipgloss.Width semantics: emoji = 2 cells, CJK = 2 cells, ANSI escapes = 0
// cells). If s already fits, it is returned unchanged. Otherwise the result
// is truncated and the trailing rune is replaced with "…", with the budget
// reserved for the ellipsis.
//
// Implementation note: this is a single O(n) pass over the runes of s,
// computing per-rune width via lipgloss.Width(string(r)) once. Previously this
// was an O(n²) loop that called lipgloss.Width on the whole string after
// removing one rune at a time, which made renderToasts catastrophically slow
// for any non-trivial message and could freeze the entire TUI by starving the
// Bubble Tea message loop. See investigation in cleanup/server-only.
func truncateToWidth(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	// Reserve 1 cell for the ellipsis itself ("…" is one cell).
	const ellipsis = "…"
	const ellipsisWidth = 1
	budget := maxWidth - ellipsisWidth
	if budget <= 0 {
		return ellipsis
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		// lipgloss.Width is the canonical authority on display width here so
		// our truncation matches what the renderer will measure.
		w := lipgloss.Width(string(r))
		if used+w > budget {
			break
		}
		b.WriteRune(r)
		used += w
	}
	b.WriteString(ellipsis)
	return b.String()
}

// overlayToasts splices the toast block so its right edge lands at column
// rightEdge (1-indexed cell count). The caller passes screenWidth so we can
// pad short lines when needed; any screen content past rightEdge is preserved
// so toasts don't paint over a sidebar.
func overlayToasts(screen string, toastBlock string, rightEdge, screenWidth int) string {
	if toastBlock == "" {
		return screen
	}
	if rightEdge > screenWidth {
		rightEdge = screenWidth
	}
	if rightEdge <= 0 {
		return screen
	}
	screenLines := strings.Split(screen, "\n")
	toastLines := strings.Split(toastBlock, "\n")

	for i, tl := range toastLines {
		if i >= len(screenLines) {
			break
		}
		toastW := lipgloss.Width(tl)

		// Toast wider than its budget — collapse to a full-line replacement
		// across the chat area so we don't crash into a sidebar.
		if toastW >= rightEdge {
			leftPart := takeVisible(screenLines[i], 0)
			_, rightPart := splitVisibleAt(screenLines[i], rightEdge)
			screenLines[i] = leftPart + tl + rightPart
			continue
		}

		// Pad the screen line out to screenWidth so we have material to
		// preserve to the right of the toast.
		screenLineW := lipgloss.Width(screenLines[i])
		if screenLineW < screenWidth {
			screenLines[i] = screenLines[i] + strings.Repeat(" ", screenWidth-screenLineW)
		}

		leftBudget := rightEdge - toastW
		leftPart := takeVisible(screenLines[i], leftBudget)
		_, rightPart := splitVisibleAt(screenLines[i], rightEdge)
		screenLines[i] = leftPart + tl + rightPart
	}
	return strings.Join(screenLines, "\n")
}

// takeVisible returns the prefix of line that occupies n visible cells,
// preserving ANSI escape sequences. If line has fewer than n visible cells
// the result is padded with spaces.
func takeVisible(line string, n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	visible := 0
	inEsc := false
	for _, r := range line {
		if r == '\x1b' {
			inEsc = true
			b.WriteRune(r)
			continue
		}
		if inEsc {
			b.WriteRune(r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
			continue
		}
		if visible >= n {
			break
		}
		b.WriteRune(r)
		visible++
	}
	for visible < n {
		b.WriteRune(' ')
		visible++
	}
	return b.String()
}

// splitVisibleAt splits line at the n-th visible cell. The left half holds
// the first n visible cells (no padding); the right half holds everything
// after, including any ANSI escape sequences in flight at the cut point so
// the right half renders with the same styling that was active.
func splitVisibleAt(line string, n int) (string, string) {
	if n <= 0 {
		return "", line
	}
	var left, right strings.Builder
	visible := 0
	inEsc := false
	cut := false
	for _, r := range line {
		if r == '\x1b' {
			inEsc = true
			if cut {
				right.WriteRune(r)
			} else {
				left.WriteRune(r)
			}
			continue
		}
		if inEsc {
			if cut {
				right.WriteRune(r)
			} else {
				left.WriteRune(r)
			}
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
			continue
		}
		if visible >= n {
			cut = true
		}
		if cut {
			right.WriteRune(r)
		} else {
			left.WriteRune(r)
			visible++
		}
	}
	return left.String(), right.String()
}

// truncateLeft trims the leading portion of s so that the result fits
// within maxWidth display cells, prefixing "…" when content was dropped.
// Used for paths where the meaningful tail (the last directory + files)
// is what the user wants to see.
func truncateLeft(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	const ellipsis = "…"
	budget := maxWidth - 1
	if budget <= 0 {
		return ellipsis
	}
	runes := []rune(s)
	// Walk from the right, accumulating until we'd exceed budget.
	used := 0
	cut := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		w := lipgloss.Width(string(runes[i]))
		if used+w > budget {
			break
		}
		used += w
		cut = i
	}
	return ellipsis + string(runes[cut:])
}

// contractHomeDir replaces a leading $HOME with "~" so paths read more
// naturally in the UI. Falls back to the original path when home isn't
// resolvable or doesn't prefix the input.
func contractHomeDir(path string) string {
	if path == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// wrapToWidth breaks s into lines no wider than width display cells,
// honoring word boundaries. Caps the result at maxLines to keep the
// failure section from overrunning the block.
func wrapToWidth(s string, width, maxLines int) []string {
	if width <= 0 || maxLines <= 0 {
		return nil
	}
	words := strings.Fields(s)
	var lines []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			lines = append(lines, cur.String())
			cur.Reset()
		}
	}
	for _, w := range words {
		if len(lines) >= maxLines {
			break
		}
		switch {
		case cur.Len() == 0:
			cur.WriteString(w)
		case lipgloss.Width(cur.String())+1+lipgloss.Width(w) > width:
			flush()
			cur.WriteString(w)
		default:
			cur.WriteByte(' ')
			cur.WriteString(w)
		}
	}
	flush()
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return lines
}
