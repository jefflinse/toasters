// MCP modal: MCP server status and tool browser UI.
package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/mcp"
)

// mcpModalState holds all state for the /mcp modal overlay.
type mcpModalState struct {
	show      bool
	servers   []mcp.ServerStatus // snapshot taken when modal opens
	serverIdx int                // selected server in left panel
	toolIdx   int                // selected tool in right panel (for scrolling)
	focus     int                // 0=left panel (servers), 1=right panel (tools)
}

// updateMCPModal handles all key presses when the MCP modal is open.
func (m *Model) updateMCPModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mcpModal.show = false

	case "tab":
		if m.mcpModal.focus == 0 {
			m.mcpModal.focus = 1
		} else {
			m.mcpModal.focus = 0
		}

	case "up":
		if m.mcpModal.focus == 0 {
			// Left panel: navigate servers.
			if m.mcpModal.serverIdx > 0 {
				m.mcpModal.serverIdx--
				m.mcpModal.toolIdx = 0
			}
		} else {
			// Right panel: navigate tools.
			if m.mcpModal.toolIdx > 0 {
				m.mcpModal.toolIdx--
			}
		}

	case "down":
		if m.mcpModal.focus == 0 {
			// Left panel: navigate servers.
			if m.mcpModal.serverIdx < len(m.mcpModal.servers)-1 {
				m.mcpModal.serverIdx++
				m.mcpModal.toolIdx = 0
			}
		} else {
			// Right panel: navigate tools.
			if len(m.mcpModal.servers) > 0 && m.mcpModal.serverIdx < len(m.mcpModal.servers) {
				server := m.mcpModal.servers[m.mcpModal.serverIdx]
				if m.mcpModal.toolIdx < len(server.Tools)-1 {
					m.mcpModal.toolIdx++
				}
			}
		}
	}
	return m, nil
}

// renderMCPModal renders the full-screen MCP server status modal.
func (m *Model) renderMCPModal() string {
	servers := m.mcpModal.servers

	// Modal dimensions: use most of the terminal (same as teams modal).
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
	if modalH > m.height {
		modalH = m.height
	}

	// Inner width after modal border + padding.
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

	// --- Left panel: server list ---
	var leftLines []string

	// Header.
	leftLines = append(leftLines, gradientText("MCP Servers", [3]uint8{50, 130, 255}, [3]uint8{0, 200, 200}))
	leftLines = append(leftLines, "")

	if len(servers) == 0 {
		leftLines = append(leftLines, DimStyle.Render("No MCP servers configured"))
	} else {
		for i, s := range servers {
			var icon string
			if s.State == mcp.ServerConnected {
				icon = ConnectedStyle.Render("✓")
			} else {
				icon = ErrorStyle.Render("✗")
			}
			name := truncateStr(s.Name, leftInnerW-8)
			line := fmt.Sprintf(" %s %s (%d)", icon, name, s.ToolCount)
			if i == m.mcpModal.serverIdx {
				line = ModalSelectedStyle.Width(leftInnerW).Render(line)
			}
			leftLines = append(leftLines, line)
		}
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
	if m.mcpModal.focus == 0 {
		leftPanel = ModalFocusedPanel.Width(leftPanelW).Height(panelH).Render(leftContent)
	} else {
		leftPanel = ModalPanelStyle.Width(leftPanelW).Height(panelH).Render(leftContent)
	}

	// --- Right panel: server details + tools ---
	var rightLines []string
	if len(servers) == 0 {
		rightLines = append(rightLines, DimStyle.Render("No MCP servers configured."))
	} else if m.mcpModal.serverIdx < len(servers) {
		server := servers[m.mcpModal.serverIdx]

		// Header.
		rightLines = append(rightLines, HeaderStyle.Render(truncateStr(server.Name, rightInnerW)))
		rightLines = append(rightLines, DimStyle.Render(strings.Repeat("─", rightInnerW)))

		// Status line.
		if server.State == mcp.ServerConnected {
			rightLines = append(rightLines, "Status: "+ConnectedStyle.Render("✓ connected"))
		} else {
			rightLines = append(rightLines, "Status: "+ErrorStyle.Render("✗ failed"))
		}

		// Error line (if failed).
		if server.State == mcp.ServerFailed && server.Error != "" {
			errText := wrapText("Error: "+server.Error, rightInnerW)
			rightLines = append(rightLines, ErrorStyle.Render(errText))
		}

		// Transport line.
		rightLines = append(rightLines, "Transport: "+server.Transport)

		// Connection info.
		switch server.Transport {
		case "stdio":
			cmd := server.Config.Command
			if len(server.Config.Args) > 0 {
				cmd += " " + strings.Join(server.Config.Args, " ")
			}
			rightLines = append(rightLines, "Command: "+truncateStr(cmd, rightInnerW-9))
		case "sse", "http":
			rightLines = append(rightLines, "URL: "+truncateStr(server.Config.URL, rightInnerW-5))
		}

		// Filter info.
		if len(server.Config.EnabledTools) > 0 {
			rightLines = append(rightLines, fmt.Sprintf("Filter: %d tools enabled", server.ToolCount))
		}

		// Blank line before tools section.
		rightLines = append(rightLines, "")

		// Tools section.
		rightLines = append(rightLines, fmt.Sprintf("Tools (%d)", server.ToolCount))

		// How many lines are left for tools after header rows.
		// Count: name, divider, status, [error], transport, connection, [filter], blank, tools-header.
		headerRows := len(rightLines)
		toolAreaH := panelInnerH - headerRows
		if toolAreaH < 1 {
			toolAreaH = 1
		}

		// Compute scroll offset so selected tool is always visible.
		scrollOffset := 0
		if len(server.Tools) > toolAreaH {
			scrollOffset = m.mcpModal.toolIdx - toolAreaH/2
			if scrollOffset < 0 {
				scrollOffset = 0
			}
			if scrollOffset > len(server.Tools)-toolAreaH {
				scrollOffset = len(server.Tools) - toolAreaH
			}
		}
		visibleTools := server.Tools
		if scrollOffset > 0 || len(server.Tools) > toolAreaH {
			end := scrollOffset + toolAreaH
			if end > len(server.Tools) {
				end = len(server.Tools)
			}
			visibleTools = server.Tools[scrollOffset:end]
		}
		for vi, t := range visibleTools {
			i := vi + scrollOffset
			line := "  · " + truncateStr(t.OriginalName, rightInnerW-4)
			if m.mcpModal.focus == 1 && i == m.mcpModal.toolIdx {
				line = ModalSelectedStyle.Width(rightInnerW).Render(line)
			}
			rightLines = append(rightLines, line)
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
	if m.mcpModal.focus == 1 {
		rightPanel = ModalFocusedPanel.Width(rightPanelW).Height(panelH).Render(rightContent)
	} else {
		rightPanel = ModalPanelStyle.Width(rightPanelW).Height(panelH).Render(rightContent)
	}

	// Join panels horizontally.
	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, " ", rightPanel)

	// Footer with key hints.
	footer := lipgloss.JoinHorizontal(lipgloss.Left,
		DimStyle.Render("[↑↓] Navigate"), "  ",
		DimStyle.Render("[Tab] Switch Panel"), "  ",
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
