// Worker stream chat blocks: live, in-chat blocks that interleave a
// worker's streamed output (text + tool calls) into the operator
// conversation. Open blocks accept additional events as long as they
// stay the most recent chat entry, no other event has superseded them,
// and 60s haven't passed since the last update. Outside that window,
// the next streamed token starts a fresh block.
package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// workerStreamDisplayOrder returns a permutation of entry indices that, within
// each contiguous run of worker-stream cards, lists finished cards before
// still-running ones (preserving each group's relative order). Non-worker
// entries keep their position. This sinks active cards to the bottom of a run
// so the work in progress stays the most visible, without mutating the
// underlying transcript order.
func workerStreamDisplayOrder(entries []service.ChatEntry) []int {
	isCard := func(e service.ChatEntry) bool {
		return e.Kind == service.ChatEntryKindWorkerStream && e.WorkerStream != nil
	}
	order := make([]int, 0, len(entries))
	for i := 0; i < len(entries); {
		if !isCard(entries[i]) {
			order = append(order, i)
			i++
			continue
		}
		var done, active []int
		for ; i < len(entries) && isCard(entries[i]); i++ {
			if entries[i].WorkerStream.Done {
				done = append(done, i)
			} else {
				active = append(active, i)
			}
		}
		order = append(order, done...)
		order = append(order, active...)
	}
	return order
}

// fanoutGroupKey returns a grouping key for a worker-stream entry that is a
// fan-out branch or judge (e.g. "implement#0", "implement.judge"), keyed by
// task + parent node so siblings of one fan-out collapse together. ok is false
// for ordinary (non-fan-out) nodes, which never roll up.
func fanoutGroupKey(e service.ChatEntry) (string, bool) {
	if e.Kind != service.ChatEntryKindWorkerStream || e.WorkerStream == nil {
		return "", false
	}
	node := strings.TrimPrefix(e.WorkerStream.WorkerName, "graph:")
	parent, ok := branchParent(node)
	if !ok {
		return "", false
	}
	return e.WorkerStream.TaskID + "\x00" + parent, true
}

// renderTaskSummary collapses all of a completed task's worker-stream cards into
// a single "✓ <task>" line, so a finished task reads as one entry instead of a
// stack of per-node cards. Purely visual: selecting the task (via arrow nav)
// expands it back to its cards / fan-out groups; full detail stays in the grid
// and Jobs modal.
func (m *Model) renderTaskSummary(members []int, width int) string {
	if len(members) == 0 || width < 8 {
		return ""
	}
	first := m.chat.entries[members[0]].WorkerStream
	title := m.taskTitleByID(first.JobID, first.TaskID)
	if title == "" {
		title = strings.TrimPrefix(first.WorkerName, "graph:")
	}

	start, end := first.StartedAt, first.LastActivity
	for _, mi := range members {
		ws := m.chat.entries[mi].WorkerStream
		if ws.StartedAt.Before(start) {
			start = ws.StartedAt
		}
		if ws.LastActivity.After(end) {
			end = ws.LastActivity
		}
	}

	innerW := width - 4
	if innerW < 8 {
		innerW = 8
	}
	avail := innerW / 2
	if avail < 12 {
		avail = 12
	}
	steps := fmt.Sprintf("%d steps", len(members))
	if len(members) == 1 {
		steps = "1 step"
	}
	head := lipgloss.NewStyle().Bold(true).Foreground(ColorConnected).Render("✓ " + truncateStr(title, avail))
	meta := DimStyle.Render(fmt.Sprintf("  ·  %s  ·  %s", steps, formatStreamDuration(start, end)))

	frame := lipgloss.NewStyle().
		Border(lipgloss.Border{Left: "▌"}, false, false, false, true).
		BorderForeground(ColorBorder).
		PaddingLeft(1).
		Width(width - 2)
	return frame.Render(head + meta)
}

