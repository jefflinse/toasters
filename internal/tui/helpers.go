// Helpers: text utilities, scrollbar rendering, toast overlay, session management, and chat entry operations.
package tui

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"

	"github.com/jefflinse/toasters/internal/service"
)

// ChatEntry is a package-level alias for service.ChatEntry so that files not
// yet migrated to the service layer can continue to use the unqualified name.
type ChatEntry = service.ChatEntry

// wrapText wraps s to fit within maxWidth columns.
func wrapText(s string, maxWidth int) string {
	if maxWidth <= 0 {
		maxWidth = 40
	}

	var result strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if lipgloss.Width(line) <= maxWidth {
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(line)
			continue
		}

		// Word-wrap long lines.
		words := strings.Fields(line)
		var currentLine strings.Builder
		for _, word := range words {
			wordW := lipgloss.Width(word)
			currentW := lipgloss.Width(currentLine.String())

			if currentW == 0 {
				// Break very long words.
				for wordW > maxWidth {
					if result.Len() > 0 || currentLine.Len() > 0 {
						result.WriteString("\n")
					}
					// Take as many runes as fit.
					runes := []rune(word)
					cut := maxWidth
					if cut > len(runes) {
						cut = len(runes)
					}
					result.WriteString(string(runes[:cut]))
					word = string(runes[cut:])
					wordW = lipgloss.Width(word)
				}
				currentLine.WriteString(word)
			} else if currentW+1+wordW <= maxWidth {
				currentLine.WriteString(" ")
				currentLine.WriteString(word)
			} else {
				if result.Len() > 0 {
					result.WriteString("\n")
				}
				result.WriteString(currentLine.String())
				currentLine.Reset()
				currentLine.WriteString(word)
			}
		}
		if currentLine.Len() > 0 {
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(currentLine.String())
		}
	}

	return result.String()
}

// truncateStr truncates s to maxLen runes, adding "..." if truncated.
// Note: truncation is rune-based, not display-width-based. Characters with
// display width > 1 (e.g. CJK, emoji) may still overflow the visual budget.
func truncateStr(s string, maxLen int) string {
	if maxLen <= 3 {
		maxLen = 3
	}
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen-3]) + "..."
}

// firstLineOf returns the first non-empty line of s.
func firstLineOf(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return s
}

// renderCompletionBlock renders the full content of a completion message as a
// dimmed indented block, skipping the first line (already shown in the header).
func renderCompletionBlock(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(DimStyle.Render("  "+line) + "\n")
	}
	return sb.String()
}

// isToolCallIndicatorIdx reports whether the entry at index i is a tool-call indicator.
func (m *Model) isToolCallIndicatorIdx(i int) bool {
	if i < 0 || i >= len(m.chat.entries) {
		return false
	}
	return m.chat.entries[i].ClaudeMeta == "tool-call-indicator"
}

// extractToolName extracts the tool/function name from a tool-call indicator
// message like "⚙ calling `function_name`…". Returns the name or a fallback.
func extractToolName(content string) string {
	// Look for backtick-delimited name.
	start := strings.Index(content, "`")
	if start >= 0 {
		end := strings.Index(content[start+1:], "`")
		if end >= 0 {
			return content[start+1 : start+1+end]
		}
	}
	return "tool call"
}

// formatToolName formats a tool name for display. MCP-namespaced tools
// (containing "__") are rendered as "tool_name (via server)" instead of
// "server__tool_name". Built-in tool names are returned unchanged.
func formatToolName(name string) string {
	if parts := strings.SplitN(name, "__", 2); len(parts) == 2 {
		return fmt.Sprintf("%s (via %s)", parts[1], parts[0])
	}
	return name
}

// formatToolCallContent transforms the content of a tool-call indicator message
// so that any MCP-namespaced tool name inside backticks is displayed in the
// "tool_name (via server)" format. Non-MCP content is returned unchanged.
func formatToolCallContent(content string) string {
	start := strings.Index(content, "`")
	if start < 0 {
		return content
	}
	end := strings.Index(content[start+1:], "`")
	if end < 0 {
		return content
	}
	name := content[start+1 : start+1+end]
	formatted := formatToolName(name)
	if formatted == name {
		return content
	}
	return content[:start+1] + formatted + content[start+1+end:]
}

