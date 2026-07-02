// Nodes screen: a master-detail view of runtime worker sessions. The left pane
// is a scrollable list of rich node rows; the right pane is the tabbed detail
// view (see cockpit.go) for the selected node. Tab moves focus to the detail
// pane; Esc returns to the list. Replaces the old paginated grid.
package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	nodeRowContentH = 4                   // headline + model + context bar + latest activity
	nodeRowBlockH   = nodeRowContentH + 1 // + one blank separator row
	nodesBarH       = 1                   // help/filter bar (bottom)
)

// nodesLayout holds the derived geometry of the nodes screen, shared by the
// render and key-handling paths so list scrolling can never drift out of sync.
type nodesLayout struct {
	listW       int
	detailW     int
	paneH       int
	listInnerW  int
	visibleRows int
}

// nodesLayoutFor computes the nodes-screen geometry. listW is the desired list
// (left pane) width — passed in so it can match the main screen's sidebar,
// making the screen read as a drill-in of the Fleet pane. It's clamped so the
// detail pane keeps a usable minimum.
func nodesLayoutFor(termW, termH, listW int) nodesLayout {
	if listW > termW-24 {
		listW = termW - 24
	}
	if listW < 20 {
		listW = 20
	}

	paneH := termH - nodesBarH
	if paneH < 3 {
		paneH = 3
	}

	// The panes are rounded-bordered with 1 col of horizontal padding each side.
	const paneFrameH = 4 // border (2) + padding (2)
	const paneFrameV = 2 // border top+bottom
	listInnerW := listW - paneFrameH
	if listInnerW < 1 {
		listInnerW = 1
	}
	// List body = inner height minus the title row and the blank under it.
	listInnerH := paneH - paneFrameV - 2
	if listInnerH < nodeRowBlockH {
		listInnerH = nodeRowBlockH
	}
	visibleRows := listInnerH / nodeRowBlockH
	if visibleRows < 1 {
		visibleRows = 1
	}

	return nodesLayout{
		listW:       listW,
		detailW:     termW - listW,
		paneH:       paneH,
		listInnerW:  listInnerW,
		visibleRows: visibleRows,
	}
}

// openNodes shows the nodes screen with the list focused and the selection
// reset to the top (Output tab, tailed).
func (m *Model) openNodes() {
	m.nodes.show = true
	m.nodes.focusDetail = false
	m.nodes.filterActive = false
	m.nodes.confirmKill = false
	m.nodes.tab = cockpitTabOutput
	m.resetNodeSelection()
}

// toggleNodes shows or hides the nodes screen.
func (m *Model) toggleNodes() {
	if m.nodes.show {
		m.nodes.show = false
		return
	}
	m.openNodes()
}

// renderNodes renders the full nodes screen: the list (left) and detail (right)
// panes above a help/filter bar at the bottom.
func (m *Model) renderNodes() string {
	lay := nodesLayoutFor(m.width, m.height, m.effectiveSidebarWidth())
	nodes := m.filteredNodeSessions()

	bar := m.renderNodesBar()

	// Resolve the selection by id and keep it visible even if the list reordered
	// since the last key press (a node finishing shuffles the groups).
	idx := m.currentNodeIndex(nodes)
	m.clampListScroll(idx, lay.visibleRows)

	listFocused := !m.nodes.focusDetail
	listPane := m.renderNodeList(nodes, idx, lay, listFocused)

	var sel *runtimeSlot
	if len(nodes) > 0 {
		sel = nodes[idx]
	}
	detailPane, clamped := m.renderDetailPane(sel, lay.detailW, lay.paneH, m.nodes.focusDetail)
	m.nodes.tabScroll[m.nodes.tab] = clamped

	panes := lipgloss.JoinHorizontal(lipgloss.Top, listPane, detailPane)
	return lipgloss.JoinVertical(lipgloss.Left, panes, bar)
}

// renderNodesBar renders the help/filter line spanning the full width, shown at the bottom of the screen.
func (m *Model) renderNodesBar() string {
	var text string
	switch {
	case m.nodes.filterActive:
		matches := len(m.filteredNodeSessions())
		text = fmt.Sprintf("  filter: %s_   ·   %d match(es)   ·   enter: apply   ·   esc: clear", m.nodes.filterQuery, matches)
	case m.nodes.focusDetail:
		text = "  ←→: tabs   ·   ↑↓: scroll   ·   x: kill   ·   esc / shift+tab: back to list   ·   ctrl+g: close"
	default:
		text = "  ↑↓: navigate   ·   tab/enter: open detail   ·   x: kill   ·   /: filter   ·   ctrl+g / esc: close"
		if m.nodes.filterQuery != "" {
			text += fmt.Sprintf("   ·   filter: %s", m.nodes.filterQuery)
		}
	}
	return DimStyle.Render(fitLine(text, m.width))
}