// renderFanoutGroupSummary collapses a completed fan-out group (all its branch
// and judge cards) into a single dim summary line, so a finished parallel phase
// reads as one entry instead of a stack of header rows. Purely visual: the
// per-branch detail is still reachable by selecting the group (which expands it)
// or via the grid / Jobs modal.
func (m *Model) renderFanoutGroupSummary(members []int, width int) string {
	if len(members) == 0 || width < 8 {
		return ""
	}
	first := m.chat.entries[members[0]].WorkerStream
	parent, _ := branchParent(strings.TrimPrefix(first.WorkerName, "graph:"))

	branches, hasJudge := 0, false
	start, end := first.StartedAt, first.LastActivity
	for _, mi := range members {
		ws := m.chat.entries[mi].WorkerStream
		n := strings.TrimPrefix(ws.WorkerName, "graph:")
		if strings.Contains(n, "#") {
			branches++
		}
		if strings.HasSuffix(n, ".judge") {
			hasJudge = true
		}
		if ws.StartedAt.Before(start) {
			start = ws.StartedAt
		}
		if ws.LastActivity.After(end) {
			end = ws.LastActivity
		}
	}

	headline := m.taskTitleByID(first.JobID, first.TaskID)
	if headline == "" {
		headline = parent
	}

	innerW := width - 4
	if innerW < 8 {
		innerW = 8
	}
	avail := innerW / 2
	if avail < 12 {
		avail = 12
	}

	countStr := fmt.Sprintf("%d branches", branches)
	if branches == 1 {
		countStr = "1 branch"
	}
	if hasJudge {
		countStr += " + judge"
	}
	head := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render("📦 " + truncateStr(headline, avail))
	meta := DimStyle.Render(fmt.Sprintf(" · %s · %s · ", parent, countStr))
	status := lipgloss.NewStyle().Foreground(ColorConnected).Render("✓ " + formatStreamDuration(start, end))

	frame := lipgloss.NewStyle().
		Border(lipgloss.Border{Left: "▌"}, false, false, false, true).
		BorderForeground(ColorBorder).
		PaddingLeft(1).
		Width(width - 2)
	return frame.Render(head + meta + status)
}

// taskTitleByID returns the title of a task within a job, or "" if not found.
func (m *Model) taskTitleByID(jobID, taskID string) string {
	for _, t := range m.progress.tasks[jobID] {
		if t.ID == taskID {
			return t.Title
		}
	}
	return ""
}

// formatStreamDuration renders the elapsed time between start and end compactly
// (e.g. "12s", "3m4s"). Empty when start is unset.
func formatStreamDuration(start, end time.Time) string {
	if start.IsZero() {
		return ""
	}
	d := end.Sub(start)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}

// slotHidden reports whether a session's activity should be kept out of the
// chat feed — true for internal decomposition steps unless --debug is set.
func (m *Model) slotHidden(slot *runtimeSlot) bool {
	return slot != nil && slot.system && !m.debug
}

// workerStreamChatMaxLines caps how many rendered lines a chat-side
// worker stream block displays. Older lines roll off so the block
// reads as a rolling tail of recent output — items themselves aren't
// trimmed (long prose should remain a single coherent run, just
// tail-truncated visually). The Jobs modal still has the full
// transcript via the runtimeSlot, which is what Enter-to-deep-link
// surfaces.
const workerStreamChatMaxLines = 6

// findWorkerStream returns the card for a session, wherever it sits in the
// chat. Each session has exactly one card that stays put and updates in place,
// so concurrent fan-out branches don't reorder or spawn duplicate boxes as they
// interleave. Scans newest-first since active cards cluster near the bottom.
func (m *Model) findWorkerStream(sessionID string) *service.WorkerStreamSnapshot {
	if sessionID == "" {
		return nil
	}
	for i := len(m.chat.entries) - 1; i >= 0; i-- {
		e := &m.chat.entries[i]
		if e.Kind == service.ChatEntryKindWorkerStream && e.WorkerStream != nil &&
			e.WorkerStream.SessionID == sessionID {
			return e.WorkerStream
		}
	}
	return nil
}

