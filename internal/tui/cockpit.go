// Worker cockpit: a tabbed, scrollable, near-fullscreen overlay for inspecting
// one runtime session — its live output (styled like the chat), its prompt
// (markdown-formatted), and its stats. Opened from the grid drill-in.
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// scrollBottom is a large sentinel scroll offset meaning "tail to the bottom";
// renderCockpit clamps it to the true maximum on the next render.
const scrollBottom = 1 << 30

// cockpitTabNames are the tab labels, indexed by cockpitTab.
var cockpitTabNames = [cockpitTabCount]string{"Output", "Prompt", "Stats"}

// openCockpit shows the cockpit for a session at the given tab, resetting scroll.
// The Output tab starts tailed to the bottom so a running worker's latest output
// is visible immediately.
func (m *Model) openCockpit(sessionID string, tab cockpitTab) {
	m.cockpit.show = true
	m.cockpit.sessionID = sessionID
	m.cockpit.tab = tab
	m.cockpit.userScrolled = false
	m.cockpit.scroll = [cockpitTabCount]int{}
	if tab == cockpitTabOutput {
		m.cockpit.scroll[cockpitTabOutput] = scrollBottom
	}
}

// updateCockpit handles key events while the cockpit overlay is visible. It
// intercepts all keys: tab switching, per-tab scrolling, and dismiss. Upward
// movement on the Output tab sets userScrolled so live events stop auto-tailing.
func (m *Model) updateCockpit(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	tab := m.cockpit.tab
	switch msg.String() {
	case "esc", "q", "ctrl+g":
		m.cockpit.show = false
		m.cockpit.sessionID = ""
		m.cockpit.userScrolled = false
	case "tab", "right", "l":
		m.cockpit.tab = (tab + 1) % cockpitTabCount
	case "shift+tab", "left", "h":
		m.cockpit.tab = (tab + cockpitTabCount - 1) % cockpitTabCount
	case "1":
		m.cockpit.tab = cockpitTabOutput
	case "2":
		m.cockpit.tab = cockpitTabPrompt
	case "3":
		m.cockpit.tab = cockpitTabStats
	case "up", "k":
		if m.cockpit.scroll[tab] > 0 {
			m.cockpit.scroll[tab]--
			m.markCockpitScrolled(tab)
		}
	case "down", "j":
		m.cockpit.scroll[tab]++
	case "ctrl+u", "pgup":
		m.cockpit.scroll[tab] -= 10
		if m.cockpit.scroll[tab] < 0 {
			m.cockpit.scroll[tab] = 0
		}
		m.markCockpitScrolled(tab)
	case "ctrl+d", "pgdown":
		m.cockpit.scroll[tab] += 10
	case "g", "home":
		m.cockpit.scroll[tab] = 0
		m.markCockpitScrolled(tab)
	case "G", "end":
		// Jump to bottom; renderCockpit clamps. Re-enables Output auto-tail.
		m.cockpit.scroll[tab] = scrollBottom
		if tab == cockpitTabOutput {
			m.cockpit.userScrolled = false
		}
	}
	return m, nil
}

// markCockpitScrolled records that the user scrolled up on the Output tab so
// incoming session events stop yanking the view back to the bottom.
func (m *Model) markCockpitScrolled(tab cockpitTab) {
	if tab == cockpitTabOutput {
		m.cockpit.userScrolled = true
	}
}

// refreshCockpitAutoTail re-tails the Output tab when a new event arrives for
// the viewed session and the user hasn't scrolled up. Setting the sentinel lets
// the next render clamp to the true bottom, matching the "G" behavior.
func (m *Model) refreshCockpitAutoTail(sessionID string) {
	if !m.cockpit.show || m.cockpit.sessionID != sessionID {
		return
	}
	if m.cockpit.tab == cockpitTabOutput && !m.cockpit.userScrolled {
		m.cockpit.scroll[cockpitTabOutput] = scrollBottom
	}
}

// cockpitDims returns the modal's total height and its visible body height,
// mirroring the fullscreen layout: tab bar + blank + body + blank + footer,
// inside a bordered box.
func (m *Model) cockpitDims() (modalH, visibleH int) {
	modalH = m.height - 4
	if modalH < 10 {
		modalH = 10
	}
	visibleH = modalH - 4 // tab bar + footer + borders/blank rows
	if visibleH < 1 {
		visibleH = 1
	}
	return modalH, visibleH
}