// renderScrollbar builds a vertical scrollbar string of the given height.
// The thumb position is determined by scrollPercent (0.0–1.0). This is meant
// to be placed alongside the viewport content via lipgloss.JoinHorizontal.
func renderScrollbar(viewportHeight int, totalLines int, scrollPercent float64) string {
	// Calculate thumb size: proportional to visible/total, minimum 2 lines.
	thumbSize := viewportHeight * viewportHeight / totalLines
	if thumbSize < 2 {
		thumbSize = 2
	}
	if thumbSize > viewportHeight {
		thumbSize = viewportHeight
	}

	// Calculate thumb position.
	trackSpace := viewportHeight - thumbSize
	thumbStart := int(scrollPercent * float64(trackSpace))
	if thumbStart < 0 {
		thumbStart = 0
	}
	if thumbStart > trackSpace {
		thumbStart = trackSpace
	}
	thumbEnd := thumbStart + thumbSize

	thumbStyle := lipgloss.NewStyle().Foreground(ColorPrimary)
	trackStyle := lipgloss.NewStyle().Foreground(ColorBorder)

	lines := make([]string, viewportHeight)
	for i := range viewportHeight {
		if i >= thumbStart && i < thumbEnd {
			lines[i] = thumbStyle.Render("█")
		} else {
			lines[i] = trackStyle.Render("░")
		}
	}
	return strings.Join(lines, "\n")
}

// renderToasts renders the toast notification stack as a single string block.
// Newest toasts appear at the top. maxOuterWidth caps the total rendered
// width (border + padding + content); messages shorter than that produce
// proportionally narrower toasts so the bubble grows with its content
// instead of always padding out to a fixed 40-cell box.
func (m *Model) renderToasts(maxOuterWidth int) string {
	if len(m.toasts) == 0 || maxOuterWidth <= 0 {
		return ""
	}
	// Outer = border (2) + padding (2) + inner content. Reserve the chrome
	// before deciding how much room the message itself gets.
	const chrome = 4
	innerBudget := maxOuterWidth - chrome
	if innerBudget < 1 {
		innerBudget = 1
	}
	var lines []string
	for i := len(m.toasts) - 1; i >= 0; i-- {
		t := m.toasts[i]
		msg := truncateToWidth(t.message, innerBudget)
		// Override MaxWidth per-render so wider terminals can show the full
		// message; the base style's MaxWidth was a hard 40 which clipped
		// every toast at the same point regardless of available room.
		var rendered string
		switch t.level {
		case toastSuccess:
			rendered = ToastSuccessStyle.MaxWidth(maxOuterWidth).Render(msg)
		case toastWarning:
			rendered = ToastWarningStyle.MaxWidth(maxOuterWidth).Render(msg)
		default:
			rendered = ToastInfoStyle.MaxWidth(maxOuterWidth).Render(msg)
		}
		lines = append(lines, rendered)
	}
	return strings.Join(lines, "\n")
}

// truncateToWidth truncates s to fit within maxWidth display cells (using
// lipgloss.Width semantics: emoji = 2 cells, CJK = 2 cells, ANSI escapes = 0
// cells). If s already fits, it is returned unchanged. Otherwise the result
// is truncated and the trailing rune is replaced with "…", with the budget
// reserved for the ellipsis.
//
// Implementation note: this is a single O(n) pass over the runes of s,
// computing per-rune width via lipgloss.Width(string(r)) once. Previously this
// was an O(n²) loop that called lipgloss.Width on the whole string after
// removing one rune at a time, which made renderToasts catastrophically slow
// for any non-trivial message and could freeze the entire TUI by starving the
// Bubble Tea message loop. See investigation in cleanup/server-only.
func truncateToWidth(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	// Reserve 1 cell for the ellipsis itself ("…" is one cell).
	const ellipsis = "…"
	const ellipsisWidth = 1
	budget := maxWidth - ellipsisWidth
	if budget <= 0 {
		return ellipsis
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		// lipgloss.Width is the canonical authority on display width here so
		// our truncation matches what the renderer will measure.
		w := lipgloss.Width(string(r))
		if used+w > budget {
			break
		}
		b.WriteRune(r)
		used += w
	}
	b.WriteString(ellipsis)
	return b.String()
}

