package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/jefflinse/toasters/internal/service"
)

// upsertJobUpdateEntry refreshes the block for the job referenced by ev.
// If a block for this job already exists in entries it mutates in place
// (preserving conversational position); otherwise a new entry is appended
// at the tail so the block shows up as soon as the first event arrives —
// even mid-operator-turn, so task progress is visible in real time.
//
// Returns a pointer to the snapshot that ends up stored (or nil when no
// snapshot is available yet).
func (m *Model) upsertJobUpdateEntry(ev service.Event) *service.JobSnapshot {
	jobID := jobIDFromEvent(ev)
	if jobID == "" {
		return nil
	}
	snap := m.buildJobSnapshot(jobID)
	if snap == nil {
		// Live state hasn't caught up yet (the very first JobCreated event
		// arrives before the next progressPollMsg refreshes m.jobs). Seed
		// a minimal snapshot from the event payload so the block appears
		// immediately; refreshJobUpdateEntries will fill in the real
		// counts on the next poll tick.
		snap = jobSnapshotFromEventPayload(ev)
		if snap == nil {
			return nil
		}
	}

	for i := range m.chat.entries {
		e := &m.chat.entries[i]
		if e.Kind == service.ChatEntryKindJobUpdate && e.JobUpdate != nil && e.JobUpdate.JobID == jobID {
			*e.JobUpdate = *snap
			e.Timestamp = snap.UpdatedAt
			return e.JobUpdate
		}
	}

	m.appendEntry(service.ChatEntry{
		Kind:      service.ChatEntryKindJobUpdate,
		Timestamp: snap.UpdatedAt,
		JobUpdate: snap,
	})
	return m.chat.entries[len(m.chat.entries)-1].JobUpdate
}

// refreshJobUpdateEntries rebuilds the snapshot on every existing job-update
// entry from the model's current job + task state. Called when fresh
// progress state arrives (progressPollMsg) so blocks stay in sync with
// truth — discrete job events like JobCompleted otherwise race the
// progress update and leave the block stuck on a stale status.
// Returns true if any entry was changed.
func (m *Model) refreshJobUpdateEntries() bool {
	changed := false
	for i := range m.chat.entries {
		e := &m.chat.entries[i]
		if e.Kind != service.ChatEntryKindJobUpdate || e.JobUpdate == nil {
			continue
		}
		snap := m.buildJobSnapshot(e.JobUpdate.JobID)
		if snap == nil {
			continue
		}
		if *e.JobUpdate != *snap {
			*e.JobUpdate = *snap
			e.Timestamp = snap.UpdatedAt
			changed = true
		}
	}
	return changed
}

// buildJobSnapshot assembles a JobSnapshot for the given jobID from the
// model's current job + task state. Returns nil when the job isn't known
// yet (e.g. an event referenced a job not yet reflected in m.jobs).
func (m *Model) buildJobSnapshot(jobID string) *service.JobSnapshot {
	job, ok := m.jobByID(jobID)
	if !ok {
		return nil
	}
	var completed, failed int
	// Count only user-facing tasks so the headline "N/M tasks" matches the
	// task tree (which hides decomposition scaffolding unless --debug).
	tasks := m.visibleTasks(m.progress.tasks[jobID])
	for _, t := range tasks {
		switch t.Status {
		case service.TaskStatusCompleted:
			completed++
		case service.TaskStatusFailed:
			failed++
		}
	}
	return &service.JobSnapshot{
		JobID:          job.ID,
		Title:          job.Title,
		Status:         job.Status,
		TasksCompleted: completed,
		TasksTotal:     len(tasks),
		TasksFailed:    failed,
		CreatedAt:      job.CreatedAt,
		UpdatedAt:      job.UpdatedAt,
	}
}

