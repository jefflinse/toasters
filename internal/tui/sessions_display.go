package tui

import (
	"log/slog"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jefflinse/toasters/internal/service"
)

// selectedWorkerStream returns the snapshot for the currently selected
// chat entry when that entry is a WorkerStream, or nil otherwise. The
// counterpart to selectedJobResult — same shape, different kind.
func (m *Model) selectedWorkerStream() *service.WorkerStreamSnapshot {
	idx := m.chat.selectedMsgIdx
	if idx < 0 || idx >= len(m.chat.entries) {
		return nil
	}
	e := m.chat.entries[idx]
	if e.Kind != service.ChatEntryKindWorkerStream {
		return nil
	}
	return e.WorkerStream
}

// openWorkspaceDir spawns the host's "open this directory in the file
// manager" command and returns a toast describing the outcome. Picks the
// command per platform: macOS uses `open`, Windows uses `explorer`,
// everything else uses `xdg-open` (which is what most Linux desktops
// honor). Errors surface as a warning toast — we never block on the
// child process so a missing handler doesn't freeze the TUI.
func (m *Model) openWorkspaceDir(path string) tea.Cmd {
	if path == "" {
		return m.addToast("⚠ No workspace path on this job", toastWarning)
	}
	cmd, args := workspaceOpenCommand(path)
	if cmd == "" {
		return m.addToast("⚠ Don't know how to open paths on this OS", toastWarning)
	}
	exe := exec.Command(cmd, args...)
	if err := exe.Start(); err != nil {
		return m.addToast("⚠ open failed: "+err.Error(), toastWarning)
	}
	// Detach: we don't want zombies if the user closes the TUI before
	// the file manager finishes launching.
	go func() { _ = exe.Wait() }()
	return m.addToast("✓ Opened "+contractHomeDir(path), toastSuccess)
}

// workspaceOpenCommand returns the (program, args) tuple for opening dir
// on the current OS. Returns ("", nil) on platforms we don't recognize so
// callers can show a graceful error rather than executing junk.
func workspaceOpenCommand(dir string) (string, []string) {
	switch runtimeGOOS() {
	case "darwin":
		return "open", []string{dir}
	case "windows":
		return "explorer", []string{dir}
	case "linux", "freebsd", "openbsd", "netbsd":
		return "xdg-open", []string{dir}
	}
	return "", nil
}

// runtimeGOOS is split out so the test suite can override the platform
// detection without monkey-patching runtime.GOOS.
var runtimeGOOS = func() string { return runtime.GOOS }

// recentCompletedJobsWindow bounds how far back the Jobs pane surfaces
// jobs in a terminal state (completed / failed / cancelled). Anything
// older than this falls off the list.
const recentCompletedJobsWindow = 24 * time.Hour

// maxCompletedWorkersInPane caps how many non-active runtime sessions the
// Workers pane shows. Active sessions are always shown.
const maxCompletedWorkersInPane = 3

// displayJobs returns the filtered and sorted list of jobs for display in the left panel.
// Rules:
//   - Completed, failed, and cancelled jobs updated more than recentCompletedJobsWindow ago are hidden.
//   - Sort order: Active first, then Paused, then Completed/Failed/Cancelled.
//     Within each group, most-recently-updated (or created, if updated is zero)
//     is first, so the freshest activity floats to the top.
func (m Model) displayJobs() []service.Job {
	now := time.Now()
	cutoff := now.Add(-recentCompletedJobsWindow)

	var active, paused, done []service.Job
	for _, j := range m.jobs {
		switch j.Status {
		case service.JobStatusCompleted, service.JobStatusFailed, service.JobStatusCancelled:
			if !j.UpdatedAt.IsZero() && j.UpdatedAt.Before(cutoff) {
				continue // hide stale terminal-state jobs
			}
			done = append(done, j)
		case service.JobStatusPaused:
			paused = append(paused, j)
		default:
			active = append(active, j)
		}
	}

	// Most-recent first within each group. Fall back to CreatedAt when
	// UpdatedAt is zero (test fixtures, freshly-created jobs before the
	// first event).
	byFreshnessDesc := func(a, b service.Job) int {
		at := a.UpdatedAt
		if at.IsZero() {
			at = a.CreatedAt
		}
		bt := b.UpdatedAt
		if bt.IsZero() {
			bt = b.CreatedAt
		}
		return bt.Compare(at) // descending
	}
	slices.SortStableFunc(active, byFreshnessDesc)
	slices.SortStableFunc(paused, byFreshnessDesc)
	slices.SortStableFunc(done, byFreshnessDesc)

	result := make([]service.Job, 0, len(active)+len(paused)+len(done))
	result = append(result, active...)
	result = append(result, paused...)
	result = append(result, done...)
	return result
}

