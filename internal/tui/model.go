package tui

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/llm"
)

const (
	minSidebarWidth = 24
	inputHeight     = 3
	minWidthForBar  = 60
)

// SessionStats tracks session-level statistics displayed in the sidebar.
type SessionStats struct {
	ModelName            string
	Endpoint             string
	Connected            bool
	ContextLength        int // max context window in tokens (0 if unknown)
	MessageCount         int
	PromptTokens         int
	CompletionTokens     int
	ReasoningTokens      int // accumulated reasoning tokens across all turns
	CompletionTokensLive int // estimated completion tokens for the in-progress response
	ReasoningTokensLive  int // estimated reasoning tokens for the in-progress response
	LastResponseTime     time.Duration
	ResponseStart        time.Time
	TotalResponses       int           // number of completed responses (for avg calc)
	TotalResponseTime    time.Duration // sum of all response times (for avg calc)
}

// Message types for the Bubble Tea event loop.

type StreamChunkMsg struct {
	Content   string
	Reasoning string
}

type StreamDoneMsg struct {
	Model string
	Usage *llm.Usage
}

type StreamErrMsg struct {
	Err error
}

type ModelsMsg struct {
	Models []llm.ModelInfo
	Err    error
}

// claudeMetaMsg carries model/mode info parsed from the claude CLI system/init event.
type claudeMetaMsg struct {
	Model          string
	PermissionMode string
	Version        string
}

// sidebarWidth returns the sidebar width as 1/4 of terminal width, with a minimum.
func sidebarWidth(termWidth int) int {
	w := termWidth / 4
	if w < minSidebarWidth {
		w = minSidebarWidth
	}
	return w
}

// Model is the root Bubble Tea model for the toasters TUI.
type Model struct {
	width  int
	height int

	llmClient        *llm.Client
	claudeCfg        config.ClaudeConfig
	messages         []llm.Message
	reasoning        []string // reasoning[i] is the thinking trace for messages[i] (assistant turns only)
	chatViewport     viewport.Model
	input            textarea.Model
	streaming        bool
	currentResponse  string
	currentReasoning string
	streamCh         <-chan llm.StreamResponse
	cancelStream     context.CancelFunc
	stats            SessionStats
	err              error
	mdRender         *glamour.TermRenderer

	// Claude CLI metadata.
	claudeActiveMeta string   // formatted byline for the in-progress claude stream; cleared when done
	claudeMeta       []string // parallel to messages; byline per message (empty for non-claude turns)

	// Slash command autocomplete popup state.
	showCmdPopup   bool
	filteredCmds   []SlashCommand
	selectedCmdIdx int
}

