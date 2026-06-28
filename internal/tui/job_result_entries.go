package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/jefflinse/toasters/internal/service"
)

// jobResultHintLine returns the dim "↑ to select for actions" affordance
// hint shown beneath the most recent unread result block. Returns "" when
// the hint shouldn't render (block is selected, snapshot isn't the latest,
// or another result has displaced this one).
func (m *Model) jobResultHintLine(snap *service.JobResultSnapshot, selected bool) string {
	if selected || snap == nil || m.recentJobResult == nil {
		return ""
	}
	if snap.JobID != m.recentJobResult.JobID {
		return ""
	}
	return DimStyle.Italic(true).Render("  ↑ to select for actions")
}

// jobResultEntryIndices returns the chat-history indices of JobResult
// entries in their natural (chronological) order. Used by chat-selection
// navigation: pressing Up while the chat is focused walks backward through
// these indices, surfacing "actionable" entries to the user without the
// noise of every assistant turn becoming a selection target.
func (m *Model) jobResultEntryIndices() []int {
	var out []int
	for i, e := range m.chat.entries {
		if e.Kind == service.ChatEntryKindJobResult && e.JobResult != nil {
			out = append(out, i)
		}
	}
	return out
}

// selectedJobResult returns the snapshot for the currently selected chat
// entry when that entry is a JobResult, or nil otherwise. Centralizing
// the lookup means callers (key handlers, footer renderer, etc.) don't
// have to repeat the bounds + kind checks.
func (m *Model) selectedJobResult() *service.JobResultSnapshot {
	idx := m.chat.selectedMsgIdx
	if idx < 0 || idx >= len(m.chat.entries) {
		return nil
	}
	e := m.chat.entries[idx]
	if e.Kind != service.ChatEntryKindJobResult {
		return nil
	}
	return e.JobResult
}

// appendJobResultEntry materializes a JobResultSnapshot from a
// JobCompleted event payload and appends it as a new chat entry. Returns
// a tea.Cmd for any side effects the caller should batch (today: a toast
// confirming completion). Returns nil when the event payload is malformed
// — the upsert path still updates the in-progress block, so silent fall-
// through here is safe.
func (m *Model) appendJobResultEntry(ev service.Event) tea.Cmd {
	p, ok := ev.Payload.(service.JobCompletedPayload)
	if !ok {
		return nil
	}
	snap := &service.JobResultSnapshot{
		JobID:             p.JobID,
		Title:             p.Title,
		Summary:           p.Summary,
		Status:            p.Status,
		Workspace:         p.Workspace,
		StartedAt:         p.StartedAt,
		EndedAt:           p.EndedAt,
		TasksTotal:        p.TasksTotal,
		TasksCompleted:    p.TasksCompleted,
		TasksFailed:       p.TasksFailed,
		TokensIn:          p.TokensIn,
		TokensOut:         p.TokensOut,
		CostUSD:           p.CostUSD,
		FilesTouched:      p.FilesTouched,
		FilesTouchedExtra: p.FilesTouchedExtra,
	}
	if snap.Status == "" {
		snap.Status = service.JobStatusCompleted
	}
	if snap.EndedAt.IsZero() {
		snap.EndedAt = time.Now()
	}
	m.appendEntry(service.ChatEntry{
		Kind:      service.ChatEntryKindJobResult,
		Timestamp: snap.EndedAt,
		JobResult: snap,
	})
	// The most-recent completion seeds the "↑ to select for actions"
	// hint that renders beneath the latest unread result block.
	m.recentJobResult = snap
	return nil
}

