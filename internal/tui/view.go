// View rendering: main View method, viewport content building, markdown rendering, loading screen, and resize handling.
package tui

import (
	"fmt"
	"image/color"
	"log/slog"
	"math"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	glamourstyles "github.com/charmbracelet/glamour/styles"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/jefflinse/toasters/internal/gateway"
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
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	if len(runes) == 1 {
		return lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", from[0], from[1], from[2]))).
			Render(string(runes[0]))
	}
	var sb strings.Builder
	n := len(runes) - 1
	for i, r := range runes {
		t := float64(i) / float64(n)
		cr := uint8(float64(from[0])*(1-t) + float64(to[0])*t)
		cg := uint8(float64(from[1])*(1-t) + float64(to[1])*t)
		cb := uint8(float64(from[2])*(1-t) + float64(to[2])*t)
		sb.WriteString(lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", cr, cg, cb))).
			Render(string(r)))
	}
	return sb.String()
}

// rainbowText applies a cycling rainbow effect to each character of text.
// The phase parameter shifts the hue offset, creating an animation when
// incremented each frame (e.g. driven by spinnerFrame).
func rainbowText(text string, phase int) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, r := range runes {
		// Spread one full hue cycle across ~20 characters; shift by phase (1 full cycle per ~30 frames).
		hue := math.Mod(float64(i)/20.0+float64(phase)/30.0, 1.0)
		cr, cg, cb := hslToRGB(hue, 1.0, 0.6)
		sb.WriteString(lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", cr, cg, cb))).
			Render(string(r)))
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
	"toasting your agents...",
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
	"defrosting the frozen agents...",
	"please do not unplug the toaster...",
	"warming up the second slot...",
	"the toast is a metaphor...",
	"agents are lightly golden...",
	"spreading the jam layer...",
	"calculating optimal ejection velocity...",
	"this will only take a moment (it won't)...",
	"convincing the bread to cooperate...",
	"toasting at a comfortable 72°F...",
	"loading loading loading...",
	"have you tried turning it off and on again...",
	"the crumbs are non-deterministic...",
	"invoking the sandwich protocol...",
	"agents are medium-rare...",
	"almost there (we think)...",
}