// overlayToasts splices the toast block so its right edge lands at column
// rightEdge (1-indexed cell count). The caller passes screenWidth so we can
// pad short lines when needed; any screen content past rightEdge is preserved
// so toasts don't paint over a sidebar.
func overlayToasts(screen string, toastBlock string, rightEdge, screenWidth int) string {
	if toastBlock == "" {
		return screen
	}
	if rightEdge > screenWidth {
		rightEdge = screenWidth
	}
	if rightEdge <= 0 {
		return screen
	}
	screenLines := strings.Split(screen, "\n")
	toastLines := strings.Split(toastBlock, "\n")

	for i, tl := range toastLines {
		if i >= len(screenLines) {
			break
		}
		toastW := lipgloss.Width(tl)

		// Toast wider than its budget — collapse to a full-line replacement
		// across the chat area so we don't crash into a sidebar.
		if toastW >= rightEdge {
			leftPart := takeVisible(screenLines[i], 0)
			_, rightPart := splitVisibleAt(screenLines[i], rightEdge)
			screenLines[i] = leftPart + tl + rightPart
			continue
		}

		// Pad the screen line out to screenWidth so we have material to
		// preserve to the right of the toast.
		screenLineW := lipgloss.Width(screenLines[i])
		if screenLineW < screenWidth {
			screenLines[i] = screenLines[i] + strings.Repeat(" ", screenWidth-screenLineW)
		}

		leftBudget := rightEdge - toastW
		leftPart := takeVisible(screenLines[i], leftBudget)
		_, rightPart := splitVisibleAt(screenLines[i], rightEdge)
		screenLines[i] = leftPart + tl + rightPart
	}
	return strings.Join(screenLines, "\n")
}