// renderJobResultBlock draws the terminal completion summary for a job —
// the "result block" — in the same border/padding language as the
// in-progress JobUpdate block, color-shifted by terminal status. Layout:
//
//	╭─────────────────────────────────────────────────╮
//	│ ✓ <title>                       done · 4m12s    │
//	│ ~/path/to/workspace                             │
//	│ ─── 8 files: 6 added · 2 modified ───────────── │
//	│  + first.go                                     │
//	│  + second.go                                    │
//	│  + 6 more                                       │
//	│ 8.2k in · 2.1k out · ~$0.04 · finished 23:34    │
//	│ [w] workspace  [d] details  [Enter] open in Jobs│
//	╰─────────────────────────────────────────────────╯
//
// width is the total outer width including border + padding (matches
// renderJobUpdateBlock's contract). selected swaps the border style to
// thick so the user can see when the block has chat-selection focus and
// the action keys are live.
func renderJobResultBlock(res *service.JobResultSnapshot, width int, selected bool) string {
	if res == nil {
		return ""
	}

	frameH := JobBlockStyle.GetHorizontalFrameSize()
	innerW := width - frameH
	if innerW < 4 {
		innerW = 4
	}

	glyph, statusWord, statusStyle, borderColor := jobResultDecoration(res)

	// --- Line 1: glyph + title + right-aligned "<status> · <duration>" ---
	durStr := formatJobDuration(res.StartedAt, res.EndedAt)
	rightStr := statusStyle.Render(statusWord)
	if durStr != "" {
		rightStr = rightStr + DimStyle.Render(" · "+durStr)
	}
	rightW := lipgloss.Width(rightStr)
	prefix := glyph + " "
	titleBudget := innerW - lipgloss.Width(prefix) - rightW - 1
	if titleBudget < 1 {
		titleBudget = 1
	}
	title := truncateStr(res.Title, titleBudget)
	titleRendered := JobBlockTitleStyle.Render(title)
	gap := innerW - lipgloss.Width(prefix) - lipgloss.Width(titleRendered) - rightW
	if gap < 1 {
		gap = 1
	}
	line1 := prefix + titleRendered + strings.Repeat(" ", gap) + rightStr

	// --- Line 2: workspace path (left-ellipsized to fit) ---
	workspaceLine := DimStyle.Render(truncateLeft(contractHomeDir(res.Workspace), innerW))

	lines := []string{line1, workspaceLine}

	// --- Optional: failure reason for failed jobs ---
	// Failed jobs emphasize the reason over file artifacts (which are
	// likely incomplete or misleading anyway). When res.Summary carries a
	// non-empty body for a failure, surface it in the prime spot.
	if res.Status == service.JobStatusFailed && strings.TrimSpace(res.Summary) != "" {
		lines = append(lines, sectionDivider("failure", innerW))
		for _, l := range wrapToWidth(strings.TrimSpace(res.Summary), innerW-2, 2) {
			lines = append(lines, "  "+l)
		}
	}

	// --- Optional: files-touched mini-section ---
	if len(res.FilesTouched) > 0 {
		header := summarizeFiles(res.FilesTouched, res.FilesTouchedExtra)
		lines = append(lines, sectionDivider(header, innerW))
		// Show up to 3 files inline, then a "+ N more" tail. 3 is enough
		// to convey breadth without making the block dominate chat.
		const inlineLimit = 3
		shown := res.FilesTouched
		if len(shown) > inlineLimit {
			shown = shown[:inlineLimit]
		}
		for _, f := range shown {
			lines = append(lines, JobBlockMetaStyle.Render(" + "+truncateStr(f.Path, innerW-3)))
		}
		extra := res.FilesTouchedExtra + len(res.FilesTouched) - len(shown)
		if extra > 0 {
			lines = append(lines, JobBlockMetaStyle.Render(fmt.Sprintf(" + %d more", extra)))
		}
	}

	// --- Cost / token line ---
	if costLine := buildCostLine(res); costLine != "" {
		lines = append(lines, JobBlockMetaStyle.Render(truncateStr(costLine, innerW)))
	}

	// --- Action hints (dim) ---
	hints := buildJobResultHints(res, selected)
	if hints != "" {
		lines = append(lines, hints)
	}

	body := strings.Join(lines, "\n")
	style := JobBlockStyle
	if selected {
		style = style.Border(lipgloss.ThickBorder())
	}
	return style.
		BorderForeground(borderColor).
		Width(width).
		Render(body)
}

// jobResultDecoration mirrors jobStatusDecoration for the small set of
// terminal statuses a JobResultSnapshot can hold. Always returns a "done"
// or "failed" decoration — cancelled/paused/setting-up don't fire result
// blocks today, but cancellation is included for completeness.
func jobResultDecoration(res *service.JobResultSnapshot) (glyph, statusWord string, statusStyle lipgloss.Style, border compat.AdaptiveColor) {
	switch res.Status {
	case service.JobStatusFailed:
		return "✗", "failed", JobBlockStatusFailedStyle, JobBlockBorderFailed
	case service.JobStatusCancelled:
		return "—", "cancelled", JobBlockMetaStyle, JobBlockBorderCancelled
	default:
		// Treat any non-failed, non-cancelled terminal state as a clean
		// completion. EventJobComplete only fires once every task has
		// reached terminal state, so this branch covers the success path.
		return "✓", "done", JobBlockStatusDoneStyle, JobBlockBorderDone
	}
}

