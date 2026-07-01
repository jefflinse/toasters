// Node detail pane: a tabbed, scrollable view of one runtime session — its live
// output (styled like the chat), its prompt (markdown-formatted), and its stats.
// Rendered into the right-hand pane of the nodes screen (see nodes.go).
package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

// scrollBottom is a large sentinel scroll offset meaning "tail to the bottom";
// the render pass clamps it to the true maximum.
const scrollBottom = 1 << 30

// cockpitTabNames are the tab labels, indexed by cockpitTab.
var cockpitTabNames = [cockpitTabCount]string{"Output", "Prompt", "Stats"}

// renderDetailPane renders the detail pane for the selected node into a bordered
// box of the given outer dimensions. focused controls the border accent (cyan
// when the pane has keyboard focus, dim otherwise) and returns the clamped
// scroll offset for the active tab so the caller can write it back.
func (m *Model) renderDetailPane(slot *runtimeSlot, width, height int, focused bool) (string, int) {
	borderColor := ColorBorder
	if focused {
		borderColor = ColorAccent
	}
	boxStyle := lipgloss.NewStyle().
		Width(width).
		Height(height).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1)

	innerW := width - boxStyle.GetHorizontalFrameSize()
	innerH := height - boxStyle.GetVerticalFrameSize()
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 3 {
		innerH = 3
	}

	tab := m.nodes.tab
	visibleH := innerH - 4 // tab bar + blank + blank + footer
	if visibleH < 1 {
		visibleH = 1
	}

	tabBar := fitLine(m.renderDetailTabBar(slot, innerW), innerW)
	bodyLines := m.detailBodyLines(slot, innerW)

	scroll := m.nodes.tabScroll[tab]
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
	truncated := make([]string, len(visible))
	for i, l := range visible {
		truncated[i] = fitLine(l, innerW)
	}
	// Pad the body to a fixed height so the footer stays pinned to the bottom.
	for len(truncated) < visibleH {
		truncated = append(truncated, "")
	}
	body := strings.Join(truncated, "\n")

	var footer string
	if m.nodes.confirmKill {
		footer = fitLine(ModalWarningStyle.Render("⚠ Kill this worker?  [Enter] confirm   [Esc] cancel"), innerW)
	} else {
		footer = fitLine(m.renderDetailFooter(tab, scroll, len(bodyLines), focused, innerW), innerW)
	}

	content := tabBar + "\n\n" + body + "\n" + footer
	return boxStyle.Render(content), scroll
}

// renderDetailTabBar renders the tab labels (active highlighted) on the left and
// the session's identity on the right.
func (m *Model) renderDetailTabBar(slot *runtimeSlot, innerW int) string {
	segs := make([]string, 0, cockpitTabCount)
	for i, name := range cockpitTabNames {
		if cockpitTab(i) == m.nodes.tab {
			// Active tab uses the cycling rainbow, matching selected section
			// headers elsewhere, rather than the purple HeaderStyle.
			segs = append(segs, rainbowText(name, m.spinnerFrame))
		} else {
			segs = append(segs, DimStyle.Render(name))
		}
	}
	tabs := strings.Join(segs, DimStyle.Render("  ·  "))

	title := detailTitle(slot)
	gap := innerW - lipgloss.Width(tabs) - lipgloss.Width(title)
	if gap < 1 {
		return tabs
	}
	return tabs + strings.Repeat(" ", gap) + DimStyle.Render(title)
}

// detailTitle returns a short "<job>:<role>" identity for the tab bar.
func detailTitle(slot *runtimeSlot) string {
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

// renderDetailFooter renders the key hints and the scroll position indicator.
// The hints depend on whether the detail pane currently has focus.
func (m *Model) renderDetailFooter(tab cockpitTab, scroll, total int, focused bool, innerW int) string {
	keys := "Tab: focus detail"
	if focused {
		keys = "←→ tabs · ↑↓ scroll · x kill · Esc/shift+tab: list"
	}
	pos := fmt.Sprintf("%d/%d", scroll+1, maxInt(total, 1))
	if focused && tab == cockpitTabOutput && !m.nodes.userScrolled {
		pos = "tailing · " + pos
	}
	gap := innerW - lipgloss.Width(keys) - lipgloss.Width(pos)
	if gap < 1 {
		return DimStyle.Render(keys)
	}
	return DimStyle.Render(keys) + strings.Repeat(" ", gap) + DimStyle.Render(pos)
}

// detailBodyLines returns the fully-rendered, wrapped body lines for the active
// tab. Splitting into lines lets the render pass apply the scroll window.
func (m *Model) detailBodyLines(slot *runtimeSlot, innerW int) []string {
	if slot == nil {
		return []string{DimStyle.Italic(true).Render("(no node selected)")}
	}
	switch m.nodes.tab {
	case cockpitTabPrompt:
		return m.detailPromptLines(slot, innerW)
	case cockpitTabStats:
		return m.detailStatsLines(slot, innerW)
	default:
		return m.detailOutputLines(slot, innerW)
	}
}

// detailOutputLines renders the session's live output the same way the chat does
// — glamour text runs and styled tool blocks — with any chain-of-thought
// reasoning shown as a dimmed block on top. Uses the detail-sized markdown
// renderer so the shared jobs-pane renderer isn't repointed at this width.
func (m *Model) detailOutputLines(slot *runtimeSlot, innerW int) []string {
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

// detailPromptLines renders the session's system prompt and initial message as
// markdown, each under a styled section header.
func (m *Model) detailPromptLines(slot *runtimeSlot, innerW int) []string {
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

// detailStatsLines renders a labeled stats block: identity, status/timing,
// model/sampling, a live context-window bar, and token/cost/throughput totals.
func (m *Model) detailStatsLines(slot *runtimeSlot, innerW int) []string {
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
	switch {
	case slot.provider != "" && slot.model != "":
		row("Model", slot.provider+"/"+slot.model)
	case slot.model != "":
		row("Model", slot.model)
	case slot.provider != "":
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
	lines = append(lines, "  "+renderMiniContextBar(int(slot.contextTokens), ctxMax, barW, slot.status != "active"))
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

// fitLine truncates a (possibly styled) line to at most w display columns using
// an ANSI-aware truncator so escape sequences are never split.
func fitLine(s string, w int) string {
	if lipgloss.Width(s) > w {
		return xansi.Truncate(s, w, "")
	}
	return s
}

// maxInt returns the larger of two ints.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
