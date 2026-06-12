// Jobs modal: job management UI including rendering, key handling, and job cancellation.
package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// jobsModalState holds all state for the /jobs modal overlay.
type jobsModalState struct {
	show             bool
	jobs             []service.Job
	jobIdx           int
	tasks            map[string][]service.Task
	progress         map[string][]service.ProgressReport
	focus            int // 0=jobs list, 1=tasks list, 2=worker detail
	taskIdx          int
	confirmCancel    bool
	confirmRetry     bool // armed by 'r' on a failed task; confirmed with Enter
	workerCardIdx    int  // focused worker among a non-graph task's sessions (legacy card pane)
	graphNodeIdx     int  // focused node when the selected task has graph state
	taskScrollOffset int  // line offset into the middle panel's task list

	// outputViewport scrolls the worker's streamed output in the graph
	// pane. It is shared across all displayed slots — when the focused
	// node changes (or the displayed slot changes for any other reason)
	// outputCurrentSlotID flips and the viewport jumps back to the bottom.
	outputViewport      viewport.Model
	outputCurrentSlotID string
	outputUserScrolled  bool
	outputViewportInit  bool
}

// openJobsModalForJob is the entry point for "deep-link" gestures from
// elsewhere in the TUI (the chat result block today, potentially toasts
// later). Pops the Jobs modal open with focus pre-positioned on the
// requested job — falls back to "first job" when the id isn't in the
// current snapshot, on the assumption a stale link is still better than
// no link.
func (m *Model) openJobsModalForJob(jobID string) tea.Cmd {
	m.jobsModal = jobsModalState{show: true}
	m.loadJobsForModal()
	for i, j := range m.jobsModal.jobs {
		if j.ID == jobID {
			m.jobsModal.jobIdx = i
			break
		}
	}
	m.loadJobDetail()
	var tickCmd tea.Cmd
	if !m.spinnerRunning {
		m.spinnerRunning = true
		tickCmd = spinnerTick()
	}
	return tickCmd
}

