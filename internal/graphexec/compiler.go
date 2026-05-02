package graphexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jefflinse/rhizome"

	"github.com/jefflinse/toasters/internal/prompt"
)

// Compile turns a validated Definition into a runnable rhizome graph. Each
// node's role is resolved via the registry; plain edges become AddEdge
// calls and router edges become AddConditionalEdge with a closure that
// reads TaskState.NodeOutputs and matches the branch values.
//
// Router semantics (v1): the `on` expression $<node>.output.<field> is
// evaluated at runtime by decoding NodeOutputs[node] and walking <field>
// (dots allowed for nested lookup). The decoded value is compared against
// each branch's When by JSON-encoding both sides — that sidesteps YAML-vs-
// JSON numeric typing quirks (YAML `3` decodes to int, JSON `3` decodes to
// float64, canonical JSON encoding of both is `3`).
func Compile(def *Definition, cfg TemplateConfig, registry *RoleRegistry) (*rhizome.CompiledGraph[*TaskState], error) {
	if def == nil {
		return nil, fmt.Errorf("compile: nil definition")
	}
	if err := def.Validate(); err != nil {
		return nil, fmt.Errorf("compile %q: %w", def.ID, err)
	}
	if registry == nil {
		registry = NewRoleRegistry()
	}

	g := rhizome.New[*TaskState]()

	// Collect each node's role so router compilation can validate the
	// `on:` expression against the source node's declared schema.
	nodeRoles := make(map[string]*prompt.Role, len(def.Nodes))

	// Nodes.
	for _, n := range def.Nodes {
		if n.Graph != "" {
			// Subgraphs are reserved for a later phase; reject cleanly for now.
			return nil, fmt.Errorf("compile %q: node %q references subgraph %q, which is not yet supported", def.ID, n.ID, n.Graph)
		}
		fn, err := registry.Build(n.Role, n.ID, n.Slots, cfg)
		if err != nil {
			return nil, fmt.Errorf("compile %q: node %q: %w", def.ID, n.ID, err)
		}
		if cfg.PromptEngine != nil {
			if role := cfg.PromptEngine.Role(n.Role); role != nil {
				nodeRoles[n.ID] = role
				if err := validateSlots(role, n.Slots, cfg.PromptEngine); err != nil {
					return nil, fmt.Errorf("compile %q: node %q: %w", def.ID, n.ID, err)
				}
			}
		}
		// Wrap the node so that a NodeContext is always present during the
		// body — even when a caller drives the graph without the
		// NodeContextMiddleware. In the executor path the middleware has
		// already set a richer context; we don't overwrite.
		nodeID := n.ID
		fn = withDefaultNodeContext(nodeID, fn)
		if err := g.AddNode(nodeID, fn); err != nil {
			return nil, fmt.Errorf("compile %q: AddNode %q: %w", def.ID, nodeID, err)
		}
	}

	// Start → Entry.
	if err := g.AddEdge(rhizome.Start, def.Entry); err != nil {
		return nil, fmt.Errorf("compile %q: start→%q: %w", def.ID, def.Entry, err)
	}

	// Edges.
	for i, e := range def.Edges {
		switch {
		case e.Router == nil:
			if err := g.AddEdge(e.From, resolveDest(e.To)); err != nil {
				return nil, fmt.Errorf("compile %q: edge[%d] %s→%s: %w", def.ID, i, e.From, e.To, err)
			}
		default:
			if cfg.PromptEngine != nil {
				if err := validateRouter(e.Router, nodeRoles, cfg.PromptEngine); err != nil {
					return nil, fmt.Errorf("compile %q: edge[%d] router from %q: %w", def.ID, i, e.From, err)
				}
			}
			fn, dests, err := buildRouter(e.From, e.Router)
			if err != nil {
				return nil, fmt.Errorf("compile %q: edge[%d] router from %q: %w", def.ID, i, e.From, err)
			}
			if err := g.AddConditionalEdge(e.From, fn, dests...); err != nil {
				return nil, fmt.Errorf("compile %q: edge[%d] AddConditionalEdge from %q: %w", def.ID, i, e.From, err)
			}
		}
	}

	// Default Exit → End, if the user named an Exit but didn't wire it
	// explicitly. Saves boilerplate in the common case.
	if def.Exit != "" && !edgesFromNode(def, def.Exit) {
		if err := g.AddEdge(def.Exit, rhizome.End); err != nil {
			return nil, fmt.Errorf("compile %q: exit→end: %w", def.ID, err)
		}
	}

	opts := []rhizome.CompileOption{}
	if def.MaxIterations > 0 {
		opts = append(opts, rhizome.WithMaxNodeExecs(def.MaxIterations))
	}
	return g.Compile(opts...)
}

