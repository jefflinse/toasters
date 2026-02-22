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
	"github.com/jefflinse/toasters/internal/gateway"
	"github.com/jefflinse/toasters/internal/llm"
	"github.com/jefflinse/toasters/internal/workeffort"
)

const (
	minSidebarWidth = 24
	inputHeight     = 3
	minWidthForBar  = 60

	minLeftPanelWidth    = 22
	minWidthForLeftPanel = 100
)

// focusedPanel identifies which panel currently holds keyboard focus.
type focusedPanel int

const (
	focusChat        focusedPanel = iota
	focusWorkEfforts focusedPanel = iota
	focusAgents      focusedPanel = iota
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

// AgentOutputMsg is sent by the gateway notify callback when any slot output changes.
type AgentOutputMsg struct{}

// claudeMetaMsg carries model/mode info parsed from the claude CLI system/init event.
type claudeMetaMsg struct {
	Model          string
	PermissionMode string
	Version        string
}

// ToolCallMsg is emitted when the LLM requests one or more tool calls.
type ToolCallMsg struct {
	Calls []llm.ToolCall
}

// leftPanelWidth returns the width of the left panel for the given terminal width.
func leftPanelWidth(termWidth int) int {
	w := termWidth / 6
	if w < minLeftPanelWidth {
		return minLeftPanelWidth
	}
	return w
}

// sidebarWidth returns the sidebar width using the same formula as leftPanelWidth.
func sidebarWidth(termWidth int) int {
	w := termWidth / 6
	if w < minLeftPanelWidth {
		return minLeftPanelWidth
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

	workEfforts        []workeffort.WorkEffort
	selectedWorkEffort int
	focused            focusedPanel

	gateway *gateway.Gateway

	// Gateway notify channel — gateway writes to this; TUI polls it.
	agentNotifyCh chan struct{}

	// Agent pane state.
	selectedAgentSlot int            // which slot is highlighted in the agents pane (0-3)
	attachedSlot      int            // -1 = not attached; 0-3 = viewing this slot's output
	agentViewport     viewport.Model // viewport for attached slot output

	// Grid screen state.
	showGrid      bool
	gridFocusCell int // 0-3

	// Kill modal state.
	showKillModal   bool
	killModalSlots  []int // actual slot indices (0-3) of running slots
	selectedKillIdx int   // index into killModalSlots
}

// NewModel returns an initialized root model.
func NewModel(client *llm.Client, claudeCfg config.ClaudeConfig, configDir string, gw *gateway.Gateway) Model {
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

	m := Model{
		llmClient:    client,
		claudeCfg:    claudeCfg,
		chatViewport: vp,
		input:        ta,
		stats: SessionStats{
			Endpoint:  client.BaseURL(),
			Connected: false,
		},
	}

	efforts, _ := workeffort.List(configDir)
	m.workEfforts = efforts
	m.selectedWorkEffort = 0
	m.focused = focusChat
	m.gateway = gw

	m.agentNotifyCh = make(chan struct{}, 8) // buffered to avoid blocking gateway goroutines
	m.attachedSlot = -1
	m.selectedAgentSlot = 0
	m.gridFocusCell = 0

	agentVP := viewport.New()
	agentVP.MouseWheelEnabled = true
	agentVP.KeyMap = viewport.KeyMap{}
	m.agentViewport = agentVP

	if gw != nil {
		notifyCh := m.agentNotifyCh
		gw.SetNotify(func() {
			select {
			case notifyCh <- struct{}{}:
			default: // drop if full — next render will catch up
			}
		})
	}

	return m
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		tea.RequestWindowSize,
		m.fetchModels(),
	}
	if m.agentNotifyCh != nil {
		cmds = append(cmds, waitForAgentUpdate(m.agentNotifyCh))
	}
	return tea.Batch(cmds...)
}

// waitForAgentUpdate blocks until the gateway signals an output update.
func waitForAgentUpdate(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-ch
		return AgentOutputMsg{}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// When the grid screen is visible, handle navigation and dismiss it.
		if m.showGrid {
			switch msg.String() {
			case "ctrl+g", "esc":
				m.showGrid = false
				return m, nil
			case "k", "ctrl+k":
				if m.gateway != nil {
					_ = m.gateway.Kill(m.gridFocusCell)
				}
				return m, nil
			case "left":
				if m.gridFocusCell%2 == 1 {
					m.gridFocusCell--
				}
				return m, nil
			case "right":
				if m.gridFocusCell%2 == 0 {
					m.gridFocusCell++
				}
				return m, nil
			case "up":
				if m.gridFocusCell >= 2 {
					m.gridFocusCell -= 2
				}
				return m, nil
			case "down":
				if m.gridFocusCell < 2 {
					m.gridFocusCell += 2
				}
				return m, nil
			}
			return m, nil
		}

		// When the kill modal is visible, intercept all keys before any other handling.
		if m.showKillModal {
			switch msg.String() {
			case "up":
				if len(m.killModalSlots) > 0 {
					m.selectedKillIdx = (m.selectedKillIdx - 1 + len(m.killModalSlots)) % len(m.killModalSlots)
				}
				return m, nil
			case "down":
				if len(m.killModalSlots) > 0 {
					m.selectedKillIdx = (m.selectedKillIdx + 1) % len(m.killModalSlots)
				}
				return m, nil
			case "enter":
				if m.gateway != nil && len(m.killModalSlots) > 0 {
					_ = m.gateway.Kill(m.killModalSlots[m.selectedKillIdx])
				}
				m.showKillModal = false
				return m, nil
			case "esc":
				m.showKillModal = false
				return m, nil
			}
			return m, nil
		}

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

		case "tab":
			// Cycle focus: chat → work efforts → agents → chat.
			// (Tab inside the slash command popup is handled above and returns early.)
			switch m.focused {
			case focusChat:
				m.focused = focusWorkEfforts
				m.input.Blur()
				return m, nil
			case focusWorkEfforts:
				m.focused = focusAgents
				return m, nil
			case focusAgents:
				m.focused = focusChat
				return m, m.input.Focus()
			}

		case "up":
			// Navigate work efforts when that panel is focused.
			if m.focused == focusWorkEfforts && len(m.workEfforts) > 0 {
				if m.selectedWorkEffort > 0 {
					m.selectedWorkEffort--
				}
				return m, nil
			}
			// Navigate agent slots when agents pane is focused.
			if m.focused == focusAgents {
				if m.selectedAgentSlot > 0 {
					m.selectedAgentSlot--
				}
				return m, nil
			}

		case "down":
			// Navigate work efforts when that panel is focused.
			if m.focused == focusWorkEfforts && len(m.workEfforts) > 0 {
				if m.selectedWorkEffort < len(m.workEfforts)-1 {
					m.selectedWorkEffort++
				}
				return m, nil
			}
			// Navigate agent slots when agents pane is focused.
			if m.focused == focusAgents {
				if m.selectedAgentSlot < gateway.MaxSlots-1 {
					m.selectedAgentSlot++
				}
				return m, nil
			}

		case "ctrl+g":
			m.showGrid = !m.showGrid
			return m, nil

		case "esc":
			// Detach from agent slot first.
			if m.attachedSlot >= 0 {
				m.attachedSlot = -1
				return m, nil
			}
			// Exit grid screen.
			if m.showGrid {
				m.showGrid = false
				return m, nil
			}
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

		case "d":
			// Dismiss a completed agent slot when the agents pane is focused.
			if m.focused == focusAgents && m.gateway != nil {
				_ = m.gateway.Dismiss(m.selectedAgentSlot)
				if m.attachedSlot == m.selectedAgentSlot {
					m.attachedSlot = -1
				}
				return m, nil
			}

		case "enter":
			// Attach to an agent slot when the agents pane is focused.
			if m.focused == focusAgents && m.gateway != nil {
				slots := m.gateway.Slots()
				snap := slots[m.selectedAgentSlot]
				if snap.Active {
					m.attachedSlot = m.selectedAgentSlot
					m.agentViewport.SetContent(snap.Output)
					m.agentViewport.GotoBottom()
					// Resize agent viewport to match chat viewport dimensions.
					m.agentViewport.SetWidth(m.chatViewport.Width())
					m.agentViewport.SetHeight(m.chatViewport.Height())
				}
				return m, nil
			}
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
				case "/kill":
					m.input.Reset()
					m.showCmdPopup = false
					if m.gateway == nil {
						return m, nil
					}
					slots := m.gateway.Slots()
					var running []int
					for i, s := range slots {
						if s.Active && s.Status == gateway.SlotRunning {
							running = append(running, i)
						}
					}
					if len(running) == 0 {
						m.messages = append(m.messages, llm.Message{Role: "assistant", Content: "No running agents."})
						m.reasoning = append(m.reasoning, "")
						m.claudeMeta = append(m.claudeMeta, "")
						m.updateViewportContent()
						m.chatViewport.GotoBottom()
					} else {
						m.killModalSlots = running
						m.selectedKillIdx = 0
						m.showKillModal = true
					}
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

	case ToolCallMsg:
		// The LLM wants to call tools. Execute them synchronously, inject results,
		// then re-invoke the stream for the final answer.
		m.streaming = false

		// Append the assistant "tool call" turn to the conversation.
		// Content is empty for tool-call-only turns; ToolCalls carries the calls.
		assistantMsg := llm.Message{
			Role:      "assistant",
			Content:   "",
			ToolCalls: msg.Calls,
		}
		m.messages = append(m.messages, assistantMsg)
		m.reasoning = append(m.reasoning, "")
		m.claudeMeta = append(m.claudeMeta, "")

		// Execute each tool call and append results.
		for _, call := range msg.Calls {
			// Show a visual indicator in the chat.
			indicator := fmt.Sprintf("⚙ calling `%s`…", call.Function.Name)
			m.messages = append(m.messages, llm.Message{
				Role:    "assistant",
				Content: indicator,
			})
			m.reasoning = append(m.reasoning, "")
			m.claudeMeta = append(m.claudeMeta, "tool-call-indicator")

			result, err := llm.ExecuteTool(call)
			if err != nil {
				result = fmt.Sprintf("error: %s", err.Error())
			}

			m.messages = append(m.messages, llm.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    result,
			})
			// tool messages don't need entries in reasoning/claudeMeta
			// because updateViewportContent only increments assistantIdx for "assistant" role
		}

		// Update the viewport so the user sees the tool call indicators.
		m.updateViewportContent()
		m.chatViewport.GotoBottom()

		// Re-invoke the stream with the updated messages for the final answer.
		msgs := make([]llm.Message, len(m.messages))
		copy(msgs, m.messages)
		return m, m.startStream(msgs)

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

	case AgentOutputMsg:
		// Re-arm the poller.
		if m.agentNotifyCh != nil {
			cmds = append(cmds, waitForAgentUpdate(m.agentNotifyCh))
		}
		// If attached to a slot, update the agent viewport.
		if m.attachedSlot >= 0 && m.gateway != nil {
			slots := m.gateway.Slots()
			snap := slots[m.attachedSlot]
			if snap.Active {
				m.agentViewport.SetContent(snap.Output)
				m.agentViewport.GotoBottom()
			}
		}
		return m, tea.Batch(cmds...)

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

	// Grid screen takes over the full terminal.
	if m.showGrid {
		v := tea.NewView(m.renderGrid())
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	showSidebar := m.width >= minWidthForBar
	showLeftPanel := m.width >= minWidthForLeftPanel

	sbWidth := sidebarWidth(m.width)
	lpWidth := leftPanelWidth(m.width)

	var mainWidth int
	if showSidebar && showLeftPanel {
		// Left panel right border (1) + sidebar left border (1).
		mainWidth = m.width - sbWidth - 1 - lpWidth - 1
	} else if showSidebar {
		// Sidebar border takes 1 char on the left side.
		mainWidth = m.width - sbWidth - 1
	} else if showLeftPanel {
		// Left panel right border (1).
		mainWidth = m.width - lpWidth - 1
	} else {
		mainWidth = m.width
	}

	// Determine chat content and input area — swapped when attached to an agent slot.
	var chatContent string
	var inputOrStatus string
	if m.attachedSlot >= 0 && m.gateway != nil {
		slots := m.gateway.Slots()
		snap := slots[m.attachedSlot]
		header := fmt.Sprintf("⬡ %s · %s", snap.AgentName, snap.WorkEffortID)
		if snap.Status == gateway.SlotDone {
			header += " [done]"
		} else {
			header += " [running]"
		}
		chatContent = m.agentViewport.View()
		inputOrStatus = InputAreaStyle.Width(mainWidth).Render(
			DimStyle.Render(header + "  ·  Esc to detach · d to dismiss"),
		)
	} else {
		chatContent = m.chatViewport.View()
		inputOrStatus = InputAreaStyle.Width(mainWidth).Render(m.input.View())
	}

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

	// Build kill modal popup (if active) — mutually exclusive with cmd popup.
	var killPopupView string
	if m.showKillModal && m.gateway != nil {
		slots := m.gateway.Slots()
		var rows []string
		for i, slotIdx := range m.killModalSlots {
			snap := slots[slotIdx]
			label := fmt.Sprintf("[%d] %s · %s", slotIdx, snap.AgentName, snap.WorkEffortID)
			if i == m.selectedKillIdx {
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
		killPopupHeight := len(m.killModalSlots) + 1 // +1 for footer
		lines := strings.Split(chatContent, "\n")
		trimTo := len(lines) - killPopupHeight
		if trimTo < 0 {
			trimTo = 0
		}
		chatContent = strings.Join(lines[:trimTo], "\n")
	}

	chatView := ChatAreaStyle.Width(mainWidth).Render(chatContent)

	// Build claude meta strip (shown while a claude stream is active).
	var metaStrip string
	if m.claudeActiveMeta != "" {
		metaStrip = ClaudeMetaStyle.Width(mainWidth).Render("⬡ " + m.claudeActiveMeta)
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

	var content string
	if showLeftPanel && showSidebar {
		sidebar := m.renderSidebar(sbWidth)
		content = lipgloss.JoinHorizontal(lipgloss.Top, leftPanelView, mainColumn, sidebar)
	} else if showLeftPanel {
		content = lipgloss.JoinHorizontal(lipgloss.Top, leftPanelView, mainColumn)
	} else if showSidebar {
		// Build sidebar.
		sidebar := m.renderSidebar(sbWidth)
		content = lipgloss.JoinHorizontal(lipgloss.Top, mainColumn, sidebar)
	} else {
		content = mainColumn
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
	showLeftPanel := m.width >= minWidthForLeftPanel

	sbWidth := sidebarWidth(m.width)
	lpWidth := leftPanelWidth(m.width)

	var mainWidth int
	if showSidebar && showLeftPanel {
		// Left panel right border (1) + sidebar left border (1).
		mainWidth = m.width - sbWidth - 1 - lpWidth - 1
	} else if showSidebar {
		mainWidth = m.width - sbWidth - 1
	} else if showLeftPanel {
		// Left panel right border (1).
		mainWidth = m.width - lpWidth - 1
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

	// Agent viewport mirrors chat viewport dimensions.
	m.agentViewport.SetWidth(vpWidth)
	m.agentViewport.SetHeight(vpHeight)

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
		var artLines []string
		for _, line := range strings.Split(toasterArt, "\n") {
			artLines = append(artLines, HeaderStyle.Render(line))
		}
		welcome := strings.Join(artLines, "\n") + "\n\n"
		welcome += DimStyle.Render("Your personal army of toasters to") + HeaderStyle.Render("get shit done.") + "\n\n"
		welcome += DimStyle.Render("Operator connected to "+m.stats.Endpoint) + "\n\n"
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
			// Tool-call indicator messages render as a dimmed line without byline/reasoning.
			if assistantIdx < len(m.claudeMeta) && m.claudeMeta[assistantIdx] == "tool-call-indicator" {
				sb.WriteString(DimStyle.Render(msg.Content) + "\n\n")
				assistantIdx++
				continue
			}
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
		case "tool":
			// Render tool result as a dimmed block.
			preview := msg.Content
			if len(preview) > 300 {
				preview = preview[:300] + "…"
			}
			sb.WriteString(DimStyle.Render("⚙ tool result: "+preview) + "\n\n")
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

// renderLeftPanel builds the left panel with three vertically-stacked sub-panes:
// Work Efforts (top 40%), DAG (middle 30%), and Chat (bottom 30%).
func (m Model) renderLeftPanel(panelWidth, panelHeight int) string {
	contentWidth := panelWidth - LeftPanelStyle.GetHorizontalFrameSize()
	if contentWidth < 1 {
		contentWidth = 1
	}

	// Split panelHeight into 3 pane heights; give leftover rows to top.
	middleH := panelHeight * 30 / 100
	bottomH := panelHeight * 30 / 100
	topH := panelHeight - middleH - bottomH

	divider := LeftPanelDividerStyle.Render(strings.Repeat("─", contentWidth))

	// --- Top pane: Work Efforts ---
	var topLines []string
	topLines = append(topLines, LeftPanelHeaderStyle.Render("Work Efforts"))
	if len(m.workEfforts) == 0 {
		topLines = append(topLines, PlaceholderPaneStyle.Render("No work efforts"))
	} else {
		for i, we := range m.workEfforts {
			name := truncateStr(we.Name, contentWidth-3)
			if i == m.selectedWorkEffort {
				topLines = append(topLines, WorkEffortSelectedStyle.Render("🍞 "+name))
			} else {
				topLines = append(topLines, WorkEffortItemStyle.Render("   "+name))
			}
		}
	}
	topPane := lipgloss.NewStyle().Height(topH).Render(
		lipgloss.JoinVertical(lipgloss.Left, topLines...),
	)

	// --- Middle pane: DAG ---
	selectedName := ""
	if len(m.workEfforts) > 0 && m.selectedWorkEffort < len(m.workEfforts) {
		selectedName = m.workEfforts[m.selectedWorkEffort].Name
	}
	middlePane := lipgloss.NewStyle().Height(middleH).Render(
		lipgloss.JoinVertical(lipgloss.Left,
			LeftPanelHeaderStyle.Render(truncateStr(selectedName, contentWidth)),
			PlaceholderPaneStyle.Render("—"),
		),
	)

	// --- Bottom pane: Chat ---
	bottomPane := lipgloss.NewStyle().Height(bottomH).Render(
		lipgloss.JoinVertical(lipgloss.Left,
			LeftPanelHeaderStyle.Render("Chat"),
			PlaceholderPaneStyle.Render("—"),
		),
	)

	inner := lipgloss.JoinVertical(lipgloss.Left, topPane, divider, middlePane, divider, bottomPane)
	return LeftPanelStyle.Width(panelWidth).Height(panelHeight).Render(inner)
}

// renderSidebar builds the right sidebar with stats.
func (m Model) renderSidebar(sbWidth int) string {
	contentWidth := sbWidth - SidebarStyle.GetHorizontalPadding()

	var sb strings.Builder

	// Operator section: header and connected status on the same line.
	connStatus := ConnectedStyle.Render("connected")
	if !m.stats.Connected {
		connStatus = ErrorStyle.Render("disconnected")
	}
	headerText := SidebarHeaderStyle.Render("operator")
	gap := contentWidth - lipgloss.Width(headerText) - lipgloss.Width(connStatus)
	if gap < 1 {
		gap = 1
	}
	sb.WriteString(headerText + strings.Repeat(" ", gap) + connStatus)
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

	// Agents section.
	sb.WriteString("\n")
	sb.WriteString(SidebarHeaderStyle.Render("Agents"))
	sb.WriteString("\n\n")

	if m.gateway != nil {
		slots := m.gateway.Slots()
		hasAny := false
		for i, snap := range slots {
			if !snap.Active {
				continue
			}
			hasAny = true
			label := snap.AgentName + " · " + snap.WorkEffortID
			var statusIcon string
			if snap.Status == gateway.SlotRunning {
				statusIcon = "▶ "
			} else {
				statusIcon = "✓ "
			}
			line := statusIcon + truncateStr(label, contentWidth-2)
			if m.focused == focusAgents && i == m.selectedAgentSlot {
				sb.WriteString(WorkEffortSelectedStyle.Render("🍞 " + truncateStr(label, contentWidth-3)))
			} else if snap.Status == gateway.SlotDone {
				sb.WriteString(DimStyle.Render(statusIcon + truncateStr(label, contentWidth-2)))
			} else {
				sb.WriteString(SidebarValueStyle.Render(line))
			}
			sb.WriteString("\n")
		}
		if !hasAny {
			sb.WriteString(TaskUpdatesPaneStyle.Render("No agents running"))
		}
	} else {
		sb.WriteString(TaskUpdatesPaneStyle.Render("No agents running"))
	}
	sb.WriteString("\n")

	return SidebarStyle.
		Width(sbWidth).
		Height(m.height).
		Render(sb.String())
}

// renderGrid renders the 2×2 agent grid screen.
func (m Model) renderGrid() string {
	cellW := m.width / 2
	cellH := m.height / 2

	var cells [4]string
	slots := [4]gateway.SlotSnapshot{}
	if m.gateway != nil {
		slots = m.gateway.Slots()
	}

	for i := 0; i < 4; i++ {
		snap := slots[i]
		focused := i == m.gridFocusCell

		// Build header line.
		var header string
		if snap.Active {
			elapsed := time.Since(snap.StartTime).Round(time.Second)
			if snap.Status == gateway.SlotDone && !snap.EndTime.IsZero() {
				elapsed = snap.EndTime.Sub(snap.StartTime).Round(time.Second)
			}
			header = fmt.Sprintf("⬡ %s · %s · %s", snap.AgentName, snap.WorkEffortID, elapsed)
		} else {
			header = fmt.Sprintf("slot %d — empty", i)
		}

		// Inner dimensions (minus border + padding).
		innerH := cellH - 3 // header + borders
		innerW := cellW - 4 // borders + padding
		if innerH < 1 {
			innerH = 1
		}
		if innerW < 1 {
			innerW = 1
		}

		var body string
		if snap.Active && snap.Output != "" {
			lines := strings.Split(snap.Output, "\n")
			if len(lines) > innerH {
				lines = lines[len(lines)-innerH:]
			}
			for j, l := range lines {
				if len(l) > innerW {
					lines[j] = l[:innerW]
				}
			}
			body = strings.Join(lines, "\n")
		} else if !snap.Active {
			body = DimStyle.Render("— empty —")
		}

		// Choose border color based on focus.
		var borderColor color.Color
		if focused {
			borderColor = ColorPrimary
		} else {
			borderColor = ColorBorder
		}

		cellStyle := lipgloss.NewStyle().
			Width(cellW).
			Height(cellH).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor).
			Padding(0, 1)

		var headerStyle lipgloss.Style
		if focused {
			headerStyle = HeaderStyle
		} else {
			headerStyle = SidebarHeaderStyle
		}

		inner := headerStyle.Render(truncateStr(header, innerW)) + "\n" + body
		cells[i] = cellStyle.Render(inner)
	}

	top := lipgloss.JoinHorizontal(lipgloss.Top, cells[0], cells[1])
	bottom := lipgloss.JoinHorizontal(lipgloss.Top, cells[2], cells[3])
	return lipgloss.JoinVertical(lipgloss.Left, top, bottom)
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

// startStream begins a new LLM stream with the current messages and available tools.
// It sets m.streaming = true and m.stats.ResponseStart.
func (m *Model) startStream(msgs []llm.Message) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelStream = cancel
	m.streaming = true
	m.currentResponse = ""
	m.currentReasoning = ""
	m.stats.ResponseStart = time.Now()

	client := m.llmClient
	return func() tea.Msg {
		ch := client.ChatCompletionStreamWithTools(ctx, msgs, llm.AvailableTools)
		return streamStartedMsg{ch: ch}
	}
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
	m.err = nil

	m.updateViewportContent()
	m.chatViewport.GotoBottom()

	// Copy messages for the goroutine.
	msgs := make([]llm.Message, len(m.messages))
	copy(msgs, m.messages)

	return m.startStream(msgs)
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
		if len(resp.ToolCalls) > 0 {
			return ToolCallMsg{Calls: resp.ToolCalls}
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