// jobByID returns the job with the given ID, or zero value and false if not found.
func (m *Model) jobByID(id string) (service.Job, bool) {
	for _, j := range m.jobs {
		if j.ID == id {
			return j, true
		}
	}
	return service.Job{}, false
}

// sortedRuntimeSessions returns the runtime sessions sorted for display:
// active sessions first, then completed/failed/cancelled, with startTime
// as the tiebreaker within each group. sessionID is used as a final stable
// tiebreaker to ensure deterministic ordering when two sessions share the
// same startTime (Go map iteration is randomized).
func (m *Model) sortedRuntimeSessions() []*runtimeSlot {
	slots := make([]*runtimeSlot, 0, len(m.runtimeSessions))
	for _, rs := range m.runtimeSessions {
		slots = append(slots, rs)
	}
	slices.SortFunc(slots, func(a, b *runtimeSlot) int {
		aActive := a.status == "active"
		bActive := b.status == "active"
		if aActive != bActive {
			if aActive {
				return -1 // active before inactive
			}
			return 1
		}
		if cmp := a.startTime.Compare(b.startTime); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.sessionID, b.sessionID) // stable tiebreaker
	})
	return slots
}

// filteredGridSessions returns the sorted runtime sessions narrowed by the
// grid's filter query (case-insensitive substring over job id, role/worker
// name, and status). With an empty query it is exactly sortedRuntimeSessions,
// so the grid render path and cell-resolution helper can share one source.
func (m *Model) filteredGridSessions() []*runtimeSlot {
	all := m.sortedRuntimeSessions()
	q := strings.ToLower(strings.TrimSpace(m.grid.filterQuery))
	if q == "" {
		return all
	}
	out := make([]*runtimeSlot, 0, len(all))
	for _, rs := range all {
		hay := strings.ToLower(rs.jobID + " " + rs.workerName + " " + rs.teamName + " " + rs.status)
		if strings.Contains(hay, q) {
			out = append(out, rs)
		}
	}
	return out
}

// gridTotalPages returns the number of grid pages for the current filtered
// session count and per-page cell capacity, always at least 1. This replaces
// the prior maxGridSlots-based count, which showed phantom pages whenever the
// live session count was below the 16-slot ceiling.
func (m *Model) gridTotalPages(cellsPerPage int) int {
	if cellsPerPage < 1 {
		cellsPerPage = 1
	}
	n := len(m.filteredGridSessions())
	pages := (n + cellsPerPage - 1) / cellsPerPage
	if pages < 1 {
		pages = 1
	}
	return pages
}

// displayRuntimeSessions returns the runtime sessions filtered for display
// in the Workers pane: every active session, plus at most
// maxCompletedWorkersInPane most-recently-ended non-active sessions.
// Ordering matches sortedRuntimeSessions (active first by start time, then
// activePlanningCount returns how many decomposition (system) graph nodes are
// currently running. displayRuntimeSessions hides these, so when real workers
// are idle the Workers pane would otherwise read "No workers running" while
// the job is in fact churning through decomposition — accurate but baffling.
// The pane uses this to say "Planning…" instead.
func (m *Model) activePlanningCount() int {
	n := 0
	for _, rs := range m.runtimeSessions {
		if rs.system && rs.status == "active" {
			n++
		}
	}
	return n
}

