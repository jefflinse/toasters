// Panel rendering: left panel (jobs and teams panes) and right sidebar.
package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

func leftPanelWidth(termWidth int) int {
	w := termWidth / 4
	if w < minLeftPanelWidth {
		return minLeftPanelWidth
	}
	return w
}

// effectiveLeftPanelWidth returns the left panel width, respecting any user override.
func (m *Model) effectiveLeftPanelWidth() int {
	if m.leftPanelWidthOverride > 0 {
		w := m.leftPanelWidthOverride
		if w < minLeftPanelWidth {
			w = minLeftPanelWidth
		}
		maxW := m.width / 2
		if w > maxW {
			w = maxW
		}
		return w
	}
	return leftPanelWidth(m.width)
}

// sidebarWidth returns the sidebar width using the same formula as leftPanelWidth.
func sidebarWidth(termWidth int) int {
	w := termWidth / 6
	if w < minLeftPanelWidth {
		return minLeftPanelWidth
	}
	return w
}

func (m Model) renderLeftPanel(panelWidth, panelHeight int) string {
	// Each pane border adds 2 horizontal (left+right border) + 2 horizontal (left+right padding) = 4.
	paneFrameH := FocusedPaneStyle.GetHorizontalBorderSize() + FocusedPaneStyle.GetHorizontalPadding()
	contentWidth := panelWidth - paneFrameH
	if contentWidth < 1 {
		contentWidth = 1
	}

	// Each pane border adds 2 vertical rows (top + bottom border line).
	paneFrameV := FocusedPaneStyle.GetVerticalBorderSize()
	// 3 panes (Jobs, Blockers, Workers) × 2 rows border = 6 rows of overhead.
	borderOverhead := 3 * paneFrameV

	// Bottom pane (Workers): content-driven height.
	// Use the filtered view (active + most-recent completed) so the pane's
	// height math matches what we actually render.
	sortedRT := m.displayRuntimeSessions()
	workerCount := len(sortedRT)
	// Each active worker with activity gets one extra "↳ <last-activity>" line
	// below it so users can see what it's doing without opening the grid.
	activityLineCount := 0
	for _, rs := range sortedRT {
		if rs.status == "active" {
			activityLineCount++
		}
	}
	bottomContentH := 1 + workerCount + activityLineCount // "Workers" header + one line per worker (+ activity line for active workers)
	if workerCount == 0 {
		bottomContentH = 2 // header + "No workers running"
	}
	if m.focused == focusWorkers {
		bottomContentH++ // hint line
	}

	// Middle pane (Blockers): content-driven height, one line per blocker.
	blockerCount := len(m.blockers)
	blockersContentH := 1 + blockerCount // "Blockers" header + one line per blocker
	if blockerCount == 0 {
		blockersContentH = 2 // header + "No blockers"
	}
	if m.focused == focusBlockers {
		blockersContentH++ // hint line
	}

	// Jobs hint line appears when the jobs pane is focused.
	jobsHintH := 0
	if m.focused == focusJobs && len(m.displayJobs()) > 0 {
		jobsHintH = 1
	}

	// Available height for content across all three panes.
	availableH := panelHeight - borderOverhead
	if availableH < 9 {
		availableH = 9
	}

	// Top pane gets whatever is left after blockers + workers + jobs hint.
	topContentH := availableH - bottomContentH - blockersContentH - jobsHintH
	if topContentH < 3 {
		topContentH = 3
	}

	displayedJobs := m.displayJobs()

	// --- Top pane: Jobs ---
	var topLines []string
	jobsTitle := gradientText("Jobs", [3]uint8{0, 200, 200}, [3]uint8{175, 50, 200})
	if m.focused == focusJobs {
		jobsTitle = rainbowText("Jobs", m.spinnerFrame)
	}
	topLines = append(topLines, jobsTitle)
	if len(displayedJobs) == 0 {
		topLines = append(topLines, PlaceholderPaneStyle.Render("No jobs"))
	} else {
		// Render each job as the same bordered block used in the chat
		// stream, keyed by status and with live task counts. Blocks stack
		// with touching borders — a TUI has no sub-row spacing, so the
		// choice is zero-gap (cards stacked) or a full row between them;
		// zero-gap keeps the list dense without losing distinctness since
		// each block still has its own status-colored border.
		for i, j := range displayedJobs {
			snap := m.buildJobSnapshot(j.ID)
			if snap == nil {
				continue
			}
			topLines = append(topLines, renderJobUpdateBlock(snap, contentWidth, i == m.selectedJob, m.spinnerFrame, true))
		}
	}
	// Hint line when jobs pane is focused.
	if m.focused == focusJobs && len(displayedJobs) > 0 {
		topLines = append(topLines, DimStyle.Render("↑↓ · Enter → job details"))
	}
	topContent := lipgloss.NewStyle().Height(topContentH + jobsHintH).Render(
		lipgloss.JoinVertical(lipgloss.Left, topLines...),
	)
	topPaneStyle := UnfocusedPaneStyle
	if m.focused == focusJobs {
		topPaneStyle = FocusedPaneStyle
	}
	topPane := topPaneStyle.Width(panelWidth).Render(topContent)

	// --- Middle pane: Blockers ---
	var blockerLines []string
	blockersTitle := gradientText("Blockers", [3]uint8{255, 175, 0}, [3]uint8{255, 90, 0})
	if m.focused == focusBlockers {
		blockersTitle = rainbowText("Blockers", m.spinnerFrame)
	}
	blockerLines = append(blockerLines, blockersTitle)
	if blockerCount == 0 {
		blockerLines = append(blockerLines, DimStyle.Italic(true).Render("No blockers"))
	} else {
		for i, b := range m.blockers {
			marker := "  "
			if m.focused == focusBlockers && i == m.blockersSel {
				marker = "▶ "
			}
			label := m.blockerLabel(b) + ": " + blockerFirstQuestion(b)
			line := "⛔ " + truncateStr(label, contentWidth-5)
			if m.focused == focusBlockers && i == m.blockersSel {
				blockerLines = append(blockerLines, BlockerSelectedStyle.Render(marker+line))
			} else {
				blockerLines = append(blockerLines, marker+line)
			}
		}
	}
	if m.focused == focusBlockers {
		hint := "Enter → answer"
		if blockerCount > 0 {
			hint = "↑↓ · Enter → answer · x → dismiss"
		}
		blockerLines = append(blockerLines, DimStyle.Render(hint))
	}
	blockersContent := lipgloss.NewStyle().Height(blockersContentH).Render(
		lipgloss.JoinVertical(lipgloss.Left, blockerLines...),
	)
	blockersPaneStyle := UnfocusedPaneStyle
	if m.focused == focusBlockers {
		blockersPaneStyle = FocusedPaneStyle
	}
	blockersPane := blockersPaneStyle.Width(panelWidth).Render(blockersContent)

	// --- Bottom pane: Workers ---
	var workerLines []string
	workersTitle := gradientText("Workers", [3]uint8{50, 130, 255}, [3]uint8{0, 200, 200})
	if m.focused == focusWorkers {
		workersTitle = rainbowText("Workers", m.spinnerFrame)
	}
	workerLines = append(workerLines, workersTitle)

	// Runtime sessions.
	runtimeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	hasAnyRuntime := len(sortedRT) > 0
	if hasAnyRuntime {
		for _, rs := range sortedRT {
			// "<short-job-id>:<role>" — e.g. graph:plan for job 67cddf28-… → "67cddf28:plan".
			role := strings.TrimPrefix(rs.workerName, "graph:")
			shortJobID := rs.jobID
			if len(shortJobID) > 8 {
				shortJobID = shortJobID[:8]
			}
			label := shortJobID + ":" + role
			var statusIcon string
			if rs.status == "active" {
				statusIcon = string(spinnerChars[m.spinnerFrame%len(spinnerChars)]) + " "
			} else {
				statusIcon = "✓ "
			}
			prefix := runtimeStyle.Render("⚡")
			line := prefix + statusIcon + truncateStr(label, contentWidth-4)
			if rs.status != "active" {
				workerLines = append(workerLines, DimStyle.Render("⚡"+statusIcon+truncateStr(label, contentWidth-4)))
			} else {
				workerLines = append(workerLines, line)
				// Show last activity for active workers so users can see what
				// they're doing without opening the grid. bottomContentH is
				// sized above to reserve a row per active worker; do not skip
				// the append when there is no activity yet, or the height
				// reservation won't match the rendered content.
				const indent = "  ↳ "
				activityText := "waiting for activity…"
				if n := len(rs.activities); n > 0 {
					activityText = rs.activities[n-1].label
				}
				maxActivityW := contentWidth - len([]rune(indent))
				if maxActivityW < 1 {
					maxActivityW = 1
				}
				workerLines = append(workerLines, DimStyle.Render(indent+truncateStr(activityText, maxActivityW)))
			}
		}
	}

	if !hasAnyRuntime {
		// Decomposition nodes are hidden from this pane, so a job that's busy
		// planning would read as idle. Say what's actually happening.
		if planning := m.activePlanningCount(); planning > 0 {
			msg := "Planning… decomposing tasks"
			if planning > 1 {
				msg = fmt.Sprintf("Planning… decomposing %d tasks", planning)
			}
			workerLines = append(workerLines, DimStyle.Italic(true).Render(msg))
		} else {
			workerLines = append(workerLines, DimStyle.Italic(true).Render("No workers running"))
		}
	}
	if m.focused == focusWorkers {
		workerLines = append(workerLines, DimStyle.Render("Enter → grid view"))
	}

	bottomContent := lipgloss.NewStyle().Height(bottomContentH).Render(
		lipgloss.JoinVertical(lipgloss.Left, workerLines...),
	)
	bottomPaneStyle := UnfocusedPaneStyle
	if m.focused == focusWorkers {
		bottomPaneStyle = FocusedPaneStyle
	}
	bottomPane := bottomPaneStyle.Width(panelWidth).Render(bottomContent)

	inner := lipgloss.JoinVertical(lipgloss.Left, topPane, blockersPane, bottomPane)
	return LeftPanelStyle.Width(panelWidth).Height(panelHeight).Render(inner)
}

