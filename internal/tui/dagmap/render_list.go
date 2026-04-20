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
// "" for focused to disable highlighting.
func RenderListFocused(t Topology, states NodeStates, focused string) string {
	if len(t.Nodes) == 0 {
		return "(empty)"
	}

	nameW := 0
	for _, n := range t.Nodes {
		if len(n) > nameW {
			nameW = len(n)
		}
	}

	var b strings.Builder
	for i, n := range t.Nodes {
		ns := states[n]
		glyph := phaseGlyph(ns.Phase)

		marker := "  "
		if n == focused {
			marker = "▶ "
		}
		row := fmt.Sprintf("%s%s  %-*s", marker, glyph, nameW, n)

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
		if n == focused {
			style = style.Bold(true).Reverse(true)
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(style.Render(row))
	}
	return b.String()
}

// RenderBreadcrumb draws a single-line progress summary, suitable for a
// global status bar. Each node becomes "glyph name" joined by " → ".
func RenderBreadcrumb(t Topology, states NodeStates) string {
	parts := make([]string, len(t.Nodes))
	for i, n := range t.Nodes {
		ns := states[n]
		s := phaseGlyph(ns.Phase) + " " + n
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
