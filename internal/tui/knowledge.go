// Knowledge screen: a full-screen master-detail view of a job's notes (the
// files a job's workers write via job_note_write — see docs/kb-design.md).
// The left pane lists notes newest-first; the right pane shows the selected
// note's content. Toggled with ctrl+k, mirroring the ctrl+g nodes screen.
//
// The TUI is a remote client and cannot read the job's workspace filesystem
// directly, so both the list and the content are fetched from the service
// (Knowledge().ListJobNotes / ReadJobNote) via async tea.Cmds, mirroring the
// /metrics modal's fetch-then-render shape (see metrics_modal.go).
package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

const (
	knowledgeRowContentH = 2                        // title line + source/age line
	knowledgeRowBlockH   = knowledgeRowContentH + 1 // + one blank separator row
	knowledgeBarH        = 1                        // help bar (bottom)
)

// knowledgeLayout holds the derived geometry of the Knowledge screen, shared
// by the render and key-handling paths so list scrolling can never drift out
// of sync. Mirrors nodesLayout.
type knowledgeLayout struct {
	listW        int
	detailW      int
	paneH        int
	listInnerW   int
	detailInnerW int
	detailInnerH int
	visibleRows  int
}

// knowledgeLayoutFor computes the Knowledge screen's geometry. listW is the
// desired list (left pane) width, passed in so it can match the main
// screen's sidebar — see nodesLayoutFor.
func knowledgeLayoutFor(termW, termH, listW int) knowledgeLayout {
	if listW > termW-24 {
		listW = termW - 24
	}
	if listW < 20 {
		listW = 20
	}

	paneH := termH - knowledgeBarH
	if paneH < 3 {
		paneH = 3
	}

	// Panes are rounded-bordered with 1 col of horizontal padding each side.
	const paneFrameH = 4 // border (2) + padding (2)
	const paneFrameV = 2 // border top+bottom
	listInnerW := listW - paneFrameH
	if listInnerW < 1 {
		listInnerW = 1
	}
	// List body = inner height minus the title row and the blank under it.
	listInnerH := paneH - paneFrameV - 2
	if listInnerH < knowledgeRowBlockH {
		listInnerH = knowledgeRowBlockH
	}
	visibleRows := listInnerH / knowledgeRowBlockH
	if visibleRows < 1 {
		visibleRows = 1
	}

	detailW := termW - listW
	detailInnerW := detailW - paneFrameH
	if detailInnerW < 1 {
		detailInnerW = 1
	}
	detailInnerH := paneH - paneFrameV
	if detailInnerH < 3 {
		detailInnerH = 3
	}

	return knowledgeLayout{
		listW:        listW,
		detailW:      detailW,
		paneH:        paneH,
		listInnerW:   listInnerW,
		detailInnerW: detailInnerW,
		detailInnerH: detailInnerH,
		visibleRows:  visibleRows,
	}
}

// fetchJobNotesCmd loads the note list for jobID via the service.
func (m Model) fetchJobNotesCmd(jobID string) tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		notes, err := svc.Knowledge().ListJobNotes(context.Background(), jobID)
		return JobNotesLoadedMsg{JobID: jobID, Notes: notes, Err: err}
	}
}

// fetchJobNoteCmd loads one note's full content via the service.
func (m Model) fetchJobNoteCmd(jobID, id string) tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		content, err := svc.Knowledge().ReadJobNote(context.Background(), jobID, id)
		return JobNoteContentMsg{ID: id, Content: content, Err: err}
	}
}

// openKnowledge shows the Knowledge screen for the job currently selected in
// the Jobs pane (the same selection the sidebar uses — see displayJobs /
// selectedJob). If no job is selected, the screen still opens, showing an
// empty state (renderKnowledgeList handles jobID == "").
func (m *Model) openKnowledge() tea.Cmd {
	m.knowledge.show = true
	m.knowledge.focusDetail = false
	m.knowledge.selected = 0
	m.knowledge.listScroll = 0
	m.knowledge.notes = nil
	m.knowledge.content = ""
	m.knowledge.contentScroll = 0
	m.knowledge.err = nil

	dj := m.displayJobs()
	if len(dj) == 0 || m.selectedJob < 0 || m.selectedJob >= len(dj) {
		m.knowledge.jobID = ""
		m.knowledge.loading = false
		return nil
	}
	m.knowledge.jobID = dj[m.selectedJob].ID
	m.knowledge.loading = true
	return m.fetchJobNotesCmd(m.knowledge.jobID)
}