// leftPanelWorkersPaneHeight returns the rendered height of the Workers bottom pane
// in the left panel, for use in mouse hit-testing. Must stay in sync with the
// height math inside renderLeftPanel.
func (m *Model) leftPanelWorkersPaneHeight() int {
	paneFrameV := FocusedPaneStyle.GetVerticalBorderSize()
	sortedRT := m.displayRuntimeSessions()
	workerCount := len(sortedRT)
	activityLineCount := 0
	for _, rs := range sortedRT {
		if rs.status == "active" {
			activityLineCount++
		}
	}
	bottomContentH := 1 + workerCount + activityLineCount
	if workerCount == 0 {
		bottomContentH = 2
	}
	if m.focused == focusWorkers {
		bottomContentH++
	}
	return bottomContentH + paneFrameV
}

// leftPanelBlockersPaneHeight returns the rendered height of the Blockers pane
// in the left panel, for mouse hit-testing. Must stay in sync with the height
// math inside renderLeftPanel.
func (m *Model) leftPanelBlockersPaneHeight() int {
	paneFrameV := FocusedPaneStyle.GetVerticalBorderSize()
	blockerCount := len(m.blockers)
	blockersContentH := 1 + blockerCount
	if blockerCount == 0 {
		blockersContentH = 2
	}
	if m.focused == focusBlockers {
		blockersContentH++
	}
	return blockersContentH + paneFrameV
}

