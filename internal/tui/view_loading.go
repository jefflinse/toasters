package tui

import (
	"fmt"
	"image/color"
	"math"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// loadingBarWidth is the number of cells in the bouncing bar track.
const loadingBarWidth = 24

// loadingBarColors are the true-color RGB values the blob cycles through as it bounces.
// Warm amber → orange → red → purple → blue → back, giving a toasty glow effect.
// Each entry is [R, G, B].
var loadingBarColors = [][3]uint8{
	{255, 175, 0},  // amber
	{255, 135, 0},  // orange
	{255, 95, 0},   // deep orange
	{255, 55, 55},  // red-orange
	{220, 50, 120}, // hot pink
	{175, 50, 200}, // purple
	{95, 80, 230},  // blue-purple
	{50, 130, 255}, // blue
	{95, 80, 230},  // blue-purple
	{175, 50, 200}, // purple
	{220, 50, 120}, // hot pink
	{255, 55, 55},  // red-orange
	{255, 95, 0},   // deep orange
	{255, 135, 0},  // orange
}

// fadeColor returns a color.Color that is the given RGB color faded toward
// black by factor (0.0 = original, 1.0 = black).
func fadeColor(r, g, b uint8, factor float64) color.Color {
	fr := uint8(float64(r) * (1.0 - factor))
	fg := uint8(float64(g) * (1.0 - factor))
	fb := uint8(float64(b) * (1.0 - factor))
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", fr, fg, fb))
}

// gradientText applies character-by-character truecolor interpolation from
// color `from` to color `to`, returning a styled string. Each visible
// character gets its own foreground color and bold styling.
func gradientText(text string, from, to [3]uint8) string {
	return gradientTextOn(text, from, to, nil)
}

// gradientTextOn is like gradientText but paints each character's background
// with `bg` so the text reads cleanly on a tinted surface (the per-rune
// styles each end in an ANSI reset, so a single outer Background wrap
// doesn't survive the resets — the bg has to be set per span). Pass `nil`
// for no background.
func gradientTextOn(text string, from, to [3]uint8, bg color.Color) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	mkStyle := func(r, g, b uint8) lipgloss.Style {
		s := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", r, g, b)))
		if bg != nil {
			s = s.Background(bg)
		}
		return s
	}
	if len(runes) == 1 {
		return mkStyle(from[0], from[1], from[2]).Render(string(runes[0]))
	}
	var sb strings.Builder
	n := len(runes) - 1
	for i, r := range runes {
		t := float64(i) / float64(n)
		cr := uint8(float64(from[0])*(1-t) + float64(to[0])*t)
		cg := uint8(float64(from[1])*(1-t) + float64(to[1])*t)
		cb := uint8(float64(from[2])*(1-t) + float64(to[2])*t)
		sb.WriteString(mkStyle(cr, cg, cb).Render(string(r)))
	}
	return sb.String()
}

// rainbowText applies a cycling rainbow effect to each character of text.
// The phase parameter shifts the hue offset, creating an animation when
// incremented each frame (e.g. driven by spinnerFrame).
func rainbowText(text string, phase int) string {
	return rainbowTextOn(text, phase, nil)
}

// rainbowTextOn is like rainbowText but paints each character's background
// with `bg` so the animation reads cleanly on a tinted surface. Pass `nil`
// for no background.
func rainbowTextOn(text string, phase int, bg color.Color) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, r := range runes {
		// Spread one full hue cycle across ~20 characters; shift by phase (1
		// full cycle per ~30 frames). Subtracting the phase makes the wave
		// travel left → right across the text. math.Mod preserves sign, so
		// renormalize negative results into [0, 1).
		hue := math.Mod(float64(i)/20.0-float64(phase)/30.0, 1.0)
		if hue < 0 {
			hue += 1.0
		}
		cr, cg, cb := hslToRGB(hue, 1.0, 0.6)
		s := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", cr, cg, cb)))
		if bg != nil {
			s = s.Background(bg)
		}
		sb.WriteString(s.Render(string(r)))
	}
	return sb.String()
}

// hslToRGB converts HSL (h in [0,1], s in [0,1], l in [0,1]) to RGB bytes.
func hslToRGB(h, s, l float64) (uint8, uint8, uint8) {
	if s == 0 {
		v := uint8(l * 255)
		return v, v, v
	}
	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q
	r := hueToRGB(p, q, h+1.0/3.0)
	g := hueToRGB(p, q, h)
	b := hueToRGB(p, q, h-1.0/3.0)
	return uint8(r * 255), uint8(g * 255), uint8(b * 255)
}

