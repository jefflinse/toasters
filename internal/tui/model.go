package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/glamour"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/gateway"
	"github.com/jefflinse/toasters/internal/llm/tools"
	"github.com/jefflinse/toasters/internal/mcp"
	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

const (
	minSidebarWidth = 24
	inputHeight     = 3
	minWidthForBar  = 60

	minLeftPanelWidth    = 22
	minWidthForLeftPanel = 100
)

// ModelConfig holds all dependencies and configuration needed to create a Model.
// It replaces the 11-parameter NewModel constructor signature.
type ModelConfig struct {
	Client       provider.Provider
	ClaudeCfg    config.ClaudeConfig
	WorkspaceDir string
	Gateway      *gateway.Gateway
	TeamsDir     string
	Teams        []agents.Team
	Awareness    string
	ToolExec     *tools.ToolExecutor
	Store        db.Store
	Runtime      *runtime.Runtime
	MCPManager   *mcp.Manager
	Operator     *operator.Operator
}

// streamingState holds all state related to the active LLM stream.
type streamingState struct {
	streaming        bool
	currentResponse  string
	currentReasoning string
	streamCh         <-chan provider.StreamEvent
	cancelStream     context.CancelFunc
	claudeActiveMeta string // formatted byline for the in-progress claude stream; cleared when done
}

// gridState holds all state for the 2×2 agent grid screen.
type gridState struct {
	showGrid      bool
	gridFocusCell int // 0-3 within current page
	gridPage      int // 0-3 (each page shows 4 slots)
}

// promptModeState holds all state for the interactive prompt mode
// (active when the operator calls ask_user, kill_slot, assign_team, etc.).
type promptModeState struct {
	promptMode        bool
	promptQuestion    string
	promptOptions     []string          // LLM-provided options; "Custom response..." appended at render time
	promptSelected    int               // cursor index
	promptCustom      bool              // true when user selected "Custom response..." and is typing
	promptPendingCall provider.ToolCall // the tool call to resume after input

	confirmDispatch bool              // true when promptMode is a dispatch confirmation
	changingTeam    bool              // true when promptMode is the "change team" sub-prompt
	pendingDispatch provider.ToolCall // the assign_team call awaiting confirmation

	confirmKill     bool // true when promptMode is a kill confirmation
	pendingKillSlot int  // slot index awaiting kill confirmation

	confirmTimeout     bool // true when promptMode is a slot-timeout confirmation
	pendingTimeoutSlot int  // slot index awaiting timeout confirmation
}

// killModalState holds all state for the /kill confirmation modal.
type killModalState struct {
	show        bool
	slots       []int // actual slot indices (0-3) of running slots
	selectedIdx int   // index into slots
}

// promptModalState holds all state for the prompt-viewing modal overlay.
type promptModalState struct {
	show    bool
	content string // the full prompt text being displayed
	scroll  int    // scroll offset in lines
}

// outputModalState holds all state for the output-viewing modal overlay.
type outputModalState struct {
	show      bool
	content   string // the full output text being displayed
	scroll    int    // scroll offset in lines
	sessionID string // runtime session ID being viewed (empty = gateway slot)
}

// blockerModalState holds all state for the blocker Q&A modal.
type blockerModalState struct {
	show        bool
	jobID       string
	blocker     *Blocker
	questionIdx int
	inputText   string
}

// cmdPopupState holds all state for the slash command autocomplete popup.
type cmdPopupState struct {
	show         bool
	filteredCmds []SlashCommand
	selectedIdx  int
}

// scrollState holds all state related to chat viewport scrolling.
type scrollState struct {
	userScrolled     bool      // true when user has manually scrolled up; suppresses auto-scroll
	hasNewMessages   bool      // true when new content arrived while user was scrolled up
	scrollbarVisible bool      // true when scrollbar should be rendered (auto-hides after inactivity)
	lastScrollTime   time.Time // when the last scroll event occurred
}

// progressState holds all state populated by the SQLite progress polling loop.
type progressState struct {
	jobs             []*db.Job
	tasks            map[string][]*db.Task
	reports          map[string][]*db.ProgressReport
	activeSessions   []*db.AgentSession
	runtimeSnapshots []runtime.SessionSnapshot // live snapshots with real token counts
}

// chatState holds all state related to the chat conversation history and
// collapsible message display.
type chatState struct {
	entries           []ChatEntry  // consolidated chat history (messages, timestamps, reasoning, metadata)
	completionMsgIdx  map[int]bool // indices of team-completion messages in entries
	expandedMsgs      map[int]bool // which completion messages are currently expanded
	selectedMsgIdx    int          // currently selected message index (-1 = none)
	expandedReasoning map[int]bool // which entry indices have reasoning expanded
	collapsedTools    map[int]bool // true = expanded; absent/false = collapsed (default)

	// pendingCompletions buffers agent-completion notifications that arrive while
	// the operator stream is active. They are drained after the stream ends.
	pendingCompletions []pendingCompletion
}