// fleetMember is one live LLM invocation shown in the fleet sidebar: the
// operator or an active/recent worker session. It carries only what the pane
// renders so buildFleet can source it from different places (operator stats vs.
// runtime slots) without the render code caring which.
type fleetMember struct {
	label     string // "operator" or "<job>:<role>"
	icon      string // glyph prefix (⬡ operator, ⚡ worker)
	model     string
	active    bool // currently streaming
	done      bool // terminal worker (completed/failed/cancelled)
	ctxUsed   int  // live context-window occupancy in tokens
	ctxMax    int  // model context length (0 if unknown)
	tokensOut int64
	costUSD   float64
	tps       float64 // tokens/sec (valid only when hasTPS)
	hasTPS    bool
	activity  string // most-recent tool-call label (workers only; empty for operator/none)
}

// buildFleet assembles the ordered list of LLMs to display: the operator first
// (always pinned), then the currently-active workers. Finished workers are
// intentionally excluded — the fleet is a live view; completed sessions live in
// the grid drill-in and job history.
func (m Model) buildFleet() []fleetMember {
	members := make([]fleetMember, 0, 1+len(m.runtimeSessions))

	// Operator, pinned first.
	op := fleetMember{
		label:     "operator",
		icon:      "⬡",
		model:     m.stats.ModelName,
		active:    m.stream.streaming,
		ctxUsed:   m.stats.PromptTokens,
		ctxMax:    m.stats.ContextLength,
		tokensOut: int64(m.stats.CompletionTokens),
	}
	if m.stats.TotalResponses > 0 && m.stats.TotalResponseTime > 0 {
		op.tps = float64(m.stats.CompletionTokens) / m.stats.TotalResponseTime.Seconds()
		op.hasTPS = true
	} else if m.stream.streaming && m.stats.LastResponseTime > 0 && m.stats.CompletionTokensLive > 0 {
		op.tps = float64(m.stats.CompletionTokensLive) / m.stats.LastResponseTime.Seconds()
		op.hasTPS = true
	}
	members = append(members, op)

	// Active workers only.
	for _, rs := range m.displayRuntimeSessions() {
		if rs.status != "active" {
			continue
		}
		role := strings.TrimPrefix(rs.workerName, "graph:")
		shortJobID := rs.jobID
		if len(shortJobID) > 8 {
			shortJobID = shortJobID[:8]
		}
		label := role
		if shortJobID != "" {
			label = shortJobID + ":" + role
		}
		mem := fleetMember{
			label:     label,
			icon:      "⚡",
			model:     rs.model,
			active:    true,
			ctxUsed:   int(rs.contextTokens),
			ctxMax:    m.modelContext[rs.model],
			tokensOut: rs.tokensOut,
			costUSD:   rs.costUSD,
		}
		if n := len(rs.activities); n > 0 {
			mem.activity = rs.activities[n-1].label
		}
		elapsed := time.Since(rs.startTime)
		if !rs.endTime.IsZero() {
			elapsed = rs.endTime.Sub(rs.startTime)
		}
		if elapsed > 0 && rs.tokensOut > 0 {
			mem.tps = float64(rs.tokensOut) / elapsed.Seconds()
			mem.hasTPS = true
		}
		members = append(members, mem)
	}
	return members
}

