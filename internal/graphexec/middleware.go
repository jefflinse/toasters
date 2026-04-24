package graphexec

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jefflinse/rhizome"
	"github.com/jefflinse/toasters/internal/db"
)

// EventSink receives graph execution events. This interface is satisfied by
// *service.LocalService — defining it here keeps the dependency direction
// one-way (service imports graphexec, not the reverse).
//
// BroadcastTaskCompleted / BroadcastTaskFailed are the operator-advance
// signals: they must propagate into the operator's event loop so it picks
// up the next ready task (the team-lead path relies on the equivalent
// EventTaskCompleted / EventTaskFailed events emitted by team_tools).
type EventSink interface {
	BroadcastGraphNodeStarted(jobID, taskID, node string)
	BroadcastGraphNodeCompleted(jobID, taskID, node, status string)
	BroadcastGraphCompleted(jobID, taskID, summary string)
	BroadcastGraphFailed(jobID, taskID, errMsg string)
	BroadcastTaskCompleted(jobID, taskID, graphID, summary string, output json.RawMessage, hasNextTask bool)
	BroadcastTaskFailed(jobID, taskID, graphID, errMsg string)
	// BroadcastPrompt surfaces a HITL question that originated inside a
	// graph node (via rhizome.Interrupt). Source is typically
	// "graph:<node>" so the TUI can render an attribution hint.
	BroadcastPrompt(requestID, question string, options []string, source string)
	// BroadcastSessionText carries streamed LLM text from a graph node. The
	// SessionID convention is "graph:<TaskID>:<Node>" so the TUI's existing
	// runtimeSlot pipeline picks it up without a special case.
	BroadcastSessionText(sessionID, text string)
	// BroadcastSessionToolCall carries a tool call the model issued inside
	// a graph node. Routes through the same session.tool_call event type
	// that worker sessions use, so the TUI's grid cards and output panel
	// render graph-node activity without per-source special cases.
	BroadcastSessionToolCall(sessionID, callID, name string, args json.RawMessage)
	// BroadcastSessionToolResult carries the dispatched tool's output.
	// CallID may be empty for graph nodes (mycelium's agent.Event does
	// not carry the originating call id on tool-result events); the TUI
	// renders results inline without requiring a pairing id.
	BroadcastSessionToolResult(sessionID, callID, name, result, errMsg string)
}

type nodeContextKey struct{}

// NodeContext carries per-node identity and an event sink so node bodies
// can emit streaming events (LLM text, tool calls) without rhizome plumbing.
type NodeContext struct {
	JobID     string
	TaskID    string
	Node      string
	SessionID string
	Sink      EventSink
}

// NodeContextFromContext returns the NodeContext injected by
// NodeContextMiddleware, or nil if none is set.
func NodeContextFromContext(ctx context.Context) *NodeContext {
	nc, _ := ctx.Value(nodeContextKey{}).(*NodeContext)
	return nc
}

// NodeContextMiddleware injects a NodeContext into ctx for every node call.
// Must be placed in the middleware chain outside (or at) any middleware
// that calls the node — the node body needs the value at call time.
func NodeContextMiddleware(sink EventSink) rhizome.Middleware[*TaskState] {
	return func(ctx context.Context, node string, state *TaskState, next rhizome.NodeFunc[*TaskState]) (*TaskState, error) {
		nc := &NodeContext{
			JobID:     state.JobID,
			TaskID:    state.TaskID,
			Node:      node,
			SessionID: "graph:" + state.TaskID + ":" + node,
			Sink:      sink,
		}
		ctx = context.WithValue(ctx, nodeContextKey{}, nc)
		return next(ctx, state)
	}
}

// EventMiddleware emits graph node lifecycle events to the service event
// stream. It broadcasts node-started before execution and node-completed
// after, giving the TUI real-time progress at node granularity.
func EventMiddleware(sink EventSink) rhizome.Middleware[*TaskState] {
	return func(ctx context.Context, node string, state *TaskState, next rhizome.NodeFunc[*TaskState]) (*TaskState, error) {
		if sink != nil {
			sink.BroadcastGraphNodeStarted(state.JobID, state.TaskID, node)
		}

		result, err := next(ctx, state)

		if sink != nil {
			status := ""
			if result != nil {
				status = result.Status
			}
			sink.BroadcastGraphNodeCompleted(state.JobID, state.TaskID, node, status)
		}

		return result, err
	}
}

// PersistenceMiddleware writes progress reports to the database at node
// boundaries. After each node completes, it persists a summary so the
// TUI can show historical progress even after reconnection.
func PersistenceMiddleware(store db.Store) rhizome.Middleware[*TaskState] {
	return func(ctx context.Context, node string, state *TaskState, next rhizome.NodeFunc[*TaskState]) (*TaskState, error) {
		result, err := next(ctx, state)

		if store != nil && result != nil {
			report := &db.ProgressReport{
				JobID:    result.JobID,
				TaskID:   result.TaskID,
				WorkerID: fmt.Sprintf("graph:%s", node),
				Status:   progressStatus(result, err),
				Message:  progressMessage(node, result, err),
			}
			if persistErr := store.ReportProgress(ctx, report); persistErr != nil {
				slog.Warn("failed to persist graph progress",
					"node", node, "job_id", result.JobID, "task_id", result.TaskID, "error", persistErr)
			}
		}

		return result, err
	}
}

// LoggingMiddleware provides slog-based logging of node entry and exit.
func LoggingMiddleware() rhizome.Middleware[*TaskState] {
	return func(ctx context.Context, node string, state *TaskState, next rhizome.NodeFunc[*TaskState]) (*TaskState, error) {
		start := time.Now()
		slog.Info("graph node started",
			"node", node, "job_id", state.JobID, "task_id", state.TaskID)

		result, err := next(ctx, state)

		elapsed := time.Since(start)
		if err != nil {
			slog.Error("graph node failed",
				"node", node, "job_id", state.JobID, "task_id", state.TaskID,
				"error", err, "elapsed", elapsed)
		} else {
			status := ""
			if result != nil {
				status = result.Status
			}
			slog.Info("graph node completed",
				"node", node, "job_id", state.JobID, "task_id", state.TaskID,
				"status", status, "elapsed", elapsed)
		}

		return result, err
	}
}

// progressStatus returns a short status string for the progress report.
func progressStatus(state *TaskState, err error) string {
	if err != nil {
		return "failed"
	}
	if state.Status != "" {
		return state.Status
	}
	return "completed"
}

// progressMessage builds a human-readable message for the progress report.
func progressMessage(node string, state *TaskState, err error) string {
	if err != nil {
		return fmt.Sprintf("Node %q failed: %s", node, err.Error())
	}
	if state.FinalText != "" {
		return fmt.Sprintf("Node %q completed: %s", node, truncateSummary(state.FinalText))
	}
	return fmt.Sprintf("Node %q completed", node)
}
