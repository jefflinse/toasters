// Graph task state: accumulates topology + per-node phase from the graph
// lifecycle events (GraphNodeStarted / GraphNodeDone) so the graph map
// modal can render live data for an ongoing run.
package tui

import (
	"github.com/jefflinse/toasters/internal/service"
	"github.com/jefflinse/toasters/internal/tui/dagmap"
)

type graphTaskState struct {
	jobID    string
	taskID   string
	jobType  string
	topology dagmap.Topology
	nodes    dagmap.NodeStates
}

// topologyForJobType maps the service.Job.Type string onto a dagmap topology.
// Unknown types fall back to BugFix, matching graphexec's JobTypeUnset default.
func topologyForJobType(t string) dagmap.Topology {
	switch t {
	case "new_feature":
		return dagmap.NewFeature()
	case "prototype":
		return dagmap.Prototype()
	case "single_worker":
		return dagmap.SingleWorker()
	default:
		return dagmap.BugFix()
	}
}

// jobTypeByID returns the Type string for a known job, or "" if not found.
func (m *Model) jobTypeByID(jobID string) string {
	for i := range m.jobs {
		if m.jobs[i].ID == jobID {
			return m.jobs[i].Type
		}
	}
	return ""
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
		jobType := m.jobTypeByID(jobID)
		gts = &graphTaskState{
			jobID:    jobID,
			taskID:   taskID,
			jobType:  jobType,
			topology: topologyForJobType(jobType),
			nodes:    make(dagmap.NodeStates),
		}
		m.graphTasks[taskID] = gts
	}
	return gts
}

// activeGraphTaskState returns the most recently touched graph task state,
// or nil if none have been seen.
func (m *Model) activeGraphTaskState() *graphTaskState {
	if m.lastGraphTaskID == "" {
		return nil
	}
	return m.graphTasks[m.lastGraphTaskID]
}

// Compile-time assertion that service.Job still carries a Type field — if
// the field is renamed or retyped, this call fails and points us at
// jobTypeByID to update.
var _ = func() string { var j service.Job; return j.Type }()