// toggleKnowledge shows or hides the Knowledge screen.
func (m *Model) toggleKnowledge() tea.Cmd {
	if m.knowledge.show {
		m.knowledge.show = false
		return nil
	}
	return m.openKnowledge()
}

// selectKnowledgeNote moves the list selection to idx and, if it names a
// real note, kicks off a fetch for its content.
func (m *Model) selectKnowledgeNote(idx int) tea.Cmd {
	m.knowledge.selected = idx
	m.knowledge.content = ""
	m.knowledge.contentScroll = 0
	m.knowledge.err = nil

	if idx < 0 || idx >= len(m.knowledge.notes) {
		return nil
	}
	m.knowledge.loading = true
	return m.fetchJobNoteCmd(m.knowledge.jobID, m.knowledge.notes[idx].ID)
}

// clampKnowledgeListScroll shifts the list viewport so the selected row (at
// idx) stays visible, given how many rows fit. Mirrors clampListScroll.
func (m *Model) clampKnowledgeListScroll(idx, visibleRows int) {
	if idx < m.knowledge.listScroll {
		m.knowledge.listScroll = idx
	}
	if idx >= m.knowledge.listScroll+visibleRows {
		m.knowledge.listScroll = idx - visibleRows + 1
	}
	if m.knowledge.listScroll < 0 {
		m.knowledge.listScroll = 0
	}
}

// updateKnowledge routes key events for the Knowledge screen: keys go to the
// list or the content pane depending on which has focus.
func (m *Model) updateKnowledge(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.knowledge.focusDetail {
		return m.updateKnowledgeDetail(msg)
	}
	return m.updateKnowledgeList(msg)
}

// updateKnowledgeList handles keys while the list (master) pane has focus.
func (m *Model) updateKnowledgeList(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+k", "esc":
		m.knowledge.show = false
	case "up", "k":
		if m.knowledge.selected > 0 {
			return m, m.selectKnowledgeNote(m.knowledge.selected - 1)
		}
	case "down", "j":
		if m.knowledge.selected < len(m.knowledge.notes)-1 {
			return m, m.selectKnowledgeNote(m.knowledge.selected + 1)
		}
	case "enter", "tab", "right", "l":
		if len(m.knowledge.notes) > 0 {
			m.knowledge.focusDetail = true
		}
	case "r":
		if m.knowledge.jobID != "" {
			m.knowledge.loading = true
			m.knowledge.err = nil
			return m, m.fetchJobNotesCmd(m.knowledge.jobID)
		}
	}
	return m, nil
}

// updateKnowledgeDetail handles keys while the content pane has focus.
func (m *Model) updateKnowledgeDetail(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+k":
		m.knowledge.show = false
	case "esc", "shift+tab", "left", "h":
		m.knowledge.focusDetail = false
	case "up", "k":
		if m.knowledge.contentScroll > 0 {
			m.knowledge.contentScroll--
		}
	case "down", "j":
		m.knowledge.contentScroll++
	case "ctrl+u", "pgup":
		m.knowledge.contentScroll -= 10
		if m.knowledge.contentScroll < 0 {
			m.knowledge.contentScroll = 0
		}
	case "ctrl+d", "pgdown":
		m.knowledge.contentScroll += 10
	case "g", "home":
		m.knowledge.contentScroll = 0
	case "G", "end":
		m.knowledge.contentScroll = scrollBottom
	}
	return m, nil
}

