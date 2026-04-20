package dagmap

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// canvas is a 2D grid of runes addressed as (row, col). Out-of-bounds writes
// are silently ignored. Used by the vertical renderer; the horizontal
// renderer builds lines directly since its layout is simpler.
type canvas struct {
	cells [][]rune
}

func newCanvas(rows, cols int) *canvas {
	c := &canvas{cells: make([][]rune, rows)}
	for i := range c.cells {
		c.cells[i] = make([]rune, cols)
		for j := range c.cells[i] {
			c.cells[i][j] = ' '
		}
	}
	return c
}

func (c *canvas) set(row, col int, r rune) {
	if row < 0 || row >= len(c.cells) {
		return
	}
	if col < 0 || col >= len(c.cells[row]) {
		return
	}
	c.cells[row][col] = r
}

func (c *canvas) writeAt(row, col int, s string) {
	for _, r := range s {
		c.set(row, col, r)
		col++
	}
}

func (c *canvas) hline(row, col1, col2 int, r rune) {
	if col1 > col2 {
		col1, col2 = col2, col1
	}
	for col := col1; col <= col2; col++ {
		c.set(row, col, r)
	}
}

func (c *canvas) vline(col, row1, row2 int, r rune) {
	if row1 > row2 {
		row1, row2 = row2, row1
	}
	for row := row1; row <= row2; row++ {
		c.set(row, col, r)
	}
}

// String renders the canvas. When states is non-nil, rows that fall inside
// a node's rowSpan are styled by the node's phase. Lines are trimmed of
// trailing whitespace.
func (c *canvas) String(states NodeStates, nodeRows map[string]rowSpan) string {
	rowStyle := make(map[int]lipgloss.Style)
	for name, rr := range nodeRows {
		style := phaseStyle(states[name].Phase)
		rowStyle[rr.top] = style
		rowStyle[rr.mid] = style
		rowStyle[rr.bot] = style
	}

	var b strings.Builder
	for i, row := range c.cells {
		if i > 0 {
			b.WriteByte('\n')
		}
		line := strings.TrimRight(string(row), " ")
		if style, ok := rowStyle[i]; ok && line != "" {
			b.WriteString(style.Render(line))
		} else {
			b.WriteString(line)
		}
	}
	return b.String()
}
