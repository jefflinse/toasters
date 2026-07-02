// View rendering: main View method, viewport content building, markdown rendering, loading screen, and resize handling.
package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

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

	// Skills modal takes over the full terminal as a centered overlay.
	if m.skillsModal.show {
		skillsView := m.renderSkillsModal()
		v := tea.NewView(skillsView)
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

	// Graph map modal (POC).
	if m.graphMapModal.show {
		gmView := m.renderGraphMapModal()
		v := tea.NewView(gmView)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// Blockers selection modal — centered overlay.
	if m.blockersModal.show {
		v := tea.NewView(m.renderBlockersModal())
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// Blocker answer wizard — centered overlay. Answering happens in a modal,
	// not the chat input, so it continues on the surface the selection dialog
	// opened on rather than dropping focus to the bottom of the screen.
	if m.prompt.promptMode {
		v := tea.NewView(m.renderPromptModal())
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// Operator modal takes over the full terminal.
	if m.operatorModal.show {
		operatorView := m.renderOperatorModal()
		v := tea.NewView(operatorView)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// Settings modal takes over the full terminal as a centered overlay.
	if m.settingsModal.show {
		settingsView := m.renderSettingsModal()
		v := tea.NewView(settingsView)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// Presets modal takes over the full terminal as a centered overlay.
	if m.presetsModal.show {
		presetsView := m.renderPresetsModal()
		v := tea.NewView(presetsView)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// Catalog modal takes over the full terminal as a centered overlay.
	if m.catalogModal.show {
		catalogView := m.renderCatalogModal()
		v := tea.NewView(catalogView)
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

	// Log view takes over the full terminal.
	if m.logView.show {
		v := tea.NewView(m.renderLogView())
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// Nodes screen takes over the full terminal.
	if m.nodes.show {
		v := tea.NewView(m.renderNodes())
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	showSidebar := m.shouldShowSidebar()

	sidebarWidth := m.effectiveSidebarWidth()

	const columnGap = 1 // consistent gap between adjacent columns

	var mainWidth int
	if showSidebar {
		mainWidth = m.width - sidebarWidth - columnGap
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

	// Determine chat content and input area.
	var chatContent string
	var inputOrStatus string
	{
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

		// Prompt mode (answering a blocker) renders as a centered modal and
		// returns early from View, so the input area here is always the normal
		// textarea.
		inputArea := inputStyle.Width(mainWidth).Render(m.input.View())
		if n := len(m.chat.queuedMessages); n > 0 {
			label := fmt.Sprintf("  %d queued · sends when operator finishes", n)
			if n == 1 {
				label = "  1 queued · sends when operator finishes"
			}
			inputArea = lipgloss.JoinVertical(lipgloss.Left, inputArea, DimStyle.Render(label))
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

	chatView := ChatAreaStyle.Width(mainWidth).Render(chatContent)

	// Build operator byline strip (shown while the operator stream is active).
	var metaStrip string
	if m.stream.operatorByline != "" {
		metaStrip = OperatorMetaStyle.Width(mainWidth).Render("⬡ " + m.stream.operatorByline)
	}

	// overlayView is whichever popup is active (cmd popup), if any.
	overlayView := popupView

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

	// Build sidebar (if visible).
	var sidebarView string
	if showSidebar {
		sidebarView = m.renderSidebar(sidebarWidth, m.height)
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
	switch {
	case showSidebar && m.sidebarOnRight():
		content = lipgloss.JoinHorizontal(lipgloss.Top, mainColumn, gap, sidebarView)
	case showSidebar:
		content = lipgloss.JoinHorizontal(lipgloss.Top, sidebarView, gap, mainColumn)
	default:
		content = mainColumn
	}

	// Overlay toast notifications, right-aligned to the chat box's right
	// edge so they sit inside the chat column rather than over the sidebar.
	if len(m.toasts) > 0 {
		chatLeft := 0
		if showSidebar && !m.sidebarOnRight() {
			chatLeft = sidebarWidth + columnGap
		}
		chatRight := chatLeft + mainWidth
		toastBlock := m.renderToasts(mainWidth)
		content = overlayToasts(content, toastBlock, chatRight, m.width)
	}

	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// resizeComponents recalculates sizes for viewport and textarea after a resize.
func (m *Model) resizeComponents() {
	showSidebar := m.shouldShowSidebar()
	m.lastSidebarShown = showSidebar

	sidebarWidth := m.effectiveSidebarWidth()

	// Cache for mouse hit-testing.
	m.sidebarWidth = sidebarWidth

	const columnGap = 1 // consistent gap between adjacent columns

	var mainWidth int
	if showSidebar {
		mainWidth = m.width - sidebarWidth - columnGap
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

	// Log view viewport.
	m.resizeLogView()

	m.input.SetWidth(mainWidth - InputAreaStyle.GetHorizontalFrameSize())
	m.input.SetHeight(inputHeight)

	m.ensureMarkdownRenderer()
	m.updateViewportContent()
}
