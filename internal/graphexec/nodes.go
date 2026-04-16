package graphexec

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jefflinse/rhizome"
	"github.com/jefflinse/toasters/internal/runtime"
)

// Tool name constants for building allowlists.
const (
	ToolReadFile  = "read_file"
	ToolWriteFile = "write_file"
	ToolEditFile  = "edit_file"
	ToolGlob      = "glob"
	ToolGrep      = "grep"
	ToolShell     = "shell"
	ToolWebFetch  = "web_fetch"
)

// Common tool sets for node builders.
var (
	// ReadOnlyTools allows only non-mutating tools.
	ReadOnlyTools = []string{ToolReadFile, ToolGlob, ToolGrep}

	// WriteTools allows mutation plus reading.
	WriteTools = []string{ToolReadFile, ToolWriteFile, ToolEditFile, ToolGlob, ToolGrep, ToolShell}

	// TestTools allows running tests.
	TestTools = []string{ToolReadFile, ToolGlob, ToolGrep, ToolShell}
)

// filteredExecutor wraps a ToolExecutor and restricts it to a named subset.
type filteredExecutor struct {
	inner   runtime.ToolExecutor
	allowed map[string]bool
}

// FilterTools creates a ToolExecutor that only exposes the named tools
// from the inner executor. This is the graphexec equivalent of
// runtime.filteredToolExecutor (which is unexported).
func FilterTools(inner runtime.ToolExecutor, allowed []string) runtime.ToolExecutor {
	m := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		m[name] = true
	}
	return &filteredExecutor{inner: inner, allowed: m}
}

func (f *filteredExecutor) Definitions() []runtime.ToolDef {
	all := f.inner.Definitions()
	filtered := make([]runtime.ToolDef, 0, len(f.allowed))
	for _, td := range all {
		if f.allowed[td.Name] {
			filtered = append(filtered, td)
		}
	}
	return filtered
}

func (f *filteredExecutor) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if !f.allowed[name] {
		// Return ErrUnknownTool so mergeTools can fall through to sibling
		// executors (e.g. decision tools) — matches the runtime convention
		// used by layeredToolExecutor.
		return "", fmt.Errorf("%w: %s", runtime.ErrUnknownTool, name)
	}
	return f.inner.Execute(ctx, name, args)
}

// --- Specialized node builders ---
//
// Each builder wraps LLMNode with a focused tool set and a system prompt
// composed by the prompt engine from role markdown (defaults/user/roles/).
// Overrides are built from accumulated TaskState artifacts so each role
// sees the structured context it needs — findings, plan, feedback, etc.

// composePrompt resolves a role's system prompt via the prompt engine, passing
// TaskState artifacts as overrides. The role template references these as
// {{ globals.task.description }} etc. Falls back to a minimal prompt if the
// engine is unset (test path) or the role is unknown.
func composePrompt(cfg TemplateConfig, roleName string, state *TaskState) (string, error) {
	if cfg.PromptEngine == nil {
		// Test fallback — acceptable because tests typically don't drive
		// the LLM on prompt content.
		return fmt.Sprintf("You are the %s. Task: %s", roleName, state.GetArtifactString("task.description")), nil
	}
	overrides := map[string]string{
		"task.description":     state.GetArtifactString("task.description"),
		"job.title":            state.GetArtifactString("job.title"),
		"job.description":      state.GetArtifactString("job.description"),
		"investigate.findings": state.GetArtifactString("investigate.findings"),
		"plan.steps":           state.GetArtifactString("plan.steps"),
		"implement.summary":    state.GetArtifactString("implement.summary"),
		"test.results":         state.GetArtifactString("test.results"),
		"review.feedback":      state.GetArtifactString("review.feedback"),
	}
	return cfg.PromptEngine.Compose(roleName, overrides)
}

// InvestigateNodeDynamic explores the codebase with read-only tools and
// writes findings to "investigate.findings".
func InvestigateNodeDynamic(cfg TemplateConfig) rhizome.NodeFunc[*TaskState] {
	roles := cfg.Roles.resolve()
	return func(ctx context.Context, state *TaskState) (*TaskState, error) {
		sysPrompt, err := composePrompt(cfg, roles.Investigate, state)
		if err != nil {
			return state, fmt.Errorf("composing investigator prompt: %w", err)
		}
		node := LLMNode(NodeConfig{
			Provider:     cfg.Provider,
			ToolExecutor: FilterTools(cfg.ToolExecutor, ReadOnlyTools),
			Model:        cfg.Model,
			ArtifactKey:  "investigate.findings",
			MaxTurns:     DefaultMaxTurns,
			SystemPrompt: sysPrompt,
		})
		return node(ctx, state)
	}
}

// PlanNodeDynamic reads investigation findings and writes an implementation
// plan to "plan.steps".
func PlanNodeDynamic(cfg TemplateConfig) rhizome.NodeFunc[*TaskState] {
	roles := cfg.Roles.resolve()
	return func(ctx context.Context, state *TaskState) (*TaskState, error) {
		sysPrompt, err := composePrompt(cfg, roles.Plan, state)
		if err != nil {
			return state, fmt.Errorf("composing planner prompt: %w", err)
		}
		node := LLMNode(NodeConfig{
			Provider:     cfg.Provider,
			ToolExecutor: FilterTools(cfg.ToolExecutor, ReadOnlyTools),
			Model:        cfg.Model,
			ArtifactKey:  "plan.steps",
			MaxTurns:     DefaultMaxTurns,
			SystemPrompt: sysPrompt,
		})
		return node(ctx, state)
	}
}