// fleetTotals computes the footer aggregates. liveCount and totalTPS both count
// only active members, so "N live · X t/s" reads as one coherent statement about
// work happening right now — an idle operator or a finished worker still carries
// a nonzero since-start rate and must not inflate the live throughput. Cost, by
// contrast, accumulates over every member: it's the run's total spend.
func fleetTotals(members []fleetMember) (liveCount int, totalTPS, totalCost float64) {
	for _, mem := range members {
		if mem.active {
			liveCount++
			if mem.hasTPS {
				totalTPS += mem.tps
			}
		}
		totalCost += mem.costUSD
	}
	return liveCount, totalTPS, totalCost
}

// renderSidebar builds the right sidebar: a borderless "fleet" pane showing
// every live LLM (operator + workers) with a context-window bar, throughput and
// cost, capped with a session-wide aggregate footer.
func (m Model) renderSidebar(sbWidth int) string {
	// Horizontal padding matches the frame width used by left-panel panes
	// (border 2 + padding 2 = 4 cols) so content sizing stays consistent.
	const sidebarHPad = 2
	contentWidth := sbWidth - 2*sidebarHPad
	if contentWidth < 1 {
		contentWidth = 1
	}

	fleet := m.buildFleet()

	// --- Header: "fleet" + connection status ---
	var sb strings.Builder
	// Leading blank row matches ChatAreaStyle's top padding so the header
	// doesn't butt up against the very top of the terminal.
	sb.WriteString("\n")

	connStatus := ConnectedStyle.Render("connected")
	if !m.stats.Connected {
		connStatus = ErrorStyle.Render("disconnected")
	}
	headerText := gradientText("fleet", [3]uint8{255, 175, 0}, [3]uint8{175, 50, 200})
	gap := contentWidth - lipgloss.Width(headerText) - lipgloss.Width(connStatus)
	if gap < 1 {
		gap = 1
	}
	sb.WriteString(headerText + strings.Repeat(" ", gap) + connStatus)
	sb.WriteString("\n\n")

	// --- Body: one block per LLM, capped to what fits above the footer ---
	// Each member renders as ~5 lines (label, model, bar, stats, blank).
	const linesPerMember = 5
	const footerLines = 3 // separator + two aggregate rows
	availableLines := m.height - 2 /*header+blank*/ - footerLines
	maxMembers := len(fleet)
	if availableLines > 0 {
		if fit := availableLines / linesPerMember; fit < maxMembers {
			maxMembers = fit
		}
	}
	if maxMembers < 1 {
		maxMembers = 1 // always show at least the operator
	}

	shown := fleet
	if len(shown) > maxMembers {
		shown = shown[:maxMembers]
	}
	for _, mem := range shown {
		sb.WriteString(m.renderFleetMember(mem, contentWidth))
		sb.WriteString("\n")
	}
	if hidden := len(fleet) - len(shown); hidden > 0 {
		sb.WriteString(DimStyle.Render(fmt.Sprintf("  +%d more…", hidden)))
		sb.WriteString("\n")
	}

	// --- Footer: session-wide aggregates ---
	liveCount, totalTPS, totalCost := fleetTotals(fleet)
	sb.WriteString(DimStyle.Render(strings.Repeat("─", contentWidth)))
	sb.WriteString("\n")
	sb.WriteString(SidebarLabelStyle.Render(fmt.Sprintf("Σ %d live", liveCount)))
	if totalTPS > 0 {
		sb.WriteString(SidebarValueStyle.Render(fmt.Sprintf(" · %.0f t/s", totalTPS)))
	}
	sb.WriteString("\n")
	if totalCost > 0 {
		sb.WriteString(SidebarLabelStyle.Render(fmt.Sprintf("Σ ~$%.2f this run", totalCost)))
		sb.WriteString("\n")
	}

	// Fleet pane fills the full sidebar height and renders borderless, matching
	// the horizontal frame width used by bordered panes so columns line up.
	paneH := m.height
	if paneH < 3 {
		paneH = 3
	}
	paneStyle := lipgloss.NewStyle().Padding(0, sidebarHPad)
	return paneStyle.Width(sbWidth).Height(paneH).Render(sb.String())
}

