package tui

import (
	"fmt"
	"log/slog"
	"strings"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// renderScrollableModal renders a scrollable modal overlay centered on the
// terminal. It computes dimensions, slices content into visible lines, applies
// the scroll offset, truncates lines to the inner width, and styles the box.
// It returns the fully rendered overlay string and the clamped scroll offset
// so the caller can write it back to the model field.
func (m *Model) renderScrollableModal(title, content string, scroll int) (string, int) {
	modalW := m.width * 3 / 4
	modalH := m.height * 3 / 4
	if modalW < 40 {
		modalW = 40
	}
	if modalH < 10 {
		modalH = 10
	}

	// Slice the content into lines, apply scroll offset.
	allLines := strings.Split(content, "\n")
	maxScroll := len(allLines) - modalH + 4
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}

	start := scroll
	end := start + modalH - 4 // -4 for title + footer + borders
	if end > len(allLines) {
		end = len(allLines)
	}
	visibleLines := allLines[start:end]

	// Wrap long lines to modal inner width instead of truncating.
	innerW := modalW - 4
	var wrapped []string
	for _, l := range visibleLines {
		if innerW > 0 && len(l) > innerW {
			for len(l) > innerW {
				// Try to break at a space.
				breakAt := innerW
				for breakAt > 0 && l[breakAt] != ' ' {
					breakAt--
				}
				if breakAt == 0 {
					breakAt = innerW // no space found, hard break
				}
				wrapped = append(wrapped, l[:breakAt])
				l = l[breakAt:]
				if len(l) > 0 && l[0] == ' ' {
					l = l[1:] // skip the space we broke at
				}
			}
			if len(l) > 0 {
				wrapped = append(wrapped, l)
			}
		} else {
			wrapped = append(wrapped, l)
		}
	}

	body := strings.Join(wrapped, "\n")
	scrollInfo := fmt.Sprintf("line %d/%d", scroll+1, len(allLines))
	footer := DimStyle.Render("↑↓/jk scroll · ctrl+u/d page · Esc to close · " + scrollInfo)

	modalContent := HeaderStyle.Render(title) + "\n\n" + body + "\n\n" + footer

	modalStyle := lipgloss.NewStyle().
		Width(modalW).
		Height(modalH).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(0, 2)

	modal := modalStyle.Render(modalContent)

	// Place modal centered over the background using lipgloss.Place.
	// WithWhitespaceStyle sets the background of the surrounding area.
	overlaid := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))))

	return overlaid, scroll
}

// outputModalLines returns the displayable lines for content in the fullscreen
// output modal: markdown-rendered (if detected), split into rendered lines, with
// tool-event lines dim-styled. Shared between renderOutputModal (view path) and
// refreshOutputModalIfShowing (auto-tail path) so both reason about the same
// line count — earlier the refresh used the RAW line count, which caused the
// scroll to get yanked around whenever the markdown expansion ratio was large.
func (m *Model) outputModalLines(content string) []string {
	cleanContent := xansi.Strip(content)
	var lines []string
	if m.outputMdRender != nil && looksLikeMarkdown(cleanContent) {
		rendered, err := m.outputMdRender.Render(cleanContent)
		if err == nil {
			lines = strings.Split(strings.TrimRight(rendered, "\n"), "\n")
		} else {
			slog.Warn("outputMdRender failed, falling back to plain text", "error", err)
		}
	}
	if lines == nil {
		lines = strings.Split(cleanContent, "\n")
	}
	for i, line := range lines {
		stripped := xansi.Strip(line)
		trimmed := strings.TrimSpace(stripped)
		if strings.HasPrefix(trimmed, "⚙") || strings.HasPrefix(trimmed, "→") {
			lines[i] = DimStyle.Render(stripped)
		}
	}
	return lines
}

// outputModalDims returns the modal's total and visible line heights, matching
// the layout used by renderOutputModal. Kept in sync with the render path so
// the auto-tail clamp can reason about the same bounds.
func (m *Model) outputModalDims() (modalH, visibleH int) {
	modalH = m.height - 4
	if modalH < 10 {
		modalH = 10
	}
	visibleH = modalH - 4 // title + footer + borders
	return
}

