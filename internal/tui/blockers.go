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
	if m.blockersModal.sel >= m.blockersModalRowCount() {
		m.blockersModal.sel = m.blockersModalRowCount() - 1
	}
	if m.blockersModal.sel < 0 {
		m.blockersModal.sel = 0
	}
}

// dismissBlocker cancels a blocker's waiting caller so it stops blocking. The
// server resolves the blocker (recording it as dismissed in history) and
// emits BlockerResolved, which removes it from the panel.
func (m *Model) dismissBlocker(requestID string) tea.Cmd {
	if requestID == "" || m.svc == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := m.svc.Operator().DismissPrompt(ctx, requestID)
	cancel()
	if err != nil {
		return m.addToast("⚠ Dismiss failed: "+err.Error(), toastWarning)
	}
	return m.addToast("Blocker dismissed", toastInfo)
}

// openBlockersModal opens the Blockers modal positioned on the pane's current
// selection and kicks off the history fetch so resolved blockers appear once
// it lands.
func (m *Model) openBlockersModal() tea.Cmd {
	sel := m.blockersSel
	if sel >= len(m.blockers) {
		sel = 0
	}
	m.blockersModal = blockersModalState{show: true, sel: sel}
	return m.fetchBlockerHistory()
}

// blockerHistoryLimit caps how many resolved blockers the modal loads.
const blockerHistoryLimit = 100

// fetchBlockerHistory loads resolved blockers for the modal's history section.
func (m Model) fetchBlockerHistory() tea.Cmd {
	svc := m.svc
	if svc == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		records, err := svc.Operator().BlockerHistory(ctx, blockerHistoryLimit)
		return BlockerHistoryMsg{Records: records, Err: err}
	}
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

// blockersModalRowCount returns the total number of selectable rows in the
// modal: the pending queue followed by resolved history.
func (m *Model) blockersModalRowCount() int {
	return len(m.blockers) + len(m.blockersModal.history)
}

