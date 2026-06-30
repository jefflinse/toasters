package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/operator"
)

// BroadcastDefinitionsReloaded broadcasts a definitions.reloaded event. Called
// from the loader's onChange callback.
func (s *LocalService) BroadcastDefinitionsReloaded() {
	s.broadcast(Event{
		Type:    EventTypeDefinitionsReloaded,
		Payload: nil,
	})
}

// BroadcastJobCreated broadcasts a job.created event. Implements
// operator.SystemEventBroadcaster — called by SystemTools.createJob after the
// new job is persisted. Also kicks off coarse-decompose automatically when
// the job has a description and the graph executor is wired.
func (s *LocalService) BroadcastJobCreated(jobID, title, description string) {
	s.broadcast(Event{
		Type: EventTypeJobCreated,
		Payload: JobCreatedPayload{
			JobID:       jobID,
			Title:       title,
			Description: description,
		},
	})
	if strings.TrimSpace(description) != "" {
		s.dispatchCoarseDecompose(jobID, title, description)
	}
}

// BroadcastTaskCreated broadcasts a task.created event. Implements
// operator.SystemEventBroadcaster — called by SystemTools.createTask after the
// new task is persisted. When the new task has no graph_id set, the service
// automatically dispatches fine-decompose to pick one.
func (s *LocalService) BroadcastTaskCreated(taskID, jobID, title, graphID string) {
	s.broadcast(Event{
		Type: EventTypeTaskCreated,
		Payload: TaskCreatedPayload{
			TaskID:  taskID,
			JobID:   jobID,
			Title:   title,
			GraphID: graphID,
		},
	})
	if graphID == "" {
		s.dispatchFineDecomposeForTask(taskID)
	}
}

// dispatchFineDecomposeForTask resolves the parent task and its job
// before handing off to dispatchFineDecompose. Separated out so
// BroadcastTaskCreated stays terse and only pays DB cost when the task
// actually needs decomposition.
//
// Defers fine-decompose for tasks with unmet predecessors. Fine-decompose
// inputs include the task title plus job context — running it before
// predecessors complete means the decomposer can't incorporate their
// summaries when picking a graph. The retro-trigger in BroadcastTaskCompleted
// re-runs this for every task as it becomes ready.
func (s *LocalService) dispatchFineDecomposeForTask(taskID string) {
	if s.cfg.Store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	task, err := s.cfg.Store.GetTask(ctx, taskID)
	if err != nil {
		slog.Warn("fine-decompose lookup failed; task missing",
			"task_id", taskID, "error", err)
		return
	}
	if !s.taskIsReady(ctx, task) {
		slog.Info("fine-decompose deferred; predecessors incomplete",
			"task_id", taskID, "job_id", task.JobID)
		return
	}
	job, err := s.cfg.Store.GetJob(ctx, task.JobID)
	if err != nil {
		slog.Warn("fine-decompose lookup failed; job missing",
			"task_id", taskID, "error", err)
		return
	}
	s.dispatchFineDecompose(task, job)
}

// dispatchFineDecomposeForReadyTasks scans the job's ready tasks and
// kicks off fine-decompose for any that don't yet have a graph_id. Called
// after a real task completes so newly-unblocked tasks pick a graph at
// the moment their dependencies' summaries are available.
func (s *LocalService) dispatchFineDecomposeForReadyTasks(jobID string) {
	if s.cfg.Store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	ready, err := s.cfg.Store.GetReadyTasks(ctx, jobID)
	if err != nil {
		slog.Warn("failed to list ready tasks after completion",
			"job_id", jobID, "error", err)
		return
	}
	for _, t := range ready {
		if t.GraphID != "" {
			continue
		}
		s.dispatchFineDecomposeForTask(t.ID)
	}
}

// taskIsReady reports whether a task's predecessors are all complete.
// Wraps GetReadyTasks(jobID) and scans the result rather than adding a
// per-task store query.
func (s *LocalService) taskIsReady(ctx context.Context, task *db.Task) bool {
	ready, err := s.cfg.Store.GetReadyTasks(ctx, task.JobID)
	if err != nil {
		slog.Warn("readiness check failed; assuming task is ready",
			"task_id", task.ID, "error", err)
		return true
	}
	for _, r := range ready {
		if r.ID == task.ID {
			return true
		}
	}
	return false
}

// BroadcastTaskAssigned broadcasts a task.assigned event. Implements
// operator.SystemEventBroadcaster — called by SystemTools.assignTask after a
// task has been pre-assigned or dispatched to a graph.
func (s *LocalService) BroadcastTaskAssigned(taskID, jobID, graphID, title string) {
	s.broadcast(Event{
		Type: EventTypeTaskAssigned,
		Payload: TaskAssignedPayload{
			TaskID:  taskID,
			JobID:   jobID,
			GraphID: graphID,
			Title:   title,
		},
	})
}

// graphNodeSessionID builds the "graph:<taskID>:<node>" id the TUI uses for
// graph-node pseudo-sessions, matching graphexec's NodeContext convention.
func graphNodeSessionID(taskID, node string) string {
	return "graph:" + taskID + ":" + node
}

