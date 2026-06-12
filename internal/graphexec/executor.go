package graphexec

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
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
// graph-dispatch path.
type GraphSource interface {
	GraphByID(id string) *Definition
	// Graphs returns all loaded definitions. Used by query_graphs to
	// surface the catalog to decomposition roles. Order is
	// implementation-defined; callers should not assume stability.
	Graphs() []*Definition
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

	// Worker-default knobs are read on each task dispatch and may be
	// updated live via SetWorkerDefaults when the user changes /settings.
	defaultsMu            sync.RWMutex
	workerThinkingEnabled bool
	workerTemperature     float64

	// In-flight task tracking. Callers run ExecuteTask in detached
	// goroutines; Drain lets shutdown wait for them to persist terminal
	// task status before the database closes underneath them, and the
	// running map lets CancelJob stop a job's executions mid-flight.
	drainMu  sync.Mutex
	draining bool
	taskWG   sync.WaitGroup
	running  map[string]taskHandle // taskID → in-flight handle
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

	// WorkerThinkingEnabled is the initial value of the global thinking
	// default for graph nodes. May be updated live via SetWorkerDefaults.
	WorkerThinkingEnabled bool

	// WorkerTemperature is the initial value of the global sampling
	// temperature default for graph nodes. May be updated live via
	// SetWorkerDefaults.
	WorkerTemperature float64
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
		registry:              cfg.Registry,
		mcpManager:            cfg.MCPManager,
		promptEngine:          cfg.PromptEngine,
		store:                 cfg.Store,
		eventSink:             cfg.EventSink,
		broker:                cfg.Broker,
		graphs:                cfg.Graphs,
		roles:                 roles,
		defaultModel:          cfg.DefaultModel,
		nodeTimeout:           cfg.NodeTimeout,
		retryAttempts:         retries,
		workerThinkingEnabled: cfg.WorkerThinkingEnabled,
		workerTemperature:     cfg.WorkerTemperature,
	}
}

// SetWorkerDefaults updates the global thinking/temperature defaults that
// graph nodes inherit when their role frontmatter doesn't override. Safe to
// call concurrently with ExecuteTask: each task reads a fresh snapshot at
// dispatch time, so updates take effect on the next task.
func (e *Executor) SetWorkerDefaults(thinkingEnabled bool, temperature float64) {
	e.defaultsMu.Lock()
	defer e.defaultsMu.Unlock()
	e.workerThinkingEnabled = thinkingEnabled
	e.workerTemperature = temperature
}

// workerDefaults returns the current global defaults under a read lock.
func (e *Executor) workerDefaults() (bool, float64) {
	e.defaultsMu.RLock()
	defer e.defaultsMu.RUnlock()
	return e.workerThinkingEnabled, e.workerTemperature
}

// taskHandle tracks one in-flight task so CancelJob can stop it.
type taskHandle struct {
	jobID  string
	cancel context.CancelFunc
}

// beginTask registers an in-flight task and derives a per-task cancellable
// context, or fails if the executor is draining. The flag check and
// WaitGroup add happen under one lock so a dispatch can't slip in between
// Drain setting the flag and waiting.
func (e *Executor) beginTask(ctx context.Context, req TaskRequest) (context.Context, error) {
	e.drainMu.Lock()
	defer e.drainMu.Unlock()
	if e.draining {
		return nil, fmt.Errorf("executor is shutting down")
	}
	runCtx, cancel := context.WithCancel(ctx)
	if e.running == nil {
		e.running = make(map[string]taskHandle)
	}
	e.running[req.TaskID] = taskHandle{jobID: req.JobID, cancel: cancel}
	e.taskWG.Add(1)
	return runCtx, nil
}

// endTask releases a task registered by beginTask.
func (e *Executor) endTask(taskID string) {
	e.drainMu.Lock()
	if h, ok := e.running[taskID]; ok {
		h.cancel()
		delete(e.running, taskID)
	}
	e.drainMu.Unlock()
	e.taskWG.Done()
}

