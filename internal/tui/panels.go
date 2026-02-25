// Panel rendering: left panel (jobs and teams panes) and right sidebar.
package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/gateway"
	"github.com/jefflinse/toasters/internal/job"
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
	// 3 panes × 2 rows border = 6 rows of border overhead.
	borderOverhead := 3 * paneFrameV

	// Bottom pane: content-driven height (header + one row per team + optional hint).
	bottomContentH := 1 + len(m.teams) // "Teams" header + one line per team
	if len(m.teams) == 0 {
		bottomContentH = 2 // header + "No teams configured"
	}
	if m.focused == focusTeams && len(m.teams) > 0 {
		bottomContentH++ // hint line
	}

	// Jobs hint line appears when the jobs pane is focused.
	jobsHintH := 0
	if m.focused == focusJobs && len(m.displayJobs()) > 0 {
		jobsHintH = 1
	}

	// Available height for content across all three panes.
	availableH := panelHeight - borderOverhead
	if availableH < 6 {
		availableH = 6
	}

	// Middle pane: fixed 30% of available content height.
	middleContentH := availableH * 30 / 100
	// Top pane gets whatever is left after middle + bottom + jobs hint.
	topContentH := availableH - middleContentH - bottomContentH - jobsHintH
	if topContentH < 3 {
		topContentH = 3
		// Re-derive middleContentH so the total still fits.
		middleContentH = availableH - topContentH - bottomContentH - jobsHintH
		if middleContentH < 2 {
			middleContentH = 2
		}
	}

	displayedJobs := m.displayJobs()

	// --- Top pane: Jobs ---
	var topLines []string
	topLines = append(topLines, gradientText("Jobs", [3]uint8{0, 200, 200}, [3]uint8{175, 50, 200}))
	if len(displayedJobs) == 0 {
		topLines = append(topLines, PlaceholderPaneStyle.Render("No jobs"))
	} else {
		for i, j := range displayedJobs {
			// Job name row with status prefix icon.
			var statusPrefix string
			switch j.Status {
			case job.StatusActive:
				statusPrefix = "▶ "
			case job.StatusPaused:
				statusPrefix = "⏸ "
			case job.StatusDone:
				statusPrefix = "✓ "
			default:
				statusPrefix = "· "
			}
			name := truncateStr(j.Name, contentWidth-len([]rune(statusPrefix))-1)
			selected := i == m.selectedJob
			if selected {
				topLines = append(topLines, JobSelectedStyle.Render(statusPrefix+name))
			} else {
				topLines = append(topLines, JobItemStyle.Render(statusPrefix+name))
			}

			// Child items: only show for active/paused jobs (not done).
			if j.Status != job.StatusDone {
				// Team + status sub-line (from first task).
				if tasks, err := job.ListTasks(j.Dir); err == nil && len(tasks) > 0 {
					t := tasks[0]
					if t.Team != "" {
						var prefix string
						switch t.Status {
						case job.StatusDone:
							prefix = "  ✓ "
						case job.StatusPaused:
							prefix = "  ⏸ "
						default:
							prefix = "  ◆ "
						}
						teamLine := prefix + truncateStr(t.Team, contentWidth-5)
						topLines = append(topLines, DimStyle.Render(teamLine))
					}
				}
				// BLOCKED child (always first if present).
				if m.hasBlocker(j) {
					blockerLine := "  ⚠ BLOCKED"
					topLines = append(topLines, TaskBlockedStyle.Render(blockerLine))
				}

				// Task subitems from TODO.md.
				if todosContent, err := job.ReadTodos(j.Dir); err == nil {
					lines := strings.Split(todosContent, "\n")
					for _, l := range lines {
						if strings.HasPrefix(l, "- [ ] ") {
							task := strings.TrimPrefix(l, "- [ ] ")
							taskLine := "  ○ " + truncateStr(task, contentWidth-5)
							topLines = append(topLines, TaskPendingStyle.Render(taskLine))
						} else if strings.HasPrefix(l, "- [x] ") {
							task := strings.TrimPrefix(l, "- [x] ")
							taskLine := "  ✓ " + truncateStr(task, contentWidth-5)
							topLines = append(topLines, TaskDoneStyle.Render(taskLine))
						}
					}
				}
			}
		}
	}
	// Hint line when jobs pane is focused.
	if m.focused == focusJobs && len(displayedJobs) > 0 {
		dj := displayedJobs
		hint := "↑↓ navigate"
		if m.selectedJob < len(dj) && m.hasBlocker(dj[m.selectedJob]) {
			hint = "Enter → resolve blocker"
		}
		topLines = append(topLines, DimStyle.Render(hint))
	}
	topContent := lipgloss.NewStyle().Height(topContentH + jobsHintH).Render(
		lipgloss.JoinVertical(lipgloss.Left, topLines...),
	)
	topPaneStyle := UnfocusedPaneStyle
	if m.focused == focusJobs {
		topPaneStyle = FocusedPaneStyle
	}
	topPane := topPaneStyle.Width(panelWidth).Render(topContent)

	// --- Middle pane: Job details (always unfocused) ---
	var middleLines []string
	if len(displayedJobs) == 0 || m.selectedJob >= len(displayedJobs) {
		middleLines = append(middleLines, LeftPanelHeaderStyle.Render("Job"))
		middleLines = append(middleLines, PlaceholderPaneStyle.Render("—"))
	} else {
		selectedJob := displayedJobs[m.selectedJob]
		middleLines = append(middleLines, LeftPanelHeaderStyle.Render(truncateStr(selectedJob.Name, contentWidth)))

		// Status badge
		var statusStyle lipgloss.Style
		switch selectedJob.Status {
		case job.StatusActive:
			statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("76"))
		case job.StatusPaused:
			statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("226"))
		default:
			statusStyle = DimStyle
		}
		statusWord := statusStyle.Render(string(selectedJob.Status))
		badge := DimStyle.Render("[") + statusWord + DimStyle.Render("]")
		middleLines = append(middleLines, badge)

		// Description (word-wrapped)
		if selectedJob.Description != "" {
			wrapped := wrapText(selectedJob.Description, contentWidth)
			for _, line := range strings.Split(wrapped, "\n") {
				middleLines = append(middleLines, DimStyle.Render(line))
			}
		}

		// TODO summary
		if todosContent, err := job.ReadTodos(selectedJob.Dir); err == nil {
			lines := strings.Split(todosContent, "\n")
			var pending []string
			doneCount := 0
			for _, l := range lines {
				if strings.HasPrefix(l, "- [ ] ") {
					pending = append(pending, strings.TrimPrefix(l, "- [ ] "))
				} else if strings.HasPrefix(l, "- [x] ") {
					doneCount++
				}
			}
			total := len(pending) + doneCount
			if total > 0 {
				summary := fmt.Sprintf("Tasks: %d/%d done", doneCount, total)
				middleLines = append(middleLines, DimStyle.Render(summary))
				shown := 0
				for _, task := range pending {
					if shown >= 3 {
						break
					}
					middleLines = append(middleLines, DimStyle.Render("· "+truncateStr(task, contentWidth-2)))
					shown++
				}
			}
		}
	}
	middleContent := lipgloss.NewStyle().Height(middleContentH).Render(
		lipgloss.JoinVertical(lipgloss.Left, middleLines...),
	)
	middlePane := UnfocusedPaneStyle.Width(panelWidth).Render(middleContent)

	// --- Bottom pane: Teams ---
	var bottomLines []string
	bottomLines = append(bottomLines, gradientText("Teams", [3]uint8{255, 175, 0}, [3]uint8{0, 200, 200}))
	if len(m.teams) == 0 {
		bottomLines = append(bottomLines, PlaceholderPaneStyle.Render("No teams configured"))
	} else {
		for i, t := range m.teams {
			teamColor := lipgloss.Color("135")
			if t.Coordinator != nil && t.Coordinator.Color != "" {
				teamColor = lipgloss.Color(t.Coordinator.Color)
			}
			prefix := lipgloss.NewStyle().Foreground(teamColor).Render("◆") + " "
			workerCount := fmt.Sprintf("(%d workers)", len(t.Workers))
			name := truncateStr(t.Name, contentWidth-2)
			if m.focused == focusTeams && i == m.selectedTeam {
				line := JobSelectedStyle.Render(prefix + name + " " + workerCount)
				bottomLines = append(bottomLines, line)
			} else {
				line := SidebarValueStyle.Bold(true).Render(prefix+name) + " " + DimStyle.Render(workerCount)
				bottomLines = append(bottomLines, line)
			}
		}
		if m.focused == focusTeams {
			bottomLines = append(bottomLines, DimStyle.Render("Enter → view team details"))
		}
	}
	bottomContent := lipgloss.JoinVertical(lipgloss.Left, bottomLines...)
	bottomPaneStyle := UnfocusedPaneStyle
	if m.focused == focusTeams {
		bottomPaneStyle = FocusedPaneStyle
	}
	bottomPane := bottomPaneStyle.Width(panelWidth).Render(bottomContent)

	inner := lipgloss.JoinVertical(lipgloss.Left, topPane, middlePane, bottomPane)
	return LeftPanelStyle.Width(panelWidth).Height(panelHeight).Render(inner)
}