// Model is the root Bubble Tea model for the toasters TUI.
type Model struct {
	width  int
	height int

	llmClient    provider.Provider
	claudeCfg    config.ClaudeConfig
	chatViewport viewport.Model
	input        textarea.Model
	stats        SessionStats
	err          error
	mdRender     *glamour.TermRenderer

	// Sub-models grouping related state.
	stream      streamingState
	grid        gridState
	prompt      promptModeState
	killModal   killModalState
	promptModal promptModalState
	outputModal outputModalState
	cmdPopup    cmdPopupState
	scroll      scrollState
	progress    progressState
	chat        chatState

	jobs         []*db.Job
	blockers     map[string]*Blocker // keyed by job ID
	selectedJob  int
	selectedTeam int
	focused      focusedPanel

	gateway        *gateway.Gateway
	toolExec       *tools.ToolExecutor
	toolsInFlight  bool
	toolCancelFunc context.CancelFunc

	teams        []agents.Team // available teams
	teamsDir     string        // path to the configured teams directory
	awareness    string        // team-awareness content used to build the operator prompt
	systemPrompt string        // assembled at startup; prepended to every LLM call

	// Teams modal state.
	teamsModal teamsModalState

	// MCP modal state.
	mcpModal mcpModalState

	// Blocker modal state.
	blockerModal blockerModalState

	// Gateway notify channel — gateway writes to this; TUI polls it.
	agentNotifyCh chan struct{}

	// Agent pane state.
	selectedAgentSlot int            // which slot is highlighted in the agents pane (0-3)
	attachedSlot      int            // -1 = not attached; 0-3 = viewing this slot's output
	agentViewport     viewport.Model // viewport for attached slot output

	loading      bool // true while waiting for AppReadyMsg before initializing the conversation
	loadingFrame int  // current animation frame index (0..numLoadingFrames-1)

	flashText string // transient status line; empty = hidden

	lpWidth int // cached left panel width for mouse hit-testing
	sbWidth int // cached sidebar width for mouse hit-testing

	// Collapsible panel state.
	leftPanelHidden        bool // true when user has toggled the left panel off via ctrl+l
	sidebarHidden          bool // true when user has toggled the sidebar off via ctrl+b
	leftPanelWidthOverride int  // 0 = use default computed width; >0 = user-resized width

	// prevSlotActive/Status track the last-seen state of each gateway slot so
	// AgentOutputMsg can detect Running→Done transitions and notify the operator.
	prevSlotActive [gateway.MaxSlots]bool
	prevSlotStatus [gateway.MaxSlots]gateway.SlotStatus

	// Shared spinner animation frame counter.
	spinnerFrame int

	// Toast notification state.
	toasts      []toast
	nextToastID int

	// MCP server manager — may be nil if no MCP servers are configured.
	mcpManager *mcp.Manager

	// Phase 1 integration: persistence and provider runtime.
	store           db.Store                // may be nil — graceful degradation
	runtime         *runtime.Runtime        // may be nil
	runtimeSessions map[string]*runtimeSlot // keyed by session ID

	// Phase 2 integration: operator event loop.
	operator *operator.Operator // may be nil — graceful degradation
}

// runtimeSlot tracks a runtime agent session for TUI display.
type runtimeSlot struct {
	sessionID      string
	agentName      string
	jobID          string
	status         string // "active", "completed", "failed", "cancelled"
	output         strings.Builder
	startTime      time.Time
	systemPrompt   string // the system prompt given to the LLM
	initialMessage string // the initial user message / task description
}

