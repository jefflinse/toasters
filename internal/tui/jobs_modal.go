// Jobs modal: job management UI including rendering, key handling, and job cancellation.
package tui

import (
	"context"
	"fmt"
	"image/color"
	"log/slog"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// jobsModalState holds all state for the /jobs modal overlay.
type jobsModalState struct {
	show              bool
	jobs              []service.Job
	jobIdx            int
	tasks             map[string][]service.Task
	progress          map[string][]service.ProgressReport
	focus             int // 0=jobs list, 1=tasks list, 2=agent detail
	taskIdx           int
	confirmCancel     bool
	agentScrollOffset int // TODO: implement scrolling in the agent detail panel (v2)
	graphNodeIdx      int // focused node when the selected task has graph state
}

// loadJobsForModal loads all jobs from the service into the jobs modal state.
func (m *Model) loadJobsForModal() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	jobs, err := m.svc.Jobs().ListAll(ctx)
	if err != nil {
		slog.Warn("failed to load jobs for modal", "error", err)
		return
	}
	m.jobsModal.jobs = jobs
}

// syncJobsModalFromProgress refreshes the modal's jobs/tasks/progress
// snapshots from the model's live progress state (fed by progressPollMsg)
// so a long-open modal reflects task progress without re-polling the
// service. Selection is preserved by ID; if the previously selected job
// or task is gone, the index clamps to 0.
func (m *Model) syncJobsModalFromProgress() {
	if !m.jobsModal.show {
		return
	}

	var selectedJobID, selectedTaskID string
	if len(m.jobsModal.jobs) > 0 && m.jobsModal.jobIdx < len(m.jobsModal.jobs) {
		selectedJobID = m.jobsModal.jobs[m.jobsModal.jobIdx].ID
		if tasks := m.jobsModal.tasks[selectedJobID]; len(tasks) > 0 && m.jobsModal.taskIdx < len(tasks) {
			selectedTaskID = tasks[m.jobsModal.taskIdx].ID
		}
	}

	m.jobsModal.jobs = m.progress.jobs
	if m.jobsModal.tasks == nil {
		m.jobsModal.tasks = make(map[string][]service.Task)
	}
	for id, ts := range m.progress.tasks {
		m.jobsModal.tasks[id] = ts
	}
	if m.jobsModal.progress == nil {
		m.jobsModal.progress = make(map[string][]service.ProgressReport)
	}
	for id, rs := range m.progress.reports {
		m.jobsModal.progress[id] = rs
	}

	m.jobsModal.jobIdx = 0
	if selectedJobID != "" {
		for i, j := range m.jobsModal.jobs {
			if j.ID == selectedJobID {
				m.jobsModal.jobIdx = i
				break
			}
		}
	}

	m.jobsModal.taskIdx = 0
	if selectedJobID != "" && selectedTaskID != "" {
		if tasks := m.jobsModal.tasks[selectedJobID]; len(tasks) > 0 {
			for i, t := range tasks {
				if t.ID == selectedTaskID {
					m.jobsModal.taskIdx = i
					break
				}
			}
		}
	}
}

// loadJobDetail loads tasks and recent progress for the currently selected job.
func (m *Model) loadJobDetail() {
	if m.jobsModal.tasks == nil {
		m.jobsModal.tasks = make(map[string][]service.Task)
	}
	if m.jobsModal.progress == nil {
		m.jobsModal.progress = make(map[string][]service.ProgressReport)
	}
	if len(m.jobsModal.jobs) == 0 || m.jobsModal.jobIdx >= len(m.jobsModal.jobs) {
		return
	}
	job := m.jobsModal.jobs[m.jobsModal.jobIdx]
	jobID := job.ID

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	detail, err := m.svc.Jobs().Get(ctx, jobID)
	if err != nil {
		slog.Warn("failed to load job detail for modal", "job", jobID, "error", err)
		return
	}
	m.jobsModal.tasks[jobID] = detail.Tasks
	m.jobsModal.progress[jobID] = detail.Progress
}

