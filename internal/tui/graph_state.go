// Graph task state: accumulates topology + per-node phase from the graph
// lifecycle events (GraphNodeStarted / GraphNodeDone) so the graph map
// modal can render live data for an ongoing run. The topology itself is
// resolved from the loaded graph-definition catalog via the task's
// graph_id.
package tui

import (
	"github.com/jefflinse/toasters/internal/service"
	"github.com/jefflinse/toasters/internal/tui/dagmap"
)

type graphTaskState struct {
	jobID    string
	taskID   string
	graphID  string
	topology dagmap.Topology
	nodes    dagmap.NodeStates
}

// taskGraphID returns the graph id assigned to (jobID, taskID) from the
// progress state, or "" if the task is not known.
func (m *Model) taskGraphID(jobID, taskID string) string {
	for _, task := range m.progress.tasks[jobID] {
		if task.ID == taskID {
			return task.GraphID
		}
	}
	return ""
}

// topologyForGraphID resolves a graph id to a dagmap topology via the cached
// definitions. Returns ok=false if the graph isn't loaded yet — the caller
// should fall back to an empty topology that renders "waiting for graph
// definition…".
func (m *Model) topologyForGraphID(graphID string) (dagmap.Topology, bool) {
	if graphID == "" {
		return dagmap.Topology{}, false
	}
	def, ok := m.graphDefs[graphID]
	if !ok {
		return dagmap.Topology{}, false
	}
	return graphDefinitionToTopology(def), true
}

// graphDefinitionToTopology converts a service-level graph definition into
// the dagmap topology shape, inserting synthetic Start/End edges so the
// renderer has a root and sink.
func graphDefinitionToTopology(def service.GraphDefinition) dagmap.Topology {
	topology := dagmap.Topology{
		Nodes: append([]string(nil), def.Nodes...),
	}
	// Start → Entry.
	if def.Entry != "" {
		topology.Edges = append(topology.Edges, dagmap.Edge{
			From: dagmap.StartName,
			To:   def.Entry,
			Kind: dagmap.EdgeStatic,
		})
	}
	// Body edges. Service edges use "" for the "end" sentinel; dagmap uses
	// its own EndName.
	for _, e := range def.Edges {
		to := e.To
		if to == "" {
			to = dagmap.EndName
		}
		kind := dagmap.EdgeStatic
		if e.Kind == service.GraphEdgeConditional {
			kind = dagmap.EdgeConditional
		}
		topology.Edges = append(topology.Edges, dagmap.Edge{
			From:  e.From,
			To:    to,
			Kind:  kind,
			Label: e.Label,
		})
	}
	// Exit → End (only if the exit doesn't already have an outgoing edge).
	if def.Exit != "" && !hasEdgeFrom(topology.Edges, def.Exit) {
		topology.Edges = append(topology.Edges, dagmap.Edge{
			From: def.Exit,
			To:   dagmap.EndName,
			Kind: dagmap.EdgeStatic,
		})
	}
	return topology
}

func hasEdgeFrom(edges []dagmap.Edge, from string) bool {
	for _, e := range edges {
		if e.From == from {
			return true
		}
	}
	return false
}

// recordGraphNodeStarted marks a node as running inside its task's graph
// state. Creates the entry on first sight. Bumps ExecCount each time the
// node enters so cycles are visible.
func (m *Model) recordGraphNodeStarted(jobID, taskID, node string) {
	gts := m.ensureGraphTaskState(jobID, taskID)
	ns := gts.nodes[node]
	ns.Phase = dagmap.PhaseRunning
	ns.ExecCount++
	gts.nodes[node] = ns
	m.lastGraphTaskID = taskID
}

// recordGraphNodeDone marks a node as completed and stores the TaskState
// status (e.g. "tests_failed", "review_approved") so the user can see what
// routed the graph.
func (m *Model) recordGraphNodeDone(jobID, taskID, node, status string) {
	gts := m.ensureGraphTaskState(jobID, taskID)
	ns := gts.nodes[node]
	ns.Phase = dagmap.PhaseCompleted
	ns.LastStatus = status
	gts.nodes[node] = ns
	m.lastGraphTaskID = taskID
}

func (m *Model) ensureGraphTaskState(jobID, taskID string) *graphTaskState {
	if m.graphTasks == nil {
		m.graphTasks = make(map[string]*graphTaskState)
	}
	gts, ok := m.graphTasks[taskID]
	if !ok {
		graphID := m.taskGraphID(jobID, taskID)
		topology, _ := m.topologyForGraphID(graphID)
		gts = &graphTaskState{
			jobID:    jobID,
			taskID:   taskID,
			graphID:  graphID,
			topology: topology,
			nodes:    make(dagmap.NodeStates),
		}
		m.graphTasks[taskID] = gts
		return gts
	}
	m.refreshGraphTaskState(gts)
	return gts
}

// refreshGraphTaskState fills in the graph id and topology on an existing
// state when they couldn't be resolved at creation time. The first
// graph.node_started event commonly arrives before the progress.update that
// carries the task's graph_id, so the TUI may cache a state with empty
// fields; render-time callers run this to recover once the missing data
// shows up.
func (m *Model) refreshGraphTaskState(gts *graphTaskState) {
	if gts == nil {
		return
	}
	if gts.graphID == "" {
		gts.graphID = m.taskGraphID(gts.jobID, gts.taskID)
	}
	if len(gts.topology.Nodes) == 0 && gts.graphID != "" {
		if topology, ok := m.topologyForGraphID(gts.graphID); ok {
			gts.topology = topology
		}
	}
}

// activeGraphTaskState returns the most recently touched graph task state,
// or nil if none have been seen.
func (m *Model) activeGraphTaskState() *graphTaskState {
	if m.lastGraphTaskID == "" {
		return nil
	}
	gts := m.graphTasks[m.lastGraphTaskID]
	m.refreshGraphTaskState(gts)
	return gts
}