// NewModel returns an initialized root model.
func NewModel(cfg ModelConfig) Model {
	client := cfg.Client
	claudeCfg := cfg.ClaudeCfg
	gw := cfg.Gateway
	teamsDir := cfg.TeamsDir
	teams := cfg.Teams
	awareness := cfg.Awareness
	toolExec := cfg.ToolExec
	store := cfg.Store
	rt := cfg.Runtime
	mcpMgr := cfg.MCPManager
	op := cfg.Operator
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
		toolExec:     toolExec,
		store:        store,
		runtime:      rt,
		mcpManager:   mcpMgr,
		operator:     op,
		stats: SessionStats{
			Endpoint:  client.Name(),
			Connected: false,
		},
	}

	m.jobs = []*db.Job{}
	m.blockers = make(map[string]*Blocker)
	m.selectedJob = 0
	m.focused = focusChat
	m.gateway = gw

	m.teamsDir = teamsDir
	m.teams = teams
	m.awareness = awareness
	if awareness == "" {
		m.loading = true
	} else {
		m.systemPrompt = agents.BuildOperatorPrompt(teams, awareness)
		m.initMessages()
	}

	m.agentNotifyCh = make(chan struct{}, 8) // buffered to avoid blocking gateway goroutines
	m.attachedSlot = -1
	m.selectedAgentSlot = 0
	m.grid.gridFocusCell = 0

	m.chat.completionMsgIdx = make(map[int]bool)
	m.chat.expandedMsgs = make(map[int]bool)
	m.chat.selectedMsgIdx = -1
	m.chat.expandedReasoning = make(map[int]bool)
	m.chat.collapsedTools = make(map[int]bool)
	m.runtimeSessions = make(map[string]*runtimeSlot)

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

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		tea.RequestWindowSize,
		m.fetchModels(),
		loadingTick(), // drive the loading screen animation
		spinnerTick(), // drive braille spinner animations
	}
	if m.agentNotifyCh != nil {
		cmds = append(cmds, waitForAgentUpdate(m.agentNotifyCh))
	}
	if m.store != nil {
		cmds = append(cmds, scheduleProgressPoll())
	}
	// Fire MCP status toasts if servers are configured.
	if m.mcpManager != nil {
		if servers := m.mcpManager.Servers(); len(servers) > 0 {
			cmds = append(cmds, func() tea.Msg {
				return MCPStatusMsg{Servers: servers}
			})
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// MCP modal key handling — intercept all keys when modal is open.
		if m.mcpModal.show {
			return m.updateMCPModal(msg)
		}

		// Teams modal key handling — intercept all keys when modal is open.
		if m.teamsModal.show {
			return m.updateTeamsModal(msg)
		}

		// Blocker modal key handling — intercept all keys when modal is open.
		if m.blockerModal.show {
			return m.updateBlockerModal(msg)
		}

		// Prompt mode key handling — highest priority.
		if m.prompt.promptMode {
			return m.updatePromptMode(msg)
		}

		// When the prompt modal is visible, intercept all keys before any other handling.
		if m.promptModal.show {
			return m.updatePromptModal(msg)
		}

		// When the output modal is visible, intercept all keys before grid navigation.
		if m.outputModal.show {
			return m.updateOutputModal(msg)
		}

		// When the grid screen is visible, handle navigation and dismiss it.
		if m.grid.showGrid {
			return m.updateGrid(msg)
		}

		// When the kill modal is visible, intercept all keys before any other handling.
		if m.killModal.show {
			return m.updateKillModal(msg)
		}

		// When the slash command popup is visible, intercept navigation keys
		// before any other handling so they don't fall through to the textarea.
		if m.cmdPopup.show {
			if handled, cmd := m.updateCmdPopup(msg); handled {
				return m, cmd
			}
		}

		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit

		case "tab":
			// Cycle focus: chat → jobs → teams → agents → chat.
			// Skip hidden panels.
			// (Tab inside the slash command popup is handled above and returns early.)
			next := m.focused
			for {
				switch next {
				case focusChat:
					next = focusJobs
				case focusJobs:
					next = focusTeams
				case focusTeams:
					next = focusAgents
				case focusAgents:
					next = focusChat
				}
				// Skip left-panel targets when left panel is hidden.
				if m.leftPanelHidden && (next == focusJobs || next == focusTeams) {
					continue
				}
				// Skip sidebar target when sidebar is hidden.
				if m.sidebarHidden && next == focusAgents {
					continue
				}
				break
			}
			m.focused = next
			if next == focusChat {
				return m, m.input.Focus()
			}
			m.input.Blur()
			return m, nil

		case "pgup":
			// Scroll chat viewport up by one page.
			if m.focused == focusChat && !m.stream.streaming {
				m.chatViewport.PageUp()
				m.scroll.userScrolled = true
				return m, m.showScrollbar()
			}

		case "pgdown":
			// Scroll chat viewport down by one page.
			if m.focused == focusChat && !m.stream.streaming {
				m.chatViewport.PageDown()
				if m.chatViewport.AtBottom() {
					m.scroll.userScrolled = false
					m.scroll.hasNewMessages = false
				} else {
					m.scroll.userScrolled = true
				}
				return m, m.showScrollbar()
			}

		case "home":
			// Scroll chat viewport to top.
			if m.focused == focusChat && !m.stream.streaming {
				m.chatViewport.GotoTop()
				m.scroll.userScrolled = true
				return m, m.showScrollbar()
			}

		case "end":
			// Scroll chat viewport to bottom.
			if m.focused == focusChat && !m.stream.streaming {
				m.chatViewport.GotoBottom()
				m.scroll.userScrolled = false
				m.scroll.hasNewMessages = false
				return m, m.showScrollbar()
			}

		case "ctrl+u":
			// Scroll chat viewport up half page.
			if m.focused == focusChat && !m.stream.streaming {
				m.chatViewport.HalfPageUp()
				m.scroll.userScrolled = true
				return m, m.showScrollbar()
			}

		case "ctrl+d":
			// Scroll chat viewport down half page.
			if m.focused == focusChat && !m.stream.streaming {
				m.chatViewport.HalfPageDown()
				if m.chatViewport.AtBottom() {
					m.scroll.userScrolled = false
					m.scroll.hasNewMessages = false
				} else {
					m.scroll.userScrolled = true
				}
				return m, m.showScrollbar()
			}

		case "up":
			// Navigate jobs when that panel is focused.
			if m.focused == focusJobs {
				dj := m.displayJobs()
				if len(dj) > 0 && m.selectedJob > 0 {
					m.selectedJob--
				}
				return m, nil
			}
			// Navigate teams when teams pane is focused.
			if m.focused == focusTeams {
				if len(m.teams) > 0 && m.selectedTeam > 0 {
					m.selectedTeam--
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
			// Navigate jobs when that panel is focused.
			if m.focused == focusJobs {
				dj := m.displayJobs()
				if len(dj) > 0 && m.selectedJob < len(dj)-1 {
					m.selectedJob++
				}
				return m, nil
			}
			// Navigate teams when teams pane is focused.
			if m.focused == focusTeams {
				if m.selectedTeam < len(m.teams)-1 {
					m.selectedTeam++
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
		case "ctrl+x":
			// Toggle expand/collapse on the selected completion message when chat is focused.
			if m.focused == focusChat && !m.stream.streaming && m.chat.selectedMsgIdx >= 0 && m.chat.completionMsgIdx[m.chat.selectedMsgIdx] {
				m.chat.expandedMsgs[m.chat.selectedMsgIdx] = !m.chat.expandedMsgs[m.chat.selectedMsgIdx]
				m.updateViewportContent()
				return m, nil
			}
			// Toggle expand/collapse on tool-call indicator or tool result messages.
			if m.focused == focusChat && !m.stream.streaming && m.chat.selectedMsgIdx >= 0 && m.chat.selectedMsgIdx < len(m.chat.entries) {
				msg := m.chat.entries[m.chat.selectedMsgIdx].Message
				isToolIndicator := msg.Role == "assistant" && m.isToolCallIndicatorIdx(m.chat.selectedMsgIdx)
				isToolResult := msg.Role == "tool"
				if isToolIndicator || isToolResult {
					m.chat.collapsedTools[m.chat.selectedMsgIdx] = !m.chat.collapsedTools[m.chat.selectedMsgIdx]
					m.updateViewportContent()
					return m, nil
				}
			}

		case "ctrl+t":
			// Toggle expand/collapse of the reasoning trace for the most recent assistant message
			// that has a non-empty reasoning block.
			if m.focused == focusChat && !m.stream.streaming {
				// Find the last entry index with reasoning.
				lastReasoningIdx := -1
				for i, entry := range m.chat.entries {
					if entry.Message.Role == "assistant" && entry.Reasoning != "" {
						lastReasoningIdx = i
					}
				}
				if lastReasoningIdx >= 0 {
					m.chat.expandedReasoning[lastReasoningIdx] = !m.chat.expandedReasoning[lastReasoningIdx]
					m.updateViewportContent()
					return m, nil
				}
			}

		case "ctrl+y":
			// Copy the last assistant message to the clipboard when chat is focused.
			if m.focused == focusChat && !m.stream.streaming && !m.prompt.promptMode {
				for i := len(m.chat.entries) - 1; i >= 0; i-- {
					if m.chat.entries[i].Message.Role == "assistant" {
						_ = clipboard.WriteAll(m.chat.entries[i].Message.Content)
						m.flashText = "  ✓ copied to clipboard"
						cmds = append(cmds, clearFlash())
						cmds = append(cmds, m.addToast("🍞 Copied to clipboard!", toastInfo))
						break
					}
				}
			}

		case "ctrl+g":
			m.grid.showGrid = !m.grid.showGrid
			return m, nil

		case "ctrl+l":
			// Toggle left panel visibility.
			m.leftPanelHidden = !m.leftPanelHidden
			// If hiding the panel while it's focused, switch to chat.
			if m.leftPanelHidden && (m.focused == focusJobs || m.focused == focusTeams) {
				m.focused = focusChat
				cmds = append(cmds, m.input.Focus())
			}
			m.resizeComponents()
			return m, tea.Batch(cmds...)

		case "ctrl+b":
			// Toggle sidebar visibility.
			m.sidebarHidden = !m.sidebarHidden
			// If hiding the sidebar while it's focused, switch to chat.
			if m.sidebarHidden && m.focused == focusAgents {
				m.focused = focusChat
				cmds = append(cmds, m.input.Focus())
			}
			m.resizeComponents()
			return m, tea.Batch(cmds...)

		case "alt+[":
			// Decrease left panel width.
			showLeftPanel := m.width >= minWidthForLeftPanel && !m.leftPanelHidden
			if showLeftPanel {
				if m.leftPanelWidthOverride == 0 {
					m.leftPanelWidthOverride = leftPanelWidth(m.width)
				}
				m.leftPanelWidthOverride -= 2
				if m.leftPanelWidthOverride < minLeftPanelWidth {
					m.leftPanelWidthOverride = minLeftPanelWidth
				}
				m.resizeComponents()
			}
			return m, nil

		case "alt+]":
			// Increase left panel width.
			showLeftPanel := m.width >= minWidthForLeftPanel && !m.leftPanelHidden
			if showLeftPanel {
				if m.leftPanelWidthOverride == 0 {
					m.leftPanelWidthOverride = leftPanelWidth(m.width)
				}
				m.leftPanelWidthOverride += 2
				maxW := m.width / 2
				if m.leftPanelWidthOverride > maxW {
					m.leftPanelWidthOverride = maxW
				}
				m.resizeComponents()
			}
			return m, nil

		case "esc":
			// Cancel in-flight async tool execution.
			if m.toolsInFlight && m.toolCancelFunc != nil {
				m.toolCancelFunc()
				m.toolsInFlight = false
				m.toolCancelFunc = nil
				m.appendEntry(ChatEntry{
					Message:    provider.Message{Role: "assistant", Content: "[tool calls cancelled]"},
					Timestamp:  time.Now(),
					ClaudeMeta: "tool-call-indicator",
				})
				m.updateViewportContent()
				if !m.scroll.userScrolled {
					m.chatViewport.GotoBottom()
				}
				return m, m.input.Focus()
			}
			// Detach from agent slot first.
			if m.attachedSlot >= 0 {
				m.attachedSlot = -1
				return m, nil
			}
			// Exit grid screen.
			if m.grid.showGrid {
				m.grid.showGrid = false
				return m, nil
			}
			// Cancel an in-flight stream. (Popup esc is handled above and returns early.)
			if m.stream.streaming && m.stream.cancelStream != nil {
				m.stream.cancelStream()
				m.stream.streaming = false
				m.stream.cancelStream = nil
				m.stream.streamCh = nil
				if m.stream.currentResponse != "" {
					m.appendEntry(ChatEntry{
						Message:    provider.Message{Role: "assistant", Content: m.stream.currentResponse},
						Timestamp:  time.Now(),
						Reasoning:  m.stream.currentReasoning,
						ClaudeMeta: m.stream.claudeActiveMeta,
					})
					m.stream.claudeActiveMeta = ""
					m.stream.currentResponse = ""
					m.stream.currentReasoning = ""
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
			// Open blocker modal when jobs pane is focused and selected job has a blocker.
			if m.focused == focusJobs {
				dj := m.displayJobs()
				if len(dj) == 0 || m.selectedJob >= len(dj) {
					return m, nil
				}
				selectedJob := dj[m.selectedJob]
				if m.hasBlocker(selectedJob) {
					m.blockerModal.show = true
					m.blockerModal.jobID = selectedJob.ID
					m.blockerModal.blocker = m.blockers[selectedJob.ID]
					m.blockerModal.questionIdx = 0
					m.blockerModal.inputText = ""
					return m, nil
				}
				// Non-blocked job: no action on Enter for now.
				return m, nil
			}
			// Open teams modal pre-selected when teams pane is focused.
			if m.focused == focusTeams && len(m.teams) > 0 {
				idx := m.selectedTeam
				if idx >= len(m.teams) {
					idx = 0
				}
				m.teamsModal = teamsModalState{
					show:              true,
					teamIdx:           idx,
					autoDetectPending: make(map[string]bool),
				}
				m.reloadTeamsForModal()
				// Clamp teamIdx after reload in case the team list changed.
				if m.teamsModal.teamIdx >= len(m.teamsModal.teams) && len(m.teamsModal.teams) > 0 {
					m.teamsModal.teamIdx = len(m.teamsModal.teams) - 1
				}
				var teamCmd tea.Cmd
				if len(m.teamsModal.teams) > 0 {
					teamCmd = m.maybeAutoDetectCoordinator(m.teamsModal.teams[m.teamsModal.teamIdx])
				}
				return m, teamCmd
			}
			// Attach to an agent slot when the agents pane is focused.
			if m.focused == focusAgents && m.gateway != nil {
				slots := m.gateway.Slots()
				snap := slots[m.selectedAgentSlot]
				if snap.Active {
					m.attachedSlot = m.selectedAgentSlot
					m.agentViewport.SetContent(m.renderMarkdown(snap.Output))
					m.agentViewport.GotoBottom()
					// Resize agent viewport to match chat viewport dimensions.
					m.agentViewport.SetWidth(m.chatViewport.Width())
					m.agentViewport.SetHeight(m.chatViewport.Height())
				}
				return m, nil
			}
			// Send message on Enter when not streaming and input has content.
			// Shift+enter inserts a newline (handled by textarea).
			if !m.stream.streaming && strings.TrimSpace(m.input.Value()) != "" {
				text := strings.TrimSpace(m.input.Value())
				switch text {
				case "/exit", "/quit":
					return m, tea.Quit
				case "/help":
					m.input.Reset()
					m.cmdPopup.show = false
					m.appendHelpMessage()
					return m, nil
				case "/new":
					m.input.Reset()
					m.cmdPopup.show = false
					m.newSession()
					return m, nil
				case "/kill":
					m.input.Reset()
					m.cmdPopup.show = false
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
						m.appendEntry(ChatEntry{
							Message:   provider.Message{Role: "assistant", Content: "No running agents."},
							Timestamp: time.Now(),
						})
						m.updateViewportContent()
						if !m.scroll.userScrolled {
							m.chatViewport.GotoBottom()
						}
					} else {
						m.killModal.slots = running
						m.killModal.selectedIdx = 0
						m.killModal.show = true
					}
					return m, nil
				case "/teams":
					m.input.Reset()
					m.cmdPopup.show = false
					m.teamsModal = teamsModalState{show: true, autoDetectPending: make(map[string]bool)}
					m.reloadTeamsForModal()
					var teamCmd tea.Cmd
					if len(m.teamsModal.teams) > 0 {
						teamCmd = m.maybeAutoDetectCoordinator(m.teamsModal.teams[0])
					}
					return m, teamCmd
				case "/mcp":
					m.input.Reset()
					m.cmdPopup.show = false
					m.mcpModal = mcpModalState{
						show:    true,
						servers: m.mcpManager.Servers(), // nil-receiver safe
					}
					return m, nil
				}
				// /job <prompt> — create a new job via the operator LLM.
				if strings.HasPrefix(text, "/job ") {
					prompt := strings.TrimSpace(strings.TrimPrefix(text, "/job "))
					if prompt == "" {
						m.input.Reset()
						m.cmdPopup.show = false
						return m, nil
					}
					m.cmdPopup.show = false
					m.input.SetValue("[JOB REQUEST] " + prompt)
					return m, m.sendMessage()
				}
				// /claude <prompt> — stream via the claude CLI subprocess.
				if strings.HasPrefix(text, "/claude ") {
					prompt := strings.TrimSpace(strings.TrimPrefix(text, "/claude "))
					if prompt == "" {
						m.input.Reset()
						m.cmdPopup.show = false
						return m, nil
					}
					m.cmdPopup.show = false
					m.input.Reset()
					return m, m.sendClaudeMessage(prompt)
				}
				// /anthropic <prompt> — stream via the Anthropic API directly.
				if strings.HasPrefix(text, "/anthropic ") {
					prompt := strings.TrimSpace(strings.TrimPrefix(text, "/anthropic "))
					if prompt == "" {
						m.input.Reset()
						m.cmdPopup.show = false
						return m, nil
					}
					m.cmdPopup.show = false
					m.input.Reset()
					return m, m.sendAnthropicMessage(prompt)
				}
				// Not a recognized slash command — send to LLM.
				m.cmdPopup.show = false
				return m, m.sendMessage()
			}
		}

		// Delegate to textarea when not a special key we handle.
		if !m.stream.streaming {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			cmds = append(cmds, cmd)

			// Update slash command popup state based on current input value.
			inputVal := m.input.Value()
			if strings.HasPrefix(inputVal, "/") {
				m.cmdPopup.filteredCmds = filterCommands(inputVal)
				m.cmdPopup.show = len(m.cmdPopup.filteredCmds) > 0
				if m.cmdPopup.show && m.cmdPopup.selectedIdx >= len(m.cmdPopup.filteredCmds) {
					m.cmdPopup.selectedIdx = 0
				}
			} else {
				m.cmdPopup.show = false
				m.cmdPopup.filteredCmds = nil
				m.cmdPopup.selectedIdx = 0
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeComponents()

	case StreamChunkMsg:
		m.stream.currentResponse += msg.Content
		m.stream.currentReasoning += msg.Reasoning
		// Live token estimates (~4 chars/token).
		m.stats.CompletionTokensLive = len([]rune(m.stream.currentResponse)) / 4
		m.stats.ReasoningTokensLive = len([]rune(m.stream.currentReasoning)) / 4
		// Elapsed response time ticks up on every chunk.
		if !m.stats.ResponseStart.IsZero() {
			m.stats.LastResponseTime = time.Since(m.stats.ResponseStart)
		}
		m.updateViewportContent()
		if !m.scroll.userScrolled {
			m.chatViewport.GotoBottom()
		} else {
			m.scroll.hasNewMessages = true
		}
		if m.stream.streamCh != nil {
			cmds = append(cmds, waitForChunk(m.stream.streamCh))
		}

	case StreamDoneMsg:
		m.stream.streaming = false
		if msg.Model != "" {
			m.stats.ModelName = msg.Model
		}
		if msg.Usage != nil {
			m.stats.PromptTokens = msg.Usage.InputTokens
			m.stats.CompletionTokens += msg.Usage.OutputTokens
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
		if m.stream.currentResponse != "" {
			// For LM Studio (operator) turns, claudeActiveMeta is empty — fill in the operator byline.
			byline := m.stream.claudeActiveMeta
			if byline == "" && m.stats.ModelName != "" {
				byline = "operator · " + m.stats.ModelName
			}
			m.appendEntry(ChatEntry{
				Message:    provider.Message{Role: "assistant", Content: m.stream.currentResponse},
				Timestamp:  time.Now(),
				Reasoning:  m.stream.currentReasoning,
				ClaudeMeta: byline,
			})
			m.stream.claudeActiveMeta = ""
		}
		m.stream.currentResponse = ""
		m.stream.currentReasoning = ""
		m.stream.streamCh = nil
		m.stream.cancelStream = nil
		m.updateViewportContent()
		if !m.scroll.userScrolled {
			m.chatViewport.GotoBottom()
		} else {
			m.scroll.hasNewMessages = true
		}
		// Drain any completion notifications that arrived while we were streaming.
		// If there are pending completions, inject them and start a new stream instead
		// of returning focus to the input.
		if msgs, ok := m.drainPendingCompletions(); ok {
			m.updateViewportContent()
			if !m.scroll.userScrolled {
				m.chatViewport.GotoBottom()
			}
			cmds = append(cmds, m.startStream(msgs))
		} else {
			cmds = append(cmds, m.input.Focus())
		}

	case ToolCallMsg:
		return m.handleToolCalls(msg)

	case ToolResultMsg:
		// If tools were already cancelled (e.g. via Escape), discard the late result.
		// The goroutine always sends a ToolResultMsg even after cancellation.
		if !m.toolsInFlight {
			return m, nil
		}

		m.toolsInFlight = false
		if m.toolCancelFunc != nil {
			m.toolCancelFunc() // clean up context
			m.toolCancelFunc = nil
		}

		// Append each tool result to the conversation.
		for _, result := range msg.Results {
			content := result.Result
			if result.Err != nil {
				content = fmt.Sprintf("error: %s", result.Err.Error())
			}
			m.appendEntry(ChatEntry{
				Message:   provider.Message{Role: "tool", ToolCallID: result.CallID, Content: content},
				Timestamp: time.Now(),
			})
		}

		// Update the viewport.
		m.updateViewportContent()
		if !m.scroll.userScrolled {
			m.chatViewport.GotoBottom()
		}

		// Drain completions into entries; we rebuild messages from entries below.
		_, _ = m.drainPendingCompletions()

		// Re-invoke the stream with the updated messages for the final answer.
		return m, m.startStream(m.messagesFromEntries())

	case AskUserResponseMsg:
		return m.handleAskUserResponse(msg)

	case StreamErrMsg:
		m.stream.streaming = false
		m.err = msg.Err
		m.stats.Connected = false
		m.stream.streamCh = nil
		m.stream.cancelStream = nil
		if m.stream.currentResponse != "" {
			byline := m.stream.claudeActiveMeta
			if byline == "" && m.stats.ModelName != "" {
				byline = "operator · " + m.stats.ModelName
			}
			m.appendEntry(ChatEntry{
				Message:    provider.Message{Role: "assistant", Content: m.stream.currentResponse},
				Timestamp:  time.Now(),
				Reasoning:  m.stream.currentReasoning,
				ClaudeMeta: byline,
			})
			m.stream.claudeActiveMeta = ""
			m.stream.currentResponse = ""
			m.stream.currentReasoning = ""
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
		m.stream.streamCh = msg.ch
		cmds = append(cmds, waitForChunk(m.stream.streamCh))

	case claudeMetaMsg:
		m.stream.claudeActiveMeta = formatClaudeMeta(msg)
		return m, waitForChunk(m.stream.streamCh)

	case TeamsReloadedMsg:
		m.teams = msg.Teams
		if m.selectedTeam >= len(m.teams) && len(m.teams) > 0 {
			m.selectedTeam = len(m.teams) - 1
		} else if len(m.teams) == 0 {
			m.selectedTeam = 0
		}
		m.awareness = msg.Awareness
		m.systemPrompt = agents.BuildOperatorPrompt(m.teams, m.awareness)
		m.stats.SystemPromptTokens = estimateTokens(m.systemPrompt)
		m.toolExec.SetTeams(m.teams)
		if m.hasConversation() {
			m.chat.entries[0].Message.Content = m.systemPrompt
		} else {
			m.initMessages()
		}
		return m, tea.Batch(cmds...)

	case JobsReloadedMsg:
		m.jobs = msg.Jobs
		dj := m.displayJobs()
		if m.selectedJob >= len(dj) {
			if len(dj) > 0 {
				m.selectedJob = len(dj) - 1
			} else {
				m.selectedJob = 0
			}
		}
		return m, nil

	case AppReadyMsg:
		m.awareness = msg.Awareness
		m.systemPrompt = agents.BuildOperatorPrompt(m.teams, m.awareness)
		m.initMessages()
		m.loading = false
		// Inject the pre-fetched greeting directly — no stream, no flash.
		if msg.Greeting != "" {
			m.appendEntry(ChatEntry{
				Message:   provider.Message{Role: "assistant", Content: msg.Greeting},
				Timestamp: time.Now(),
			})
			m.updateViewportContent()
		}
		return m, tea.Batch(cmds...)

	case gateway.SlotTimeoutMsg:
		// Look up the slot snapshot to get team/job info for the prompt message.
		snapshots := m.gateway.Slots()
		snap := snapshots[msg.SlotID]
		slotDesc := fmt.Sprintf("slot %d", msg.SlotID)
		if snap.Active {
			slotDesc = fmt.Sprintf("slot %d (%s on %s)", msg.SlotID, snap.AgentName, snap.JobID)
		}
		promptText := fmt.Sprintf("⏱ %s has been running for 15m. Continue for another 15m, or kill it?\n\n(Auto-continuing in 1 minute...)", slotDesc)
		// Append as an assistant message so it shows in the chat.
		m.appendEntry(ChatEntry{
			Message:    provider.Message{Role: "assistant", Content: promptText},
			Timestamp:  time.Now(),
			ClaudeMeta: "ask-user-prompt",
		})
		// Enter prompt mode.
		m.prompt.promptMode = true
		m.prompt.confirmTimeout = true
		m.prompt.pendingTimeoutSlot = msg.SlotID
		m.prompt.promptOptions = []string{"Continue (+15m)", "Kill"}
		m.prompt.promptSelected = 0
		// Zero out the pending tool call so the AskUserResponseMsg handler
		// doesn't try to execute a real tool.
		m.prompt.promptPendingCall = provider.ToolCall{}
		m.updateViewportContent()
		if !m.scroll.userScrolled {
			m.chatViewport.GotoBottom()
		}
		return m, tea.Batch(m.input.Focus(), slotTimeoutPromptCmd(msg.SlotID))

	case SlotTimeoutPromptExpiredMsg:
		// Only act if this prompt is still active for this slot.
		if !m.prompt.confirmTimeout || m.prompt.pendingTimeoutSlot != msg.SlotID {
			return m, nil
		}
		// Auto-continue: extend the slot.
		m.prompt.confirmTimeout = false
		m.prompt.promptMode = false
		m.prompt.promptOptions = nil
		m.prompt.promptSelected = 0
		m.prompt.promptPendingCall = provider.ToolCall{}
		_ = m.gateway.ExtendSlot(msg.SlotID)
		m.appendEntry(ChatEntry{
			Message:    provider.Message{Role: "assistant", Content: fmt.Sprintf("Slot %d auto-continued (no response within 1m).", msg.SlotID)},
			Timestamp:  time.Now(),
			ClaudeMeta: "tool-call-indicator",
		})
		m.updateViewportContent()
		if !m.scroll.userScrolled {
			m.chatViewport.GotoBottom()
		}
		return m, m.input.Focus()

	case TeamsAutoDetectDoneMsg:
		m.teamsModal.autoDetecting = false
		if msg.agentName != "" && msg.err == nil {
			_ = agents.SetCoordinator(msg.teamDir, msg.agentName)
			m.reloadTeamsForModal()
		}
		return m, tea.Batch(cmds...)

	case blockerAnswersSubmittedMsg:
		// Mark answered, close modal.
		if b, ok := m.blockers[msg.jobID]; ok {
			b.Answered = true
		}
		m.blockerModal.show = false
		m.blockerModal.inputText = ""

		// Re-spawn the team with blocker context.
		_, ok := m.jobByID(msg.jobID)
		if ok && m.gateway != nil {
			spawnPrompt := fmt.Sprintf("A blocker was encountered on job '%s' and the user has provided responses. Resume the job addressing the blocker.", msg.jobID)

			// Use the team from the blocker directly.
			teamName := msg.blocker.Team

			// Find the team by name.
			var matchedTeam agents.Team
			for _, t := range m.teams {
				if t.Name == teamName {
					matchedTeam = t
					break
				}
			}
			if _, _, err := m.gateway.SpawnTeam(teamName, msg.jobID, spawnPrompt, matchedTeam); err != nil {
				slog.Error("failed to re-spawn team after blocker", "team", teamName, "job", msg.jobID, "error", err)
			} else {
				return m, spinnerTick() // re-arm spinner for agent heartbeat
			}
		}
		return m, nil

	case RuntimeSessionStartedMsg:
		m.runtimeSessions[msg.SessionID] = &runtimeSlot{
			sessionID:      msg.SessionID,
			agentName:      msg.AgentName,
			jobID:          msg.JobID,
			status:         "active",
			startTime:      time.Now(),
			systemPrompt:   msg.SystemPrompt,
			initialMessage: msg.InitialMessage,
		}
		cmds = append(cmds, m.addToast("🤖 "+msg.AgentName+" started (runtime)", toastInfo))
		return m, tea.Batch(cmds...)

	case RuntimeSessionEventMsg:
		ev := msg.Event
		slot, ok := m.runtimeSessions[ev.SessionID]
		if !ok {
			return m, nil // unknown session, ignore
		}
		switch ev.Type {
		case runtime.SessionEventText:
			slot.output.WriteString(ev.Text)
		case runtime.SessionEventToolCall:
			if ev.ToolCall != nil {
				fmt.Fprintf(&slot.output, "\n⚙ %s\n", ev.ToolCall.Name)
			}
		case runtime.SessionEventToolResult:
			if ev.ToolResult != nil {
				result := ev.ToolResult.Result
				if len(result) > 200 {
					result = result[:200] + "..."
				}
				fmt.Fprintf(&slot.output, "→ %s\n", result)
			}
		}

		// Live-update the output modal if it's showing this session.
		if m.outputModal.show && m.outputModal.sessionID == ev.SessionID {
			m.outputModal.content = slot.output.String()
			// Auto-tail: keep scroll at bottom if user hasn't scrolled up.
			allLines := strings.Split(m.outputModal.content, "\n")
			modalH := m.height * 3 / 4
			maxScroll := len(allLines) - modalH + 4
			if maxScroll < 0 {
				maxScroll = 0
			}
			if m.outputModal.scroll >= maxScroll-2 {
				m.outputModal.scroll = maxScroll
			}
		}

		return m, nil

	case RuntimeSessionDoneMsg:
		slot, ok := m.runtimeSessions[msg.SessionID]
		if !ok {
			return m, nil
		}
		slot.status = msg.Status

		// Build completion notification for the operator (same pattern as gateway).
		outputTail := slot.output.String()
		const maxTail = 2000
		if len(outputTail) > maxTail {
			outputTail = "…" + outputTail[len(outputTail)-maxTail:]
		}
		notification := fmt.Sprintf(
			"Agent '%s' (runtime session) has completed (job: %s, status: %s).\n\nOutput (last 2000 chars):\n%s",
			msg.AgentName, msg.JobID, msg.Status, outputTail,
		)

		cmds = append(cmds, m.addToast("🍞 "+msg.AgentName+" is done (runtime).", toastSuccess))

		if m.stream.streaming {
			m.chat.pendingCompletions = append(m.chat.pendingCompletions, pendingCompletion{
				notification: notification,
			})
		} else {
			m.appendEntry(ChatEntry{
				Message:   provider.Message{Role: "user", Content: notification},
				Timestamp: time.Now(),
			})
			completionIdx := len(m.chat.entries) - 1
			m.chat.completionMsgIdx[completionIdx] = true
			m.chat.selectedMsgIdx = completionIdx
			m.updateViewportContent()
			if !m.scroll.userScrolled {
				m.chatViewport.GotoBottom()
			}
			cmds = append(cmds, m.startStream(m.messagesFromEntries()))
		}
		return m, tea.Batch(cmds...)

	case AgentOutputMsg:
		return m.handleAgentOutput(msg)

	case tea.MouseClickMsg:
		// Click-to-focus: route clicks to the appropriate panel.
		// Don't steal clicks when any overlay is active.
		if !m.teamsModal.show && !m.mcpModal.show && !m.blockerModal.show && !m.grid.showGrid &&
			!m.killModal.show && !m.promptModal.show && !m.outputModal.show && !m.loading {
			showLeftPanel := m.width >= minWidthForLeftPanel && !m.leftPanelHidden
			showSidebar := m.width >= minWidthForBar && !m.sidebarHidden
			sidebarStartX := m.width - m.sbWidth
			if showLeftPanel && msg.X < m.lpWidth {
				// Clicked left panel — determine if Teams (bottom) or Jobs (top) pane.
				// Compute the Y row where the Teams pane begins.
				paneFrameV := FocusedPaneStyle.GetVerticalBorderSize()
				bottomContentH := 1 + len(m.teams)
				if len(m.teams) == 0 {
					bottomContentH = 2
				}
				teamsPaneH := bottomContentH + paneFrameV
				teamsPaneY := m.height - teamsPaneH
				if msg.Y >= teamsPaneY {
					// Clicked Teams pane.
					if m.focused != focusTeams {
						m.focused = focusTeams
						m.input.Blur()
					}
				} else {
					// Clicked Jobs or Job Detail pane.
					if m.focused != focusJobs {
						m.focused = focusJobs
						m.input.Blur()
					}
				}
			} else if showSidebar && msg.X >= sidebarStartX {
				// Clicked sidebar — focus agents pane.
				if m.focused != focusAgents {
					m.focused = focusAgents
					m.input.Blur()
				}
			} else {
				// Clicked chat area — focus chat.
				if m.focused != focusChat {
					m.focused = focusChat
					cmds = append(cmds, m.input.Focus())
				}
			}
		}

	case tea.MouseWheelMsg:
		// Forward mouse wheel events to viewport for scroll support.
		var cmd tea.Cmd
		m.chatViewport, cmd = m.chatViewport.Update(msg)
		cmds = append(cmds, cmd)
		cmds = append(cmds, m.showScrollbar())
		// Track whether user has scrolled away from the bottom.
		if m.chatViewport.AtBottom() {
			m.scroll.userScrolled = false
			m.scroll.hasNewMessages = false
		} else {
			m.scroll.userScrolled = true
		}

	case scrollbarHideMsg:
		// Hide the scrollbar if enough time has passed since the last scroll event.
		if time.Since(m.scroll.lastScrollTime) >= scrollbarHideDuration {
			m.scroll.scrollbarVisible = false
		}

	case loadingTickMsg:
		if m.loading {
			m.loadingFrame++
			return m, loadingTick()
		}
		return m, nil

	case spinnerTickMsg:
		m.spinnerFrame++
		// Re-arm only if something is animating: operator streaming, tools in flight, or any agent running.
		needTick := m.stream.streaming || m.toolsInFlight
		if !needTick && m.gateway != nil {
			for _, snap := range m.gateway.Slots() {
				if snap.Status == gateway.SlotRunning {
					needTick = true
					break
				}
			}
		}
		if !needTick {
			for _, rs := range m.runtimeSessions {
				if rs.status == "active" {
					needTick = true
					break
				}
			}
		}
		// Keep ticking while grid is visible so the rainbow title animates.
		if !needTick && m.grid.showGrid {
			needTick = true
		}
		if needTick {
			return m, spinnerTick()
		}
		return m, nil

	case clearFlashMsg:
		m.flashText = ""
		return m, nil

	case dismissToastMsg:
		for i, t := range m.toasts {
			if t.id == msg.id {
				m.toasts = append(m.toasts[:i], m.toasts[i+1:]...)
				break
			}
		}
		return m, nil

	case MCPStatusMsg:
		var toastCmds []tea.Cmd
		var connectedCount, totalTools int
		for _, s := range msg.Servers {
			switch s.State {
			case mcp.ServerConnected:
				connectedCount++
				totalTools += s.ToolCount
			case mcp.ServerFailed:
				toastCmds = append(toastCmds, m.addToast(
					fmt.Sprintf("⚠ MCP: %s failed", s.Name),
					toastWarning,
				))
			}
		}
		if connectedCount > 0 {
			serverWord := "servers"
			if connectedCount == 1 {
				serverWord = "server"
			}
			toolWord := "tools"
			if totalTools == 1 {
				toolWord = "tool"
			}
			toastCmds = append(toastCmds, m.addToast(
				fmt.Sprintf("🔌 MCP: %d %s, %d %s", connectedCount, serverWord, totalTools, toolWord),
				toastInfo,
			))
		}
		cmds = append(cmds, toastCmds...)
		return m, tea.Batch(cmds...)

	case DefinitionsReloadedMsg:
		slog.Info("definitions reloaded from file watcher")
		return m, nil

	case OperatorTextMsg:
		slog.Debug("operator text", "len", len(msg.Text))
		return m, nil

	case OperatorEventMsg:
		slog.Debug("operator event", "type", msg.Event.Type)
		return m, nil

	case progressPollTickMsg:
		if m.store != nil {
			return m, progressPollCmd(m.store, m.runtime)
		}
		return m, nil

	case progressPollMsg:
		m.progress.jobs = msg.Jobs
		m.progress.tasks = msg.Tasks
		m.progress.reports = msg.Progress
		m.progress.activeSessions = msg.Sessions
		m.progress.runtimeSnapshots = msg.RuntimeSessions
		return m, scheduleProgressPoll()
	}

	return m, tea.Batch(cmds...)
}