// renderOutputModal renders a fullscreen scrollable modal for worker output.
// Unlike renderScrollableModal, it uses nearly the full terminal dimensions,
// renders markdown when detected, and applies distinct styling to tool event lines.
func (m *Model) renderOutputModal(title, content string, scroll int) (string, int) {
	modalW := m.width - 4
	modalH, visibleH := m.outputModalDims()
	if modalW < 40 {
		modalW = 40
	}

	innerW := modalW - 4 // account for border + padding

	allLines := m.outputModalLines(content)

	maxScroll := len(allLines) - visibleH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}

	start := scroll
	end := start + visibleH
	if end > len(allLines) {
		end = len(allLines)
	}
	visibleLines := allLines[start:end]

	// Truncate each line to modal inner width using an ANSI-aware truncator so
	// we never slice mid-escape-sequence (which would leave raw codes like
	// "[38;2;98;98;98m" visible in the terminal).
	truncated := make([]string, len(visibleLines))
	for i, l := range visibleLines {
		if lipgloss.Width(l) > innerW {
			truncated[i] = xansi.Truncate(l, innerW, "")
		} else {
			truncated[i] = l
		}
	}

	body := strings.Join(truncated, "\n")
	scrollInfo := fmt.Sprintf("line %d/%d", scroll+1, len(allLines))
	footer := DimStyle.Render("↑↓/jk scroll · ctrl+u/d page · Esc to close · " + scrollInfo)

	modalContent := HeaderStyle.Render(title) + "\n\n" + body + "\n\n" + footer

	modalStyle := lipgloss.NewStyle().
		Width(modalW).
		Height(modalH).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(0, 2)

	modal := modalStyle.Render(modalContent)

	overlaid := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))))

	return overlaid, scroll
}

// promptWidgetInner builds the prompt wizard content (byline, question, and
// either the option list or the custom-text input, plus a hint). It is shared
// by the inline widget and the centered prompt modal.
func (m Model) promptWidgetInner() string {
	byline := m.promptByline()
	question := HeaderStyle.Render(m.prompt.promptQuestion)

	if m.prompt.promptCustom {
		// Custom text mode: byline + question above the normal textarea.
		hint := DimStyle.Render("Enter to submit · Esc to go back")
		return lipgloss.JoinVertical(lipgloss.Left, byline, question, m.input.View(), hint)
	}

	// Option selection mode: numbered list with cursor.
	allOptions := append(m.prompt.promptOptions, "Custom response...")

	// The answer already committed for this question (if revisited via ←→) is
	// marked with a check so the user can see their prior choice.
	committed := ""
	if m.prompt.roundIndex < len(m.prompt.roundAnswers) {
		committed = m.prompt.roundAnswers[m.prompt.roundIndex]
	}

	var rows []string
	for i, opt := range allOptions {
		prefix := "  "
		if i == m.prompt.promptSelected {
			prefix = "▶ "
		} else if opt == committed && i < len(allOptions)-1 {
			prefix = "✓ "
		}
		label := prefix + fmt.Sprintf("%d. %s", i+1, opt)
		if i == m.prompt.promptSelected {
			rows = append(rows, CmdPopupSelectedStyle.Render(label))
		} else {
			rows = append(rows, DimStyle.Render(label))
		}
	}

	optionList := lipgloss.JoinVertical(lipgloss.Left, rows...)
	hintText := "↑↓ navigate · Enter select · Esc cancel"
	if len(m.prompt.round) > 1 {
		hintText = "↑↓ navigate · ←→ switch question · Enter select · Esc cancel"
	}
	hint := DimStyle.Render(hintText)

	return lipgloss.JoinVertical(lipgloss.Left,
		byline,
		question,
		"",
		optionList,
		"",
		hint,
	)
}

// renderPromptModal renders the blocker answer wizard as a centered overlay.
// Blockers are answered in a modal (not the chat input) so the flow continues
// the same surface the selection dialog opened on. The textarea is sized to the
// modal for this render and restored afterward so the normal input is unaffected.
func (m *Model) renderPromptModal() string {
	modalW := m.width * 2 / 3
	if modalW < 50 {
		modalW = 50
	}
	if modalW > m.width-4 {
		modalW = m.width - 4
	}
	innerW := modalW - 6
	if innerW < 1 {
		innerW = 1
	}

	saved := m.input.Width()
	m.input.SetWidth(innerW)
	content := m.promptWidgetInner()
	m.input.SetWidth(saved)

	modalStyle := lipgloss.NewStyle().
		Width(modalW).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorStreaming).
		Padding(0, 2)
	modal := modalStyle.Render(content)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))))
}

// promptByline renders the asker label for the live prompt widget, plus the
// "question N of M" progress indicator when the operator asked a multi-question
// round.
func (m Model) promptByline() string {
	label := "operator asks"
	if src := m.prompt.source; src != "" {
		// "graph:<node>" → "<node> asks"
		label = strings.TrimPrefix(src, "graph:") + " asks"
	}
	line := HeaderStyle.Render("◆ " + label)
	// Name the job the blocker is gating so "who is asking about what" is clear.
	if title := m.jobTitle(m.prompt.jobID); title != "" {
		line += DimStyle.Render("  ·  " + title)
	}
	if n := len(m.prompt.round); n > 1 {
		line += DimStyle.Render(fmt.Sprintf("  ·  question %d of %d", m.prompt.roundIndex+1, n))
	}
	return line
}
