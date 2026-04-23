package graphexec

import (
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// TemplateConfig holds the shared configuration every compiled graph needs:
// the LLM provider, the per-workspace tool executor, the default model, the
// prompt engine that composes role system prompts, and the role name map.
// The declarative compiler passes this into each node's builder.
type TemplateConfig struct {
	// Provider is the LLM provider for all nodes.
	Provider provider.Provider

	// ToolExecutor is the base tool executor (typically CompositeTools).
	ToolExecutor runtime.ToolExecutor

	// Model is the default model for all nodes.
	Model string

	// PromptEngine composes each node's system prompt from role markdown
	// definitions (defaults/user/roles/*.md). When nil, nodes fall back to
	// hardcoded prompts — useful only in tests.
	PromptEngine *prompt.Engine

	// Roles maps graph phase names to role names the prompt engine will
	// compose. Zero value uses DefaultRoles().
	Roles RoleMap
}

// RoleMap assigns a role name to each graph phase. Role names must match a
// role file in the prompt engine (e.g. defaults/user/roles/investigator.md).
type RoleMap struct {
	Investigate string
	Plan        string
	Implement   string
	Test        string
	Review      string
}

// DefaultRoles returns the standard role mapping used when TemplateConfig.Roles
// is left zero. Each name corresponds to a markdown file in defaults/user/roles/.
func DefaultRoles() RoleMap {
	return RoleMap{
		Investigate: "investigator",
		Plan:        "planner",
		Implement:   "implementer",
		Test:        "tester",
		Review:      "reviewer",
	}
}

// resolve returns r with any empty fields filled from DefaultRoles().
func (r RoleMap) resolve() RoleMap {
	d := DefaultRoles()
	if r.Investigate == "" {
		r.Investigate = d.Investigate
	}
	if r.Plan == "" {
		r.Plan = d.Plan
	}
	if r.Implement == "" {
		r.Implement = d.Implement
	}
	if r.Test == "" {
		r.Test = d.Test
	}
	if r.Review == "" {
		r.Review = d.Review
	}
	return r
}
