package tui

import (
	"context"
	"fmt"
	"log"
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
	"github.com/jefflinse/toasters/internal/job"
	"github.com/jefflinse/toasters/internal/llm"
	"github.com/jefflinse/toasters/internal/llm/tools"
	"github.com/jefflinse/toasters/internal/runtime"
)

const (
	minSidebarWidth = 24
	inputHeight     = 3
	minWidthForBar  = 60

	minLeftPanelWidth    = 22
	minWidthForLeftPanel = 100
)

// Model is the root Bubble Tea model for the toasters TUI.
type Model struct {
	width  int
	height int

	llmClient        llm.Provider
	claudeCfg        config.ClaudeConfig
	entries          []ChatEntry // consolidated chat history (messages, timestamps, reasoning, metadata)
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
	claudeActiveMeta string // formatted byline for the in-progress claude stream; cleared when done

	// Slash command autocomplete popup state.
	showCmdPopup   bool
	filteredCmds   []SlashCommand
	selectedCmdIdx int

	jobs         []job.Job
	blockers     map[string]*job.Blocker // keyed by job ID
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
	repoRoot     string        // path to repo root (for /claude slash command path)

	// Teams modal state.
	teamsModal teamsModalState

	// Blocker modal state.
	blockerModal struct {
		show        bool
		jobID       string
		blocker     *job.Blocker
		questionIdx int
		inputText   string
	}

	// Gateway notify channel — gateway writes to this; TUI polls it.
	agentNotifyCh chan struct{}

	// Agent pane state.
	selectedAgentSlot int            // which slot is highlighted in the agents pane (0-3)
	attachedSlot      int            // -1 = not attached; 0-3 = viewing this slot's output
	agentViewport     viewport.Model // viewport for attached slot output

	// Grid screen state.
	showGrid      bool
	gridFocusCell int // 0-3 within current page
	gridPage      int // 0-3 (each page shows 4 slots)

	// Kill modal state.
	showKillModal   bool
	killModalSlots  []int // actual slot indices (0-3) of running slots
	selectedKillIdx int   // index into killModalSlots

	// Prompt modal state.
	showPromptModal    bool
	promptModalContent string // the full prompt text being displayed
	promptModalScroll  int    // scroll offset in lines

	// Output modal state.
	showOutputModal    bool
	outputModalContent string // the full output text being displayed
	outputModalScroll  int    // scroll offset in lines

	// Prompt mode — active when the operator calls ask_user
	promptMode        bool
	promptQuestion    string
	promptOptions     []string     // LLM-provided options; "Custom response..." appended at render time
	promptSelected    int          // cursor index
	promptCustom      bool         // true when user selected "Custom response..." and is typing
	promptPendingCall llm.ToolCall // the tool call to resume after input

	confirmDispatch bool         // true when promptMode is a dispatch confirmation
	changingTeam    bool         // true when promptMode is the "change team" sub-prompt
	pendingDispatch llm.ToolCall // the assign_team call awaiting confirmation

	confirmKill     bool // true when promptMode is a kill confirmation
	pendingKillSlot int  // slot index awaiting kill confirmation

	confirmTimeout     bool // true when promptMode is a slot-timeout confirmation
	pendingTimeoutSlot int  // slot index awaiting timeout confirmation

	loading      bool // true while waiting for AppReadyMsg before initializing the conversation
	loadingFrame int  // current animation frame index (0..numLoadingFrames-1)

	flashText string // transient status line; empty = hidden

	lpWidth int // cached left panel width for mouse hit-testing
	sbWidth int // cached sidebar width for mouse hit-testing

	// Collapsible panel state.
	leftPanelHidden        bool // true when user has toggled the left panel off via ctrl+l
	sidebarHidden          bool // true when user has toggled the sidebar off via ctrl+b
	leftPanelWidthOverride int  // 0 = use default computed width; >0 = user-resized width

	userScrolled     bool      // true when user has manually scrolled up; suppresses auto-scroll
	hasNewMessages   bool      // true when new content arrived while user was scrolled up
	scrollbarVisible bool      // true when scrollbar should be rendered (auto-hides after inactivity)
	lastScrollTime   time.Time // when the last scroll event occurred

	// prevSlotActive/Status track the last-seen state of each gateway slot so
	// AgentOutputMsg can detect Running→Done transitions and notify the operator.
	prevSlotActive [gateway.MaxSlots]bool
	prevSlotStatus [gateway.MaxSlots]gateway.SlotStatus

	// pendingCompletions buffers agent-completion notifications that arrive while
	// the operator stream is active. They are drained after the stream ends.
	pendingCompletions []pendingCompletion

	// Collapsible completion message state.
	completionMsgIdx map[int]bool // indices of team-completion messages in m.entries
	expandedMsgs     map[int]bool // which completion messages are currently expanded
	selectedMsgIdx   int          // currently selected message index (-1 = none)

	// Collapsible reasoning (thinking) state.
	expandedReasoning map[int]bool // which entry indices have reasoning expanded

	// Collapsible tool call/result state — keyed by message index.
	collapsedTools map[int]bool // true = expanded; absent/false = collapsed (default)

	// Shared spinner animation frame counter.
	spinnerFrame int

	// Toast notification state.
	toasts      []toast
	nextToastID int

	// Phase 1 integration: persistence and provider runtime.
	store           db.Store                // may be nil — graceful degradation
	runtime         *runtime.Runtime        // may be nil
	runtimeSessions map[string]*runtimeSlot // keyed by session ID
}

