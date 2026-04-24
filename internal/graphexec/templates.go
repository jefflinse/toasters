package graphexec

import (
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

	// ToolExecutor is the base tool executor (typically CompositeTools).
	ToolExecutor runtime.ToolExecutor

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
}
