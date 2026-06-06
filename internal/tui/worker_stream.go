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
		WorkerName:   slot.agentName,
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

	// Two-line header: job title (bold) on top; task · node · status dim beneath.
	line1 := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render("🍞 " + truncateStr(jobTitle, innerW-3))
	if selected {
		line1 += "  " + DimStyle.Render("[enter to view]")
	}
	// The task title gets a brighter foreground than the surrounding metadata
	// so it stands out from the dim tool-call detail lines, without competing
	// with the bold job title above or the bright body text below.
	taskStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	line2 := DimStyle.Render("   ")
	if taskTitle != "" {
		line2 += taskStyle.Render(truncateStr(taskTitle, innerW/2)) + DimStyle.Render(" · ")
	}
	line2 += DimStyle.Render(node+" · ") + status

	// Indent the body to the same column as the title/task text (past the "🍞 "
	// icon — 3 cells), and render it that much narrower so it doesn't overflow.
	const bodyIndent = 3
	body := m.renderWorkerStreamItems(snap.Items, innerW-bodyIndent)

	content := line1 + "\n" + line2
	if body != "" {
		content += "\n" + indentLines(body, bodyIndent)
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
	var b strings.Builder
	for i := range items {
		it := &items[i]
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		switch it.Kind {
		case service.WorkerStreamItemText:
			if it.Text == "" {
				continue
			}
			// Wrap to the block's exact content width rather than markdown-
			// rendering at the wider chat width — the latter overflows this
			// narrower card and the frame re-wraps it, orphaning trailing words.
			// The full markdown view is still available via Enter-to-deep-link.
			b.WriteString(wrapText(it.Text, width))
		case service.WorkerStreamItemTool:
			b.WriteString(renderToolBlock(workerStreamItemAsOutputItem(it), width))
		}
	}
	full := strings.TrimRight(b.String(), "\n")
	lines := strings.Split(full, "\n")
	if len(lines) <= workerStreamChatMaxLines {
		return full
	}
	return strings.Join(lines[len(lines)-workerStreamChatMaxLines:], "\n")
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
	}
}
