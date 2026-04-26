// Graph-pane rendering for the Jobs modal. When the selected task has
// graph state, the right pane renders the node list (with focus navigation)
// on top and the focused node's output below. Legacy agent cards still
// render for tasks that aren't graph-based.
package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/tui/dagmap"
)

// selectedJobsModalGraphTaskState returns the graph state for whichever
// task is currently selected in the jobs modal, or nil if that task has
// no graph state (legacy / non-graph task).
func (m *Model) selectedJobsModalGraphTaskState() *graphTaskState {
	if m.jobsModal.jobIdx >= len(m.jobsModal.jobs) || m.jobsModal.jobIdx < 0 {
		return nil
	}
	job := m.jobsModal.jobs[m.jobsModal.jobIdx]
	tasks := m.jobsModal.tasks[job.ID]
	if m.jobsModal.taskIdx >= len(tasks) || m.jobsModal.taskIdx < 0 {
		return nil
	}
	return m.graphTasks[tasks[m.jobsModal.taskIdx].ID]
}

// renderGraphTaskPane returns lines for the right-pane graph view: a
// focused list on top, a divider, and the selected node's detail / output
// below. innerW/innerH are the usable width and height inside the panel
// frame. When the graph topology hasn't been resolved yet (graph definition
// not loaded, or task graph_id missing) the list collapses to one
// placeholder row so the worker's streaming output still gets the rest of
// the pane — the output is the user's window into the running model and
// shouldn't go dark just because the topology lookup hasn't caught up.
func (m *Model) renderGraphTaskPane(gts *graphTaskState, innerW, innerH int) []string {
	// Clamp graphNodeIdx.
	if m.jobsModal.graphNodeIdx >= len(gts.topology.Nodes) {
		m.jobsModal.graphNodeIdx = len(gts.topology.Nodes) - 1
	}
	if m.jobsModal.graphNodeIdx < 0 {
		m.jobsModal.graphNodeIdx = 0
	}
	focusedName := ""
	if len(gts.topology.Nodes) > 0 {
		focusedName = gts.topology.Nodes[m.jobsModal.graphNodeIdx]
	}

	// List occupies roughly the top half; output gets the rest. List height
	// grows with node count but capped so the output always has space. With
	// no topology yet, listH stays at 1 so output gets nearly the entire
	// pane.
	listH := 1
	if n := len(gts.topology.Nodes); n > 0 {
		listH = n + 1 // one row per node + small header
	}
	maxListH := innerH / 2
	if listH > maxListH && maxListH > 0 {
		listH = maxListH
	}
	if listH < 1 {
		listH = 1
	}
	outputH := innerH - listH - 2 // -2 for divider and spacing
	if outputH < 3 {
		outputH = 3
	}

	var listLines []string
	if len(gts.topology.Nodes) > 0 {
		listBody := dagmap.RenderListFocused(gts.topology, gts.nodes, focusedName)
		listLines = strings.Split(listBody, "\n")
	} else {
		listLines = []string{DimStyle.Italic(true).Render("(waiting for graph topology…)")}
	}
	// Truncate list to listH rows (should rarely trigger with sensible topologies).
	if len(listLines) > listH {
		listLines = listLines[:listH]
	}

	divider := DimStyle.Render(strings.Repeat("─", innerW))

	// Pick the session we'll stream from: the focused graph node if we have
	// one, otherwise any active worker session for this task. The fallback
	// is what makes the pane useful before the topology has resolved.
	displayName, displaySlot := m.pickGraphPaneDisplay(gts, focusedName)

	outputHeader := "Output"
	if displayName != "" {
		outputHeader = "Output · " + displayName
	}
	headerLines := []string{
		lipgloss.NewStyle().Bold(true).Foreground(ColorAccent).Render(outputHeader),
	}
	if focusedName != "" {
		ns := gts.nodes[focusedName]
		meta := []string{
			fmt.Sprintf("phase: %s", phaseWord(ns.Phase)),
		}
		if ns.ExecCount > 0 {
			meta = append(meta, fmt.Sprintf("runs: %d", ns.ExecCount))
		}
		if ns.LastStatus != "" {
			meta = append(meta, "status: "+ns.LastStatus)
		}
		headerLines = append(headerLines, DimStyle.Render(strings.Join(meta, "  ·  ")))
	} else if displaySlot != nil {
		headerLines = append(headerLines, DimStyle.Render("status: "+displaySlot.status))
	}
	headerLines = append(headerLines, "")

	bodyH := outputH - len(headerLines)
	if bodyH < 1 {
		bodyH = 1
	}

	bodyLines := m.renderGraphPaneOutputViewport(displaySlot, innerW, bodyH)

	outputLines := append(headerLines, bodyLines...)
	for len(outputLines) < outputH {
		outputLines = append(outputLines, "")
	}
	if len(outputLines) > outputH {
		outputLines = outputLines[:outputH]
	}

	lines := []string{}
	lines = append(lines, listLines...)
	lines = append(lines, divider)
	lines = append(lines, outputLines...)
	return lines
}