// memStatusIcon returns the leading status glyph for a fleet member: a spinner
// while active, two spaces for an idle operator.
func (m Model) memStatusIcon(mem fleetMember) string {
	if mem.active {
		return string(spinnerChars[m.spinnerFrame%len(spinnerChars)]) + " "
	}
	return "  "
}

// memStats returns the throughput/cost/tokens segments in priority order — the
// most useful metrics first so a tight width drops the least essential (output
// tokens, which the context bar already implies) before cost or rate.
func memStats(mem fleetMember) []string {
	var stats []string
	if mem.hasTPS {
		stats = append(stats, fmt.Sprintf("%.0f t/s", mem.tps))
	}
	if mem.costUSD > 0 {
		stats = append(stats, fmt.Sprintf("~$%.2f", mem.costUSD))
	}
	if mem.tokensOut > 0 {
		stats = append(stats, formatTokenCount(mem.tokensOut)+"↓")
	}
	return stats
}

// renderFleetMember renders one LLM block honoring the configured row density.
// Both densities show the context bar and the most-recent activity line (for
// workers); compact folds throughput onto the label line and stats onto the bar
// line, dropping the standalone model line (still visible in the grid drill-in).
func (m Model) renderFleetMember(mem fleetMember, contentWidth int) string {
	if m.fleetDensity == "compact" {
		return m.renderFleetMemberCompact(mem, contentWidth)
	}
	return m.renderFleetMemberFull(mem, contentWidth)
}

