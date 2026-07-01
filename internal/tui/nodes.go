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
	nodesBarH       = 1                   // top help/filter bar
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

// nodesLayoutFor computes the nodes-screen geometry for the given terminal size.
func nodesLayoutFor(termW, termH int) nodesLayout {
	listW := termW * 2 / 5
	if listW < 34 {
		listW = 34
	}
	if listW > 56 {
		listW = 56
	}
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

// renderNodes renders the full nodes screen: a top help/filter bar over the
// list (left) and detail (right) panes.
func (m *Model) renderNodes() string {
	lay := nodesLayoutFor(m.width, m.height)
	nodes := m.filteredNodeSessions()

	bar := m.renderNodesBar()

	listFocused := !m.nodes.focusDetail
	listPane := m.renderNodeList(nodes, lay, listFocused)

	var sel *runtimeSlot
	if m.nodes.sel >= 0 && m.nodes.sel < len(nodes) {
		sel = nodes[m.nodes.sel]
	}
	detailPane, clamped := m.renderDetailPane(sel, lay.detailW, lay.paneH, m.nodes.focusDetail)
	m.nodes.tabScroll[m.nodes.tab] = clamped

	panes := lipgloss.JoinHorizontal(lipgloss.Top, listPane, detailPane)
	return lipgloss.JoinVertical(lipgloss.Left, bar, panes)
}

// renderNodesBar renders the top help/filter line spanning the full width.
func (m *Model) renderNodesBar() string {
	var text string
	switch {
	case m.nodes.filterActive:
		matches := len(m.filteredNodeSessions())
		text = fmt.Sprintf("  filter: %s_   ·   %d match(es)   ·   enter: apply   ·   esc: clear", m.nodes.filterQuery, matches)
	case m.nodes.focusDetail:
		text = "  ←→: tabs   ·   ↑↓: scroll   ·   x: kill   ·   esc: back to list   ·   ctrl+g: close"
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
func (m *Model) renderNodeList(nodes []*runtimeSlot, lay nodesLayout, focused bool) string {
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

	// Title: "Nodes" + count.
	title := gradientText("Nodes", [3]uint8{0, 200, 200}, [3]uint8{50, 130, 255})
	if focused {
		title = rainbowText("Nodes", m.spinnerFrame)
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
		lines = append(lines, m.renderNodeRow(nodes[i], innerW, i == m.nodes.sel, focused)...)
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
	ctxMax := m.modelContext[rs.model]
	card := renderWorkerCard(rs, cardW, nodeRowContentH, ctxMax, selected, m.spinnerFrame)
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
		if m.nodes.sel > 0 {
			m.nodes.sel--
			m.onNodeSelectionChanged()
		}
	case "down", "j":
		if m.nodes.sel < len(nodes)-1 {
			m.nodes.sel++
			m.onNodeSelectionChanged()
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
	case "esc":
		m.nodes.focusDetail = false
	case "tab", "right", "l":
		m.nodes.tab = (tab + 1) % cockpitTabCount
	case "shift+tab", "left", "h":
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

// armNodeKill arms the kill confirmation for the selected node, but only for a
// live, real worker session (graph pseudo-sessions have no runtime.Session).
func (m *Model) armNodeKill(nodes []*runtimeSlot) {
	if m.nodes.sel < 0 || m.nodes.sel >= len(nodes) {
		return
	}
	rs := nodes[m.nodes.sel]
	if rs.status == "active" && !strings.HasPrefix(rs.sessionID, "graph:") {
		m.nodes.confirmKill = true
	}
}

// onNodeSelectionChanged keeps the selected row visible and resets the detail
// pane to a fresh, tailed view of the newly-selected node.
func (m *Model) onNodeSelectionChanged() {
	lay := nodesLayoutFor(m.width, m.height)
	if m.nodes.sel < m.nodes.listScroll {
		m.nodes.listScroll = m.nodes.sel
	}
	if m.nodes.sel >= m.nodes.listScroll+lay.visibleRows {
		m.nodes.listScroll = m.nodes.sel - lay.visibleRows + 1
	}
	if m.nodes.listScroll < 0 {
		m.nodes.listScroll = 0
	}
	m.nodes.tabScroll = [cockpitTabCount]int{}
	m.nodes.tabScroll[cockpitTabOutput] = scrollBottom
	m.nodes.userScrolled = false
}

// resetNodeSelection returns the list to the top after a filter change.
func (m *Model) resetNodeSelection() {
	m.nodes.sel = 0
	m.nodes.listScroll = 0
	m.onNodeSelectionChanged()
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
	if m.nodes.sel < 0 || m.nodes.sel >= len(nodes) {
		return nil
	}
	return nodes[m.nodes.sel]
}

// selectedNodeSessionID returns the selected node's session ID, or "".
func (m *Model) selectedNodeSessionID() string {
	if rs := m.selectedNode(); rs != nil {
		return rs.sessionID
	}
	return ""
}