// ensureWorkerStream returns the session's stable card, creating it (appended at
// the current bottom, i.e. first-activity order) if it doesn't exist yet.
func (m *Model) ensureWorkerStream(slot *runtimeSlot) *service.WorkerStreamSnapshot {
	if snap := m.findWorkerStream(slot.sessionID); snap != nil {
		return snap
	}
	return m.newWorkerStreamEntry(slot)
}

// newWorkerStreamEntry appends a fresh worker stream block to the chat
// and returns the snapshot for the caller to populate. Called when no
// open block matches.
func (m *Model) newWorkerStreamEntry(slot *runtimeSlot) *service.WorkerStreamSnapshot {
	snap := &service.WorkerStreamSnapshot{
		WorkerName:   slot.workerName,
		JobID:        slot.jobID,
		TaskID:       slot.taskID,
		SessionID:    slot.sessionID,
		StartedAt:    time.Now(),
		LastActivity: time.Now(),
	}
	m.appendEntry(service.ChatEntry{
		Kind:         service.ChatEntryKindWorkerStream,
		Timestamp:    snap.StartedAt,
		WorkerStream: snap,
	})
	return snap
}

// appendWorkerStreamText records streamed text from a worker session
// into the matching open block, or starts a new block if none is open.
// Coalesces with the most recent text item so a long response is one
// run instead of hundreds of one-token fragments.
func (m *Model) appendWorkerStreamText(slot *runtimeSlot, text string) {
	if slot == nil || text == "" || m.slotHidden(slot) {
		return
	}
	snap := m.ensureWorkerStream(slot)
	snap.LastActivity = time.Now()
	if n := len(snap.Items); n > 0 && snap.Items[n-1].Kind == service.WorkerStreamItemText {
		snap.Items[n-1].Text += text
		return
	}
	snap.Items = append(snap.Items, service.WorkerStreamItem{
		Kind: service.WorkerStreamItemText,
		Text: text,
	})
}

// appendWorkerStreamToolCall records a new in-flight tool call into
// the matching open block (or a fresh one). Each call is one item;
// the matching result patches it in place (see appendWorkerStreamToolResult).
func (m *Model) appendWorkerStreamToolCall(slot *runtimeSlot, callID, toolName string, args json.RawMessage) {
	if slot == nil || toolName == "" || m.slotHidden(slot) {
		return
	}
	snap := m.ensureWorkerStream(slot)
	snap.LastActivity = time.Now()
	snap.Items = append(snap.Items, service.WorkerStreamItem{
		Kind:      service.WorkerStreamItemTool,
		ToolID:    callID,
		ToolName:  toolName,
		ToolArgs:  args,
		StartedAt: time.Now(),
	})
}

// appendWorkerStreamToolResult patches the matching tool call item
// with its result. Walks items newest-first so the most recent call
// for the same ID gets the result, even if duplicate IDs ever occur
// across blocks. Falls back to a synthesized completed item when no
// matching call is found in the open block — keeps results visible
// even if events arrive out of order.
func (m *Model) appendWorkerStreamToolResult(slot *runtimeSlot, callID, toolName, result string, isError bool) {
	if slot == nil || m.slotHidden(slot) {
		return
	}
	snap := m.ensureWorkerStream(slot)
	snap.LastActivity = time.Now()
	// Patch the matching call. Prefer an exact ToolID match; if the result has
	// no call ID (or none match), fall back to the most recent still-pending
	// tool call of the same name. This keeps the call and its result a single
	// merged item instead of rendering the call ("running…") and the result
	// ("✓ ok") as two separate blocks.
	patch := func(it *service.WorkerStreamItem) {
		it.ToolResult = result
		it.ToolError = isError
		it.EndedAt = time.Now()
	}
	if callID != "" {
		for i := len(snap.Items) - 1; i >= 0; i-- {
			if it := &snap.Items[i]; it.Kind == service.WorkerStreamItemTool && it.ToolID == callID {
				patch(it)
				return
			}
		}
	}
	for i := len(snap.Items) - 1; i >= 0; i-- {
		it := &snap.Items[i]
		if it.Kind == service.WorkerStreamItemTool && it.EndedAt.IsZero() &&
			(toolName == "" || it.ToolName == toolName) {
			patch(it)
			return
		}
	}
	now := time.Now()
	snap.Items = append(snap.Items, service.WorkerStreamItem{
		Kind:       service.WorkerStreamItemTool,
		ToolID:     callID,
		ToolName:   toolName,
		ToolResult: result,
		ToolError:  isError,
		StartedAt:  now,
		EndedAt:    now,
	})
}