func (m Model) renderFleetMemberFull(mem fleetMember, contentWidth int) string {
	var b strings.Builder

	// Line 1: icon + status + label. Measure the prefix with lipgloss.Width so a
	// double-width glyph (e.g. ⚡) doesn't push the label past the column.
	prefix := mem.icon + m.memStatusIcon(mem)
	labelMax := contentWidth - lipgloss.Width(prefix)
	if labelMax < 1 {
		labelMax = 1
	}
	b.WriteString(SidebarValueStyle.Render(prefix + truncateStr(mem.label, labelMax)))
	b.WriteString("\n")

	// Line 2: model name (dim).
	model := mem.model
	if model == "" {
		model = "…"
	}
	b.WriteString("  " + DimStyle.Render(truncateStr(model, contentWidth-2)))
	b.WriteString("\n")

	// Line 3: context-window bar.
	b.WriteString("  " + renderMiniContextBar(mem.ctxUsed, mem.ctxMax, contentWidth-2))
	b.WriteString("\n")

	// Line 4: throughput · cost · tokens.
	if stats := memStats(mem); len(stats) > 0 {
		b.WriteString("  " + DimStyle.Render(truncateStr(strings.Join(stats, " · "), contentWidth-2)))
		b.WriteString("\n")
	}

	// Line 5: most-recent activity (workers only).
	if mem.activity != "" {
		b.WriteString(DimStyle.Render("  ↳ " + truncateStr(mem.activity, contentWidth-4)))
		b.WriteString("\n")
	}
	return b.String()
}

func (m Model) renderFleetMemberCompact(mem fleetMember, contentWidth int) string {
	var b strings.Builder

	// Line 1: icon + status + label ........ t/s (right-aligned).
	tps := ""
	if mem.hasTPS {
		tps = fmt.Sprintf("%.0f t/s", mem.tps)
	}
	prefix := mem.icon + m.memStatusIcon(mem)
	labelMax := contentWidth - lipgloss.Width(prefix) - lipgloss.Width(tps) - 1
	if labelMax < 1 {
		labelMax = 1
	}
	left := prefix + truncateStr(mem.label, labelMax)
	gap := contentWidth - lipgloss.Width(left) - lipgloss.Width(tps)
	if gap < 1 {
		gap = 1
	}
	b.WriteString(SidebarValueStyle.Render(left) + strings.Repeat(" ", gap) + DimStyle.Render(tps))
	b.WriteString("\n")

	// Line 2: context bar + cost/tokens folded onto the same line.
	var tail []string
	if mem.costUSD > 0 {
		tail = append(tail, fmt.Sprintf("~$%.2f", mem.costUSD))
	}
	if mem.tokensOut > 0 {
		tail = append(tail, formatTokenCount(mem.tokensOut)+"↓")
	}
	barW := contentWidth - 2
	tailStr := ""
	if len(tail) > 0 {
		tailStr = " · " + strings.Join(tail, " · ")
		barW -= lipgloss.Width(tailStr)
	}
	if barW < 6 {
		barW = 6
	}
	b.WriteString("  " + renderMiniContextBar(mem.ctxUsed, mem.ctxMax, barW) + DimStyle.Render(tailStr))
	b.WriteString("\n")

	// Line 3: most-recent activity (workers only).
	if mem.activity != "" {
		b.WriteString(DimStyle.Render("  ↳ " + truncateStr(mem.activity, contentWidth-4)))
		b.WriteString("\n")
	}
	return b.String()
}