// renderSidebar builds the right sidebar as two independent bordered panes
// stacked vertically: an operator/stats pane (top, fills remaining space)
// and an agents pane (bottom, auto-sized to content).
func (m Model) renderSidebar(sbWidth int) string {
	paneFrameH := FocusedPaneStyle.GetHorizontalBorderSize() + FocusedPaneStyle.GetHorizontalPadding()
	contentWidth := sbWidth - paneFrameH
	if contentWidth < 1 {
		contentWidth = 1
	}

	// --- Top pane: Operator stats ---
	var sb strings.Builder

	connStatus := ConnectedStyle.Render("connected")
	if !m.stats.Connected {
		connStatus = ErrorStyle.Render("disconnected")
	}
	headerText := gradientText("operator", [3]uint8{255, 175, 0}, [3]uint8{175, 50, 200})
	gap := contentWidth - lipgloss.Width(headerText) - lipgloss.Width(connStatus)
	if gap < 1 {
		gap = 1
	}
	sb.WriteString(headerText + strings.Repeat(" ", gap) + connStatus)
	sb.WriteString("\n\n")

	modelName := m.stats.ModelName
	if modelName == "" {
		modelName = "Loading..."
	}
	sb.WriteString(SidebarLabelStyle.Render("Model"))
	sb.WriteString("\n")
	sb.WriteString(SidebarValueStyle.Render(truncateStr(modelName, contentWidth)))
	sb.WriteString("\n\n")

	sb.WriteString(SidebarLabelStyle.Render("Endpoint"))
	sb.WriteString("\n")
	sb.WriteString(SidebarValueStyle.Render(truncateStr(m.stats.Endpoint, contentWidth)))
	sb.WriteString("\n")

	sb.WriteString("\n")

	// While streaming, blend in live estimates for the current response.
	liveCompletionTokens := m.stats.CompletionTokens + m.stats.CompletionTokensLive
	liveReasoningTokens := m.stats.ReasoningTokens + m.stats.ReasoningTokensLive

	sb.WriteString(sidebarRow("Messages", fmt.Sprintf("%d", m.stats.MessageCount)))
	sb.WriteString(sidebarRow("Tokens in", fmt.Sprintf("%d", m.stats.PromptTokens)))
	sb.WriteString(sidebarRow("Tokens out", fmt.Sprintf("%d", liveCompletionTokens)))
	sb.WriteString(sidebarRow("Reasoning", fmt.Sprintf("%d", liveReasoningTokens)))

	tokPerSec := "-"
	if m.stats.TotalResponses > 0 && m.stats.TotalResponseTime > 0 {
		tps := float64(m.stats.CompletionTokens) / m.stats.TotalResponseTime.Seconds()
		tokPerSec = fmt.Sprintf("%.1f t/s", tps)
	} else if m.streaming && m.stats.LastResponseTime > 0 && m.stats.CompletionTokensLive > 0 {
		tps := float64(m.stats.CompletionTokensLive) / m.stats.LastResponseTime.Seconds()
		tokPerSec = fmt.Sprintf("%.1f t/s", tps)
	}
	sb.WriteString(sidebarRow("Speed", tokPerSec))

	totalTokens := m.stats.PromptTokens + liveCompletionTokens + liveReasoningTokens
	sb.WriteString(SidebarLabelStyle.Render("Context"))
	sb.WriteString("\n")
	sb.WriteString(renderContextBar(totalTokens, m.stats.SystemPromptTokens, m.stats.ContextLength, contentWidth, m.streaming, m.spinnerFrame))
	sb.WriteString("\n")

	lastResp := "-"
	if m.stats.LastResponseTime > 0 {
		lastResp = fmt.Sprintf("%.1fs", m.stats.LastResponseTime.Seconds())
	}
	avgResp := "-"
	if m.stats.TotalResponses > 0 {
		avg := m.stats.TotalResponseTime / time.Duration(m.stats.TotalResponses)
		avgResp = fmt.Sprintf("%.1fs", avg.Seconds())
	}
	sb.WriteString(sidebarRow("Last resp", lastResp))
	sb.WriteString(sidebarRow("Avg resp", avgResp))

	// --- Bottom pane: Agents (auto-sized to content) ---
	var agentsSB strings.Builder
	agentsSB.WriteString(gradientText("Agents", [3]uint8{50, 130, 255}, [3]uint8{0, 200, 200}))
	agentsSB.WriteString("\n")

	hasAnyGateway := false
	if m.gateway != nil {
		slots := m.gateway.Slots()
		for i, snap := range slots {
			if !snap.Active {
				continue
			}
			hasAnyGateway = true
			label := snap.AgentName + " · " + snap.JobID
			var statusIcon string
			if snap.Status == gateway.SlotRunning {
				statusIcon = string(spinnerChars[m.spinnerFrame%len(spinnerChars)]) + " "
			} else {
				statusIcon = "✓ "
			}
			line := statusIcon + truncateStr(label, contentWidth-2)
			if m.focused == focusAgents && i == m.selectedAgentSlot {
				agentsSB.WriteString(JobSelectedStyle.Render("🍞 " + truncateStr(label, contentWidth-3)))
			} else if snap.Status == gateway.SlotDone {
				agentsSB.WriteString(DimStyle.Render(statusIcon + truncateStr(label, contentWidth-2)))
			} else {
				agentsSB.WriteString(SidebarValueStyle.Render(line))
			}
			agentsSB.WriteString("\n")
		}

		var totalAgentIn, totalAgentOut int
		for _, snap := range slots {
			totalAgentIn += snap.InputTokens
			totalAgentOut += snap.OutputTokens
		}
		if totalAgentIn > 0 || totalAgentOut > 0 {
			agentsSB.WriteString("\n")
			agentsSB.WriteString(sidebarRow("Agent ↑ tok", compactNum(totalAgentIn)))
			agentsSB.WriteString(sidebarRow("Agent ↓ tok", compactNum(totalAgentOut)))
			for i, snap := range slots {
				if snap.InputTokens > 0 || snap.OutputTokens > 0 {
					perSlot := fmt.Sprintf("  s%d: ↑%s ↓%s", i, compactNum(snap.InputTokens), compactNum(snap.OutputTokens))
					agentsSB.WriteString(DimStyle.Render(truncateStr(perSlot, contentWidth)))
					agentsSB.WriteString("\n")
				}
			}
		}
	}

	// Runtime sessions — sorted by start time for stable ordering.
	runtimeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("39")) // cyan/blue for runtime
	sortedRT := m.sortedRuntimeSessions()
	hasAnyRuntime := len(sortedRT) > 0
	if hasAnyRuntime {
		for _, rs := range sortedRT {
			label := rs.agentName + " · " + rs.jobID
			var statusIcon string
			if rs.status == "active" {
				statusIcon = string(spinnerChars[m.spinnerFrame%len(spinnerChars)]) + " "
			} else {
				statusIcon = "✓ "
			}
			prefix := runtimeStyle.Render("⚡")
			line := prefix + statusIcon + truncateStr(label, contentWidth-4)
			if rs.status != "active" {
				agentsSB.WriteString(DimStyle.Render("⚡" + statusIcon + truncateStr(label, contentWidth-4)))
			} else {
				agentsSB.WriteString(line)
			}
			agentsSB.WriteString("\n")
		}
	}

	if !hasAnyGateway && !hasAnyRuntime {
		agentsSB.WriteString(DimStyle.Italic(true).Render("No agents running"))
	}

	agentsPaneStyle := UnfocusedPaneStyle
	if m.focused == focusAgents {
		agentsPaneStyle = FocusedPaneStyle
	}

	// Ensure the agents pane is at least as tall as the input area so their
	// top borders align across the three columns.
	minAgentsH := inputHeight + InputAreaStyle.GetVerticalFrameSize()
	agentsPane := agentsPaneStyle.Width(sbWidth).Render(agentsSB.String())
	agentsH := lipgloss.Height(agentsPane)
	if agentsH < minAgentsH {
		agentsPane = agentsPaneStyle.Width(sbWidth).Height(minAgentsH).Render(agentsSB.String())
		agentsH = minAgentsH
	}

	// Calculate top pane height so the sidebar fills the terminal exactly.
	// agentsH includes the agents pane's border. Style.Height() sets the
	// outer height (including border/padding), so no extra subtraction needed.
	topContentH := m.height - agentsH
	if topContentH < 3 {
		topContentH = 3
	}

	topPaneStyle := UnfocusedPaneStyle
	topPane := topPaneStyle.Width(sbWidth).Height(topContentH).Render(sb.String())

	return lipgloss.JoinVertical(lipgloss.Left, topPane, agentsPane)
}

func sidebarRow(label, value string) string {
	return SidebarLabelStyle.Render(fmt.Sprintf("%-12s", label)) +
		SidebarValueStyle.Render(value) + "\n"
}
