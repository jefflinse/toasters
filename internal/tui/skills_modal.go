// Skills modal: skill management UI including rendering, key handling, and CRUD operations.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/agentfmt"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/loader"
)

// skillsModalState holds all state for the /skills modal overlay.
type skillsModalState struct {
	show          bool
	skills        []*db.Skill // local copy for the modal
	skillIdx      int         // selected skill in left panel
	focus         int         // 0=left panel, 1=right panel
	nameInput     string      // text being typed for new skill name
	inputMode     bool        // true when typing a new skill name
	confirmDelete bool        // true when delete confirmation is showing

	// LLM generation state.
	generateMode  bool   // true when user is typing a generation prompt
	generateInput string // the prompt text being typed
	generating    bool   // true while LLM call is in flight
}

// editorFinishedMsg is sent when an external $EDITOR process completes.
type editorFinishedMsg struct {
	err error
}

// updateSkillsModal handles all key presses when the skills modal is open.
func (m *Model) updateSkillsModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// When typing a generation prompt, intercept all keys.
	if m.skillsModal.generateMode {
		switch msg.String() {
		case "esc":
			m.skillsModal.generateMode = false
			m.skillsModal.generateInput = ""
		case "enter":
			if strings.TrimSpace(m.skillsModal.generateInput) != "" {
				m.skillsModal.generating = true
				m.skillsModal.generateMode = false
				prompt := m.skillsModal.generateInput
				m.skillsModal.generateInput = ""
				return m, generateSkillCmd(m.llmClient, prompt)
			}
		case "backspace":
			if len(m.skillsModal.generateInput) > 0 {
				runes := []rune(m.skillsModal.generateInput)
				m.skillsModal.generateInput = string(runes[:len(runes)-1])
			}
		default:
			if msg.Text != "" {
				m.skillsModal.generateInput += msg.Text
			}
		}
		return m, nil
	}

	// When typing a new skill name, only esc/enter/backspace have special meaning.
	if m.skillsModal.inputMode {
		switch msg.String() {
		case "esc":
			m.skillsModal.inputMode = false
			m.skillsModal.nameInput = ""
		case "enter":
			name := strings.TrimSpace(m.skillsModal.nameInput)
			valid := name != "" && !strings.ContainsAny(name, "/\\.\n\r:")
			if valid {
				if err := createSkillFile(name); err != nil {
					slog.Error("failed to create skill file", "name", name, "error", err)
				} else {
					m.reloadSkillsForModal()
					for i, sk := range m.skillsModal.skills {
						if sk.Name == name {
							m.skillsModal.skillIdx = i
							break
						}
					}
				}
			}
			m.skillsModal.inputMode = false
			m.skillsModal.nameInput = ""
		case "backspace":
			if len(m.skillsModal.nameInput) > 0 {
				runes := []rune(m.skillsModal.nameInput)
				m.skillsModal.nameInput = string(runes[:len(runes)-1])
			}
		default:
			if msg.Text != "" {
				m.skillsModal.nameInput += msg.Text
			}
		}
		return m, nil
	}

	switch msg.String() {
	case "esc":
		if m.skillsModal.confirmDelete {
			m.skillsModal.confirmDelete = false
		} else {
			m.skillsModal.show = false
		}

	case "tab":
		if m.skillsModal.focus == 0 {
			m.skillsModal.focus = 1
		} else {
			m.skillsModal.focus = 0
		}

	case "up":
		if m.skillsModal.focus == 0 {
			if m.skillsModal.skillIdx > 0 {
				m.skillsModal.skillIdx--
			}
			m.skillsModal.confirmDelete = false
		}

	case "down":
		if m.skillsModal.focus == 0 {
			if m.skillsModal.skillIdx < len(m.skillsModal.skills)-1 {
				m.skillsModal.skillIdx++
			}
			m.skillsModal.confirmDelete = false
		}

	case "ctrl+n":
		m.skillsModal.inputMode = true
		m.skillsModal.nameInput = ""

	case "ctrl+g":
		if !m.skillsModal.generating {
			if m.llmClient == nil {
				return m, m.addToast("⚠ No LLM provider configured", toastWarning)
			}
			m.skillsModal.generateMode = true
			m.skillsModal.generateInput = ""
		}

	case "ctrl+d":
		if !m.skillsModal.confirmDelete && len(m.skillsModal.skills) > 0 && m.skillsModal.skillIdx < len(m.skillsModal.skills) {
			sk := m.skillsModal.skills[m.skillsModal.skillIdx]
			if sk.Source != "system" {
				m.skillsModal.confirmDelete = true
			}
		}

	case "enter":
		if m.skillsModal.confirmDelete {
			if len(m.skillsModal.skills) > 0 && m.skillsModal.skillIdx < len(m.skillsModal.skills) {
				sk := m.skillsModal.skills[m.skillsModal.skillIdx]
				if sk.SourcePath != "" && sk.Source != "system" {
					_ = os.Remove(sk.SourcePath)
				}
			}
			m.reloadSkillsForModal()
			if m.skillsModal.skillIdx >= len(m.skillsModal.skills) && len(m.skillsModal.skills) > 0 {
				m.skillsModal.skillIdx = len(m.skillsModal.skills) - 1
			} else if len(m.skillsModal.skills) == 0 {
				m.skillsModal.skillIdx = 0
			}
			m.skillsModal.confirmDelete = false
		}

	case "e":
		if m.skillsModal.focus == 0 && len(m.skillsModal.skills) > 0 && m.skillsModal.skillIdx < len(m.skillsModal.skills) {
			sk := m.skillsModal.skills[m.skillsModal.skillIdx]
			if sk.SourcePath != "" && sk.Source != "system" {
				return m, openInEditor(sk.SourcePath)
			}
		}
	}
	return m, nil
}