// updateBlockersModal handles keys while the blockers modal is open. ↑↓ move
// the cursor across pending and resolved rows; Enter opens a pending blocker
// in the prompt wizard; x dismisses a pending blocker (the waiting caller
// receives a cancellation); Esc closes the modal. Resolved rows are
// browse-only — Enter and x are no-ops on them.
func (m *Model) updateBlockersModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.blockersModal.sel > 0 {
			m.blockersModal.sel--
		}
	case "down", "j":
		if m.blockersModal.sel < m.blockersModalRowCount()-1 {
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

	// --- Left panel: pending queue + resolved history ---
	// Rows are built as (text, selectable-index) pairs so section headers can
	// interleave with selectable rows, then windowed so the cursor stays
	// visible when the combined list outgrows the panel.
	type leftRow struct {
		text string
		idx  int // selection index; -1 for headers/spacers
	}
	var rows []leftRow

	if len(m.blockers) == 0 {
		rows = append(rows, leftRow{DimStyle.Italic(true).Render("No pending blockers"), -1})
	} else {
		for i, b := range m.blockers {
			attr := truncateStr("⛔ "+m.blockerLabel(b), leftInnerW-6)
			rows = append(rows, leftRow{fmt.Sprintf(" %s · %s", attr, compactAge(b.CreatedAt)), i})
		}
	}
	if m.blockersModal.histErr != nil {
		rows = append(rows, leftRow{"", -1})
		rows = append(rows, leftRow{ErrorStyle.Render(truncateStr("History unavailable: "+m.blockersModal.histErr.Error(), leftInnerW)), -1})
	} else if len(m.blockersModal.history) > 0 {
		rows = append(rows, leftRow{"", -1})
		rows = append(rows, leftRow{DimStyle.Render("Resolved"), -1})
		for i, r := range m.blockersModal.history {
			icon := blockerDispositionIcon(r.Disposition)
			attr := truncateStr(m.blockerLabel(r.Blocker), leftInnerW-8)
			text := DimStyle.Render(fmt.Sprintf(" %s %s · %s", icon, attr, compactAge(r.ResolvedAt)))
			rows = append(rows, leftRow{text, len(m.blockers) + i})
		}
	}

	title := gradientText("Blockers", [3]uint8{255, 175, 0}, [3]uint8{255, 90, 0})
	if n := len(m.blockers); n > 0 {
		title += BlockerCountStyle.Render(fmt.Sprintf(" · %d waiting", n))
	}
	leftLines := []string{title, ""}

	// Window the rows so the selected one is visible in the space under the
	// title. Selection highlight is applied after windowing so the style's
	// full-width render can't be truncated by the slice.
	rowArea := panelInnerH - len(leftLines)
	if rowArea < 1 {
		rowArea = 1
	}
	selPos := 0
	for i, r := range rows {
		if r.idx == m.blockersModal.sel {
			selPos = i
			break
		}
	}
	offset := 0
	if len(rows) > rowArea {
		offset = selPos - rowArea/2
		if offset < 0 {
			offset = 0
		}
		if offset > len(rows)-rowArea {
			offset = len(rows) - rowArea
		}
	}
	end := offset + rowArea
	if end > len(rows) {
		end = len(rows)
	}
	for _, r := range rows[offset:end] {
		text := r.text
		if r.idx >= 0 && r.idx == m.blockersModal.sel {
			text = ModalSelectedStyle.Width(leftInnerW).Render(text)
		}
		leftLines = append(leftLines, text)
	}

	leftLines = padOrTrimLines(leftLines, panelInnerH)
	leftPanel := ModalFocusedPanel.Width(leftPanelW).Height(panelH).
		Render(strings.Join(leftLines, "\n"))

	// --- Right panel: selected blocker detail ---
	var rightLines []string
	sel := m.blockersModal.sel
	switch {
	case m.blockersModalRowCount() == 0:
		rightLines = append(rightLines, DimStyle.Italic(true).Render("Nothing is waiting on you."))
	case sel < len(m.blockers):
		if sel < 0 {
			sel = 0
		}
		b := m.blockers[sel]
		rightLines = append(rightLines, HeaderStyle.Render(truncateStr(blockerSourceLabel(b.Source), rightInnerW)))
		rightLines = append(rightLines, DimStyle.Render(strings.Repeat("─", rightInnerW)))
		rightLines = append(rightLines, m.blockerJobLine(b.JobID, rightInnerW)...)
		rightLines = append(rightLines, DimStyle.Render("Raised: ")+compactAge(b.CreatedAt)+DimStyle.Render(" ago"))
		rightLines = append(rightLines, "")
		rightLines = append(rightLines, blockerQuestionsSection(b.Questions, rightInnerW)...)
	default:
		r := m.blockersModal.history[sel-len(m.blockers)]
		rightLines = append(rightLines, HeaderStyle.Render(truncateStr(blockerSourceLabel(r.Source), rightInnerW)))
		rightLines = append(rightLines, DimStyle.Render(strings.Repeat("─", rightInnerW)))
		rightLines = append(rightLines, m.blockerJobLine(r.JobID, rightInnerW)...)
		rightLines = append(rightLines, DimStyle.Render("Raised:   ")+compactAge(r.CreatedAt)+DimStyle.Render(" ago"))
		rightLines = append(rightLines,
			DimStyle.Render("Resolved: ")+compactAge(r.ResolvedAt)+DimStyle.Render(" ago · ")+
				blockerDispositionIcon(r.Disposition)+" "+r.Disposition)
		rightLines = append(rightLines, "")
		rightLines = append(rightLines, blockerQuestionsSection(r.Questions, rightInnerW)...)
		if r.Answer != "" {
			rightLines = append(rightLines, "")
			rightLines = append(rightLines, DimStyle.Render("Answer"))
			rightLines = append(rightLines, strings.Split(wrapText(r.Answer, rightInnerW), "\n")...)
		}
	}
	rightLines = padOrTrimLines(rightLines, panelInnerH)
	rightPanel := ModalPanelStyle.Width(rightPanelW).Height(panelH).
		Render(strings.Join(rightLines, "\n"))

	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, " ", rightPanel)

	// Footer: answer/dismiss hints only apply to pending rows.
	hints := []string{DimStyle.Render("[↑↓] Navigate")}
	if m.blockersModal.sel < len(m.blockers) && len(m.blockers) > 0 {
		hints = append(hints, DimStyle.Render("[Enter] Answer"), DimStyle.Render("[x] Dismiss"))
	}
	hints = append(hints, DimStyle.Render("[Esc] Close"))
	footer := strings.Join(hints, "  ")

	inner := lipgloss.JoinVertical(lipgloss.Left, panels, footer)
	modal := ModalStyle.Width(modalW).Render(inner)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}

// blockerDispositionIcon returns the marker for a resolved blocker's
// disposition: answered gets a green check; dismissed and cancelled get
// dim/red markers.
func blockerDispositionIcon(disposition string) string {
	switch disposition {
	case service.BlockerDispositionAnswered:
		return ConnectedStyle.Render("✓")
	case service.BlockerDispositionDismissed:
		return DimStyle.Render("–")
	default: // cancelled (or unknown)
		return ErrorStyle.Render("⊘")
	}
}

// blockerJobLine renders the "Job: <title>" detail line, or nothing for
// operator-raised blockers with no job context.
func (m Model) blockerJobLine(jobID string, width int) []string {
	if jobID == "" {
		return nil
	}
	job := m.jobTitle(jobID)
	if job == "" {
		job = jobID
	}
	return []string{DimStyle.Render("Job:      ") + truncateStr(job, width-10)}
}

// blockerQuestionsSection renders a blocker's question round: a single
// question renders bare; multiple get a count header and numbering.
func blockerQuestionsSection(questions []service.PromptQuestion, width int) []string {
	if len(questions) == 1 {
		return blockerQuestionLines(questions[0], -1, width)
	}
	lines := []string{fmt.Sprintf("Questions (%d)", len(questions)), ""}
	for qi, q := range questions {
		lines = append(lines, blockerQuestionLines(q, qi+1, width)...)
		if qi < len(questions)-1 {
			lines = append(lines, "")
		}
	}
	return lines
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
