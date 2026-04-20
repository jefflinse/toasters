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
type Topology struct {
	Nodes []string
	Edges []Edge
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
