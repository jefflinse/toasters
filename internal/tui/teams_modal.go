// Teams modal: team management UI including rendering, key handling, coordinator auto-detection, and auto-team promotion.
package tui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// teamsModalState holds all state for the /teams modal overlay.
type teamsModalState struct {
	show              bool
	teams             []service.TeamView // local copy for the modal; separate from m.teams
	teamIdx           int                // selected team in left panel
	agentIdx          int                // selected agent in right panel (for 'c' key)
	focus             int                // 0=left panel, 1=right panel
	nameInput         string             // text being typed for new team name
	inputMode         bool               // true when typing a new team name
	confirmDelete     bool               // true when delete confirmation is showing
	autoDetectPending map[string]bool    // keyed by team Dir; prevents re-firing
	autoDetecting     bool               // true while LLM call is in flight
	promoting         bool               // true while auto-team promotion is in flight

	// Picker sub-modal: select an agent to add to the current team.
	pickerMode   bool            // true when the add-agent picker overlay is active
	pickerAgents []service.Agent // agents available to add (filtered: not already in team)
	pickerIdx    int             // currently highlighted picker item

	// LLM generation state.
	generateMode   bool            // true when user is typing a generation prompt
	generateInput  string          // the prompt text being typed
	generating     bool            // true while LLM call is in flight
	generateAgents []service.Agent // available agents captured when generateMode was entered
}

// teamPromotedMsg is sent when the async auto-team promotion finishes.
type teamPromotedMsg struct {
	teamName string
	err      error
}

// promoteAutoTeamCmd wraps the service PromoteTeam call as a tea.Cmd.
func (m *Model) promoteAutoTeamCmd(tv service.TeamView) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := m.svc.Definitions().PromoteTeam(ctx, tv.Team.ID)
		if err != nil {
			return teamPromotedMsg{teamName: tv.Name(), err: err}
		}
		// The service will push an operation.completed event; we also send
		// a teamPromotedMsg so the modal can update immediately.
		return teamPromotedMsg{teamName: tv.Name()}
	}
}

// ---------------------------------------------------------------------------
// Team classification helpers (local equivalents of service-internal helpers)
// ---------------------------------------------------------------------------

var (
	cachedHomeDir     string
	cachedHomeDirOnce sync.Once
)

func getCachedHomeDir() string {
	cachedHomeDirOnce.Do(func() {
		cachedHomeDir, _ = os.UserHomeDir()
	})
	return cachedHomeDir
}

// isReadOnlyTeam returns true if the team's directory is one of the well-known
// auto-detected read-only directories (e.g. ~/.claude/agents).
func isReadOnlyTeam(tv service.TeamView) bool {
	home := getCachedHomeDir()
	if home == "" {
		return false
	}
	readOnlyDirs := []string{
		filepath.Join(home, ".config", "opencode", "agents"),
		filepath.Join(home, ".claude", "agents"),
	}
	for _, d := range readOnlyDirs {
		if tv.Dir() == d {
			return true
		}
	}
	return false
}

// isSystemTeam returns true if the team's directory is under the system directory.
func isSystemTeam(tv service.TeamView, configDir string) bool {
	systemDir := filepath.Join(configDir, "system")
	return strings.HasPrefix(tv.Dir(), systemDir+string(filepath.Separator))
}

// isAutoTeam returns true if the team is auto-detected.
func isAutoTeam(tv service.TeamView) bool {
	if isReadOnlyTeam(tv) {
		return true
	}
	if tv.IsAuto() {
		return true
	}
	_, err := os.Stat(filepath.Join(tv.Dir(), ".auto-team"))
	return err == nil
}

