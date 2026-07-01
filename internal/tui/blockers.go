// Blockers: a queue of pending ask_user requests (from the operator or a graph
// node) surfaced in a dedicated panel. Rather than prompt inline — where a stray
// Enter could misfire a response — blockers accumulate and the user answers each
// one deliberately by opening it from the panel.
package tui

import (
	"context"
	"fmt"
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
	if m.blockersModal.sel >= len(m.blockers) {
		m.blockersModal.sel = len(m.blockers) - 1
	}
	if m.blockersModal.sel < 0 {
		m.blockersModal.sel = 0
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

// compactAge renders how long ago t was as a compact single unit ("12s",
// "3m", "2h", "5d"), for blocker attributions where space is tight.
func compactAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		s := int(d.Seconds())
		if s < 1 {
			s = 1
		}
		return fmt.Sprintf("%ds", s)
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// updateBlockersModal handles keys while the blockers modal is open. ↑↓ move
// the cursor, Enter opens the chosen blocker in the prompt wizard, x dismisses
// it (answers the waiting caller with a cancellation), Esc closes the modal.
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
	case "x":
		if m.blockersModal.sel >= 0 && m.blockersModal.sel < len(m.blockers) {
			b := m.blockers[m.blockersModal.sel]
			// The BlockerResolved event removes it from m.blockers; the
			// cursor is clamped by removeBlocker when that lands.
			return m, m.dismissBlocker(b.RequestID)
		}
	case "esc":
		m.blockersModal = blockersModalState{}
	}
	return m, nil
}

// renderBlockersModal builds the Blockers modal: a two-panel overlay in the
// same visual language as the Jobs and MCP modals — the blocker queue on the
// left, the selected blocker's full detail (attribution, job, age, every
// question with its options) on the right.
func (m *Model) renderBlockersModal() string {
	modalW := m.width - 4
	if modalW < 60 {
		modalW = 60
	}
	if modalW > m.width {
		modalW = m.width
	}
	modalH := m.height - 4
	if modalH < 16 {
		modalH = 16
	}
	if modalH > m.height {
		modalH = m.height
	}

	innerW := modalW - ModalStyle.GetHorizontalFrameSize()
	if innerW < 10 {
		innerW = 10
	}

	// Left panel: ~36 chars of inner content, capped at half the modal.
	leftInnerW := 34
	leftPanelW := leftInnerW + ModalPanelStyle.GetHorizontalFrameSize()
	if leftPanelW > innerW/2 {
		leftPanelW = innerW / 2
		leftInnerW = leftPanelW - ModalPanelStyle.GetHorizontalFrameSize()
	}

	rightPanelW := innerW - leftPanelW - 1 // -1 for spacing
	rightInnerW := rightPanelW - ModalPanelStyle.GetHorizontalFrameSize()
	if rightInnerW < 5 {
		rightInnerW = 5
	}

	footerLines := 1
	panelH := modalH - ModalStyle.GetVerticalFrameSize() - footerLines - 1
	if panelH < 5 {
		panelH = 5
	}
	panelInnerH := panelH - ModalPanelStyle.GetVerticalFrameSize()
	if panelInnerH < 3 {
		panelInnerH = 3
	}

	// --- Left panel: blocker queue ---
	var leftLines []string
	title := gradientText("Blockers", [3]uint8{255, 175, 0}, [3]uint8{255, 90, 0})
	if n := len(m.blockers); n > 0 {
		title += BlockerCountStyle.Render(fmt.Sprintf(" · %d waiting", n))
	}
	leftLines = append(leftLines, title)
	leftLines = append(leftLines, "")

	if len(m.blockers) == 0 {
		leftLines = append(leftLines, DimStyle.Italic(true).Render("No pending blockers"))
	} else {
		for i, b := range m.blockers {
			attr := truncateStr("⛔ "+m.blockerLabel(b), leftInnerW-6)
			line := fmt.Sprintf(" %s · %s", attr, compactAge(b.CreatedAt))
			if i == m.blockersModal.sel {
				line = ModalSelectedStyle.Width(leftInnerW).Render(line)
			}
			leftLines = append(leftLines, line)
		}
	}
	leftLines = padOrTrimLines(leftLines, panelInnerH)
	leftPanel := ModalFocusedPanel.Width(leftPanelW).Height(panelH).
		Render(strings.Join(leftLines, "\n"))

	// --- Right panel: selected blocker detail ---
	var rightLines []string
	if len(m.blockers) == 0 {
		rightLines = append(rightLines, DimStyle.Italic(true).Render("Nothing is waiting on you."))
	} else {
		sel := m.blockersModal.sel
		if sel < 0 || sel >= len(m.blockers) {
			sel = 0
		}
		b := m.blockers[sel]

		rightLines = append(rightLines, HeaderStyle.Render(truncateStr(blockerSourceLabel(b.Source), rightInnerW)))
		rightLines = append(rightLines, DimStyle.Render(strings.Repeat("─", rightInnerW)))
		if b.JobID != "" {
			job := m.jobTitle(b.JobID)
			if job == "" {
				job = b.JobID
			}
			rightLines = append(rightLines, DimStyle.Render("Job:    ")+truncateStr(job, rightInnerW-8))
		}
		rightLines = append(rightLines, DimStyle.Render("Raised: ")+compactAge(b.CreatedAt)+DimStyle.Render(" ago"))
		rightLines = append(rightLines, "")

		if len(b.Questions) == 1 {
			rightLines = append(rightLines, blockerQuestionLines(b.Questions[0], -1, rightInnerW)...)
		} else {
			rightLines = append(rightLines, fmt.Sprintf("Questions (%d)", len(b.Questions)))
			rightLines = append(rightLines, "")
			for qi, q := range b.Questions {
				rightLines = append(rightLines, blockerQuestionLines(q, qi+1, rightInnerW)...)
				if qi < len(b.Questions)-1 {
					rightLines = append(rightLines, "")
				}
			}
		}
	}
	rightLines = padOrTrimLines(rightLines, panelInnerH)
	rightPanel := ModalPanelStyle.Width(rightPanelW).Height(panelH).
		Render(strings.Join(rightLines, "\n"))

	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, " ", rightPanel)

	footer := lipgloss.JoinHorizontal(lipgloss.Left,
		DimStyle.Render("[↑↓] Navigate"), "  ",
		DimStyle.Render("[Enter] Answer"), "  ",
		DimStyle.Render("[x] Dismiss"), "  ",
		DimStyle.Render("[Esc] Close"),
	)

	inner := lipgloss.JoinVertical(lipgloss.Left, panels, footer)
	modal := ModalStyle.Width(modalW).Render(inner)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}

// blockerQuestionLines renders one question (wrapped to width) plus its
// suggested options as dim bullets. num >= 1 prefixes "N." for multi-question
// rounds; -1 renders the question bare.
func blockerQuestionLines(q service.PromptQuestion, num, width int) []string {
	text := strings.TrimSpace(q.Question)
	if text == "" {
		text = "(no question)"
	}
	if num >= 1 {
		text = fmt.Sprintf("%d. %s", num, text)
	}
	lines := strings.Split(wrapText(text, width), "\n")
	for _, opt := range q.Options {
		lines = append(lines, DimStyle.Render(truncateStr("   ○ "+opt, width)))
	}
	return lines
}

// padOrTrimLines pads lines with blanks (or trims) to exactly h entries so a
// modal panel always fills its fixed height.
func padOrTrimLines(lines []string, h int) []string {
	for len(lines) < h {
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return lines
}
