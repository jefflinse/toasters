package dagmap

// RenderVertical draws t top-to-bottom with back-edges routed through right
// gutters. Uses uniform box width so every node shares the same center
// column — simpler routing, and scales to fan-out cleanly later.
func RenderVertical(t Topology, states NodeStates) string {
	if len(t.Nodes) == 0 {
		return "(empty graph)"
	}

	_, back := Layout(t)
	fwd, _ := partitionEdges(t.Edges, back)

	boxW := 0
	for _, n := range t.Nodes {
		if len(n) > boxW {
			boxW = len(n)
		}
	}
	boxW += 4 // " name " padding

	// Row plan: start (3 rows: ● │ ▼), then per node 3 box rows + connector
	// rows (2 or 3 depending on label), ending with ● for End.
	nodeRows := make(map[string]rowSpan, len(t.Nodes))
	forwardLabel := make(map[string]string, len(t.Nodes))

	for i, n := range t.Nodes {
		var label string
		if i == len(t.Nodes)-1 {
			for _, e := range t.Edges {
				if e.From == n && e.To == EndName {
					label = e.Label
					break
				}
			}
		} else if e, ok := fwd[n+"|"+t.Nodes[i+1]]; ok {
			label = e.Label
		}
		forwardLabel[n] = label
	}

	rows := 3 // ● │ ▼
	for i, n := range t.Nodes {
		nodeRows[n] = rowSpan{top: rows, mid: rows + 1, bot: rows + 2}
		rows += 3
		connectorRows := 2 // │ ▼
		if forwardLabel[n] != "" {
			connectorRows = 3 // label │ ▼
		}
		if i == len(t.Nodes)-1 {
			// Final connector to End marker — same pattern.
			rows += connectorRows
		} else {
			rows += connectorRows
		}
	}
	rows++ // ●

	leftPad := 2
	boxLeft := leftPad
	boxCenter := boxLeft + boxW/2
	gutterPerLane := 4
	gutterStart := boxLeft + boxW + 1
	labelCol := gutterStart + len(back)*gutterPerLane + 1
	maxLabelW := 0
	for _, b := range back {
		if len(b.Label) > maxLabelW {
			maxLabelW = len(b.Label)
		}
	}
	cols := labelCol + maxLabelW + 2

	c := newCanvas(rows, cols)

	// Start marker and entry arrow.
	c.set(0, boxCenter, '●')
	c.set(1, boxCenter, '│')
	c.set(2, boxCenter, '▼')

	for i, n := range t.Nodes {
		rr := nodeRows[n]
		drawBox(c, rr.top, boxLeft, boxW, n, states[n])

		// Connector below the node, heading to either the next node or End.
		label := forwardLabel[n]
		labelRows := 0
		if label != "" {
			labelRows = 1
		}
		var nextTop int
		if i == len(t.Nodes)-1 {
			nextTop = rows - 1 // the End marker row
		} else {
			nextTop = nodeRows[t.Nodes[i+1]].top
		}
		connStart := rr.bot + 1
		arrowRow := nextTop - 1
		c.vline(boxCenter, connStart, arrowRow-1, '│')
		c.set(arrowRow, boxCenter, '▼')
		if labelRows > 0 {
			labelRow := arrowRow - 1
			start := boxCenter - len(label)/2
			c.writeAt(labelRow, start, label)
			// Clear the │ that vline drew at labelRow (label takes the row).
			c.set(labelRow, boxCenter, ' ')
			c.writeAt(labelRow, start, label)
		}
	}

	c.set(rows-1, boxCenter, '●')

	// Back-edges routed through right gutters.
	for laneIdx, b := range back {
		srcR := nodeRows[b.From].mid
		dstR := nodeRows[b.To].mid
		gutterCol := gutterStart + laneIdx*gutterPerLane

		// Outgoing from source: horizontal ─ from box edge to gutter, corner ╯.
		c.hline(srcR, boxLeft+boxW, gutterCol, '─')
		c.set(srcR, gutterCol, '╯')

		// Vertical up through gutter.
		for r := dstR + 1; r < srcR; r++ {
			c.set(r, gutterCol, '│')
		}

		// Corner at target row: ╮ (down+left), then ← arrow head into box.
		c.set(dstR, gutterCol, '╮')
		c.hline(dstR, boxLeft+boxW, gutterCol-1, '─')
		c.set(dstR, boxLeft+boxW, '◀')

		// Label at far right, on source row.
		if b.Label != "" {
			c.writeAt(srcR, labelCol, b.Label)
		}
	}

	return c.String(states, nodeRows)
}

type rowSpan struct {
	top, mid, bot int
}

func drawBox(c *canvas, topRow, leftCol, width int, name string, ns NodeState) {
	right := leftCol + width - 1
	c.set(topRow, leftCol, '┌')
	c.set(topRow, right, '┐')
	for col := leftCol + 1; col < right; col++ {
		c.set(topRow, col, '─')
	}
	c.set(topRow+1, leftCol, '│')
	c.set(topRow+1, right, '│')
	for col := leftCol + 1; col < right; col++ {
		c.set(topRow+1, col, ' ')
	}
	display := decorate(name, ns)
	start := leftCol + 1 + (width-2-len(display))/2
	c.writeAt(topRow+1, start, display)
	c.set(topRow+2, leftCol, '└')
	c.set(topRow+2, right, '┘')
	for col := leftCol + 1; col < right; col++ {
		c.set(topRow+2, col, '─')
	}
}
