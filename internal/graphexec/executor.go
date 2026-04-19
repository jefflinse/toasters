package graphexec

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gofrs/uuid/v5"
	"github.com/jefflinse/rhizome"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/hitl"
	"github.com/jefflinse/toasters/internal/mcp"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// Executor wraps rhizome graph execution with toasters infrastructure.
// It resolves providers, applies middleware for events and persistence,
// and updates task status after execution.
type Executor struct {
	registry     *provider.Registry
	mcpManager   *mcp.Manager
	promptEngine *prompt.Engine
	store        db.Store
	eventSink    EventSink
	broker       *hitl.Broker
	defaultModel string
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
}

// NewExecutor creates an Executor with the given configuration.
func NewExecutor(cfg ExecutorConfig) *Executor {
	return &Executor{
		registry:     cfg.Registry,
		mcpManager:   cfg.MCPManager,
		promptEngine: cfg.PromptEngine,
		store:        cfg.Store,
		eventSink:    cfg.EventSink,
		broker:       cfg.Broker,
		defaultModel: cfg.DefaultModel,
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
// in the database based on the outcome. teamID is carried through to the
// task_completed / task_failed events so the operator's event loop can
// advance to the next ready task.
func (e *Executor) Execute(ctx context.Context, graph *rhizome.CompiledGraph[*TaskState], state *TaskState, teamID string) error {
	// Run the graph with middleware + interrupt handler. The interrupt
	// handler translates rhizome.Interrupt calls from nodes into HITL
	// broker Asks, so ask_user on a graph node and ask_user on the
	// operator both end up in the same TUI prompt modal.
	result, err := graph.Run(ctx, state,
		rhizome.WithMiddleware[*TaskState](
			LoggingMiddleware(),
			EventMiddleware(e.eventSink),
			PersistenceMiddleware(e.store),
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
			e.eventSink.BroadcastTaskFailed(state.JobID, state.TaskID, teamID, err.Error())
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
		// Advance the operator. Operator-level task_completed event is what
		// drives assignNextTask — the team-lead path emits this via
		// team_tools.completeTask.
		e.eventSink.BroadcastTaskCompleted(state.JobID, state.TaskID, teamID, summary, hasNextTask)
	}

	return nil
}

// TaskRequest carries everything needed to execute a task through the graph
// executor. Replaces the prior 11-positional-string signature which was
// error-prone at call sites.
type TaskRequest struct {
	JobID          string
	JobType        JobType
	JobTitle       string
	JobDescription string

	TaskID    string
	TaskTitle string
	TeamID    string

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

// ExecuteTask builds the appropriate graph template based on req.JobType,
// resolves the provider, and runs it.
func (e *Executor) ExecuteTask(ctx context.Context, req TaskRequest) error {
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
		Roles:        DefaultRoles(),
	}

	graph, err := buildGraphForJobType(req.JobType, tmplCfg, req)
	if err != nil {
		return fmt.Errorf("building graph for job type %q: %w", req.JobType, err)
	}

	state := NewTaskState(req.JobID, req.TaskID, req.WorkspaceDir, req.ProviderName, model)
	state.SetArtifact("task.description", req.TaskTitle)
	state.SetArtifact("job.title", req.JobTitle)
	state.SetArtifact("job.description", req.JobDescription)

	return e.Execute(ctx, graph, state, req.TeamID)
}

// buildGraphForJobType picks the template. Untyped jobs default to BugFixGraph
// so the full investigate → plan → implement → test → review cycle runs —
// SingleWorkerGraph would bypass everything rhizome adds.
func buildGraphForJobType(jobType JobType, tmplCfg TemplateConfig, req TaskRequest) (*rhizome.CompiledGraph[*TaskState], error) {
	switch jobType {
	case JobTypeNewFeature:
		return NewFeatureGraph(tmplCfg)
	case JobTypePrototype:
		return PrototypeGraph(tmplCfg)
	case JobTypeSingleWorker:
		return SingleWorkerGraph(tmplCfg,
			"You are a general-purpose worker. Complete the assigned task.",
			fmt.Sprintf("Task: %s\n\nJob: %s\n%s", req.TaskTitle, req.JobTitle, req.JobDescription),
		)
	case JobTypeBugFix, JobTypeUnset:
		return BugFixGraph(tmplCfg)
	default:
		return BugFixGraph(tmplCfg)
	}
}