// renderNodeList renders the left pane: a bordered, scrollable list of node
// rows. The selected row is marked with a cyan gutter; the border is cyan when
// the list has focus, dim otherwise.
func (m *Model) renderNodeList(nodes []*runtimeSlot, selIdx int, lay nodesLayout, focused bool) string {
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

	// Title: "Fleet" + count — matches the main screen's Fleet pane header.
	title := gradientText("Fleet", [3]uint8{50, 130, 255}, [3]uint8{0, 200, 200})
	if focused {
		title = rainbowText("Fleet", m.spinnerFrame)
	}
	count := DimStyle.Render(fmt.Sprintf("%d", len(nodes)))
	gap := innerW - lipgloss.Width(title) - lipgloss.Width(count)
	if gap < 1 {
		gap = 1
	}
	var lines []string
	lines = append(lines, title+strings.Repeat(" ", gap)+count)
	lines = append(lines, "")

	if len(nodes) == 0 {
		lines = append(lines, PlaceholderPaneStyle.Render("No nodes"))
		return box.Render(strings.Join(lines, "\n"))
	}

	// Window the item list around the current scroll offset.
	start := m.nodes.listScroll
	if start > len(nodes)-1 {
		start = len(nodes) - 1
	}
	if start < 0 {
		start = 0
	}
	end := start + lay.visibleRows
	if end > len(nodes) {
		end = len(nodes)
	}

	for i := start; i < end; i++ {
		if i > start {
			lines = append(lines, "")
		}
		lines = append(lines, m.renderNodeRow(nodes[i], innerW, i == selIdx, focused)...)
	}

	// Overflow hint when there are rows off-screen.
	if start > 0 || end < len(nodes) {
		hint := fmt.Sprintf("  ↕ %d–%d of %d", start+1, end, len(nodes))
		lines = append(lines, DimStyle.Render(hint))
	}

	return box.Render(strings.Join(lines, "\n"))
}

// renderNodeRow renders one node as a fixed-height row: a selection/status
// gutter followed by the compact worker card. Selected rows get a cyan gutter
// (dimmed when the list isn't focused); others get the status color.
func (m *Model) renderNodeRow(rs *runtimeSlot, innerW int, selected, listFocused bool) []string {
	cardW := innerW - 2 // gutter (1) + space (1)
	if cardW < 1 {
		cardW = 1
	}
	ctxMax := m.slotCtxMax(rs)
	card := renderWorkerCard(rs, cardW, nodeRowContentH, ctxMax, float64(m.workerCompactionThreshold)/100, selected, m.spinnerFrame)
	cardLines := strings.Split(card, "\n")
	for len(cardLines) < nodeRowContentH {
		cardLines = append(cardLines, "")
	}

	gutterColor := nodeStatusColor(rs) // status color for unselected
	glyph := "▕"
	if selected {
		glyph = "▐"
		gutterColor = ColorAccent
		if !listFocused {
			gutterColor = ColorSecondary
		}
	}
	gutter := lipgloss.NewStyle().Foreground(gutterColor).Render(glyph)

	out := make([]string, len(cardLines))
	for i, l := range cardLines {
		out[i] = gutter + " " + l
	}
	return out
}

// updateNodes routes key events for the nodes screen. Filter capture and a
// pending kill confirmation take priority; otherwise keys go to the list or the
// detail pane depending on which has focus.
func (m *Model) updateNodes(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.nodes.filterActive {
		return m.updateNodesFilter(msg)
	}

	// A pending kill confirmation intercepts everything: Enter kills the
	// selected node, any other key cancels.
	if m.nodes.confirmKill {
		m.nodes.confirmKill = false
		if msg.String() == "enter" {
			return m, m.killWorkerSession(m.selectedNodeSessionID())
		}
		return m, nil
	}

	nodes := m.filteredNodeSessions()
	if m.nodes.focusDetail {
		return m.updateNodesDetail(msg, nodes)
	}
	return m.updateNodesList(msg, nodes)
}

// updateNodesList handles keys while the list (master) pane has focus.
func (m *Model) updateNodesList(msg tea.KeyPressMsg, nodes []*runtimeSlot) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+g", "esc":
		m.nodes.show = false
	case "up", "k":
		if idx := m.currentNodeIndex(nodes); idx > 0 {
			m.selectNode(nodes[idx-1].sessionID)
		}
	case "down", "j":
		if idx := m.currentNodeIndex(nodes); idx < len(nodes)-1 {
			m.selectNode(nodes[idx+1].sessionID)
		}
	case "enter", "tab", "right", "l":
		if len(nodes) > 0 {
			m.nodes.focusDetail = true
		}
	case "x":
		m.armNodeKill(nodes)
	case "/":
		m.nodes.filterActive = true
	}
	return m, nil
}