// updateJobsModal handles all key presses when the jobs modal is open.
func (m *Model) updateJobsModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var modalCmds []tea.Cmd

	switch msg.String() {
	case "esc":
		if m.jobsModal.confirmCancel {
			m.jobsModal.confirmCancel = false
		} else {
			m.jobsModal.show = false
		}

	case "tab":
		m.jobsModal.focus = (m.jobsModal.focus + 1) % 3

	case "shift+tab":
		m.jobsModal.focus = (m.jobsModal.focus + 2) % 3

	case "up":
		switch m.jobsModal.focus {
		case 0:
			if m.jobsModal.jobIdx > 0 {
				m.jobsModal.jobIdx--
				m.jobsModal.taskIdx = 0
				m.jobsModal.agentScrollOffset = 0
				m.jobsModal.graphNodeIdx = 0
				m.loadJobDetail()
			}
		case 1:
			if m.jobsModal.taskIdx > 0 {
				m.jobsModal.taskIdx--
				m.jobsModal.agentScrollOffset = 0
				m.jobsModal.graphNodeIdx = 0
			}
		case 2:
			if gts := m.selectedJobsModalGraphTaskState(); gts != nil {
				if m.jobsModal.graphNodeIdx > 0 {
					m.jobsModal.graphNodeIdx--
				}
			}
		}

	case "down":
		switch m.jobsModal.focus {
		case 0:
			if m.jobsModal.jobIdx < len(m.jobsModal.jobs)-1 {
				m.jobsModal.jobIdx++
				m.jobsModal.taskIdx = 0
				m.jobsModal.agentScrollOffset = 0
				m.jobsModal.graphNodeIdx = 0
				m.loadJobDetail()
			}
		case 1:
			if len(m.jobsModal.jobs) > 0 && m.jobsModal.jobIdx < len(m.jobsModal.jobs) {
				job := m.jobsModal.jobs[m.jobsModal.jobIdx]
				tasks := m.jobsModal.tasks[job.ID]
				if m.jobsModal.taskIdx < len(tasks)-1 {
					m.jobsModal.taskIdx++
					m.jobsModal.agentScrollOffset = 0
					m.jobsModal.graphNodeIdx = 0
				}
			}
		case 2:
			if gts := m.selectedJobsModalGraphTaskState(); gts != nil {
				if m.jobsModal.graphNodeIdx < len(gts.topology.Nodes)-1 {
					m.jobsModal.graphNodeIdx++
				}
			}
		}

	case "ctrl+x":
		if m.jobsModal.focus == 0 && len(m.jobsModal.jobs) > 0 && m.jobsModal.jobIdx < len(m.jobsModal.jobs) {
			job := m.jobsModal.jobs[m.jobsModal.jobIdx]
			if job.Status == service.JobStatusActive || job.Status == service.JobStatusPending ||
				job.Status == service.JobStatusSettingUp {
				m.jobsModal.confirmCancel = true
			}
		}

	case "enter":
		if m.jobsModal.confirmCancel && len(m.jobsModal.jobs) > 0 && m.jobsModal.jobIdx < len(m.jobsModal.jobs) {
			job := m.jobsModal.jobs[m.jobsModal.jobIdx]
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := m.svc.Jobs().Cancel(ctx, job.ID)
			cancel()
			if err != nil {
				slog.Warn("failed to cancel job", "job", job.ID, "error", err)
				modalCmds = append(modalCmds, m.addToast("⚠ Cancel failed: "+err.Error(), toastWarning))
			} else {
				m.loadJobsForModal()
				m.loadJobDetail()
				modalCmds = append(modalCmds, m.addToast("✓ Job cancelled", toastSuccess))
			}
			m.jobsModal.confirmCancel = false
		} else if m.jobsModal.focus == 1 {
			// Drill into agent detail panel.
			m.jobsModal.focus = 2
		}
		// focus==0: no additional action beyond confirmCancel handling above.
		// focus==2: no-op.
	}

	return m, tea.Batch(modalCmds...)
}