// formatJobDuration returns a compact "Hh Mm Ss" / "Mm Ss" / "Ss" string
// for the run length, dropping zero-valued leading units. Empty when
// either timestamp is missing — the header just shows the status word in
// that case.
func formatJobDuration(start, end time.Time) string {
	if start.IsZero() || end.IsZero() {
		return ""
	}
	d := end.Sub(start)
	if d < 0 {
		d = 0
	}
	if d >= time.Hour {
		h := int(d / time.Hour)
		m := int((d % time.Hour) / time.Minute)
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	if d >= time.Minute {
		m := int(d / time.Minute)
		s := int((d % time.Minute) / time.Second)
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}

// summarizeFiles produces the section-header label, e.g.
// "8 files: 6 added · 2 modified", from a list of FileTouch entries.
func summarizeFiles(files []service.FileTouch, extra int) string {
	total := len(files) + extra
	added, modified := 0, 0
	for _, f := range files {
		if f.IsNew {
			added++
		} else {
			modified++
		}
	}
	// Add capped/extra entries to whichever bucket is appropriate. We
	// don't know whether suppressed entries were add vs modify, so they
	// inflate the total only.
	noun := "file"
	if total != 1 {
		noun = "files"
	}
	if added > 0 && modified > 0 {
		return fmt.Sprintf("%d %s: %d added · %d modified", total, noun, added, modified)
	}
	if added > 0 {
		return fmt.Sprintf("%d %s added", total, noun)
	}
	if modified > 0 {
		return fmt.Sprintf("%d %s modified", total, noun)
	}
	return fmt.Sprintf("%d %s touched", total, noun)
}

// sectionDivider draws a `─── label ───────────` line with the label
// inset. Used to introduce sub-sections inside the result block. Falls
// back to a plain rule when label is empty or the block is too narrow.
func sectionDivider(label string, innerW int) string {
	if label == "" || innerW < 6 {
		return DimStyle.Render(strings.Repeat("─", innerW))
	}
	leftRule := "─── " + label + " "
	if lipgloss.Width(leftRule) >= innerW {
		// Label too wide; just render label dimmed without trailing rule.
		return DimStyle.Render(truncateStr(label, innerW))
	}
	rightRule := strings.Repeat("─", innerW-lipgloss.Width(leftRule))
	return DimStyle.Render(leftRule + rightRule)
}

// buildCostLine assembles the bottom meta line: token counts, optional
// cost, and finish-time stamp. Returns empty when nothing meaningful is
// set (older jobs without session aggregation, etc.).
func buildCostLine(res *service.JobResultSnapshot) string {
	var parts []string
	if res.TokensIn > 0 || res.TokensOut > 0 {
		parts = append(parts, fmt.Sprintf("%s in", formatTokenCount(res.TokensIn)))
		parts = append(parts, fmt.Sprintf("%s out", formatTokenCount(res.TokensOut)))
	}
	if res.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("~$%.2f", res.CostUSD))
	}
	if !res.EndedAt.IsZero() {
		parts = append(parts, "finished "+res.EndedAt.Format("15:04"))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

// buildJobResultHints renders the action-hint footer line. When the block
// is selected, hints become opaque + readable; when unselected they're
// dim, advertising the existence of actions without committing visual
// real estate. [Enter] leads because "see what happened" is the more
// common follow-up than "open the directory in Finder".
func buildJobResultHints(res *service.JobResultSnapshot, selected bool) string {
	hints := []string{"[Enter] details"}
	if res.Workspace != "" {
		hints = append(hints, "[w] workspace")
	}
	line := strings.Join(hints, "  ")
	if selected {
		// Brighten selected so the user knows the keys are armed.
		return JobBlockTitleStyle.Foreground(ColorAccent).Render(line)
	}
	return DimStyle.Render(line)
}
