// Panel rendering: the sidebar's three stacked panes — Jobs, Fleet, Blockers.
package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

func defaultSidebarWidth(termWidth int) int {
	w := termWidth / 4
	if w < minSidebarWidth {
		return minSidebarWidth
	}
	return w
}

// effectiveSidebarWidth returns the sidebar width, respecting any user override.
func (m *Model) effectiveSidebarWidth() int {
	if m.sidebarWidthOverride > 0 {
		w := m.sidebarWidthOverride
		if w < minSidebarWidth {
			w = minSidebarWidth
		}
		maxW := m.width / 2
		if w > maxW {
			w = maxW
		}
		return w
	}
	return defaultSidebarWidth(m.width)
}

// sidebarOnRight reports whether the sidebar renders to the right of the
// chat window (settings-driven; the default is left).
func (m *Model) sidebarOnRight() bool {
	return m.sidebarSide == "right"
}

// pointInSidebar reports whether terminal column x falls inside the sidebar,
// accounting for which side it renders on. Callers must already have checked
// that the sidebar is visible.
func (m *Model) pointInSidebar(x int) bool {
	if m.sidebarOnRight() {
		return x >= m.width-m.sidebarWidth
	}
	return x < m.sidebarWidth
}

// sidebarContentWidth returns the per-pane content width for a given left
// panel width (panel width minus one pane's horizontal border+padding frame).
func sidebarContentWidth(panelWidth int) int {
	paneFrameH := FocusedPaneStyle.GetHorizontalBorderSize() + FocusedPaneStyle.GetHorizontalPadding()
	cw := panelWidth - paneFrameH
	if cw < 1 {
		cw = 1
	}
	return cw
}

// paneStyleFor returns the bordered pane style for a focused/unfocused pane.
func paneStyleFor(focused bool) lipgloss.Style {
	if focused {
		return FocusedPaneStyle
	}
	return UnfocusedPaneStyle
}

// renderSidebar renders the three stacked panes: Jobs (top) and Blockers
// (bottom) are content-driven; Fleet (middle) takes the remaining height so the
// live-LLM view gets the slack. All height math lives in sidebarPaneHeights, which
// mouse hit-testing shares so pane boundaries can never drift out of sync.
func (m Model) renderSidebar(panelWidth, panelHeight int) string {
	contentWidth := sidebarContentWidth(panelWidth)
	jobsH, fleetH, blockersH := m.sidebarPaneHeights(panelWidth, panelHeight)

	jobsContent := lipgloss.NewStyle().Height(jobsH).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.buildJobsLines(contentWidth)...),
	)
	jobsPane := paneStyleFor(m.focused == focusJobs).Width(panelWidth).Render(jobsContent)

	fleetContent := lipgloss.NewStyle().Height(fleetH).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.buildFleetLines(contentWidth, fleetH)...),
	)
	fleetPane := paneStyleFor(m.focused == focusFleet).Width(panelWidth).Render(fleetContent)

	blockersContent := lipgloss.NewStyle().Height(blockersH).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.buildBlockersLines(contentWidth)...),
	)
	blockersPane := paneStyleFor(m.focused == focusBlockers).Width(panelWidth).Render(blockersContent)

	inner := lipgloss.JoinVertical(lipgloss.Left, jobsPane, fleetPane, blockersPane)
	return SidebarStyle.Width(panelWidth).Height(panelHeight).Render(inner)
}