// attachWorkerStreamFileChange pairs a session.file_change event with the
// chat card's matching tool item, mirroring attachFileChange in
// slot_output.go for the worker-stream (chat card) copy of the same
// lifecycle. Only the diff fields are set on a match — EndedAt is left
// alone so a later tool_result still completes the item in place instead
// of finding it "done" and synthesizing a duplicate (see
// appendWorkerStreamToolResult).
func (m *Model) attachWorkerStreamFileChange(slot *runtimeSlot, msg SessionFileChangeMsg) {
	if slot == nil || m.slotHidden(slot) {
		return
	}
	snap := m.ensureWorkerStream(slot)
	snap.LastActivity = time.Now()

	set := func(it *service.WorkerStreamItem) {
		it.FileDiff = msg.Diff
		it.DiffAdded = msg.Added
		it.DiffRemoved = msg.Removed
		it.DiffCreated = msg.Created
		it.DiffTruncated = msg.Truncated
	}

	// Pass 1: name + path match, preferring a pending item.
	var completedMatch *service.WorkerStreamItem
	for i := len(snap.Items) - 1; i >= 0; i-- {
		it := &snap.Items[i]
		if it.Kind != service.WorkerStreamItemTool || it.ToolName != msg.ToolName ||
			toolArgPath(it.ToolArgs) != msg.Path {
			continue
		}
		if it.EndedAt.IsZero() {
			set(it)
			return
		}
		if completedMatch == nil {
			completedMatch = it
		}
	}
	if completedMatch != nil {
		set(completedMatch)
		return
	}

	// Pass 2: name-only fallback, preferring the newest pending item.
	var completedByName *service.WorkerStreamItem
	for i := len(snap.Items) - 1; i >= 0; i-- {
		it := &snap.Items[i]
		if it.Kind != service.WorkerStreamItemTool || it.ToolName != msg.ToolName {
			continue
		}
		if it.EndedAt.IsZero() {
			set(it)
			return
		}
		if completedByName == nil {
			completedByName = it
		}
	}
	if completedByName != nil {
		set(completedByName)
		return
	}

	// Pass 3: no matching tool item — synthesize a completed one.
	now := time.Now()
	it := service.WorkerStreamItem{
		Kind:      service.WorkerStreamItemTool,
		ToolName:  msg.ToolName,
		StartedAt: now,
		EndedAt:   now,
	}
	set(&it)
	snap.Items = append(snap.Items, it)
}

// markWorkerStreamDone flips the Done flag on whichever open worker
// stream block matches the just-finished session. Called from the
// SessionDoneMsg handler. The block stays at the top of the stack but
// renders as "✓ done" instead of "● streaming" — which also blocks
// future events from coalescing into it.
func (m *Model) markWorkerStreamDone(sessionID string) {
	for i := len(m.chat.entries) - 1; i >= 0; i-- {
		e := &m.chat.entries[i]
		if e.Kind != service.ChatEntryKindWorkerStream || e.WorkerStream == nil {
			continue
		}
		if e.WorkerStream.SessionID == sessionID {
			e.WorkerStream.Done = true
			return
		}
	}
}

