package graphexec

import (
	"github.com/jefflinse/rhizome"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// TemplateConfig holds the shared configuration every compiled graph needs:
// the LLM provider, the per-workspace tool executor, the default model, and
// the prompt engine that composes role system prompts. The declarative
// compiler passes this into each node's builder.
type TemplateConfig struct {
	// Provider is the LLM provider for all nodes.
	Provider provider.Provider

	// ToolExecutor is a fixed tool executor used only when ToolExecutorFor is
	// nil — mainly for tests and callers that don't need per-workspace
	// scoping. Production sets ToolExecutorFor instead.
	ToolExecutor runtime.ToolExecutor

	// ToolExecutorFor builds a tool executor scoped to a workspace directory.
	// When non-nil it takes precedence over ToolExecutor: each RoleNode
	// resolves its tools via ToolExecutorFor(state.WorkspaceDir,
	// state.WorkspaceBase). This is what lets a fanout branch operating in an
	// isolated workspace get tools pointed at that workspace rather than the
	// task's shared one. workspaceBase is the task's canonical workspace;
	// when it differs from workspaceDir (fan-out branch), absolute paths
	// under it are aliased into workspaceDir so leaked canonical paths in
	// instructions and artifacts keep working inside the branch. Production
	// wires this to Executor.buildToolExecutor.
	ToolExecutorFor func(workspaceDir, workspaceBase, source string) runtime.ToolExecutor

	// Model is the default model for all nodes.
	Model string

	// PromptEngine composes each node's system prompt from role markdown
	// definitions (defaults/user/roles/*.md) and resolves a role's declared
	// output schema. Required in production; node builders surface a clear
	// error when nil at run time.
	PromptEngine *prompt.Engine

	// Store persists each node's LLM conversation to `worker_sessions` +
	// `session_messages` so post-hoc debugging (including failures where
	// the model never called `complete`) can read the full transcript via
	// SQLite. Optional — when nil, graph nodes skip persistence.
	Store db.Store

	// CheckpointStore, when non-nil, makes Compile enable rhizome
	// checkpointing (WithCheckpointing) so graph state is persisted after
	// every node and the run can resume after a crash. Nil disables it.
	CheckpointStore rhizome.CheckpointStore

	// WorkerThinkingEnabled is the default value of the per-request thinking
	// toggle for graph nodes. Roles may override via their frontmatter.
	WorkerThinkingEnabled bool

	// WorkerTemperature is the default sampling temperature for graph
	// nodes. Roles may override via their frontmatter.
	WorkerTemperature float64

	// TemperatureOverride and ThinkingOverride, when non-nil, force the
	// sampling temperature / reasoning toggle with the highest precedence —
	// above role frontmatter and the graph-wide default. Set per fan-out
	// branch (from FanoutBranch overrides) so the same role can run at
	// different temperatures across branches. nil leaves normal resolution.
	TemperatureOverride *float64
	ThinkingOverride    *bool

	// ContextWindows resolves the effective context window for a node
	// session's provider/model, for the worker_sessions.context_window
	// column recorded at session close. Optional — nil leaves that column
	// at 0 ("unresolved").
	ContextWindows runtime.ContextWindowSource
}