// BroadcastGraphNodeStarted broadcasts a graph.node_started event and records
// the node as active so a reconnecting client can rebuild the Workers panel.
func (s *LocalService) BroadcastGraphNodeStarted(jobID, taskID, node string) {
	sid := graphNodeSessionID(taskID, node)
	s.graphNodeMu.Lock()
	s.activeGraphNodes[sid] = GraphNodeSnapshot{
		SessionID: sid, JobID: jobID, TaskID: taskID, Node: node, StartedAt: time.Now(),
	}
	s.graphNodeMu.Unlock()
	s.broadcast(Event{
		Type: EventTypeGraphNodeStarted,
		Payload: GraphNodeStartedPayload{
			JobID:  jobID,
			TaskID: taskID,
			Node:   node,
		},
	})
}

// BroadcastGraphNodeCompleted broadcasts a graph.node_completed event and drops
// the node from the active set.
func (s *LocalService) BroadcastGraphNodeCompleted(jobID, taskID, node, status string) {
	s.graphNodeMu.Lock()
	delete(s.activeGraphNodes, graphNodeSessionID(taskID, node))
	s.graphNodeMu.Unlock()
	s.broadcast(Event{
		Type: EventTypeGraphNodeCompleted,
		Payload: GraphNodeCompletedPayload{
			JobID:  jobID,
			TaskID: taskID,
			Node:   node,
			Status: status,
		},
	})
}

// BroadcastGraphCompleted broadcasts a graph.completed event.
func (s *LocalService) BroadcastGraphCompleted(jobID, taskID, summary string) {
	s.broadcast(Event{
		Type: EventTypeGraphCompleted,
		Payload: GraphCompletedPayload{
			JobID:   jobID,
			TaskID:  taskID,
			Summary: summary,
		},
	})
}

// BroadcastGraphFailed broadcasts a graph.failed event.
func (s *LocalService) BroadcastGraphFailed(jobID, taskID, errMsg string) {
	s.broadcast(Event{
		Type: EventTypeGraphFailed,
		Payload: GraphFailedPayload{
			JobID:  jobID,
			TaskID: taskID,
			Error:  errMsg,
		},
	})
}

// BroadcastTaskCompleted signals task completion to the operator's event loop
// so it can advance to the next ready task. Called by graphexec.Executor
// after a graph finishes successfully. When the completed graph is a
// decomposition graph, the service consumes the output itself and creates
// follow-up tasks in the database instead of forwarding to the operator.
func (s *LocalService) BroadcastTaskCompleted(jobID, taskID, graphID, summary string, output json.RawMessage, hasNextTask bool) {
	if s.handleDecompositionCompleted(jobID, taskID, graphID, output) {
		return
	}
	// Fan out fine-decompose to tasks that became ready due to this
	// completion. Tasks already pre-assigned to a graph (i.e. fine-decompose
	// already ran) are advanced by the operator's assignNextTask path.
	s.dispatchFineDecomposeForReadyTasks(jobID)
	op := s.currentOperator()
	if op == nil {
		slog.Warn("graph task completed but operator unavailable; next task will not auto-advance",
			"task_id", taskID, "job_id", jobID)
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	if err := op.Send(ctx, operator.Event{
		Type: operator.EventTaskCompleted,
		Payload: operator.TaskCompletedPayload{
			TaskID:      taskID,
			JobID:       jobID,
			GraphID:     graphID,
			Summary:     summary,
			HasNextTask: hasNextTask,
		},
	}); err != nil {
		slog.Warn("failed to forward task_completed to operator",
			"task_id", taskID, "job_id", jobID, "error", err)
	}
}

// BroadcastTaskFailed signals task failure to the operator's event loop so it
// can consult the blocker-handler.
func (s *LocalService) BroadcastTaskFailed(jobID, taskID, graphID, errMsg string) {
	// A failed decomposition bootstrap produces no real tasks, so the job would
	// otherwise strand at "running · 0/0 tasks" forever (there's nothing to
	// retry). Mark it failed so the Jobs panel reflects reality; the operator is
	// still notified below so it can surface the failure and offer to recreate.
	if isDecompositionGraph(graphID) && s.cfg.Store != nil {
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		if err := s.cfg.Store.UpdateJobStatus(ctx, jobID, db.JobStatusFailed); err != nil {
			slog.Error("failed to mark job failed after decomposition failure",
				"job_id", jobID, "graph_id", graphID, "error", err)
		}
		cancel()
	}

	op := s.currentOperator()
	if op == nil {
		slog.Warn("graph task failed but operator unavailable",
			"task_id", taskID, "job_id", jobID)
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	if err := op.Send(ctx, operator.Event{
		Type: operator.EventTaskFailed,
		Payload: operator.TaskFailedPayload{
			TaskID:  taskID,
			JobID:   jobID,
			GraphID: graphID,
			Error:   errMsg,
		},
	}); err != nil {
		slog.Warn("failed to forward task_failed to operator",
			"task_id", taskID, "job_id", jobID, "error", err)
	}
}

// handleDecompositionCompleted consumes task-completion events for the
// two decomposition graphs. Returns true when it fully handled the event
// (caller should not forward to the operator). Returns false when the
// completed graph is not a decomposition graph — the caller continues
// with its normal forwarding path.
func (s *LocalService) handleDecompositionCompleted(jobID, taskID, graphID string, output json.RawMessage) bool {
	if !isDecompositionGraph(graphID) {
		return false
	}
	slog.Info("decomposition graph completed",
		"graph_id", graphID, "job_id", jobID, "task_id", taskID, "output_bytes", len(output))
	s.consumeDecompositionOutput(graphID, taskID, output)
	return true
}
