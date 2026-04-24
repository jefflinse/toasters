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
	// 2 panes × 2 rows border = 4 rows of border overhead.
	borderOverhead := 2 * paneFrameV

	// Bottom pane (Agents): content-driven height.
	// Use the filtered view (active + most-recent completed) so the pane's
	// height math matches what we actually render.
	sortedRT := m.displayRuntimeSessions()
	agentCount := len(sortedRT)
	// Each active worker with activity gets one extra "↳ <last-activity>" line
	// below it so users can see what it's doing without opening the grid.
	activityLineCount := 0
	for _, rs := range sortedRT {
		if rs.status == "active" {
			activityLineCount++
		}
	}
	bottomContentH := 1 + agentCount + activityLineCount // "Workers" header + one line per agent (+ activity line for active workers)
	if agentCount == 0 {
		bottomContentH = 2 // header + "No workers running"
	}
	if m.focused == focusAgents {
		bottomContentH++ // hint line
	}

	// Jobs hint line appears when the jobs pane is focused.
	jobsHintH := 0
	if m.focused == focusJobs && len(m.displayJobs()) > 0 {
		jobsHintH = 1
	}

	// Available height for content across all two panes.
	availableH := panelHeight - borderOverhead
	if availableH < 6 {
		availableH = 6
	}

	// Top pane gets whatever is left after bottom + jobs hint.
	topContentH := availableH - bottomContentH - jobsHintH
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
			case service.JobStatusActive:
				statusPrefix = "▶ "
			case service.JobStatusPaused:
				statusPrefix = "⏸ "
			case service.JobStatusCompleted:
				statusPrefix = "✓ "
			case service.JobStatusSettingUp:
				statusPrefix = "⚙ "
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

	// --- Bottom pane: Agents ---
	var agentLines []string
	agentsTitle := gradientText("Workers", [3]uint8{50, 130, 255}, [3]uint8{0, 200, 200})
	if m.focused == focusAgents && m.focusAnimFrames > 0 {
		agentsTitle = rainbowText("Workers", m.spinnerFrame)
	}
	agentLines = append(agentLines, agentsTitle)

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
				agentLines = append(agentLines, DimStyle.Render(indent+truncateStr(activityText, maxActivityW)))
			}
		}
	}

	if !hasAnyRuntime {
		agentLines = append(agentLines, DimStyle.Italic(true).Render("No workers running"))
	}
	if m.focused == focusAgents {
		agentLines = append(agentLines, DimStyle.Render("Enter → grid view"))
	}

	bottomContent := lipgloss.NewStyle().Height(bottomContentH).Render(
		lipgloss.JoinVertical(lipgloss.Left, agentLines...),
	)
	bottomPaneStyle := UnfocusedPaneStyle
	if m.focused == focusAgents {
		bottomPaneStyle = FocusedPaneStyle
	}
	bottomPane := bottomPaneStyle.Width(panelWidth).Render(bottomContent)

	inner := lipgloss.JoinVertical(lipgloss.Left, topPane, bottomPane)
	return LeftPanelStyle.Width(panelWidth).Height(panelHeight).Render(inner)
}

// leftPanelAgentsPaneHeight returns the rendered height of the Agents bottom pane
// in the left panel, for use in mouse hit-testing. Must stay in sync with the
// height math inside renderLeftPanel.
func (m *Model) leftPanelAgentsPaneHeight() int {
	paneFrameV := FocusedPaneStyle.GetVerticalBorderSize()
	sortedRT := m.displayRuntimeSessions()
	agentCount := len(sortedRT)
	activityLineCount := 0
	for _, rs := range sortedRT {
		if rs.status == "active" {
			activityLineCount++
		}
	}
	bottomContentH := 1 + agentCount + activityLineCount
	if agentCount == 0 {
		bottomContentH = 2
	}
	if m.focused == focusAgents {
		bottomContentH++
	}
	return bottomContentH + paneFrameV
}

// renderSidebar builds the right sidebar: a borderless operator/stats pane
// that fills the full sidebar height.
func (m Model) renderSidebar(sbWidth int) string {
	// Horizontal padding matches the frame width used by left-panel panes
	// (border 2 + padding 2 = 4 cols) so content sizing stays consistent.
	const sidebarHPad = 2
	contentWidth := sbWidth - 2*sidebarHPad
	if contentWidth < 1 {
		contentWidth = 1
	}

	// --- Operator stats ---
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

	// Operator pane fills the full sidebar height and renders borderless,
	// matching the horizontal frame width used by bordered panes so columns
	// line up.
	operatorPaneH := m.height
	if operatorPaneH < 3 {
		operatorPaneH = 3
	}

	operatorPaneStyle := lipgloss.NewStyle().Padding(0, sidebarHPad)
	return operatorPaneStyle.Width(sbWidth).Height(operatorPaneH).Render(sb.String())
}

func sidebarRow(label, value string) string {
	return SidebarLabelStyle.Render(fmt.Sprintf("%-12s", label)) +
		SidebarValueStyle.Render(value) + "\n"
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
