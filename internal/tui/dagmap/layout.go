package dagmap

type Position struct {
	Layer int
	Row   int
}

// Layout places every node on its own column in Nodes-list order. Edges
// whose target precedes or equals their source become back-edges and are
// returned separately so the renderer can draw them as arcs below the
// main row.
func Layout(t Topology) (map[string]Position, []Edge) {
	positions := make(map[string]Position, len(t.Nodes)+2)
	positions[StartName] = Position{Layer: 0}
	for i, n := range t.Nodes {
		positions[n] = Position{Layer: i + 1}
	}
	positions[EndName] = Position{Layer: len(t.Nodes) + 1}

	var back []Edge
	for _, e := range t.Edges {
		if e.From == StartName || e.To == EndName {
			continue
		}
		if positions[e.From].Layer >= positions[e.To].Layer {
			back = append(back, e)
		}
	}
	return positions, back
}
