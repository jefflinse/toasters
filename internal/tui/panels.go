// Panel rendering: left panel (jobs and teams panes) and right sidebar.
package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/gateway"
	"github.com/jefflinse/toasters/internal/mcp"
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

	// Middle pane (Agents): content-driven height.
	// Count active gateway slots + runtime sessions for the agents pane.
	agentCount := 0
	if m.gateway != nil {
		for _, snap := range m.gateway.Slots() {
			if snap.Active {
				agentCount++
			}
		}
	}
	sortedRT := m.sortedRuntimeSessions()
	agentCount += len(sortedRT)
	middleContentH := 1 + agentCount // "Agents" header + one line per agent
	if agentCount == 0 {
		middleContentH = 2 // header + "No agents running"
	}
	if m.focused == focusAgents {
		middleContentH++ // hint line
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

	// Top pane gets whatever is left after middle + bottom + jobs hint.
	topContentH := availableH - middleContentH - bottomContentH - jobsHintH
	if topContentH < 3 {
		topContentH = 3
	}

	displayedJobs := m.displayJobs()

	// --- Top pane: Jobs ---
	var topLines []string
	jobsTitle := gradientText("Jobs", [3]uint8{0, 200, 200}, [3]uint8{175, 50, 200})
	if m.focused == focusJobs && m.focusAnimFrames > 0 {
		jobsTitle = rainbowText("Jobs", m.spinnerFrame)
	}
	topLines = append(topLines, jobsTitle)
	if len(displayedJobs) == 0 {
		topLines = append(topLines, PlaceholderPaneStyle.Render("No jobs"))
	} else {
		for i, j := range displayedJobs {
			// Job name row with status prefix icon.
			var statusPrefix string
			switch j.Status {
			case db.JobStatusActive:
				statusPrefix = "▶ "
			case db.JobStatusPaused:
				statusPrefix = "⏸ "
			case db.JobStatusCompleted:
				statusPrefix = "✓ "
			default:
				statusPrefix = "· "
			}
			name := truncateStr(j.Title, contentWidth-len([]rune(statusPrefix))-1)
			selected := i == m.selectedJob
			if selected {
				topLines = append(topLines, JobSelectedStyle.Render(statusPrefix+name))
			} else {
				topLines = append(topLines, JobItemStyle.Render(statusPrefix+name))
			}

			// Child items: only show for active/paused jobs (not completed).
			if j.Status != db.JobStatusCompleted {
				// BLOCKED child (always first if present).
				if m.hasBlocker(j) {
					blockerLine := "  ⚠ BLOCKED"
					topLines = append(topLines, TaskBlockedStyle.Render(blockerLine))
				}

				// SQLite task progress summary (if available from polling).
				if dbTasks := m.progress.tasks[j.ID]; len(dbTasks) > 0 {
					summary := renderJobProgressSummary(dbTasks)
					if summary != "" {
						topLines = append(topLines, DimStyle.Render("  ")+summary)
					}
				}

				// Task subitems from SQLite.
				if dbTasks := m.progress.tasks[j.ID]; len(dbTasks) > 0 {
					for _, task := range dbTasks {
						indicator, style := taskStatusIndicator(task.Status)
						title := task.Title
						if task.TeamID != "" {
							title += " (" + task.TeamID + ")"
						}
						taskLine := "  " + indicator + " " + truncateStr(title, contentWidth-5)
						topLines = append(topLines, style.Render(taskLine))
					}
				}
			}
		}
	}
	// Hint line when jobs pane is focused.
	if m.focused == focusJobs && len(displayedJobs) > 0 {
		dj := displayedJobs
		hint := "↑↓ · Enter → job details"
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

	// --- Middle pane: Agents ---
	var agentLines []string
	agentsTitle := gradientText("Agents", [3]uint8{50, 130, 255}, [3]uint8{0, 200, 200})
	if m.focused == focusAgents && m.focusAnimFrames > 0 {
		agentsTitle = rainbowText("Agents", m.spinnerFrame)
	}
	agentLines = append(agentLines, agentsTitle)

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
				agentLines = append(agentLines, JobSelectedStyle.Render("🍞 "+truncateStr(label, contentWidth-3)))
			} else if snap.Status == gateway.SlotDone {
				agentLines = append(agentLines, DimStyle.Render(statusIcon+truncateStr(label, contentWidth-2)))
			} else {
				agentLines = append(agentLines, SidebarValueStyle.Render(line))
			}
		}
	}

	// Runtime sessions.
	runtimeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
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
				agentLines = append(agentLines, DimStyle.Render("⚡"+statusIcon+truncateStr(label, contentWidth-4)))
			} else {
				agentLines = append(agentLines, line)
			}
		}
	}

	if !hasAnyGateway && !hasAnyRuntime {
		agentLines = append(agentLines, DimStyle.Italic(true).Render("No agents running"))
	}
	if m.focused == focusAgents {
		agentLines = append(agentLines, DimStyle.Render("Enter → grid view"))
	}

	middleContent := lipgloss.NewStyle().Height(middleContentH).Render(
		lipgloss.JoinVertical(lipgloss.Left, agentLines...),
	)
	middlePaneStyle := UnfocusedPaneStyle
	if m.focused == focusAgents {
		middlePaneStyle = FocusedPaneStyle
	}
	middlePane := middlePaneStyle.Width(panelWidth).Render(middleContent)

	// --- Bottom pane: Teams ---
	var bottomLines []string
	teamsTitle := gradientText("Teams", [3]uint8{255, 175, 0}, [3]uint8{0, 200, 200})
	if m.focused == focusTeams && m.focusAnimFrames > 0 {
		teamsTitle = rainbowText("Teams", m.spinnerFrame)
	}
	bottomLines = append(bottomLines, teamsTitle)
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
			// Append badge for system or auto teams.
			badge := ""
			if isSystemTeam(t) {
				badge = " ⚙"
			} else if isAutoTeam(t) {
				badge = " ↻"
			}
			name := truncateStr(t.Name, contentWidth-2)
			if m.focused == focusTeams && i == m.selectedTeam {
				line := JobSelectedStyle.Render(prefix + name + badge + " " + workerCount)
				bottomLines = append(bottomLines, line)
			} else {
				line := SidebarValueStyle.Bold(true).Render(prefix+name+badge) + " " + DimStyle.Render(workerCount)
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

// leftPanelAgentsPaneHeight returns the rendered height of the Agents middle pane
// in the left panel, for use in mouse hit-testing.
func (m *Model) leftPanelAgentsPaneHeight() int {
	paneFrameV := FocusedPaneStyle.GetVerticalBorderSize()
	agentCount := 0
	if m.gateway != nil {
		for _, snap := range m.gateway.Slots() {
			if snap.Active {
				agentCount++
			}
		}
	}
	sortedRT := m.sortedRuntimeSessions()
	agentCount += len(sortedRT)
	middleContentH := 1 + agentCount
	if agentCount == 0 {
		middleContentH = 2
	}
	if m.focused == focusAgents {
		middleContentH++
	}
	return middleContentH + paneFrameV
}

// leftPanelTeamsPaneHeight returns the rendered height of the Teams bottom pane
// in the left panel, for use in mouse hit-testing.
func (m *Model) leftPanelTeamsPaneHeight() int {
	paneFrameV := FocusedPaneStyle.GetVerticalBorderSize()
	bottomContentH := 1 + len(m.teams)
	if len(m.teams) == 0 {
		bottomContentH = 2
	}
	if m.focused == focusTeams && len(m.teams) > 0 {
		bottomContentH++
	}
	return bottomContentH + paneFrameV
}

// renderSidebar builds the right sidebar as two independent bordered panes
// stacked vertically: an operator/stats pane (top) and an MCP pane (bottom).
func (m Model) renderSidebar(sbWidth int) string {
	paneFrameH := FocusedPaneStyle.GetHorizontalBorderSize() + FocusedPaneStyle.GetHorizontalPadding()
	contentWidth := sbWidth - paneFrameH
	if contentWidth < 1 {
		contentWidth = 1
	}

	// --- Bottom pane: MCP ---
	var mcpSB strings.Builder
	mcpTitle := gradientText("MCP", [3]uint8{50, 130, 255}, [3]uint8{175, 50, 200})
	if m.focused == focusMCP && m.focusAnimFrames > 0 {
		mcpTitle = rainbowText("MCP", m.spinnerFrame)
	}
	mcpSB.WriteString(mcpTitle)
	mcpSB.WriteString("\n")

	hasMCP := false
	if m.mcpManager != nil {
		servers := m.mcpManager.Servers()
		if len(servers) > 0 {
			hasMCP = true
			var totalTools int
			for _, s := range servers {
				var icon string
				var style lipgloss.Style
				switch s.State {
				case mcp.ServerConnected:
					icon = "✓"
					style = ConnectedStyle
				case mcp.ServerFailed:
					icon = "✗"
					style = ErrorStyle
				default:
					icon = "○"
					style = DimStyle
				}
				totalTools += s.ToolCount

				label := s.Name
				toolInfo := fmt.Sprintf("(%d tools)", s.ToolCount)
				if s.State == mcp.ServerFailed {
					toolInfo = "(failed)"
				}

				line := style.Render(icon) + " " + truncateStr(label, contentWidth-len(icon)-len(toolInfo)-3) + " " + DimStyle.Render(toolInfo)
				mcpSB.WriteString(line)
				mcpSB.WriteString("\n")
			}

			// Summary line
			summary := fmt.Sprintf("%d servers, %d tools", len(servers), totalTools)
			mcpSB.WriteString(DimStyle.Render(summary))
			mcpSB.WriteString("\n")
		}
	}
	if !hasMCP {
		mcpSB.WriteString(DimStyle.Italic(true).Render("no MCP servers"))
		mcpSB.WriteString("\n")
	}
	if m.focused == focusMCP {
		mcpSB.WriteString(DimStyle.Render("Enter → MCP details"))
		mcpSB.WriteString("\n")
	}

	mcpPaneStyle := UnfocusedPaneStyle
	if m.focused == focusMCP {
		mcpPaneStyle = FocusedPaneStyle
	}
	// Ensure MCP pane is at least as tall as the input area so borders align.
	minMCPH := inputHeight + InputAreaStyle.GetVerticalFrameSize()
	mcpPane := mcpPaneStyle.Width(sbWidth).Render(mcpSB.String())
	mcpH := lipgloss.Height(mcpPane)
	if mcpH < minMCPH {
		mcpPane = mcpPaneStyle.Width(sbWidth).Height(minMCPH).Render(mcpSB.String())
		mcpH = minMCPH
	}

	// --- Top pane: Operator stats ---
	var sb strings.Builder

	connStatus := ConnectedStyle.Render("connected")
	if !m.stats.Connected {
		connStatus = ErrorStyle.Render("disconnected")
	}
	headerText := gradientText("operator", [3]uint8{255, 175, 0}, [3]uint8{175, 50, 200})
	if m.focused == focusOperator && m.focusAnimFrames > 0 {
		headerText = rainbowText("operator", m.spinnerFrame)
	}
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
	sb.WriteString(sidebarRow("Prompt ctx", fmt.Sprintf("%d", m.stats.PromptTokens)))
	sb.WriteString(sidebarRow("Tokens out", fmt.Sprintf("%d", liveCompletionTokens)))
	sb.WriteString(sidebarRow("Reasoning", fmt.Sprintf("%d", liveReasoningTokens)))

	tokPerSec := "-"
	if m.stats.TotalResponses > 0 && m.stats.TotalResponseTime > 0 {
		tps := float64(m.stats.CompletionTokens) / m.stats.TotalResponseTime.Seconds()
		tokPerSec = fmt.Sprintf("%.1f t/s", tps)
	} else if m.stream.streaming && m.stats.LastResponseTime > 0 && m.stats.CompletionTokensLive > 0 {
		tps := float64(m.stats.CompletionTokensLive) / m.stats.LastResponseTime.Seconds()
		tokPerSec = fmt.Sprintf("%.1f t/s", tps)
	}
	sb.WriteString(sidebarRow("Speed", tokPerSec))

	totalTokens := m.stats.PromptTokens + m.stats.CompletionTokensLive + m.stats.ReasoningTokensLive
	sb.WriteString(SidebarLabelStyle.Render("Context"))
	sb.WriteString("\n")
	sb.WriteString(renderContextBar(totalTokens, m.stats.SystemPromptTokens, m.stats.ContextLength, contentWidth, m.stream.streaming, m.spinnerFrame))
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

	// Calculate operator pane height so the sidebar fills the terminal exactly.
	operatorPaneH := m.height - mcpH
	if operatorPaneH < 3 {
		operatorPaneH = 3
	}

	operatorPaneStyle := UnfocusedPaneStyle
	if m.focused == focusOperator {
		operatorPaneStyle = FocusedPaneStyle
	}
	operatorPane := operatorPaneStyle.Width(sbWidth).Height(operatorPaneH).Render(sb.String())

	return lipgloss.JoinVertical(lipgloss.Left, operatorPane, mcpPane)
}

func sidebarRow(label, value string) string {
	return SidebarLabelStyle.Render(fmt.Sprintf("%-12s", label)) +
		SidebarValueStyle.Render(value) + "\n"
}

// taskStatusIndicator returns the status indicator rune and style for a db task status.
func taskStatusIndicator(status db.TaskStatus) (string, lipgloss.Style) {
	switch status {
	case db.TaskStatusPending:
		return "○", dbTaskPendingStyle
	case db.TaskStatusInProgress:
		return "◉", dbTaskInProgressStyle
	case db.TaskStatusCompleted:
		return "✓", dbTaskCompletedStyle
	case db.TaskStatusFailed:
		return "✗", dbTaskFailedStyle
	case db.TaskStatusBlocked:
		return "⊘", dbTaskBlockedStyle
	case db.TaskStatusCancelled:
		return "—", dbTaskCancelledStyle
	default:
		return "?", dbTaskPendingStyle
	}
}

// renderJobProgressSummary returns a summary line for a job's SQLite task progress.
// Returns an empty string if there are no tasks.
func renderJobProgressSummary(tasks []*db.Task) string {
	if len(tasks) == 0 {
		return ""
	}
	var completed, blocked, failed int
	for _, t := range tasks {
		switch t.Status {
		case db.TaskStatusCompleted:
			completed++
		case db.TaskStatusBlocked:
			blocked++
		case db.TaskStatusFailed:
			failed++
		}
	}
	if blocked > 0 {
		_, style := taskStatusIndicator(db.TaskStatusBlocked)
		return style.Render("⚠ BLOCKED")
	}
	if failed > 0 {
		_, style := taskStatusIndicator(db.TaskStatusFailed)
		return style.Render(fmt.Sprintf("%d failed", failed))
	}
	_, style := taskStatusIndicator(db.TaskStatusCompleted)
	return style.Render(fmt.Sprintf("%d/%d tasks ✓", completed, len(tasks)))
}

// formatTokenCount formats a token count compactly: ≥1000 → "1.2k", else as-is.
func formatTokenCount(n int64) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000.0)
	}
	return fmt.Sprintf("%d", n)
}
