package graphexec

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/jefflinse/rhizome"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/hitl"
	"github.com/jefflinse/toasters/internal/mcp"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// GraphSource looks up a declarative graph Definition by ID. Implementations
// are expected to be concurrency-safe and to hot-reload transparently (for
// instance, backed by *loader.Loader). A nil GraphSource disables the
// graph-dispatch path — ExecuteTask falls back to the hard-coded templates.
type GraphSource interface {
	GraphByID(id string) *Definition
}

// Executor wraps rhizome graph execution with toasters infrastructure.
// It resolves providers, applies middleware for events and persistence,
// and updates task status after execution.
type Executor struct {
	registry      *provider.Registry
	mcpManager    *mcp.Manager
	promptEngine  *prompt.Engine
	store         db.Store
	eventSink     EventSink
	broker        *hitl.Broker
	graphs        GraphSource
	roles         *RoleRegistry
	defaultModel  string
	nodeTimeout   time.Duration
	retryAttempts int
}

// ExecutorConfig holds configuration for creating an Executor.
type ExecutorConfig struct {
	// Registry resolves provider names to Provider instances.
	Registry *provider.Registry

	// MCPManager supplies MCP-sourced tools. May be nil. A fresh CoreTools
	// is built per task inside ExecuteTask (scoped to the task's workspace
	// directory) and composited with MCP tools via runtime.NewCompositeTools
	// when MCP has any tools registered.
	MCPManager *mcp.Manager

	// PromptEngine composes each node's system prompt from role markdown
	// definitions. Required in production; may be nil in tests that provide
	// their own TemplateConfig.
	PromptEngine *prompt.Engine

	// Store is the database for persistence middleware and task status updates.
	Store db.Store

	// EventSink receives graph execution events (typically *service.LocalService).
	EventSink EventSink

	// Broker handles HITL prompts from nodes (via rhizome.Interrupt). When
	// nil, nodes that call Interrupt receive an error — fine in tests, but
	// production must supply one so investigator/reviewer/planner roles can
	// ask clarifying questions.
	Broker *hitl.Broker

	// DefaultModel is used when the task state doesn't specify a model.
	DefaultModel string

	// NodeTimeout bounds each node attempt. Zero disables the middleware-level
	// timeout; LLM providers still apply their own timeouts inside the call.
	NodeTimeout time.Duration

	// RetryAttempts is the max number of attempts per node, including the
	// initial call. Values ≤ 1 disable retries. Defaults to rhizome's default
	// when zero.
	RetryAttempts int

	// Graphs supplies declarative graph Definitions by ID for the
	// graph-dispatch path. When nil (or when a TaskRequest has no GraphID),
	// ExecuteTask falls back to the hard-coded templates.
	Graphs GraphSource

	// Roles is the role registry used to resolve a YAML node's role: field
	// at dispatch time. When nil, NewRoleRegistry() is used.
	Roles *RoleRegistry
}

// NewExecutor creates an Executor with the given configuration.
func NewExecutor(cfg ExecutorConfig) *Executor {
	retries := cfg.RetryAttempts
	if retries <= 0 {
		retries = rhizome.DefaultRetryMaxAttempts
	}
	roles := cfg.Roles
	if roles == nil {
		roles = NewRoleRegistry()
	}
	return &Executor{
		registry:      cfg.Registry,
		mcpManager:    cfg.MCPManager,
		promptEngine:  cfg.PromptEngine,
		store:         cfg.Store,
		eventSink:     cfg.EventSink,
		broker:        cfg.Broker,
		graphs:        cfg.Graphs,
		roles:         roles,
		defaultModel:  cfg.DefaultModel,
		nodeTimeout:   cfg.NodeTimeout,
		retryAttempts: retries,
	}
}

