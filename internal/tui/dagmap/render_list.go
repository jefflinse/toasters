package dagmap

import (
	"fmt"
	"strings"
)

// RenderList draws a one-row-per-node status list. This is the scalable
// default — grows vertically with node count, width only grows with the
// longest node name. Fan-out will become indented siblings later.
func RenderList(t Topology, states NodeStates) string {
	return RenderListFocused(t, states, "")
}

// RenderListFocused is RenderList with the named node highlighted. Pass
// "" for focused to disable highlighting. Fan-out children (Topology.Children)
// are rendered as indented rows beneath their parent node.
func RenderListFocused(t Topology, states NodeStates, focused string) string {
	if len(t.Nodes) == 0 {
		return "(empty)"
	}

	nameW := 0
	for _, n := range t.Nodes {
		if len(n) > nameW {
			nameW = len(n)
		}
		for _, ch := range t.Children[n] {
			if len(ch) > nameW {
				nameW = len(ch)
			}
		}
	}

	var lines []string
	for _, n := range t.Nodes {
		lines = append(lines, listRow(n, states[n], focused, nameW, ""))
		for _, ch := range t.Children[n] {
			lines = append(lines, listRow(ch, states[ch], focused, nameW, "    "))
		}
	}
	return strings.Join(lines, "\n")
}

// listRow renders one status line for a node (or fan-out child), prefixed by
// indent for nesting.
func listRow(name string, ns NodeState, focused string, nameW int, indent string) string {
	glyph := phaseGlyph(ns.Phase)
	marker := "  "
	if name == focused {
		marker = "▶ "
	}
	row := fmt.Sprintf("%s%s%s  %-*s", indent, marker, glyph, nameW, name)

	var meta []string
	if ns.ExecCount > 1 {
		meta = append(meta, fmt.Sprintf("×%d", ns.ExecCount))
	}
	if ns.LastStatus != "" {
		meta = append(meta, "← "+ns.LastStatus)
	}
	if len(meta) > 0 {
		row += "  " + strings.Join(meta, "  ")
	}

	style := phaseStyle(ns.Phase)
	if name == focused {
		style = style.Bold(true).Reverse(true)
	}
	return style.Render(row)
}

// branchSuffix returns an ASCII fan-out badge (" +N") for a node with N
// dynamic children, or "" when it has none. ASCII keeps len() equal to the
// rendered cell width, which the box-drawing diagram views rely on.
func branchSuffix(t Topology, n string) string {
	if c := len(t.Children[n]); c > 0 {
		return fmt.Sprintf(" +%d", c)
	}
	return ""
}

// displayName is a node's name plus its fan-out badge, used by the linear
// diagram renderers where children can't be expanded inline.
func displayName(t Topology, n string) string {
	return n + branchSuffix(t, n)
}

// RenderBreadcrumb draws a single-line progress summary, suitable for a
// global status bar. Each node becomes "glyph name" joined by " → ".
func RenderBreadcrumb(t Topology, states NodeStates) string {
	parts := make([]string, len(t.Nodes))
	for i, n := range t.Nodes {
		ns := states[n]
		s := phaseGlyph(ns.Phase) + " " + n + branchSuffix(t, n)
		if ns.ExecCount > 1 {
			s += fmt.Sprintf(" ×%d", ns.ExecCount)
		}
		parts[i] = phaseStyle(ns.Phase).Render(s)
	}
	return strings.Join(parts, " → ")
}

func phaseGlyph(p Phase) string {
	switch p {
	case PhaseRunning:
		return "◐"
	case PhaseCompleted:
		return "✓"
	case PhaseFailed:
		return "✗"
	case PhaseInterrupted:
		return "⏸"
	default:
		return "○"
	}
}
