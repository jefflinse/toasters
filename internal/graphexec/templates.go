package graphexec

import (
	"context"

	"github.com/jefflinse/rhizome"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// TemplateConfig holds the shared configuration for building graph templates.
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

// SingleWorkerGraph builds a minimal Start -> work -> End graph.
// This is the Phase 1 proof-of-concept: a single LLM node that receives
// a task description and executes it with the full tool set.
func SingleWorkerGraph(cfg TemplateConfig, systemPrompt, initialMessage string) (*rhizome.CompiledGraph[*TaskState], error) {
	g := rhizome.New[*TaskState]()

	if err := g.AddNode("work", LLMNode(NodeConfig{
		Provider:       cfg.Provider,
		ToolExecutor:   cfg.ToolExecutor,
		SystemPrompt:   systemPrompt,
		InitialMessage: initialMessage,
		Model:          cfg.Model,
		ArtifactKey:    "work.output",
	})); err != nil {
		return nil, err
	}

	if err := g.AddEdge(rhizome.Start, "work"); err != nil {
		return nil, err
	}
	if err := g.AddEdge("work", rhizome.End); err != nil {
		return nil, err
	}

	return g.Compile()
}

// BugFixGraph builds a multi-node graph for bug fix tasks:
//
//	Start -> investigate -> plan -> implement -> test -> review -> End
//	                                    ^                  |
//	                                    |  (review_rejected)|
//	                                    +------------------+
//
// The review node routes back to implement on rejection, creating a
// revision cycle capped at 3 iterations via WithMaxNodeExecs.
func BugFixGraph(cfg TemplateConfig) (*rhizome.CompiledGraph[*TaskState], error) {
	g := rhizome.New[*TaskState]()

	if err := g.AddNode("investigate", InvestigateNodeDynamic(cfg)); err != nil {
		return nil, err
	}
	if err := g.AddNode("plan", PlanNodeDynamic(cfg)); err != nil {
		return nil, err
	}
	if err := g.AddNode("implement", ImplementNodeDynamic(cfg)); err != nil {
		return nil, err
	}
	if err := g.AddNode("test", TestNodeDynamic(cfg)); err != nil {
		return nil, err
	}
	if err := g.AddNode("review", ReviewNodeDynamic(cfg)); err != nil {
		return nil, err
	}

	// Linear edges: Start -> investigate -> plan -> implement -> test.
	if err := g.AddEdge(rhizome.Start, "investigate"); err != nil {
		return nil, err
	}
	if err := g.AddEdge("investigate", "plan"); err != nil {
		return nil, err
	}
	if err := g.AddEdge("plan", "implement"); err != nil {
		return nil, err
	}
	if err := g.AddEdge("implement", "test"); err != nil {
		return nil, err
	}

	// Conditional: test -> review (if passed) or -> implement (if failed).
	if err := g.AddConditionalEdge("test", func(_ context.Context, s *TaskState) (string, error) {
		if s.Status == "tests_passed" {
			return "review", nil
		}
		return "implement", nil // tests failed — retry implementation
	}, "review", "implement"); err != nil {
		return nil, err
	}

	// Conditional: review -> End (if approved) or -> implement (if rejected).
	if err := g.AddConditionalEdge("review", func(_ context.Context, s *TaskState) (string, error) {
		if s.Status == "review_approved" {
			return rhizome.End, nil
		}
		return "implement", nil // review rejected — revise
	}, rhizome.End, "implement"); err != nil {
		return nil, err
	}

	return g.Compile(rhizome.WithMaxNodeExecs(3))
}

// NewFeatureGraph builds a graph for new feature tasks:
//
//	Start -> plan -> implement -> test -> review -> End
//	                    ^                    |
//	                    |  (review_rejected)  |
//	                    +--------------------+
//
// Skips the investigation phase (the task description is assumed to be
// sufficient for planning). Otherwise similar to BugFixGraph.
func NewFeatureGraph(cfg TemplateConfig) (*rhizome.CompiledGraph[*TaskState], error) {
	g := rhizome.New[*TaskState]()

	if err := g.AddNode("plan", PlanNodeDynamic(cfg)); err != nil {
		return nil, err
	}
	if err := g.AddNode("implement", ImplementNodeDynamic(cfg)); err != nil {
		return nil, err
	}
	if err := g.AddNode("test", TestNodeDynamic(cfg)); err != nil {
		return nil, err
	}
	if err := g.AddNode("review", ReviewNodeDynamic(cfg)); err != nil {
		return nil, err
	}

	if err := g.AddEdge(rhizome.Start, "plan"); err != nil {
		return nil, err
	}
	if err := g.AddEdge("plan", "implement"); err != nil {
		return nil, err
	}
	if err := g.AddEdge("implement", "test"); err != nil {
		return nil, err
	}

	if err := g.AddConditionalEdge("test", func(_ context.Context, s *TaskState) (string, error) {
		if s.Status == "tests_passed" {
			return "review", nil
		}
		return "implement", nil
	}, "review", "implement"); err != nil {
		return nil, err
	}

	if err := g.AddConditionalEdge("review", func(_ context.Context, s *TaskState) (string, error) {
		if s.Status == "review_approved" {
			return rhizome.End, nil
		}
		return "implement", nil
	}, rhizome.End, "implement"); err != nil {
		return nil, err
	}

	return g.Compile(rhizome.WithMaxNodeExecs(3))
}

// PrototypeGraph builds a lightweight graph for prototyping:
//
//	Start -> implement -> test -> End (if passed)
//	            ^            |
//	            |  (failed)  |
//	            +------------+
//
// No investigation, planning, or review — just implement and test
// with a retry loop. Good for quick iterations.
func PrototypeGraph(cfg TemplateConfig) (*rhizome.CompiledGraph[*TaskState], error) {
	g := rhizome.New[*TaskState]()

	if err := g.AddNode("implement", ImplementNodeDynamic(cfg)); err != nil {
		return nil, err
	}
	if err := g.AddNode("test", TestNodeDynamic(cfg)); err != nil {
		return nil, err
	}

	if err := g.AddEdge(rhizome.Start, "implement"); err != nil {
		return nil, err
	}
	if err := g.AddEdge("implement", "test"); err != nil {
		return nil, err
	}

	if err := g.AddConditionalEdge("test", func(_ context.Context, s *TaskState) (string, error) {
		if s.Status == "tests_passed" {
			return rhizome.End, nil
		}
		return "implement", nil
	}, rhizome.End, "implement"); err != nil {
		return nil, err
	}

	return g.Compile(rhizome.WithMaxNodeExecs(3))
}