// reloadSkillsForModal refreshes m.skillsModal.skills from the DB.
func (m *Model) reloadSkillsForModal() {
	if m.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	skills, err := m.store.ListSkills(ctx)
	if err != nil {
		slog.Error("failed to list skills for modal", "error", err)
		return
	}
	m.skillsModal.skills = skills
}

// createSkillFile writes a template .md file for a new skill.
func createSkillFile(name string) error {
	cfgDir, err := config.Dir()
	if err != nil {
		return fmt.Errorf("getting config dir: %w", err)
	}
	skillsDir := filepath.Join(cfgDir, "user", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("creating skills dir: %w", err)
	}

	// Sanitize name for filename using Slugify for consistent, safe filenames.
	filename := loader.Slugify(name) + ".md"
	if filename == ".md" {
		return fmt.Errorf("invalid skill name: produces empty filename")
	}
	path := filepath.Join(skillsDir, filename)

	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("skill file %q already exists", filename)
	}

	template := fmt.Sprintf(`---
name: %s
description: A brief description of what this skill does
tools: []
---

Your skill prompt goes here. This text will be injected into agents that use this skill.
`, name)

	return os.WriteFile(path, []byte(template), 0o644)
}

// writeGeneratedSkillFile writes LLM-generated skill content to the user skills
// directory. It derives the filename from the skill name in the content, and
// appends -2, -3, etc. if the file already exists. Returns the written path.
func writeGeneratedSkillFile(content string) (string, error) {
	cfgDir, err := config.Dir()
	if err != nil {
		return "", fmt.Errorf("getting config dir: %w", err)
	}
	skillsDir := filepath.Join(cfgDir, "user", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return "", fmt.Errorf("creating skills dir: %w", err)
	}

	// Parse the content to extract the skill name for the filename.
	slug := "generated-skill"
	if parsed, err := agentfmt.ParseBytes([]byte(content), agentfmt.DefSkill); err == nil {
		if skillDef, ok := parsed.(*agentfmt.SkillDef); ok && skillDef.Name != "" {
			s := loader.Slugify(skillDef.Name)
			if s != "" {
				slug = s
			}
		}
	}

	// Find a free filename, appending -2, -3, etc. if needed.
	path := filepath.Join(skillsDir, slug+".md")
	if _, err := os.Stat(path); err == nil {
		for i := 2; ; i++ {
			candidate := filepath.Join(skillsDir, fmt.Sprintf("%s-%d.md", slug, i))
			if _, err := os.Stat(candidate); os.IsNotExist(err) {
				path = candidate
				break
			}
		}
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("writing skill file: %w", err)
	}
	return path, nil
}

