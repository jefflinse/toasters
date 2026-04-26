// Worker stream chat blocks: live, in-chat blocks that interleave a
// worker's streamed output (text + tool calls) into the operator
// conversation. Open blocks accept additional events as long as they
// stay the most recent chat entry, no other event has superseded them,
// and 60s haven't passed since the last update. Outside that window,
// the next streamed token starts a fresh block.
package tui

import (
	"encoding/json"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// workerStreamIdleWindow caps how long a worker stream block stays
// "open" without new activity. Past this, the block is treated as
// closed for both grouping (the next event creates a new block) and
// rendering (header shows "(idle)" instead of "● streaming").
const workerStreamIdleWindow = 60 * time.Second

// openWorkerStream returns the current open worker stream block for
// (workerName, jobID) — i.e. the last entry in the chat, if it is a
// worker_stream matching the same worker+job, hasn't been closed by
// the worker, and has activity within the idle window. Returns nil
// otherwise; callers should append a new block in that case.
func (m *Model) openWorkerStream(workerName, jobID string) *service.WorkerStreamSnapshot {
	if len(m.chat.entries) == 0 {
		return nil
	}
	last := &m.chat.entries[len(m.chat.entries)-1]
	if last.Kind != service.ChatEntryKindWorkerStream || last.WorkerStream == nil {
		return nil
	}
	snap := last.WorkerStream
	if snap.WorkerName != workerName || snap.JobID != jobID {
		return nil
	}
	if snap.Done {
		return nil
	}
	if time.Since(snap.LastActivity) > workerStreamIdleWindow {
		return nil
	}
	return snap
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
	if slot == nil || text == "" {
		return
	}
	snap := m.openWorkerStream(slot.agentName, slot.jobID)
	if snap == nil {
		snap = m.newWorkerStreamEntry(slot)
	}
	snap.SessionID = slot.sessionID
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
	if slot == nil || toolName == "" {
		return
	}
	snap := m.openWorkerStream(slot.agentName, slot.jobID)
	if snap == nil {
		snap = m.newWorkerStreamEntry(slot)
	}
	snap.SessionID = slot.sessionID
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
	if slot == nil {
		return
	}
	snap := m.openWorkerStream(slot.agentName, slot.jobID)
	if snap == nil {
		snap = m.newWorkerStreamEntry(slot)
	}
	snap.SessionID = slot.sessionID
	snap.LastActivity = time.Now()
	for i := len(snap.Items) - 1; i >= 0; i-- {
		it := &snap.Items[i]
		if it.Kind == service.WorkerStreamItemTool && it.ToolID == callID {
			it.ToolResult = result
			it.ToolError = isError
			it.EndedAt = time.Now()
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

	worker := snap.WorkerName
	if worker == "" {
		worker = "worker"
	}
	shortJob := snap.JobID
	if len(shortJob) > 8 {
		shortJob = shortJob[:8]
	}

	var status string
	switch {
	case snap.Done:
		status = lipgloss.NewStyle().Foreground(ColorConnected).Render("✓ done")
	case time.Since(snap.LastActivity) <= workerStreamIdleWindow:
		status = lipgloss.NewStyle().Foreground(ColorStreaming).Render("● streaming")
	default:
		status = DimStyle.Render("(idle)")
	}

	headerLeft := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render("🍞 "+worker) +
		DimStyle.Render(" · "+shortJob)
	header := headerLeft + "  " + status
	if selected {
		header += "  " + DimStyle.Render("[enter to view]")
	}

	body := m.renderWorkerStreamItems(snap.Items, innerW)

	content := header
	if body != "" {
		content += "\n" + body
	}

	borderColor := ColorAccent
	if selected {
		borderColor = ColorPrimary
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
// through the shared renderToolBlock helper from slot_render.go.
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
			b.WriteString(m.renderMarkdown(it.Text))
		case service.WorkerStreamItemTool:
			b.WriteString(renderToolBlock(workerStreamItemAsOutputItem(it), width))
		}
	}
	return strings.TrimRight(b.String(), "\n")
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