func (m *Model) View() tea.View {
	if m.width == 0 || m.height == 0 {
		v := tea.NewView("")
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	if m.loading {
		return m.renderLoading()
	}

	// Teams modal takes over the full terminal as a centered overlay.
	if m.teamsModal.show {
		teamsView := m.renderTeamsModal()
		v := tea.NewView(teamsView)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// Skills modal takes over the full terminal as a centered overlay.
	if m.skillsModal.show {
		skillsView := m.renderSkillsModal()
		v := tea.NewView(skillsView)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// Agents modal takes over the full terminal as a centered overlay.
	if m.agentsModal.show {
		agentsView := m.renderAgentsModal()
		v := tea.NewView(agentsView)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// Jobs modal takes over the full terminal as a centered overlay.
	if m.jobsModal.show {
		jobsView := m.renderJobsModal()
		v := tea.NewView(jobsView)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// MCP modal takes over the full terminal as a centered overlay.
	if m.mcpModal.show {
		mcpView := m.renderMCPModal()
		v := tea.NewView(mcpView)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// Blocker modal takes over the full terminal as a centered overlay.
	if m.blockerModal.show {
		blockerView := m.renderBlockerModal()
		v := tea.NewView(blockerView)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// Log view takes over the full terminal.
	if m.logView.show {
		v := tea.NewView(m.renderLogView())
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// Grid screen takes over the full terminal.
	if m.grid.showGrid {
		gridView := m.renderGrid()
		if m.promptModal.show {
			overlaid, clampedScroll := m.renderScrollableModal("Prompt", m.promptModal.content, m.promptModal.scroll)
			m.promptModal.scroll = clampedScroll

			v := tea.NewView(overlaid)
			v.AltScreen = true
			v.MouseMode = tea.MouseModeCellMotion
			return v
		} else if m.outputModal.show {
			overlaid, clampedScroll := m.renderOutputModal("Output", m.outputModal.content, m.outputModal.scroll)
			m.outputModal.scroll = clampedScroll

			v := tea.NewView(overlaid)
			v.AltScreen = true
			v.MouseMode = tea.MouseModeCellMotion
			return v
		}
		v := tea.NewView(gridView)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	showSidebar := m.width >= minWidthForBar && !m.sidebarHidden
	showLeftPanel := m.width >= minWidthForLeftPanel && !m.leftPanelHidden

	sbWidth := sidebarWidth(m.width)
	lpWidth := m.effectiveLeftPanelWidth()

	const columnGap = 1 // consistent gap between adjacent columns

	var mainWidth int
	if showSidebar && showLeftPanel {
		mainWidth = m.width - lpWidth - sbWidth - 2*columnGap
	} else if showSidebar {
		mainWidth = m.width - sbWidth - columnGap
	} else if showLeftPanel {
		mainWidth = m.width - lpWidth - columnGap
	} else {
		mainWidth = m.width
	}

	// Build input area style — dim borders when chat is not focused.
	inputStyle := InputAreaStyle
	if m.focused != focusChat {
		inputStyle = inputStyle.
			BorderLeftForeground(ColorBorder).
			BorderTopForeground(ColorBorder).
			BorderRightForeground(ColorBorder).
			BorderBottomForeground(ColorBorder)
	}

	// Build flash line (zero height when empty).
	var flashLine string
	if m.flashText != "" {
		flashLine = DimStyle.Render(m.flashText)
	}

	// Determine chat content and input area — swapped when attached to an agent slot.
	var chatContent string
	var inputOrStatus string
	if m.attachedSlot >= 0 && m.gateway != nil {
		slots := m.gateway.Slots()
		snap := slots[m.attachedSlot]
		header := fmt.Sprintf("⬡ %s · %s", snap.AgentName, snap.JobID)
		if snap.Status == gateway.SlotDone {
			header += " [done]"
		} else {
			header += " [running]"
		}
		chatContent = m.agentViewport.View()
		inputArea := inputStyle.Width(mainWidth).Render(
			DimStyle.Render(header + "  ·  Esc to detach · d to dismiss"),
		)
		if flashLine != "" {
			inputOrStatus = lipgloss.JoinVertical(lipgloss.Left, flashLine, inputArea)
		} else {
			inputOrStatus = inputArea
		}
	} else {
		chatContent = m.chatViewport.View()

		// Render scrollbar column alongside the chat content.
		// Always reserve the column to prevent layout shifts, but only draw
		// the thumb/track when the user has recently scrolled.
		if m.chatViewport.TotalLineCount() > m.chatViewport.Height() {
			var scrollCol string
			if m.scroll.scrollbarVisible {
				scrollCol = renderScrollbar(
					m.chatViewport.Height(),
					m.chatViewport.TotalLineCount(),
					m.chatViewport.ScrollPercent(),
				)
			} else {
				// Empty column — one space per line to reserve the gutter.
				lines := make([]string, m.chatViewport.Height())
				for i := range lines {
					lines[i] = " "
				}
				scrollCol = strings.Join(lines, "\n")
			}
			chatContent = lipgloss.JoinHorizontal(lipgloss.Top, chatContent, scrollCol)
		}

		// Overlay "new messages" indicator when scrolled up and new content arrived.
		if m.scroll.hasNewMessages && m.scroll.userScrolled {
			chatLines := strings.Split(chatContent, "\n")
			if len(chatLines) > 0 {
				indicator := "  ↓ New messages (End to jump)  "
				styledIndicator := lipgloss.NewStyle().
					Background(ColorStreaming).
					Foreground(lipgloss.Color("0")).
					Bold(true).
					Render(indicator)
				// Center the indicator within the chat width.
				vpWidth := m.chatViewport.Width()
				if vpWidth > 0 {
					styledIndicator = lipgloss.PlaceHorizontal(vpWidth, lipgloss.Center, styledIndicator)
				}
				chatLines[len(chatLines)-1] = styledIndicator
				chatContent = strings.Join(chatLines, "\n")
			}
		}

		var inputArea string
		if m.prompt.promptMode {
			inputArea = m.renderPromptWidget(mainWidth, inputStyle)
		} else {
			inputArea = inputStyle.Width(mainWidth).Render(m.input.View())
		}
		if flashLine != "" {
			inputOrStatus = lipgloss.JoinVertical(lipgloss.Left, flashLine, inputArea)
		} else {
			inputOrStatus = inputArea
		}
	}

	// Build slash command popup (if active).
	var popupView string
	if m.cmdPopup.show && len(m.cmdPopup.filteredCmds) > 0 {
		var rows []string
		for i, cmd := range m.cmdPopup.filteredCmds {
			if i == m.cmdPopup.selectedIdx {
				nameStr := CmdPopupNameSelectedStyle.Render(cmd.Name)
				descStr := CmdPopupDescSelectedStyle.Render(cmd.Description)
				row := CmdPopupSelectedStyle.Width(mainWidth).Render(
					lipgloss.JoinHorizontal(lipgloss.Left, nameStr, descStr),
				)
				rows = append(rows, row)
			} else {
				nameStr := CmdPopupNameStyle.Render(cmd.Name)
				descStr := CmdPopupDescStyle.Render(cmd.Description)
				row := CmdPopupRowStyle.Width(mainWidth).Render(
					lipgloss.JoinHorizontal(lipgloss.Left, nameStr, descStr),
				)
				rows = append(rows, row)
			}
		}
		popupView = CmdPopupContainerStyle.Width(mainWidth).Render(
			lipgloss.JoinVertical(lipgloss.Left, rows...),
		)

		// Trim the chat content to make room for the popup so the layout
		// doesn't overflow the terminal height.
		popupHeight := len(m.cmdPopup.filteredCmds)
		lines := strings.Split(chatContent, "\n")
		trimTo := len(lines) - popupHeight
		if trimTo < 0 {
			trimTo = 0
		}
		chatContent = strings.Join(lines[:trimTo], "\n")
	}

	// Build kill modal popup (if active) — mutually exclusive with cmd popup.
	var killPopupView string
	if m.killModal.show && m.gateway != nil {
		slots := m.gateway.Slots()
		var rows []string
		for i, slotIdx := range m.killModal.slots {
			snap := slots[slotIdx]
			label := fmt.Sprintf("[%d] %s · %s", slotIdx, snap.AgentName, snap.JobID)
			if i == m.killModal.selectedIdx {
				row := CmdPopupSelectedStyle.Width(mainWidth).Render(
					CmdPopupNameSelectedStyle.Render(label),
				)
				rows = append(rows, row)
			} else {
				row := CmdPopupRowStyle.Width(mainWidth).Render(
					CmdPopupNameStyle.Render(label),
				)
				rows = append(rows, row)
			}
		}
		footer := CmdPopupRowStyle.Width(mainWidth).Render(
			DimStyle.Render("Enter to kill · Esc to cancel"),
		)
		rows = append(rows, footer)
		killPopupView = CmdPopupContainerStyle.Width(mainWidth).Render(
			lipgloss.JoinVertical(lipgloss.Left, rows...),
		)
		// Trim chatContent to make room for the modal.
		killPopupHeight := len(m.killModal.slots) + 1 // +1 for footer
		lines := strings.Split(chatContent, "\n")
		trimTo := len(lines) - killPopupHeight
		if trimTo < 0 {
			trimTo = 0
		}
		chatContent = strings.Join(lines[:trimTo], "\n")
	}

	// Trim chatContent when in prompt option-selection mode to prevent overflow.
	// The prompt widget is taller than the normal input area; subtract the extra lines.
	if m.prompt.promptMode && !m.prompt.promptCustom {
		allOpts := append(m.prompt.promptOptions, "Custom response...")
		// Widget inner content: 1 question + 1 blank + N options + 1 blank + 1 hint = N+4 lines.
		// InputAreaStyle border adds 2 vertical lines. Normal input = inputHeight(3) + 2 = 5 lines.
		promptWidgetHeight := len(allOpts) + 4 + 2
		extraLines := promptWidgetHeight - (inputHeight + 2)
		if extraLines > 0 {
			lines := strings.Split(chatContent, "\n")
			trimTo := len(lines) - extraLines
			if trimTo < 0 {
				trimTo = 0
			}
			chatContent = strings.Join(lines[:trimTo], "\n")
		}
	}

	chatView := ChatAreaStyle.Width(mainWidth).Render(chatContent)

	// Build claude meta strip (shown while a claude stream is active).
	var metaStrip string
	if m.stream.claudeActiveMeta != "" {
		metaStrip = ClaudeMetaStyle.Width(mainWidth).Render("⬡ " + m.stream.claudeActiveMeta)
	}

	// overlayView is whichever popup is active (cmd popup or kill modal), if any.
	overlayView := popupView
	if killPopupView != "" {
		overlayView = killPopupView
	}

	// Join chat + overlay (if any) + meta strip (if any) + input/status vertically.
	var mainColumn string
	if overlayView != "" && metaStrip != "" {
		mainColumn = lipgloss.JoinVertical(lipgloss.Left, chatView, overlayView, metaStrip, inputOrStatus)
	} else if overlayView != "" {
		mainColumn = lipgloss.JoinVertical(lipgloss.Left, chatView, overlayView, inputOrStatus)
	} else if metaStrip != "" {
		mainColumn = lipgloss.JoinVertical(lipgloss.Left, chatView, metaStrip, inputOrStatus)
	} else {
		mainColumn = lipgloss.JoinVertical(lipgloss.Left, chatView, inputOrStatus)
	}

	// Build left panel (if visible).
	var leftPanelView string
	if showLeftPanel {
		leftPanelView = m.renderLeftPanel(lpWidth, m.height)
	}

	// Build a vertical gap spacer (1-column wide, full terminal height) for
	// consistent spacing between adjacent columns. Each line must contain a
	// space character so JoinHorizontal measures it as 1 column wide.
	gapLines := make([]string, m.height)
	for i := range gapLines {
		gapLines[i] = " "
	}
	gap := strings.Join(gapLines, "\n")

	var content string
	if showLeftPanel && showSidebar {
		sidebar := m.renderSidebar(sbWidth)
		content = lipgloss.JoinHorizontal(lipgloss.Top, leftPanelView, gap, mainColumn, gap, sidebar)
	} else if showLeftPanel {
		content = lipgloss.JoinHorizontal(lipgloss.Top, leftPanelView, gap, mainColumn)
	} else if showSidebar {
		sidebar := m.renderSidebar(sbWidth)
		content = lipgloss.JoinHorizontal(lipgloss.Top, mainColumn, gap, sidebar)
	} else {
		content = mainColumn
	}

	// Overlay toast notifications in the top-right corner.
	if len(m.toasts) > 0 {
		toastBlock := m.renderToasts()
		content = overlayToasts(content, toastBlock, m.width)
	}

	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
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

// renderScrollableModal renders a scrollable modal overlay centered on the
// terminal. It computes dimensions, slices content into visible lines, applies
// the scroll offset, truncates lines to the inner width, and styles the box.
// It returns the fully rendered overlay string and the clamped scroll offset
// so the caller can write it back to the model field.
func (m *Model) renderScrollableModal(title, content string, scroll int) (string, int) {
	modalW := m.width * 3 / 4
	modalH := m.height * 3 / 4
	if modalW < 40 {
		modalW = 40
	}
	if modalH < 10 {
		modalH = 10
	}

	// Slice the content into lines, apply scroll offset.
	allLines := strings.Split(content, "\n")
	maxScroll := len(allLines) - modalH + 4
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}

	start := scroll
	end := start + modalH - 4 // -4 for title + footer + borders
	if end > len(allLines) {
		end = len(allLines)
	}
	visibleLines := allLines[start:end]

	// Truncate each line to modal inner width.
	innerW := modalW - 4
	truncated := make([]string, len(visibleLines))
	for i, l := range visibleLines {
		if len(l) > innerW {
			truncated[i] = l[:innerW]
		} else {
			truncated[i] = l
		}
	}

	body := strings.Join(truncated, "\n")
	scrollInfo := fmt.Sprintf("line %d/%d", scroll+1, len(allLines))
	footer := DimStyle.Render("↑↓/jk scroll · ctrl+u/d page · Esc to close · " + scrollInfo)

	modalContent := HeaderStyle.Render(title) + "\n\n" + body + "\n\n" + footer

	modalStyle := lipgloss.NewStyle().
		Width(modalW).
		Height(modalH).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(0, 2)

	modal := modalStyle.Render(modalContent)

	// Place modal centered over the background using lipgloss.Place.
	// WithWhitespaceStyle sets the background of the surrounding area.
	overlaid := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))))

	return overlaid, scroll
}

// isNumberedListItem returns true if line starts with a markdown numbered list
// marker: one or more digits immediately followed by ". " (e.g. "1. ", "12. ").
// This avoids false positives on sentences that merely start with a digit.
func isNumberedListItem(line string) bool {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(line) && line[i] == '.' && line[i+1] == ' '
}

// looksLikeMarkdown returns true if the content appears to contain markdown
// formatting. The check is intentionally broad — false positives (plain text
// rendered as markdown) are acceptable in the fullscreen output modal.
func looksLikeMarkdown(s string) bool {
	if strings.Contains(s, "```") {
		return true
	}
	for _, line := range strings.SplitN(s, "\n", 200) {
		switch {
		case strings.HasPrefix(line, "#"):
			return true
		case strings.HasPrefix(line, "**") || strings.HasPrefix(line, "__"):
			return true
		case strings.HasPrefix(line, "- [") || strings.HasPrefix(line, "* ["):
			return true
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* "):
			return true
		case strings.HasPrefix(line, "> "): // require space after > to avoid matching git diff / log output
			return true
		case isNumberedListItem(line):
			return true
		case strings.Count(line, "|") >= 2: // markdown table needs at least two columns
			return true
		}
	}
	return false
}

// renderOutputModal renders a fullscreen scrollable modal for agent output.
// Unlike renderScrollableModal, it uses nearly the full terminal dimensions,
// renders markdown when detected, and applies distinct styling to tool event lines.
func (m *Model) renderOutputModal(title, content string, scroll int) (string, int) {
	modalW := m.width - 4
	modalH := m.height - 4
	if modalW < 40 {
		modalW = 40
	}
	if modalH < 10 {
		modalH = 10
	}

	innerW := modalW - 4 // account for border + padding

	// Step 1: Strip any pre-existing ANSI escape codes from the raw content so
	// that DimStyle.Render() and Glamour start from clean text. Without this,
	// escape sequences embedded in the content (e.g. from tool output) appear
	// as literal text in the viewport.
	cleanContent := xansi.Strip(content)

	// Step 2: Apply dim styling to tool event lines on the clean content string,
	// before any markdown rendering. This avoids nesting ANSI escape sequences
	// inside Glamour-rendered output (which would corrupt the display).
	rawLines := strings.Split(cleanContent, "\n")
	for i, line := range rawLines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "⚙") || strings.HasPrefix(trimmed, "→") {
			rawLines[i] = DimStyle.Render(line)
		}
	}
	dimmedContent := strings.Join(rawLines, "\n")

	// Step 2: Optionally render markdown on the dimmed content, then split into lines.
	var allLines []string
	if m.outputMdRender != nil && looksLikeMarkdown(dimmedContent) {
		rendered, err := m.outputMdRender.Render(dimmedContent)
		if err == nil {
			allLines = strings.Split(strings.TrimRight(rendered, "\n"), "\n")
		} else {
			slog.Warn("outputMdRender failed, falling back to plain text", "error", err)
		}
	}
	if allLines == nil {
		allLines = strings.Split(dimmedContent, "\n")
	}

	// Compute scroll bounds.
	visibleH := modalH - 4 // -4 for title + footer + borders
	maxScroll := len(allLines) - visibleH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}

	start := scroll
	end := start + visibleH
	if end > len(allLines) {
		end = len(allLines)
	}
	visibleLines := allLines[start:end]

	// Truncate each line to modal inner width using an ANSI-aware truncator so
	// we never slice mid-escape-sequence (which would leave raw codes like
	// "[38;2;98;98;98m" visible in the terminal).
	truncated := make([]string, len(visibleLines))
	for i, l := range visibleLines {
		if lipgloss.Width(l) > innerW {
			truncated[i] = xansi.Truncate(l, innerW, "")
		} else {
			truncated[i] = l
		}
	}

	body := strings.Join(truncated, "\n")
	scrollInfo := fmt.Sprintf("line %d/%d", scroll+1, len(allLines))
	footer := DimStyle.Render("↑↓/jk scroll · ctrl+u/d page · Esc to close · " + scrollInfo)

	modalContent := HeaderStyle.Render(title) + "\n\n" + body + "\n\n" + footer

	modalStyle := lipgloss.NewStyle().
		Width(modalW).
		Height(modalH).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(0, 2)

	modal := modalStyle.Render(modalContent)

	overlaid := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))))

	return overlaid, scroll
}

