package graphexec

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/jefflinse/mycelium/agent"
	"github.com/jefflinse/rhizome"

	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// resumingCtxKey marks a graph run as a resume after an interruption. The
// executor sets it on the context passed to CompiledGraph.Resume; RoleNode
// reads it to prepend a hygiene directive so the re-running (interrupted)
// node reconciles any partial work left on disk instead of duplicating it.
type resumingCtxKey struct{}

func withResuming(ctx context.Context) context.Context {
	return context.WithValue(ctx, resumingCtxKey{}, true)
}

func isResuming(ctx context.Context) bool {
	v, _ := ctx.Value(resumingCtxKey{}).(bool)
	return v
}

// resumeHygienePreamble is prepended to a node's first message when its task
// is resuming after an interruption. The graph skips already-completed nodes,
// but the node that was mid-flight when the process died re-runs from scratch,
// and its partial side effects (half-written files — and, if it commits,
// possibly a commit) may still be on disk. This directs the model to reconcile
// the existing state rather than blindly redo work. It is the reconciliation
// mechanism for resume: resuming already minimizes re-execution (only the
// interrupted node re-runs), and this keeps that single re-run from
// duplicating non-idempotent effects.
const resumeHygienePreamble = "NOTE: This task is resuming after an interruption — " +
	"a previous attempt of this step did not finish. Earlier steps completed and are " +
	"saved (they will not re-run), but THIS step may have partially executed before the " +
	"interruption. Before acting, inspect the current state of the workspace; if you use " +
	"version control, run `git status` and `git log` first. Continue from where it left " +
	"off and do NOT duplicate work that is already present — e.g. recreating files that " +
	"already exist, or repeating a commit that was already made.\n\n"

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

		exec := cfg.ToolExecutor
		if cfg.ToolExecutorFor != nil {
			exec = cfg.ToolExecutorFor(state.WorkspaceDir, state.WorkspaceBase)
		}
		tools := toolsForRole(exec, role)
		thinkingEnabled, temperature := effectiveWorkerDefaults(cfg, role)
		tunedProv := newTunedProvider(cfg.Provider, thinkingEnabled, temperature)
		baseOnEvent := onEventSink(ctx)

		// contextWindow is resolved once per node call (provider/model don't
		// change across request_context rounds); lastInputTokens is updated
		// per round-trip below and read at session close, capturing the
		// node's context occupancy at completion the same way runtime
		// sessions track it via lastInputTokens.
		var contextWindow int
		if cfg.ContextWindows != nil {
			contextWindow = cfg.ContextWindows.Window(state.ProviderName, cfg.Model)
		}
		var lastInputTokens int64

		messages := []provider.Message{{Role: "user", Content: buildInitialMessage(state)}}
		// On a resumed run the interrupted node re-runs from scratch; warn the
		// model that partial work from the prior attempt may be on disk so it
		// reconciles rather than duplicates (commits, files, …).
		if isResuming(ctx) {
			messages[0].Content = resumeHygienePreamble + messages[0].Content
		}

		// A node terminates with StatusNeedsContext when it calls request_context
		// because it lacks information to proceed. Rather than failing the task —
		// which strands the request where the user never sees it — surface the
		// requested items through the same HITL path as ask_user, then re-run the
		// node with the supplied context appended. maxContextRounds bounds the
		// back-and-forth so a confused model can't loop forever.
		const maxContextRounds = 3
		for round := 0; ; round++ {
			sess := openGraphSession(ctx, cfg.Store, state, effectiveNodeID(ctx, nodeID), sysPrompt, toolNamesOf(tools), cfg)

			// The TUI card is keyed by the stable NodeContext session id, so it
			// persists across re-runs; only announce it once.
			if round == 0 {
				if nc := NodeContextFromContext(ctx); nc != nil && nc.Sink != nil {
					nc.Sink.BroadcastSessionPrompt(nc.SessionID, sysPrompt, messages[0].Content)
					nc.Sink.BroadcastSessionMeta(nc.SessionID, cfg.Model, state.ProviderName, temperature, thinkingEnabled)
				}
			}

			// onEvent fans out: forward to the TUI sink AND accumulate reasoning
			// locally so it can be persisted as its own session_messages row.
			// Reasoning is not part of agent.Result.History by design, so we
			// capture it here or it's lost.
			var reasoningBuf strings.Builder
			onEvent := func(ev agent.Event) {
				if ev.Kind == agent.EventKindReasoning {
					reasoningBuf.WriteString(ev.Text)
				}
				if ev.Kind == agent.EventKindUsage && ev.Usage != nil {
					lastInputTokens = int64(ev.Usage.InputTokens)
				}
				if baseOnEvent != nil {
					baseOnEvent(ev)
				}
			}

			res, runErr := agent.Run(ctx, agent.Config[json.RawMessage]{
				Provider:     tunedProv,
				Model:        cfg.Model,
				System:       sysPrompt,
				Messages:     messages,
				Tools:        tools,
				OutputSchema: schemaRaw,
				MaxTurns:     role.MaxTurns,
				OnEvent:      onEvent,
			})
			closeGraphSession(ctx, cfg.Store, sess, res, runErr, reasoningBuf.String(), lastInputTokens, contextWindow)

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
				if round >= maxContextRounds {
					return state, fmt.Errorf("role %q: node still needs context after %d rounds: %s",
						role.Name, maxContextRounds, formatContextNeeds(res.Required))
				}
				answer, err := requestContextViaHITL(ctx, res.Required)
				if err != nil {
					// No HITL broker, or the user dismissed the prompt: fail the
					// task with the request visible rather than looping silently.
					return state, fmt.Errorf("role %q: node requested context that could not be supplied (%s): %w",
						role.Name, formatContextNeeds(res.Required), err)
				}
				// mycelium appends the request_context tool-call assistant message
				// to History before returning; re-running with it would leave an
				// unanswered tool call. Drop it, keep the prior exploration, and
				// feed the supplied context as the next user turn.
				messages = append(stripTrailingToolCall(res.History),
					provider.Message{Role: "user", Content: "Here is the additional context you requested:\n\n" + answer})
				slog.Info("graph node re-running with supplied context",
					"role", role.Name, "node", nodeID, "round", round+1)
				continue
			case agent.StatusError:
				return state, fmt.Errorf("role %q: node reported error: %s", role.Name, res.Error.Error())
			}
			return state, fmt.Errorf("role %q: unexpected terminal status %q", role.Name, res.Status)
		}
	}
}

