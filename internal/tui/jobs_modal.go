// Jobs modal: job management UI including rendering, key handling, and job cancellation.
package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/db"
)

// jobsModalState holds all state for the /jobs modal overlay.
type jobsModalState struct {
	show          bool
	jobs          []*db.Job
	jobIdx        int
	tasks         map[string][]*db.Task
	progress      map[string][]*db.ProgressReport
	focus         int // 0=left (jobs list), 1=right (task detail)
	taskIdx       int
	confirmCancel bool
}

// loadJobsForModal loads all jobs from the store into the jobs modal state.
func (m *Model) loadJobsForModal() {
	if m.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	jobs, err := m.store.ListAllJobs(ctx)
	if err != nil {
		slog.Warn("failed to load jobs for modal", "error", err)
		return
	}
	m.jobsModal.jobs = jobs
}

// loadJobDetail loads tasks and recent progress for the currently selected job.
func (m *Model) loadJobDetail() {
	if m.store == nil {
		return
	}
	if m.jobsModal.tasks == nil {
		m.jobsModal.tasks = make(map[string][]*db.Task)
	}
	if m.jobsModal.progress == nil {
		m.jobsModal.progress = make(map[string][]*db.ProgressReport)
	}
	if len(m.jobsModal.jobs) == 0 || m.jobsModal.jobIdx >= len(m.jobsModal.jobs) {
		return
	}
	job := m.jobsModal.jobs[m.jobsModal.jobIdx]
	jobID := job.ID

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	tasks, err := m.store.ListTasksForJob(ctx, jobID)
	if err != nil {
		slog.Warn("failed to load tasks for job modal", "job", jobID, "error", err)
	} else {
		m.jobsModal.tasks[jobID] = tasks
	}

	progress, err := m.store.GetRecentProgress(ctx, jobID, 5)
	if err != nil {
		slog.Warn("failed to load progress for job modal", "job", jobID, "error", err)
	} else {
		m.jobsModal.progress[jobID] = progress
	}
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
		if m.jobsModal.focus == 0 {
			m.jobsModal.focus = 1
		} else {
			m.jobsModal.focus = 0
		}

	case "up":
		if m.jobsModal.focus == 0 {
			if m.jobsModal.jobIdx > 0 {
				m.jobsModal.jobIdx--
				m.jobsModal.taskIdx = 0
				m.loadJobDetail()
			}
		} else {
			if m.jobsModal.taskIdx > 0 {
				m.jobsModal.taskIdx--
			}
		}

	case "down":
		if m.jobsModal.focus == 0 {
			if m.jobsModal.jobIdx < len(m.jobsModal.jobs)-1 {
				m.jobsModal.jobIdx++
				m.jobsModal.taskIdx = 0
				m.loadJobDetail()
			}
		} else {
			if len(m.jobsModal.jobs) > 0 && m.jobsModal.jobIdx < len(m.jobsModal.jobs) {
				job := m.jobsModal.jobs[m.jobsModal.jobIdx]
				tasks := m.jobsModal.tasks[job.ID]
				if m.jobsModal.taskIdx < len(tasks)-1 {
					m.jobsModal.taskIdx++
				}
			}
		}

	case "ctrl+x":
		if m.jobsModal.focus == 0 && len(m.jobsModal.jobs) > 0 && m.jobsModal.jobIdx < len(m.jobsModal.jobs) {
			job := m.jobsModal.jobs[m.jobsModal.jobIdx]
			if job.Status == db.JobStatusActive || job.Status == db.JobStatusPending ||
				job.Status == db.JobStatusSettingUp || job.Status == db.JobStatusDecomposing {
				m.jobsModal.confirmCancel = true
			}
		}

	case "enter":
		if m.jobsModal.confirmCancel && len(m.jobsModal.jobs) > 0 && m.jobsModal.jobIdx < len(m.jobsModal.jobs) {
			job := m.jobsModal.jobs[m.jobsModal.jobIdx]
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := m.store.UpdateJobStatus(ctx, job.ID, db.JobStatusCancelled)
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
		}
	}

	return m, tea.Batch(modalCmds...)
}

