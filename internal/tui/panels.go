// Panel rendering: the sidebar's three stacked panes — Jobs, Fleet, Blockers.
package tui

import (
	"fmt"
	"image/color"
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
			if m.compactSidebar {
				lines = append(lines, m.renderJobLine(snap, contentWidth, i == m.selectedJob))
			} else {
				lines = append(lines, renderJobUpdateBlock(snap, contentWidth, i == m.selectedJob, m.spinnerFrame, true))
			}
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
			if m.compactSidebar {
				// One line: attribution only; the question folds away.
				line := marker + truncateStr(attr, contentWidth-3)
				if selected {
					lines = append(lines, BlockerSelectedStyle.Render(line))
				} else {
					lines = append(lines, DimStyle.Render(line))
				}
				continue
			}
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
	switch {
	case len(fleet) == 0:
		// Operator moved to the input border, so with no workers this pane has
		// nothing live to show — say so rather than leaving a blank gap.
		memLines = append(memLines, DimStyle.Italic(true).Render("  no workers active"))
	case m.compactSidebar:
		// One row per worker; no blank separators (density is the point).
		shown := 0
		for _, mem := range fleet {
			if shown >= memBudget {
				break
			}
			memLines = append(memLines, m.renderFleetMemberLine(mem, contentWidth))
			shown++
		}
		if hidden := len(fleet) - shown; hidden > 0 {
			memLines = append(memLines, DimStyle.Render(fmt.Sprintf("  +%d more…", hidden)))
		}
	default:
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
	label       string // "operator" or "<job>:<role>"
	icon        string // glyph prefix (⬡ operator, ⚡ worker)
	model       string
	active      bool    // currently streaming (operator) / running (worker)
	ctxUsed     int     // live context-window occupancy in tokens
	ctxMax      int     // model context length (0 if unknown)
	threshold   float64 // compaction threshold as a fraction (0 = disabled/no tick)
	compactions int     // digest handoffs observed (operator row; renders as ↺n)
	tokensOut   int64
	costUSD     float64
	tps         float64 // tokens/sec (valid only when hasTPS)
	hasTPS      bool
	activity    string // most-recent activity (worker tool call, or operator compaction trace)
}

// operatorMember builds the operator's fleet-member view from the live session
// stats. The operator no longer renders in the Fleet pane — it rides the input
// box's top border (see renderOperatorBorderLabel) — but the same struct still
// drives that strip, so the construction lives here next to buildFleet.
func (m Model) operatorMember() fleetMember {
	op := fleetMember{
		label:       "operator",
		icon:        "⬡",
		model:       m.stats.ModelName,
		active:      m.stream.streaming,
		ctxUsed:     m.stats.PromptTokens,
		ctxMax:      m.stats.ContextLength,
		threshold:   float64(m.opCompactionThreshold) / 100,
		compactions: m.opCompactionCount,
		tokensOut:   int64(m.stats.CompletionTokens),
	}
	if m.opLastCompaction != "" {
		op.activity = m.opLastCompaction
	}
	if m.stats.TotalResponses > 0 && m.stats.TotalResponseTime > 0 {
		op.tps = float64(m.stats.CompletionTokens) / m.stats.TotalResponseTime.Seconds()
		op.hasTPS = true
	} else if m.stream.streaming && m.stats.LastResponseTime > 0 && m.stats.CompletionTokensLive > 0 {
		op.tps = float64(m.stats.CompletionTokensLive) / m.stats.LastResponseTime.Seconds()
		op.hasTPS = true
	}
	return op
}

// buildFleet assembles the currently-active workers, in dispatch order. The
// operator is intentionally excluded — it lives on the input-box border now, so
// the Fleet pane is a pure worker view. Finished workers are also excluded — the
// fleet is a live view; completed sessions live in the grid drill-in and job
// history.
func (m Model) buildFleet() []fleetMember {
	members := make([]fleetMember, 0, len(m.runtimeSessions))

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
			label:       label,
			icon:        "⚡",
			model:       rs.model,
			active:      true,
			ctxUsed:     int(rs.contextTokens),
			ctxMax:      m.slotCtxMax(rs),
			threshold:   float64(m.workerCompactionThreshold) / 100,
			compactions: rs.compactions,
			tokensOut:   rs.tokensOut,
			costUSD:     rs.costUSD,
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

	// Line 3: context-window bar (+ compaction count when the member has
	// handed off at least once this client session).
	barW := contentWidth - 2
	suffix := ""
	if mem.compactions > 0 {
		suffix = fmt.Sprintf(" ↺%d", mem.compactions)
		barW -= lipgloss.Width(suffix)
	}
	b.WriteString("  " + renderMiniContextBar(mem.ctxUsed, mem.ctxMax, barW, false, mem.threshold) + DimStyle.Render(suffix))
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
	if mem.compactions > 0 {
		tail = append(tail, fmt.Sprintf("↺%d", mem.compactions))
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
	b.WriteString("  " + renderMiniContextBar(mem.ctxUsed, mem.ctxMax, barW, false, mem.threshold) + DimStyle.Render(tailStr))
	b.WriteString("\n")

	// Line 3: most-recent activity (workers only).
	if mem.activity != "" {
		b.WriteString(DimStyle.Render("  ↳ " + truncateStr(mem.activity, contentWidth-4)))
		b.WriteString("\n")
	}
	return b.String()
}

// renderFleetMemberLine renders a worker as a single sidebar row for compact
// mode: "<icon><spin> <label> ........ <ctx%> · <t/s>". Occupancy and throughput
// fold onto the one line (context first, so it survives a tight width); the
// model name and activity are dropped — both stay visible in the grid drill-in.
func (m Model) renderFleetMemberLine(mem fleetMember, contentWidth int) string {
	prefix := mem.icon + m.memStatusIcon(mem)

	var right []string
	if mem.ctxMax > 0 && mem.ctxUsed > 0 {
		pct := float64(mem.ctxUsed) / float64(mem.ctxMax)
		if pct > 1 {
			pct = 1
		}
		right = append(right, fmt.Sprintf("%.0f%%", pct*100))
	}
	if mem.hasTPS {
		right = append(right, fmt.Sprintf("%.0f t/s", mem.tps))
	}
	rightStr := strings.Join(right, " · ")

	labelMax := contentWidth - lipgloss.Width(prefix) - lipgloss.Width(rightStr) - 1
	if labelMax < 1 {
		labelMax = 1
	}
	left := prefix + truncateStr(mem.label, labelMax)
	gap := contentWidth - lipgloss.Width(left) - lipgloss.Width(rightStr)
	if gap < 1 {
		gap = 1
	}
	return SidebarValueStyle.Render(left) + strings.Repeat(" ", gap) + DimStyle.Render(rightStr)
}

// renderJobLine renders a job as a single sidebar row for compact mode:
// "<marker><glyph> <title> ........ <status>". Selection shows a ▶ marker and
// the accent style, matching the compact blocker rows. The glyph animates for
// active/pending jobs just like the full block.
func (m Model) renderJobLine(snap *service.JobSnapshot, contentWidth int, selected bool) string {
	glyph, statusWord, statusStyle, _ := jobStatusDecoration(snap)
	if snap.Status == service.JobStatusActive || snap.Status == service.JobStatusPending {
		glyph = string(spinnerChars[m.spinnerFrame%len(spinnerChars)])
	}
	marker := "  "
	if selected {
		marker = "▶ "
	}
	statusRendered := statusStyle.Render(statusWord)
	prefix := marker + glyph + " "
	titleMax := contentWidth - lipgloss.Width(prefix) - lipgloss.Width(statusRendered) - 1
	if titleMax < 1 {
		titleMax = 1
	}
	title := truncateStr(snap.Title, titleMax)
	gap := contentWidth - lipgloss.Width(prefix) - lipgloss.Width(title) - lipgloss.Width(statusRendered)
	if gap < 1 {
		gap = 1
	}
	titleStyle := JobBlockTitleStyle
	if selected {
		titleStyle = BlockerSelectedStyle
	}
	return titleStyle.Render(prefix+title) + strings.Repeat(" ", gap) + statusRendered
}

// renderOperatorBorderLabel builds the operator-stats strip embedded in the
// input box's top border. Segments render in display order but fit greedily
// left-to-right, so a tight input box drops the least-essential stats (cost,
// tokens, rate) first while the model name and context-window occupancy — the
// two worth watching while you type — survive. Returns "" when there's no room
// or nothing to show. The result's lipgloss.Width is <= maxWidth.
func (m Model) renderOperatorBorderLabel(maxWidth int) string {
	if maxWidth < 3 {
		return ""
	}
	op := m.operatorMember()

	type seg struct {
		plain string
		style lipgloss.Style
	}
	var segs []seg

	model := op.model
	if model == "" {
		model = "operator"
	}
	segs = append(segs, seg{op.icon + " " + model, SidebarValueStyle})

	if op.ctxMax > 0 && op.ctxUsed > 0 {
		pct := float64(op.ctxUsed) / float64(op.ctxMax)
		if pct > 1 {
			pct = 1
		}
		segs = append(segs, seg{
			fmt.Sprintf("%.0f%%", pct*100),
			lipgloss.NewStyle().Foreground(contextBarFillColor(pct, op.threshold, false)),
		})
	}
	if op.hasTPS {
		segs = append(segs, seg{fmt.Sprintf("%.0f t/s", op.tps), DimStyle})
	}
	if op.tokensOut > 0 {
		segs = append(segs, seg{formatTokenCount(op.tokensOut) + "↓", DimStyle})
	}
	if op.costUSD > 0 {
		segs = append(segs, seg{fmt.Sprintf("~$%.2f", op.costUSD), DimStyle})
	}
	if op.compactions > 0 {
		segs = append(segs, seg{fmt.Sprintf("↺%d", op.compactions), DimStyle})
	}

	const sep = " · "
	sepW := lipgloss.Width(sep)
	var parts []string
	width := 0
	for i, s := range segs {
		add := lipgloss.Width(s.plain)
		if i > 0 {
			add += sepW
		}
		if width+add > maxWidth {
			break
		}
		if i > 0 {
			parts = append(parts, DimStyle.Render(sep))
		}
		parts = append(parts, s.style.Render(s.plain))
		width += add
	}
	if len(parts) == 0 {
		// Even the first segment overflows: hard-truncate it so the border still
		// reads as the operator's rather than falling back to a bare rule.
		return SidebarValueStyle.Render(truncateStr(op.icon+" "+model, maxWidth))
	}
	return strings.Join(parts, "")
}

// spliceTopBorderLabel replaces the top border row of a rendered bordered box
// with a rule that embeds a label right-aligned, just before the top-right
// corner:
//
//	┌───────────── <label> ─┐
//
// box must be the fully rendered box with its top border intact; outerWidth is
// its total cell width; ruleColor styles the drawn border runs. The label is
// expected to already fit the width budget (outerWidth-5); if it doesn't the
// original box is returned unchanged rather than overflowing the line.
func spliceTopBorderLabel(box string, outerWidth int, label string, ruleColor color.Color) string {
	lines := strings.Split(box, "\n")
	if len(lines) == 0 {
		return box
	}
	rule := lipgloss.NewStyle().Foreground(ruleColor)
	// Fixed chrome around the label: "┌" (1) + " " (1) + " " (1) + "─┐" (2) = 5 cells.
	const chrome = 5
	fill := outerWidth - chrome - lipgloss.Width(label)
	if fill < 0 {
		return box
	}
	lines[0] = rule.Render("┌"+strings.Repeat("─", fill)) + " " + label + " " + rule.Render("─┐")
	return strings.Join(lines, "\n")
}

// renderMiniContextBar renders a single-line context-window occupancy bar with a
// trailing percentage (or a raw token count when the model's context length is
// unknown). When dim is set (a finished node), the fill is a muted green
// regardless of occupancy, so completed rows read as quiet history distinct
// from live workers.
//
// threshold is the compaction threshold as a fraction (0.5 = 50%); 0 means
// compaction is disabled. When set, a tick mark renders at the threshold
// position and the fill colors relative to it: green below (comfortable),
// yellow at or past it (compaction pending), red well past it (compaction
// overdue — disabled at runtime, failing, or not keeping up). Without a
// threshold, the legacy fixed 60%/85% color breakpoints apply.
func renderMiniContextBar(used, total, width int, dim bool, threshold float64) string {
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

	fillStyle := lipgloss.NewStyle().Foreground(contextBarFillColor(pct, threshold, dim))
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("237"))

	// Tick position: only meaningful when the total is known. Rendering is
	// segment-based (fill | tick | empty) so the bar stays a handful of
	// styled runs rather than per-cell escapes.
	tickIdx := -1
	if threshold > 0 && total > 0 {
		tickIdx = int(threshold * float64(barW))
		if tickIdx >= barW {
			tickIdx = barW - 1
		}
	}
	segment := func(from, to int) string {
		if from >= to {
			return ""
		}
		f := min(max(filled-from, 0), to-from)
		return fillStyle.Render(strings.Repeat("█", f)) +
			emptyStyle.Render(strings.Repeat("░", to-from-f))
	}

	var bar string
	if tickIdx < 0 {
		bar = segment(0, barW)
	} else {
		tick := contextBarTickStyle.Render("│")
		if tickIdx < filled {
			tick = contextBarTickEngulfedStyle.Render("│")
		}
		bar = segment(0, tickIdx) + tick + segment(tickIdx+1, barW)
	}
	return bar + DimStyle.Render(label)
}

// contextBarFillColor picks the fill color for a context bar. With a
// compaction threshold, colors are threshold-relative: green below it,
// yellow at or past it (compaction pending), red 15 points past it
// (compaction overdue — disabled at runtime, failing, or not keeping up).
// Without one, the legacy fixed 60%/85% breakpoints apply. A finished node
// (dim) always uses a muted green so it reads as quiet history.
func contextBarFillColor(pct, threshold float64, dim bool) color.Color {
	switch {
	case dim:
		return lipgloss.Color("#3f6b3f") // dim green
	case threshold > 0 && pct >= min(threshold+0.15, 1):
		return lipgloss.Color("#f5222d") // red: compaction overdue
	case threshold > 0 && pct >= threshold:
		return lipgloss.Color("#faad14") // yellow: compaction pending
	case threshold > 0:
		return lipgloss.Color("#52c41a") // green: below threshold
	case pct >= 0.85:
		return lipgloss.Color("#f5222d") // red (legacy breakpoints)
	case pct >= 0.6:
		return lipgloss.Color("#faad14") // yellow (legacy breakpoints)
	default:
		return lipgloss.Color("#52c41a") // green
	}
}

// contextBarTickStyle renders the compaction-threshold tick over the empty
// region of a context bar — brighter than the empty cells so it reads as a
// marker, dimmer than fill so it doesn't shout.
var contextBarTickStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

// contextBarTickEngulfedStyle renders the tick once the fill has passed it —
// a pale notch in the fill so the threshold stays visible.
var contextBarTickEngulfedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("255"))

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
