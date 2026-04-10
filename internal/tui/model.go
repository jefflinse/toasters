package tui

import (
	"context"
	"encoding/json"
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
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/jefflinse/toasters/internal/service"
)

const (
	minSidebarWidth = 24
	inputHeight     = 3
	minWidthForBar  = 60

	minLeftPanelWidth    = 22
	minWidthForLeftPanel = 100
)

// ModelConfig holds all dependencies and configuration needed to create a Model.
type ModelConfig struct {
	Service      service.Service
	OpenInEditor func(path string) tea.Cmd // nil in client mode (remote server can't open local editor)
}

// streamingState holds all state related to the active operator stream.
type streamingState struct {
	streaming        bool
	currentResponse  string
	currentReasoning string
	operatorByline   string // formatted byline for the in-progress operator stream; cleared when done
}

// gridState holds all state for the dynamic NxM agent grid screen.
type gridState struct {
	showGrid      bool
	gridFocusCell int // 0-(cols*rows-1) within current page
	gridPage      int // current page index
	gridCols      int // computed from terminal width
	gridRows      int // computed from terminal height
}

// promptModeState holds all state for the interactive prompt mode
// (active when the operator calls ask_user, assign_team, etc.).
type promptModeState struct {
	promptMode     bool
	promptQuestion string
	promptOptions  []string // LLM-provided options; "Custom response..." appended at render time
	promptSelected int      // cursor index
	promptCustom   bool     // true when user selected "Custom response..." and is typing

	confirmDispatch bool             // true when promptMode is a dispatch confirmation
	changingTeam    bool             // true when promptMode is the "change team" sub-prompt
	pendingDispatch service.ToolCall // the assign_team call awaiting confirmation
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
	sessionID string // runtime session ID being viewed
}

// blockerModalState holds all state for the blocker Q&A modal.
type blockerModalState struct {
	show        bool
	jobID       string
	blocker     *service.Blocker
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

// progressState holds all state populated by the progress update events.
type progressState struct {
	jobs           []service.Job
	tasks          map[string][]service.Task
	reports        map[string][]service.ProgressReport
	activeSessions []service.AgentSession
	feedEntries    []service.FeedEntry       // recent activity feed entries
	mcpServers     []service.MCPServerStatus // MCP server connection status
}

// chatState holds all state related to the chat conversation history and
// collapsible message display.
type chatState struct {
	entries           []service.ChatEntry // consolidated chat history (messages, timestamps, reasoning, metadata)
	completionMsgIdx  map[int]bool        // indices of team-completion messages in entries
	expandedMsgs      map[int]bool        // which completion messages are currently expanded
	selectedMsgIdx    int                 // currently selected message index (-1 = none)
	expandedReasoning map[int]bool        // which entry indices have reasoning expanded
	collapsedTools    map[int]bool        // true = expanded; absent/false = collapsed (default)
}

// Model is the root Bubble Tea model for the toasters TUI.
type Model struct {
	width  int
	height int

	svc            service.Service
	openInEditor   func(path string) tea.Cmd
	chatViewport   viewport.Model
	input          textarea.Model
	stats          SessionStats
	err            error
	mdRender       *glamour.TermRenderer
	outputMdRender *glamour.TermRenderer // separate renderer sized for the fullscreen output modal

	// Sub-models grouping related state.
	stream      streamingState
	grid        gridState
	prompt      promptModeState
	promptModal promptModalState
	outputModal outputModalState
	cmdPopup    cmdPopupState
	scroll      scrollState
	progress    progressState
	chat        chatState

	jobs         []service.Job
	blockers     map[string]*service.Blocker // keyed by job ID
	selectedJob  int
	selectedTeam int
	focused      focusedPanel

	teams        []service.TeamView // available teams
	teamsDir     string             // path to the configured teams directory
	systemPrompt string             // assembled at startup; prepended to every LLM call

	// Teams modal state.
	teamsModal teamsModalState

	// Skills modal state.
	skillsModal skillsModalState

	// Agents modal state.
	agentsModal agentsModalState

	// MCP modal state.
	mcpModal mcpModalState

	// Catalog modal state (models.dev browser).
	catalogModal catalogModalState

	// Blocker modal state.
	blockerModal blockerModalState

	// Jobs modal state.
	jobsModal jobsModalState

	// Agent pane state.
	selectedAgentSlot int // which slot is highlighted in the agents pane

	loading      bool // true while waiting for AppReadyMsg before initializing the conversation
	loadingFrame int  // current animation frame index (0..numLoadingFrames-1)

	flashText string // transient status line; empty = hidden

	lpWidth int // cached left panel width for mouse hit-testing
	sbWidth int // cached sidebar width for mouse hit-testing

	// Collapsible panel state.
	leftPanelHidden        bool // true when user has toggled the left panel off via ctrl+l
	sidebarHidden          bool // true when user has toggled the sidebar off via ctrl+b
	leftPanelWidthOverride int  // 0 = use default computed width; >0 = user-resized width

	// Shared spinner animation frame counter.
	spinnerFrame   int
	spinnerRunning bool // true while the spinnerTick loop is live; prevents double-arming

	// Focus burst animation: plays rainbowText on the newly-focused sidebar tile
	// for focusAnimFramesTotal ticks, then stops.
	focusAnimPanel  focusedPanel // which panel is currently animating
	focusAnimFrames int          // frames remaining (counts down from 13 to 0)

	// Toast notification state.
	toasts      []toast
	nextToastID int

	// Runtime session tracking. Populated by SessionStartedMsg / SessionTextMsg
	// / SessionToolCallMsg / SessionToolResultMsg / SessionDoneMsg, all of which
	// originate from session.* events on the unified service event stream.
	runtimeSessions map[string]*runtimeSlot // keyed by session ID

	// Log view state.
	logView logViewState
}

// activityItem represents a single tool-call activity for display in a runtime agent card.
type activityItem struct {
	label    string // formatted display label, e.g. "write: main.go"
	toolName string // raw tool name
}

// runtimeSlot tracks a runtime agent session for TUI display.
type runtimeSlot struct {
	sessionID      string
	agentName      string
	teamName       string // team this agent belongs to (may be empty)
	task           string // short human-readable description of what this agent is doing
	jobID          string
	taskID         string
	status         string // "active", "completed", "failed", "cancelled"
	output         strings.Builder
	startTime      time.Time
	endTime        time.Time      // set when session completes; zero while active
	systemPrompt   string         // the system prompt given to the LLM
	initialMessage string         // the initial user message / task description
	activities     []activityItem // recent tool-call activities; newest appended last, capped at 6
}

// NewModel returns an initialized root model.
func NewModel(cfg ModelConfig) Model {
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
		svc:          cfg.Service,
		openInEditor: cfg.OpenInEditor,
		chatViewport: vp,
		input:        ta,
		stats: SessionStats{
			Connected: false,
		},
	}

	m.jobs = []service.Job{}
	m.blockers = make(map[string]*service.Blocker)
	m.selectedJob = 0
	m.focused = focusChat

	m.loading = true

	m.selectedAgentSlot = 0
	m.grid.gridFocusCell = 0
	m.grid.gridCols = 1
	m.grid.gridRows = 1

	m.chat.completionMsgIdx = make(map[int]bool)
	m.chat.expandedMsgs = make(map[int]bool)
	m.chat.selectedMsgIdx = -1
	m.chat.expandedReasoning = make(map[int]bool)
	m.chat.collapsedTools = make(map[int]bool)
	m.runtimeSessions = make(map[string]*runtimeSlot)

	return m
}