// updateNodesDetail handles keys while the detail pane has focus.
func (m *Model) updateNodesDetail(msg tea.KeyPressMsg, nodes []*runtimeSlot) (tea.Model, tea.Cmd) {
	tab := m.nodes.tab
	switch msg.String() {
	case "ctrl+g":
		m.nodes.show = false
	case "esc", "shift+tab":
		// Both return focus to the list; only left/right cycle tabs.
		m.nodes.focusDetail = false
	case "right", "l":
		m.nodes.tab = (tab + 1) % cockpitTabCount
	case "left", "h":
		m.nodes.tab = (tab + cockpitTabCount - 1) % cockpitTabCount
	case "1":
		m.nodes.tab = cockpitTabOutput
	case "2":
		m.nodes.tab = cockpitTabPrompt
	case "3":
		m.nodes.tab = cockpitTabStats
	case "up", "k":
		if m.nodes.tabScroll[tab] > 0 {
			m.nodes.tabScroll[tab]--
			m.markNodeScrolled(tab)
		}
	case "down", "j":
		m.nodes.tabScroll[tab]++
	case "ctrl+u", "pgup":
		m.nodes.tabScroll[tab] -= 10
		if m.nodes.tabScroll[tab] < 0 {
			m.nodes.tabScroll[tab] = 0
		}
		m.markNodeScrolled(tab)
	case "ctrl+d", "pgdown":
		m.nodes.tabScroll[tab] += 10
	case "g", "home":
		m.nodes.tabScroll[tab] = 0
		m.markNodeScrolled(tab)
	case "G", "end":
		m.nodes.tabScroll[tab] = scrollBottom
		if tab == cockpitTabOutput {
			m.nodes.userScrolled = false
		}
	case "x":
		m.armNodeKill(nodes)
	}
	return m, nil
}

// updateNodesFilter handles keystrokes while the list filter is being typed.
func (m *Model) updateNodesFilter(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.nodes.filterActive = false
		m.nodes.filterQuery = ""
		m.resetNodeSelection()
	case "enter":
		m.nodes.filterActive = false
	case "backspace":
		if n := len(m.nodes.filterQuery); n > 0 {
			m.nodes.filterQuery = m.nodes.filterQuery[:n-1]
			m.resetNodeSelection()
		}
	default:
		if msg.Text != "" {
			m.nodes.filterQuery += msg.Text
			m.resetNodeSelection()
		}
	}
	return m, nil
}

// currentNodeIndex resolves the selected node's index in the given list by its
// session id, so live reordering (a worker finishing moves to the finished
// group, a new one spawning shifts positions) keeps the selection pinned to the
// same node. Falls back to a clamped 0 when the selection is gone or unset.
func (m *Model) currentNodeIndex(nodes []*runtimeSlot) int {
	for i, rs := range nodes {
		if rs.sessionID == m.nodes.selID {
			return i
		}
	}
	return 0
}

// clampListScroll shifts the list viewport so the selected row (at idx) stays
// visible, given how many rows fit.
func (m *Model) clampListScroll(idx, visibleRows int) {
	if idx < m.nodes.listScroll {
		m.nodes.listScroll = idx
	}
	if idx >= m.nodes.listScroll+visibleRows {
		m.nodes.listScroll = idx - visibleRows + 1
	}
	if m.nodes.listScroll < 0 {
		m.nodes.listScroll = 0
	}
}

// selectNode moves the selection to the given session id and resets the detail
// pane to a fresh, tailed view of it. The row is kept visible on next render.
func (m *Model) selectNode(sessionID string) {
	m.nodes.selID = sessionID
	m.nodes.tabScroll = [cockpitTabCount]int{}
	m.nodes.tabScroll[cockpitTabOutput] = scrollBottom
	m.nodes.userScrolled = false
}

// armNodeKill arms the kill confirmation for the selected node, but only for a
// live, real worker session (graph pseudo-sessions have no runtime.Session).
func (m *Model) armNodeKill(nodes []*runtimeSlot) {
	if len(nodes) == 0 {
		return
	}
	rs := nodes[m.currentNodeIndex(nodes)]
	if rs.status == "active" && !strings.HasPrefix(rs.sessionID, "graph:") {
		m.nodes.confirmKill = true
	}
}

// resetNodeSelection selects the first node (top of the list) after a filter
// change or when the screen opens.
func (m *Model) resetNodeSelection() {
	m.nodes.listScroll = 0
	nodes := m.filteredNodeSessions()
	if len(nodes) > 0 {
		m.selectNode(nodes[0].sessionID)
	} else {
		m.selectNode("")
	}
}

// markNodeScrolled records a manual upward scroll on the Output tab so incoming
// events stop auto-tailing.
func (m *Model) markNodeScrolled(tab cockpitTab) {
	if tab == cockpitTabOutput {
		m.nodes.userScrolled = true
	}
}

// refreshNodesAutoTail re-tails the detail Output tab when a new event arrives
// for the selected node and the user hasn't scrolled up.
func (m *Model) refreshNodesAutoTail(sessionID string) {
	if !m.nodes.show || m.nodes.tab != cockpitTabOutput || m.nodes.userScrolled {
		return
	}
	if m.selectedNodeSessionID() == sessionID {
		m.nodes.tabScroll[cockpitTabOutput] = scrollBottom
	}
}

// selectedNode returns the currently-selected runtime slot, or nil.
func (m *Model) selectedNode() *runtimeSlot {
	nodes := m.filteredNodeSessions()
	if len(nodes) == 0 {
		return nil
	}
	return nodes[m.currentNodeIndex(nodes)]
}

// selectedNodeSessionID returns the selected node's session ID, or "".
func (m *Model) selectedNodeSessionID() string {
	if rs := m.selectedNode(); rs != nil {
		return rs.sessionID
	}
	return ""
}