// updateTeamsModal handles all key presses when the teams modal is open.
func (m *Model) updateTeamsModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var modalCmds []tea.Cmd

	// When typing a generation prompt, intercept all keys.
	if m.teamsModal.generateMode {
		switch msg.String() {
		case "esc":
			m.teamsModal.generateMode = false
			m.teamsModal.generateInput = ""
		case "enter":
			if strings.TrimSpace(m.teamsModal.generateInput) != "" {
				m.teamsModal.generating = true
				m.teamsModal.generateMode = false
				prompt := m.teamsModal.generateInput
				m.teamsModal.generateInput = ""
				return m, m.generateTeamAsync(prompt)
			}
		case "backspace":
			if len(m.teamsModal.generateInput) > 0 {
				runes := []rune(m.teamsModal.generateInput)
				m.teamsModal.generateInput = string(runes[:len(runes)-1])
			}
		default:
			if msg.Text != "" {
				m.teamsModal.generateInput += msg.Text
			}
		}
		return m, tea.Batch(modalCmds...)
	}

	// When typing a new team name, only esc/enter/backspace have special
	// meaning. Everything else — including named keys like "space" — feeds
	// into the name input via msg.Text (which is the actual typed character
	// for all printable input, unlike msg.String() which returns key names).
	if m.teamsModal.inputMode {
		switch msg.String() {
		case "esc":
			m.teamsModal.inputMode = false
			m.teamsModal.nameInput = ""
		case "enter":
			name := strings.TrimSpace(m.teamsModal.nameInput)
			valid := name != "" && !strings.ContainsAny(name, "/\\.\n\r:")
			if valid {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, err := m.svc.Definitions().CreateTeam(ctx, name)
				cancel()
				if err != nil {
					slog.Error("failed to create team directory", "name", name, "error", err)
				} else {
					m.reloadTeamsForModal()
					for i, t := range m.teamsModal.teams {
						if t.Name() == name {
							m.teamsModal.teamIdx = i
							break
						}
					}
				}
			}
			m.teamsModal.inputMode = false
			m.teamsModal.nameInput = ""
		case "backspace":
			if len(m.teamsModal.nameInput) > 0 {
				runes := []rune(m.teamsModal.nameInput)
				m.teamsModal.nameInput = string(runes[:len(runes)-1])
			}
		default:
			// msg.Text is the actual typed character(s); empty for
			// non-printable keys (arrows, function keys, etc.).
			if msg.Text != "" {
				m.teamsModal.nameInput += msg.Text
			}
		}
		return m, tea.Batch(modalCmds...)
	}

	// Picker mode: intercept navigation and selection keys.
	if m.teamsModal.pickerMode {
		switch msg.String() {
		case "esc":
			m.teamsModal.pickerMode = false
			m.teamsModal.pickerAgents = nil
			m.teamsModal.pickerIdx = 0
		case "up":
			if m.teamsModal.pickerIdx > 0 {
				m.teamsModal.pickerIdx--
			}
		case "down":
			if m.teamsModal.pickerIdx < len(m.teamsModal.pickerAgents)-1 {
				m.teamsModal.pickerIdx++
			}
		case "enter":
			if len(m.teamsModal.pickerAgents) > 0 && m.teamsModal.pickerIdx < len(m.teamsModal.pickerAgents) {
				agent := m.teamsModal.pickerAgents[m.teamsModal.pickerIdx]
				if agent.SourcePath == "" {
					modalCmds = append(modalCmds, m.addToast("Cannot add agent: source file unknown", toastWarning))
				} else if len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
					tv := m.teamsModal.teams[m.teamsModal.teamIdx]
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					err := m.svc.Definitions().AddAgentToTeam(ctx, tv.Team.ID, agent.ID)
					cancel()
					if err != nil {
						modalCmds = append(modalCmds, m.addToast("⚠ Add failed: "+err.Error(), toastWarning))
					} else {
						m.teamsModal.pickerMode = false
						m.teamsModal.pickerAgents = nil
						m.teamsModal.pickerIdx = 0
						m.reloadTeamsForModal()
						modalCmds = append(modalCmds, m.addToast("✓ Added '"+agent.Name+"' to team", toastSuccess))
					}
				}
			}
		}
		return m, tea.Batch(modalCmds...)
	}

	switch msg.String() {
	case "esc":
		if m.teamsModal.confirmDelete {
			m.teamsModal.confirmDelete = false
		} else {
			m.teamsModal.show = false
		}

	case "tab":
		if !m.teamsModal.inputMode {
			if m.teamsModal.focus == 0 {
				m.teamsModal.focus = 1
			} else {
				m.teamsModal.focus = 0
			}
		}

	case "up":
		if m.teamsModal.focus == 0 {
			if m.teamsModal.teamIdx > 0 {
				m.teamsModal.teamIdx--
			}
			m.teamsModal.confirmDelete = false
			m.teamsModal.agentIdx = 0
			if len(m.teamsModal.teams) > 0 {
				modalCmds = append(modalCmds, m.maybeAutoDetectCoordinator(m.teamsModal.teams[m.teamsModal.teamIdx]))
			}
		} else {
			// Right panel: navigate agents (coordinator first, then workers).
			if len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
				if m.teamsModal.agentIdx > 0 {
					m.teamsModal.agentIdx--
				}
			}
		}

	case "down":
		if m.teamsModal.focus == 0 {
			if m.teamsModal.teamIdx < len(m.teamsModal.teams)-1 {
				m.teamsModal.teamIdx++
			}
			m.teamsModal.confirmDelete = false
			m.teamsModal.agentIdx = 0
			if len(m.teamsModal.teams) > 0 {
				modalCmds = append(modalCmds, m.maybeAutoDetectCoordinator(m.teamsModal.teams[m.teamsModal.teamIdx]))
			}
		} else {
			// Right panel: navigate agents (coordinator first, then workers).
			if len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
				tv := m.teamsModal.teams[m.teamsModal.teamIdx]
				total := len(tv.Workers)
				if tv.Coordinator != nil {
					total++
				}
				if m.teamsModal.agentIdx < total-1 {
					m.teamsModal.agentIdx++
				}
			}
		}

	case "ctrl+n":
		if m.teamsModal.focus == 0 {
			// Creating a new team is never gated on the selected team's
			// read-only status — you can always create a new user-defined team.
			m.teamsModal.inputMode = true
			m.teamsModal.nameInput = ""
		}

	case "ctrl+d":
		if m.teamsModal.focus == 0 && !m.teamsModal.confirmDelete {
			if len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
				if !isReadOnlyTeam(m.teamsModal.teams[m.teamsModal.teamIdx]) {
					m.teamsModal.confirmDelete = true
				}
			}
		}

	case "enter":
		if m.teamsModal.confirmDelete {
			if len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
				tv := m.teamsModal.teams[m.teamsModal.teamIdx]
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := m.svc.Definitions().DeleteTeam(ctx, tv.Team.ID)
				cancel()
				if err != nil {
					slog.Error("failed to delete team", "team", tv.Name(), "error", err)
					modalCmds = append(modalCmds, m.addToast("⚠ Delete failed: "+err.Error(), toastWarning))
				}
			}
			m.reloadTeamsForModal()
			if m.teamsModal.teamIdx >= len(m.teamsModal.teams) && len(m.teamsModal.teams) > 0 {
				m.teamsModal.teamIdx = len(m.teamsModal.teams) - 1
			} else if len(m.teamsModal.teams) == 0 {
				m.teamsModal.teamIdx = 0
			}
			m.teamsModal.confirmDelete = false
		}

	case "ctrl+k":
		if m.teamsModal.focus == 1 && len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
			tv := m.teamsModal.teams[m.teamsModal.teamIdx]
			if !isReadOnlyTeam(tv) {
				// Build the ordered agent list: coordinator first, then workers.
				var agentList []service.Agent
				if tv.Coordinator != nil {
					agentList = append(agentList, *tv.Coordinator)
				}
				agentList = append(agentList, tv.Workers...)
				if m.teamsModal.agentIdx < len(agentList) {
					target := agentList[m.teamsModal.agentIdx]
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					err := m.svc.Definitions().SetCoordinator(ctx, tv.Team.ID, target.Name)
					cancel()
					if err != nil {
						slog.Error("failed to set coordinator", "team", tv.Name(), "agent", target.Name, "error", err)
						modalCmds = append(modalCmds, m.addToast("⚠ Set coordinator failed: "+err.Error(), toastWarning))
					} else {
						m.reloadTeamsForModal()
						modalCmds = append(modalCmds, m.addToast("✓ Coordinator set to '"+target.Name+"'", toastSuccess))
					}
				}
			}
		}

	case "ctrl+p":
		if m.teamsModal.focus == 0 && len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
			tv := m.teamsModal.teams[m.teamsModal.teamIdx]
			if isAutoTeam(tv) && !isSystemTeam(tv, m.configDir) && !m.teamsModal.promoting {
				m.teamsModal.promoting = true
				modalCmds = append(modalCmds, m.promoteAutoTeamCmd(tv))
			}
		}

	case "ctrl+a":
		// Open the add-agent picker when a non-system, non-read-only team is selected.
		if !m.teamsModal.inputMode && len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
			tv := m.teamsModal.teams[m.teamsModal.teamIdx]
			if !isReadOnlyTeam(tv) && !isSystemTeam(tv, m.configDir) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				allAgents, err := m.svc.Definitions().ListAgents(ctx)
				cancel()
				if err != nil {
					slog.Error("failed to list agents for picker", "error", err)
					modalCmds = append(modalCmds, m.addToast("⚠ Failed to load agents", toastWarning))
				} else {
					available := filterAgentsForTeam(tv, allAgents)
					if len(available) == 0 {
						modalCmds = append(modalCmds, m.addToast("No additional agents available", toastInfo))
					} else {
						m.teamsModal.pickerMode = true
						m.teamsModal.pickerAgents = available
						m.teamsModal.pickerIdx = 0
					}
				}
			}
		}

	case "ctrl+g":
		// Enter LLM generation mode (only when idle and not in any sub-mode).
		if !m.teamsModal.inputMode && !m.teamsModal.generating && !m.teamsModal.pickerMode {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			allAgents, err := m.svc.Definitions().ListAgents(ctx)
			cancel()
			if err != nil {
				slog.Error("failed to list agents for team generation", "error", err)
				modalCmds = append(modalCmds, m.addToast("⚠ Failed to load agents", toastWarning))
			} else {
				m.teamsModal.generateMode = true
				m.teamsModal.generateInput = ""
				m.teamsModal.generateAgents = allAgents
			}
		}
	}
	return m, tea.Batch(modalCmds...)
}