// interruptHandler is registered on every graph Run via
// rhizome.WithInterruptHandler. Nodes pause by calling rhizome.Interrupt;
// this handler translates the request into a HITL broker Ask so the TUI
// (or any other subscriber) receives the question and the node resumes
// with the user's response. For v1, only "ask_user" kind is supported.
func (e *Executor) interruptHandler(ctx context.Context, req rhizome.InterruptRequest) (rhizome.InterruptResponse, error) {
	switch req.Kind {
	case InterruptKindAskUser:
		if e.broker == nil {
			return rhizome.InterruptResponse{}, fmt.Errorf("ask_user unavailable: no HITL broker configured")
		}
		payload, ok := req.Payload.(AskUserPayload)
		if !ok {
			return rhizome.InterruptResponse{}, fmt.Errorf("ask_user: expected AskUserPayload, got %T", req.Payload)
		}
		requestID := "graph-ask-" + uuid.Must(uuid.NewV4()).String()
		source := "graph:" + req.Node
		broadcast := func() {
			if e.eventSink != nil {
				e.eventSink.BroadcastPrompt(requestID, payload.Question, payload.Options, source)
			}
		}
		text, err := e.broker.Ask(ctx, requestID, broadcast)
		if err != nil {
			return rhizome.InterruptResponse{}, err
		}
		return rhizome.InterruptResponse{Value: text}, nil
	default:
		return rhizome.InterruptResponse{}, fmt.Errorf("unknown interrupt kind %q", req.Kind)
	}
}

// buildToolExecutor assembles a ToolExecutor scoped to the given workspace
// directory. Mirrors the per-spawn pattern used by runtime.SpawnWorker
// (runtime/runtime.go). CoreTools construction is cheap — no I/O.
func (e *Executor) buildToolExecutor(workspaceDir string) runtime.ToolExecutor {
	coreTools := runtime.NewCoreTools(workspaceDir,
		runtime.WithShell(true),
		runtime.WithStore(e.store),
	)
	if e.mcpManager != nil && len(e.mcpManager.Tools()) > 0 {
		truncating := mcp.NewTruncatingCaller(e.mcpManager, mcp.DefaultMaxResultLen)
		return runtime.NewCompositeTools(coreTools, truncating, mcp.ToRuntimeToolDefs(e.mcpManager.Tools()))
	}
	return coreTools
}

// Execute runs a compiled graph with the given initial state. It applies
// event, persistence, and logging middleware, then updates the task status
// in the database based on the outcome. graphID is carried through to the
// task_completed / task_failed events so the operator's event loop can
// advance to the next ready task.
func (e *Executor) Execute(ctx context.Context, graph *rhizome.CompiledGraph[*TaskState], state *TaskState, graphID string) error {
	// Middleware chain, outermost → innermost:
	//   NodeContext — inject per-node identity + sink into ctx for node bodies
	//   Event       — one start/complete per logical execution (UI)
	//   Persistence — persist outcomes once per logical execution
	//   Retry       — re-invoke on transient errors
	//   Recover     — per-attempt panic safety; Retry sees panics as errors
	//   Timeout     — per-attempt deadline (doc: Timeout inside Retry)
	//   Logging     — slog per attempt
	result, err := graph.Run(ctx, state,
		rhizome.WithMiddleware[*TaskState](
			NodeContextMiddleware(e.eventSink),
			EventMiddleware(e.eventSink),
			PersistenceMiddleware(e.store),
			rhizome.Retry[*TaskState](rhizome.WithMaxAttempts(e.retryAttempts)),
			rhizome.Recover[*TaskState](),
			rhizome.Timeout[*TaskState](e.nodeTimeout),
			LoggingMiddleware(),
		),
		rhizome.WithInterruptHandler[*TaskState](e.interruptHandler),
	)

	// Update task status based on outcome.
	if err != nil {
		slog.Error("graph execution failed",
			"job_id", state.JobID, "task_id", state.TaskID, "error", err)

		if e.store != nil {
			failSummary := fmt.Sprintf("Graph execution failed: %s", err.Error())
			if dbErr := e.store.UpdateTaskStatus(ctx, state.TaskID, db.TaskStatusFailed, failSummary); dbErr != nil {
				slog.Warn("failed to mark task as failed", "task_id", state.TaskID, "error", dbErr)
			}
		}

		if e.eventSink != nil {
			e.eventSink.BroadcastGraphFailed(state.JobID, state.TaskID, err.Error())
			// Advance the operator. Operator-level task_failed event is distinct
			// from the service-level graph.failed broadcast above.
			e.eventSink.BroadcastTaskFailed(state.JobID, state.TaskID, graphID, err.Error())
		}

		return fmt.Errorf("graph execution: %w", err)
	}

	// Success. Guard against a nil result — rhizome returns (nil, nil)
	// for graphs that reach End without running any nodes (defensive;
	// shouldn't happen with our templates).
	if result == nil {
		result = state
	}
	summary := truncateSummary(result.FinalText)

	slog.Info("graph execution completed",
		"job_id", state.JobID, "task_id", state.TaskID, "status", result.Status)

	if e.store != nil {
		if dbErr := e.store.UpdateTaskStatus(ctx, state.TaskID, db.TaskStatusCompleted, summary); dbErr != nil {
			slog.Warn("failed to mark task as completed", "task_id", state.TaskID, "error", dbErr)
		}
	}

	// Determine HasNextTask so the operator knows whether to advance
	// mechanically (next task exists) or consult the scheduler.
	hasNextTask := false
	if e.store != nil {
		if ready, readyErr := e.store.GetReadyTasks(ctx, state.JobID); readyErr == nil {
			hasNextTask = len(ready) > 0
		} else {
			slog.Warn("failed to check ready tasks after graph completion",
				"job_id", state.JobID, "error", readyErr)
		}
	}

	if e.eventSink != nil {
		e.eventSink.BroadcastGraphCompleted(state.JobID, state.TaskID, summary)
		// Advance the operator. Operator-level task_completed event drives
		// assignNextTask.
		e.eventSink.BroadcastTaskCompleted(state.JobID, state.TaskID, graphID, summary, hasNextTask)
	}

	return nil
}