// terminal sessions by start time), so rendering code doesn't need to care.
func (m *Model) displayRuntimeSessions() []*runtimeSlot {
	all := m.sortedRuntimeSessions()

	// Hide internal decomposition sessions unless --debug, so the Workers pane
	// shows real work rather than the planning scaffolding.
	if !m.debug {
		filtered := all[:0:0]
		for _, rs := range all {
			if rs.system {
				continue
			}
			filtered = append(filtered, rs)
		}
		all = filtered
	}

	// Split active vs. terminal while preserving their existing order.
	active := make([]*runtimeSlot, 0, len(all))
	terminal := make([]*runtimeSlot, 0, len(all))
	for _, rs := range all {
		if rs.status == "active" {
			active = append(active, rs)
		} else {
			terminal = append(terminal, rs)
		}
	}

	if len(terminal) > maxCompletedWorkersInPane {
		// Keep the most recently finished ones. Fall back to startTime when
		// endTime is zero so sessions that never recorded an end still sort
		// sensibly.
		recencyOf := func(rs *runtimeSlot) time.Time {
			if !rs.endTime.IsZero() {
				return rs.endTime
			}
			return rs.startTime
		}
		slices.SortFunc(terminal, func(a, b *runtimeSlot) int {
			// Most recent first.
			return recencyOf(b).Compare(recencyOf(a))
		})
		terminal = terminal[:maxCompletedWorkersInPane]
		// Re-sort the kept slice back to start-time ascending so pane
		// ordering matches what sortedRuntimeSessions would have produced.
		slices.SortFunc(terminal, func(a, b *runtimeSlot) int {
			if c := a.startTime.Compare(b.startTime); c != 0 {
				return c
			}
			return strings.Compare(a.sessionID, b.sessionID)
		})
	}

	return append(active, terminal...)
}

// syncLeftPanelVisibility re-runs resizeComponents whenever the left-panel
// visibility has flipped since the last resize. Called as a defer from
// Update so state-driven changes (a job arriving, a worker ending) keep
// the chat viewport width in sync with the rendered layout.
func (m *Model) syncLeftPanelVisibility() {
	if m.width == 0 || m.height == 0 {
		// No initial WindowSizeMsg yet; nothing sensible to resize.
		return
	}
	if m.shouldShowLeftPanel() != m.lastLeftPanelShown {
		m.resizeComponents()
	}
}

// shouldShowLeftPanel reports whether the left panel (Jobs + Workers) should
// be rendered. Resolution order, outermost gate first:
//
//  1. Width gate — terminals narrower than minWidthForLeftPanel never show
//     the panel regardless of preferences (geometry wins).
//  2. Explicit user override (ctrl+j) — pins the panel until cleared.
//  3. Settings default — when ShowJobsPanelByDefault is true the panel
//     stays visible even with no content.
//  4. Content fallback — show only when there's a job or runtime session
//     to surface (the original behavior, preserved as the default).
func (m *Model) shouldShowLeftPanel() bool {
	if m.width < minWidthForLeftPanel {
		return false
	}
	if m.leftPanelOverride != nil {
		return *m.leftPanelOverride
	}
	if m.showJobsPanelDefault {
		return true
	}
	if len(m.displayJobs()) > 0 {
		return true
	}
	if len(m.displayRuntimeSessions()) > 0 {
		return true
	}
	return false
}

// applyPanelVisibilityDefaults caches the panel-visibility settings from a
// freshly loaded or saved Settings snapshot, then runs resizeComponents so
// any change in the effective visibility takes effect immediately.
//
// The startup load path always keeps any user-set override intact (it's nil
// at that point anyway). On a /settings save, the user has just expressed
// an explicit preference, so we drop the override too — otherwise a stale
// ctrl+j toggle could mask the new default and the save would feel
// silently broken.
func (m *Model) applyPanelVisibilityDefaults(s service.Settings) {
	m.showJobsPanelDefault = s.ShowJobsPanelByDefault
	m.showOperatorPanelDefault = s.ShowOperatorPanelByDefault
	if m.settingsModal.show {
		// Heuristic for "this came from a save, not the initial load":
		// the modal is open. Clear overrides so the new default wins.
		m.leftPanelOverride = nil
		m.sidebarOverride = nil
	}
	if m.width > 0 && m.height > 0 {
		m.resizeComponents()
	}
}