func (m *Model) Init() tea.Cmd {
	m.spinnerRunning = true // spinnerTick() is always armed at startup
	cmds := []tea.Cmd{
		tea.RequestWindowSize,
		m.fetchModels(),
		loadingTick(), // drive the loading screen animation
		spinnerTick(), // drive braille spinner animations
	}

	return tea.Batch(cmds...)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// Catalog modal key handling — intercept all keys when modal is open.
		if m.catalogModal.show {
			return m.updateCatalogModal(msg)
		}

		// MCP modal key handling — intercept all keys when modal is open.
		if m.mcpModal.show {
			return m.updateMCPModal(msg)
		}

		// Teams modal key handling — intercept all keys when modal is open.
		if m.teamsModal.show {
			return m.updateTeamsModal(msg)
		}

		// Skills modal key handling — intercept all keys when modal is open.
		if m.skillsModal.show {
			return m.updateSkillsModal(msg)
		}

		// Agents modal key handling — intercept all keys when modal is open.
		if m.agentsModal.show {
			return m.updateAgentsModal(msg)
		}

		// Jobs modal key handling — intercept all keys when modal is open.
		if m.jobsModal.show {
			return m.updateJobsModal(msg)
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

		// When the log view is visible, handle navigation and dismiss it.
		if m.logView.show {
			return m.updateLogView(msg)
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
			// Cycle focus: chat → jobs → agents → teams → operator → mcp → chat.
			// Skip hidden panels.
			// (Tab inside the slash command popup is handled above and returns early.)
			next := m.focused
			for {
				switch next {
				case focusChat:
					next = focusJobs
				case focusJobs:
					next = focusAgents
				case focusAgents:
					next = focusTeams
				case focusTeams:
					next = focusOperator
				case focusOperator:
					next = focusMCP
				case focusMCP:
					next = focusChat
				default:
					next = focusChat
				}
				// Skip left-panel targets when left panel is hidden.
				if m.leftPanelHidden && (next == focusJobs || next == focusAgents || next == focusTeams) {
					continue
				}
				// Skip sidebar targets when sidebar is hidden.
				if m.sidebarHidden && (next == focusOperator || next == focusMCP) {
					continue
				}
				break
			}
			focusCmd := m.setFocus(next)
			if next == focusChat {
				return m, tea.Batch(m.input.Focus(), focusCmd)
			}
			m.input.Blur()
			return m, focusCmd

		case "shift+tab":
			// Reverse cycle: chat → mcp → operator → teams → agents → jobs → chat.
			next := m.focused
			for {
				switch next {
				case focusChat:
					next = focusMCP
				case focusMCP:
					next = focusOperator
				case focusOperator:
					next = focusTeams
				case focusTeams:
					next = focusAgents
				case focusAgents:
					next = focusJobs
				case focusJobs:
					next = focusChat
				default:
					next = focusChat
				}
				// Skip left-panel targets when left panel is hidden.
				if m.leftPanelHidden && (next == focusJobs || next == focusAgents || next == focusTeams) {
					continue
				}
				// Skip sidebar targets when sidebar is hidden.
				if m.sidebarHidden && (next == focusOperator || next == focusMCP) {
					continue
				}
				break
			}
			focusCmd := m.setFocus(next)
			if next == focusChat {
				return m, tea.Batch(m.input.Focus(), focusCmd)
			}
			m.input.Blur()
			return m, focusCmd

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
				if m.selectedAgentSlot < maxGridSlots-1 {
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

		case "ctrl+v":
			// Paste clipboard text into the chat input when chat is focused.
			if m.focused == focusChat && !m.stream.streaming && !m.prompt.promptMode {
				if text, err := clipboard.ReadAll(); err == nil && text != "" {
					m.input.InsertString(text)
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

		case `ctrl+\`:
			if m.logView.show {
				m.closeLogView()
				return m, nil
			}
			cmd := m.openLogView()
			return m, cmd

		case "ctrl+l":
			// Toggle left panel visibility.
			m.leftPanelHidden = !m.leftPanelHidden
			// If hiding the panel while it's focused, switch to chat.
			if m.leftPanelHidden && (m.focused == focusJobs || m.focused == focusTeams || m.focused == focusAgents) {
				cmds = append(cmds, m.setFocus(focusChat))
				cmds = append(cmds, m.input.Focus())
			}
			m.resizeComponents()
			return m, tea.Batch(cmds...)

		case "ctrl+b":
			// Toggle sidebar visibility.
			m.sidebarHidden = !m.sidebarHidden
			// If hiding the sidebar while it's focused, switch to chat.
			if m.sidebarHidden && (m.focused == focusOperator || m.focused == focusMCP) {
				cmds = append(cmds, m.setFocus(focusChat))
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
			// Exit grid screen.
			if m.grid.showGrid {
				m.grid.showGrid = false
				return m, nil
			}
			// Cancel an in-flight operator stream.
			if m.stream.streaming {
				m.stream.streaming = false
				if m.stream.currentResponse != "" {
					m.appendEntry(service.ChatEntry{
						Message: service.ChatMessage{
							Role:    service.MessageRoleAssistant,
							Content: m.stream.currentResponse,
						},
						Timestamp:  time.Now(),
						Reasoning:  m.stream.currentReasoning,
						ClaudeMeta: m.stream.operatorByline,
					})
					m.stream.operatorByline = ""
					m.stream.currentResponse = ""
					m.stream.currentReasoning = ""
				}
				m.stats.CompletionTokensLive = 0
				m.stats.ReasoningTokensLive = 0
				m.updateViewportContent()
				return m, m.input.Focus()
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
				// Open jobs modal pre-selected on current job.
				m.jobsModal = jobsModalState{
					show:   true,
					jobIdx: m.selectedJob,
				}
				m.loadJobsForModal()
				m.loadJobDetail()
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
			// Open grid view when agents pane is focused.
			if m.focused == focusAgents {
				m.grid.showGrid = true
				return m, nil
			}
			// Open MCP modal when MCP pane is focused.
			if m.focused == focusMCP {
				m.mcpModal = mcpModalState{show: true}
				return m, nil
			}
			// focusJobs, focusOperator, focusChat: handled above or fall through to send.
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
				case "/skills":
					m.input.Reset()
					m.cmdPopup.show = false
					m.skillsModal = skillsModalState{show: true}
					m.reloadSkillsForModal()
					return m, nil
				case "/agents":
					m.input.Reset()
					m.cmdPopup.show = false
					m.agentsModal = agentsModalState{show: true}
					m.reloadAgentsForModal()
					return m, nil
				case "/jobs":
					m.input.Reset()
					m.cmdPopup.show = false
					m.jobsModal = jobsModalState{
						show: true,
					}
					m.loadJobsForModal()
					if len(m.jobsModal.jobs) > 0 {
						m.loadJobDetail()
					}
					return m, nil
				case "/mcp":
					m.input.Reset()
					m.cmdPopup.show = false
					m.mcpModal = mcpModalState{show: true}
					// servers field will be populated when mcpModal is updated to use service types
					return m, nil
				case "/models", "/providers":
					m.input.Reset()
					m.cmdPopup.show = false
					m.catalogModal = catalogModalState{show: true, loading: true}
					return m, m.fetchCatalog()
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
		m.grid.gridCols, m.grid.gridRows = computeGridDimensions(m.width, m.height)
		// Clamp page and focus cell to new bounds.
		cellsPerPage := m.grid.gridCols * m.grid.gridRows
		totalPages := (maxGridSlots + cellsPerPage - 1) / cellsPerPage
		if m.grid.gridPage >= totalPages {
			m.grid.gridPage = totalPages - 1
		}
		if m.grid.gridFocusCell >= cellsPerPage {
			m.grid.gridFocusCell = cellsPerPage - 1
		}
		m.resizeComponents()

	case tea.PasteMsg:
		// Handle bracketed paste (e.g. macOS Cmd+V in terminal) when chat is focused.
		if m.focused == focusChat && !m.stream.streaming && !m.prompt.promptMode {
			if msg.Content != "" {
				m.input.InsertString(msg.Content)
			}
		}
		return m, nil

	case ModelsMsg:
		// ListModels is a non-essential capability check. The model name
		// and endpoint already come from Operator().Status() during
		// AppReadyMsg, and the chat works whether or not the provider
		// supports a model listing endpoint. So when the call fails:
		//   - log a warning so a real failure is debuggable
		//   - flip the sidebar Connected indicator (it's the only signal
		//     of "the operator's provider is reachable")
		//   - DO NOT set m.err — surfacing a non-fatal capability check
		//     as a chat error makes the whole TUI look broken when in
		//     fact the operator is fully functional.
		if msg.Err != nil {
			m.stats.Connected = false
			slog.Warn("ListModels failed; sidebar context length unavailable", "error", msg.Err)
		} else {
			m.stats.Connected = true
			if len(msg.Models) > 0 {
				if m.stats.ModelName != "" {
					// We already have a configured model name from
					// AppReadyMsg. Try to find its context length from the
					// list, but never overwrite the name itself — provider
					// IDs (e.g. LM Studio filenames) often don't match the
					// canonical config value.
					for _, mi := range msg.Models {
						if mi.ID == m.stats.ModelName {
							m.stats.ContextLength = mi.ContextLength()
							break
						}
					}
				} else {
					// No configured name yet — fall back to a "loaded" model,
					// or the first one in the list.
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
		}
		m.updateViewportContent()

	case CatalogMsg:
		m.catalogModal.loading = false
		if msg.Err != nil {
			m.catalogModal.err = msg.Err
		} else {
			m.catalogModal.providers = msg.Providers
			m.catalogModal.configuredIDs = make(map[string]bool, len(msg.ConfiguredIDs))
			for _, id := range msg.ConfiguredIDs {
				m.catalogModal.configuredIDs[id] = true
			}
			m.catalogModal.filterProviders()
		}

	case AddProviderMsg:
		if msg.Err != nil {
			m.catalogModal.configErr = msg.Err.Error()
		} else {
			m.catalogModal.configDone = "Provider saved! It will be available shortly."
			// Mark as configured so it shows the indicator immediately.
			id := m.catalogModal.configValues[fieldID]
			if m.catalogModal.configuredIDs == nil {
				m.catalogModal.configuredIDs = make(map[string]bool)
			}
			m.catalogModal.configuredIDs[id] = true
			m.catalogModal.filterProviders()
		}

	case TeamsReloadedMsg:
		m.teams = msg.Teams
		if m.selectedTeam >= len(m.teams) && len(m.teams) > 0 {
			m.selectedTeam = len(m.teams) - 1
		} else if len(m.teams) == 0 {
			m.selectedTeam = 0
		}
		// The operator's system prompt is composed from operator.md at startup
		// (server side) and does not change when teams reload. The operator
		// discovers teams at runtime via the query_teams tool.
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
		m.initMessages()
		m.loading = false
		// Hydrate sidebar fields from the server-provided operator status so
		// they reflect the canonical configured values (rather than e.g. an
		// LM Studio filename that ListModels would return). Set these BEFORE
		// the ListModels response arrives so the model picker doesn't clobber.
		if msg.ModelName != "" {
			m.stats.ModelName = msg.ModelName
		}
		if msg.Endpoint != "" {
			m.stats.Endpoint = msg.Endpoint
		}
		// Hydrate persisted chat history from the server. This survives
		// server restarts so the user picks up where they left off.
		for _, entry := range msg.History {
			m.appendEntry(entry)
		}
		// Inject the pre-fetched greeting directly — no stream, no flash.
		// Only fire a greeting when no history exists; otherwise it would
		// look stale on top of a real conversation.
		if msg.Greeting != "" && len(msg.History) == 0 {
			m.appendEntry(service.ChatEntry{
				Message: service.ChatMessage{
					Role:    service.MessageRoleAssistant,
					Content: msg.Greeting,
				},
				Timestamp: time.Now(),
			})
		}
		m.updateViewportContent()
		return m, tea.Batch(cmds...)

	case TeamsAutoDetectDoneMsg:
		m.teamsModal.autoDetecting = false
		// The service's DetectCoordinator already called SetCoordinator if a match was found.
		// Just reload the teams list to reflect any changes.
		if msg.err == nil {
			m.reloadTeamsForModal()
		}
		return m, tea.Batch(cmds...)

	case teamPromotedMsg:
		m.teamsModal.promoting = false
		if msg.err != nil {
			slog.Error("failed to promote auto-team", "team", msg.teamName, "error", msg.err)
			cmds = append(cmds, m.addToast("⚠ Promote failed: "+msg.err.Error(), toastWarning))
		} else {
			cmds = append(cmds, m.addToast("✓ Promoted '"+msg.teamName+"' to managed team", toastSuccess))
			m.reloadTeamsForModal()
			// Select the newly promoted team (it is no longer an auto-team after promotion).
			for i, t := range m.teamsModal.teams {
				if t.Name() == msg.teamName && !t.IsAuto() {
					m.teamsModal.teamIdx = i
					break
				}
			}
		}
		return m, tea.Batch(cmds...)

	case teamGeneratedMsg:
		m.teamsModal.generating = false
		if msg.err != nil {
			cmds = append(cmds, m.addToast("⚠ Team generation failed: "+msg.err.Error(), toastWarning))
			return m, tea.Batch(cmds...)
		}

		// The service has already written the team directory and triggered a reload.
		// Extract team name from the generated team.md content (YAML frontmatter name: field).
		agentCount := len(msg.agentNames)
		teamName := extractFrontmatterName(msg.content)

		// Reload and select the newly created team.
		m.reloadTeamsForModal()
		for i, t := range m.teamsModal.teams {
			if t.Name() == teamName {
				m.teamsModal.teamIdx = i
				break
			}
		}

		cmds = append(cmds, m.addToast(
			fmt.Sprintf("✓ Team '%s' generated with %d agents", teamName, agentCount),
			toastSuccess,
		))
		return m, tea.Batch(cmds...)

	case blockerAnswersSubmittedMsg:
		// Mark answered, close modal.
		if b, ok := m.blockers[msg.jobID]; ok {
			b.Answered = true
		}
		m.blockerModal.show = false
		m.blockerModal.inputText = ""

		// Blocker re-spawn is handled by the operator/runtime; nothing to do here.
		_ = m.jobByID // suppress unused warning
		return m, nil

	case SessionStartedMsg:
		m.runtimeSessions[msg.SessionID] = &runtimeSlot{
			sessionID:      msg.SessionID,
			agentName:      msg.AgentName,
			teamName:       msg.TeamName,
			task:           msg.Task,
			jobID:          msg.JobID,
			taskID:         msg.TaskID,
			status:         "active",
			startTime:      time.Now(),
			systemPrompt:   msg.SystemPrompt,
			initialMessage: msg.InitialMessage,
		}
		cmds = append(cmds, m.addToast("🤖 "+msg.AgentName+" started", toastInfo))
		return m, tea.Batch(cmds...)

	case SessionTextMsg:
		slot, ok := m.runtimeSessions[msg.SessionID]
		if !ok {
			return m, nil
		}
		slot.output.WriteString(msg.Text)
		m.refreshOutputModalIfShowing(msg.SessionID, slot)
		return m, nil

	case SessionToolCallMsg:
		slot, ok := m.runtimeSessions[msg.SessionID]
		if !ok {
			return m, nil
		}
		if msg.ToolName != "" {
			fmt.Fprintf(&slot.output, "\n⚙ %s\n", msg.ToolName)
			label := activityLabel(msg.ToolName, json.RawMessage(msg.ToolInput))
			slot.activities = append(slot.activities, activityItem{label: label, toolName: msg.ToolName})
			if len(slot.activities) > 6 {
				slot.activities = slot.activities[len(slot.activities)-6:]
			}
		}
		m.refreshOutputModalIfShowing(msg.SessionID, slot)
		return m, nil

	case SessionToolResultMsg:
		slot, ok := m.runtimeSessions[msg.SessionID]
		if !ok {
			return m, nil
		}
		if msg.ToolOutput != "" {
			result := xansi.Strip(msg.ToolOutput)
			if len(result) > 200 {
				result = result[:200] + "..."
			}
			fmt.Fprintf(&slot.output, "→ %s\n", result)
		}
		m.refreshOutputModalIfShowing(msg.SessionID, slot)
		return m, nil

	case SessionDoneMsg:
		slot, ok := m.runtimeSessions[msg.SessionID]
		if !ok {
			return m, nil
		}
		slot.status = msg.Status
		slot.endTime = time.Now()
		cmds = append(cmds, m.addToast("🍞 "+msg.AgentName+" is done.", toastSuccess))
		// Note: agent completion is no longer reported back to the operator from
		// the TUI. The server is responsible for routing task completion into the
		// operator's event channel. The TUI is a viewer, not a router.
		return m, tea.Batch(cmds...)

	case tea.MouseClickMsg:
		// Click-to-focus: route clicks to the appropriate panel.
		// Don't steal clicks when any overlay is active.
		if !m.teamsModal.show && !m.skillsModal.show && !m.agentsModal.show &&
			!m.mcpModal.show && !m.catalogModal.show && !m.blockerModal.show && !m.grid.showGrid &&
			!m.promptModal.show && !m.outputModal.show && !m.loading {
			showLeftPanel := m.width >= minWidthForLeftPanel && !m.leftPanelHidden
			showSidebar := m.width >= minWidthForBar && !m.sidebarHidden
			sidebarStartX := m.width - m.sbWidth
			if showLeftPanel && msg.X < m.lpWidth {
				// Clicked left panel — determine which of the three panes was clicked.
				// Pane order (top to bottom): Jobs, Agents, Teams.
				teamsPaneH := m.leftPanelTeamsPaneHeight()
				agentsPaneH := m.leftPanelAgentsPaneHeight()
				teamsPaneY := m.height - teamsPaneH
				agentsPaneY := teamsPaneY - agentsPaneH
				if msg.Y >= teamsPaneY {
					// Clicked Teams pane.
					if m.focused != focusTeams {
						cmds = append(cmds, m.setFocus(focusTeams))
						m.input.Blur()
					}
				} else if msg.Y >= agentsPaneY {
					// Clicked Agents pane.
					if m.focused != focusAgents {
						cmds = append(cmds, m.setFocus(focusAgents))
						m.input.Blur()
					}
				} else {
					// Clicked Jobs pane.
					if m.focused != focusJobs {
						cmds = append(cmds, m.setFocus(focusJobs))
						m.input.Blur()
					}
				}
			} else if showSidebar && msg.X >= sidebarStartX {
				// Clicked sidebar — determine if Operator (top) or MCP (bottom) pane.
				// MCP pane sits at the bottom; compute its start Y.
				minMCPH := inputHeight + InputAreaStyle.GetVerticalFrameSize()
				mcpPaneY := m.height - minMCPH
				if msg.Y >= mcpPaneY {
					// Clicked MCP pane.
					if m.focused != focusMCP {
						cmds = append(cmds, m.setFocus(focusMCP))
						m.input.Blur()
					}
				} else {
					// Clicked Operator pane.
					if m.focused != focusOperator {
						cmds = append(cmds, m.setFocus(focusOperator))
						m.input.Blur()
					}
				}
			} else {
				// Clicked chat area — focus chat.
				if m.focused != focusChat {
					cmds = append(cmds, m.setFocus(focusChat))
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
		if m.focusAnimFrames > 0 {
			m.focusAnimFrames--
		}
		// Re-arm only if something is animating: operator streaming or any agent running.
		needTick := m.stream.streaming
		if !needTick {
			for _, rs := range m.runtimeSessions {
				if rs.status == "active" {
					needTick = true
					break
				}
			}
		}
		if !needTick && m.focusAnimFrames > 0 {
			needTick = true
		}
		if needTick {
			m.spinnerRunning = true
			return m, spinnerTick()
		}
		m.spinnerRunning = false
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
			case service.MCPServerStateConnected:
				connectedCount++
				totalTools += s.ToolCount
			case service.MCPServerStateFailed:
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

	case EditorFinishedMsg:
		// Editor closed — reload modal data. The fsnotify watcher will also
		// trigger a DB reload, but we refresh the modal's local copy immediately
		// so the user sees the change without waiting for the poll.
		if msg.Err != nil {
			slog.Error("editor exited with error", "error", msg.Err)
		}
		if m.skillsModal.show {
			m.reloadSkillsForModal()
		}
		if m.agentsModal.show {
			m.reloadAgentsForModal()
		}
		return m, nil

	case skillGeneratedMsg:
		m.skillsModal.generating = false
		if msg.err != nil {
			return m, m.addToast("⚠ Generation failed: "+msg.err.Error(), toastWarning)
		}
		// The service has already written the file and triggered a reload.
		// Extract the skill name from the generated content and reload the modal.
		skillName := extractFrontmatterName(msg.content)
		m.reloadSkillsForModal()
		// Select the newly created skill by matching its name.
		for i, sk := range m.skillsModal.skills {
			if sk.Name == skillName {
				m.skillsModal.skillIdx = i
				break
			}
		}
		if skillName == "" {
			skillName = "generated skill"
		}
		return m, m.addToast("✓ Skill '"+skillName+"' generated", toastSuccess)

	case agentGeneratedMsg:
		m.agentsModal.generating = false
		if msg.err != nil {
			return m, m.addToast("⚠ Generation failed: "+msg.err.Error(), toastWarning)
		}
		// The service has already written the file and triggered a reload.
		// Extract the agent name from the generated content and reload the modal.
		agentName := extractFrontmatterName(msg.content)
		m.reloadAgentsForModal()
		// Select the newly created agent by matching its name.
		for i, a := range m.agentsModal.agents {
			if a.Name == agentName {
				m.agentsModal.agentIdx = i
				break
			}
		}
		if agentName == "" {
			agentName = "generated agent"
		}
		return m, m.addToast("✓ Agent '"+agentName+"' generated", toastSuccess)

	case DefinitionsReloadedMsg:
		slog.Info("definitions reloaded from file watcher")
		return m, reloadTeamsCmd(m.svc)

	case ConnectionLostMsg:
		m.stats.Connected = false
		return m, m.addToast("Server connection lost, reconnecting...", toastWarning)

	case ConnectionRestoredMsg:
		m.stats.Connected = true
		return m, m.addToast("Server connection restored", toastSuccess)

	case OperatorTextMsg:
		slog.Debug("operator text", "len", len(msg.Text))
		// Accumulate streamed text into currentResponse.
		// The full response is committed as a single entry when OperatorDoneMsg arrives.
		if msg.Text != "" {
			m.stream.currentResponse += msg.Text
			// Live token estimate (~4 chars/token).
			m.stats.CompletionTokensLive = len([]rune(m.stream.currentResponse)) / 4
			if !m.stats.ResponseStart.IsZero() {
				m.stats.LastResponseTime = time.Since(m.stats.ResponseStart)
			}
			m.updateViewportContent()
			if !m.scroll.userScrolled {
				m.chatViewport.GotoBottom()
			} else {
				m.scroll.hasNewMessages = true
			}
		}
		return m, nil

	case OperatorDoneMsg:
		slog.Debug("operator turn done", "err", msg.Err)
		m.stream.streaming = false
		if msg.Err != nil {
			m.err = msg.Err
			m.updateViewportContent()
			cmds = append(cmds, m.input.Focus())
			return m, tea.Batch(cmds...)
		}
		if !m.stats.ResponseStart.IsZero() {
			m.stats.LastResponseTime = time.Since(m.stats.ResponseStart)
			m.stats.TotalResponseTime += m.stats.LastResponseTime
			m.stats.TotalResponses++
		}
		m.stats.CompletionTokens += msg.TokensOut
		m.stats.ReasoningTokens += msg.ReasoningTokens
		m.stats.CompletionTokensLive = 0
		if m.stream.currentResponse != "" {
			byline := "operator"
			if msg.ModelName != "" {
				byline = "operator · " + msg.ModelName
				m.stats.ModelName = msg.ModelName
			} else if m.stats.ModelName != "" {
				byline = "operator · " + m.stats.ModelName
			}
			m.appendEntry(service.ChatEntry{
				Message: service.ChatMessage{
					Role:    service.MessageRoleAssistant,
					Content: m.stream.currentResponse,
				},
				Timestamp:  time.Now(),
				ClaudeMeta: byline,
			})
			m.stream.currentResponse = ""
		}
		m.updateViewportContent()
		if !m.scroll.userScrolled {
			m.chatViewport.GotoBottom()
		} else {
			m.scroll.hasNewMessages = true
		}
		cmds = append(cmds, m.input.Focus())
		return m, tea.Batch(cmds...)

	case OperatorEventMsg:
		slog.Debug("operator event", "type", msg.Event.Type)
		// Render visible operator events as styled system entries in the chat.
		if line := formatServiceEvent(msg.Event); line != "" {
			m.appendEntry(service.ChatEntry{
				Message: service.ChatMessage{
					Role:    service.MessageRoleAssistant,
					Content: line,
				},
				Timestamp:  time.Now(),
				ClaudeMeta: "feed-event",
			})
			m.updateViewportContent()
			if !m.scroll.userScrolled {
				m.chatViewport.GotoBottom()
			}
		}
		return m, nil

	case progressPollMsg:
		m.progress.jobs = msg.Jobs
		m.progress.tasks = msg.Tasks
		m.progress.reports = msg.Progress
		m.progress.activeSessions = msg.Sessions
		m.progress.feedEntries = msg.FeedEntries
		m.progress.mcpServers = msg.MCPServers
		// Keep m.jobs in sync so the Jobs panel (which reads m.jobs via
		// displayJobs) reflects the latest polled state.
		m.jobs = msg.Jobs
		return m, nil

	case logTailTickMsg:
		return m.handleLogTailTick()

	case logContentMsg:
		m.applyLogContent(msg.lines)
		return m, nil
	}

	return m, tea.Batch(cmds...)
}

// refreshOutputModalIfShowing updates the output modal's content if it is
// currently displaying the given session, and applies an auto-tail policy.
// Called from session text/tool_call/tool_result message handlers.
func (m *Model) refreshOutputModalIfShowing(sessionID string, slot *runtimeSlot) {
	if !m.outputModal.show || m.outputModal.sessionID != sessionID {
		return
	}
	m.outputModal.content = slot.output.String()
	allLines := strings.Split(m.outputModal.content, "\n")
	modalH := m.height - 4
	if modalH < 10 {
		modalH = 10
	}
	// NOTE: maxScroll is computed on raw content lines; after markdown rendering
	// the actual rendered line count may differ. This is an approximation for auto-tail.
	maxScroll := len(allLines) - (modalH - 4)
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.outputModal.scroll >= maxScroll-2 {
		m.outputModal.scroll = maxScroll
	}
}

// extractFrontmatterName extracts the name: field from a YAML frontmatter block.
// Returns empty string if not found. Used to get the name from generated definition content.
func extractFrontmatterName(content string) string {
	// Find the frontmatter block between --- delimiters.
	if !strings.HasPrefix(content, "---") {
		return ""
	}
	rest := content[3:]
	// Skip optional newline after opening ---
	if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	fm := rest[:end]
	for _, line := range strings.Split(fm, "\n") {
		if strings.HasPrefix(line, "name:") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			// Strip surrounding quotes if present.
			name = strings.Trim(name, `"'`)
			return name
		}
	}
	return ""
}

// formatServiceEvent returns a styled single-line string for a service.Event,
// or empty string if the event type should not be displayed in the feed.
// This is the service-layer equivalent of formatOperatorEvent (defined in helpers.go).
func formatServiceEvent(ev service.Event) string {
	switch ev.Type {
	case service.EventTypeJobCreated:
		if p, ok := ev.Payload.(service.JobCreatedPayload); ok {
			return FeedTaskStartedStyle.Render(fmt.Sprintf("📋 Job created: %q", p.Title))
		}
		return FeedTaskStartedStyle.Render("📋 job created")

	case service.EventTypeTaskCreated:
		if p, ok := ev.Payload.(service.TaskCreatedPayload); ok {
			return FeedTaskStartedStyle.Render(fmt.Sprintf("◇ Task created: %q", p.Title))
		}
		return FeedTaskStartedStyle.Render("◇ task created")

	case service.EventTypeTaskAssigned:
		if p, ok := ev.Payload.(service.TaskAssignedPayload); ok {
			return FeedTaskStartedStyle.Render(fmt.Sprintf("➤ Task %q assigned to %s", p.Title, p.TeamID))
		}
		return FeedTaskStartedStyle.Render("➤ task assigned")

	case service.EventTypeTaskStarted:
		if p, ok := ev.Payload.(service.TaskStartedPayload); ok {
			return FeedTaskStartedStyle.Render(fmt.Sprintf("⚡ %s started task: %q", p.TeamID, p.Title))
		}
		return FeedTaskStartedStyle.Render("⚡ task started")

	case service.EventTypeTaskCompleted:
		if p, ok := ev.Payload.(service.TaskCompletedPayload); ok {
			return FeedTaskCompletedStyle.Render(fmt.Sprintf("✓ %s completed task", p.TeamID))
		}
		return FeedTaskCompletedStyle.Render("✓ task completed")

	case service.EventTypeTaskFailed:
		if p, ok := ev.Payload.(service.TaskFailedPayload); ok {
			return FeedTaskFailedStyle.Render(fmt.Sprintf("✗ %s failed task: %s", p.TeamID, p.Error))
		}
		return FeedTaskFailedStyle.Render("✗ task failed")

	case service.EventTypeBlockerReported:
		if p, ok := ev.Payload.(service.BlockerReportedPayload); ok {
			return FeedBlockerReportedStyle.Render(fmt.Sprintf("🚫 %s reported blocker: %s", p.TeamID, p.Description))
		}
		return FeedBlockerReportedStyle.Render("🚫 blocker reported")

	case service.EventTypeJobCompleted:
		if p, ok := ev.Payload.(service.JobCompletedPayload); ok {
			return FeedJobCompleteStyle.Render(fmt.Sprintf("✅ Job %q complete", p.Title))
		}
		return FeedJobCompleteStyle.Render("✅ job complete")

	case service.EventTypeProgressUpdate:
		// Progress updates are too noisy for the main feed — skip.
		return ""

	default:
		slog.Debug("unhandled service event type in feed", "type", ev.Type)
		return ""
	}
}

// reloadTeamsCmd returns a tea.Cmd that fetches the current team list from the
// service and delivers it as a TeamsReloadedMsg. Used by the
// DefinitionsReloadedMsg handler to keep m.teams in sync after file changes.
func reloadTeamsCmd(svc service.Service) tea.Cmd {
	return func() tea.Msg {
		teams, err := svc.Definitions().ListTeams(context.Background())
		if err != nil {
			slog.Warn("failed to reload teams after definitions change", "error", err)
			return nil
		}
		return TeamsReloadedMsg{Teams: teams}
	}
}
