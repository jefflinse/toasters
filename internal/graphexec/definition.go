package graphexec

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// EndNode is the sentinel node ID used in YAML edges to mean "exit the graph."
// The compiler maps this to rhizome.End when building edges.
const EndNode = "end"

// Definition is the declarative, YAML-authored shape of a graph. Users write
// these as files in ~/.config/toasters/graphs/*.yaml (or ship them bundled
// under defaults/); the compiler turns a Definition into a runnable
// rhizome.CompiledGraph[*TaskState].
//
// Definitions are encapsulated — from the outside a graph is a black box
// with an InputSchema and OutputSchema. Internally, nodes are typed roles
// wired with edges. Subgraphs (a node whose role references another graph)
// compose naturally as nested compilations.
type Definition struct {
	// ID uniquely identifies this graph at discovery time. Required.
	ID string `yaml:"id" json:"id"`

	// Name is the human-readable label shown in catalogs and UIs.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// Description explains what class of tasks this graph handles. Used by
	// the decomposer to pick a matching graph for a work item.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Tags carry free-form capability metadata (e.g. "language:go",
	// "kind:bugfix"). The decomposer may use these as a fast prefilter.
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`

	// InputSchema is the JSON Schema the caller must satisfy when starting
	// this graph. Optional; no schema means "accept anything."
	InputSchema json.RawMessage `yaml:"input_schema,omitempty" json:"input_schema,omitempty"`

	// OutputSchema is the JSON Schema the graph's final output will satisfy.
	// Optional — if set, the compiler will validate Exit's node output
	// against this schema at graph completion.
	OutputSchema json.RawMessage `yaml:"output_schema,omitempty" json:"output_schema,omitempty"`

	// Entry is the node ID Start routes to. Required.
	Entry string `yaml:"entry" json:"entry"`

	// Exit optionally names the node whose output becomes the graph's
	// output. When unset, callers must locate output via NodeOutputs keyed
	// by whatever node routed to "end" last. Recommended: set this.
	Exit string `yaml:"exit,omitempty" json:"exit,omitempty"`

	// Nodes is the set of role-backed nodes in this graph. Order is not
	// significant.
	Nodes []Node `yaml:"nodes" json:"nodes"`

	// Edges wires nodes together. An edge is either a plain forward edge
	// (To set) or a router (Router set) — never both.
	Edges []Edge `yaml:"edges,omitempty" json:"edges,omitempty"`

	// MaxIterations bounds the total number of node executions in one
	// graph run. Maps to rhizome.WithMaxNodeExecs. Zero disables the cap.
	MaxIterations int `yaml:"max_iterations,omitempty" json:"max_iterations,omitempty"`
}

// Node binds a rhizome node ID to a Role. In v1 the role name resolves to
// one of the built-in NodeFunc builders via the role registry; later phases
// will source a Role's system prompt, tools, and output schema from the
// role definition files on disk.
type Node struct {
	// ID is the node's identifier within this graph. Must be unique and
	// must not collide with the "end" sentinel.
	ID string `yaml:"id" json:"id"`

	// Role names a role whose builder handles this node's LLM call. Must
	// resolve in the role registry at compile time.
	Role string `yaml:"role" json:"role"`

	// Graph, when set, makes this node a subgraph: compile-time, the ID
	// resolves to another Definition loaded from disk and this node's
	// work is handled by a nested runner. Node is either role-bound or
	// graph-bound, not both. Unused in Phase 2 — reserved for Phase 3.
	Graph string `yaml:"graph,omitempty" json:"graph,omitempty"`

	// Slots binds the role's declared slot names to concrete values
	// (currently always toolchain ids). Every slot the role declares in
	// frontmatter must have a binding here, or compose fails.
	Slots map[string]string `yaml:"slots,omitempty" json:"slots,omitempty"`
}

// Edge connects nodes. Exactly one of To or Router must be set.
type Edge struct {
	// From is the node ID this edge starts at. Required. Must reference
	// a node defined in the graph.
	From string `yaml:"from" json:"from"`

	// To is the unconditional destination. Set either this or Router,
	// never both. May be a node ID or the "end" sentinel.
	To string `yaml:"to,omitempty" json:"to,omitempty"`

	// Router defines conditional routing from this From. Mutually
	// exclusive with To.
	Router *Router `yaml:"router,omitempty" json:"router,omitempty"`
}

// Router picks a destination based on a field in the source node's typed
// output. v1 supports a single path expression of the form
//
//	$<nodeID>.output.<field>
//
// resolved against NodeOutputs[<nodeID>]. No arithmetic, no templates —
// anything transform-y should be its own role.
type Router struct {
	// On is the path expression to evaluate. Required.
	On string `yaml:"on" json:"on"`

	// Branches lists destinations keyed by the matched value. The
	// compiler walks them in order and picks the first match.
	Branches []Branch `yaml:"branches" json:"branches"`

	// Default is the destination when no branch matches. Optional. If
	// unset and no branch matches, the router returns an error at
	// runtime.
	Default string `yaml:"default,omitempty" json:"default,omitempty"`
}

// Branch matches a value and routes to a destination.
type Branch struct {
	// When is the expected value of the path expression. Compared by
	// value equality after JSON decoding (so booleans match booleans,
	// numbers match numbers, strings match strings).
	When any `yaml:"when" json:"when"`

	// To is the destination node ID or "end".
	To string `yaml:"to" json:"to"`
}

// ParseDefinition decodes YAML bytes into a Definition and validates the
// result. Returned definitions are ready to hand to Compile.
func ParseDefinition(data []byte) (*Definition, error) {
	var d Definition
	if err := yaml.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("parse graph yaml: %w", err)
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

// LoadDefinition reads and parses a graph YAML file from disk.
func LoadDefinition(path string) (*Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return ParseDefinition(data)
}

// ParseDefinitionReader parses a graph from any io.Reader. Useful for
// embed.FS or network-sourced definitions.
func ParseDefinitionReader(r io.Reader) (*Definition, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read graph yaml: %w", err)
	}
	return ParseDefinition(data)
}

// pathExpr is a router expression: $<node>.output.<field>. Kept deliberately
// narrow — anything richer should be modeled as a role.
var pathExpr = regexp.MustCompile(`^\$([A-Za-z0-9_-]+)\.output\.([A-Za-z0-9_.-]+)$`)

// parsePath splits a v1 router expression into (nodeID, fieldPath). Returns
// ok=false if the expression is not a valid v1 path. fieldPath may contain
// dots for nested lookups (e.g. "foo.bar").
func parsePath(expr string) (nodeID, fieldPath string, ok bool) {
	m := pathExpr.FindStringSubmatch(expr)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// Validate performs structural validation against this Definition in
// isolation. Role resolution and output-schema reachability are handled at
// compile time, not here — this pass catches shape errors that are
// independent of any runtime context.
func (d *Definition) Validate() error {
	if strings.TrimSpace(d.ID) == "" {
		return fmt.Errorf("graph id is required")
	}
	if len(d.Nodes) == 0 {
		return fmt.Errorf("graph %q has no nodes", d.ID)
	}
	if strings.TrimSpace(d.Entry) == "" {
		return fmt.Errorf("graph %q has no entry node", d.ID)
	}
	if d.MaxIterations < 0 {
		return fmt.Errorf("graph %q: max_iterations must be >= 0, got %d", d.ID, d.MaxIterations)
	}

	nodeIDs := make(map[string]struct{}, len(d.Nodes))
	for i, n := range d.Nodes {
		if strings.TrimSpace(n.ID) == "" {
			return fmt.Errorf("graph %q: node[%d] has empty id", d.ID, i)
		}
		if n.ID == EndNode {
			return fmt.Errorf("graph %q: node id %q collides with the end sentinel", d.ID, n.ID)
		}
		if _, dup := nodeIDs[n.ID]; dup {
			return fmt.Errorf("graph %q: duplicate node id %q", d.ID, n.ID)
		}
		nodeIDs[n.ID] = struct{}{}
		if strings.TrimSpace(n.Role) == "" && strings.TrimSpace(n.Graph) == "" {
			return fmt.Errorf("graph %q: node %q must set role or graph", d.ID, n.ID)
		}
		if n.Role != "" && n.Graph != "" {
			return fmt.Errorf("graph %q: node %q sets both role and graph (pick one)", d.ID, n.ID)
		}
	}

	if _, ok := nodeIDs[d.Entry]; !ok {
		return fmt.Errorf("graph %q: entry %q is not a declared node", d.ID, d.Entry)
	}
	if d.Exit != "" {
		if _, ok := nodeIDs[d.Exit]; !ok {
			return fmt.Errorf("graph %q: exit %q is not a declared node", d.ID, d.Exit)
		}
	}

	isValidDest := func(dest string) bool {
		if dest == EndNode {
			return true
		}
		_, ok := nodeIDs[dest]
		return ok
	}

	for i, e := range d.Edges {
		if strings.TrimSpace(e.From) == "" {
			return fmt.Errorf("graph %q: edge[%d] missing from", d.ID, i)
		}
		if _, ok := nodeIDs[e.From]; !ok {
			return fmt.Errorf("graph %q: edge[%d]: from %q is not a declared node", d.ID, i, e.From)
		}
		hasTo := strings.TrimSpace(e.To) != ""
		hasRouter := e.Router != nil
		switch {
		case hasTo && hasRouter:
			return fmt.Errorf("graph %q: edge[%d] from %q sets both to and router", d.ID, i, e.From)
		case !hasTo && !hasRouter:
			return fmt.Errorf("graph %q: edge[%d] from %q sets neither to nor router", d.ID, i, e.From)
		case hasTo:
			if !isValidDest(e.To) {
				return fmt.Errorf("graph %q: edge[%d] from %q: to %q is not a declared node", d.ID, i, e.From, e.To)
			}
		case hasRouter:
			r := e.Router
			if strings.TrimSpace(r.On) == "" {
				return fmt.Errorf("graph %q: edge[%d] from %q: router.on is required", d.ID, i, e.From)
			}
			srcNode, _, ok := parsePath(r.On)
			if !ok {
				return fmt.Errorf("graph %q: edge[%d] from %q: router.on %q is not a valid expression (expected $node.output.field)", d.ID, i, e.From, r.On)
			}
			if _, ok := nodeIDs[srcNode]; !ok {
				return fmt.Errorf("graph %q: edge[%d] from %q: router.on references unknown node %q", d.ID, i, e.From, srcNode)
			}
			if len(r.Branches) == 0 && r.Default == "" {
				return fmt.Errorf("graph %q: edge[%d] from %q: router has no branches or default", d.ID, i, e.From)
			}
			for j, b := range r.Branches {
				if strings.TrimSpace(b.To) == "" {
					return fmt.Errorf("graph %q: edge[%d] from %q: branch[%d] missing to", d.ID, i, e.From, j)
				}
				if !isValidDest(b.To) {
					return fmt.Errorf("graph %q: edge[%d] from %q: branch[%d] to %q is not a declared node", d.ID, i, e.From, j, b.To)
				}
			}
			if r.Default != "" && !isValidDest(r.Default) {
				return fmt.Errorf("graph %q: edge[%d] from %q: router default %q is not a declared node", d.ID, i, e.From, r.Default)
			}
		}
	}

	return nil
}