// ImplementNodeDynamic reads the plan (and optional review feedback) and
// makes code changes. Output goes to "implement.summary".
func ImplementNodeDynamic(cfg TemplateConfig) rhizome.NodeFunc[*TaskState] {
	roles := cfg.Roles.resolve()
	return func(ctx context.Context, state *TaskState) (*TaskState, error) {
		sysPrompt, err := composePrompt(cfg, roles.Implement, state)
		if err != nil {
			return state, fmt.Errorf("composing implementer prompt: %w", err)
		}
		node := LLMNode(NodeConfig{
			Provider:     cfg.Provider,
			ToolExecutor: FilterTools(cfg.ToolExecutor, WriteTools),
			Model:        cfg.Model,
			ArtifactKey:  "implement.summary",
			MaxTurns:     DefaultMaxTurns,
			SystemPrompt: sysPrompt,
		})
		return node(ctx, state)
	}
}

// TestNodeDynamic runs tests and records the outcome via decision tools.
// The LLM must call decide_tests_passed or decide_tests_failed to advance
// the graph — substring parsing was dropped because it was fragile against
// multi-line test output.
func TestNodeDynamic(cfg TemplateConfig) rhizome.NodeFunc[*TaskState] {
	roles := cfg.Roles.resolve()
	return func(ctx context.Context, state *TaskState) (*TaskState, error) {
		sysPrompt, err := composePrompt(cfg, roles.Test, state)
		if err != nil {
			return state, fmt.Errorf("composing tester prompt: %w", err)
		}
		decision := newDecisionExecutor(state,
			DecisionOutcome{
				ToolName:    "decide_tests_passed",
				Description: "Record that tests passed. Call this tool once you have confirmed the relevant tests all pass.",
				Status:      "tests_passed",
				ArtifactKey: "test.results",
				ArgField:    "summary",
			},
			DecisionOutcome{
				ToolName:    "decide_tests_failed",
				Description: "Record that tests failed. Call this tool if any relevant test did not pass. Include the failing output in the summary so the next implementation round can address it.",
				Status:      "tests_failed",
				ArtifactKey: "test.results",
				ArgField:    "summary",
			},
		)
		tools := mergeTools(decision, FilterTools(cfg.ToolExecutor, TestTools))

		node := LLMNode(NodeConfig{
			Provider:      cfg.Provider,
			ToolExecutor:  tools,
			Model:         cfg.Model,
			MaxTurns:      DefaultMaxTurns,
			SystemPrompt:  sysPrompt,
			TerminalTools: []string{"decide_tests_passed", "decide_tests_failed"},
		})
		state, err = node(ctx, state)
		if err != nil {
			return state, err
		}
		if !decision.Decided() {
			return state, fmt.Errorf("test node ended without calling decide_tests_passed or decide_tests_failed")
		}
		return state, nil
	}
}

// ReviewNodeDynamic reviews the implementation against the plan. The LLM
// must call decide_approved or decide_rejected to advance the graph —
// substring parsing was dropped because "not approved" matched "approved"
// and routed rejected work to End.
func ReviewNodeDynamic(cfg TemplateConfig) rhizome.NodeFunc[*TaskState] {
	roles := cfg.Roles.resolve()
	return func(ctx context.Context, state *TaskState) (*TaskState, error) {
		sysPrompt, err := composePrompt(cfg, roles.Review, state)
		if err != nil {
			return state, fmt.Errorf("composing reviewer prompt: %w", err)
		}
		decision := newDecisionExecutor(state,
			DecisionOutcome{
				ToolName:    "decide_approved",
				Description: "Record that the implementation is approved. Call this tool when the work satisfies the plan and needs no further revision.",
				Status:      "review_approved",
				ArtifactKey: "review.feedback",
				ArgField:    "reason",
			},
			DecisionOutcome{
				ToolName:    "decide_rejected",
				Description: "Record that the implementation is rejected. Call this tool with concrete feedback when the work needs revision — the feedback will be passed to the next implementation round.",
				Status:      "review_rejected",
				ArtifactKey: "review.feedback",
				ArgField:    "feedback",
			},
		)
		tools := mergeTools(decision, FilterTools(cfg.ToolExecutor, ReadOnlyTools))

		node := LLMNode(NodeConfig{
			Provider:      cfg.Provider,
			ToolExecutor:  tools,
			Model:         cfg.Model,
			MaxTurns:      DefaultMaxTurns,
			SystemPrompt:  sysPrompt,
			TerminalTools: []string{"decide_approved", "decide_rejected"},
		})
		state, err = node(ctx, state)
		if err != nil {
			return state, err
		}
		if !decision.Decided() {
			return state, fmt.Errorf("review node ended without calling decide_approved or decide_rejected")
		}
		return state, nil
	}
}

// Compile-time interface check.
var _ runtime.ToolExecutor = (*filteredExecutor)(nil)
