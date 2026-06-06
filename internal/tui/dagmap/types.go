// Package dagmap renders a rhizome graph topology as a terminal-friendly
// diagram. It knows nothing about rhizome itself — callers supply topology
// (see topologies.go for the current toasters templates).
package dagmap

type EdgeKind int

const (
	EdgeStatic EdgeKind = iota
	EdgeConditional
)

type Edge struct {
	From, To string
	Kind     EdgeKind
	Label    string
}

// StartName and EndName mirror rhizome's reserved sentinels so the renderer
// can treat them specially (small glyphs instead of full boxes).
const (
	StartName = "__start__"
	EndName   = "__end__"
)

// Topology is the static shape of a graph. Nodes MUST be listed in
// execution order; layout treats any edge whose target precedes its source
// in this list as a back-edge.
//
// Children optionally maps a node to the dynamic sub-nodes that fan out from
// it at runtime — the per-branch sessions of a fan-out node (e.g.
// "implement" → ["implement#0", "implement#1", "implement.judge"]). Children
// are deliberately NOT in Nodes: the static layout (column order, back-edges)
// is unaffected, and renderers expand them only where it reads well (the list
// view) or summarize them as a count badge (the linear diagram views).
type Topology struct {
	Nodes    []string
	Edges    []Edge
	Children map[string][]string
}

type Phase int

const (
	PhasePending Phase = iota
	PhaseRunning
	PhaseCompleted
	PhaseFailed
	PhaseInterrupted
)

type NodeState struct {
	Phase      Phase
	ExecCount  int
	LastStatus string
}

type NodeStates map[string]NodeState