// renderJobsModal renders the full-screen jobs management modal as a three-panel layout:
// Left (20%): job list, Middle (30%): task list for selected job, Right (~50%): agent cards for selected task.
func (m *Model) renderJobsModal() string {
	const columnGap = 1 // 1-column gap between panels, matching main layout convention

	// Modal dimensions: use most of the terminal.
	modalW := m.width - 4
	if modalW < 60 {
		modalW = 60
	}
	if modalW > m.width {
		modalW = m.width
	}
	modalH := m.height - 4
	if modalH < 20 {
		modalH = 20
	}

	// Inner width after modal border + padding.
	innerW := modalW - ModalStyle.GetHorizontalFrameSize()
	if innerW < 10 {
		innerW = 10
	}

	// Panel frame overhead (border + padding on each side).
	panelFrameW := ModalPanelStyle.GetHorizontalFrameSize()

	// Left panel: 20% of modal width, min 18 cols inner.
	leftInnerW := innerW * 20 / 100
	if leftInnerW < 18 {
		leftInnerW = 18
	}
	leftPanelW := leftInnerW + panelFrameW

	// Middle panel: 30% of modal width, min 24 cols inner.
	midInnerW := innerW * 30 / 100
	if midInnerW < 24 {
		midInnerW = 24
	}
	midPanelW := midInnerW + panelFrameW

	// Right panel: remaining width after left + middle + two gaps.
	rightPanelW := innerW - leftPanelW - midPanelW - 2*columnGap
	if rightPanelW < panelFrameW+4 {
		rightPanelW = panelFrameW + 4
	}
	rightInnerW := rightPanelW - panelFrameW
	if rightInnerW < 4 {
		rightInnerW = 4
	}

	// Panel height: subtract modal frame + footer line.
	footerLines := 1
	panelH := modalH - ModalStyle.GetVerticalFrameSize() - footerLines - 1 // -1 for visual breathing room
	if panelH < 5 {
		panelH = 5
	}
	panelInnerH := panelH - ModalPanelStyle.GetVerticalFrameSize()
	if panelInnerH < 3 {
		panelInnerH = 3
	}

	// Use the modal's own job list (same source as the key handler) for consistency.
	displayedJobs := m.jobsModal.jobs

	// Clamp jobIdx to valid bounds.
	jobIdx := m.jobsModal.jobIdx
	if jobIdx >= len(displayedJobs) {
		jobIdx = len(displayedJobs) - 1
	}
	if jobIdx < 0 {
		jobIdx = 0
	}

	// --- Left panel: jobs list ---
	var leftLines []string
	leftLines = append(leftLines, gradientText("Jobs", [3]uint8{0, 200, 200}, [3]uint8{175, 50, 200}))

	if len(displayedJobs) == 0 {
		leftLines = append(leftLines, DimStyle.Render("No jobs."))
	} else {
		for i, j := range displayedJobs {
			var icon string
			switch j.Status {
			case service.JobStatusActive:
				icon = "▶"
			case service.JobStatusPaused:
				icon = "⏸"
			case service.JobStatusCompleted:
				icon = "✓"
			case service.JobStatusFailed:
				icon = "✗"
			case service.JobStatusSettingUp:
				icon = "⚙"
			default:
				icon = "·"
			}
			name := truncateStr(j.Title, leftInnerW-4)
			if i == jobIdx {
				leftLines = append(leftLines, JobSelectedStyle.Width(leftInnerW).Render(fmt.Sprintf("%s %s", icon, name)))
			} else {
				leftLines = append(leftLines, JobItemStyle.Render(fmt.Sprintf("%s %s", icon, name)))
			}
		}
	}

	// Pad/trim left panel to fill height.
	for len(leftLines) < panelInnerH {
		leftLines = append(leftLines, "")
	}
	if len(leftLines) > panelInnerH {
		leftLines = leftLines[:panelInnerH]
	}

	leftContent := strings.Join(leftLines, "\n")
	var leftPanel string
	if m.jobsModal.focus == 0 {
		leftPanel = ModalFocusedPanel.Width(leftPanelW).Height(panelH).Render(leftContent)
	} else {
		leftPanel = ModalPanelStyle.Width(leftPanelW).Height(panelH).Render(leftContent)
	}

	// --- Middle panel: task list for selected job ---
	var midLines []string

	var selectedJob *service.Job
	var selectedJobTasks []service.Task
	if len(displayedJobs) > 0 && jobIdx < len(displayedJobs) {
		j := displayedJobs[jobIdx]
		selectedJob = &j
		selectedJobTasks = m.jobsModal.tasks[selectedJob.ID]
	}

	if selectedJob == nil {
		midLines = append(midLines, DimStyle.Render("No job selected."))
	} else {
		// Title: selected job's title as header.
		midLines = append(midLines, HeaderStyle.Render(truncateStr(selectedJob.Title, midInnerW)))

		// Status line with color coding.
		var statusStr string
		switch selectedJob.Status {
		case service.JobStatusActive:
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true).Render(string(selectedJob.Status))
		case service.JobStatusCompleted:
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render(string(selectedJob.Status))
		case service.JobStatusFailed:
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true).Render(string(selectedJob.Status))
		case service.JobStatusPaused:
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render(string(selectedJob.Status))
		case service.JobStatusSettingUp:
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("178")).Render(string(selectedJob.Status))
		default:
			statusStr = DimStyle.Render(string(selectedJob.Status))
		}
		midLines = append(midLines, "Status: "+statusStr)

		// Created timestamp.
		midLines = append(midLines, DimStyle.Render("Created: "+selectedJob.CreatedAt.Format("2006-01-02 15:04")))

		// Separator.
		midLines = append(midLines, DimStyle.Render(strings.Repeat("─", midInnerW)))

		// Cancel confirmation (shown in middle panel when active).
		if m.jobsModal.confirmCancel {
			midLines = append(midLines, ModalWarningStyle.Render("⚠ Cancel this job?"))
			midLines = append(midLines, DimStyle.Render("[Enter] confirm  [Esc] cancel"))
			midLines = append(midLines, "")
		}

		// Task list.
		if len(selectedJobTasks) == 0 {
			midLines = append(midLines, DimStyle.Render("No tasks yet"))
		} else {
			// Clamp taskIdx to valid bounds.
			taskIdx := m.jobsModal.taskIdx
			if taskIdx >= len(selectedJobTasks) {
				taskIdx = len(selectedJobTasks) - 1
			}
			if taskIdx < 0 {
				taskIdx = 0
			}

			for i, task := range selectedJobTasks {
				indicator, style := taskStatusIndicator(task.Status)
				taskTitle := truncateStr(task.Title, midInnerW-4)
				if i == taskIdx {
					midLines = append(midLines, ModalSelectedStyle.Width(midInnerW).Render(indicator+" "+taskTitle))
				} else {
					midLines = append(midLines, style.Render(indicator+" "+taskTitle))
				}
				// Graph nesting: 4-space indent, same status icon, graph name in dim gray.
				if task.GraphID != "" {
					graphIndicator, _ := taskStatusIndicator(task.Status)
					graphLine := "    " + DimStyle.Render(graphIndicator) + " " + TaskPendingStyle.Render(truncateStr(task.GraphID, midInnerW-7))
					midLines = append(midLines, graphLine)
				}
			}
		}
	}

	// Pad/trim middle panel to fill height.
	for len(midLines) < panelInnerH {
		midLines = append(midLines, "")
	}
	if len(midLines) > panelInnerH {
		midLines = midLines[:panelInnerH]
	}

	midContent := strings.Join(midLines, "\n")
	var midPanel string
	if m.jobsModal.focus == 1 {
		midPanel = ModalFocusedPanel.Width(midPanelW).Height(panelH).Render(midContent)
	} else {
		midPanel = ModalPanelStyle.Width(midPanelW).Height(panelH).Render(midContent)
	}

	// --- Right panel: graph view (when task has graph state) or agent cards. ---
	var rightPanel string
	{
		var rightLines []string

		// Resolve selected task.
		var selectedTask *service.Task
		if len(selectedJobTasks) > 0 {
			taskIdx := m.jobsModal.taskIdx
			if taskIdx >= len(selectedJobTasks) {
				taskIdx = len(selectedJobTasks) - 1
			}
			if taskIdx < 0 {
				taskIdx = 0
			}
			t := selectedJobTasks[taskIdx]
			selectedTask = &t
		}

		// Panel title.
		panelTitle := "Workers"
		if selectedTask != nil {
			panelTitle = truncateStr(selectedTask.Title, rightInnerW)
		}
		rightLines = append(rightLines, gradientText(panelTitle, [3]uint8{50, 130, 255}, [3]uint8{0, 200, 200}))

		// Branch: if the task has graph state, render graph + output instead
		// of the legacy agent cards.
		if selectedTask != nil {
			if gts, ok := m.graphTasks[selectedTask.ID]; ok {
				rightLines = append(rightLines, m.renderGraphTaskPane(gts, rightInnerW, panelInnerH-1)...)
				goto finishRightPanel
			}
		}

		if selectedTask == nil {
			// No task selected yet.
			placeholder := "Select a task"
			rightLines = append(rightLines, "")
			rightLines = append(rightLines, lipgloss.PlaceHorizontal(rightInnerW, lipgloss.Center, DimStyle.Render(placeholder)))
		} else {
			// Get runtime sessions for this task.
			sessions := m.runtimeSessionsForTask(selectedTask.ID)

			if len(sessions) == 0 {
				// Show context-appropriate placeholder.
				var placeholder string
				switch selectedTask.Status {
				case service.TaskStatusPending:
					placeholder = "No workers assigned"
				case service.TaskStatusInProgress:
					placeholder = "Waiting for workers..."
				case service.TaskStatusCompleted:
					placeholder = "Task completed"
				default:
					placeholder = "No workers assigned"
				}
				rightLines = append(rightLines, "")
				rightLines = append(rightLines, lipgloss.PlaceHorizontal(rightInnerW, lipgloss.Center, DimStyle.Render(placeholder)))
			} else {
				// Compute card height: divide available height evenly among sessions.
				// Available height = panelInnerH - 1 (title line already added).
				availH := panelInnerH - 1
				if availH < 1 {
					availH = 1
				}
				cardH := availH / len(sessions)
				if cardH < 6 {
					cardH = 6
				}
				if cardH > 12 {
					cardH = 12
				}
				cardInnerH := cardH - 2 // subtract border top+bottom
				if cardInnerH < 1 {
					cardInnerH = 1
				}
				cardInnerW := rightInnerW - 4 // subtract border (2) + padding (2)
				if cardInnerW < 1 {
					cardInnerW = 1
				}

				for _, rs := range sessions {
					// Choose border color: green for active, dim for completed.
					var borderColor color.Color
					if rs.status == "active" {
						borderColor = ColorConnected
					} else {
						borderColor = ColorDim
					}
					cardStyle := lipgloss.NewStyle().
						Width(rightInnerW).
						Height(cardH).
						Border(lipgloss.RoundedBorder()).
						BorderForeground(borderColor).
						Padding(0, 1)

					cardContent := renderAgentCard(rs, cardInnerW, cardInnerH, false, m.spinnerFrame)
					rendered := cardStyle.Render(cardContent)
					rightLines = append(rightLines, strings.Split(rendered, "\n")...)
				}
			}
		}

	finishRightPanel:
		// Pad/trim right panel to fill height.
		for len(rightLines) < panelInnerH {
			rightLines = append(rightLines, "")
		}
		if len(rightLines) > panelInnerH {
			rightLines = rightLines[:panelInnerH]
		}

		rightContent := strings.Join(rightLines, "\n")
		if m.jobsModal.focus == 2 {
			rightPanel = ModalFocusedPanel.Width(rightPanelW).Height(panelH).Render(rightContent)
		} else {
			rightPanel = ModalPanelStyle.Width(rightPanelW).Height(panelH).Render(rightContent)
		}
	}

	// Join all three panels horizontally with 1-column gaps.
	gapStr := strings.Repeat(" ", columnGap)
	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, gapStr, midPanel, gapStr, rightPanel)

	// Footer with key hints.
	cancelHint := "[Ctrl+X] Cancel Job"
	canCancel := selectedJob != nil &&
		(selectedJob.Status == service.JobStatusActive || selectedJob.Status == service.JobStatusPending ||
			selectedJob.Status == service.JobStatusSettingUp)
	if !canCancel {
		cancelHint = DimStyle.Render(cancelHint)
	}
	enterHint := DimStyle.Render("[Enter] View Workers")
	if m.jobsModal.focus != 1 {
		enterHint = ""
	}
	footerParts := []string{
		DimStyle.Render("[Tab] Switch Panel"), "  ",
		DimStyle.Render("[↑↓] Navigate"), "  ",
		cancelHint, "  ",
		DimStyle.Render("[Esc] Close"),
	}
	if enterHint != "" {
		footerParts = append(footerParts, "  ", enterHint)
	}
	footer := lipgloss.JoinHorizontal(lipgloss.Left, footerParts...)

	inner := lipgloss.JoinVertical(lipgloss.Left, panels, footer)
	modal := ModalStyle.Width(modalW).Render(inner)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}
