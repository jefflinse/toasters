package graphexec

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
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
// is picked from the role's Access field. Slots binds the role's declared
// slots to concrete values (today: toolchain ids).
//
// Every role runs through the same path — there are no per-role builders.
// Slot-bearing roles (coder, code-reviewer, …) parameterize their bodies
// at compose time via the slots map; otherwise they go through the same
// path as plain roles.
func RoleNode(cfg TemplateConfig, role *prompt.Role, nodeID string, slots map[string]string) rhizome.NodeFunc[*TaskState] {
	return func(ctx context.Context, state *TaskState) (*TaskState, error) {
		schemaRaw, _, err := ResolveSchema(cfg.PromptEngine, role)
		if err != nil {
			return state, fmt.Errorf("role %q: %w", role.Name, err)
		}

		sysPrompt, err := composePrompt(cfg, role, state, slots)
		if err != nil {
			return state, fmt.Errorf("composing prompt for role %q: %w", role.Name, err)
		}

		tools := toolsForRole(cfg.ToolExecutor, role)

		sess := openGraphSession(ctx, cfg.Store, state, effectiveNodeID(ctx, nodeID), sysPrompt, toolNamesOf(tools), cfg)

		// onEvent fans out: forward to the TUI sink AND accumulate reasoning
		// locally so it can be persisted as its own session_messages row.
		// Reasoning is not part of agent.Result.History by design, so we
		// capture it here or it's lost.
		baseOnEvent := onEventSink(ctx)
		var reasoningBuf strings.Builder
		onEvent := func(ev agent.Event) {
			if ev.Kind == agent.EventKindReasoning {
				reasoningBuf.WriteString(ev.Text)
			}
			if baseOnEvent != nil {
				baseOnEvent(ev)
			}
		}

		thinkingEnabled, temperature := effectiveWorkerDefaults(cfg, role)
		tunedProv := newTunedProvider(cfg.Provider, thinkingEnabled, temperature)

		res, runErr := agent.Run(ctx, agent.Config[json.RawMessage]{
			Provider:     tunedProv,
			Model:        cfg.Model,
			System:       sysPrompt,
			Messages:     []provider.Message{{Role: "user", Content: buildInitialMessage(state)}},
			Tools:        tools,
			OutputSchema: schemaRaw,
			MaxTurns:     role.MaxTurns,
			OnEvent:      onEvent,
		})
		closeGraphSession(ctx, cfg.Store, sess, res, runErr, reasoningBuf.String())

		if runErr != nil {
			if tail := lastAssistantText(res.History); tail != "" {
				slog.Warn("role node failed without a terminal tool call",
					"role", role.Name, "node", nodeID, "error", runErr, "tail_chars", len(tail))
			}
			return state, fmt.Errorf("role %q: %w", role.Name, runErr)
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
// for any upstream node. Slots binds parameterized fillers declared on the
// role (e.g. {"toolchain": "go"}); slot values may themselves be template
// references like `{{ globals.task.toolchain }}` that resolve against
// state artifacts at compose time, letting graphs stay toolchain-generic.
func composePrompt(cfg TemplateConfig, role *prompt.Role, state *TaskState, slots map[string]string) (string, error) {
	if cfg.PromptEngine == nil {
		return fmt.Sprintf("You are %s. Task: %s", roleLabel(role), state.GetArtifactString("task.description")), nil
	}
	overrides := make(map[string]string, len(state.Artifacts))
	for key, val := range state.Artifacts {
		if s, ok := val.(string); ok {
			overrides[key] = s
		}
	}
	resolved, err := resolveSlotValues(slots, overrides)
	if err != nil {
		return "", err
	}
	return cfg.PromptEngine.Compose(roleLookupKey(role), overrides, resolved)
}

// slotRef matches a single template reference that occupies the entire
// slot value, e.g. `{{ globals.task.toolchain }}`. Slot values are not
// arbitrary templates — they're either a literal id or one ref.
var slotRef = regexp.MustCompile(`^\s*\{\{\s*([\w-]+)\.([\w.-]+)\s*\}\}\s*$`)

// resolveSlotValues replaces `{{ globals.X }}` references in slot values
// with the matching state artifact. Plain literal values pass through.
// Returns an error when a referenced artifact is missing or empty.
func resolveSlotValues(slots, artifacts map[string]string) (map[string]string, error) {
	if len(slots) == 0 {
		return slots, nil
	}
	out := make(map[string]string, len(slots))
	for name, value := range slots {
		m := slotRef.FindStringSubmatch(value)
		if m == nil {
			out[name] = value
			continue
		}
		category, key := m[1], m[2]
		if category != "globals" {
			return nil, fmt.Errorf("slot %q: only globals.* references are supported in slot values, got %q", name, value)
		}
		resolved, ok := artifacts[key]
		if !ok || resolved == "" {
			return nil, fmt.Errorf("slot %q: reference %q has no value in task artifacts", name, value)
		}
		out[name] = resolved
	}
	return out, nil
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

// toolsForRole picks the tool list a graph node gets based on its role.
// The `access:` frontmatter field selects a base allowlist (readonly /
// write / test / all); any tool names in the role's `tools:` frontmatter
// list are merged in as explicit opt-ins on top.
//
// Read-only roles also get the mid-loop ask_user tool so they can
// surface clarifying questions. Write/test roles do not by convention:
// the user should not be asked to choose between variants of "run this
// or that command."
//
// Opt-in is how narrow-use tools like query_graphs stay off most roles'
// radar while remaining available to the one role that needs them
// (fine-decomposer). Keeping the surface small matters for local-model
// ergonomics.
func toolsForRole(exec runtime.ToolExecutor, role *prompt.Role) []agent.Tool {
	access := strings.ToLower(strings.TrimSpace(role.Access))
	var adapted []agent.Tool
	if access == "all" {
		adapted = AdaptTools(exec, nil)
	} else {
		allow := baseAllowlistForAccess(access)
		for _, t := range role.Tools {
			if !slices.Contains(allow, t) {
				allow = append(allow, t)
			}
		}
		adapted = AdaptTools(exec, allow)
	}
	if isReadOnlyAccess(access) {
		return append([]agent.Tool{AskUserTool()}, adapted...)
	}
	return adapted
}

// baseAllowlistForAccess returns a fresh copy of the allowlist for an
// access level. Returning a copy means callers may mutate (e.g.
// append role-declared opt-ins) without aliasing the package-level
// slice.
func baseAllowlistForAccess(access string) []string {
	switch access {
	case "write":
		return append([]string(nil), WriteTools...)
	case "test":
		return append([]string(nil), TestTools...)
	default:
		return append([]string(nil), ReadOnlyTools...)
	}
}

// isReadOnlyAccess reports whether the (already-normalized) access
// value selects the readonly preset. The empty string is the default.
func isReadOnlyAccess(access string) bool {
	return access == "" || access == "readonly" || access == "read-only"
}

// onEventSink returns an agent OnEvent handler that broadcasts streaming
// text, tool calls, and tool results to the EventSink attached to the
// current NodeContext. No-op when no sink is configured — tests and
// library-only uses pay nothing.
//
// Tool-call events route through the same session.tool_call /
// session.tool_result service events that worker sessions emit, so the
// TUI's existing runtimeSlot pipeline (grid cards, output panel) picks
// them up without per-source special cases.
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
			if ev.Text != "" {
				nc.Sink.BroadcastSessionReasoning(nc.SessionID, ev.Text)
			}
		case agent.EventKindToolCall:
			if ev.ToolCall == nil {
				return
			}
			nc.Sink.BroadcastSessionToolCall(nc.SessionID, ev.ToolCall.ID, ev.ToolCall.Name, ev.ToolCall.Arguments)
		case agent.EventKindToolResult:
			nc.Sink.BroadcastSessionToolResult(nc.SessionID, "", ev.ToolName, ev.Result, "")
		}
	}
}