// generateTeamAsync calls the service to generate a team asynchronously.
func (m *Model) generateTeamAsync(prompt string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, err := m.svc.Definitions().GenerateTeam(ctx, prompt)
		if err != nil {
			return teamGeneratedMsg{err: err}
		}
		return nil
	}
}

// filterAgentsForTeam returns agents from available that are not already in the
// team (neither coordinator nor workers). Comparison is by agent name (case-sensitive).
func filterAgentsForTeam(tv service.TeamView, available []service.Agent) []service.Agent {
	inTeam := make(map[string]bool)
	if tv.Coordinator != nil {
		inTeam[tv.Coordinator.Name] = true
	}
	for _, w := range tv.Workers {
		inTeam[w.Name] = true
	}
	var result []service.Agent
	for _, a := range available {
		if !inTeam[a.Name] {
			result = append(result, a)
		}
	}
	return result
}

// reloadTeamsForModal refreshes m.teamsModal.teams from the service.
func (m *Model) reloadTeamsForModal() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	teams, err := m.svc.Definitions().ListTeams(ctx)
	if err != nil {
		slog.Error("failed to reload teams for modal", "error", err)
		return
	}
	m.teamsModal.teams = teams
}

// maybeAutoDetectCoordinator fires a service DetectCoordinator call for the team
// if the team has no coordinator, is not read-only, and hasn't been attempted yet.
func (m *Model) maybeAutoDetectCoordinator(tv service.TeamView) tea.Cmd {
	if isReadOnlyTeam(tv) {
		return nil
	}
	if tv.Coordinator != nil {
		return nil
	}
	if len(tv.Workers) == 0 {
		return nil
	}
	if m.teamsModal.autoDetectPending == nil {
		m.teamsModal.autoDetectPending = make(map[string]bool)
	}
	if m.teamsModal.autoDetectPending[tv.Dir()] {
		return nil
	}
	m.teamsModal.autoDetectPending[tv.Dir()] = true
	m.teamsModal.autoDetecting = true

	teamID := tv.Team.ID
	teamDir := tv.Dir()

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := m.svc.Definitions().DetectCoordinator(ctx, teamID)
		if err != nil {
			return TeamsAutoDetectDoneMsg{teamDir: teamDir, err: err}
		}
		// The service will push an operation.completed event with Kind == "detect_coordinator".
		// We send a TeamsAutoDetectDoneMsg with no agentName so the modal clears autoDetecting.
		return TeamsAutoDetectDoneMsg{teamDir: teamDir}
	}
}

