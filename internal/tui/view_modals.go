package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

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