// renderMiniContextBar renders a single-line context-window occupancy bar with a
// trailing percentage (or a raw token count when the model's context length is
// unknown). Fill color goes green → yellow → red as the window fills, so a
// worker about to blow its context reads at a glance.
func renderMiniContextBar(used, total, width int) string {
	if width < 4 {
		width = 4
	}
	var pct float64
	var label string
	switch {
	case used <= 0:
		// No live occupancy reported (e.g. a graph-node worker, whose usage
		// isn't streamed). Show "—" rather than a misleading 0% empty bar,
		// even when the model's context length is known.
		label = " —"
	case total > 0:
		pct = float64(used) / float64(total)
		if pct > 1 {
			pct = 1
		}
		label = fmt.Sprintf(" %.0f%%", pct*100)
	default:
		label = " " + formatTokenCount(int64(used))
	}

	barW := width - lipgloss.Width(label)
	if barW < 3 {
		barW = 3
	}
	filled := int(pct * float64(barW))
	if filled > barW {
		filled = barW
	}

	// Threshold coloring: comfortable / warming / near-limit.
	fillColor := lipgloss.Color("#52c41a") // green
	switch {
	case pct >= 0.85:
		fillColor = lipgloss.Color("#f5222d") // red
	case pct >= 0.6:
		fillColor = lipgloss.Color("#faad14") // yellow
	}
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("237"))

	bar := lipgloss.NewStyle().Foreground(fillColor).Render(strings.Repeat("█", filled)) +
		emptyStyle.Render(strings.Repeat("░", barW-filled))
	return bar + DimStyle.Render(label)
}

// taskStatusIndicator returns the status indicator rune and style for a service task status.
func taskStatusIndicator(status service.TaskStatus) (string, lipgloss.Style) {
	switch status {
	case service.TaskStatusPending:
		return "○", dbTaskPendingStyle
	case service.TaskStatusInProgress:
		return "◉", dbTaskInProgressStyle
	case service.TaskStatusCompleted:
		return "✓", dbTaskCompletedStyle
	case service.TaskStatusFailed:
		return "✗", dbTaskFailedStyle
	case service.TaskStatusBlocked:
		return "⊘", dbTaskBlockedStyle
	case service.TaskStatusCancelled:
		return "—", dbTaskCancelledStyle
	default:
		return "?", dbTaskPendingStyle
	}
}

// renderJobProgressSummary returns a summary line for a job's task progress.
// Returns an empty string if there are no tasks.
func renderJobProgressSummary(tasks []service.Task) string {
	if len(tasks) == 0 {
		return ""
	}
	var completed, blocked, failed int
	for _, t := range tasks {
		switch t.Status {
		case service.TaskStatusCompleted:
			completed++
		case service.TaskStatusBlocked:
			blocked++
		case service.TaskStatusFailed:
			failed++
		}
	}
	if blocked > 0 {
		_, style := taskStatusIndicator(service.TaskStatusBlocked)
		return style.Render("⚠ BLOCKED")
	}
	if failed > 0 {
		_, style := taskStatusIndicator(service.TaskStatusFailed)
		return style.Render(fmt.Sprintf("%d failed", failed))
	}
	_, style := taskStatusIndicator(service.TaskStatusCompleted)
	return style.Render(fmt.Sprintf("%d/%d tasks ✓", completed, len(tasks)))
}

// formatTokenCount formats a token count compactly: ≥1000 → "1.2k", else as-is.
func formatTokenCount(n int64) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000.0)
	}
	return fmt.Sprintf("%d", n)
}