// renderJobsModal renders the full-screen jobs management modal.
func (m *Model) renderJobsModal() string {
	jobs := m.jobsModal.jobs

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

	// Inner width after modal border + padding (border=2, padding=2 each side).
	innerW := modalW - ModalStyle.GetHorizontalFrameSize()
	if innerW < 10 {
		innerW = 10
	}

	// Left panel: ~30% of modal width.
	leftInnerW := innerW * 30 / 100
	if leftInnerW < 20 {
		leftInnerW = 20
	}
	leftPanelW := leftInnerW + ModalPanelStyle.GetHorizontalFrameSize()
	if leftPanelW > innerW/2 {
		leftPanelW = innerW / 2
		leftInnerW = leftPanelW - ModalPanelStyle.GetHorizontalFrameSize()
	}

	// Right panel: remaining width.
	rightPanelW := innerW - leftPanelW - 1 // -1 for spacing
	rightInnerW := rightPanelW - ModalPanelStyle.GetHorizontalFrameSize()
	if rightInnerW < 5 {
		rightInnerW = 5
	}

	// Panel inner height (subtract border + footer line).
	footerLines := 1
	panelH := modalH - ModalStyle.GetVerticalFrameSize() - footerLines - 1
	if panelH < 5 {
		panelH = 5
	}
	panelInnerH := panelH - ModalPanelStyle.GetVerticalFrameSize()
	if panelInnerH < 3 {
		panelInnerH = 3
	}

	// --- Left panel: jobs list ---
	var leftLines []string
	leftLines = append(leftLines, gradientText("Jobs", [3]uint8{0, 200, 200}, [3]uint8{175, 50, 200}))

	if len(jobs) == 0 {
		leftLines = append(leftLines, DimStyle.Render("No jobs."))
	} else {
		for i, j := range jobs {
			var icon string
			switch j.Status {
			case db.JobStatusActive:
				icon = "▶"
			case db.JobStatusPaused:
				icon = "⏸"
			case db.JobStatusCompleted:
				icon = "✓"
			case db.JobStatusFailed:
				icon = "✗"
			case db.JobStatusSettingUp:
				icon = "⚙"
			case db.JobStatusDecomposing:
				icon = "◈"
			default:
				icon = "·"
			}
			name := truncateStr(j.Title, leftInnerW-4)
			line := fmt.Sprintf("%s %s", icon, name)
			if j.Type != "" {
				line += " " + DimStyle.Render("["+j.Type+"]")
			}
			if i == m.jobsModal.jobIdx {
				line = ModalSelectedStyle.Width(leftInnerW).Render(fmt.Sprintf("%s %s", icon, name))
				if j.Type != "" {
					// Re-render with type badge outside the selected highlight to avoid style bleed.
					line = ModalSelectedStyle.Width(leftInnerW).Render(fmt.Sprintf("%s %s [%s]", icon, name, j.Type))
				}
			}
			leftLines = append(leftLines, line)
		}
	}

	// Pad left panel to fill height.
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

	// --- Right panel: job detail ---
	var rightLines []string

	if len(jobs) == 0 || m.jobsModal.jobIdx >= len(jobs) {
		rightLines = append(rightLines, DimStyle.Render("No job selected."))
	} else {
		job := jobs[m.jobsModal.jobIdx]
		tasks := m.jobsModal.tasks[job.ID]
		progress := m.jobsModal.progress[job.ID]

		// Header: job title.
		rightLines = append(rightLines, HeaderStyle.Render(truncateStr(job.Title, rightInnerW)))
		rightLines = append(rightLines, DimStyle.Render(strings.Repeat("─", rightInnerW)))

		// Status line with color coding.
		var statusStr string
		switch job.Status {
		case db.JobStatusActive:
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true).Render(string(job.Status))
		case db.JobStatusCompleted:
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render(string(job.Status))
		case db.JobStatusFailed:
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true).Render(string(job.Status))
		case db.JobStatusPaused:
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render(string(job.Status))
		case db.JobStatusSettingUp:
			// Muted yellow: system is preparing the job (e.g. cloning repos).
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("178")).Render(string(job.Status))
		case db.JobStatusDecomposing:
			// Muted cyan: LLM is decomposing the work into tasks.
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("38")).Render(string(job.Status))
		default:
			statusStr = DimStyle.Render(string(job.Status))
		}
		rightLines = append(rightLines, "Status: "+statusStr)

		// Description (if non-empty).
		if job.Description != "" {
			rightLines = append(rightLines, DimStyle.Render(truncateStr(job.Description, rightInnerW)))
		}

		// Workspace dir.
		if job.WorkspaceDir != "" {
			rightLines = append(rightLines, DimStyle.Render("Workspace: "+truncateStr(job.WorkspaceDir, rightInnerW-12)))
		}

		// Created at.
		rightLines = append(rightLines, DimStyle.Render("Created: "+job.CreatedAt.Format("2006-01-02 15:04")))

		rightLines = append(rightLines, "")

		// Tasks section.
		rightLines = append(rightLines, fmt.Sprintf("Tasks (%d)", len(tasks)))
		for i, task := range tasks {
			indicator, style := taskStatusIndicator(task.Status)
			line := indicator + " " + truncateStr(task.Title, rightInnerW-4)
			if m.jobsModal.focus == 1 && i == m.jobsModal.taskIdx {
				line = ModalSelectedStyle.Width(rightInnerW).Render(indicator + " " + truncateStr(task.Title, rightInnerW-4))
			} else {
				line = style.Render(line)
			}
			rightLines = append(rightLines, line)
		}

		rightLines = append(rightLines, "")

		// Recent progress section.
		rightLines = append(rightLines, "Recent Progress")
		for _, p := range progress {
			rightLines = append(rightLines, DimStyle.Render("["+p.CreatedAt.Format("15:04:05")+"] "+truncateStr(p.Message, rightInnerW-12)))
		}
		if len(progress) == 0 {
			rightLines = append(rightLines, DimStyle.Render("No recent progress."))
		}

		// Cancel confirmation.
		if m.jobsModal.confirmCancel {
			rightLines = append(rightLines, "")
			rightLines = append(rightLines, ModalWarningStyle.Render("⚠ Cancel this job? [Enter] confirm  [Esc] cancel"))
		}
	}

	// Pad right panel to fill height.
	for len(rightLines) < panelInnerH {
		rightLines = append(rightLines, "")
	}
	if len(rightLines) > panelInnerH {
		rightLines = rightLines[:panelInnerH]
	}

	rightContent := strings.Join(rightLines, "\n")
	var rightPanel string
	if m.jobsModal.focus == 1 {
		rightPanel = ModalFocusedPanel.Width(rightPanelW).Height(panelH).Render(rightContent)
	} else {
		rightPanel = ModalPanelStyle.Width(rightPanelW).Height(panelH).Render(rightContent)
	}

	// Join panels horizontally.
	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, " ", rightPanel)

	// Footer with key hints.
	cancelHint := "[Ctrl+X] Cancel Job"
	canCancel := len(jobs) > 0 && m.jobsModal.jobIdx < len(jobs) &&
		(jobs[m.jobsModal.jobIdx].Status == db.JobStatusActive || jobs[m.jobsModal.jobIdx].Status == db.JobStatusPending ||
			jobs[m.jobsModal.jobIdx].Status == db.JobStatusSettingUp || jobs[m.jobsModal.jobIdx].Status == db.JobStatusDecomposing)
	if !canCancel {
		cancelHint = DimStyle.Render(cancelHint)
	}
	footer := lipgloss.JoinHorizontal(lipgloss.Left,
		DimStyle.Render("[Tab] Switch"), "  ",
		cancelHint, "  ",
		DimStyle.Render("[Esc] Close"),
	)

	inner := lipgloss.JoinVertical(lipgloss.Left, panels, footer)
	modal := ModalStyle.Width(modalW).Render(inner)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}