// resolveDest maps a YAML destination (which may be a node ID or the "end"
// sentinel) to a rhizome node name.
func resolveDest(dst string) string {
	if dst == EndNode {
		return rhizome.End
	}
	return dst
}

// withDefaultNodeContext wraps a NodeFunc so that if no NodeContext is
// present on ctx (e.g. when tests invoke CompiledGraph.Run directly without
// the executor middleware), a minimal NodeContext with just the rhizome
// node ID is injected. The executor's NodeContextMiddleware sets a richer
// context outside this wrapper, in which case the wrapper is a no-op.
func withDefaultNodeContext(nodeID string, fn rhizome.NodeFunc[*TaskState]) rhizome.NodeFunc[*TaskState] {
	return func(ctx context.Context, s *TaskState) (*TaskState, error) {
		if NodeContextFromContext(ctx) == nil {
			ctx = context.WithValue(ctx, nodeContextKey{}, &NodeContext{Node: nodeID})
		}
		return fn(ctx, s)
	}
}

// edgesFromNode reports whether the definition already has any edge whose
// From is the given node. Used to avoid adding a redundant Exit→End edge.
func edgesFromNode(def *Definition, node string) bool {
	for _, e := range def.Edges {
		if e.From == node {
			return true
		}
	}
	return false
}

// buildRouter compiles a declarative Router into a rhizome router function
// plus the list of possible destinations (needed by AddConditionalEdge).
func buildRouter(fromNode string, r *Router) (func(context.Context, *TaskState) (string, error), []string, error) {
	srcNode, fieldPath, ok := parsePath(r.On)
	if !ok {
		return nil, nil, fmt.Errorf("invalid router expression %q", r.On)
	}

	// Canonicalize each branch's When to its JSON-encoded form so runtime
	// comparisons are numeric-typing-agnostic.
	type canonicalBranch struct {
		when []byte
		to   string
	}
	canonical := make([]canonicalBranch, 0, len(r.Branches))
	seen := make(map[string]struct{})
	destNames := make([]string, 0, len(r.Branches)+1)

	for j, b := range r.Branches {
		whenJSON, err := json.Marshal(b.When)
		if err != nil {
			return nil, nil, fmt.Errorf("branch[%d] when: %w", j, err)
		}
		canonical = append(canonical, canonicalBranch{when: whenJSON, to: b.To})
		dest := resolveDest(b.To)
		if _, dup := seen[dest]; !dup {
			seen[dest] = struct{}{}
			destNames = append(destNames, dest)
		}
	}
	defaultDest := ""
	if r.Default != "" {
		defaultDest = resolveDest(r.Default)
		if _, dup := seen[defaultDest]; !dup {
			seen[defaultDest] = struct{}{}
			destNames = append(destNames, defaultDest)
		}
	}

	onExpr := r.On
	fn := func(_ context.Context, s *TaskState) (string, error) {
		raw := s.GetNodeOutput(srcNode)
		if len(raw) == 0 {
			return "", fmt.Errorf("router %q from %q: no output recorded for node %q", onExpr, fromNode, srcNode)
		}
		val, err := extractField(raw, fieldPath)
		if err != nil {
			return "", fmt.Errorf("router %q from %q: %w", onExpr, fromNode, err)
		}
		valJSON, err := json.Marshal(val)
		if err != nil {
			return "", fmt.Errorf("router %q from %q: encode value: %w", onExpr, fromNode, err)
		}
		for _, b := range canonical {
			if bytes.Equal(valJSON, b.when) {
				return resolveDest(b.to), nil
			}
		}
		if defaultDest != "" {
			return defaultDest, nil
		}
		return "", fmt.Errorf("router %q from %q: no branch matched value %s", onExpr, fromNode, string(valJSON))
	}
	return fn, destNames, nil
}

