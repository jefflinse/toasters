package dagmap

// These fixtures mirror the four templates in internal/graphexec/templates.go.
// Keep them in sync until a builder wrapper captures topology automatically.

func BugFix() Topology {
	return Topology{
		Nodes: []string{"investigate", "plan", "implement", "test", "review"},
		Edges: []Edge{
			{From: StartName, To: "investigate", Kind: EdgeStatic},
			{From: "investigate", To: "plan", Kind: EdgeStatic},
			{From: "plan", To: "implement", Kind: EdgeStatic},
			{From: "implement", To: "test", Kind: EdgeStatic},
			{From: "test", To: "review", Kind: EdgeConditional, Label: "passed"},
			{From: "test", To: "implement", Kind: EdgeConditional, Label: "failed"},
			{From: "review", To: EndName, Kind: EdgeConditional, Label: "approved"},
			{From: "review", To: "implement", Kind: EdgeConditional, Label: "rejected"},
		},
	}
}

func NewFeature() Topology {
	return Topology{
		Nodes: []string{"plan", "implement", "test", "review"},
		Edges: []Edge{
			{From: StartName, To: "plan", Kind: EdgeStatic},
			{From: "plan", To: "implement", Kind: EdgeStatic},
			{From: "implement", To: "test", Kind: EdgeStatic},
			{From: "test", To: "review", Kind: EdgeConditional, Label: "passed"},
			{From: "test", To: "implement", Kind: EdgeConditional, Label: "failed"},
			{From: "review", To: EndName, Kind: EdgeConditional, Label: "approved"},
			{From: "review", To: "implement", Kind: EdgeConditional, Label: "rejected"},
		},
	}
}

func Prototype() Topology {
	return Topology{
		Nodes: []string{"implement", "test"},
		Edges: []Edge{
			{From: StartName, To: "implement", Kind: EdgeStatic},
			{From: "implement", To: "test", Kind: EdgeStatic},
			{From: "test", To: EndName, Kind: EdgeConditional, Label: "passed"},
			{From: "test", To: "implement", Kind: EdgeConditional, Label: "failed"},
		},
	}
}

func SingleWorker() Topology {
	return Topology{
		Nodes: []string{"work"},
		Edges: []Edge{
			{From: StartName, To: "work", Kind: EdgeStatic},
			{From: "work", To: EndName, Kind: EdgeStatic},
		},
	}
}
