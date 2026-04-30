// Presets modal: hardcoded picker for the user's recurring test prompts.
// Selecting a preset populates the input and dispatches it as a job.
package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// presetPrompts is the hardcoded list shown by the /presets modal. Order
// is the display order; the keys "1", "2", … select directly.
var presetPrompts = []string{
	"Create a simple Hello World CLI program in Go.",
	"Create a To-Do management web app. The backend should be in Go and SQLite. The frontend should be vanilla HTML, CSS, and TypeScript. The entire app should be packaged as a Docker container that serves up both the API and the frontend.",
}

// presetsModalState holds state for the /presets modal.
type presetsModalState struct {
	show        bool
	selectedIdx int
}

// updatePresetsModal handles key presses while the presets modal is open.
// On confirmation, the preset is dropped into the input box and sent as a
// job request, mirroring the /job <prompt> path.
func (m *Model) updatePresetsModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.presetsModal.show = false
		return m, nil

	case "up":
		if m.presetsModal.selectedIdx > 0 {
			m.presetsModal.selectedIdx--
		}
		return m, nil

	case "down":
		if m.presetsModal.selectedIdx < len(presetPrompts)-1 {
			m.presetsModal.selectedIdx++
		}
		return m, nil

	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx := int(msg.String()[0] - '1')
		if idx >= 0 && idx < len(presetPrompts) {
			return m.dispatchPreset(idx)
		}
		return m, nil

	case "enter":
		return m.dispatchPreset(m.presetsModal.selectedIdx)
	}
	return m, nil
}

// dispatchPreset closes the modal, populates the input with the selected
// preset prefixed for the operator's job-request classifier, and sends it.
func (m *Model) dispatchPreset(idx int) (tea.Model, tea.Cmd) {
	if idx < 0 || idx >= len(presetPrompts) {
		return m, nil
	}
	m.presetsModal.show = false
	m.input.SetValue("[JOB REQUEST] " + presetPrompts[idx])
	return m, m.sendMessage()
}

// renderPresetsModal renders the centered overlay listing the presets.
func (m *Model) renderPresetsModal() string {
	modalW := m.width - 4
	if modalW > 80 {
		modalW = 80
	}
	if modalW > m.width {
		modalW = m.width
	}

	innerW := modalW - ModalStyle.GetHorizontalFrameSize()
	if innerW < 10 {
		innerW = 10
	}

	var lines []string
	lines = append(lines, gradientText("Preset Prompts", [3]uint8{100, 150, 255}, [3]uint8{50, 200, 255}))
	lines = append(lines, DimStyle.Render(strings.Repeat("─", innerW)))
	lines = append(lines, "")
	lines = append(lines, DimStyle.Render("Pick a preset to send as a job request:"))
	lines = append(lines, "")

	for i, p := range presetPrompts {
		label := wrapText(p, innerW-6)
		head := indexedLine(i+1, label, innerW)
		if i == m.presetsModal.selectedIdx {
			head = ModalSelectedStyle.Width(innerW).Render(head)
		}
		lines = append(lines, head)
		lines = append(lines, "")
	}

	footer := lipgloss.JoinHorizontal(lipgloss.Left,
		DimStyle.Render("[↑↓] Navigate"), "  ",
		DimStyle.Render("[1–9] Quick-pick"), "  ",
		DimStyle.Render("[Enter] Send"), "  ",
		DimStyle.Render("[Esc] Cancel"),
	)
	lines = append(lines, footer)

	content := strings.Join(lines, "\n")
	modal := ModalStyle.Width(modalW).Render(content)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}

// indexedLine renders "  <n>. <text>" with the index dimmed and the text
// flowed across the available width. The text is already wrapped by the
// caller; this just stitches the prefix on.
func indexedLine(n int, text string, _ int) string {
	prefix := DimStyle.Render(itoa(n) + ".")
	first, rest, hasRest := strings.Cut(text, "\n")
	out := "  " + prefix + " " + first
	if hasRest {
		pad := strings.Repeat(" ", 5)
		for _, line := range strings.Split(rest, "\n") {
			out += "\n" + pad + line
		}
	}
	return out
}
