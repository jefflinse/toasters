package graphexec

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/jefflinse/rhizome"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/runtime"
)

// EventSink receives graph execution events. This interface is satisfied by
// *service.LocalService — defining it here keeps the dependency direction
// one-way (service imports graphexec, not the reverse).
//
// BroadcastTaskCompleted / BroadcastTaskFailed are the operator-advance
// signals: they must propagate into the operator's event loop so it picks
// up the next ready task.
type EventSink interface {
	BroadcastGraphNodeStarted(jobID, taskID, node string)
	BroadcastGraphNodeCompleted(jobID, taskID, node, status string)
	BroadcastGraphCompleted(jobID, taskID, summary string)
	BroadcastGraphFailed(jobID, taskID, errMsg string)
	BroadcastTaskCompleted(jobID, taskID, graphID, summary string, output json.RawMessage, hasNextTask bool)
	BroadcastTaskFailed(jobID, taskID, graphID, errMsg string)
	// BroadcastPrompt surfaces a HITL round (one or more questions) that
	// originated inside a graph node (via rhizome.Interrupt). Source is
	// typically "graph:<node>" so the TUI can render an attribution hint;
	// jobID/taskID identify the work being gated so the client can show which
	// job the node is asking about.
	BroadcastPrompt(requestID string, questions []PromptQuestion, source, jobID, taskID string)
	// ResolveBlocker clears a pending HITL request once the node's broker.Ask
	// returns — whether the user answered or the node's context was cancelled.
	// Idempotent: resolving an unknown request is a no-op.
	ResolveBlocker(requestID string)
	// BroadcastSessionPrompt fires once per node session, after the system
	// prompt has been composed and before the LLM starts. The TUI uses it
	// to populate the prompt-viewer modal for the existing slot created
	// by graph.node_started.
	BroadcastSessionPrompt(sessionID, systemPrompt, initialMessage string)
	// BroadcastSessionMeta carries the resolved model, provider, sampling
	// temperature, and thinking flag for a node session. Graph nodes don't
	// surface these through the active-session snapshot (their DB row is keyed
	// by a UUID, not the "graph:<task>:<node>" slot id), so the executor emits
	// them explicitly once per node for the grid card to display.
	BroadcastSessionMeta(sessionID, model, provider string, temperature float64, thinking bool)
	// BroadcastSessionContextTokens carries a node session's live context-window
	// occupancy (most recent round-trip's prompt size), emitted per round-trip so
	// the fleet pane's context bar fills while the node runs — graph-node token
	// counts otherwise only reach the DB at completion.
	BroadcastSessionContextTokens(sessionID string, contextTokens int64)
	// BroadcastSessionText carries streamed LLM text from a graph node. The
	// SessionID convention is "graph:<TaskID>:<Node>" so the TUI's existing
	// runtimeSlot pipeline picks it up without a special case.
	BroadcastSessionText(sessionID, text string)
	// BroadcastSessionReasoning carries streamed reasoning (chain-of-thought)
	// from a graph node. Routes through a session.reasoning event; the TUI
	// renders it alongside the node's text output with its own style.
	BroadcastSessionReasoning(sessionID, text string)
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
	// BroadcastSessionFileChange carries a write_file/edit_file mutation's
	// diff from a graph node's tool execution, wired via CoreTools'
	// FileChangeNotifier (see buildToolExecutor in executor.go). It's a pure
	// display side-channel — never part of the tool result the LLM sees.
	BroadcastSessionFileChange(sessionID string, fc runtime.FileChange)
	// BroadcastSessionShellExec carries a shell tool execution's exit code,
	// duration, and output size from a graph node's tool execution, wired via
	// CoreTools' ShellExecNotifier (see buildToolExecutor in executor.go).
	// Like BroadcastSessionFileChange, it's a pure display side-channel.
	BroadcastSessionShellExec(sessionID string, se runtime.ShellExec)
	// BroadcastSessionWorkerSpawn carries a spawn_worker attempt's role,
	// task, depth, and outcome from a graph node's tool execution, wired via
	// CoreTools' WorkerSpawnNotifier (see buildToolExecutor in executor.go).
	// Like BroadcastSessionShellExec, it's a pure display side-channel. In
	// practice buildToolExecutor never attaches a spawner to graph-node
	// CoreTools, so spawn_worker isn't advertised there and this fires only
	// on the defensive "spawn_worker is not available" path — wired anyway
	// for parity with the other two side-channels and in case that changes.
	BroadcastSessionWorkerSpawn(sessionID string, ws runtime.WorkerSpawn)
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
			// Mirror emitNodeCompleted (fanout_node.go): a node error must
			// broadcast "failed" so the TUI can mark the node ✗ and
			// failedGraphNode can name it. result.Status, when a node sets
			// one, is a routing outcome on a successful node.
			status := "completed"
			switch {
			case err != nil:
				status = "failed"
			case result != nil && result.Status != "":
				status = result.Status
			}
			sink.BroadcastGraphNodeCompleted(state.JobID, state.TaskID, node, status)
		}

		return result, err
	}
}

// PersistenceMiddleware writes progress reports to the database at node
// boundaries. After each node completes, it persists a summary so the
// TUI can show historical progress even after reconnection. It also
// records one node_executions row per logical execution — this sits
// outside rhizome.Retry in the middleware chain (see Execute's chain
// comment), so internal retries of the same node collapse into the row
// this single call produces rather than one row per attempt.
func PersistenceMiddleware(store db.Store, graphID string) rhizome.Middleware[*TaskState] {
	return func(ctx context.Context, node string, state *TaskState, next rhizome.NodeFunc[*TaskState]) (*TaskState, error) {
		start := time.Now()
		result, err := next(ctx, state)
		elapsed := time.Since(start)

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

			id, uuidErr := uuid.NewV4()
			if uuidErr != nil {
				slog.Warn("failed to mint node execution id", "node", node, "error", uuidErr)
			} else {
				exec := &db.NodeExecution{
					ID:        id.String(),
					JobID:     result.JobID,
					TaskID:    result.TaskID,
					GraphID:   graphID,
					Node:      node,
					Status:    progressStatus(result, err),
					ElapsedMS: elapsed.Milliseconds(),
				}
				if persistErr := store.InsertNodeExecution(ctx, exec); persistErr != nil {
					slog.Warn("failed to persist node execution",
						"node", node, "job_id", result.JobID, "task_id", result.TaskID, "error", persistErr)
				}
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