// renderJobUpdateBlock draws a compact bordered block summarizing a job's
// current state. Width is the total outer width (border + padding + content)
// the block should occupy. The block renders two content rows — a header
// line with a status glyph + title + status word, and a meta line with a
// short id and task rollup — so its total height is fixed at 4 rows.
//
// When selected is true, the border is drawn thick instead of rounded so
// the block reads as the current selection — useful when the block is
// used in a list context like the Jobs pane.
//
// spinnerFrame animates the glyph for active/pending jobs when animated is
// true. Pass animated=true only from callers rendered on every tick (the
// sidebar); the chat viewport is re-rendered only on events, so it passes
// animated=false to get a static glyph instead of a frozen braille frame.
func renderJobUpdateBlock(snap *service.JobSnapshot, width int, selected bool, spinnerFrame int, animated bool) string {
	if snap == nil {
		return ""
	}

	// Content width available inside border + padding.
	frameH := JobBlockStyle.GetHorizontalFrameSize()
	innerW := width - frameH
	if innerW < 4 {
		innerW = 4
	}

	glyph, statusWord, statusStyle, borderColor := jobStatusDecoration(snap)
	// Active/pending jobs show the braille spinner used for running workers —
	// but only where it actually animates (the sidebar); the chat keeps the
	// static glyph so it doesn't look like a frozen dot.
	if animated && (snap.Status == service.JobStatusActive || snap.Status == service.JobStatusPending) {
		glyph = string(spinnerChars[spinnerFrame%len(spinnerChars)])
	}

	// Line 1: "<glyph> <title>                              <status>"
	titlePrefix := glyph + " "
	// Reserve room for the right-aligned status word (with one space margin).
	statusRendered := statusStyle.Render(statusWord)
	statusW := lipgloss.Width(statusRendered)
	available := innerW - lipgloss.Width(titlePrefix) - statusW - 1
	if available < 1 {
		available = 1
	}
	title := truncateStr(snap.Title, available)
	titleRendered := JobBlockTitleStyle.Render(title)
	gap := innerW - lipgloss.Width(titlePrefix) - lipgloss.Width(titleRendered) - statusW
	if gap < 1 {
		gap = 1
	}
	line1 := titlePrefix + titleRendered + strings.Repeat(" ", gap) + statusRendered

	// Line 2: "<short-id> · N/M tasks" (+ failed count when non-zero).
	shortID := snap.JobID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	meta := shortID + " · " + fmt.Sprintf("%d/%d tasks", snap.TasksCompleted, snap.TasksTotal)
	if snap.TasksFailed > 0 {
		meta += " · " + fmt.Sprintf("%d failed", snap.TasksFailed)
	}
	line2 := JobBlockMetaStyle.Render(truncateStr(meta, innerW))

	body := line1 + "\n" + line2
	style := JobBlockStyle
	if selected {
		style = style.Border(lipgloss.ThickBorder())
	}
	// In lipgloss v2, Width() sets the total outer width (content + padding +
	// border), so we pass the full available width and let the style subtract
	// its own frame internally. Passing anything smaller produces a content
	// area narrower than the body lines we just built, which wraps them onto
	// extra rows.
	return style.
		BorderForeground(borderColor).
		Width(width).
		Render(body)
}

// jobStatusDecoration returns the status glyph, status word, status-text
// style, and border color for a job snapshot.
func jobStatusDecoration(snap *service.JobSnapshot) (glyph, statusWord string, statusStyle lipgloss.Style, border compat.AdaptiveColor) {
	switch snap.Status {
	case service.JobStatusCompleted:
		return "✓", "done", JobBlockStatusDoneStyle, JobBlockBorderDone
	case service.JobStatusFailed:
		return "✗", "failed", JobBlockStatusFailedStyle, JobBlockBorderFailed
	case service.JobStatusCancelled:
		return "—", "cancelled", JobBlockMetaStyle, JobBlockBorderCancelled
	case service.JobStatusPaused:
		return "⏸", "paused", JobBlockMetaStyle, JobBlockBorderPaused
	case service.JobStatusSettingUp:
		return "⚙", "setting up", JobBlockStatusActiveStyle, JobBlockBorderActive
	case service.JobStatusActive, service.JobStatusPending:
		if snap.TasksFailed > 0 {
			return "●", "running", JobBlockStatusBlockedStyle, JobBlockBorderBlocked
		}
		return "●", "running", JobBlockStatusActiveStyle, JobBlockBorderActive
	default:
		return "·", string(snap.Status), JobBlockMetaStyle, JobBlockBorderCancelled
	}
}

// jobSnapshotFromEventPayload synthesizes a minimal JobSnapshot from the
// event itself when live state isn't yet available. Only JobCreatedPayload
// carries enough to stand up a useful initial block (it's the one that
// races the progress-poll cycle). Other job-scoped events always arrive
// after JobCreated, by which point m.jobs is populated and the live path
// wins.
func jobSnapshotFromEventPayload(ev service.Event) *service.JobSnapshot {
	p, ok := ev.Payload.(service.JobCreatedPayload)
	if !ok {
		return nil
	}
	now := time.Now()
	return &service.JobSnapshot{
		JobID:     p.JobID,
		Title:     p.Title,
		Status:    service.JobStatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// jobIDFromEvent pulls the job_id field out of any of the job-scoped event
// payloads (Job*/Task*). Returns empty string if the event type isn't one
// of those or the payload type assertion fails.
func jobIDFromEvent(ev service.Event) string {
	switch p := ev.Payload.(type) {
	case service.JobCreatedPayload:
		return p.JobID
	case service.TaskCreatedPayload:
		return p.JobID
	case service.TaskAssignedPayload:
		return p.JobID
	case service.TaskStartedPayload:
		return p.JobID
	case service.TaskCompletedPayload:
		return p.JobID
	case service.TaskFailedPayload:
		return p.JobID
	case service.JobCompletedPayload:
		return p.JobID
	}
	return ""
}