// sidebarPaneHeights computes the three panes' content heights. Jobs and Blockers
// are content-driven (measured from their rendered lines); Fleet takes the rest.
// When Jobs+Blockers would starve Fleet below minFleetH, Jobs is compressed
// first (its list scrolls; blockers are usually short). Shared by render and
// mouse hit-testing so the two never disagree.
func (m *Model) sidebarPaneHeights(panelWidth, panelHeight int) (jobsH, fleetH, blockersH int) {
	contentWidth := sidebarContentWidth(panelWidth)
	paneFrameV := FocusedPaneStyle.GetVerticalBorderSize()
	availableH := panelHeight - 3*paneFrameV
	if availableH < 9 {
		availableH = 9
	}
	const minFleetH = 4

	jobsH = lipgloss.Height(lipgloss.JoinVertical(lipgloss.Left, m.buildJobsLines(contentWidth)...))
	blockersH = lipgloss.Height(lipgloss.JoinVertical(lipgloss.Left, m.buildBlockersLines(contentWidth)...))
	if blockersH > availableH-minFleetH-1 {
		blockersH = availableH - minFleetH - 1
	}
	if blockersH < 1 {
		blockersH = 1
	}
	if jobsH > availableH-minFleetH-blockersH {
		jobsH = availableH - minFleetH - blockersH
	}
	if jobsH < 1 {
		jobsH = 1
	}
	fleetH = availableH - jobsH - blockersH
	if fleetH < minFleetH {
		fleetH = minFleetH
	}
	return jobsH, fleetH, blockersH
}

// buildJobsLines builds the Jobs pane content (title, job blocks, focus hint).
func (m Model) buildJobsLines(contentWidth int) []string {
	displayedJobs := m.displayJobs()
	var lines []string
	jobsTitle := gradientText("Jobs", [3]uint8{0, 200, 200}, [3]uint8{175, 50, 200})
	if m.focused == focusJobs {
		jobsTitle = rainbowText("Jobs", m.spinnerFrame)
	}
	lines = append(lines, jobsTitle)
	if len(displayedJobs) == 0 {
		lines = append(lines, PlaceholderPaneStyle.Render("No jobs"))
	} else {
		for i, j := range displayedJobs {
			snap := m.buildJobSnapshot(j.ID)
			if snap == nil {
				continue
			}
			lines = append(lines, renderJobUpdateBlock(snap, contentWidth, i == m.selectedJob, m.spinnerFrame, true))
		}
	}
	if m.focused == focusJobs && len(displayedJobs) > 0 {
		lines = append(lines, DimStyle.Render("↑↓ · Enter → job details"))
	}
	return lines
}

// buildBlockersLines builds the Blockers pane content (title with pending
// count, two-line blocker rows, hint). Each blocker renders as an attribution
// line (who's asking, about what, how long ago) and a question line so the
// question isn't crowded out by the attribution.
func (m Model) buildBlockersLines(contentWidth int) []string {
	var lines []string
	blockersTitle := gradientText("Blockers", [3]uint8{255, 175, 0}, [3]uint8{255, 90, 0})
	if m.focused == focusBlockers {
		blockersTitle = rainbowText("Blockers", m.spinnerFrame)
	}
	if n := len(m.blockers); n > 0 {
		blockersTitle += BlockerCountStyle.Render(fmt.Sprintf(" · %d waiting", n))
	}
	lines = append(lines, blockersTitle)
	if len(m.blockers) == 0 {
		lines = append(lines, DimStyle.Italic(true).Render("No blockers"))
	} else {
		for i, b := range m.blockers {
			selected := m.focused == focusBlockers && i == m.blockersSel
			marker := "  "
			if selected {
				marker = "▶ "
			}
			attr := "⛔ " + m.blockerLabel(b) + " · " + compactAge(b.CreatedAt)
			question := blockerFirstQuestion(b)
			if extra := len(b.Questions) - 1; extra > 0 {
				question += fmt.Sprintf(" (+%d more)", extra)
			}
			attrLine := marker + truncateStr(attr, contentWidth-3)
			questionLine := "     " + truncateStr(question, contentWidth-6)
			if selected {
				lines = append(lines,
					BlockerSelectedStyle.Render(attrLine),
					BlockerSelectedStyle.Render(questionLine),
				)
			} else {
				lines = append(lines, attrLine, DimStyle.Render(questionLine))
			}
		}
	}
	if m.focused == focusBlockers {
		hint := "Enter → answer"
		if len(m.blockers) > 0 {
			hint = "↑↓ · Enter → answer · x → dismiss"
		}
		lines = append(lines, DimStyle.Render(hint))
	}
	return lines
}