// CancelJob cancels every in-flight graph execution belonging to jobID and
// returns how many were cancelled. Each cancelled run persists its task as
// cancelled through Execute's terminal-status path.
func (e *Executor) CancelJob(jobID string) int {
	e.drainMu.Lock()
	defer e.drainMu.Unlock()
	n := 0
	for _, h := range e.running {
		if h.jobID == jobID {
			h.cancel()
			n++
		}
	}
	return n
}

// Drain stops accepting new task dispatches and waits up to timeout for
// in-flight ExecuteTask calls to finish persisting their terminal task
// status. Call after cancelling the context the tasks run under, and
// before closing the store. Returns false if the timeout elapsed first.
func (e *Executor) Drain(timeout time.Duration) bool {
	e.drainMu.Lock()
	e.draining = true
	e.drainMu.Unlock()

	done := make(chan struct{})
	go func() {
		e.taskWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
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
		// Prefer the multi-question round; fall back to the single-question
		// shorthand. The TUI presents them as one form and returns a single
		// combined answer string.
		questions := payload.Questions
		if len(questions) == 0 {
			questions = []PromptQuestion{{Question: payload.Question, Options: payload.Options}}
		}
		requestID := "graph-ask-" + uuid.Must(uuid.NewV4()).String()
		source := "graph:" + req.Node
		// The node's ctx carries the NodeContext (job/task identity) injected by
		// NodeContextMiddleware, so the blocker can name which work it gates.
		var jobID, taskID string
		if nc := NodeContextFromContext(ctx); nc != nil {
			jobID, taskID = nc.JobID, nc.TaskID
		}
		broadcast := func() {
			if e.eventSink != nil {
				e.eventSink.BroadcastPrompt(requestID, questions, source, jobID, taskID)
			}
		}
		// Resolve the blocker once Ask returns, whether the user answered or the
		// node's context was cancelled (task killed / shutdown). Idempotent with
		// any concurrent resolution.
		if e.eventSink != nil {
			defer e.eventSink.ResolveBlocker(requestID)
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
// workspaceBase is the task's canonical workspace: when it differs from
// workspaceDir (fan-out branch isolation), absolute paths under it are
// aliased into workspaceDir so canonical paths leaked into instructions
// and artifacts keep working inside the branch.
func (e *Executor) buildToolExecutor(workspaceDir, workspaceBase string) runtime.ToolExecutor {
	coreOpts := []runtime.CoreToolsOption{
		runtime.WithShell(true),
		runtime.WithStore(e.store),
	}
	if workspaceBase != "" && workspaceBase != workspaceDir {
		coreOpts = append(coreOpts, runtime.WithPathAlias(workspaceBase))
	}
	if e.graphs != nil {
		coreOpts = append(coreOpts, runtime.WithGraphCatalog(graphSourceCatalog{e.graphs}))
	}
	coreTools := runtime.NewCoreTools(workspaceDir, coreOpts...)
	if e.mcpManager != nil && len(e.mcpManager.Tools()) > 0 {
		truncating := mcp.NewTruncatingCaller(e.mcpManager, mcp.DefaultMaxResultLen)
		return runtime.NewCompositeTools(coreTools, truncating, mcp.ToRuntimeToolDefs(e.mcpManager.Tools()))
	}
	return coreTools
}

// graphSourceCatalog adapts a GraphSource to the runtime.GraphCatalog
// interface query_graphs expects. Kept in graphexec so runtime stays
// independent of Definition.
type graphSourceCatalog struct{ src GraphSource }

func (c graphSourceCatalog) Graphs() []runtime.GraphSummary {
	defs := c.src.Graphs()
	out := make([]runtime.GraphSummary, 0, len(defs))
	for _, d := range defs {
		out = append(out, runtime.GraphSummary{
			ID:          d.ID,
			Name:        d.Name,
			Description: d.Description,
			Tags:        d.Tags,
		})
	}
	return out
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

	// Persist the terminal status on a detached context: when the run failed
	// because ctx was cancelled (shutdown, kill), reusing ctx would fail the
	// status write too, stranding the task in_progress forever and wedging
	// the job's serial-dispatch gate behind it.
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelPersist()

	// Update task status based on outcome.
	if err != nil {
		// A run that died because its context was cancelled (job cancel,
		// shutdown) is a deliberate stop, not a failure: persist the task as
		// cancelled and don't ask the operator to react — a task_failed
		// event would prompt it to retry work the user just cancelled.
		cancelled := ctx.Err() != nil

		slog.Error("graph execution ended with error",
			"job_id", state.JobID, "task_id", state.TaskID, "cancelled", cancelled, "error", err)

		if e.store != nil {
			status := db.TaskStatusFailed
			summary := fmt.Sprintf("Graph execution failed: %s", err.Error())
			if cancelled {
				status = db.TaskStatusCancelled
				summary = "Cancelled while running"
			}
			if dbErr := e.store.UpdateTaskStatus(persistCtx, state.TaskID, status, summary); dbErr != nil {
				slog.Warn("failed to persist terminal task status", "task_id", state.TaskID, "error", dbErr)
			}
		}

		if e.eventSink != nil {
			e.eventSink.BroadcastGraphFailed(state.JobID, state.TaskID, err.Error())
			if !cancelled {
				// Advance the operator. Operator-level task_failed event is
				// distinct from the service-level graph.failed broadcast above.
				e.eventSink.BroadcastTaskFailed(state.JobID, state.TaskID, graphID, err.Error())
			}
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
		if dbErr := e.store.UpdateTaskStatus(persistCtx, state.TaskID, db.TaskStatusCompleted, summary); dbErr != nil {
			slog.Warn("failed to mark task as completed", "task_id", state.TaskID, "error", dbErr)
		}
	}

	// Determine HasNextTask so the operator knows whether to advance
	// mechanically (next task exists) or consult the scheduler.
	hasNextTask := false
	if e.store != nil {
		if ready, readyErr := e.store.GetReadyTasks(persistCtx, state.JobID); readyErr == nil {
			hasNextTask = len(ready) > 0
		} else {
			slog.Warn("failed to check ready tasks after graph completion",
				"job_id", state.JobID, "error", readyErr)
		}
	}

	if e.eventSink != nil {
		e.eventSink.BroadcastGraphCompleted(state.JobID, state.TaskID, summary)
		// Advance the operator. Operator-level task_completed event drives
		// assignNextTask. Also carries the exit node's raw JSON output so
		// auto-dispatch consumers (e.g. decomposition) can parse it without
		// re-running the graph.
		exitOutput := exitNodeOutput(result)
		e.eventSink.BroadcastTaskCompleted(state.JobID, state.TaskID, graphID, summary, exitOutput, hasNextTask)
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

	// TaskDescription is the task's contract — what the work entails, as
	// produced by coarse-decompose (or create_task). Surfaced to graph
	// nodes via the `task.description` artifact; when empty, the executor
	// falls back to TaskTitle so prompts never render blank. Without this,
	// workers see only the title and end up asking the user for details
	// the system already has.
	TaskDescription string

	// GraphID selects a declarative graph Definition by id from the
	// executor's GraphSource. Required.
	GraphID string

	// Toolchain names the toolchain id (e.g. "go", "python", "typescript")
	// the task should execute against. Surfaced to graphs via the
	// `task.toolchain` artifact so slot bindings like
	// `slots: { toolchain: "{{ task.toolchain }}" }` resolve.
	// Required for graphs whose roles bind toolchain slots; optional
	// otherwise. Set by fine-decompose; callers without a real source
	// should leave empty rather than defaulting.
	Toolchain string

	// Siblings is a pre-formatted bullet list of other task titles in
	// the same job, excluding this task and any decomposition bootstrap
	// tasks. Empty string is treated as "no siblings"; the executor
	// substitutes a placeholder so role templates that reference
	// `{{ task.siblings }}` always render meaningful text.
	Siblings string

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
//
// Errors raised before the graph runs (provider missing, graph not found,
// compile failure, executor draining) mark the task failed and notify the
// operator, mirroring what Execute does for run failures. Callers dispatch
// ExecuteTask in detached goroutines that only log the returned error, and
// the task was already transitioned to in_progress — without this, an early
// error strands the task there forever and wedges the job's serial gate.
func (e *Executor) ExecuteTask(ctx context.Context, req TaskRequest) error {
	runCtx, err := e.beginTask(ctx, req)
	if err != nil {
		return e.failDispatch(req, err)
	}
	defer e.endTask(req.TaskID)

	graph, state, err := e.prepareTask(req)
	if err != nil {
		return e.failDispatch(req, err)
	}
	return e.Execute(runCtx, graph, state, req.GraphID)
}

// prepareTask validates the request, resolves the provider, and compiles the
// graph, returning the compiled graph and initial state ready for Execute.
func (e *Executor) prepareTask(req TaskRequest) (*rhizome.CompiledGraph[*TaskState], *TaskState, error) {
	if req.GraphID == "" {
		return nil, nil, fmt.Errorf("ExecuteTask: graph_id is required")
	}
	prov, ok := e.registry.Get(req.ProviderName)
	if !ok {
		return nil, nil, fmt.Errorf("provider %q not found in registry", req.ProviderName)
	}

	model := req.Model
	if model == "" {
		model = e.defaultModel
	}

	thinking, temperature := e.workerDefaults()
	tmplCfg := TemplateConfig{
		Provider:              prov,
		ToolExecutorFor:       e.buildToolExecutor,
		Model:                 model,
		PromptEngine:          e.promptEngine,
		Store:                 e.store,
		WorkerThinkingEnabled: thinking,
		WorkerTemperature:     temperature,
	}

	if e.graphs == nil {
		return nil, nil, fmt.Errorf("graph %q requested but no GraphSource configured", req.GraphID)
	}
	def := e.graphs.GraphByID(req.GraphID)
	if def == nil {
		return nil, nil, fmt.Errorf("graph %q not found", req.GraphID)
	}
	graph, err := Compile(def, tmplCfg, e.roles)
	if err != nil {
		return nil, nil, fmt.Errorf("compiling graph %q: %w", req.GraphID, err)
	}

	state := NewTaskState(req.JobID, req.TaskID, req.WorkspaceDir, req.ProviderName, model)
	state.SetArtifact("task.title", req.TaskTitle)
	desc := req.TaskDescription
	if desc == "" {
		desc = req.TaskTitle
	}
	state.SetArtifact("task.description", desc)
	if req.Toolchain != "" {
		state.SetArtifact("task.toolchain", req.Toolchain)
	}
	state.SetArtifact("task.siblings", siblingsArtifact(req.Siblings))
	state.SetArtifact("job.title", req.JobTitle)
	state.SetArtifact("job.description", req.JobDescription)
	state.ExitNode = def.Exit

	return graph, state, nil
}

// failDispatch marks a task failed and notifies the operator when the task
// couldn't even start. Uses a detached context so the failure is persisted
// even when dispatch happens during shutdown. Returns taskErr for the caller
// to propagate.
func (e *Executor) failDispatch(req TaskRequest, taskErr error) error {
	slog.Error("task dispatch failed",
		"job_id", req.JobID, "task_id", req.TaskID, "graph_id", req.GraphID, "error", taskErr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if e.store != nil {
		summary := fmt.Sprintf("Dispatch failed: %s", taskErr.Error())
		if dbErr := e.store.UpdateTaskStatus(ctx, req.TaskID, db.TaskStatusFailed, summary); dbErr != nil {
			slog.Warn("failed to mark task as failed after dispatch error",
				"task_id", req.TaskID, "error", dbErr)
		}
	}
	if e.eventSink != nil {
		e.eventSink.BroadcastGraphFailed(req.JobID, req.TaskID, taskErr.Error())
		e.eventSink.BroadcastTaskFailed(req.JobID, req.TaskID, req.GraphID, taskErr.Error())
	}
	return taskErr
}

// exitNodeOutput returns the raw JSON output of the graph's exit node, or
// nil when no exit node is recorded or the node produced no output. Nodes
// that produce their output through the NodeContext middleware key into
// NodeOutputs under the rhizome node id, matching Definition.Exit.
func exitNodeOutput(state *TaskState) json.RawMessage {
	if state == nil || state.ExitNode == "" {
		return nil
	}
	return state.GetNodeOutput(state.ExitNode)
}