// renderKnowledge renders the full Knowledge screen: the list (left) and
// content (right) panes above a help bar at the bottom.
func (m *Model) renderKnowledge() string {
	lay := knowledgeLayoutFor(m.width, m.height, m.effectiveSidebarWidth())
	bar := m.renderKnowledgeBar()

	m.clampKnowledgeListScroll(m.knowledge.selected, lay.visibleRows)

	listFocused := !m.knowledge.focusDetail
	listPane := m.renderKnowledgeList(lay, listFocused)
	detailPane, clamped := m.renderKnowledgeDetail(lay, m.knowledge.focusDetail)
	m.knowledge.contentScroll = clamped

	panes := lipgloss.JoinHorizontal(lipgloss.Top, listPane, detailPane)
	return lipgloss.JoinVertical(lipgloss.Left, panes, bar)
}

// renderKnowledgeBar renders the help line spanning the full width, shown at
// the bottom of the screen.
func (m *Model) renderKnowledgeBar() string {
	var text string
	switch {
	case m.knowledge.jobID == "":
		text = "  No job selected — pick one in the Jobs pane, then reopen   ·   ctrl+k / esc: close"
	case m.knowledge.focusDetail:
		text = "  ↑↓: scroll   ·   esc / ←: back to list   ·   ctrl+k: close"
	default:
		text = "  ↑↓: navigate   ·   enter/→: read note   ·   r: refresh   ·   ctrl+k / esc: close"
	}
	return DimStyle.Render(fitLine(text, m.width))
}

// renderKnowledgeList renders the left pane: a bordered, scrollable list of
// note rows. Mirrors renderNodeList.
func (m *Model) renderKnowledgeList(lay knowledgeLayout, focused bool) string {
	borderColor := ColorBorder
	if focused {
		borderColor = ColorAccent
	}
	box := lipgloss.NewStyle().
		Width(lay.listW).
		Height(lay.paneH).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1)

	innerW := lay.listInnerW

	title := gradientText("Knowledge", [3]uint8{100, 200, 150}, [3]uint8{50, 150, 255})
	if focused {
		title = rainbowText("Knowledge", m.spinnerFrame)
	}
	count := DimStyle.Render(fmt.Sprintf("%d", len(m.knowledge.notes)))
	gap := innerW - lipgloss.Width(title) - lipgloss.Width(count)
	if gap < 1 {
		gap = 1
	}
	var lines []string
	lines = append(lines, title+strings.Repeat(" ", gap)+count)
	lines = append(lines, "")

	switch {
	case m.knowledge.jobID == "":
		lines = append(lines, PlaceholderPaneStyle.Render("No job selected"))
		return box.Render(strings.Join(lines, "\n"))
	case m.knowledge.loading && len(m.knowledge.notes) == 0:
		lines = append(lines, DimStyle.Render("Loading..."))
		return box.Render(strings.Join(lines, "\n"))
	case m.knowledge.err != nil && len(m.knowledge.notes) == 0:
		lines = append(lines, ErrorStyle.Render(fitLine("Error: "+m.knowledge.err.Error(), innerW)))
		return box.Render(strings.Join(lines, "\n"))
	case len(m.knowledge.notes) == 0:
		lines = append(lines, PlaceholderPaneStyle.Render("No notes yet"))
		return box.Render(strings.Join(lines, "\n"))
	}

	// Window the item list around the current scroll offset.
	start := m.knowledge.listScroll
	if start > len(m.knowledge.notes)-1 {
		start = len(m.knowledge.notes) - 1
	}
	if start < 0 {
		start = 0
	}
	end := start + lay.visibleRows
	if end > len(m.knowledge.notes) {
		end = len(m.knowledge.notes)
	}

	for i := start; i < end; i++ {
		if i > start {
			lines = append(lines, "")
		}
		lines = append(lines, m.renderKnowledgeRow(m.knowledge.notes[i], innerW, i == m.knowledge.selected, focused)...)
	}

	if start > 0 || end < len(m.knowledge.notes) {
		hint := fmt.Sprintf("  ↕ %d–%d of %d", start+1, end, len(m.knowledge.notes))
		lines = append(lines, DimStyle.Render(hint))
	}

	return box.Render(strings.Join(lines, "\n"))
}