// TaskRequest carries everything needed to execute a task through the graph
// executor. GraphID names the declarative graph to run — required, since the
// hard-coded template path has been retired.
type TaskRequest struct {
	JobID          string
	JobTitle       string
	JobDescription string

	TaskID    string
	TaskTitle string

	// GraphID selects a declarative graph Definition by id from the
	// executor's GraphSource. Required.
	GraphID string

	WorkspaceDir string
	ProviderName string
	Model        string
}

// TaskExecutor is the interface operator uses to dispatch tasks to the graph
// engine. *Executor implements this. Defined here so the TaskRequest shape
// is owned by graphexec — callers import graphexec to build the request.
type TaskExecutor interface {
	ExecuteTask(ctx context.Context, req TaskRequest) error
}

// ExecuteTask resolves the provider, looks up the declarative graph by id,
// compiles it, and runs it. All dispatches go through the YAML-driven
// catalog — there is no hard-coded template fallback.
func (e *Executor) ExecuteTask(ctx context.Context, req TaskRequest) error {
	if req.GraphID == "" {
		return fmt.Errorf("ExecuteTask: graph_id is required")
	}
	prov, ok := e.registry.Get(req.ProviderName)
	if !ok {
		return fmt.Errorf("provider %q not found in registry", req.ProviderName)
	}

	model := req.Model
	if model == "" {
		model = e.defaultModel
	}

	tmplCfg := TemplateConfig{
		Provider:     prov,
		ToolExecutor: e.buildToolExecutor(req.WorkspaceDir),
		Model:        model,
		PromptEngine: e.promptEngine,
	}

	if e.graphs == nil {
		return fmt.Errorf("graph %q requested but no GraphSource configured", req.GraphID)
	}
	def := e.graphs.GraphByID(req.GraphID)
	if def == nil {
		return fmt.Errorf("graph %q not found", req.GraphID)
	}
	graph, err := Compile(def, tmplCfg, e.roles)
	if err != nil {
		return fmt.Errorf("compiling graph %q: %w", req.GraphID, err)
	}

	state := NewTaskState(req.JobID, req.TaskID, req.WorkspaceDir, req.ProviderName, model)
	state.SetArtifact("task.description", req.TaskTitle)
	state.SetArtifact("job.title", req.JobTitle)
	state.SetArtifact("job.description", req.JobDescription)

	return e.Execute(ctx, graph, state, req.GraphID)
}