// renderCockpit renders the cockpit overlay and returns it along with the
// clamped scroll offset for the active tab (which the caller writes back).
func (m *Model) renderCockpit() (string, int) {
	modalW := m.width - 4
	if modalW < 40 {
		modalW = 40
	}
	modalH, visibleH := m.cockpitDims()

	modalStyle := lipgloss.NewStyle().
		Width(modalW).
		Height(modalH).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(0, 2)

	// Derive the content width from the style's actual frame (border + padding
	// on both sides) so no chrome line can overflow and widen the box.
	innerW := modalW - modalStyle.GetHorizontalFrameSize()
	if innerW < 1 {
		innerW = 1
	}

	slot := m.runtimeSessions[m.cockpit.sessionID]
	tab := m.cockpit.tab

	// Every chrome line must fit innerW: a line wider than the content area makes
	// lipgloss widen the whole box, which throws off the centered overlay.
	tabBar := fitLine(m.renderCockpitTabBar(slot, innerW), innerW)
	bodyLines := m.cockpitBodyLines(slot, innerW)

	scroll := m.cockpit.scroll[tab]
	maxScroll := len(bodyLines) - visibleH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
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

	// ANSI-aware truncation so we never slice mid-escape-sequence.
	truncated := make([]string, len(visible))
	for i, l := range visible {
		if lipgloss.Width(l) > innerW {
			truncated[i] = xansi.Truncate(l, innerW, "")
		} else {
			truncated[i] = l
		}
	}
	body := strings.Join(truncated, "\n")

	footer := fitLine(m.renderCockpitFooter(tab, scroll, len(bodyLines), innerW), innerW)

	modalContent := tabBar + "\n\n" + body + "\n\n" + footer
	modal := modalStyle.Render(modalContent)

	overlaid := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))))
	return overlaid, scroll
}

// renderCockpitTabBar renders the tab labels (active highlighted) on the left and
// the session's identity on the right.
func (m *Model) renderCockpitTabBar(slot *runtimeSlot, innerW int) string {
	segs := make([]string, 0, cockpitTabCount)
	for i, name := range cockpitTabNames {
		if cockpitTab(i) == m.cockpit.tab {
			segs = append(segs, HeaderStyle.Render(name))
		} else {
			segs = append(segs, DimStyle.Render(name))
		}
	}
	tabs := strings.Join(segs, DimStyle.Render("  ·  "))

	title := cockpitTitle(slot)
	gap := innerW - lipgloss.Width(tabs) - lipgloss.Width(title)
	if gap < 1 {
		return tabs
	}
	return tabs + strings.Repeat(" ", gap) + DimStyle.Render(title)
}

// cockpitTitle returns a short "<job>:<role>" identity for the tab bar.
func cockpitTitle(slot *runtimeSlot) string {
	if slot == nil {
		return ""
	}
	role := strings.TrimPrefix(slot.workerName, "graph:")
	if role == "" {
		role = "worker"
	}
	shortJob := slot.jobID
	if len(shortJob) > 8 {
		shortJob = shortJob[:8]
	}
	if shortJob == "" {
		return role
	}
	return shortJob + ":" + role
}

// renderCockpitFooter renders the key hints and the scroll position indicator.
func (m *Model) renderCockpitFooter(tab cockpitTab, scroll, total, innerW int) string {
	keys := "↑↓ scroll · Tab tabs · Esc close"
	pos := fmt.Sprintf("%d/%d", scroll+1, maxInt(total, 1))
	if tab == cockpitTabOutput && !m.cockpit.userScrolled {
		pos = "tailing · " + pos
	}
	gap := innerW - lipgloss.Width(keys) - lipgloss.Width(pos)
	if gap < 1 {
		return DimStyle.Render(keys)
	}
	return DimStyle.Render(keys) + strings.Repeat(" ", gap) + DimStyle.Render(pos)
}

// fitLine truncates a (possibly styled) line to at most w display columns using
// an ANSI-aware truncator so escape sequences are never split.
func fitLine(s string, w int) string {
	if lipgloss.Width(s) > w {
		return xansi.Truncate(s, w, "")
	}
	return s
}

// cockpitBodyLines returns the fully-rendered, wrapped body lines for the active
// tab. Splitting into lines here lets renderCockpit apply the scroll window.
func (m *Model) cockpitBodyLines(slot *runtimeSlot, innerW int) []string {
	if slot == nil {
		return []string{DimStyle.Italic(true).Render("(session not found)")}
	}
	switch m.cockpit.tab {
	case cockpitTabPrompt:
		return m.cockpitPromptLines(slot, innerW)
	case cockpitTabStats:
		return m.cockpitStatsLines(slot, innerW)
	default:
		return m.cockpitOutputLines(slot, innerW)
	}
}