// openInEditor launches $EDITOR (or vi) for the given file path, suspending the TUI.
func openInEditor(path string) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editorFinishedMsg{err: err}
	})
}

// renderSkillsModal renders the full-screen skills management modal.
func (m *Model) renderSkillsModal() string {
	skills := m.skillsModal.skills

	// Modal dimensions: use most of the terminal (same pattern as teams modal).
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

	innerW := modalW - ModalStyle.GetHorizontalFrameSize()
	if innerW < 10 {
		innerW = 10
	}

	leftInnerW := 30
	leftPanelW := leftInnerW + ModalPanelStyle.GetHorizontalFrameSize()
	if leftPanelW > innerW/2 {
		leftPanelW = innerW / 2
		leftInnerW = leftPanelW - ModalPanelStyle.GetHorizontalFrameSize()
	}

	rightPanelW := innerW - leftPanelW - 1
	rightInnerW := rightPanelW - ModalPanelStyle.GetHorizontalFrameSize()
	if rightInnerW < 5 {
		rightInnerW = 5
	}

	footerLines := 1
	panelH := modalH - ModalStyle.GetVerticalFrameSize() - footerLines - 1
	if panelH < 5 {
		panelH = 5
	}
	panelInnerH := panelH - ModalPanelStyle.GetVerticalFrameSize()
	if panelInnerH < 3 {
		panelInnerH = 3
	}

	// --- Left panel: skill list ---
	var leftLines []string
	leftLines = append(leftLines, gradientText("Skills", [3]uint8{255, 175, 0}, [3]uint8{255, 95, 0}))
	leftLines = append(leftLines, "")

	if len(skills) == 0 {
		leftLines = append(leftLines, DimStyle.Render("No skills configured"))
		leftLines = append(leftLines, DimStyle.Render("Press [Ctrl+N] to create one"))
	} else {
		for i, sk := range skills {
			icon := "◇"
			if sk.Source == "system" {
				icon = "⚙"
			}
			name := truncateStr(sk.Name, leftInnerW-4)
			line := fmt.Sprintf(" %s %s", icon, name)
			if sk.Source == "system" {
				line += " 🔒"
			}
			if i == m.skillsModal.skillIdx {
				line = ModalSelectedStyle.Width(leftInnerW).Render(line)
			} else if sk.Source == "system" {
				line = ModalReadOnlyStyle.Render(line)
			}
			leftLines = append(leftLines, line)
		}
	}

	// Input mode: show name-entry prompt at the bottom.
	if m.skillsModal.inputMode {
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, DimStyle.Render("> New skill name:"))
		cursor := m.skillsModal.nameInput + "█"
		leftLines = append(leftLines, "  "+cursor)
	}

	// Generate mode: show generation prompt input at the bottom.
	if m.skillsModal.generateMode {
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, DimStyle.Render("> Describe the skill:"))
		cursor := m.skillsModal.generateInput + "█"
		leftLines = append(leftLines, "  "+cursor)
	}

	// Generating: show spinner status line.
	if m.skillsModal.generating {
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, DimStyle.Render("⟳ Generating skill..."))
	}

	for len(leftLines) < panelInnerH {
		leftLines = append(leftLines, "")
	}
	if len(leftLines) > panelInnerH {
		leftLines = leftLines[:panelInnerH]
	}

	leftContent := strings.Join(leftLines, "\n")
	var leftPanel string
	if m.skillsModal.focus == 0 {
		leftPanel = ModalFocusedPanel.Width(leftPanelW).Height(panelH).Render(leftContent)
	} else {
		leftPanel = ModalPanelStyle.Width(leftPanelW).Height(panelH).Render(leftContent)
	}

	// --- Right panel: skill detail ---
	var rightLines []string
	if len(skills) == 0 {
		rightLines = append(rightLines, DimStyle.Render("No skills configured."))
		rightLines = append(rightLines, DimStyle.Render("Press [Ctrl+N] to create one."))
	} else if m.skillsModal.skillIdx < len(skills) {
		sk := skills[m.skillsModal.skillIdx]

		headerText := truncateStr(sk.Name, rightInnerW-12)
		if sk.Source == "system" {
			headerText += " " + DimStyle.Render("⚙ system")
		}
		rightLines = append(rightLines, HeaderStyle.Render(headerText))
		rightLines = append(rightLines, DimStyle.Render(strings.Repeat("─", rightInnerW)))

		if sk.Description != "" {
			rightLines = append(rightLines, DimStyle.Render(truncateStr(sk.Description, rightInnerW)))
		}

		rightLines = append(rightLines, "")

		// Source info.
		rightLines = append(rightLines, DimStyle.Render("Source: "+sk.Source))
		if sk.SourcePath != "" {
			rightLines = append(rightLines, DimStyle.Render("Path: "+truncateStr(sk.SourcePath, rightInnerW-6)))
		}

		// Tools.
		if len(sk.Tools) > 0 {
			var toolNames []string
			_ = json.Unmarshal(sk.Tools, &toolNames)
			if len(toolNames) > 0 {
				rightLines = append(rightLines, "")
				rightLines = append(rightLines, fmt.Sprintf("Tools (%d)", len(toolNames)))
				for _, t := range toolNames {
					rightLines = append(rightLines, "  · "+truncateStr(t, rightInnerW-4))
				}
			}
		}

		// Prompt preview.
		if sk.Prompt != "" {
			rightLines = append(rightLines, "")
			rightLines = append(rightLines, "Prompt:")
			promptLines := strings.SplitN(sk.Prompt, "\n", 6)
			for i, pl := range promptLines {
				if i >= 5 {
					rightLines = append(rightLines, DimStyle.Render("  ..."))
					break
				}
				pl = strings.TrimSpace(pl)
				if pl != "" {
					rightLines = append(rightLines, DimStyle.Render("  "+truncateStr(pl, rightInnerW-2)))
				}
			}
		}

		// Delete confirmation.
		if m.skillsModal.confirmDelete {
			rightLines = append(rightLines, "")
			rightLines = append(rightLines, ModalWarningStyle.Render(
				fmt.Sprintf("⚠ Delete '%s'? [Enter] confirm  [Esc] cancel", truncateStr(sk.Name, rightInnerW-30)),
			))
		}
	}

	for len(rightLines) < panelInnerH {
		rightLines = append(rightLines, "")
	}
	if len(rightLines) > panelInnerH {
		rightLines = rightLines[:panelInnerH]
	}

	rightContent := strings.Join(rightLines, "\n")
	var rightPanel string
	if m.skillsModal.focus == 1 {
		rightPanel = ModalFocusedPanel.Width(rightPanelW).Height(panelH).Render(rightContent)
	} else {
		rightPanel = ModalPanelStyle.Width(rightPanelW).Height(panelH).Render(rightContent)
	}

	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, " ", rightPanel)

	// Footer with key hints — dim edit/delete keys when skill is system (read-only).
	readOnly := len(skills) > 0 && m.skillsModal.skillIdx < len(skills) && skills[m.skillsModal.skillIdx].Source == "system"
	nHint := "[Ctrl+N] New"
	dHint := "[Ctrl+D] Delete"
	eHint := "[e] Edit"
	gHint := "[Ctrl+G] Generate"
	if readOnly {
		dHint = DimStyle.Render(dHint)
		eHint = DimStyle.Render(eHint)
	}
	if m.llmClient == nil || m.skillsModal.generating {
		gHint = DimStyle.Render(gHint)
	}
	footer := lipgloss.JoinHorizontal(lipgloss.Left,
		nHint, "  ", eHint, "  ", dHint, "  ", gHint, "  ",
		DimStyle.Render("[Tab] Switch"), "  ",
		DimStyle.Render("[Esc] Close"),
	)

	inner := lipgloss.JoinVertical(lipgloss.Left, panels, footer)
	modal := ModalStyle.Width(modalW).Render(inner)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}