// openJobsModalForWorkerStream is the deep-link entry point for chat
// worker_stream blocks. Opens the Jobs modal pre-positioned to the
// snapshot's job + task + graph node so the user lands on the live
// output for that worker. The third panel takes focus directly so the
// next Tab/scroll lines up with what they came to see.
func (m *Model) openJobsModalForWorkerStream(snap *service.WorkerStreamSnapshot) tea.Cmd {
	if snap == nil {
		return nil
	}
	cmd := m.openJobsModalForJob(snap.JobID)
	if snap.TaskID != "" {
		for i, t := range m.modalTasks(snap.JobID) {
			if t.ID == snap.TaskID {
				m.jobsModal.taskIdx = i
				break
			}
		}
	}
	// Position the focused graph node when this worker is graph-driven.
	// Graph session IDs encode the node ("graph:<task>:<node>"); fall
	// back to the worker's logical name for non-graph sessions.
	if gts := m.graphTasks[snap.TaskID]; gts != nil && len(gts.topology.Nodes) > 0 {
		nodeName := graphNodeFromSessionID(snap.SessionID)
		if nodeName == "" {
			nodeName = snap.WorkerName
		}
		for i, n := range gts.topology.Nodes {
			if n == nodeName {
				m.jobsModal.graphNodeIdx = i
				break
			}
		}
	}
	m.jobsModal.focus = 2
	return cmd
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
		if tasks := m.modalTasks(selectedJobID); len(tasks) > 0 && m.jobsModal.taskIdx < len(tasks) {
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
		if tasks := m.modalTasks(selectedJobID); len(tasks) > 0 {
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
		switch {
		case m.jobsModal.confirmCancel:
			m.jobsModal.confirmCancel = false
		case m.jobsModal.confirmRetry:
			m.jobsModal.confirmRetry = false
		default:
			m.jobsModal.show = false
		}

	case "r":
		// Arm a retry confirmation for the selected task when it has failed.
		// Available from the tasks list (focus 1) or the graph pane (focus 2).
		if m.jobsModal.focus == 1 || m.jobsModal.focus == 2 {
			if t := m.selectedModalTask(); t != nil && t.Status == service.TaskStatusFailed {
				m.jobsModal.confirmRetry = true
			}
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
				m.jobsModal.workerCardIdx = 0
				m.jobsModal.graphNodeIdx = 0
				m.loadJobDetail()
			}
		case 1:
			if m.jobsModal.taskIdx > 0 {
				m.jobsModal.taskIdx--
				m.jobsModal.workerCardIdx = 0
				m.jobsModal.graphNodeIdx = 0
			}
		case 2:
			if gts := m.selectedJobsModalGraphTaskState(); gts != nil {
				if m.jobsModal.graphNodeIdx > 0 {
					m.jobsModal.graphNodeIdx--
				}
			} else if m.jobsModal.workerCardIdx > 0 {
				// Non-graph task: cycle the focused worker card.
				m.jobsModal.workerCardIdx--
			}
		}

	case "down":
		switch m.jobsModal.focus {
		case 0:
			if m.jobsModal.jobIdx < len(m.jobsModal.jobs)-1 {
				m.jobsModal.jobIdx++
				m.jobsModal.taskIdx = 0
				m.jobsModal.workerCardIdx = 0
				m.jobsModal.graphNodeIdx = 0
				m.loadJobDetail()
			}
		case 1:
			if len(m.jobsModal.jobs) > 0 && m.jobsModal.jobIdx < len(m.jobsModal.jobs) {
				job := m.jobsModal.jobs[m.jobsModal.jobIdx]
				tasks := m.modalTasks(job.ID)
				if m.jobsModal.taskIdx < len(tasks)-1 {
					m.jobsModal.taskIdx++
					m.jobsModal.workerCardIdx = 0
					m.jobsModal.graphNodeIdx = 0
				}
			}
		case 2:
			if gts := m.selectedJobsModalGraphTaskState(); gts != nil {
				if m.jobsModal.graphNodeIdx < len(gts.topology.Nodes)-1 {
					m.jobsModal.graphNodeIdx++
				}
			} else if t := m.selectedModalTask(); t != nil {
				// Non-graph task: cycle the focused worker card.
				if n := len(m.runtimeSessionsForTask(t.ID)); m.jobsModal.workerCardIdx < n-1 {
					m.jobsModal.workerCardIdx++
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
		if m.jobsModal.confirmRetry {
			if t := m.selectedModalTask(); t != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				err := m.svc.Jobs().RetryTask(ctx, t.ID)
				cancel()
				if err != nil {
					slog.Warn("failed to retry task", "task", t.ID, "error", err)
					modalCmds = append(modalCmds, m.addToast("⚠ Retry failed: "+err.Error(), toastWarning))
				} else {
					m.loadJobsForModal()
					m.loadJobDetail()
					modalCmds = append(modalCmds, m.addToast("↻ Task retrying", toastSuccess))
				}
			}
			m.jobsModal.confirmRetry = false
		} else if m.jobsModal.confirmCancel && len(m.jobsModal.jobs) > 0 && m.jobsModal.jobIdx < len(m.jobsModal.jobs) {
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
			// Drill into worker detail panel.
			m.jobsModal.focus = 2
		}
		// focus==0: no additional action beyond confirmCancel handling above.
		// focus==2: no-op.

	// Output viewport scrolling — only meaningful when the third panel
	// is focused and the viewport has been initialized by a prior
	// render. Up/Down stay bound to graph-node navigation; PgUp/PgDn,
	// Home/End, and shift+up/down move the streamed-output viewport.
	case "pgup":
		m.scrollGraphPaneOutput(-1, true)
	case "pgdown":
		m.scrollGraphPaneOutput(1, true)
	case "shift+up":
		m.scrollGraphPaneOutput(-1, false)
	case "shift+down":
		m.scrollGraphPaneOutput(1, false)
	case "home":
		if m.jobsModal.focus == 2 && m.jobsModal.outputViewportInit {
			m.jobsModal.outputViewport.GotoTop()
			m.jobsModal.outputUserScrolled = true
		}
	case "end":
		if m.jobsModal.focus == 2 && m.jobsModal.outputViewportInit {
			m.jobsModal.outputViewport.GotoBottom()
			m.jobsModal.outputUserScrolled = false
		}
	}

	return m, tea.Batch(modalCmds...)
}

// renderJobsModal renders the full-screen jobs management modal as a
// three-panel layout: Left (20%): job list, Middle (30%): task list for
// selected job, Right (~50%): worker cards for selected task. The modal
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
			leftLines = append(leftLines, renderJobUpdateBlock(snap, leftInnerW, i == jobIdx, m.spinnerFrame, true))
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
		selectedJobTasks = m.modalTasks(selectedJob.ID)
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

		// Type tag (e.g. bug_fix, new_feature) — classifies the job at a
		// glance; previously computed but never surfaced.
		if selectedJob.Type != "" {
			midLines = append(midLines, dimBg.Render("Type: "+truncateStr(selectedJob.Type, midInnerW-6)))
		}

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

		// Description — capped at 3 lines so it doesn't crowd the task list.
		if selectedJob.Description != "" {
			for _, l := range wrapToWidth(selectedJob.Description, midInnerW, 3) {
				midLines = append(midLines, dimBg.Render(l))
			}
		}

		// Separator.
		midLines = append(midLines, dimBg.Render(strings.Repeat("─", midInnerW)))

		// Recommendations for the selected task — follow-up guidance that's
		// otherwise dropped from the event stream. Shown above the task list so
		// it stays associated with the highlighted task. Capped to keep room.
		if len(selectedJobTasks) > 0 {
			ti := m.jobsModal.taskIdx
			if ti < 0 {
				ti = 0
			}
			if ti >= len(selectedJobTasks) {
				ti = len(selectedJobTasks) - 1
			}
			if rec := selectedJobTasks[ti].Recommendations; rec != "" {
				midLines = append(midLines, headerBg.Render("Recommendations"))
				for _, l := range wrapToWidth(rec, midInnerW, 4) {
					midLines = append(midLines, dimBg.Render(l))
				}
				midLines = append(midLines, dimBg.Render(strings.Repeat("─", midInnerW)))
			}
		}

		// Cancel confirmation (shown in middle panel when active).
		if m.jobsModal.confirmCancel {
			midLines = append(midLines, ModalWarningStyle.Background(bgColor).Render("⚠ Cancel this job?"))
			midLines = append(midLines, dimBg.Render("[Enter] confirm  [Esc] cancel"))
			midLines = append(midLines, "")
		}

		// Retry confirmation (shown in middle panel when active).
		if m.jobsModal.confirmRetry {
			midLines = append(midLines, ModalWarningStyle.Background(bgColor).Render("↻ Retry this failed task?"))
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

			// Depth of each visible task in the decomposition hierarchy, so
			// subtasks render indented under their parent and deep graphs read
			// as a tree instead of a flat list.
			taskByID := make(map[string]service.Task, len(selectedJobTasks))
			for _, t := range selectedJobTasks {
				taskByID[t.ID] = t
			}
			depthOf := func(t service.Task) int {
				d := 0
				for t.ParentID != "" && d <= 8 { // depth cap guards against cycles
					p, ok := taskByID[t.ParentID]
					if !ok {
						break
					}
					d++
					t = p
				}
				return d
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
				treePrefix := ""
				if d := depthOf(task); d > 0 {
					treePrefix = strings.Repeat("  ", d-1) + "└ "
				}
				taskTitle := truncateStr(task.Title, midInnerW-4-lipgloss.Width(treePrefix))
				if i == taskIdx {
					b.lines = append(b.lines, ModalSelectedStyle.Width(midInnerW).Render(treePrefix+indicator+" "+taskTitle))
				} else {
					b.lines = append(b.lines, style.Background(bgColor).Render(treePrefix+indicator+" "+taskTitle))
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

	// --- Right panel: graph view (when task has graph state) or worker cards. ---
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
		// of the legacy worker cards.
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
				// Non-graph task: focus one worker and stream its output through
				// the shared scrollable viewport so long output is no longer
				// clipped (the old fixed-height cards silently truncated). ↑↓
				// cycle workers; the graph-pane scroll handlers move the body.
				idx := m.jobsModal.workerCardIdx
				if idx >= len(sessions) {
					idx = len(sessions) - 1
				}
				if idx < 0 {
					idx = 0
				}
				focused := sessions[idx]

				if len(sessions) > 1 {
					rightLines = append(rightLines, dimBg.Render(fmt.Sprintf(
						"worker %d/%d · %s  (↑↓ switch)", idx+1, len(sessions), focused.workerName)))
				} else {
					rightLines = append(rightLines, dimBg.Render("worker: "+focused.workerName+" · "+focused.status))
				}

				bodyH := panelInnerH - len(rightLines)
				if bodyH < 1 {
					bodyH = 1
				}
				rightLines = append(rightLines, m.renderGraphPaneOutputViewport(focused, rightInnerW, bodyH)...)
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
	// Retry hint — only meaningful (and only highlighted) when the selected
	// task has actually failed.
	retryHint := ""
	if st := m.selectedModalTask(); st != nil && st.Status == service.TaskStatusFailed {
		retryHint = bgFill.Render("[R] Retry Task")
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
	if retryHint != "" {
		footerParts = append(footerParts, spacer, retryHint)
	}
	footer := bgFill.Width(m.width).Render(lipgloss.JoinHorizontal(lipgloss.Left, footerParts...))

	// Fill the terminal — no outer border, no outer inset. The breathing
	// row between panels and footer lives inside panelH's height math.
	// A subtle blue background tints the whole screen so it reads as
	// visually distinct from the main screen.
	content := lipgloss.JoinVertical(lipgloss.Left, panels, footer)
	return JobsScreenBgStyle.Width(m.width).Height(m.height).Render(content)
}

// systemDecomposeGraphs are the internal decomposition graph IDs whose tasks
// are scaffolding (coarse/fine decomposition + graph selection), not real
// user-facing work. Their tasks are hidden from the job tree unless --debug.
var systemDecomposeGraphs = map[string]bool{
	"coarse-decompose": true,
	"fine-decompose":   true,
}

// isSystemNode reports whether a graph node is internal decomposition
// scaffolding. Both coarse-decompose and fine-decompose use a single node
// named "decompose" (the coarse-/fine-decomposer roles); real work graphs name
// their phases otherwise (plan, investigate, implement, …), so the node name is
// an exact discriminator.
func isSystemNode(node string) bool {
	return node == "decompose"
}

// isSystemTask reports whether a task is internal decomposition scaffolding
// rather than real user-facing work.
func isSystemTask(t service.Task) bool {
	if systemDecomposeGraphs[t.GraphID] {
		return true
	}
	return strings.HasPrefix(t.Title, "Decompose:") || strings.HasPrefix(t.Title, "Pick graph:")
}

// visibleTasks filters out internal system steps unless debug mode is on, so
// the job tree shows real work the way Claude Code hides its own planning.
func (m *Model) visibleTasks(tasks []service.Task) []service.Task {
	if m.debug {
		return tasks
	}
	out := make([]service.Task, 0, len(tasks))
	for _, t := range tasks {
		if isSystemTask(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// modalTasks returns a job's task list filtered the same way the task tree
// renders it. ALL jobs-modal task access (navigation, selection, graph-state
// lookup, rendering) must go through this so taskIdx stays aligned with what
// the user actually sees — otherwise the index points at a different task than
// the one highlighted.
func (m *Model) modalTasks(jobID string) []service.Task {
	return m.visibleTasks(m.jobsModal.tasks[jobID])
}

// selectedModalTask returns the task currently highlighted in the jobs modal
// (the taskIdx-th visible task of the selected job), or nil if none. Used by the
// retry gesture and other per-task actions.
func (m *Model) selectedModalTask() *service.Task {
	if len(m.jobsModal.jobs) == 0 || m.jobsModal.jobIdx >= len(m.jobsModal.jobs) {
		return nil
	}
	tasks := m.modalTasks(m.jobsModal.jobs[m.jobsModal.jobIdx].ID)
	idx := m.jobsModal.taskIdx
	if idx < 0 || idx >= len(tasks) {
		return nil
	}
	return &tasks[idx]
}
