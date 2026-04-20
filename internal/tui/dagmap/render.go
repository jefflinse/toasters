package dagmap

import (
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	startPrefix      = "●──▶ "
	startPrefixWidth = 5 // visible cells of startPrefix
	endMarker        = "●"
	minGap           = 5 // minimum gap cells (fits " ──▶ ")
)

// Render draws t horizontally. states may be nil; when non-nil, node boxes
// are styled by their phase.
func Render(t Topology, states NodeStates) string {
	if len(t.Nodes) == 0 {
		return "(empty graph)"
	}

	li := computeLayout(t)
	_, back := Layout(t)
	incomingBack := map[string]bool{}
	for _, b := range back {
		incomingBack[b.To] = true
	}

	var top, mid, bot strings.Builder

	top.WriteString(strings.Repeat(" ", startPrefixWidth))
	mid.WriteString(startPrefix)
	bot.WriteString(strings.Repeat(" ", startPrefixWidth))

	for i, n := range t.Nodes {
		style := phaseStyle(states[n].Phase)
		inner := li.widths[i] - 2
		top.WriteString(style.Render("┌" + strings.Repeat("─", inner) + "┐"))
		mid.WriteString(style.Render("│ " + decorate(n, states[n]) + " │"))
		if incomingBack[n] {
			half := inner / 2
			bot.WriteString(style.Render("└" + strings.Repeat("─", half) + "▲" + strings.Repeat("─", inner-half-1) + "┘"))
		} else {
			bot.WriteString(style.Render("└" + strings.Repeat("─", inner) + "┘"))
		}

		g := li.gaps[i]
		top.WriteString(centerIn(g.label, g.width))
		mid.WriteString(drawArrow(g.kind, g.width, g.isEnd))
		bot.WriteString(strings.Repeat(" ", g.width))
	}

	var out strings.Builder
	out.WriteString(top.String())
	out.WriteByte('\n')
	out.WriteString(mid.String())
	out.WriteByte('\n')
	out.WriteString(bot.String())
	for _, lane := range renderBackLanes(t, back, li.centers, li.total) {
		out.WriteByte('\n')
		out.WriteString(lane)
	}
	return out.String()
}

type gapInfo struct {
	width int
	label string
	kind  EdgeKind
	isEnd bool // last gap, terminating in End marker rather than next box
}

type layoutInfo struct {
	widths  []int
	centers []int
	gaps    []gapInfo // gaps[i] is the connector after node i (last is end)
	total   int
}

func computeLayout(t Topology) layoutInfo {
	widths := make([]int, len(t.Nodes))
	centers := make([]int, len(t.Nodes))
	gaps := make([]gapInfo, len(t.Nodes))

	for i, n := range t.Nodes {
		widths[i] = len(n) + 4
	}

	_, back := Layout(t)
	fwd, _ := partitionEdges(t.Edges, back)

	for i := 0; i < len(t.Nodes)-1; i++ {
		from := t.Nodes[i]
		to := t.Nodes[i+1]
		if e, ok := fwd[from+"|"+to]; ok {
			gaps[i] = gapInfo{kind: e.Kind, label: e.Label}
		} else {
			gaps[i] = gapInfo{kind: EdgeStatic}
		}
		gaps[i].width = minGap
		if gaps[i].kind == EdgeConditional && gaps[i].label != "" {
			gaps[i].width = max(minGap, len(gaps[i].label)+4)
		}
	}

	// End connector for the last node.
	last := t.Nodes[len(t.Nodes)-1]
	endGap := gapInfo{width: minGap, kind: EdgeStatic, isEnd: true}
	for _, e := range t.Edges {
		if e.From == last && e.To == EndName {
			endGap.kind = e.Kind
			endGap.label = e.Label
			if e.Kind == EdgeConditional && e.Label != "" {
				endGap.width = max(minGap, len(e.Label)+4)
			}
			break
		}
	}
	gaps[len(t.Nodes)-1] = endGap

	cursor := startPrefixWidth
	for i, w := range widths {
		centers[i] = cursor + w/2
		cursor += w + gaps[i].width
	}
	return layoutInfo{widths: widths, centers: centers, gaps: gaps, total: cursor}
}

// drawArrow builds the connector line for the middle row. width is the total
// cells consumed by the connector; the arrow body fills width-2 cells with a
// leading and trailing space, except when isEnd is true (appends the end dot).
func drawArrow(kind EdgeKind, width int, isEnd bool) string {
	bar := "─"
	head := "▶"
	if kind == EdgeConditional {
		bar = "═"
	}
	// Layout: " <bar*(width-3)><head> " then optional "●" (but end case
	// replaces the trailing space with the end dot, so width stays the same
	// overall render line — the end marker spills past the last cell visually;
	// we include it regardless for compactness).
	body := strings.Repeat(bar, width-3) + head
	if isEnd {
		return " " + body + endMarker
	}
	return " " + body + " "
}

func centerIn(s string, width int) string {
	if s == "" {
		return strings.Repeat(" ", width)
	}
	if len(s) >= width {
		return s[:width]
	}
	pad := (width - len(s)) / 2
	return strings.Repeat(" ", pad) + s + strings.Repeat(" ", width-pad-len(s))
}

func decorate(name string, _ NodeState) string {
	// v0: no in-box decoration. Cycle-count and other badges will render as
	// separate overlays outside the box (keeps widths predictable).
	return name
}

func phaseStyle(p Phase) lipgloss.Style {
	base := lipgloss.NewStyle()
	switch p {
	case PhaseRunning:
		return base.Foreground(lipgloss.Color("#ffb000")).Bold(true)
	case PhaseCompleted:
		return base.Foreground(lipgloss.Color("#5fd7a0"))
	case PhaseFailed:
		return base.Foreground(lipgloss.Color("#ff5f5f")).Bold(true)
	case PhaseInterrupted:
		return base.Foreground(lipgloss.Color("#5fd7ff"))
	default:
		return base.Foreground(lipgloss.Color("#6c6c6c"))
	}
}

func partitionEdges(all, back []Edge) (map[string]Edge, map[Edge]bool) {
	backSet := make(map[Edge]bool, len(back))
	for _, b := range back {
		backSet[b] = true
	}
	fwd := map[string]Edge{}
	for _, e := range all {
		if backSet[e] {
			continue
		}
		fwd[e.From+"|"+e.To] = e
	}
	return fwd, backSet
}

func renderBackLanes(t Topology, back []Edge, centers []int, total int) []string {
	if len(back) == 0 {
		return nil
	}
	idx := map[string]int{}
	for i, n := range t.Nodes {
		idx[n] = i
	}
	lanes := make([]string, 0, len(back))
	for _, b := range back {
		src := centers[idx[b.From]]
		dst := centers[idx[b.To]]
		left, right := dst, src
		if left > right {
			left, right = right, left
		}
		lane := make([]rune, total)
		for i := range lane {
			lane[i] = ' '
		}
		lane[left] = '╰'
		lane[right] = '╯'
		for c := left + 1; c < right; c++ {
			lane[c] = '─'
		}
		if b.Label != "" {
			lbl := []rune(" " + b.Label + " ")
			midCol := (left + right) / 2
			start := max(midCol-len(lbl)/2, left+1)
			for j, r := range lbl {
				if start+j >= right {
					break
				}
				lane[start+j] = r
			}
		}
		lanes = append(lanes, strings.TrimRight(string(lane), " "))
	}
	return lanes
}