// buildFleetLines builds the Fleet pane content: a title+connection row, one
// block per live LLM (operator + active workers) rendered at the configured
// density, an aggregate footer, and a focus hint. Member blocks are capped to
// what fits in maxH with a "+N more" line so a burst of workers can't overflow.
func (m Model) buildFleetLines(contentWidth, maxH int) []string {
	var lines []string

	fleetTitle := gradientText("Fleet", [3]uint8{50, 130, 255}, [3]uint8{0, 200, 200})
	if m.focused == focusFleet {
		fleetTitle = rainbowText("Fleet", m.spinnerFrame)
	}
	conn := ConnectedStyle.Render("connected")
	if !m.stats.Connected {
		conn = ErrorStyle.Render("disconnected")
	}
	gap := contentWidth - lipgloss.Width(fleetTitle) - lipgloss.Width(conn)
	if gap < 1 {
		gap = 1
	}
	lines = append(lines, fleetTitle+strings.Repeat(" ", gap)+conn)

	fleet := m.buildFleet()
	live, tps, cost := fleetTotals(fleet)

	// Footer: separator + aggregate rows.
	footer := []string{DimStyle.Render(strings.Repeat("─", contentWidth))}
	sigma := SidebarLabelStyle.Render(fmt.Sprintf("Σ %d live", live))
	if tps > 0 {
		sigma += SidebarValueStyle.Render(fmt.Sprintf(" · %.0f t/s", tps))
	}
	footer = append(footer, sigma)
	if cost > 0 {
		footer = append(footer, SidebarLabelStyle.Render(fmt.Sprintf("Σ ~$%.2f", cost)))
	}

	hintH := 0
	if m.focused == focusFleet {
		hintH = 1
	}
	// Budget for member blocks = maxH - title - footer - hint.
	memBudget := maxH - 1 - len(footer) - hintH
	if memBudget < 1 {
		memBudget = 1
	}

	var memLines []string
	shown := 0
	for _, mem := range fleet {
		block := strings.Split(strings.TrimRight(m.renderFleetMember(mem, contentWidth), "\n"), "\n")
		extra := len(block)
		if shown > 0 {
			extra++ // blank separator row
		}
		if shown > 0 && len(memLines)+extra > memBudget {
			break
		}
		if shown > 0 {
			memLines = append(memLines, "")
		}
		memLines = append(memLines, block...)
		shown++
	}
	if hidden := len(fleet) - shown; hidden > 0 {
		memLines = append(memLines, DimStyle.Render(fmt.Sprintf("  +%d more…", hidden)))
	}

	lines = append(lines, memLines...)
	lines = append(lines, footer...)
	if m.focused == focusFleet {
		lines = append(lines, DimStyle.Render("Enter → grid view"))
	}
	return lines
}

// fleetMember is one live LLM invocation shown in the fleet sidebar: the
// operator or an active/recent worker session. It carries only what the pane
// renders so buildFleet can source it from different places (operator stats vs.
// runtime slots) without the render code caring which.
type fleetMember struct {
	label     string // "operator" or "<job>:<role>"
	icon      string // glyph prefix (⬡ operator, ⚡ worker)
	model     string
	active    bool // currently streaming (operator) / running (worker)
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
			ctxMax:    m.slotCtxMax(rs),
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
	b.WriteString("  " + renderMiniContextBar(mem.ctxUsed, mem.ctxMax, contentWidth-2, false))
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
	b.WriteString("  " + renderMiniContextBar(mem.ctxUsed, mem.ctxMax, barW, false) + DimStyle.Render(tailStr))
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
// worker about to blow its context reads at a glance. When dim is set (a
// finished node), the fill is a muted green regardless of occupancy, so
// completed rows read as quiet history distinct from live workers.
func renderMiniContextBar(used, total, width int, dim bool) string {
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

	// Threshold coloring: comfortable / warming / near-limit. A finished node
	// uses a muted green regardless of occupancy so it reads as quiet history.
	fillColor := lipgloss.Color("#52c41a") // green
	switch {
	case dim:
		fillColor = lipgloss.Color("#3f6b3f") // dim green
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