// hueToRGB is a helper for hslToRGB.
func hueToRGB(p, q, t float64) float64 {
	if t < 0 {
		t++
	}
	if t > 1 {
		t--
	}
	switch {
	case t < 1.0/6.0:
		return p + (q-p)*6*t
	case t < 1.0/2.0:
		return q
	case t < 2.0/3.0:
		return p + (q-p)*(2.0/3.0-t)*6
	default:
		return p
	}
}

// numLoadingFrames is the total number of animation frames (ping-pong across the bar).
// The blob travels loadingBarWidth-1 steps right then loadingBarWidth-1 steps left = full cycle.
const numLoadingFrames = (loadingBarWidth - 1) * 2

// loadingMessages are the absurd status messages that cycle during loading.
var loadingMessages = []string{
	"heating elements...",
	"calibrating crispiness...",
	"warming up the slots...",
	"toasting your workers...",
	"achieving optimal browning...",
	"do not put metal in the toaster...",
	"this is fine 🔥",
	"preheating to 450°...",
	"sourcing artisanal bread...",
	"consulting the bread oracle...",
	"buttering the context window...",
	"negotiating with the gluten...",
	"applying light pressure...",
	"waiting for the ding...",
	"checking for even browning...",
	"deploying crumbs...",
	"establishing crust integrity...",
	"syncing with the toaster cloud...",
	"reticulating bread splines...",
	"defrosting the frozen workers...",
	"please do not unplug the toaster...",
	"warming up the second slot...",
	"the toast is a metaphor...",
	"workers are lightly golden...",
	"spreading the jam layer...",
	"calculating optimal ejection velocity...",
	"this will only take a moment (it won't)...",
	"convincing the bread to cooperate...",
	"toasting at a comfortable 72°F...",
	"loading loading loading...",
	"have you tried turning it off and on again...",
	"the crumbs are non-deterministic...",
	"invoking the sandwich protocol...",
	"workers are medium-rare...",
	"almost there (we think)...",
}

// renderLoading renders a centered animated loading screen while the app is initializing.
func (m *Model) renderLoading() tea.View {
	msgStyle := DimStyle.Italic(true)

	// Compute blob position: ping-pong across the bar.
	frame := m.loadingFrame % numLoadingFrames
	var blobPos int
	if frame < loadingBarWidth-1 {
		blobPos = frame
	} else {
		blobPos = numLoadingFrames - frame
	}

	// Pick blob color from the palette, cycling with the frame.
	rgb := loadingBarColors[m.loadingFrame%len(loadingBarColors)]
	blobColor := fadeColor(rgb[0], rgb[1], rgb[2], 0.0)

	// Determine direction: moving right when frame < loadingBarWidth-1, left otherwise.
	movingRight := frame < loadingBarWidth-1

	// Trail: 3 cells behind the blob, each progressively faded (25%, 55%, 80% toward black).
	trailFade := [3]float64{0.35, 0.62, 0.82}
	trailPos := [3]int{-1, -1, -1}
	for d := 0; d < 3; d++ {
		var p int
		if movingRight {
			p = blobPos - (d + 1)
		} else {
			p = blobPos + (d + 1)
		}
		if p >= 0 && p < loadingBarWidth {
			trailPos[d] = p
		}
	}

	// Build the bar cell by cell so each position can be styled independently.
	trackStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	blobStyle := lipgloss.NewStyle().Foreground(blobColor).Bold(true)

	var barParts []string
	for i := range loadingBarWidth {
		ch := "-"
		if i == blobPos {
			barParts = append(barParts, blobStyle.Render("O"))
			continue
		}
		isTrail := false
		for d, tp := range trailPos {
			if tp == i {
				tc := fadeColor(rgb[0], rgb[1], rgb[2], trailFade[d])
				trailStyle := lipgloss.NewStyle().Foreground(tc)
				barParts = append(barParts, trailStyle.Render(ch))
				isTrail = true
				break
			}
		}
		if !isTrail {
			barParts = append(barParts, trackStyle.Render(ch))
		}
	}

	barStr := strings.Join(barParts, "")

	// Cycle the status message every 24 frames (~720ms at 30ms/frame).
	msgIdx := (m.loadingFrame / 24) % len(loadingMessages)
	statusMsg := msgStyle.Render(loadingMessages[msgIdx])

	// Place each element independently at the center of the screen,
	// stacked vertically. Avoids JoinVertical width-measurement issues
	// with multi-column emoji.
	barLine := lipgloss.Place(m.width, 1, lipgloss.Center, lipgloss.Center, barStr)
	breadLine := lipgloss.Place(m.width, 1, lipgloss.Center, lipgloss.Center, "🍞")
	msgLine := lipgloss.Place(m.width, 1, lipgloss.Center, lipgloss.Center, statusMsg)

	content := lipgloss.JoinVertical(lipgloss.Left,
		strings.Repeat("\n", m.height/2-2),
		barLine,
		breadLine,
		"",
		msgLine,
	)

	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