// runtimeSlot tracks a runtime agent session for TUI display.
type runtimeSlot struct {
	sessionID string
	agentName string
	jobID     string
	status    string // "active", "completed", "failed", "cancelled"
	output    strings.Builder
	startTime time.Time
}

// NewModel returns an initialized root model.
func NewModel(client llm.Provider, claudeCfg config.ClaudeConfig, workspaceDir string, gw *gateway.Gateway, repoRoot string, teamsDir string, teams []agents.Team, awareness string, toolExec *tools.ToolExecutor, store db.Store, rt *runtime.Runtime) Model {
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
		stats: SessionStats{
			Endpoint:  client.BaseURL(),
			Connected: false,
		},
	}

	jobs, _ := job.List(workspaceDir)
	m.jobs = jobs
	m.blockers = make(map[string]*job.Blocker)
	m.selectedJob = 0
	m.focused = focusChat
	m.gateway = gw

	m.repoRoot = repoRoot
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
	m.gridFocusCell = 0

	m.completionMsgIdx = make(map[int]bool)
	m.expandedMsgs = make(map[int]bool)
	m.selectedMsgIdx = -1
	m.expandedReasoning = make(map[int]bool)
	m.collapsedTools = make(map[int]bool)
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
	return tea.Batch(cmds...)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// Teams modal key handling — intercept all keys when modal is open.
		if m.teamsModal.show {
			return m.updateTeamsModal(msg)
		}

		// Blocker modal key handling — intercept all keys when modal is open.
		if m.blockerModal.show {
			return m.updateBlockerModal(msg)
		}

		// Prompt mode key handling — highest priority.
		if m.promptMode {
			return m.updatePromptMode(msg)
		}

		// When the prompt modal is visible, intercept all keys before any other handling.
		if m.showPromptModal {
			return m.updatePromptModal(msg)
		}

		// When the output modal is visible, intercept all keys before grid navigation.
		if m.showOutputModal {
			return m.updateOutputModal(msg)
		}

		// When the grid screen is visible, handle navigation and dismiss it.
		if m.showGrid {
			return m.updateGrid(msg)
		}

		// When the kill modal is visible, intercept all keys before any other handling.
		if m.showKillModal {
			return m.updateKillModal(msg)
		}

		// When the slash command popup is visible, intercept navigation keys
		// before any other handling so they don't fall through to the textarea.
		if m.showCmdPopup {
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
			if m.focused == focusChat && !m.streaming {
				m.chatViewport.PageUp()
				m.userScrolled = true
				return m, m.showScrollbar()
			}

		case "pgdown":
			// Scroll chat viewport down by one page.
			if m.focused == focusChat && !m.streaming {
				m.chatViewport.PageDown()
				if m.chatViewport.AtBottom() {
					m.userScrolled = false
					m.hasNewMessages = false
				} else {
					m.userScrolled = true
				}
				return m, m.showScrollbar()
			}

		case "home":
			// Scroll chat viewport to top.
			if m.focused == focusChat && !m.streaming {
				m.chatViewport.GotoTop()
				m.userScrolled = true
				return m, m.showScrollbar()
			}

		case "end":
			// Scroll chat viewport to bottom.
			if m.focused == focusChat && !m.streaming {
				m.chatViewport.GotoBottom()
				m.userScrolled = false
				m.hasNewMessages = false
				return m, m.showScrollbar()
			}

		case "ctrl+u":
			// Scroll chat viewport up half page.
			if m.focused == focusChat && !m.streaming {
				m.chatViewport.HalfPageUp()
				m.userScrolled = true
				return m, m.showScrollbar()
			}

		case "ctrl+d":
			// Scroll chat viewport down half page.
			if m.focused == focusChat && !m.streaming {
				m.chatViewport.HalfPageDown()
				if m.chatViewport.AtBottom() {
					m.userScrolled = false
					m.hasNewMessages = false
				} else {
					m.userScrolled = true
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
			if m.focused == focusChat && !m.streaming && m.selectedMsgIdx >= 0 && m.completionMsgIdx[m.selectedMsgIdx] {
				m.expandedMsgs[m.selectedMsgIdx] = !m.expandedMsgs[m.selectedMsgIdx]
				m.updateViewportContent()
				return m, nil
			}
			// Toggle expand/collapse on tool-call indicator or tool result messages.
			if m.focused == focusChat && !m.streaming && m.selectedMsgIdx >= 0 && m.selectedMsgIdx < len(m.entries) {
				msg := m.entries[m.selectedMsgIdx].Message
				isToolIndicator := msg.Role == "assistant" && m.isToolCallIndicatorIdx(m.selectedMsgIdx)
				isToolResult := msg.Role == "tool"
				if isToolIndicator || isToolResult {
					m.collapsedTools[m.selectedMsgIdx] = !m.collapsedTools[m.selectedMsgIdx]
					m.updateViewportContent()
					return m, nil
				}
			}

		case "ctrl+t":
			// Toggle expand/collapse of the reasoning trace for the most recent assistant message
			// that has a non-empty reasoning block.
			if m.focused == focusChat && !m.streaming {
				// Find the last entry index with reasoning.
				lastReasoningIdx := -1
				for i, entry := range m.entries {
					if entry.Message.Role == "assistant" && entry.Reasoning != "" {
						lastReasoningIdx = i
					}
				}
				if lastReasoningIdx >= 0 {
					m.expandedReasoning[lastReasoningIdx] = !m.expandedReasoning[lastReasoningIdx]
					m.updateViewportContent()
					return m, nil
				}
			}

		case "ctrl+y":
			// Copy the last assistant message to the clipboard when chat is focused.
			if m.focused == focusChat && !m.streaming && !m.promptMode {
				for i := len(m.entries) - 1; i >= 0; i-- {
					if m.entries[i].Message.Role == "assistant" {
						_ = clipboard.WriteAll(m.entries[i].Message.Content)
						m.flashText = "  ✓ copied to clipboard"
						cmds = append(cmds, clearFlash())
						cmds = append(cmds, m.addToast("🍞 Copied to clipboard!", toastInfo))
						break
					}
				}
			}

		case "ctrl+g":
			m.showGrid = !m.showGrid
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
					Message:    llm.Message{Role: "assistant", Content: "[tool calls cancelled]"},
					Timestamp:  time.Now(),
					ClaudeMeta: "tool-call-indicator",
				})
				m.updateViewportContent()
				if !m.userScrolled {
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
					m.appendEntry(ChatEntry{
						Message:    llm.Message{Role: "assistant", Content: m.currentResponse},
						Timestamp:  time.Now(),
						Reasoning:  m.currentReasoning,
						ClaudeMeta: m.claudeActiveMeta,
					})
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
						m.appendEntry(ChatEntry{
							Message:   llm.Message{Role: "assistant", Content: "No running agents."},
							Timestamp: time.Now(),
						})
						m.updateViewportContent()
						if !m.userScrolled {
							m.chatViewport.GotoBottom()
						}
					} else {
						m.killModalSlots = running
						m.selectedKillIdx = 0
						m.showKillModal = true
					}
					return m, nil
				case "/teams":
					m.input.Reset()
					m.showCmdPopup = false
					m.teamsModal = teamsModalState{show: true, autoDetectPending: make(map[string]bool)}
					m.reloadTeamsForModal()
					var teamCmd tea.Cmd
					if len(m.teamsModal.teams) > 0 {
						teamCmd = m.maybeAutoDetectCoordinator(m.teamsModal.teams[0])
					}
					return m, teamCmd
				}
				// /job <prompt> — create a new job via the operator LLM.
				if strings.HasPrefix(text, "/job ") {
					prompt := strings.TrimSpace(strings.TrimPrefix(text, "/job "))
					if prompt == "" {
						m.input.Reset()
						m.showCmdPopup = false
						return m, nil
					}
					m.showCmdPopup = false
					m.input.SetValue("[JOB REQUEST] " + prompt)
					return m, m.sendMessage()
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
				// /anthropic <prompt> — stream via the Anthropic API directly.
				if strings.HasPrefix(text, "/anthropic ") {
					prompt := strings.TrimSpace(strings.TrimPrefix(text, "/anthropic "))
					if prompt == "" {
						m.input.Reset()
						m.showCmdPopup = false
						return m, nil
					}
					m.showCmdPopup = false
					m.input.Reset()
					return m, m.sendAnthropicMessage(prompt)
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
		if !m.userScrolled {
			m.chatViewport.GotoBottom()
		} else {
			m.hasNewMessages = true
		}
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
			// For LM Studio (operator) turns, claudeActiveMeta is empty — fill in the operator byline.
			byline := m.claudeActiveMeta
			if byline == "" && m.stats.ModelName != "" {
				byline = "operator · " + m.stats.ModelName
			}
			m.appendEntry(ChatEntry{
				Message:    llm.Message{Role: "assistant", Content: m.currentResponse},
				Timestamp:  time.Now(),
				Reasoning:  m.currentReasoning,
				ClaudeMeta: byline,
			})
			m.claudeActiveMeta = ""
		}
		m.currentResponse = ""
		m.currentReasoning = ""
		m.streamCh = nil
		m.cancelStream = nil
		m.updateViewportContent()
		if !m.userScrolled {
			m.chatViewport.GotoBottom()
		} else {
			m.hasNewMessages = true
		}
		// Drain any completion notifications that arrived while we were streaming.
		// If there are pending completions, inject them and start a new stream instead
		// of returning focus to the input.
		if msgs, ok := m.drainPendingCompletions(); ok {
			m.updateViewportContent()
			if !m.userScrolled {
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
				Message:   llm.Message{Role: "tool", ToolCallID: result.CallID, Content: content},
				Timestamp: time.Now(),
			})
		}

		// Update the viewport.
		m.updateViewportContent()
		if !m.userScrolled {
			m.chatViewport.GotoBottom()
		}

		// Drain completions into entries; we rebuild messages from entries below.
		_, _ = m.drainPendingCompletions()

		// Re-invoke the stream with the updated messages for the final answer.
		return m, m.startStream(m.messagesFromEntries())

	case AskUserResponseMsg:
		return m.handleAskUserResponse(msg)

	case StreamErrMsg:
		m.streaming = false
		m.err = msg.Err
		m.stats.Connected = false
		m.streamCh = nil
		m.cancelStream = nil
		if m.currentResponse != "" {
			byline := m.claudeActiveMeta
			if byline == "" && m.stats.ModelName != "" {
				byline = "operator · " + m.stats.ModelName
			}
			m.appendEntry(ChatEntry{
				Message:    llm.Message{Role: "assistant", Content: m.currentResponse},
				Timestamp:  time.Now(),
				Reasoning:  m.currentReasoning,
				ClaudeMeta: byline,
			})
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
		m.toolExec.Teams = m.teams
		if m.hasConversation() {
			m.entries[0].Message.Content = m.systemPrompt
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
		for _, j := range m.jobs {
			if _, exists := m.blockers[j.ID]; !exists {
				if b, err := job.ReadBlocker(j.Dir); err == nil && b != nil {
					m.blockers[j.ID] = b
				}
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
				Message:   llm.Message{Role: "assistant", Content: msg.Greeting},
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
			Message:    llm.Message{Role: "assistant", Content: promptText},
			Timestamp:  time.Now(),
			ClaudeMeta: "ask-user-prompt",
		})
		// Enter prompt mode.
		m.promptMode = true
		m.confirmTimeout = true
		m.pendingTimeoutSlot = msg.SlotID
		m.promptOptions = []string{"Continue (+15m)", "Kill"}
		m.promptSelected = 0
		// Zero out the pending tool call so the AskUserResponseMsg handler
		// doesn't try to execute a real tool.
		m.promptPendingCall = llm.ToolCall{}
		m.updateViewportContent()
		if !m.userScrolled {
			m.chatViewport.GotoBottom()
		}
		return m, tea.Batch(m.input.Focus(), slotTimeoutPromptCmd(msg.SlotID))

	case SlotTimeoutPromptExpiredMsg:
		// Only act if this prompt is still active for this slot.
		if !m.confirmTimeout || m.pendingTimeoutSlot != msg.SlotID {
			return m, nil
		}
		// Auto-continue: extend the slot.
		m.confirmTimeout = false
		m.promptMode = false
		m.promptOptions = nil
		m.promptSelected = 0
		m.promptPendingCall = llm.ToolCall{}
		_ = m.gateway.ExtendSlot(msg.SlotID)
		m.appendEntry(ChatEntry{
			Message:    llm.Message{Role: "assistant", Content: fmt.Sprintf("Slot %d auto-continued (no response within 1m).", msg.SlotID)},
			Timestamp:  time.Now(),
			ClaudeMeta: "tool-call-indicator",
		})
		m.updateViewportContent()
		if !m.userScrolled {
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
		j, ok := m.jobByID(msg.jobID)
		if ok && m.gateway != nil {
			spawnPrompt := fmt.Sprintf("A blocker was encountered on job '%s' and the user has provided responses. See BLOCKER.md in the job directory for the full context and answers. Resume the job addressing the blocker.", msg.jobID)

			// Prefer team from TASK.md; fall back to blocker.Team for backward compat.
			teamName := msg.blocker.Team
			if t, err := job.GetFirstTaskTeam(j.Dir); err == nil && t != "" {
				teamName = t
			}

			// Find the team by name.
			var matchedTeam agents.Team
			for _, t := range m.teams {
				if t.Name == teamName {
					matchedTeam = t
					break
				}
			}
			if _, _, err := m.gateway.SpawnTeam(teamName, j.ID, spawnPrompt, matchedTeam); err != nil {
				log.Printf("failed to re-spawn team after blocker: %v", err)
			} else {
				return m, spinnerTick() // re-arm spinner for agent heartbeat
			}
		}
		return m, nil

	case RuntimeSessionStartedMsg:
		m.runtimeSessions[msg.SessionID] = &runtimeSlot{
			sessionID: msg.SessionID,
			agentName: msg.AgentName,
			jobID:     msg.JobID,
			status:    "active",
			startTime: time.Now(),
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

		if m.streaming {
			m.pendingCompletions = append(m.pendingCompletions, pendingCompletion{
				notification: notification,
			})
		} else {
			m.appendEntry(ChatEntry{
				Message:   llm.Message{Role: "user", Content: notification},
				Timestamp: time.Now(),
			})
			completionIdx := len(m.entries) - 1
			m.completionMsgIdx[completionIdx] = true
			m.selectedMsgIdx = completionIdx
			m.updateViewportContent()
			if !m.userScrolled {
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
		if !m.teamsModal.show && !m.blockerModal.show && !m.showGrid &&
			!m.showKillModal && !m.showPromptModal && !m.showOutputModal && !m.loading {
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
			m.userScrolled = false
			m.hasNewMessages = false
		} else {
			m.userScrolled = true
		}

	case scrollbarHideMsg:
		// Hide the scrollbar if enough time has passed since the last scroll event.
		if time.Since(m.lastScrollTime) >= scrollbarHideDuration {
			m.scrollbarVisible = false
		}

	case loadingTickMsg:
		if m.loading {
			m.loadingFrame = (m.loadingFrame + 1) % numLoadingFrames
			return m, loadingTick()
		}
		return m, nil

	case spinnerTickMsg:
		m.spinnerFrame++
		// Re-arm only if something is animating: operator streaming, tools in flight, or any agent running.
		needTick := m.streaming || m.toolsInFlight
		if !needTick && m.gateway != nil {
			for _, snap := range m.gateway.Slots() {
				if snap.Status == gateway.SlotRunning {
					needTick = true
					break
				}
			}
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
	}

	return m, tea.Batch(cmds...)
}