// cockpitOutputLines renders the session's live output the same way the chat
// does — glamour text runs and styled tool blocks — with any chain-of-thought
// reasoning shown as a dimmed block on top. Uses the cockpit-sized markdown
// renderer so the shared jobs-pane renderer isn't repointed at the modal width.
func (m *Model) cockpitOutputLines(slot *runtimeSlot, innerW int) []string {
	var sb strings.Builder
	if reasoning := strings.TrimSpace(slot.reasoning.String()); reasoning != "" {
		sb.WriteString(ReasoningHeaderStyle.Render("⟳ thinking"))
		sb.WriteString("\n")
		sb.WriteString(ReasoningStyle.Render(wrapText(reasoning, innerW)))
		sb.WriteString("\n\n")
	}
	sb.WriteString(renderOutputItems(slot.items, innerW, m.outputMdRender))

	out := strings.TrimRight(sb.String(), "\n")
	if strings.TrimSpace(xansi.Strip(out)) == "" {
		if slot.status == "active" {
			return []string{DimStyle.Italic(true).Render("(waiting for output…)")}
		}
		return []string{DimStyle.Italic(true).Render("(no output)")}
	}
	return strings.Split(out, "\n")
}

// cockpitPromptLines renders the session's system prompt and initial message as
// markdown, each under a styled section header.
func (m *Model) cockpitPromptLines(slot *runtimeSlot, innerW int) []string {
	if slot.systemPrompt == "" && slot.initialMessage == "" {
		return []string{DimStyle.Italic(true).Render("(no prompt captured)")}
	}
	var lines []string
	section := func(title, content string) {
		if content == "" {
			return
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, HeaderStyle.Render(title))
		lines = append(lines, "")
		rendered := renderMarkdownWith(m.outputMdRender, content)
		lines = append(lines, strings.Split(rendered, "\n")...)
	}
	section("System Prompt", slot.systemPrompt)
	section("Initial Message", slot.initialMessage)
	return lines
}

// cockpitStatsLines renders a labeled stats block: identity, status/timing,
// model/sampling, a live context-window bar, and token/cost/throughput totals.
func (m *Model) cockpitStatsLines(slot *runtimeSlot, innerW int) []string {
	var lines []string
	row := func(label, value string) {
		if value == "" {
			return
		}
		lines = append(lines, SidebarLabelStyle.Render(fmt.Sprintf("%-14s", label))+SidebarValueStyle.Render(value))
	}
	header := func(title string) {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, HeaderStyle.Render(title))
	}

	end := time.Now()
	if !slot.endTime.IsZero() {
		end = slot.endTime
	}
	elapsed := end.Sub(slot.startTime).Round(time.Second)

	header("Session")
	row("Worker", gridWorkerLabel(slot))
	if slot.teamName != "" {
		row("Team", slot.teamName)
	}
	row("Job", slot.jobID)
	if slot.taskID != "" {
		row("Task", slot.taskID)
	}
	row("Status", slot.status)
	row("Elapsed", elapsed.String())

	header("Model")
	if slot.provider != "" && slot.model != "" {
		row("Model", slot.provider+"/"+slot.model)
	} else if slot.model != "" {
		row("Model", slot.model)
	} else if slot.provider != "" {
		row("Provider", slot.provider)
	}
	if slot.hasTemp {
		temp := fmt.Sprintf("%.1f", slot.temperature)
		if slot.thinking {
			temp += "  🧠 thinking"
		}
		row("Temperature", temp)
	}

	header("Context window")
	ctxMax := m.modelContext[slot.model]
	barW := innerW - 20
	if barW < 8 {
		barW = 8
	}
	lines = append(lines, "  "+renderMiniContextBar(int(slot.contextTokens), ctxMax, barW))
	if slot.contextTokens > 0 {
		detail := fmt.Sprintf("%s tokens", commaInt(int(slot.contextTokens)))
		if ctxMax > 0 {
			detail += fmt.Sprintf(" / %s", commaInt(ctxMax))
		}
		row("Occupancy", detail)
	}

	header("Usage")
	if slot.tokensIn > 0 || slot.tokensOut > 0 {
		row("Tokens", fmt.Sprintf("%s in · %s out", commaInt(int(slot.tokensIn)), commaInt(int(slot.tokensOut))))
	}
	if slot.costUSD > 0 {
		row("Cost", fmt.Sprintf("~$%.2f", slot.costUSD))
	}
	if elapsed > 0 && slot.tokensOut > 0 {
		row("Throughput", fmt.Sprintf("%.0f t/s", float64(slot.tokensOut)/elapsed.Seconds()))
	}
	return lines
}

// maxInt returns the larger of two ints.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