// validateRouter checks that a router's `on:` expression references a field
// declared on the source node's output schema. Returns a clear error when
// the source role is unknown, the referenced schema is not loaded, or the
// leading field segment is missing from the schema.
//
// Routers today only inspect the top-level field (nested paths are rare in
// practice) so validation stops at the first segment — that's enough to
// catch "graph wired for the wrong role" mistakes without being clever.
func validateRouter(r *Router, nodeRoles map[string]*prompt.Role, engine *prompt.Engine) error {
	srcNode, fieldPath, ok := parsePath(r.On)
	if !ok {
		return fmt.Errorf("invalid router expression %q", r.On)
	}
	role, ok := nodeRoles[srcNode]
	if !ok {
		// Role was not in the prompt engine — Build() already errored, or
		// the test path omitted it. Nothing to validate against.
		return nil
	}
	_, schema, err := ResolveSchema(engine, role)
	if err != nil {
		return err
	}
	head := fieldPath
	if idx := strings.IndexByte(head, '.'); idx >= 0 {
		head = head[:idx]
	}
	if _, ok := schema.Fields[head]; !ok {
		names := make([]string, 0, len(schema.Fields))
		for name := range schema.Fields {
			names = append(names, name)
		}
		return fmt.Errorf("router expression %q: node %q (role %q) emits schema %q which has no field %q (available: %s)", r.On, srcNode, role.Name, schema.Name, head, strings.Join(names, ", "))
	}
	return nil
}

// validateSlots checks at compile time that a node's slot bindings line
// up with the role's declared slots and that any literal toolchain ids
// resolve to a loaded toolchain. Template-reference values
// (`{{ globals.X }}`) are deferred to runtime since the artifacts they
// resolve against don't exist yet.
//
// The runtime path in prompt.Engine.Compose performs the same checks
// against fully-resolved values; doing them here surfaces typos in
// graph YAML at compile time so the operator never spawns a job that
// will fail on the first node.
func validateSlots(role *prompt.Role, bindings map[string]string, engine *prompt.Engine) error {
	declared := make(map[string]struct{}, len(role.Slots))
	for _, name := range role.Slots {
		declared[name] = struct{}{}
		if _, ok := bindings[name]; !ok {
			return fmt.Errorf("role %q declares slot %q but the node binds no value", role.Name, name)
		}
	}
	loaded := engine.Toolchains()
	loadedSet := make(map[string]struct{}, len(loaded))
	for _, id := range loaded {
		loadedSet[id] = struct{}{}
	}
	for name, value := range bindings {
		if _, ok := declared[name]; !ok {
			return fmt.Errorf("node binds slot %q but role %q declares no such slot (available: %s)", name, role.Name, strings.Join(role.Slots, ", "))
		}
		// Skip values that resolve at runtime against task artifacts.
		if slotRef.MatchString(value) {
			continue
		}
		if _, ok := loadedSet[value]; !ok {
			return fmt.Errorf("slot %q bound to unknown toolchain %q (loaded: %s)", name, value, strings.Join(loaded, ", "))
		}
	}
	return nil
}

// extractField walks fieldPath (dot-separated) through the JSON decoded
// from raw. Returns the terminal value or an error if any segment is
// missing or the structure doesn't match.
func extractField(raw json.RawMessage, fieldPath string) (any, error) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("unmarshal output: %w", err)
	}
	cur := root
	for _, part := range strings.Split(fieldPath, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("field path %q: expected object at %q, got %T", fieldPath, part, cur)
		}
		next, ok := m[part]
		if !ok {
			return nil, fmt.Errorf("field path %q: key %q not found", fieldPath, part)
		}
		cur = next
	}
	return cur, nil
}