// renderWorkerStreamBlock formats a worker stream snapshot as a
// bordered block for the chat viewport. Header line shows the worker
// name, short job id, and a status indicator; body interleaves
// glamour-rendered text and Lipgloss-styled tool blocks (the same
// helpers the Jobs pane uses, for visual consistency). selected
// brightens the left border so the user can see which block their
// Up-arrow selection has landed on, mirroring the JobResult block.
func (m *Model) renderWorkerStreamBlock(snap *service.WorkerStreamSnapshot, width int, selected bool) string {
	if snap == nil || width < 8 {
		return ""
	}

	const indicatorWidth = 2 // border + space

	innerW := width - indicatorWidth - 2 // border + padding
	if innerW < 8 {
		innerW = 8
	}

	// Node label — strip the "graph:" prefix; graph-ness is implied.
	node := strings.TrimPrefix(snap.WorkerName, "graph:")
	if node == "" {
		node = "worker"
	}

	// Job + task context so a card is traceable to what it belongs to. The
	// short job hash alone is meaningless once more than one job is running.
	jobTitle := ""
	if j, ok := m.jobByID(snap.JobID); ok {
		jobTitle = j.Title
	}
	if jobTitle == "" {
		jobTitle = snap.JobID
		if len(jobTitle) > 8 {
			jobTitle = jobTitle[:8]
		}
	}
	taskTitle := m.taskTitleByID(snap.JobID, snap.TaskID)

	// Status glyph + duration. A card is either done or still running — no
	// token-activity "idle" state (slow local models pause far longer than any
	// timeout, and an in-flight tool means it's still working).
	var status string
	if snap.Done {
		status = lipgloss.NewStyle().Foreground(ColorConnected).Render("✓ " + formatStreamDuration(snap.StartedAt, snap.LastActivity))
	} else {
		status = lipgloss.NewStyle().Foreground(ColorStreaming).Render("● " + formatStreamDuration(snap.StartedAt, time.Now()))
	}

	// Two-line header. The bold headline is the task — the most descriptive,
	// least-repeated label — with the node id beside it (the per-branch
	// distinguisher under fan-out). The job title is identical on every card,
	// so it's demoted to dim context alongside the status on line 2.
	headline := taskTitle
	if headline == "" {
		headline = node // graphless or odd cases: fall back to the node id
	}
	nodeSuffix := ""
	if taskTitle != "" && node != "" {
		nodeSuffix = node
	}
	avail := innerW - 3 // room for the "🍞 " icon
	if nodeSuffix != "" {
		avail -= len(nodeSuffix) + 3 // " · " + node
	}
	if avail < 10 {
		avail = 10
	}
	line1 := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render("🍞 " + truncateStr(headline, avail))
	if nodeSuffix != "" {
		nodeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
		line1 += DimStyle.Render(" · ") + nodeStyle.Render(nodeSuffix)
	}
	if selected {
		line1 += "  " + DimStyle.Render("[enter to view]")
	}
	// Line 2: status, then the job title in dim as traceable context (it repeats
	// across every card, so it stays quiet). Indented to align under the icon.
	line2 := DimStyle.Render("   ") + status
	if jobTitle != "" {
		line2 += DimStyle.Render(" · " + truncateStr(jobTitle, innerW/2))
	}

	// Finished cards collapse to just their two header rows so a completed run
	// stays scannable instead of a wall of stale output. The body is shown only
	// while the card is still streaming, or when the user has selected it
	// (arrow-navigation peek). Full output is always reachable via [enter to
	// view] regardless.
	content := line1 + "\n" + line2
	if !snap.Done || selected {
		// Indent the body to the same column as the title/task text (past the
		// "🍞 " icon — 3 cells), rendered that much narrower so it doesn't overflow.
		const bodyIndent = 3
		body := m.renderWorkerStreamItems(snap.Items, innerW-bodyIndent)
		if body != "" {
			content += "\n" + indentLines(body, bodyIndent)
		}
	}

	borderColor := ColorAccent
	switch {
	case selected:
		borderColor = ColorPrimary
	case snap.Done:
		// Dim the left bar once the node has finished so attention stays on
		// the cards that are still running.
		borderColor = ColorBorder
	}
	frame := lipgloss.NewStyle().
		Border(lipgloss.Border{Left: "▌"}, false, false, false, true).
		BorderForeground(borderColor).
		PaddingLeft(1).
		Width(width - 2)
	return frame.Render(content)
}