// NewModel returns an initialized root model.
func NewModel(client *llm.Client, claudeCfg config.ClaudeConfig) Model {
	ta := textarea.New()
	ta.Placeholder = "Type your message here..."
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.SetHeight(inputHeight)
	ta.CharLimit = 0 // no limit

	// Clear all internal textarea styling — the border on InputAreaStyle provides the visual chrome.
	// In bubbles v2, styles are accessed via Styles()/SetStyles().
	noStyle := lipgloss.NewStyle()
	s := ta.Styles()
	s.Focused.Base = noStyle
	s.Focused.CursorLine = noStyle
	s.Focused.Text = noStyle
	s.Focused.Placeholder = noStyle.Foreground(ColorDim)
	s.Focused.EndOfBuffer = noStyle
	s.Blurred.Base = noStyle
	s.Blurred.CursorLine = noStyle
	s.Blurred.Text = noStyle
	s.Blurred.Placeholder = noStyle.Foreground(ColorDim)
	s.Blurred.EndOfBuffer = noStyle
	ta.SetStyles(s)

	// Rebind InsertNewline to shift+enter so plain Enter can send messages.
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter"))
	ta.Focus()

	vp := viewport.New()
	vp.MouseWheelEnabled = true
	// Disable viewport key bindings so they don't capture keys from the textarea.
	vp.KeyMap = viewport.KeyMap{}

	return Model{
		llmClient:    client,
		claudeCfg:    claudeCfg,
		chatViewport: vp,
		input:        ta,
		stats: SessionStats{
			Endpoint:  client.BaseURL(),
			Connected: false,
		},
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		tea.RequestWindowSize,
		m.fetchModels(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// When the slash command popup is visible, intercept navigation keys
		// before any other handling so they don't fall through to the textarea.
		if m.showCmdPopup {
			switch msg.String() {
			case "up":
				if len(m.filteredCmds) > 0 {
					m.selectedCmdIdx = (m.selectedCmdIdx - 1 + len(m.filteredCmds)) % len(m.filteredCmds)
				}
				return m, nil
			case "down":
				if len(m.filteredCmds) > 0 {
					m.selectedCmdIdx = (m.selectedCmdIdx + 1) % len(m.filteredCmds)
				}
				return m, nil
			case "tab", "enter":
				if len(m.filteredCmds) > 0 {
					m.input.SetValue(m.filteredCmds[m.selectedCmdIdx].Name + " ")
				}
				m.showCmdPopup = false
				return m, nil
			case "esc":
				m.showCmdPopup = false
				return m, nil
			}
		}

		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit

		case "esc":
			// Cancel an in-flight stream. (Popup esc is handled above and returns early.)
			if m.streaming && m.cancelStream != nil {
				m.cancelStream()
				m.streaming = false
				m.cancelStream = nil
				m.streamCh = nil
				if m.currentResponse != "" {
					m.messages = append(m.messages, llm.Message{
						Role:    "assistant",
						Content: m.currentResponse,
					})
					m.reasoning = append(m.reasoning, m.currentReasoning)
					m.claudeMeta = append(m.claudeMeta, m.claudeActiveMeta)
					m.claudeActiveMeta = ""
					m.currentResponse = ""
					m.currentReasoning = ""
				}
				m.stats.CompletionTokensLive = 0
				m.stats.ReasoningTokensLive = 0
				m.updateViewportContent()
				return m, m.input.Focus()
			}

		case "enter":
			// Send message on Enter when not streaming and input has content.
			// Shift+enter inserts a newline (handled by textarea).
			if !m.streaming && strings.TrimSpace(m.input.Value()) != "" {
				text := strings.TrimSpace(m.input.Value())
				switch text {
				case "/exit", "/quit":
					return m, tea.Quit
				case "/help":
					m.input.Reset()
					m.showCmdPopup = false
					m.appendHelpMessage()
					return m, nil
				case "/new":
					m.input.Reset()
					m.showCmdPopup = false
					m.newSession()
					return m, nil
				}
				// /claude <prompt> — stream via the claude CLI subprocess.
				if strings.HasPrefix(text, "/claude ") {
					prompt := strings.TrimSpace(strings.TrimPrefix(text, "/claude "))
					if prompt == "" {
						m.input.Reset()
						m.showCmdPopup = false
						return m, nil
					}
					m.showCmdPopup = false
					m.input.Reset()
					return m, m.sendClaudeMessage(prompt)
				}
				// Not a recognized slash command — send to LLM.
				m.showCmdPopup = false
				return m, m.sendMessage()
			}
		}

		// Delegate to textarea when not a special key we handle.
		if !m.streaming {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			cmds = append(cmds, cmd)

			// Update slash command popup state based on current input value.
			inputVal := m.input.Value()
			if strings.HasPrefix(inputVal, "/") {
				m.filteredCmds = filterCommands(inputVal)
				m.showCmdPopup = len(m.filteredCmds) > 0
				if m.showCmdPopup && m.selectedCmdIdx >= len(m.filteredCmds) {
					m.selectedCmdIdx = 0
				}
			} else {
				m.showCmdPopup = false
				m.filteredCmds = nil
				m.selectedCmdIdx = 0
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeComponents()

	case StreamChunkMsg:
		m.currentResponse += msg.Content
		m.currentReasoning += msg.Reasoning
		// Live token estimates (~4 chars/token).
		m.stats.CompletionTokensLive = len([]rune(m.currentResponse)) / 4
		m.stats.ReasoningTokensLive = len([]rune(m.currentReasoning)) / 4
		// Elapsed response time ticks up on every chunk.
		if !m.stats.ResponseStart.IsZero() {
			m.stats.LastResponseTime = time.Since(m.stats.ResponseStart)
		}
		m.updateViewportContent()
		m.chatViewport.GotoBottom()
		if m.streamCh != nil {
			cmds = append(cmds, waitForChunk(m.streamCh))
		}

	case StreamDoneMsg:
		m.streaming = false
		if msg.Model != "" {
			m.stats.ModelName = msg.Model
		}
		if msg.Usage != nil {
			m.stats.PromptTokens += msg.Usage.PromptTokens
			m.stats.CompletionTokens += msg.Usage.CompletionTokens
		}
		// Accumulate reasoning tokens from live estimate (server doesn't report them separately).
		m.stats.ReasoningTokens += m.stats.ReasoningTokensLive
		m.stats.CompletionTokensLive = 0
		m.stats.ReasoningTokensLive = 0
		if !m.stats.ResponseStart.IsZero() {
			m.stats.LastResponseTime = time.Since(m.stats.ResponseStart)
			m.stats.TotalResponseTime += m.stats.LastResponseTime
			m.stats.TotalResponses++
		}
		if m.currentResponse != "" {
			m.messages = append(m.messages, llm.Message{
				Role:    "assistant",
				Content: m.currentResponse,
			})
			m.reasoning = append(m.reasoning, m.currentReasoning)
			// For LM Studio (operator) turns, claudeActiveMeta is empty — fill in the operator byline.
			byline := m.claudeActiveMeta
			if byline == "" && m.stats.ModelName != "" {
				byline = "operator · " + m.stats.ModelName
			}
			m.claudeMeta = append(m.claudeMeta, byline)
			m.claudeActiveMeta = ""
		}
		m.currentResponse = ""
		m.currentReasoning = ""
		m.streamCh = nil
		m.cancelStream = nil
		m.updateViewportContent()
		m.chatViewport.GotoBottom()
		cmds = append(cmds, m.input.Focus())

	case StreamErrMsg:
		m.streaming = false
		m.err = msg.Err
		m.stats.Connected = false
		m.streamCh = nil
		m.cancelStream = nil
		if m.currentResponse != "" {
			m.messages = append(m.messages, llm.Message{
				Role:    "assistant",
				Content: m.currentResponse,
			})
			m.reasoning = append(m.reasoning, m.currentReasoning)
			byline := m.claudeActiveMeta
			if byline == "" && m.stats.ModelName != "" {
				byline = "operator · " + m.stats.ModelName
			}
			m.claudeMeta = append(m.claudeMeta, byline)
			m.claudeActiveMeta = ""
			m.currentResponse = ""
			m.currentReasoning = ""
		}
		m.stats.CompletionTokensLive = 0
		m.stats.ReasoningTokensLive = 0
		m.updateViewportContent()
		cmds = append(cmds, m.input.Focus())

	case ModelsMsg:
		if msg.Err != nil {
			m.stats.Connected = false
			m.err = fmt.Errorf("fetching models: %w", msg.Err)
		} else {
			m.stats.Connected = true
			m.err = nil
			// Prefer the loaded model; fall back to first in list.
			if len(msg.Models) > 0 {
				picked := msg.Models[0]
				for _, mi := range msg.Models {
					if mi.State == "loaded" {
						picked = mi
						break
					}
				}
				m.stats.ModelName = picked.ID
				m.stats.ContextLength = picked.ContextLength()
			}
		}
		m.updateViewportContent()

	case streamStartedMsg:
		m.streamCh = msg.ch
		cmds = append(cmds, waitForChunk(m.streamCh))

	case claudeMetaMsg:
		m.claudeActiveMeta = formatClaudeMeta(msg)
		return m, waitForChunk(m.streamCh)

	case tea.MouseWheelMsg:
		// Forward mouse wheel events to viewport for scroll support.
		var cmd tea.Cmd
		m.chatViewport, cmd = m.chatViewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m Model) View() tea.View {
	if m.width == 0 || m.height == 0 {
		v := tea.NewView("")
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	showSidebar := m.width >= minWidthForBar

	sbWidth := sidebarWidth(m.width)

	var mainWidth int
	if showSidebar {
		// Sidebar border takes 1 char on the left side.
		mainWidth = m.width - sbWidth - 1
	} else {
		mainWidth = m.width
	}

	// Build chat content area.
	// Width includes padding, so use mainWidth directly.
	chatContent := m.chatViewport.View()

	// Build slash command popup (if active).
	var popupView string
	if m.showCmdPopup && len(m.filteredCmds) > 0 {
		var rows []string
		for i, cmd := range m.filteredCmds {
			if i == m.selectedCmdIdx {
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
		popupHeight := len(m.filteredCmds)
		lines := strings.Split(chatContent, "\n")
		trimTo := len(lines) - popupHeight
		if trimTo < 0 {
			trimTo = 0
		}
		chatContent = strings.Join(lines[:trimTo], "\n")
	}

	chatView := ChatAreaStyle.Width(mainWidth).Render(chatContent)

	// Build input area.
	inputView := InputAreaStyle.Width(mainWidth).Render(m.input.View())

	// Build claude meta strip (shown while a claude stream is active).
	var metaStrip string
	if m.claudeActiveMeta != "" {
		metaStrip = ClaudeMetaStyle.Width(mainWidth).Render("⬡ " + m.claudeActiveMeta)
	}

	// Join chat + popup (if any) + meta strip (if any) + input vertically.
	var mainColumn string
	if popupView != "" && metaStrip != "" {
		mainColumn = lipgloss.JoinVertical(lipgloss.Left, chatView, popupView, metaStrip, inputView)
	} else if popupView != "" {
		mainColumn = lipgloss.JoinVertical(lipgloss.Left, chatView, popupView, inputView)
	} else if metaStrip != "" {
		mainColumn = lipgloss.JoinVertical(lipgloss.Left, chatView, metaStrip, inputView)
	} else {
		mainColumn = lipgloss.JoinVertical(lipgloss.Left, chatView, inputView)
	}

	var content string
	if !showSidebar {
		content = mainColumn
	} else {
		// Build sidebar.
		sidebar := m.renderSidebar(sbWidth)
		content = lipgloss.JoinHorizontal(lipgloss.Top, mainColumn, sidebar)
	}

	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
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

// ensureMarkdownRenderer creates or recreates the glamour renderer for the current width.
func (m *Model) ensureMarkdownRenderer() {
	w := m.chatViewport.Width()
	if w < 1 {
		w = 80
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStylePath("dark"),
		glamour.WithWordWrap(w),
	)
	if err == nil {
		m.mdRender = r
	}
}

// resizeComponents recalculates sizes for viewport and textarea after a resize.
func (m *Model) resizeComponents() {
	showSidebar := m.width >= minWidthForBar

	sbWidth := sidebarWidth(m.width)

	var mainWidth int
	if showSidebar {
		mainWidth = m.width - sbWidth - 1
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

	vpWidth := mainWidth - ChatAreaStyle.GetHorizontalPadding()
	if vpWidth < 1 {
		vpWidth = 1
	}

	m.chatViewport.SetWidth(vpWidth)
	m.chatViewport.SetHeight(vpHeight)

	m.input.SetWidth(mainWidth - InputAreaStyle.GetHorizontalFrameSize())
	m.input.SetHeight(inputHeight)

	m.ensureMarkdownRenderer()
	m.updateViewportContent()
}

// updateViewportContent rebuilds the chat history string and sets it on the viewport.
func (m *Model) updateViewportContent() {
	var sb strings.Builder
	contentWidth := m.chatViewport.Width()
	if contentWidth < 1 {
		contentWidth = 40
	}

	// Show welcome message when there's no conversation yet.
	if len(m.messages) == 0 && !m.streaming {
		welcome := HeaderStyle.Render("Welcome to toasters!") + "\n"
		welcome += DimStyle.Render("Type a message and press Enter to chat.") + "\n"
		welcome += DimStyle.Render("Connected to "+m.stats.Endpoint) + "\n\n"
		welcome += DimStyle.Render("Esc to cancel a response · Ctrl+C to quit.")
		sb.WriteString(welcome + "\n\n")
	}

	assistantIdx := 0
	for _, msg := range m.messages {
		switch msg.Role {
		case "user":
			blockWidth := contentWidth - UserMsgBlockStyle.GetHorizontalFrameSize()
			if blockWidth < 1 {
				blockWidth = 1
			}
			block := UserMsgBlockStyle.Width(blockWidth).Render(wrapText(msg.Content, blockWidth))
			sb.WriteString(block + "\n\n")
		case "assistant":
			// Render claude byline (if any) above the response.
			if assistantIdx < len(m.claudeMeta) && m.claudeMeta[assistantIdx] != "" {
				sb.WriteString(ClaudeBylineStyle.Render("⬡ "+m.claudeMeta[assistantIdx]) + "\n")
			}
			// Render reasoning trace (if any) above the response.
			if assistantIdx < len(m.reasoning) && m.reasoning[assistantIdx] != "" {
				sb.WriteString(renderReasoningBlock(m.reasoning[assistantIdx], contentWidth))
				sb.WriteString("\n")
			}
			sb.WriteString(m.renderMarkdown(msg.Content) + "\n\n")
			assistantIdx++
		}
	}

	// Show streaming response in progress — re-render markdown incrementally.
	if m.streaming {
		// Live reasoning trace while thinking.
		if m.currentReasoning != "" {
			sb.WriteString(renderReasoningBlock(m.currentReasoning, contentWidth))
			sb.WriteString("\n")
		} else {
			sb.WriteString(ReasoningStyle.Render("Thinking...") + "\n\n")
		}
		// Live response content.
		if m.currentResponse != "" {
			sb.WriteString(m.renderMarkdown(m.currentResponse))
			sb.WriteString(StreamingStyle.Render(" ▍"))
			sb.WriteString("\n\n")
		}
	}

	// Show error if present.
	if m.err != nil {
		sb.WriteString(ErrorStyle.Render("Error: "+m.err.Error()) + "\n\n")
	}

	m.chatViewport.SetContent(sb.String())
}

// renderSidebar builds the right sidebar with stats.
func (m Model) renderSidebar(sbWidth int) string {
	contentWidth := sbWidth - SidebarStyle.GetHorizontalPadding()

	var sb strings.Builder

	// Connection section.
	sb.WriteString(SidebarHeaderStyle.Render("Connection"))
	sb.WriteString("\n\n")

	modelName := m.stats.ModelName
	if modelName == "" {
		modelName = "Loading..."
	}
	sb.WriteString(SidebarLabelStyle.Render("Model"))
	sb.WriteString("\n")
	sb.WriteString(SidebarValueStyle.Render(truncateStr(modelName, contentWidth)))
	sb.WriteString("\n\n")

	sb.WriteString(SidebarLabelStyle.Render("Endpoint"))
	sb.WriteString("\n")
	sb.WriteString(SidebarValueStyle.Render(truncateStr(m.stats.Endpoint, contentWidth)))
	sb.WriteString("\n\n")

	status := "Disconnected"
	statusStyle := ErrorStyle
	if m.stats.Connected {
		status = "Connected"
		statusStyle = ConnectedStyle
	}
	sb.WriteString(SidebarLabelStyle.Render("Status"))
	sb.WriteString("\n")
	sb.WriteString(statusStyle.Render(status))
	sb.WriteString("\n")

	sb.WriteString("\n")
	sb.WriteString(SidebarHeaderStyle.Render("Session"))
	sb.WriteString("\n\n")

	// While streaming, blend in live estimates for the current response.
	liveCompletionTokens := m.stats.CompletionTokens + m.stats.CompletionTokensLive
	liveReasoningTokens := m.stats.ReasoningTokens + m.stats.ReasoningTokensLive

	sb.WriteString(sidebarRow("Messages", fmt.Sprintf("%d", m.stats.MessageCount)))
	sb.WriteString(sidebarRow("Tokens in", fmt.Sprintf("%d", m.stats.PromptTokens)))
	sb.WriteString(sidebarRow("Tokens out", fmt.Sprintf("%d", liveCompletionTokens)))
	sb.WriteString(sidebarRow("Reasoning", fmt.Sprintf("%d", liveReasoningTokens)))

	// Tokens/sec: completion tokens over total response time.
	tokPerSec := "-"
	if m.stats.TotalResponses > 0 && m.stats.TotalResponseTime > 0 {
		tps := float64(m.stats.CompletionTokens) / m.stats.TotalResponseTime.Seconds()
		tokPerSec = fmt.Sprintf("%.1f t/s", tps)
	} else if m.streaming && m.stats.LastResponseTime > 0 && m.stats.CompletionTokensLive > 0 {
		tps := float64(m.stats.CompletionTokensLive) / m.stats.LastResponseTime.Seconds()
		tokPerSec = fmt.Sprintf("%.1f t/s", tps)
	}
	sb.WriteString(sidebarRow("Speed", tokPerSec))

	totalTokens := m.stats.PromptTokens + liveCompletionTokens + liveReasoningTokens
	sb.WriteString(SidebarLabelStyle.Render("Context"))
	sb.WriteString("\n")
	sb.WriteString(renderContextBar(totalTokens, m.stats.ContextLength, contentWidth))
	sb.WriteString("\n")

	lastResp := "-"
	if m.stats.LastResponseTime > 0 {
		lastResp = fmt.Sprintf("%.1fs", m.stats.LastResponseTime.Seconds())
	}
	avgResp := "-"
	if m.stats.TotalResponses > 0 {
		avg := m.stats.TotalResponseTime / time.Duration(m.stats.TotalResponses)
		avgResp = fmt.Sprintf("%.1fs", avg.Seconds())
	}
	sb.WriteString(sidebarRow("Last resp", lastResp))
	sb.WriteString(sidebarRow("Avg resp", avgResp))

	return SidebarStyle.
		Width(sbWidth).
		Height(m.height).
		Render(sb.String())
}

// renderContextBar renders a segmented progress bar showing context window usage.
// It color-shifts green → yellow → red as usage increases, and prints a
// summary line beneath it.
func renderContextBar(used, total, width int) string {
	if width < 4 {
		width = 4
	}

	var pct float64
	var summary string
	if total > 0 {
		pct = float64(used) / float64(total)
		if pct > 1 {
			pct = 1
		}
		summary = fmt.Sprintf("%d / %d (%.0f%%)", used, total, pct*100)
	} else {
		summary = fmt.Sprintf("%d / ?", used)
	}

	// Build the bar.
	filled := int(pct * float64(width))
	empty := width - filled

	// Color: green (82) → yellow (226) → red (196) based on usage.
	var barColor color.Color
	switch {
	case pct < 0.6:
		barColor = lipgloss.Color("82") // green
	case pct < 0.8:
		barColor = lipgloss.Color("226") // yellow
	default:
		barColor = lipgloss.Color("196") // red
	}

	filledStyle := lipgloss.NewStyle().Foreground(barColor)
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("237"))

	bar := filledStyle.Render(strings.Repeat("█", filled)) +
		emptyStyle.Render(strings.Repeat("░", empty))

	summaryStr := DimStyle.Render(summary)

	return bar + "\n" + summaryStr
}

// renderReasoningBlock renders a chain-of-thought reasoning trace as a dimmed,
// left-bordered block with a "thinking" header.
func renderReasoningBlock(reasoning string, contentWidth int) string {
	blockWidth := contentWidth - ReasoningBlockStyle.GetHorizontalFrameSize()
	if blockWidth < 1 {
		blockWidth = 1
	}
	header := ReasoningHeaderStyle.Render("⟳ thinking")
	body := ReasoningBlockStyle.Width(blockWidth).Render(wrapText(reasoning, blockWidth))
	return header + "\n" + body
}

func sidebarRow(label, value string) string {
	return SidebarLabelStyle.Render(fmt.Sprintf("%-12s", label)) +
		SidebarValueStyle.Render(value) + "\n"
}

// appendHelpMessage adds a help message to the chat as an assistant turn.
func (m *Model) appendHelpMessage() {
	helpText := "**Toasters — Help**\n\n" +
		"**Slash Commands**\n" +
		"- `/help` — Show this help message\n" +
		"- `/new` — Start a new session (clears chat history)\n" +
		"- `/exit`, `/quit` — Exit the application\n\n" +
		"**Keyboard Shortcuts**\n" +
		"- `Enter` — Send message\n" +
		"- `Shift+Enter` — New line in message\n" +
		"- `Esc` — Cancel current response\n" +
		"- `Ctrl+C` — Quit\n\n" +
		"**Slash Command Autocomplete**\n" +
		"Type `/` to open the command picker. Use ↑↓ to navigate, Tab or Enter to select, Esc to dismiss."

	m.messages = append(m.messages, llm.Message{Role: "assistant", Content: helpText})
	m.reasoning = append(m.reasoning, "")
	m.claudeMeta = append(m.claudeMeta, "")
	m.stats.MessageCount++
	m.updateViewportContent()
	m.chatViewport.GotoBottom()
}

// newSession resets the conversation and all session statistics.
func (m *Model) newSession() {
	m.messages = nil
	m.reasoning = nil
	m.claudeMeta = nil
	m.claudeActiveMeta = ""
	m.currentResponse = ""
	m.currentReasoning = ""
	m.stats.MessageCount = 0
	m.stats.PromptTokens = 0
	m.stats.CompletionTokens = 0
	m.stats.ReasoningTokens = 0
	m.stats.TotalResponses = 0
	m.stats.TotalResponseTime = 0
	m.stats.LastResponseTime = 0
	m.err = nil
	m.updateViewportContent()
	m.chatViewport.GotoBottom()
	m.input.Focus()
}

// sendMessage takes the current input, appends it to history, and starts streaming.
func (m *Model) sendMessage() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}

	m.input.Reset()
	m.input.Blur()
	m.showCmdPopup = false
	m.filteredCmds = nil
	m.selectedCmdIdx = 0

	m.messages = append(m.messages, llm.Message{
		Role:    "user",
		Content: text,
	})
	m.stats.MessageCount++
	m.streaming = true
	m.currentResponse = ""
	m.err = nil
	m.stats.ResponseStart = time.Now()

	m.updateViewportContent()
	m.chatViewport.GotoBottom()

	// Copy messages for the goroutine.
	msgs := make([]llm.Message, len(m.messages))
	copy(msgs, m.messages)

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelStream = cancel

	client := m.llmClient
	return func() tea.Msg {
		ch := client.ChatCompletionStream(ctx, msgs)
		// We need to send the channel back to the model so it can keep reading.
		// Use a special message for this.
		return streamStartedMsg{ch: ch}
	}
}

// sendClaudeMessage appends the user prompt to history and starts a subprocess
// stream via the claude CLI, reusing the same streaming pipeline as sendMessage.
func (m *Model) sendClaudeMessage(prompt string) tea.Cmd {
	m.input.Blur()
	m.filteredCmds = nil
	m.selectedCmdIdx = 0

	m.messages = append(m.messages, llm.Message{
		Role:    "user",
		Content: "/claude " + prompt,
	})
	m.stats.MessageCount++
	m.streaming = true
	m.currentResponse = ""
	m.currentReasoning = ""
	m.err = nil
	m.stats.ResponseStart = time.Now()

	m.updateViewportContent()
	m.chatViewport.GotoBottom()

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelStream = cancel

	ch := streamClaudeResponse(ctx, prompt, m.claudeCfg)
	return func() tea.Msg {
		return streamStartedMsg{ch: ch}
	}
}

// streamStartedMsg carries the channel back to the model after the stream begins.
type streamStartedMsg struct {
	ch <-chan llm.StreamResponse
}

// waitForChunk reads one item from the stream channel and returns the appropriate Msg.
func waitForChunk(ch <-chan llm.StreamResponse) tea.Cmd {
	return func() tea.Msg {
		resp, ok := <-ch
		if !ok {
			return StreamDoneMsg{}
		}
		if resp.Error != nil {
			return StreamErrMsg{Err: resp.Error}
		}
		if resp.Meta != nil {
			return claudeMetaMsg{
				Model:          resp.Meta.Model,
				PermissionMode: resp.Meta.PermissionMode,
				Version:        resp.Meta.Version,
			}
		}
		if resp.Done {
			return StreamDoneMsg{Model: resp.Model, Usage: resp.Usage}
		}
		return StreamChunkMsg{Content: resp.Content, Reasoning: resp.Reasoning}
	}
}

// fetchModels returns a command that fetches available models from the LLM server.
func (m Model) fetchModels() tea.Cmd {
	client := m.llmClient
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		models, err := client.FetchModels(ctx)
		return ModelsMsg{Models: models, Err: err}
	}
}

// wrapText wraps s to fit within maxWidth columns.
func wrapText(s string, maxWidth int) string {
	if maxWidth <= 0 {
		maxWidth = 40
	}

	var result strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if lipgloss.Width(line) <= maxWidth {
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(line)
			continue
		}

		// Word-wrap long lines.
		words := strings.Fields(line)
		var currentLine strings.Builder
		for _, word := range words {
			wordW := lipgloss.Width(word)
			currentW := lipgloss.Width(currentLine.String())

			if currentW == 0 {
				// Break very long words.
				for wordW > maxWidth {
					if result.Len() > 0 || currentLine.Len() > 0 {
						result.WriteString("\n")
					}
					// Take as many runes as fit.
					runes := []rune(word)
					cut := maxWidth
					if cut > len(runes) {
						cut = len(runes)
					}
					result.WriteString(string(runes[:cut]))
					word = string(runes[cut:])
					wordW = lipgloss.Width(word)
				}
				currentLine.WriteString(word)
			} else if currentW+1+wordW <= maxWidth {
				currentLine.WriteString(" ")
				currentLine.WriteString(word)
			} else {
				if result.Len() > 0 {
					result.WriteString("\n")
				}
				result.WriteString(currentLine.String())
				currentLine.Reset()
				currentLine.WriteString(word)
			}
		}
		if currentLine.Len() > 0 {
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(currentLine.String())
		}
	}

	return result.String()
}

// formatClaudeMeta returns a short byline string for a claudeMetaMsg.
func formatClaudeMeta(msg claudeMetaMsg) string {
	return msg.Model + " · " + msg.PermissionMode + " mode"
}

// truncateStr truncates s to maxLen, adding "..." if truncated.
func truncateStr(s string, maxLen int) string {
	if maxLen <= 3 {
		maxLen = 3
	}
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
