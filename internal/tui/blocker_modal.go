// Blocker modal: blocker Q&A UI including rendering, key handling, and answer submission.
package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/db"
)

// BlockerQuestion is a single question posed by a blocked team.
type BlockerQuestion struct {
	Text    string
	Options []string
	Answer  string
}

// Blocker represents a blocker that needs user input.
type Blocker struct {
	Team           string
	BlockerSummary string
	Context        string
	WhatWasTried   string
	WhatIsNeeded   string
	Questions      []BlockerQuestion
	Answered       bool
	RawBody        string
}

// updateBlockerModal handles all key presses when the blocker modal is open.
func (m *Model) updateBlockerModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	b := m.blockerModal.blocker
	if b != nil && len(b.Questions) > 0 {
		q := b.Questions[m.blockerModal.questionIdx]

		switch msg.String() {
		case "esc":
			m.blockerModal.show = false
			m.blockerModal.inputText = ""

		case "up", "k":
			if m.blockerModal.questionIdx > 0 {
				m.blockerModal.questionIdx--
				m.blockerModal.inputText = b.Questions[m.blockerModal.questionIdx].Answer
			}

		case "down", "j":
			if m.blockerModal.questionIdx < len(b.Questions)-1 {
				m.blockerModal.questionIdx++
				m.blockerModal.inputText = b.Questions[m.blockerModal.questionIdx].Answer
			}

		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			if len(q.Options) > 0 {
				idx, _ := strconv.Atoi(msg.String())
				idx-- // 0-based
				if idx >= 0 && idx < len(q.Options) {
					b.Questions[m.blockerModal.questionIdx].Answer = q.Options[idx]
					// Advance to next question if not on last.
					if m.blockerModal.questionIdx < len(b.Questions)-1 {
						m.blockerModal.questionIdx++
						m.blockerModal.inputText = b.Questions[m.blockerModal.questionIdx].Answer
					}
				}
			} else {
				// Free-form: append digit to input.
				m.blockerModal.inputText += msg.String()
			}

		case "enter":
			// Confirm free-form answer.
			if len(q.Options) == 0 && m.blockerModal.inputText != "" {
				b.Questions[m.blockerModal.questionIdx].Answer = m.blockerModal.inputText
				m.blockerModal.inputText = ""
				if m.blockerModal.questionIdx < len(b.Questions)-1 {
					m.blockerModal.questionIdx++
				}
			}

		case "backspace":
			if len(m.blockerModal.inputText) > 0 {
				runes := []rune(m.blockerModal.inputText)
				m.blockerModal.inputText = string(runes[:len(runes)-1])
			}

		case "s":
			// Submit all answers.
			return m, m.submitBlockerAnswers()

		default:
			// Free-form: append printable chars.
			if len(q.Options) == 0 && len(msg.String()) == 1 {
				m.blockerModal.inputText += msg.String()
			}
		}
	} else {
		// No questions — just allow closing.
		switch msg.String() {
		case "esc", "s":
			m.blockerModal.show = false
		}
	}
	return m, nil
}

// hasBlocker reports whether the given job has an unanswered blocker recorded.
func (m Model) hasBlocker(j *db.Job) bool {
	b, ok := m.blockers[j.ID]
	return ok && b != nil && !b.Answered
}

// submitBlockerAnswers emits a blockerAnswersSubmittedMsg to the event loop.
// Blocker answers are no longer written to the filesystem; they will be
// stored differently in the new architecture (Phase 3 Session B).
func (m *Model) submitBlockerAnswers() tea.Cmd {
	b := m.blockerModal.blocker
	jobID := m.blockerModal.jobID
	return func() tea.Msg {
		return blockerAnswersSubmittedMsg{jobID: jobID, blocker: b}
	}
}

// renderBlockerModal renders the full-screen blocker Q&A modal.
func (m *Model) renderBlockerModal() string {
	if !m.blockerModal.show || m.blockerModal.blocker == nil {
		return ""
	}
	b := m.blockerModal.blocker

	modalW := m.width - 8
	if modalW > 90 {
		modalW = 90
	}
	if modalW < 40 {
		modalW = 40
	}
	modalH := m.height - 6
	if modalH > 30 {
		modalH = 30
	}
	if modalH < 10 {
		modalH = 10
	}
	innerW := modalW - 4 // account for border + padding

	// Header.
	header := lipgloss.NewStyle().Bold(true).Foreground(ColorStreaming).Render(
		fmt.Sprintf("⚠  Blocker: %s", b.BlockerSummary),
	)

	var sections []string

	// Context (truncated to 6 lines).
	if b.Context != "" {
		lines := strings.Split(b.Context, "\n")
		if len(lines) > 6 {
			lines = lines[:6]
			lines = append(lines, DimStyle.Render("..."))
		}
		sections = append(sections, HeaderStyle.Render("Context"))
		sections = append(sections, strings.Join(lines, "\n"))
	}

	// Questions.
	sections = append(sections, HeaderStyle.Render(fmt.Sprintf("Questions (%d total)", len(b.Questions))))
	for i, q := range b.Questions {
		prefix := "  "
		qStyle := DimStyle
		if i == m.blockerModal.questionIdx {
			prefix = "▶ "
			qStyle = lipgloss.NewStyle() // normal weight for current
		}

		qLine := prefix + qStyle.Render(fmt.Sprintf("%d. %s", i+1, q.Text))
		sections = append(sections, qLine)

		if i == m.blockerModal.questionIdx {
			if len(q.Options) > 0 {
				for oi, opt := range q.Options {
					optStyle := DimStyle
					if q.Answer == opt {
						optStyle = lipgloss.NewStyle().Foreground(ColorConnected).Bold(true)
					}
					sections = append(sections, optStyle.Render(fmt.Sprintf("    [%d] %s", oi+1, opt)))
				}
			} else {
				// Free-form input line.
				inputVal := m.blockerModal.inputText
				if q.Answer != "" && inputVal == "" {
					inputVal = q.Answer
				}
				sections = append(sections, fmt.Sprintf("    > %s_", inputVal))
			}
		} else if q.Answer != "" {
			sections = append(sections, DimStyle.Render(fmt.Sprintf("    ✓ %s", q.Answer)))
		}
	}

	// Footer hints.
	var footerHint string
	if len(b.Questions) > 0 && len(b.Questions[m.blockerModal.questionIdx].Options) > 0 {
		footerHint = "[1-9] select  [↑↓] navigate  [s] submit  [Esc] close"
	} else {
		footerHint = "[Enter] confirm  [↑↓] navigate  [s] submit  [Esc] close"
	}
	footer := DimStyle.Render(footerHint)

	allParts := append([]string{header, ""}, append(sections, "", footer)...)
	body := lipgloss.NewStyle().Width(innerW).Render(
		lipgloss.JoinVertical(lipgloss.Left, allParts...),
	)

	modal := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorStreaming).
		Padding(1, 2).
		Width(modalW).
		Height(modalH).
		Render(body)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}