// renderWorkerStreamItems iterates items in order, rendering text
// runs through the chat's glamour renderer (m.mdRender) and tool calls
// through the shared renderToolBlock helper from slot_render.go. The
// final output is tail-truncated to workerStreamChatMaxLines so the
// in-chat block stays compact regardless of how chatty the worker
// gets — long prose simply scrolls off the top of its own block.
func (m *Model) renderWorkerStreamItems(items []service.WorkerStreamItem, width int) string {
	if len(items) == 0 {
		return DimStyle.Italic(true).Render("(waiting for output…)")
	}
	var parts []string
	for i := range items {
		it := &items[i]
		switch it.Kind {
		case service.WorkerStreamItemText:
			// Streamed model output often carries stray blank-line runs that
			// render as large vertical gaps inside the card; collapse them.
			txt := collapseBlankLines(it.Text)
			if txt == "" {
				continue
			}
			// Wrap to the block's exact content width rather than markdown-
			// rendering at the wider chat width — the latter overflows this
			// narrower card and the frame re-wraps it, orphaning trailing words.
			// The full markdown view is still available via Enter-to-deep-link.
			parts = append(parts, wrapText(txt, width))
		case service.WorkerStreamItemTool:
			parts = append(parts, renderToolBlock(workerStreamItemAsOutputItem(it), width))
		}
	}
	full := strings.TrimRight(strings.Join(parts, "\n"), "\n")
	lines := strings.Split(full, "\n")
	if len(lines) <= workerStreamChatMaxLines {
		return full
	}
	return strings.Join(lines[len(lines)-workerStreamChatMaxLines:], "\n")
}

// collapseBlankLines trims trailing whitespace from each line, drops leading
// and trailing blank lines, and collapses any run of blank lines down to a
// single one. A streaming model frequently emits stray blank runs that would
// otherwise open large vertical gaps inside a worker card; a single blank line
// is kept so genuine paragraph breaks survive.
func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	prevBlank := true // seeded true so leading blank lines are dropped
	for _, ln := range lines {
		ln = strings.TrimRight(ln, " \t")
		blank := ln == ""
		if blank && prevBlank {
			continue
		}
		out = append(out, ln)
		prevBlank = blank
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// workerStreamItemAsOutputItem adapts a service.WorkerStreamItem to
// the unexported outputItem the slot_render.renderToolBlock helper
// expects. Reusing the same renderer keeps tool blocks visually
// identical between the Jobs pane and the chat.
func workerStreamItemAsOutputItem(it *service.WorkerStreamItem) *outputItem {
	return &outputItem{
		kind:       outputItemTool,
		toolID:     it.ToolID,
		toolName:   it.ToolName,
		toolArgs:   it.ToolArgs,
		toolResult: it.ToolResult,
		toolError:  it.ToolError,
		startedAt:  it.StartedAt,
		endedAt:    it.EndedAt,

		fileDiff:      it.FileDiff,
		diffAdded:     it.DiffAdded,
		diffRemoved:   it.DiffRemoved,
		diffCreated:   it.DiffCreated,
		diffTruncated: it.DiffTruncated,
	}
}
