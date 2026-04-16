package graphexec

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jefflinse/rhizome"
	"github.com/jefflinse/toasters/internal/db"
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
		defaultModel: cfg.DefaultModel,
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
// in the database based on the outcome.
func (e *Executor) Execute(ctx context.Context, graph *rhizome.CompiledGraph[*TaskState], state *TaskState) error {
	// Run the graph with middleware.
	result, err := graph.Run(ctx, state,
		rhizome.WithMiddleware[*TaskState](
			LoggingMiddleware(),
			EventMiddleware(e.eventSink),
			PersistenceMiddleware(e.store),
		),
	)

	// Update task status based on outcome.
	if err != nil {
		slog.Error("graph execution failed",
			"job_id", state.JobID, "task_id", state.TaskID, "error", err)

		if e.eventSink != nil {
			e.eventSink.BroadcastGraphFailed(state.JobID, state.TaskID, err.Error())
		}

		if e.store != nil {
			failSummary := fmt.Sprintf("Graph execution failed: %s", err.Error())
			if dbErr := e.store.UpdateTaskStatus(ctx, state.TaskID, db.TaskStatusFailed, failSummary); dbErr != nil {
				slog.Warn("failed to mark task as failed", "task_id", state.TaskID, "error", dbErr)
			}
		}

		return fmt.Errorf("graph execution: %w", err)
	}

	// Success.
	summary := ""
	if result != nil {
		summary = result.FinalText
		if len(summary) > 500 {
			summary = summary[:500] + "..."
		}
	}

	slog.Info("graph execution completed",
		"job_id", state.JobID, "task_id", state.TaskID, "status", result.Status)

	if e.eventSink != nil {
		e.eventSink.BroadcastGraphCompleted(state.JobID, state.TaskID, summary)
	}

	if e.store != nil {
		if dbErr := e.store.UpdateTaskStatus(ctx, state.TaskID, db.TaskStatusCompleted, summary); dbErr != nil {
			slog.Warn("failed to mark task as completed", "task_id", state.TaskID, "error", dbErr)
		}
	}

	return nil
}

// ExecuteTask is a convenience method that builds the appropriate graph
// template based on job type, resolves the provider, and runs it.
func (e *Executor) ExecuteTask(ctx context.Context, jobType, jobID, taskID, taskTitle, jobTitle, jobDescription, workspaceDir, providerName, model string) error {
	// Resolve provider.
	prov, ok := e.registry.Get(providerName)
	if !ok {
		return fmt.Errorf("provider %q not found in registry", providerName)
	}

	if model == "" {
		model = e.defaultModel
	}

	tmplCfg := TemplateConfig{
		Provider:     prov,
		ToolExecutor: e.buildToolExecutor(workspaceDir),
		Model:        model,
		PromptEngine: e.promptEngine,
		Roles:        DefaultRoles(),
	}

	// Select graph template by job type.
	var graph *rhizome.CompiledGraph[*TaskState]
	var err error
	switch jobType {
	case "bug_fix":
		graph, err = BugFixGraph(tmplCfg)
	case "new_feature":
		graph, err = NewFeatureGraph(tmplCfg)
	case "prototype":
		graph, err = PrototypeGraph(tmplCfg)
	default:
		// Default to single worker graph for unknown types.
		graph, err = SingleWorkerGraph(tmplCfg,
			"You are a general-purpose worker. Complete the assigned task.",
			fmt.Sprintf("Task: %s\n\nJob: %s\n%s", taskTitle, jobTitle, jobDescription),
		)
	}
	if err != nil {
		return fmt.Errorf("building graph for job type %q: %w", jobType, err)
	}

	// Build initial state.
	state := NewTaskState(jobID, taskID, workspaceDir, providerName, model)
	state.SetArtifact("task.description", taskTitle)
	state.SetArtifact("job.title", jobTitle)
	state.SetArtifact("job.description", jobDescription)

	return e.Execute(ctx, graph, state)
}