// requestContextViaHITL surfaces a node's request_context items to the user
// through the same interrupt path as ask_user and returns the combined answer.
// Each ContextNeed becomes one question; the executor's interrupt handler
// presents them as a single form and returns one combined string.
func requestContextViaHITL(ctx context.Context, needs []agent.ContextNeed) (string, error) {
	questions := make([]PromptQuestion, 0, len(needs))
	for _, n := range needs {
		q := strings.TrimSpace(n.Description)
		if q == "" {
			q = fmt.Sprintf("Please provide: %s", n.Key)
		}
		questions = append(questions, PromptQuestion{Question: q})
	}
	if len(questions) == 0 {
		questions = []PromptQuestion{{Question: "The task needs more information to proceed. What should it know?"}}
	}
	resp, err := rhizome.Interrupt(ctx, rhizome.InterruptRequest{
		Kind:    InterruptKindAskUser,
		Payload: AskUserPayload{Questions: questions},
	})
	if err != nil {
		return "", err
	}
	text, _ := resp.Value.(string)
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("empty response")
	}
	return text, nil
}

// formatContextNeeds renders requested context items for an error message.
func formatContextNeeds(needs []agent.ContextNeed) string {
	if len(needs) == 0 {
		return "(unspecified)"
	}
	parts := make([]string, 0, len(needs))
	for _, n := range needs {
		if n.Description != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", n.Key, n.Description))
		} else {
			parts = append(parts, n.Key)
		}
	}
	return strings.Join(parts, "; ")
}

// stripTrailingToolCall drops a final assistant message that carries tool
// calls with no following tool results — the shape mycelium leaves behind
// when a node terminates via request_context. Removing it yields a transcript
// that ends cleanly (on a tool result or user turn) so the re-run's provider
// call has no dangling tool call.
func stripTrailingToolCall(history []provider.Message) []provider.Message {
	if n := len(history); n > 0 && history[n-1].Role == "assistant" && len(history[n-1].ToolCalls) > 0 {
		return history[:n-1]
	}
	return history
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
// exposed as a data value so role templates can reference `{{ <node-id>.<field> }}`
// for any upstream node. Slots binds parameterized fillers declared on the
// role (e.g. {"toolchain": "go"}); slot values may themselves be template
// references like `{{ task.toolchain }}` that resolve against
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
	resolved, err := resolveSlotValues(slots, overrides, cfg.PromptEngine.Instructions())
	if err != nil {
		return "", err
	}
	return cfg.PromptEngine.Compose(roleLookupKey(role), overrides, resolved)
}

// slotRef matches a single template reference that occupies the entire
// slot value, e.g. `{{ task.toolchain }}`. Slot values are not
// arbitrary templates — they're either a literal id or one ref.
var slotRef = regexp.MustCompile(`^\s*\{\{\s*([\w-]+)\.([\w.-]+)\s*\}\}\s*$`)