// shouldShowSidebar reports whether the right Operator/sidebar panel should
// be rendered. Same resolution shape as shouldShowLeftPanel: width gate →
// explicit override → settings default. The legacy default kept the
// sidebar visible whenever the terminal was wide enough; that's preserved
// when ShowOperatorPanelByDefault is true (the default).
func (m *Model) shouldShowSidebar() bool {
	if m.width < minWidthForBar {
		return false
	}
	if m.sidebarOverride != nil {
		return *m.sidebarOverride
	}
	return m.showOperatorPanelDefault
}

// runtimeSessionsForTask returns all runtime sessions associated with the given task ID,
// sorted with active sessions first, then completed, ordered by start time within each group.
func (m *Model) runtimeSessionsForTask(taskID string) []*runtimeSlot {
	var slots []*runtimeSlot
	for _, rs := range m.runtimeSessions {
		if rs.taskID == taskID {
			slots = append(slots, rs)
		}
	}
	slices.SortFunc(slots, func(a, b *runtimeSlot) int {
		aActive := a.status == "active"
		bActive := b.status == "active"
		if aActive != bActive {
			if aActive {
				return -1 // active before inactive
			}
			return 1
		}
		if cmp := a.startTime.Compare(b.startTime); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.sessionID, b.sessionID) // stable tiebreaker
	})
	if slots == nil {
		return []*runtimeSlot{}
	}
	return slots
}

// runtimeSessionForGridCell returns the runtime session displayed in the given
// grid cell index (within the current page), or nil if the cell does not
// contain a runtime session.
func (m *Model) runtimeSessionForGridCell(cellIdx int) *runtimeSlot {
	cols := m.grid.gridCols
	rows := m.grid.gridRows
	// Safety floor: mirrors the floor applied in renderGrid.
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	cellsPerPage := cols * rows
	pageOffset := m.grid.gridPage * cellsPerPage

	sortedRT := m.filteredGridSessions()

	// The absolute index into the sorted runtime session list for the given cell.
	absIdx := pageOffset + cellIdx
	if absIdx < len(sortedRT) {
		return sortedRT[absIdx]
	}
	return nil
}

// formatFeedEntry returns a styled string for a service.FeedEntry.
// maxWidth is used to word-wrap long content (e.g. blocker descriptions).
func formatFeedEntry(entry service.FeedEntry, maxWidth int) string {
	switch entry.EntryType {
	case service.FeedEntryTypeSystemEvent:
		return FeedSystemEventStyle.Render("  ⚙ " + entry.Content)
	case service.FeedEntryTypeConsultationTrace:
		return FeedConsultationTraceStyle.Render("    ↳ " + entry.Content)
	case service.FeedEntryTypeTaskStarted:
		return FeedTaskStartedStyle.Render("⚡ " + entry.Content)
	case service.FeedEntryTypeTaskCompleted:
		return FeedTaskCompletedStyle.Render("✓ " + entry.Content)
	case service.FeedEntryTypeTaskFailed:
		return FeedTaskFailedStyle.Render("✗ " + entry.Content)
	case service.FeedEntryTypeBlockerReported:
		text := "🚫 " + entry.Content
		if maxWidth > 4 {
			text = wrapText(text, maxWidth-4)
		}
		return FeedBlockerReportedStyle.Render(text)
	case service.FeedEntryTypeJobComplete:
		return FeedJobCompleteStyle.Render("✅ " + entry.Content)
	case service.FeedEntryTypeUserMessage, service.FeedEntryTypeOperatorMessage:
		// These are already rendered as chat entries; skip to avoid duplication.
		return ""
	default:
		slog.Debug("unhandled feed entry type", "type", entry.EntryType)
		return DimStyle.Render(entry.Content)
	}
}