// indentLines prepends each line of s with n spaces.
func indentLines(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n")
}

// renderMarkdown renders markdown content to styled terminal output.
func (m *Model) renderMarkdown(content string) string {
	if m.mdRender == nil {
		return content
	}
	rendered, err := m.mdRender.Render(content)
	if err != nil {
		return content
	}
	// glamour adds trailing newlines; trim them so we control spacing.
	return strings.TrimRight(rendered, "\n")
}

// toastersStyle returns a Glamour style config based on Dracula with
// code block colors adjusted to match the toasters dark palette.
func toastersStyle() ansi.StyleConfig {
	s := glamourstyles.DraculaStyleConfig

	// Tighten document margin — the chat area already provides padding.
	zero := uint(0)
	s.Document.Margin = &zero

	// Darken code block background to blend with the toasters dark chrome.
	bg := "#1e1e2e"
	s.CodeBlock.Chroma.Background = ansi.StylePrimitive{
		BackgroundColor: &bg,
	}

	return s
}

// ensureMarkdownRenderer creates or recreates the glamour renderer for the current width.
// It also recreates outputMdRender sized for the fullscreen output modal.
func (m *Model) ensureMarkdownRenderer() {
	w := m.chatViewport.Width() - AssistantMsgIndent
	if w < 1 {
		w = 80
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(toastersStyle()),
		glamour.WithWordWrap(w),
	)
	if err == nil {
		m.mdRender = r
	}

	// Output modal renderer: sized for the fullscreen modal inner width.
	// Modal is m.width-4 wide; inner width after border+padding is m.width-8.
	outputW := m.width - 8
	if outputW < 40 {
		outputW = 40
	}
	or, oerr := glamour.NewTermRenderer(
		glamour.WithStyles(toastersStyle()),
		glamour.WithWordWrap(outputW),
	)
	if oerr == nil {
		m.outputMdRender = or
	}
}