// resolveSlotValues resolves a single template reference used as a slot value:
// `{{ instructions.<name> }}` to that instruction's body, and any other
// `{{ <root>.<key> }}` to the matching task data value (e.g.
// `{{ task.toolchain }}`). Plain literal values pass through. Returns an error
// when a reference can't be resolved.
func resolveSlotValues(slots, artifacts, instructions map[string]string) (map[string]string, error) {
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
		switch category {
		case "instructions":
			body, ok := instructions[key]
			if !ok {
				return nil, fmt.Errorf("slot %q: instruction %q not found", name, value)
			}
			out[name] = strings.TrimSpace(body)
		default:
			// Any non-instructions reference is a data lookup, keyed by the
			// full dotted name (e.g. task.toolchain).
			fullKey := category + "." + key
			resolved, ok := artifacts[fullKey]
			if !ok || resolved == "" {
				if fullKey == "task.toolchain" {
					return nil, fmt.Errorf(
						"slot %q: reference %q has no value in task data — this role binds a "+
							"toolchain slot but the dispatch that started this task carried no "+
							"Toolchain (TaskRequest.Toolchain empty). Toolchain is chosen by "+
							"fine-decompose and must be persisted on the task and recovered by "+
							"every re-dispatch path (initial assign, retry, serial-gate advance)",
						name, value)
				}
				return nil, fmt.Errorf(
					"slot %q: reference %q has no value in task data — the dispatch that "+
						"started this task did not set a %q artifact; check the TaskRequest "+
						"built by whichever code path dispatched it",
					name, value, fullKey)
			}
			out[name] = resolved
		}
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
//
// Scope: only the task description (added explicitly), workspace dir, and
// upstream node outputs from this graph (artifact keys of the form
// `<nodeID>.<field>`). Job-level context (`job.title`, `job.description`)
// and `task.*` artifacts beyond the description are deliberately excluded
// — roles that need them already pull them in via `{{ * }}`
// references in their templates. Including everything by default makes
// narrow-scope roles (coder, tester, reviewer) treat the whole job as
// their scope and overreach.
func buildInitialMessage(state *TaskState) string {
	var parts []string

	if desc := state.GetArtifactString("task.description"); desc != "" {
		parts = append(parts, fmt.Sprintf("Task: %s", desc))
	}
	// The workspace path is deliberately never shown to the model. A path
	// that passes through model-generated text gets re-typed, and local
	// models mangle long opaque tokens: a plan node once dropped a UUID
	// group from the workspace path and downstream builders shelled half
	// the job's output into the phantom sibling directory. Every tool
	// already resolves relative paths against the session's workspace
	// (with fan-out branch aliasing handled in buildToolExecutor), so the
	// model never needs the absolute path — describe the workspace
	// positionally instead.
	if state.WorkspaceDir != "" || state.WorkspaceBase != "" {
		parts = append(parts,
			"Workspace: your working directory. File tools and shell commands run inside it and resolve "+
				"relative paths against it. Always use relative paths in your work, commands, and outputs; "+
				"never construct an absolute path to the workspace.")
	}
	// Deterministic order: map iteration would shuffle sections between
	// composes, hurting prompt-cache hits and making transcripts hard to diff.
	keys := make([]string, 0, len(state.Artifacts))
	for key := range state.Artifacts {
		if strings.HasPrefix(key, "task.") || strings.HasPrefix(key, "job.") {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if s, ok := state.Artifacts[key].(string); ok && s != "" {
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
// The ask_user human-interrupt tool is opt-in: a role gets it only by
// listing "ask_user" in its tools frontmatter. It is deliberately NOT
// granted to every read-only role — a worker that can halt the whole job
// to ask the user is a footgun for weak local models and for unattended
// autonomy, so only roles that genuinely clarify requirements (e.g.
// investigator) should opt in.
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
	if slices.Contains(role.Tools, InterruptKindAskUser) {
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

// accessWritesWorkspace reports whether an access level grants file-mutating
// tools (write_file/edit_file). Only "write" and "all" do; "test" runs and
// reads (shell + read tools, see TestTools) but cannot edit source. This is
// what fan-out keys off to decide isolation: write branches need a private
// workspace and winner-promotion, while test/read-only branches share the
// workspace and have their outputs aggregated.
func accessWritesWorkspace(access string) bool {
	switch normalizeAccess(access) {
	case "write", "all":
		return true
	default:
		return false
	}
}

// onEventSink returns a mycelium agent OnEvent handler that broadcasts streaming
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
	// mycelium's agent.Event carries the call ID on a ToolCall but not on the
	// corresponding ToolResult. Track outstanding call IDs in arrival order and
	// pair each result with its call (results come back in call order), so the
	// UI can merge a call with its result instead of rendering two items.
	var pendingCallIDs []string
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
			pendingCallIDs = append(pendingCallIDs, ev.ToolCall.ID)
			nc.Sink.BroadcastSessionToolCall(nc.SessionID, ev.ToolCall.ID, ev.ToolCall.Name, ev.ToolCall.Arguments)
		case agent.EventKindToolResult:
			callID := ""
			if len(pendingCallIDs) > 0 {
				callID = pendingCallIDs[0]
				pendingCallIDs = pendingCallIDs[1:]
			}
			nc.Sink.BroadcastSessionToolResult(nc.SessionID, callID, ev.ToolName, ev.Result, "")
		case agent.EventKindUsage:
			// The round-trip's input tokens are the node's current context
			// occupancy; forward them so the fleet pane's context bar fills.
			if ev.Usage != nil {
				nc.Sink.BroadcastSessionContextTokens(nc.SessionID, int64(ev.Usage.InputTokens))
			}
		}
	}
}
