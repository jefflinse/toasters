// Jobs modal: job management UI including rendering, key handling, and job cancellation.
package tui

import (
	"context"
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
	taskScrollOffset  int // line offset into the middle panel's task list
}

// loadJobsForModal populates the modal's job list from the same filtered
// and sorted view used by the main-screen Jobs pane so the two surfaces
// read identically. Live updates arrive via syncJobsModalFromProgress.
func (m *Model) loadJobsForModal() {
	m.jobsModal.jobs = m.displayJobs()
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

	m.jobsModal.jobs = m.displayJobs()
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

// scrollJobsModal adjusts the middle panel's task-line scroll offset in
// response to a mouse-wheel event. The render path re-clamps the offset
// so the selected task stays visible, so runaway scrolling self-corrects
// once the user hits an arrow key again.
func (m *Model) scrollJobsModal(msg tea.MouseWheelMsg) {
	// Only wheel events on the middle panel scroll the task list today;
	// left/right panels already fit their content on screen or have
	// their own navigation keys. Keep the geometry check permissive —
	// if anything goes off, the offset just won't move.
	const step = 3
	switch msg.Button {
	case tea.MouseWheelUp:
		m.jobsModal.taskScrollOffset -= step
		if m.jobsModal.taskScrollOffset < 0 {
			m.jobsModal.taskScrollOffset = 0
		}
	case tea.MouseWheelDown:
		m.jobsModal.taskScrollOffset += step
		// Upper bound is clamped by the render path against the actual
		// task-line count; overshooting here is harmless.
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

// renderJobsModal renders the full-screen jobs management modal as a
// three-panel layout: Left (20%): job list, Middle (30%): task list for
// selected job, Right (~50%): agent cards for selected task. The modal
// fills the terminal — no outer frame — so it reads as a dedicated screen
// rather than a floating popup.
func (m *Model) renderJobsModal() string {
	const columnGap = 1 // 1-column gap between panels, matching main layout convention

	// Use the full terminal width — no horizontal inset so the left column
	// aligns flush with the main-screen Jobs pane (same column origin).
	innerW := m.width
	if innerW < 10 {
		innerW = 10
	}

	// Reapply the screen tint on every pane + gap + footer so ANSI resets
	// inside panel content don't revert individual cells to the terminal
	// default. Keeps the whole screen visually uniform as focus shifts.
	bgColor := JobsScreenBgStyle.GetBackground()
	panelUnfocused := ModalPanelStyle.Background(bgColor)
	// BorderBackground tints the cyan border glyphs themselves so the
	// rounded corners / horizontal runs sit on the navy field.
	panelFocused := ModalFocusedPanel.Background(bgColor).BorderBackground(bgColor)
	bgFill := lipgloss.NewStyle().Background(bgColor)
	// Sub-styles re-derived with the screen tint applied. Without these,
	// any ANSI reset inside the sub-style's render would drop the cell
	// back to the terminal default instead of the navy field.
	dimBg := DimStyle.Background(bgColor)
	// Drop HeaderStyle's Padding(0, 1) so the unfocused panel title lines
	// up with the body text at column 0 and matches the focused rainbow
	// title (which has no padding either).
	headerBg := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Background(bgColor)
	taskPendingBg := TaskPendingStyle.Background(bgColor)

	// Panel frame overhead (border + padding on each side).
	panelFrameW := panelUnfocused.GetHorizontalFrameSize()

	// Left panel: match the main-screen Jobs pane's outer width so the
	// job-block list reads at the same scale in both views. Min 18 cols
	// inner as a safety floor on narrow terminals.
	leftPanelW := leftPanelWidth(m.width)
	leftInnerW := leftPanelW - panelFrameW
	if leftInnerW < 18 {
		leftInnerW = 18
		leftPanelW = leftInnerW + panelFrameW
	}

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
	panelH := m.height - footerLines - 1 // -1 for visual breathing row at the bottom
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
	// Render each job as the same bordered block used in the main-screen
	// Jobs pane so the two views read as the same list. Blocks stack with
	// touching borders; selected block switches to a thick border.
	var leftLines []string
	leftTitle := gradientTextOn("Jobs", [3]uint8{0, 200, 200}, [3]uint8{175, 50, 200}, bgColor)
	if m.jobsModal.focus == 0 {
		leftTitle = rainbowTextOn("Jobs", m.spinnerFrame, bgColor)
	}
	leftLines = append(leftLines, leftTitle)

	if len(displayedJobs) == 0 {
		leftLines = append(leftLines, dimBg.Render("No jobs."))
	} else {
		for i, j := range displayedJobs {
			snap := m.buildJobSnapshot(j.ID)
			if snap == nil {
				continue
			}
			leftLines = append(leftLines, renderJobUpdateBlock(snap, leftInnerW, i == jobIdx, m.spinnerFrame))
		}
	}

	// Pad/trim left panel to fill the panel's inner height. Each element
	// in leftLines may be a multi-row string (job blocks render as 4 rows
	// each), so count visible rows with lipgloss.Height — counting slice
	// entries would under-count and let the bottom border drop off screen.
	leftContent := strings.Join(leftLines, "\n")
	if h := lipgloss.Height(leftContent); h < panelInnerH {
		leftContent += strings.Repeat("\n", panelInnerH-h)
	}
	var leftPanel string
	if m.jobsModal.focus == 0 {
		leftPanel = panelFocused.Width(leftPanelW).Height(panelH).Render(leftContent)
	} else {
		leftPanel = panelUnfocused.Width(leftPanelW).Height(panelH).Render(leftContent)
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
		midLines = append(midLines, dimBg.Render("No job selected."))
	} else {
		// Title: selected job's title as header. When the middle panel is
		// focused, cycle through a rainbow to match the main-screen idiom.
		titleText := truncateStr(selectedJob.Title, midInnerW)
		var midTitle string
		if m.jobsModal.focus == 1 {
			midTitle = rainbowTextOn(titleText, m.spinnerFrame, bgColor)
		} else {
			midTitle = headerBg.Render(titleText)
		}
		midLines = append(midLines, midTitle)

		// Status line with color coding.
		var statusStr string
		switch selectedJob.Status {
		case service.JobStatusActive:
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Background(bgColor).Bold(true).Render(string(selectedJob.Status))
		case service.JobStatusCompleted:
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Background(bgColor).Render(string(selectedJob.Status))
		case service.JobStatusFailed:
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Background(bgColor).Bold(true).Render(string(selectedJob.Status))
		case service.JobStatusPaused:
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Background(bgColor).Render(string(selectedJob.Status))
		case service.JobStatusSettingUp:
			statusStr = lipgloss.NewStyle().Foreground(lipgloss.Color("178")).Background(bgColor).Render(string(selectedJob.Status))
		default:
			statusStr = dimBg.Render(string(selectedJob.Status))
		}
		midLines = append(midLines, bgFill.Render("Status: ")+statusStr)

		// Created timestamp.
		midLines = append(midLines, dimBg.Render("Created: "+selectedJob.CreatedAt.Format("2006-01-02 15:04")))

		// Separator.
		midLines = append(midLines, dimBg.Render(strings.Repeat("─", midInnerW)))

		// Cancel confirmation (shown in middle panel when active).
		if m.jobsModal.confirmCancel {
			midLines = append(midLines, ModalWarningStyle.Background(bgColor).Render("⚠ Cancel this job?"))
			midLines = append(midLines, dimBg.Render("[Enter] confirm  [Esc] cancel"))
			midLines = append(midLines, "")
		}

		// Task list.
		if len(selectedJobTasks) == 0 {
			midLines = append(midLines, dimBg.Render("No tasks yet"))
		} else {
			// Clamp taskIdx to valid bounds.
			taskIdx := m.jobsModal.taskIdx
			if taskIdx >= len(selectedJobTasks) {
				taskIdx = len(selectedJobTasks) - 1
			}
			if taskIdx < 0 {
				taskIdx = 0
			}

			// Build per-task blocks (1 or 2 lines each) so the visible
			// window can include/exclude a whole task at a time. Keeping
			// the "task line" and its "graph-name sub-row" together in
			// one block prevents the sub-row from orphaning at the top
			// of the window when scrolling.
			type taskBlock struct{ lines []string }
			blocks := make([]taskBlock, len(selectedJobTasks))
			for i, task := range selectedJobTasks {
				var b taskBlock
				indicator, style := taskStatusIndicator(task.Status)
				taskTitle := truncateStr(task.Title, midInnerW-4)
				if i == taskIdx {
					b.lines = append(b.lines, ModalSelectedStyle.Width(midInnerW).Render(indicator+" "+taskTitle))
				} else {
					b.lines = append(b.lines, style.Background(bgColor).Render(indicator+" "+taskTitle))
				}
				if task.GraphID != "" {
					graphIndicator, _ := taskStatusIndicator(task.Status)
					graphLine := bgFill.Render("    ") + dimBg.Render(graphIndicator) + bgFill.Render(" ") + taskPendingBg.Render(truncateStr(task.GraphID, midInnerW-7))
					b.lines = append(b.lines, graphLine)
				}
				blocks[i] = b
			}

			// Flatten, remembering each block's line range.
			starts := make([]int, len(blocks)+1)
			for i, b := range blocks {
				starts[i+1] = starts[i] + len(b.lines)
			}
			var allTaskLines []string
			for _, b := range blocks {
				allTaskLines = append(allTaskLines, b.lines...)
			}

			// Budget of task lines after the header/status/separator rows.
			avail := panelInnerH - len(midLines)
			if avail < 1 {
				avail = 1
			}

			// Clamp taskScrollOffset: (a) keep the selected task fully
			// visible, (b) don't scroll past end, (c) don't scroll before
			// the start.
			off := m.jobsModal.taskScrollOffset
			selStart := starts[taskIdx]
			selEnd := starts[taskIdx+1]
			if selStart < off {
				off = selStart
			}
			if selEnd > off+avail {
				off = selEnd - avail
			}
			maxOff := starts[len(blocks)] - avail
			if maxOff < 0 {
				maxOff = 0
			}
			if off > maxOff {
				off = maxOff
			}
			if off < 0 {
				off = 0
			}
			m.jobsModal.taskScrollOffset = off

			end := off + avail
			if end > len(allTaskLines) {
				end = len(allTaskLines)
			}
			midLines = append(midLines, allTaskLines[off:end]...)
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
	if h := lipgloss.Height(midContent); h < panelInnerH {
		midContent += strings.Repeat("\n", panelInnerH-h)
	}
	var midPanel string
	if m.jobsModal.focus == 1 {
		midPanel = panelFocused.Width(midPanelW).Height(panelH).Render(midContent)
	} else {
		midPanel = panelUnfocused.Width(midPanelW).Height(panelH).Render(midContent)
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
		var rightTitleRendered string
		if m.jobsModal.focus == 2 {
			rightTitleRendered = rainbowTextOn(panelTitle, m.spinnerFrame, bgColor)
		} else {
			rightTitleRendered = gradientTextOn(panelTitle, [3]uint8{50, 130, 255}, [3]uint8{0, 200, 200}, bgColor)
		}
		rightLines = append(rightLines, rightTitleRendered)

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
			rightLines = append(rightLines, lipgloss.PlaceHorizontal(rightInnerW, lipgloss.Center, dimBg.Render(placeholder), lipgloss.WithWhitespaceStyle(bgFill)))
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
				rightLines = append(rightLines, lipgloss.PlaceHorizontal(rightInnerW, lipgloss.Center, dimBg.Render(placeholder), lipgloss.WithWhitespaceStyle(bgFill)))
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
			rightPanel = panelFocused.Width(rightPanelW).Height(panelH).Render(rightContent)
		} else {
			rightPanel = panelUnfocused.Width(rightPanelW).Height(panelH).Render(rightContent)
		}
	}

	// Join all three panels horizontally. The gap between panels has to be
	// a full-height vertical strip, not a one-row string — JoinHorizontal
	// pads the shorter operand with *unstyled* spaces, which would show as
	// untinted columns down the middle.
	gapRow := bgFill.Render(strings.Repeat(" ", columnGap))
	gapStrip := strings.Repeat(gapRow+"\n", panelH-1) + gapRow
	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, gapStrip, midPanel, gapStrip, rightPanel)

	// Footer with key hints.
	cancelHintText := "[Ctrl+X] Cancel Job"
	canCancel := selectedJob != nil &&
		(selectedJob.Status == service.JobStatusActive || selectedJob.Status == service.JobStatusPending ||
			selectedJob.Status == service.JobStatusSettingUp)
	var cancelHint string
	if !canCancel {
		cancelHint = dimBg.Render(cancelHintText)
	} else {
		cancelHint = bgFill.Render(cancelHintText)
	}
	enterHint := ""
	if m.jobsModal.focus == 1 {
		enterHint = dimBg.Render("[Enter] View Workers")
	}
	spacer := bgFill.Render("  ")
	footerParts := []string{
		dimBg.Render("[Tab] Switch Panel"), spacer,
		dimBg.Render("[↑↓] Navigate"), spacer,
		cancelHint, spacer,
		dimBg.Render("[Esc] Close"),
	}
	if enterHint != "" {
		footerParts = append(footerParts, spacer, enterHint)
	}
	footer := bgFill.Width(m.width).Render(lipgloss.JoinHorizontal(lipgloss.Left, footerParts...))

	// Fill the terminal — no outer border, no outer inset. The breathing
	// row between panels and footer lives inside panelH's height math.
	// A subtle blue background tints the whole screen so it reads as
	// visually distinct from the main screen.
	content := lipgloss.JoinVertical(lipgloss.Left, panels, footer)
	return JobsScreenBgStyle.Width(m.width).Height(m.height).Render(content)
}
