// Graph-pane rendering for the Jobs modal. When the selected task has
// graph state, the right pane renders the node list (with focus navigation)
// on top and the focused node's output below. Legacy agent cards still
// render for tasks that aren't graph-based.
package tui

import (
	"fmt"
	"strings"

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
	outputLines := []string{
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
		outputLines = append(outputLines, DimStyle.Render(strings.Join(meta, "  ·  ")))
	} else if displaySlot != nil {
		outputLines = append(outputLines, DimStyle.Render("status: "+displaySlot.status))
	}
	outputLines = append(outputLines, "")

	outputBodyH := outputH - len(outputLines)
	if outputBodyH < 1 {
		outputBodyH = 1
	}
	outputLines = append(outputLines, renderSlotOutput(displaySlot, innerW, outputBodyH)...)

	// Pad output to outputH.
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

// renderSlotOutput wraps and tail-truncates a runtime slot's accumulated
// output to fit the given pane geometry. Operates directly on a slot
// pointer so the caller is free to choose between focus-specified and
// fallback resolutions.
func renderSlotOutput(slot *runtimeSlot, width, height int) []string {
	if slot == nil || height <= 0 {
		return []string{DimStyle.Italic(true).Render("(waiting for output…)")}
	}
	if slot.output.Len() == 0 {
		return []string{DimStyle.Italic(true).Render("(waiting for output…)")}
	}
	wrapped := lipgloss.NewStyle().Width(width).Render(slot.output.String())
	lines := strings.Split(wrapped, "\n")
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	return lines
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