// renderTeamsModal renders the full-screen teams management modal.
func (m *Model) renderTeamsModal() string {
	teams := m.teamsModal.teams

	// Modal dimensions: use most of the terminal.
	modalW := m.width - 4
	if modalW < 60 {
		modalW = 60
	}
	if modalW > m.width {
		modalW = m.width
	}
	modalH := m.height - 4
	if modalH < 20 {
		modalH = 20
	}

	// Inner width after modal border + padding (border=2, padding=2 each side).
	innerW := modalW - ModalStyle.GetHorizontalFrameSize()
	if innerW < 10 {
		innerW = 10
	}

	// Left panel: ~32 chars inner content.
	leftInnerW := 30
	leftPanelW := leftInnerW + ModalPanelStyle.GetHorizontalFrameSize()
	if leftPanelW > innerW/2 {
		leftPanelW = innerW / 2
		leftInnerW = leftPanelW - ModalPanelStyle.GetHorizontalFrameSize()
	}

	// Right panel: remaining width.
	rightPanelW := innerW - leftPanelW - 1 // -1 for spacing
	rightInnerW := rightPanelW - ModalPanelStyle.GetHorizontalFrameSize()
	if rightInnerW < 5 {
		rightInnerW = 5
	}

	// Panel inner height (subtract border + footer line).
	footerLines := 1
	panelH := modalH - ModalStyle.GetVerticalFrameSize() - footerLines - 1
	if panelH < 5 {
		panelH = 5
	}
	panelInnerH := panelH - ModalPanelStyle.GetVerticalFrameSize()
	if panelInnerH < 3 {
		panelInnerH = 3
	}

	// --- Left panel: team list ---
	var leftLines []string
	for i, t := range teams {
		var icon string
		if t.Coordinator != nil {
			icon = "◆"
		} else {
			icon = "■"
		}
		name := truncateStr(t.Name(), leftInnerW-4)
		line := fmt.Sprintf(" %s %s", icon, name)
		// Append badges for system, auto, and read-only teams.
		if isSystemTeam(t, m.configDir) {
			line += " ⚙"
		} else if isAutoTeam(t) {
			line += " ↻"
		}
		if isReadOnlyTeam(t) {
			line += " 🔒"
		}
		if i == m.teamsModal.teamIdx {
			line = ModalSelectedStyle.Width(leftInnerW).Render(line)
		} else if isReadOnlyTeam(t) {
			line = ModalReadOnlyStyle.Render(line)
		}
		leftLines = append(leftLines, line)
	}

	// Input mode: show name-entry prompt at the bottom.
	if m.teamsModal.inputMode {
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, DimStyle.Render("> New team name:"))
		cursor := m.teamsModal.nameInput + "█"
		leftLines = append(leftLines, "  "+cursor)
	}

	// Generate mode: show generation prompt input at the bottom.
	if m.teamsModal.generateMode {
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, DimStyle.Render("> Describe the team:"))
		cursor := m.teamsModal.generateInput + "█"
		leftLines = append(leftLines, "  "+cursor)
	}

	// Generating: show status indicator at the bottom.
	if m.teamsModal.generating {
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, DimStyle.Render("⟳ Generating team..."))
	}

	// Promoting: show status indicator at the bottom.
	if m.teamsModal.promoting {
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, DimStyle.Render("⟳ Promoting team..."))
	}

	// Pad left panel to fill height.
	for len(leftLines) < panelInnerH {
		leftLines = append(leftLines, "")
	}
	if len(leftLines) > panelInnerH {
		leftLines = leftLines[:panelInnerH]
	}

	leftContent := strings.Join(leftLines, "\n")
	var leftPanel string
	if m.teamsModal.focus == 0 {
		leftPanel = ModalFocusedPanel.Width(leftPanelW).Height(panelH).Render(leftContent)
	} else {
		leftPanel = ModalPanelStyle.Width(leftPanelW).Height(panelH).Render(leftContent)
	}

	// --- Right panel: team detail or picker overlay ---
	var rightLines []string
	if m.teamsModal.pickerMode {
		// Picker overlay: show selectable list of agents to add.
		rightLines = append(rightLines, HeaderStyle.Render("Select an agent to add:"))
		rightLines = append(rightLines, DimStyle.Render(strings.Repeat("─", rightInnerW)))
		rightLines = append(rightLines, "")

		if len(m.teamsModal.pickerAgents) == 0 {
			rightLines = append(rightLines, DimStyle.Render("No agents available."))
		} else {
			// Compute scroll window so selected item stays visible.
			agentAreaH := panelInnerH - 3 // 3 lines for header + separator + blank
			if agentAreaH < 1 {
				agentAreaH = 1
			}
			scrollOffset := 0
			if len(m.teamsModal.pickerAgents) > agentAreaH {
				scrollOffset = m.teamsModal.pickerIdx - agentAreaH/2
				if scrollOffset < 0 {
					scrollOffset = 0
				}
				if scrollOffset > len(m.teamsModal.pickerAgents)-agentAreaH {
					scrollOffset = len(m.teamsModal.pickerAgents) - agentAreaH
				}
			}
			end := scrollOffset + agentAreaH
			if end > len(m.teamsModal.pickerAgents) {
				end = len(m.teamsModal.pickerAgents)
			}
			for vi, a := range m.teamsModal.pickerAgents[scrollOffset:end] {
				i := vi + scrollOffset
				icon := "■"
				if a.Mode == "lead" {
					icon = "◆"
				}
				line := fmt.Sprintf(" %s %s", icon, truncateStr(a.Name, rightInnerW-4))
				if i == m.teamsModal.pickerIdx {
					line = ModalSelectedStyle.Width(rightInnerW).Render(line)
				}
				rightLines = append(rightLines, line)
			}
		}
	} else if len(teams) == 0 {
		rightLines = append(rightLines, DimStyle.Render("No teams configured."))
		rightLines = append(rightLines, DimStyle.Render("Press [Ctrl+N] to create one."))
	} else if m.teamsModal.teamIdx < len(teams) {
		tv := teams[m.teamsModal.teamIdx]

		// Header with badges.
		headerText := truncateStr(tv.Name(), rightInnerW-12)
		if isSystemTeam(tv, m.configDir) {
			headerText += " " + DimStyle.Render("⚙ system")
		} else if isAutoTeam(tv) {
			headerText += " " + DimStyle.Render("↻ auto")
		}
		rightLines = append(rightLines, HeaderStyle.Render(headerText))
		rightLines = append(rightLines, DimStyle.Render(strings.Repeat("─", rightInnerW)))

		// Description line (if available).
		if tv.Description() != "" {
			rightLines = append(rightLines, DimStyle.Render(truncateStr(tv.Description(), rightInnerW)))
		}

		// Promote hint for auto-teams.
		if isAutoTeam(tv) && !isSystemTeam(tv, m.configDir) {
			rightLines = append(rightLines, DimStyle.Render("⇧ Ctrl+P to promote to managed team"))
		}

		// Coordinator line.
		coordName := "(none)"
		if tv.Coordinator != nil {
			coordName = tv.Coordinator.Name
		}
		coordLine := "Coordinator: " + coordName
		if m.teamsModal.autoDetecting {
			coordLine += DimStyle.Render(" [detecting...]")
		}
		rightLines = append(rightLines, coordLine)

		// Team composition info from Team fields.
		if len(tv.Team.Skills) > 0 {
			rightLines = append(rightLines, DimStyle.Render("Skills: "+strings.Join(tv.Team.Skills, ", ")))
		}
		if tv.Team.Provider != "" || tv.Team.Model != "" {
			pmLine := ""
			if tv.Team.Provider != "" {
				pmLine = tv.Team.Provider
			}
			if tv.Team.Model != "" {
				if pmLine != "" {
					pmLine += "/"
				}
				pmLine += tv.Team.Model
			}
			rightLines = append(rightLines, DimStyle.Render("Provider: "+truncateStr(pmLine, rightInnerW-10)))
		}
		if tv.Team.Culture != "" {
			rightLines = append(rightLines, DimStyle.Render("Culture:"))
			cultureLines := strings.SplitN(tv.Team.Culture, "\n", 4)
			for i, cl := range cultureLines {
				if i >= 3 {
					break
				}
				cl = strings.TrimSpace(cl)
				if cl != "" {
					rightLines = append(rightLines, DimStyle.Render("  "+truncateStr(cl, rightInnerW-2)))
				}
			}
		}
		rightLines = append(rightLines, "")

		// Build ordered agent list for right panel: coordinator first, then workers.
		var agentList []service.Agent
		if tv.Coordinator != nil {
			agentList = append(agentList, *tv.Coordinator)
		}
		agentList = append(agentList, tv.Workers...)

		// Workers section — scroll a window around the selected agent so long
		// lists don't get clipped by the panel height.
		rightLines = append(rightLines, fmt.Sprintf("Workers (%d)", len(tv.Workers)))
		// How many lines are left for agents after the header rows we've already
		// rendered (rightLines so far) and optional confirm (2).
		confirmExtra := 0
		if m.teamsModal.confirmDelete {
			confirmExtra = 2
		}
		agentAreaH := panelInnerH - len(rightLines) - confirmExtra
		if agentAreaH < 1 {
			agentAreaH = 1
		}
		// Compute scroll offset so selected agent is always visible.
		scrollOffset := 0
		if len(agentList) > agentAreaH {
			scrollOffset = m.teamsModal.agentIdx - agentAreaH/2
			if scrollOffset < 0 {
				scrollOffset = 0
			}
			if scrollOffset > len(agentList)-agentAreaH {
				scrollOffset = len(agentList) - agentAreaH
			}
		}
		visibleAgents := agentList
		if scrollOffset > 0 || len(agentList) > agentAreaH {
			end := scrollOffset + agentAreaH
			if end > len(agentList) {
				end = len(agentList)
			}
			visibleAgents = agentList[scrollOffset:end]
		}
		for vi, a := range visibleAgents {
			i := vi + scrollOffset
			prefix := "  ■ "
			if tv.Coordinator != nil && i == 0 {
				prefix = "  ◆ " // coordinator marker
			}
			line := prefix + truncateStr(a.Name, rightInnerW-4)
			if m.teamsModal.focus == 1 && i == m.teamsModal.agentIdx {
				line = ModalSelectedStyle.Width(rightInnerW).Render(line)
			}
			rightLines = append(rightLines, line)
		}

		// Delete confirmation.
		if m.teamsModal.confirmDelete {
			rightLines = append(rightLines, "")
			rightLines = append(rightLines, ModalWarningStyle.Render(
				fmt.Sprintf("⚠ Delete '%s'? [Enter] confirm  [Esc] cancel", truncateStr(tv.Name(), rightInnerW-30)),
			))
		}
	}

	// Pad right panel to fill height.
	for len(rightLines) < panelInnerH {
		rightLines = append(rightLines, "")
	}
	if len(rightLines) > panelInnerH {
		rightLines = rightLines[:panelInnerH]
	}

	rightContent := strings.Join(rightLines, "\n")
	var rightPanel string
	if m.teamsModal.focus == 1 {
		rightPanel = ModalFocusedPanel.Width(rightPanelW).Height(panelH).Render(rightContent)
	} else {
		rightPanel = ModalPanelStyle.Width(rightPanelW).Height(panelH).Render(rightContent)
	}

	// Join panels horizontally.
	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, " ", rightPanel)

	// Footer with key hints — dim read-only-gated keys when team is read-only.
	readOnly := len(teams) > 0 && m.teamsModal.teamIdx < len(teams) && isReadOnlyTeam(teams[m.teamsModal.teamIdx])
	autoTeam := len(teams) > 0 && m.teamsModal.teamIdx < len(teams) && isAutoTeam(teams[m.teamsModal.teamIdx]) && !isSystemTeam(teams[m.teamsModal.teamIdx], m.configDir)
	systemTeam := len(teams) > 0 && m.teamsModal.teamIdx < len(teams) && isSystemTeam(teams[m.teamsModal.teamIdx], m.configDir)
	noTeamSelected := len(teams) == 0
	nHint := "[Ctrl+N] New"
	dHint := "[Ctrl+D] Delete"
	cHint := "[Ctrl+K] Set Coordinator"
	pHint := "[Ctrl+P] Promote"
	aHint := "[Ctrl+A] Add Agent"
	gHint := "[Ctrl+G] Generate"
	if readOnly {
		dHint = DimStyle.Render(dHint)
		cHint = DimStyle.Render(cHint)
	}
	if !autoTeam {
		pHint = DimStyle.Render(pHint)
	}
	// Dim the add-agent hint when: no team selected, team is system/read-only, or picker is already active.
	if noTeamSelected || readOnly || systemTeam || m.teamsModal.pickerMode {
		aHint = DimStyle.Render(aHint)
	}
	// Dim the generate hint when generation is in progress.
	if m.teamsModal.generating {
		gHint = DimStyle.Render(gHint)
	}
	var footer string
	if m.teamsModal.pickerMode {
		footer = lipgloss.JoinHorizontal(lipgloss.Left,
			"[Enter] Add", "  ",
			"[Esc] Cancel",
		)
	} else {
		footer = lipgloss.JoinHorizontal(lipgloss.Left,
			nHint, "  ", dHint, "  ", cHint, "  ", pHint, "  ", aHint, "  ", gHint, "  ",
			DimStyle.Render("[Tab] Switch"), "  ",
			DimStyle.Render("[Esc] Close"),
		)
	}

	inner := lipgloss.JoinVertical(lipgloss.Left, panels, footer)

	modal := ModalStyle.Width(modalW).Render(inner)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}
