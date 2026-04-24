// Settings modal: /settings overlay for viewing and editing user-editable
// runtime settings (values the operator + workers consult at compose-time).
package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/service"
)

// settingsModalState holds state for the /settings modal.
type settingsModalState struct {
	show     bool
	loading  bool
	saving   bool
	err      error
	settings service.Settings // last-loaded, canonical values
	dirty    service.Settings // pending edits
	rowIdx   int              // currently selected row
}

// SettingsLoadedMsg delivers the current settings snapshot to the modal.
type SettingsLoadedMsg struct {
	Settings service.Settings
	Err      error
}

// SettingsSavedMsg delivers the outcome of a save. On success, Settings is
// the just-persisted payload so the modal can sync its canonical copy.
type SettingsSavedMsg struct {
	Settings service.Settings
	Err      error
}

// fetchSettings loads the current settings from the service.
func (m Model) fetchSettings() tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		s, err := svc.System().GetSettings(context.Background())
		return SettingsLoadedMsg{Settings: s, Err: err}
	}
}

// saveSettings persists the given settings via the service.
func (m Model) saveSettings(next service.Settings) tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		err := svc.System().UpdateSettings(context.Background(), next)
		return SettingsSavedMsg{Settings: next, Err: err}
	}
}

// settingsRow describes one editable row in the /settings modal.
type settingsRow struct {
	label string
	desc  string
	// get returns the row's current value from the given Settings snapshot.
	get func(*service.Settings) string
	// set writes value into the given Settings snapshot.
	set func(*service.Settings, string)
	// options returns the allowed values in display order.
	options func() []string
}

// settingsRows is the ordered list of rows rendered in the modal. Adding a
// new setting is just a matter of appending a row here.
var settingsRows = []settingsRow{
	{
		label:   "Coarse Granularity",
		desc:    "How large the tasks emitted by coarse-decompose are.",
		get:     func(s *service.Settings) string { return s.CoarseGranularity },
		set:     func(s *service.Settings, v string) { s.CoarseGranularity = v },
		options: config.GranularityLevels,
	},
	{
		label:   "Fine Granularity",
		desc:    "How finely fine-decompose slices a task into subtasks / graph nodes.",
		get:     func(s *service.Settings) string { return s.FineGranularity },
		set:     func(s *service.Settings, v string) { s.FineGranularity = v },
		options: config.GranularityLevels,
	},
}

// updateSettingsModal handles key presses while the settings modal is open.
func (m *Model) updateSettingsModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.settingsModal.show = false
		return m, nil

	case "up":
		if m.settingsModal.rowIdx > 0 {
			m.settingsModal.rowIdx--
		}
		return m, nil

	case "down":
		if m.settingsModal.rowIdx < len(settingsRows)-1 {
			m.settingsModal.rowIdx++
		}
		return m, nil

	case "left":
		m.cycleSettingAt(m.settingsModal.rowIdx, -1)
		return m, nil

	case "right":
		m.cycleSettingAt(m.settingsModal.rowIdx, 1)
		return m, nil

	case "enter":
		if m.settingsModal.saving {
			return m, nil
		}
		if m.settingsModal.dirty == m.settingsModal.settings {
			return m, nil
		}
		m.settingsModal.saving = true
		m.settingsModal.err = nil
		return m, m.saveSettings(m.settingsModal.dirty)
	}
	return m, nil
}

// cycleSettingAt nudges the row's value by step (-1 or +1) through its
// allowed options, wrapping at the ends.
func (m *Model) cycleSettingAt(row, step int) {
	if row < 0 || row >= len(settingsRows) {
		return
	}
	r := settingsRows[row]
	opts := r.options()
	if len(opts) == 0 {
		return
	}
	cur := r.get(&m.settingsModal.dirty)
	idx := indexOf(opts, cur)
	if idx < 0 {
		idx = 0
	}
	idx = (idx + step + len(opts)) % len(opts)
	r.set(&m.settingsModal.dirty, opts[idx])
}

func indexOf(xs []string, v string) int {
	for i, x := range xs {
		if x == v {
			return i
		}
	}
	return -1
}

// renderSettingsModal renders the /settings overlay.
func (m *Model) renderSettingsModal() string {
	modalW := m.width - 4
	if modalW > 70 {
		modalW = 70
	}
	if modalW > m.width {
		modalW = m.width
	}
	innerW := modalW - ModalStyle.GetHorizontalFrameSize()
	if innerW < 20 {
		innerW = 20
	}

	var lines []string
	lines = append(lines, gradientText("Settings", [3]uint8{100, 150, 255}, [3]uint8{50, 200, 255}))
	lines = append(lines, DimStyle.Render(strings.Repeat("─", innerW)))
	lines = append(lines, "")

	if m.settingsModal.loading {
		lines = append(lines, DimStyle.Render("Loading..."))
	} else if m.settingsModal.err != nil {
		lines = append(lines, ErrorStyle.Render("Error: "+m.settingsModal.err.Error()))
		lines = append(lines, "")
	}

	for i, r := range settingsRows {
		lines = append(lines, m.renderSettingsRow(r, i == m.settingsModal.rowIdx))
		lines = append(lines, DimStyle.Render("  "+r.desc))
		if i < len(settingsRows)-1 {
			lines = append(lines, "")
		}
	}
	lines = append(lines, "")

	// Dirty/saved indicator.
	if m.settingsModal.saving {
		lines = append(lines, DimStyle.Render("Saving..."))
	} else if m.settingsModal.dirty != m.settingsModal.settings {
		lines = append(lines, DimStyle.Render("Unsaved changes — press Enter to save."))
	}

	lines = append(lines, "")

	footer := lipgloss.JoinHorizontal(lipgloss.Left,
		DimStyle.Render("[←/→] Cycle"), "  ",
		DimStyle.Render("[↑/↓] Navigate"), "  ",
		DimStyle.Render("[Enter] Save"), "  ",
		DimStyle.Render("[Esc] Close"),
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

// renderSettingsRow renders a single label + inline chip selector.
func (m *Model) renderSettingsRow(r settingsRow, selected bool) string {
	current := r.get(&m.settingsModal.dirty)
	if current == "" {
		// Fall back to the defaulted value so the chip row always has a
		// highlighted entry.
		if opts := r.options(); len(opts) > 0 {
			current = opts[len(opts)/2]
		}
	}

	var chips []string
	for _, lvl := range r.options() {
		chip := fmt.Sprintf(" %s ", lvl)
		if lvl == current {
			chips = append(chips, ModalSelectedStyle.Render(chip))
		} else {
			chips = append(chips, DimStyle.Render(chip))
		}
	}
	chipRow := strings.Join(chips, " ")

	marker := "  "
	if selected {
		marker = "▶ "
	}
	labelLine := marker + r.label
	if selected {
		labelLine = HeaderStyle.Render(labelLine)
	}

	return labelLine + "\n  " + chipRow
}