// renderGraphPaneOutputViewport configures the shared output viewport
// for the given slot and returns its rendered lines, padded to height.
// Auto-tails to the bottom unless the user has scrolled away. When the
// displayed slot changes (focused node moves, slot finishes), scroll
// state resets so the new content is shown from the bottom.
func (m *Model) renderGraphPaneOutputViewport(slot *runtimeSlot, width, height int) []string {
	if width <= 0 || height <= 0 {
		return []string{}
	}
	jm := &m.jobsModal

	if !jm.outputViewportInit {
		jm.outputViewport = viewport.New()
		jm.outputViewport.MouseWheelEnabled = true
		jm.outputViewport.KeyMap = viewport.KeyMap{}
		jm.outputViewportInit = true
	}

	jm.outputViewport.SetWidth(width)
	jm.outputViewport.SetHeight(height)

	if slot == nil {
		jm.outputViewport.SetContent(DimStyle.Italic(true).Render("(waiting for output…)"))
		return padViewportLines(jm.outputViewport.View(), height)
	}

	// Slot switch — reset scroll state so we don't carry the previous
	// worker's offset into the new one.
	if jm.outputCurrentSlotID != slot.sessionID {
		jm.outputCurrentSlotID = slot.sessionID
		jm.outputUserScrolled = false
	}

	content := m.renderSlotOutputContent(slot, width)
	if content == "" {
		content = DimStyle.Italic(true).Render("(waiting for output…)")
	}
	jm.outputViewport.SetContent(content)
	if !jm.outputUserScrolled {
		jm.outputViewport.GotoBottom()
	}

	return padViewportLines(jm.outputViewport.View(), height)
}

// scrollGraphPaneOutput scrolls the graph pane's shared output viewport
// by either a half-page (page=true) or a single line (page=false). dir
// is -1 for up, +1 for down. No-op unless the third panel is focused
// and the viewport has been initialized. Updates the
// userScrolled flag so subsequent streamed text doesn't yank the
// position back to the bottom while the user is reading.
func (m *Model) scrollGraphPaneOutput(dir int, page bool) {
	if m.jobsModal.focus != 2 || !m.jobsModal.outputViewportInit {
		return
	}
	vp := &m.jobsModal.outputViewport
	if page {
		if dir < 0 {
			vp.HalfPageUp()
		} else {
			vp.HalfPageDown()
		}
	} else {
		if dir < 0 {
			vp.ScrollUp(1)
		} else {
			vp.ScrollDown(1)
		}
	}
	m.jobsModal.outputUserScrolled = !vp.AtBottom()
}

// padViewportLines splits a viewport's rendered View() into lines and
// pads with empty lines so callers can rely on an exact line count.
func padViewportLines(view string, height int) []string {
	lines := strings.Split(view, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

// pickGraphPaneDisplay returns the (display name, runtime slot) pair the
// graph pane should stream from. When a graph node is focused we look its
// session up by the conventional "graph:<task>:<node>" key; if that's
// missing — or no node is focused yet — we fall back to the most recently
// started active session for this task, then to any session for the task.
// The display name follows the slot when possible so the pane header still
// identifies which worker is talking.
func (m *Model) pickGraphPaneDisplay(gts *graphTaskState, focusedName string) (string, *runtimeSlot) {
	if focusedName != "" {
		sessionID := "graph:" + gts.taskID + ":" + focusedName
		if slot, ok := m.runtimeSessions[sessionID]; ok {
			return focusedName, slot
		}
	}
	slots := m.runtimeSessionsForTask(gts.taskID)
	if len(slots) == 0 {
		return focusedName, nil
	}
	slot := slots[0] // active first, then by start time — see runtimeSessionsForTask
	name := focusedName
	if name == "" {
		name = graphNodeFromSessionID(slot.sessionID)
		if name == "" {
			name = slot.agentName
		}
	}
	return name, slot
}

// graphNodeFromSessionID extracts the node component from a synthesized
// graph session id ("graph:<task>:<node>"). Returns "" for any other shape
// so callers can fall back to a different label.
func graphNodeFromSessionID(sessionID string) string {
	const prefix = "graph:"
	if !strings.HasPrefix(sessionID, prefix) {
		return ""
	}
	rest := sessionID[len(prefix):]
	idx := strings.LastIndex(rest, ":")
	if idx < 0 {
		return ""
	}
	return rest[idx+1:]
}

func phaseWord(p dagmap.Phase) string {
	switch p {
	case dagmap.PhaseRunning:
		return "running"
	case dagmap.PhaseCompleted:
		return "completed"
	case dagmap.PhaseFailed:
		return "failed"
	case dagmap.PhaseInterrupted:
		return "interrupted"
	default:
		return "pending"
	}
}