// takeVisible returns the prefix of line that occupies n visible cells,
// preserving ANSI escape sequences. If line has fewer than n visible cells
// the result is padded with spaces.
func takeVisible(line string, n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	visible := 0
	inEsc := false
	for _, r := range line {
		if r == '\x1b' {
			inEsc = true
			b.WriteRune(r)
			continue
		}
		if inEsc {
			b.WriteRune(r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
			continue
		}
		if visible >= n {
			break
		}
		b.WriteRune(r)
		visible++
	}
	for visible < n {
		b.WriteRune(' ')
		visible++
	}
	return b.String()
}

// splitVisibleAt splits line at the n-th visible cell. The left half holds
// the first n visible cells (no padding); the right half holds everything
// after, including any ANSI escape sequences in flight at the cut point so
// the right half renders with the same styling that was active.
func splitVisibleAt(line string, n int) (string, string) {
	if n <= 0 {
		return "", line
	}
	var left, right strings.Builder
	visible := 0
	inEsc := false
	cut := false
	for _, r := range line {
		if r == '\x1b' {
			inEsc = true
			if cut {
				right.WriteRune(r)
			} else {
				left.WriteRune(r)
			}
			continue
		}
		if inEsc {
			if cut {
				right.WriteRune(r)
			} else {
				left.WriteRune(r)
			}
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
			continue
		}
		if visible >= n {
			cut = true
		}
		if cut {
			right.WriteRune(r)
		} else {
			left.WriteRune(r)
			visible++
		}
	}
	return left.String(), right.String()
}

// appendHelpMessage adds a help message to the chat as an assistant turn.
func (m *Model) appendHelpMessage() {
	helpText := "**Toasters — Help**\n\n" +
		"**Slash Commands**\n" +
		"- `/help` — Show this help message\n" +
		"- `/new` — Start a new session (clears chat history)\n" +
		"- `/exit`, `/quit` — Exit the application\n\n" +
		"**Keyboard Shortcuts**\n" +
		"- `Enter` — Send message\n" +
		"- `Shift+Enter` — New line in message\n" +
		"- `Esc` — Cancel current response\n" +
		"- `Ctrl+C` — Quit\n\n" +
		"**Slash Command Autocomplete**\n" +
		"Type `/` to open the command picker. Use ↑↓ to navigate, Tab or Enter to select, Esc to dismiss."

	m.appendEntry(service.ChatEntry{
		Message:   service.ChatMessage{Role: service.MessageRoleAssistant, Content: helpText},
		Timestamp: time.Now(),
	})
	m.stats.MessageCount++
	m.updateViewportContent()
	if !m.scroll.userScrolled {
		m.chatViewport.GotoBottom()
	}
}

// newSession resets the conversation and all session statistics.
// initMessages resets m.chat.entries and seeds it with the system prompt as entries[0]
// (if a system prompt is set). Call this at startup and on /new.
func (m *Model) initMessages() {
	m.chat.entries = nil
	if m.systemPrompt != "" {
		m.appendEntry(service.ChatEntry{
			Message:   service.ChatMessage{Role: service.MessageRoleSystem, Content: m.systemPrompt},
			Timestamp: time.Now(),
		})
		m.stats.SystemPromptTokens = estimateTokens(m.systemPrompt)
	} else {
		m.stats.SystemPromptTokens = 0
	}
	m.chat.completionMsgIdx = make(map[int]bool)
	m.chat.expandedMsgs = make(map[int]bool)
	m.chat.selectedMsgIdx = -1
	m.chat.expandedReasoning = make(map[int]bool)
	m.chat.collapsedTools = make(map[int]bool)
	m.prompt.confirmDispatch = false
	m.prompt.changingTeam = false
	m.prompt.pendingDispatch = service.ToolCall{}
}

// appendEntry adds a new chat entry to the conversation history.
func (m *Model) appendEntry(e service.ChatEntry) {
	m.chat.entries = append(m.chat.entries, e)
}

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
	tasks := m.progress.tasks[jobID]
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
// spinnerFrame animates the glyph for active/pending jobs. Pass
// m.spinnerFrame from callers rendered on every tick (sidebar). Callers
// whose output is cached longer (e.g. chat viewport) can pass any value —
// they'll just see a fixed frame until the viewport is rebuilt.
func renderJobUpdateBlock(snap *service.JobSnapshot, width int, selected bool, spinnerFrame int) string {
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
	// Active/pending jobs show the same braille spinner used for running
	// workers in the Workers pane rather than a static dot.
	if snap.Status == service.JobStatusActive || snap.Status == service.JobStatusPending {
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

// selectableEntryIndices returns the indices of chat entries that
// participate in Up/Down selection — currently job results and worker
// stream blocks. Both are deep-link targets into the Jobs modal, so
// they share one selection cursor and the same Enter behavior.
func (m *Model) selectableEntryIndices() []int {
	var out []int
	for i, e := range m.chat.entries {
		switch e.Kind {
		case service.ChatEntryKindJobResult:
			if e.JobResult != nil {
				out = append(out, i)
			}
		case service.ChatEntryKindWorkerStream:
			if e.WorkerStream != nil {
				out = append(out, i)
			}
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

// stepBlockSelection moves the chat selection one step (delta = -1 for
// previous, +1 for next) through the selectable entries (job results
// and worker stream blocks). Returns true when the selection changed;
// false (and leaves state untouched) when there's nothing to select or
// the move would walk off the end. Selection wraps off the start to
// "no selection" so the user can return to free typing.
func (m *Model) stepBlockSelection(delta int) bool {
	indices := m.selectableEntryIndices()
	if len(indices) == 0 {
		return false
	}
	cur := -1
	for i, idx := range indices {
		if idx == m.chat.selectedMsgIdx {
			cur = i
			break
		}
	}
	switch {
	case cur < 0 && delta < 0:
		// Entering selection mode from "no selection" via Up — land on
		// the most recent result, which is what the user intuits.
		m.chat.selectedMsgIdx = indices[len(indices)-1]
		return true
	case cur < 0 && delta > 0:
		// Down with no current selection is a no-op (nothing below the
		// input area to walk into).
		return false
	}
	next := cur + delta
	if next < 0 {
		// Stepping past the oldest result clears selection — user is
		// back at "free chat", not stuck cycling.
		m.chat.selectedMsgIdx = -1
		return true
	}
	if next >= len(indices) {
		// Stepping past the newest also clears selection so Down feels
		// symmetric with Up.
		m.chat.selectedMsgIdx = -1
		return true
	}
	m.chat.selectedMsgIdx = indices[next]
	return true
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

// truncateLeft trims the leading portion of s so that the result fits
// within maxWidth display cells, prefixing "…" when content was dropped.
// Used for paths where the meaningful tail (the last directory + files)
// is what the user wants to see.
func truncateLeft(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	const ellipsis = "…"
	budget := maxWidth - 1
	if budget <= 0 {
		return ellipsis
	}
	runes := []rune(s)
	// Walk from the right, accumulating until we'd exceed budget.
	used := 0
	cut := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		w := lipgloss.Width(string(runes[i]))
		if used+w > budget {
			break
		}
		used += w
		cut = i
	}
	return ellipsis + string(runes[cut:])
}

// contractHomeDir replaces a leading $HOME with "~" so paths read more
// naturally in the UI. Falls back to the original path when home isn't
// resolvable or doesn't prefix the input.
func contractHomeDir(path string) string {
	if path == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// wrapToWidth breaks s into lines no wider than width display cells,
// honoring word boundaries. Caps the result at maxLines to keep the
// failure section from overrunning the block.
func wrapToWidth(s string, width, maxLines int) []string {
	if width <= 0 || maxLines <= 0 {
		return nil
	}
	words := strings.Fields(s)
	var lines []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			lines = append(lines, cur.String())
			cur.Reset()
		}
	}
	for _, w := range words {
		if len(lines) >= maxLines {
			break
		}
		switch {
		case cur.Len() == 0:
			cur.WriteString(w)
		case lipgloss.Width(cur.String())+1+lipgloss.Width(w) > width:
			flush()
			cur.WriteString(w)
		default:
			cur.WriteByte(' ')
			cur.WriteString(w)
		}
	}
	flush()
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return lines
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

// isDisplayOnly reports whether an entry is UI-only chrome that must never be
// sent to the LLM API. Categories:
//
//  1. Pure confirmation/prompt assistant messages (dispatch-confirm, kill-confirm,
//     ask-user-prompt, escalate-prompt) — these are text-only assistant messages
//     injected for the user's benefit; they have no ToolCalls and no matching
//     tool_result, so sending them would confuse the API.
//
//  2. Visual tool-call indicator messages — entries with ClaudeMeta "tool-call-indicator"
//     that have no ToolCalls set (i.e. the "⚙ calling foo…" text lines). Entries
//     with ToolCalls set ARE real tool_use records and must be kept.
//
//  3. Structured entries (Kind != ChatEntryKindMessage) such as job-update
//     blocks — they render from typed payloads and have no text content the
//     model should see.
func isDisplayOnly(e service.ChatEntry) bool {
	if e.Kind != service.ChatEntryKindMessage {
		return true
	}
	switch e.ClaudeMeta {
	case "ask-user-prompt", "dispatch-confirm", "kill-confirm", "escalate-prompt", "feed-event":
		return true
	case "tool-call-indicator":
		// Keep entries that carry actual tool calls; drop text-only indicators.
		return len(e.Message.ToolCalls) == 0
	}
	return false
}

// messagesFromEntries extracts the service.ChatMessage slice from entries.
// Display-only entries (visual indicators, confirmation prompts) are filtered out.
func (m *Model) messagesFromEntries() []service.ChatMessage {
	msgs := make([]service.ChatMessage, 0, len(m.chat.entries))
	for _, e := range m.chat.entries {
		if isDisplayOnly(e) {
			continue
		}
		msgs = append(msgs, e.Message)
	}
	return msgs
}

// hasConversation reports whether the conversation contains at least one user
// message (i.e. the welcome art should be hidden). Assistant-only messages
// (e.g. the startup greeting) are shown alongside the art.
func (m *Model) hasConversation() bool {
	for _, entry := range m.chat.entries {
		if entry.Message.Role == service.MessageRoleUser {
			return true
		}
	}
	return false
}

// setFocus changes the focused panel and arms the spinner tick if moving to
// a panel whose title should animate (rainbow-cycle while focused). The
// ticker is single-armed — a second spinnerTick while one is live would
// double-increment spinnerFrame and run the animation at 2×+ speed.
func (m *Model) setFocus(p focusedPanel) tea.Cmd {
	if p == m.focused {
		return nil
	}
	m.focused = p
	if (p == focusJobs || p == focusAgents) && !m.spinnerRunning {
		m.spinnerRunning = true
		return spinnerTick()
	}
	return nil
}

func (m *Model) newSession() {
	m.initMessages()
	// entries is already reset by initMessages.
	m.stream.operatorByline = ""
	m.stream.currentResponse = ""
	m.stream.currentReasoning = ""
	m.stats.MessageCount = 0
	m.stats.PromptTokens = 0
	m.stats.CompletionTokens = 0
	m.stats.ReasoningTokens = 0
	m.stats.TotalResponses = 0
	m.stats.TotalResponseTime = 0
	m.stats.LastResponseTime = 0
	m.err = nil
	m.scroll.userScrolled = false
	m.updateViewportContent()
	m.chatViewport.GotoBottom()
	m.input.Focus()
}

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

// displayRuntimeSessions returns the runtime sessions filtered for display
// in the Workers pane: every active session, plus at most
// maxCompletedWorkersInPane most-recently-ended non-active sessions.
// Ordering matches sortedRuntimeSessions (active first by start time, then
// terminal sessions by start time), so rendering code doesn't need to care.
func (m *Model) displayRuntimeSessions() []*runtimeSlot {
	all := m.sortedRuntimeSessions()

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

	sortedRT := m.sortedRuntimeSessions()

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