// resizeComponents recalculates sizes for viewport and textarea after a resize.
func (m *Model) resizeComponents() {
	showSidebar := m.width >= minWidthForBar && !m.sidebarHidden
	showLeftPanel := m.width >= minWidthForLeftPanel && !m.leftPanelHidden

	sbWidth := sidebarWidth(m.width)
	lpWidth := m.effectiveLeftPanelWidth()

	// Cache for mouse hit-testing.
	m.lpWidth = lpWidth
	m.sbWidth = sbWidth

	const columnGap = 1 // consistent gap between adjacent columns

	var mainWidth int
	if showSidebar && showLeftPanel {
		mainWidth = m.width - lpWidth - sbWidth - 2*columnGap
	} else if showSidebar {
		mainWidth = m.width - sbWidth - columnGap
	} else if showLeftPanel {
		mainWidth = m.width - lpWidth - columnGap
	} else {
		mainWidth = m.width
	}

	// Input takes a fixed height plus its border.
	inputFrameHeight := inputHeight + InputAreaStyle.GetVerticalFrameSize()

	// Chat viewport gets remaining height.
	chatPadding := ChatAreaStyle.GetVerticalPadding()
	vpHeight := m.height - inputFrameHeight - chatPadding
	if vpHeight < 1 {
		vpHeight = 1
	}

	vpWidth := mainWidth - ChatAreaStyle.GetHorizontalPadding() - 1 // -1 reserves space for scrollbar column
	if vpWidth < 1 {
		vpWidth = 1
	}

	m.chatViewport.SetWidth(vpWidth)
	m.chatViewport.SetHeight(vpHeight)

	// Agent viewport mirrors chat viewport dimensions.
	m.agentViewport.SetWidth(vpWidth)
	m.agentViewport.SetHeight(vpHeight)

	// Log view viewport.
	m.resizeLogView()

	m.input.SetWidth(mainWidth - InputAreaStyle.GetHorizontalFrameSize())
	m.input.SetHeight(inputHeight)

	m.ensureMarkdownRenderer()
	m.updateViewportContent()
}