// renderKnowledgeRow renders one note as a fixed-height row: a
// selection/status gutter, the title, and a dim "source · age" line.
// Mirrors renderNodeRow.
func (m *Model) renderKnowledgeRow(n service.NoteMeta, innerW int, selected, listFocused bool) []string {
	cardW := innerW - 2 // gutter (1) + space (1)
	if cardW < 1 {
		cardW = 1
	}

	gutterColor := ColorSecondary
	glyph := "▕"
	if selected {
		glyph = "▐"
		gutterColor = ColorAccent
		if !listFocused {
			gutterColor = ColorSecondary
		}
	}
	gutter := lipgloss.NewStyle().Foreground(gutterColor).Render(glyph)

	title := truncateStr(n.Title, cardW)
	meta := compactAge(n.ModTime) + " ago"
	if n.Source != "" {
		meta = n.Source + " · " + meta
	}
	metaLine := DimStyle.Render(truncateStr(meta, cardW))

	return []string{gutter + " " + title, gutter + " " + metaLine}
}

// renderKnowledgeDetail renders the content pane for the selected note into
// a bordered box of the given outer dimensions. Returns the clamped scroll
// offset so the caller can write it back (mirrors renderDetailPane).
func (m *Model) renderKnowledgeDetail(lay knowledgeLayout, focused bool) (string, int) {
	borderColor := ColorBorder
	if focused {
		borderColor = ColorAccent
	}
	box := lipgloss.NewStyle().
		Width(lay.detailW).
		Height(lay.paneH).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1)

	innerW := lay.detailInnerW
	innerH := lay.detailInnerH
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 3 {
		innerH = 3
	}

	var sel *service.NoteMeta
	if m.knowledge.selected >= 0 && m.knowledge.selected < len(m.knowledge.notes) {
		sel = &m.knowledge.notes[m.knowledge.selected]
	}

	title := "Note"
	if sel != nil {
		title = sel.Title
	}
	titleLine := fitLine(HeaderStyle.Render(title), innerW)

	visibleH := innerH - 2 // title row + blank
	if visibleH < 1 {
		visibleH = 1
	}

	scroll := m.knowledge.contentScroll
	var bodyLines []string
	switch {
	case sel == nil:
		bodyLines = []string{PlaceholderPaneStyle.Render("Select a note to read it")}
		scroll = 0
	case m.knowledge.loading:
		bodyLines = []string{DimStyle.Render("Loading...")}
		scroll = 0
	case m.knowledge.err != nil:
		bodyLines = []string{ErrorStyle.Render("Error: " + m.knowledge.err.Error())}
		scroll = 0
	default:
		body := stripNoteTitleHeading(m.knowledge.content, sel.Title)
		bodyLines = wrapLogLines(strings.Split(body, "\n"), innerW)
	}

	maxScroll := len(bodyLines) - visibleH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll == scrollBottom || scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}

	end := scroll + visibleH
	if end > len(bodyLines) {
		end = len(bodyLines)
	}
	visible := bodyLines[scroll:end]
	truncated := make([]string, len(visible))
	for i, l := range visible {
		truncated[i] = fitLine(l, innerW)
	}
	for len(truncated) < visibleH {
		truncated = append(truncated, "")
	}

	content := titleLine + "\n\n" + strings.Join(truncated, "\n")
	return box.Render(content), scroll
}

// stripNoteTitleHeading drops a leading "# <title>" H1 line (and the blank
// line under it) from a note's content when it matches title. job_note_write
// stores notes as "# <title>\n\n<body>", so the detail pane — which already
// renders the title as its header — would otherwise show it twice.
func stripNoteTitleHeading(content, title string) string {
	parts := strings.SplitN(content, "\n", 2)
	firstLine := strings.TrimSpace(parts[0])
	if strings.HasPrefix(firstLine, "#") &&
		strings.TrimSpace(strings.TrimLeft(firstLine, "#")) == strings.TrimSpace(title) {
		if len(parts) == 2 {
			return strings.TrimLeft(parts[1], "\n")
		}
		return ""
	}
	return content
}
