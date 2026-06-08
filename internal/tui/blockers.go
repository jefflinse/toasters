// Blockers: a queue of pending ask_user requests (from the operator or a graph
// node) surfaced in a dedicated panel. Rather than prompt inline — where a stray
// Enter could misfire a response — blockers accumulate and the user answers each
// one deliberately by opening it from the panel.
package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// blockerSourceLabel renders a human-readable attribution for a blocker's
// Source: "operator" for the operator's own ask_user, or "node <name>" for a
// graph node interrupt ("graph:<node>").
func blockerSourceLabel(source string) string {
	if source == "" {
		return "operator"
	}
	if node := strings.TrimPrefix(source, "graph:"); node != source {
		return "node " + node
	}
	return source
}

// blockerFirstQuestion returns the first question text of a blocker, for compact
// one-line summaries (panel rows, toasts).
func blockerFirstQuestion(b service.Blocker) string {
	if len(b.Questions) > 0 && strings.TrimSpace(b.Questions[0].Question) != "" {
		return b.Questions[0].Question
	}
	return "(no question)"
}

// jobTitle resolves a job's title from the model's known jobs, or "" if the job
// isn't loaded.
func (m Model) jobTitle(jobID string) string {
	for _, j := range m.jobs {
		if j.ID == jobID {
			return j.Title
		}
	}
	return ""
}

// blockerLabel attributes a blocker to its asker and, for graph-node blockers,
// the job it's gating — so the user can tell who is asking and about which work.
// e.g. "node plan · Create a To-Do management web app" or "operator".
func (m Model) blockerLabel(b service.Blocker) string {
	label := blockerSourceLabel(b.Source)
	if b.JobID != "" {
		if title := m.jobTitle(b.JobID); title != "" {
			label += " · " + title
		}
	}
	return label
}

// removeBlocker drops the blocker with the given request ID from the queue and
// keeps the panel cursor in range.
func (m *Model) removeBlocker(requestID string) {
	out := m.blockers[:0]
	for _, b := range m.blockers {
		if b.RequestID != requestID {
			out = append(out, b)
		}
	}
	m.blockers = out
	if m.blockersSel >= len(m.blockers) {
		m.blockersSel = len(m.blockers) - 1
	}
	if m.blockersSel < 0 {
		m.blockersSel = 0
	}
}

// dismissBlocker answers a blocker's waiting caller with a cancellation so it
// stops blocking. The server resolves the blocker and emits BlockerResolved,
// which removes it from the panel.
func (m *Model) dismissBlocker(requestID string) tea.Cmd {
	if requestID == "" || m.svc == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := m.svc.Operator().RespondToPrompt(ctx, requestID, "User cancelled.")
	cancel()
	if err != nil {
		return m.addToast("⚠ Dismiss failed: "+err.Error(), toastWarning)
	}
	return m.addToast("Blocker dismissed", toastInfo)
}

// openBlocker enters the prompt wizard for a blocker. It reuses the existing
// prompt-mode machinery (multi-question rounds, options, custom text), flagged
// fromBlocker so Esc backs out without resolving the blocker.
func (m *Model) openBlocker(b service.Blocker) {
	round := b.Questions
	if len(round) == 0 {
		round = []service.PromptQuestion{{Question: blockerFirstQuestion(b)}}
	}
	m.prompt = promptModeState{
		promptMode:   true,
		round:        round,
		roundIndex:   0,
		roundAnswers: make([]string, len(round)),
		roundCursor:  make([]int, len(round)),
		source:       b.Source,
		jobID:        b.JobID,
		requestID:    b.RequestID,
		fromBlocker:  true,
	}
	m.loadPromptQuestion(0)
}

// updateBlockersModal handles keys while the blocker selection dialog is open.
// ↑↓ move the cursor, Enter opens the chosen blocker in the prompt wizard, Esc
// closes the dialog.
func (m *Model) updateBlockersModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.blockersModal.sel > 0 {
			m.blockersModal.sel--
		}
	case "down", "j":
		if m.blockersModal.sel < len(m.blockers)-1 {
			m.blockersModal.sel++
		}
	case "enter":
		if m.blockersModal.sel >= 0 && m.blockersModal.sel < len(m.blockers) {
			b := m.blockers[m.blockersModal.sel]
			m.blockersModal = blockersModalState{}
			m.openBlocker(b)
		}
	case "esc":
		m.blockersModal = blockersModalState{}
	}
	return m, nil
}

// renderBlockersModal builds the centered blocker-selection dialog: a numbered
// list of pending blockers with the cursor highlighted.
func (m *Model) renderBlockersModal() string {
	modalW := m.width / 2
	if modalW < 48 {
		modalW = 48
	}
	if modalW > m.width-4 {
		modalW = m.width - 4
	}
	innerW := modalW - 6

	var lines []string
	lines = append(lines, HeaderStyle.Render("Blockers"))
	lines = append(lines, "")
	if len(m.blockers) == 0 {
		lines = append(lines, DimStyle.Italic(true).Render("No blockers"))
	} else {
		for i, b := range m.blockers {
			marker := "  "
			if i == m.blockersModal.sel {
				marker = "▶ "
			}
			label := m.blockerLabel(b) + " — " + blockerFirstQuestion(b)
			row := marker + truncateStr(label, innerW)
			if i == m.blockersModal.sel {
				row = CmdPopupSelectedStyle.Render(row)
			}
			lines = append(lines, row)
		}
	}
	lines = append(lines, "")
	lines = append(lines, DimStyle.Render("↑↓ select · Enter answer · Esc close"))

	modalStyle := lipgloss.NewStyle().
		Width(modalW).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorStreaming).
		Padding(0, 2)
	modal := modalStyle.Render(strings.Join(lines, "\n"))

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))))
}