// renderPromptWidget renders the prompt mode input area, replacing the normal textarea.
// In option-selection mode (promptCustom == false) it shows a numbered list of choices.
// In custom-text mode (promptCustom == true) it shows the question above the textarea.
// style is the InputAreaStyle variant to use (may have dimmed borders when unfocused).
func (m Model) renderPromptWidget(width int, style lipgloss.Style) string {
	if m.prompt.promptCustom {
		// Custom text mode: question header above the normal textarea.
		question := HeaderStyle.Render("? " + m.prompt.promptQuestion)
		hint := DimStyle.Render("Enter to submit · Esc to go back")
		inner := lipgloss.JoinVertical(lipgloss.Left, question, m.input.View(), hint)
		return style.Width(width).Render(inner)
	}

	// Option selection mode: numbered list with cursor.
	allOptions := append(m.prompt.promptOptions, "Custom response...")

	var rows []string
	for i, opt := range allOptions {
		label := fmt.Sprintf("%d. %s", i+1, opt)
		if i == m.prompt.promptSelected {
			rows = append(rows, CmdPopupSelectedStyle.Render("▶ "+label))
		} else {
			rows = append(rows, DimStyle.Render("  "+label))
		}
	}

	question := HeaderStyle.Render("? " + m.prompt.promptQuestion)
	optionList := lipgloss.JoinVertical(lipgloss.Left, rows...)
	hint := DimStyle.Render("↑↓ navigate · Enter select · Esc cancel")

	inner := lipgloss.JoinVertical(lipgloss.Left,
		question,
		"",
		optionList,
		"",
		hint,
	)
	return style.Width(width).Render(inner)
}

