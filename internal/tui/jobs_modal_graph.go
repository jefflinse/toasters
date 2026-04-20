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
// frame.
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
	// grows with node count but capped so the output always has space.
	listH := len(gts.topology.Nodes) + 1 // one row per node + small header
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

	listBody := dagmap.RenderListFocused(gts.topology, gts.nodes, focusedName)
	listLines := strings.Split(listBody, "\n")
	// Truncate list to listH rows (should rarely trigger with sensible topologies).
	if len(listLines) > listH {
		listLines = listLines[:listH]
	}

	divider := DimStyle.Render(strings.Repeat("─", innerW))

	// Output pane: header + meta + streaming LLM text for the focused node.
	outputHeader := "Output"
	if focusedName != "" {
		outputHeader = "Output · " + focusedName
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
	}
	outputLines = append(outputLines, "")

	// Streamed LLM text comes from the synthesized session whose ID matches
	// NodeContextMiddleware's convention. GraphNodeStarted creates the slot;
	// SessionTextMsg handler appends chunks into slot.output.
	outputBodyH := outputH - len(outputLines)
	if outputBodyH < 1 {
		outputBodyH = 1
	}
	outputLines = append(outputLines, renderGraphNodeOutput(m, gts.taskID, focusedName, innerW, outputBodyH)...)

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

// renderGraphNodeOutput returns the body lines for the focused node's
// streamed text, wrapped to width and tail-truncated to height. Shows a
// subtle placeholder when nothing has streamed yet.
func renderGraphNodeOutput(m *Model, taskID, node string, width, height int) []string {
	if node == "" || height <= 0 {
		return nil
	}
	sessionID := "graph:" + taskID + ":" + node
	slot, ok := m.runtimeSessions[sessionID]
	if !ok || slot.output.Len() == 0 {
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
