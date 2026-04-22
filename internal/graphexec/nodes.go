package graphexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jefflinse/mycelium/agent"
	"github.com/jefflinse/rhizome"

	"github.com/jefflinse/toasters/internal/provider"
)

// --- Specialized node builders ---
//
// Each builder wraps a bounded mycelium.agent.Run with a focused tool set,
// a role-composed system prompt, and a typed output schema. The model is
// required to end each node by calling `complete` with a payload conforming
// to the role's schema; the node's apply step folds the typed output into
// TaskState so conditional edges can route.

// composePrompt resolves a role's system prompt via the prompt engine,
// passing TaskState artifacts as overrides. The role template references
// these as {{ globals.task.description }} etc. Falls back to a minimal
// prompt if the engine is unset (test path) or the role is unknown.
func composePrompt(cfg TemplateConfig, roleName string, state *TaskState) (string, error) {
	if cfg.PromptEngine == nil {
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

// buildInitialMessage constructs a user message from TaskState artifacts.
// Individual node builders may override it when they want stricter framing.
func buildInitialMessage(state *TaskState) string {
	var parts []string

	if desc := state.GetArtifactString("task.description"); desc != "" {
		parts = append(parts, fmt.Sprintf("Task: %s", desc))
	}
	if state.WorkspaceDir != "" {
		parts = append(parts, fmt.Sprintf("Workspace: %s", state.WorkspaceDir))
	}
	for key, val := range state.Artifacts {
		if key == "task.description" {
			continue
		}
		if s, ok := val.(string); ok && s != "" {
			parts = append(parts, fmt.Sprintf("## %s\n%s", key, s))
		}
	}
	if len(parts) == 0 {
		return "Please complete the assigned task."
	}
	return strings.Join(parts, "\n\n")
}

// onEventSink returns an agent OnEvent handler that broadcasts streaming
// text and reasoning chunks to the EventSink attached to the current
// NodeContext, if any. No-op when no sink is configured — tests and
// library-only uses pay nothing.
func onEventSink(ctx context.Context) func(agent.Event) {
	nc := NodeContextFromContext(ctx)
	if nc == nil || nc.Sink == nil {
		return nil
	}
	return func(ev agent.Event) {
		switch ev.Kind {
		case agent.EventKindText:
			if ev.Text != "" {
				nc.Sink.BroadcastSessionText(nc.SessionID, ev.Text)
			}
		case agent.EventKindReasoning:
			// Reasoning is rendered separately by the TUI — prefix it so
			// the existing session-text pipeline can distinguish if it
			// ever grows a real channel for reasoning. For now, just log
			// it into the stream so it appears in live output.
			if ev.Text != "" {
				nc.Sink.BroadcastSessionText(nc.SessionID, ev.Text)
			}
		}
	}
}

// runNode is the shared inner loop for every role node. It composes the
// prompt, runs a typed agent.Run, forwards streaming events, and surfaces
// the typed result + status string for the caller's apply step.
func runNode[O any](
	ctx context.Context,
	cfg TemplateConfig,
	roleName string,
	state *TaskState,
	tools []agent.Tool,
	schema json.RawMessage,
) (agent.Result[O], error) {
	sysPrompt, err := composePrompt(cfg, roleName, state)
	if err != nil {
		return agent.Result[O]{}, fmt.Errorf("composing %s prompt: %w", roleName, err)
	}
	return agent.Run(ctx, agent.Config[O]{
		Provider:     cfg.Provider,
		Model:        cfg.Model,
		System:       sysPrompt,
		Messages:     []provider.Message{{Role: "user", Content: buildInitialMessage(state)}},
		Tools:        tools,
		OutputSchema: schema,
		OnEvent:      onEventSink(ctx),
	})
}

// InvestigateNodeDynamic explores the codebase with read-only tools and
// writes findings to "investigate.findings".
func InvestigateNodeDynamic(cfg TemplateConfig) rhizome.NodeFunc[*TaskState] {
	roles := cfg.Roles.resolve()
	return func(ctx context.Context, state *TaskState) (*TaskState, error) {
		tools := append([]agent.Tool{AskUserTool()}, AdaptTools(cfg.ToolExecutor, ReadOnlyTools)...)
		res, err := runNode[FindingsOutput](ctx, cfg, roles.Investigate, state, tools, findingsSchema)
		if err != nil {
			return state, err
		}
		switch res.Status {
		case agent.StatusCompleted:
			state.FinalText = res.Output.Summary
			state.SetArtifact("investigate.findings", res.Output.Summary)
			return state, nil
		case agent.StatusNeedsContext:
			return state, fmt.Errorf("investigate node requested context: %+v", res.Required)
		case agent.StatusError:
			return state, fmt.Errorf("investigate node reported error: %s", res.Error.Error())
		}
		return state, fmt.Errorf("investigate node: unexpected terminal status %q", res.Status)
	}
}

// PlanNodeDynamic reads investigation findings and writes an implementation
// plan to "plan.steps".
func PlanNodeDynamic(cfg TemplateConfig) rhizome.NodeFunc[*TaskState] {
	roles := cfg.Roles.resolve()
	return func(ctx context.Context, state *TaskState) (*TaskState, error) {
		tools := append([]agent.Tool{AskUserTool()}, AdaptTools(cfg.ToolExecutor, ReadOnlyTools)...)
		res, err := runNode[PlanOutput](ctx, cfg, roles.Plan, state, tools, planSchema)
		if err != nil {
			return state, err
		}
		switch res.Status {
		case agent.StatusCompleted:
			state.FinalText = res.Output.Summary
			state.SetArtifact("plan.steps", res.Output.Summary)
			return state, nil
		case agent.StatusNeedsContext:
			return state, fmt.Errorf("plan node requested context: %+v", res.Required)
		case agent.StatusError:
			return state, fmt.Errorf("plan node reported error: %s", res.Error.Error())
		}
		return state, fmt.Errorf("plan node: unexpected terminal status %q", res.Status)
	}
}

// ImplementNodeDynamic reads the plan (and optional review feedback) and
// makes code changes. Output goes to "implement.summary".
func ImplementNodeDynamic(cfg TemplateConfig) rhizome.NodeFunc[*TaskState] {
	roles := cfg.Roles.resolve()
	return func(ctx context.Context, state *TaskState) (*TaskState, error) {
		tools := AdaptTools(cfg.ToolExecutor, WriteTools)
		res, err := runNode[ImplementOutput](ctx, cfg, roles.Implement, state, tools, implementSchema)
		if err != nil {
			return state, err
		}
		switch res.Status {
		case agent.StatusCompleted:
			state.FinalText = res.Output.Summary
			state.SetArtifact("implement.summary", res.Output.Summary)
			return state, nil
		case agent.StatusNeedsContext:
			return state, fmt.Errorf("implement node requested context: %+v", res.Required)
		case agent.StatusError:
			return state, fmt.Errorf("implement node reported error: %s", res.Error.Error())
		}
		return state, fmt.Errorf("implement node: unexpected terminal status %q", res.Status)
	}
}

// TestNodeDynamic runs tests. The typed TestOutput.Passed field drives the
// graph's conditional edge (tests_passed → review; tests_failed →
// implement retry).
func TestNodeDynamic(cfg TemplateConfig) rhizome.NodeFunc[*TaskState] {
	roles := cfg.Roles.resolve()
	return func(ctx context.Context, state *TaskState) (*TaskState, error) {
		tools := AdaptTools(cfg.ToolExecutor, TestTools)
		res, err := runNode[TestOutput](ctx, cfg, roles.Test, state, tools, testSchema)
		if err != nil {
			return state, err
		}
		switch res.Status {
		case agent.StatusCompleted:
			state.FinalText = res.Output.Summary
			state.SetArtifact("test.results", res.Output.Summary)
			if res.Output.Passed {
				state.Status = StatusTestsPassed
			} else {
				state.Status = StatusTestsFailed
			}
			return state, nil
		case agent.StatusNeedsContext:
			return state, fmt.Errorf("test node requested context: %+v", res.Required)
		case agent.StatusError:
			return state, fmt.Errorf("test node reported error: %s", res.Error.Error())
		}
		return state, fmt.Errorf("test node: unexpected terminal status %q", res.Status)
	}
}

// ReviewNodeDynamic reviews the implementation against the plan. The typed
// ReviewOutput.Approved field drives routing (approved → End; rejected →
// implement retry).
func ReviewNodeDynamic(cfg TemplateConfig) rhizome.NodeFunc[*TaskState] {
	roles := cfg.Roles.resolve()
	return func(ctx context.Context, state *TaskState) (*TaskState, error) {
		tools := append([]agent.Tool{AskUserTool()}, AdaptTools(cfg.ToolExecutor, ReadOnlyTools)...)
		res, err := runNode[ReviewOutput](ctx, cfg, roles.Review, state, tools, reviewSchema)
		if err != nil {
			return state, err
		}
		switch res.Status {
		case agent.StatusCompleted:
			state.FinalText = res.Output.Feedback
			state.SetArtifact("review.feedback", res.Output.Feedback)
			if res.Output.Approved {
				state.Status = StatusReviewApproved
			} else {
				state.Status = StatusReviewRejected
			}
			return state, nil
		case agent.StatusNeedsContext:
			return state, fmt.Errorf("review node requested context: %+v", res.Required)
		case agent.StatusError:
			return state, fmt.Errorf("review node reported error: %s", res.Error.Error())
		}
		return state, fmt.Errorf("review node: unexpected terminal status %q", res.Status)
	}
}

// SingleWorkerNode runs one bounded agent call with the full tool set. It
// replaces the old LLMNode for single-worker graphs.
func SingleWorkerNode(cfg TemplateConfig, sysPrompt, initialMessage string) rhizome.NodeFunc[*TaskState] {
	return func(ctx context.Context, state *TaskState) (*TaskState, error) {
		msg := initialMessage
		if msg == "" {
			msg = buildInitialMessage(state)
		}
		res, err := agent.Run(ctx, agent.Config[WorkOutput]{
			Provider:     cfg.Provider,
			Model:        cfg.Model,
			System:       sysPrompt,
			Messages:     []provider.Message{{Role: "user", Content: msg}},
			Tools:        AdaptTools(cfg.ToolExecutor, nil),
			OutputSchema: workSchema,
			OnEvent:      onEventSink(ctx),
		})
		if err != nil {
			return state, err
		}
		switch res.Status {
		case agent.StatusCompleted:
			state.FinalText = res.Output.Output
			state.SetArtifact("work.output", res.Output.Output)
			state.Status = StatusCompleted
			return state, nil
		case agent.StatusNeedsContext:
			return state, fmt.Errorf("work node requested context: %+v", res.Required)
		case agent.StatusError:
			return state, fmt.Errorf("work node reported error: %s", res.Error.Error())
		}
		return state, fmt.Errorf("work node: unexpected terminal status %q", res.Status)
	}
}