// updateViewportContent rebuilds the chat history string and sets it on the viewport.
func (m *Model) updateViewportContent() {
	var sb strings.Builder
	contentWidth := m.chatViewport.Width()
	if contentWidth < 1 {
		contentWidth = 40
	}

	// Show welcome message when there's no conversation yet.
	if !m.hasConversation() && !m.stream.streaming {
		// ASCII art: an angry toaster wielding a hammer.
		// Each line is rendered with HeaderStyle so it picks up the accent color.
		const toasterArt = `                     [###]
                       |
                       |
         ___________   |            xxx  
        |  |||  ||| |  O     ______  |
        |           | /|    | w  w | |
        |  {O}  {o} |/ |    | .  . |/|
        |   \_v_/   |  |    |  --- |
        |   -----   |       |______|
        |___________|         |  |
        |___________|
           |     |
           |     |`
		// Render the art as a single block with color but no per-line padding,
		// so lipgloss.Place can measure and center it correctly as a unit.
		artStyled := lipgloss.NewStyle().Foreground(ColorPrimary).Render(toasterArt)
		tagline := DimStyle.Render("Your personal army of toasters to ") + lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render("get shit done.")
		endpoint := DimStyle.Render("Operator connected to " + m.stats.Endpoint)
		hints := DimStyle.Render("Esc to cancel a response · Ctrl+C to quit.")
		block := lipgloss.JoinVertical(lipgloss.Center, artStyled, "", tagline, endpoint, "", hints)

		vpH := m.chatViewport.Height()
		if vpH < 1 {
			vpH = 24
		}
		// Count how many assistant messages (e.g. greeting) will render below.
		hasGreeting := false
		for _, entry := range m.chat.entries {
			if entry.Message.Role == "assistant" && entry.Message.Content != "" {
				hasGreeting = true
				break
			}
		}
		if hasGreeting {
			// When a greeting follows, center the art horizontally but only
			// use the space it needs so the greeting is visible below.
			blockLines := strings.Count(block, "\n") + 1
			topPad := (vpH - blockLines) / 3 // bias toward upper third
			if topPad < 1 {
				topPad = 1
			}
			sb.WriteString(strings.Repeat("\n", topPad))
			for _, line := range strings.Split(block, "\n") {
				sb.WriteString(lipgloss.PlaceHorizontal(contentWidth, lipgloss.Center, line) + "\n")
			}
			sb.WriteString("\n")
		} else {
			welcome := lipgloss.Place(contentWidth, vpH, lipgloss.Center, lipgloss.Center, block)
			sb.WriteString(welcome)
		}
	}

	for i, entry := range m.chat.entries {
		msg := entry.Message
		// Timestamp helper.
		var ts string
		if !entry.Timestamp.IsZero() {
			ts = " · " + entry.Timestamp.Format("3:04 PM")
		}

		switch msg.Role {
		case "user":
			// Completion messages render as collapsible blocks.
			if m.chat.completionMsgIdx[i] {
				firstLine := firstLineOf(msg.Content)
				if m.chat.expandedMsgs[i] {
					hint := ""
					if i == m.chat.selectedMsgIdx {
						hint = DimStyle.Render(" [ctrl+x to collapse]")
					}
					header := DimStyle.Render("▼ "+firstLine) + hint
					sb.WriteString(header + "\n" + renderCompletionBlock(msg.Content) + "\n")
				} else {
					hint := ""
					if i == m.chat.selectedMsgIdx {
						hint = DimStyle.Render(" [ctrl+x to expand]")
					}
					sb.WriteString(DimStyle.Render("▶ "+firstLine) + hint + "\n\n")
				}
				continue
			}
			// Render user message block with optional timestamp.
			blockWidth := contentWidth - UserMsgBlockStyle.GetHorizontalFrameSize()
			if blockWidth < 1 {
				blockWidth = 1
			}
			content := wrapText(msg.Content, blockWidth)
			if ts != "" {
				content += "\n" + DimStyle.Render(ts[3:]) // strip leading " · "
			}
			block := UserMsgBlockStyle.Width(blockWidth).Render(content)
			sb.WriteString(block + "\n\n")
		case "assistant":
			aIndent := strings.Repeat(" ", AssistantMsgIndent)
			// ask-user-prompt and escalate-prompt messages render as a styled question header.
			if entry.ClaudeMeta == "ask-user-prompt" || entry.ClaudeMeta == "escalate-prompt" {
				sb.WriteString(aIndent + HeaderStyle.Render("? "+msg.Content) + "\n\n")
				continue
			}
			// Feed event entries render as styled single-line system events.
			if entry.ClaudeMeta == "feed-event" {
				sb.WriteString(aIndent + msg.Content + "\n\n")
				continue
			}
			// Tool-call indicator messages render as collapsible tool blocks.
			if entry.ClaudeMeta == "tool-call-indicator" {
				if m.chat.collapsedTools[i] {
					// Expanded: show full content with MCP tool names formatted.
					hint := ""
					if i == m.chat.selectedMsgIdx {
						hint = DimStyle.Render(" [ctrl+x to collapse]")
					}
					sb.WriteString(aIndent + DimStyle.Render(formatToolCallContent(msg.Content)) + hint + "\n\n")
				} else {
					// Collapsed (default): show summary line with MCP tool names formatted.
					toolName := formatToolName(extractToolName(msg.Content))
					hint := ""
					if i == m.chat.selectedMsgIdx {
						hint = DimStyle.Render(" [ctrl+x to expand]")
					}
					sb.WriteString(aIndent + DimStyle.Render("⚙ "+toolName+" ▶") + hint + "\n")
				}
				continue
			}
			// Render claude byline (if any) above the response, with timestamp.
			indent := strings.Repeat(" ", AssistantMsgIndent)
			if entry.ClaudeMeta != "" {
				byline := ClaudeBylineStyle.Render("⬡ " + entry.ClaudeMeta)
				if ts != "" {
					byline += DimStyle.Render(ts)
				}
				sb.WriteString(indent + byline + "\n")
			}
			// Render reasoning trace (if any) above the response — only when expanded.
			if entry.Reasoning != "" {
				if m.chat.expandedReasoning[i] {
					sb.WriteString(indentLines(renderReasoningBlock(entry.Reasoning, contentWidth-AssistantMsgIndent), AssistantMsgIndent))
					sb.WriteString("\n")
				} else {
					sb.WriteString(indent + ReasoningStyle.Render("▶ thinking (press ctrl+t to expand)") + "\n\n")
				}
			}
			sb.WriteString(indentLines(m.renderMarkdown(msg.Content), AssistantMsgIndent) + "\n\n")
		case "tool":
			// Render tool result as a collapsible dimmed block.
			if m.chat.collapsedTools[i] {
				// Expanded: show full content.
				preview := msg.Content
				if len(preview) > 300 {
					preview = preview[:300] + "…"
				}
				hint := ""
				if i == m.chat.selectedMsgIdx {
					hint = DimStyle.Render(" [ctrl+x to collapse]")
				}
				sb.WriteString(DimStyle.Render("⚙ tool result: "+preview) + hint + "\n\n")
			} else {
				// Collapsed (default): show summary line.
				hint := ""
				if i == m.chat.selectedMsgIdx {
					hint = DimStyle.Render(" [ctrl+x to expand]")
				}
				sb.WriteString(DimStyle.Render("⚙ tool result ▶") + hint + "\n")
			}
		}
	}

	// Render activity feed entries from SQLite only when no operator is wired
	// (operator events are already rendered via OperatorEventMsg as chat entries).
	if len(m.progress.feedEntries) > 0 && m.operator == nil {
		for _, entry := range m.progress.feedEntries {
			line := formatFeedEntry(entry)
			if line != "" {
				sb.WriteString(line + "\n")
			}
		}
		sb.WriteString("\n")
	}

	// Show streaming response in progress — re-render markdown incrementally.
	if m.stream.streaming {
		streamIndent := strings.Repeat(" ", AssistantMsgIndent)
		// Live reasoning trace while thinking.
		if m.stream.currentReasoning != "" {
			sb.WriteString(indentLines(renderReasoningBlock(m.stream.currentReasoning, contentWidth-AssistantMsgIndent), AssistantMsgIndent))
			sb.WriteString("\n")
		} else {
			sb.WriteString(streamIndent + ReasoningStyle.Render("Thinking...") + "\n\n")
		}
		// Live response content.
		if m.stream.currentResponse != "" {
			sb.WriteString(indentLines(m.renderMarkdown(m.stream.currentResponse), AssistantMsgIndent))
			cursor := string(spinnerChars[m.spinnerFrame%len(spinnerChars)])
			sb.WriteString(StreamingStyle.Render(" " + cursor))
			sb.WriteString("\n\n")
		}
	}

	// Show error if present.
	if m.err != nil {
		sb.WriteString(ErrorStyle.Render("Error: "+m.err.Error()) + "\n\n")
	}

	m.chatViewport.SetContent(sb.String())
}
