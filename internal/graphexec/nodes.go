package graphexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jefflinse/mycelium/agent"
	"github.com/jefflinse/rhizome"

	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// RoleNode returns a generic rhizome NodeFunc bound to a single role. The
// system prompt is composed from the role's markdown body; the terminal
// output shape comes from the role's declared schema; the tool allowlist
// is picked from the role's Access field.
//
// Every role runs through the same path — there are no per-role builders.
// Language-specific roles (go-coder, py-tester, …) only differ from the
// generic investigator/planner/etc. via their markdown bodies and their
// declared schemas.
func RoleNode(cfg TemplateConfig, role *prompt.Role, nodeID string) rhizome.NodeFunc[*TaskState] {
	return func(ctx context.Context, state *TaskState) (*TaskState, error) {
		schemaRaw, _, err := ResolveSchema(cfg.PromptEngine, role)
		if err != nil {
			return state, fmt.Errorf("role %q: %w", role.Name, err)
		}

		sysPrompt, err := composePrompt(cfg, role, state)
		if err != nil {
			return state, fmt.Errorf("composing prompt for role %q: %w", role.Name, err)
		}

		tools := toolsForAccess(cfg.ToolExecutor, role.Access)

		res, err := agent.Run(ctx, agent.Config[json.RawMessage]{
			Provider:     cfg.Provider,
			Model:        cfg.Model,
			System:       sysPrompt,
			Messages:     []provider.Message{{Role: "user", Content: buildInitialMessage(state)}},
			Tools:        tools,
			OutputSchema: schemaRaw,
			OnEvent:      onEventSink(ctx),
		})
		if err != nil {
			return state, fmt.Errorf("role %q: %w", role.Name, err)
		}

		switch res.Status {
		case agent.StatusCompleted:
			return applyOutput(ctx, state, nodeID, res.Output)
		case agent.StatusNeedsContext:
			return state, fmt.Errorf("role %q: node requested context: %+v", role.Name, res.Required)
		case agent.StatusError:
			return state, fmt.Errorf("role %q: node reported error: %s", role.Name, res.Error.Error())
		}
		return state, fmt.Errorf("role %q: unexpected terminal status %q", role.Name, res.Status)
	}
}

// applyOutput folds a node's terminal JSON output into TaskState. The raw
// payload is stored in NodeOutputs keyed by nodeID (routers read from
// here); each field is also exposed under "<nodeID>.<field>" in Artifacts
// so downstream role prompts can reference it via template expansion.
func applyOutput(ctx context.Context, state *TaskState, nodeID string, raw json.RawMessage) (*TaskState, error) {
	id := effectiveNodeID(ctx, nodeID)
	if err := state.SetNodeOutput(id, raw); err != nil {
		return state, err
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return state, fmt.Errorf("node %q: unmarshal output: %w", id, err)
	}
	for name, v := range fields {
		state.SetArtifact(id+"."+name, v)
		if name == "summary" {
			if s, ok := v.(string); ok {
				state.FinalText = s
			}
		}
	}
	return state, nil
}

// effectiveNodeID returns the rhizome node id from NodeContext when one is
// attached (i.e. the executor middleware has run), falling back to the
// compile-time nodeID when a caller drives the graph without middleware
// (tests, direct invocations).
func effectiveNodeID(ctx context.Context, fallback string) string {
	if nc := NodeContextFromContext(ctx); nc != nil && nc.Node != "" {
		return nc.Node
	}
	return fallback
}

// composePrompt resolves a role's system prompt via the prompt engine,
// passing TaskState artifacts as overrides. Every artifact string value is
// exposed as a global so role templates can reference `{{ globals.<node-id>.<field> }}`
// for any upstream node.
func composePrompt(cfg TemplateConfig, role *prompt.Role, state *TaskState) (string, error) {
	if cfg.PromptEngine == nil {
		return fmt.Sprintf("You are %s. Task: %s", roleLabel(role), state.GetArtifactString("task.description")), nil
	}
	overrides := make(map[string]string, len(state.Artifacts))
	for key, val := range state.Artifacts {
		if s, ok := val.(string); ok {
			overrides[key] = s
		}
	}
	return cfg.PromptEngine.Compose(roleLookupKey(role), overrides)
}

// roleLookupKey picks the key the prompt engine registered the role under.
// The engine stores roles under both the slugified frontmatter name and the
// filename stem — we prefer the slug since that matches the field in
// Role.Name. Empty falls back to "role" to give the error path something
// printable.
func roleLookupKey(role *prompt.Role) string {
	if role.Name != "" {
		return slugify(role.Name)
	}
	return "role"
}

// roleLabel is a human-friendly label for the role (used in the fallback
// prompt and in error messages).
func roleLabel(role *prompt.Role) string {
	if role == nil {
		return "a worker"
	}
	if role.Name != "" {
		return role.Name
	}
	return "a worker"
}

// slugify mirrors prompt.slugify so graphexec can produce the same lookup
// key without exporting the helper.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	var buf strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

// buildInitialMessage constructs a user message from TaskState artifacts.
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

// toolsForAccess picks a tool allowlist based on the role's declared
// access level. Read-only roles also get the mid-loop ask_user tool so
// they can surface clarifying questions; write/test roles do not, by
// convention: the user should not be asked to choose between variants of
// "run this or that command."
func toolsForAccess(exec runtime.ToolExecutor, access string) []agent.Tool {
	switch strings.ToLower(strings.TrimSpace(access)) {
	case "write":
		return AdaptTools(exec, WriteTools)
	case "test":
		return AdaptTools(exec, TestTools)
	case "all":
		return AdaptTools(exec, nil)
	default: // "", "readonly", "read-only"
		return append([]agent.Tool{AskUserTool()}, AdaptTools(exec, ReadOnlyTools)...)
	}
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
		if ev.Kind == agent.EventKindText && ev.Text != "" {
			nc.Sink.BroadcastSessionText(nc.SessionID, ev.Text)
		}
	}
}
