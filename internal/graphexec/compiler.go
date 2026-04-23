package graphexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jefflinse/rhizome"
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

	// Nodes.
	for _, n := range def.Nodes {
		if n.Graph != "" {
			// Subgraphs are reserved for Phase 3; reject cleanly for now.
			return nil, fmt.Errorf("compile %q: node %q references subgraph %q, which is not yet supported", def.ID, n.ID, n.Graph)
		}
		fn, err := registry.Build(n.Role, cfg)
		if err != nil {
			return nil, fmt.Errorf("compile %q: node %q: %w", def.ID, n.ID, err)
		}
		// Wrap the node so that a NodeContext is always present during the
		// body — even when a caller drives the graph without the
		// NodeContextMiddleware. Without this, role nodes writing into
		// NodeOutputs fall back to keying by role name, and routers that
		// read $<nodeID>.output.* miss their source. In the executor path
		// the middleware has already set a richer context; we don't
		// overwrite.
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
