package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
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
	Debug        bool                      // show internal system steps (decomposition/planning) normally hidden
}

// Model is the root Bubble Tea model for the toasters TUI.
type Model struct {
	width  int
	height int

	svc            service.Service
	openInEditor   func(path string) tea.Cmd
	debug          bool // show internal system steps (decomposition/planning) normally hidden
	chatViewport   viewport.Model
	input          textarea.Model
	stats          SessionStats
	err            error
	mdRender       *glamour.TermRenderer
	outputMdRender *glamour.TermRenderer // separate renderer sized for the fullscreen output modal
	// jobsPaneMdRender renders worker output in the Jobs modal's graph
	// pane. The pane has its own width (different from the chat and the
	// fullscreen modal) and resizes with the layout, so it gets its own
	// renderer that's reissued when the configured width drifts. See
	// ensureJobsPaneMarkdownRenderer.
	jobsPaneMdRender      *glamour.TermRenderer
	jobsPaneMdRenderWidth int

	// Sub-models grouping related state.
	stream      streamingState
	grid        gridState
	prompt      promptModeState
	promptModal promptModalState

	// Blockers panel: pending ask_user requests queued for the user to answer
	// on their own schedule. blockersSel is the cursor in the panel;
	// blockersModal is the selection dialog opened with Enter.
	blockers      []service.Blocker
	blockersSel   int
	blockersModal blockersModalState

	outputModal outputModalState
	cmdPopup    cmdPopupState
	scroll      scrollState
	progress    progressState
	chat        chatState

	jobs        []service.Job
	selectedJob int
	focused     focusedPanel

	systemPrompt string // assembled at startup; prepended to every LLM call

	// Skills modal state.
	skillsModal skillsModalState

	// Settings modal state (/settings).
	settingsModal settingsModalState

	// MCP modal state.
	mcpModal mcpModalState

	// Catalog modal state (models.dev browser).
	catalogModal catalogModalState

	// Operator modal state (provider picker).
	operatorModal operatorModalState

	// Jobs modal state.
	jobsModal jobsModalState

	// Presets modal state (/presets).
	presetsModal presetsModalState

	// Most recent JobResult snapshot. Drives the "↑ to select for
	// actions" hint that appears beneath the latest unread result block.
	// Cleared when the user submits another turn or a newer result
	// displaces it.
	recentJobResult *service.JobResultSnapshot

	// Graph map modal state (POC viewer for dagmap renderers).
	graphMapModal graphMapModalState

	// Per-task graph execution state — populated by GraphNodeStartedMsg /
	// GraphNodeDoneMsg. The modal reads from lastGraphTaskID.
	graphTasks      map[string]*graphTaskState
	lastGraphTaskID string

	// Cache of loaded graph definitions, keyed by id. Populated by the
	// fetchGraphs command at startup and on catalog-change events. Used to
	// resolve a task's graph_id to a dagmap topology.
	graphDefs map[string]service.GraphDefinition

	// Worker pane state.
	selectedWorkerSlot int // which slot is highlighted in the workers pane

	loading          bool // true while waiting for AppReadyMsg before initializing the conversation
	loadingFrame     int  // current animation frame index (0..numLoadingFrames-1)
	operatorDisabled bool // true when no operator provider is configured

	flashText string // transient status line; empty = hidden

	lpWidth int // cached left panel width for mouse hit-testing
	sbWidth int // cached sidebar width for mouse hit-testing

	// lastLeftPanelShown tracks the visibility outcome from the last
	// resizeComponents call so we can re-run the size math when the left
	// panel flips between shown/hidden due to state changes (a job or
	// worker appearing/disappearing) rather than an explicit toggle.
	// Without this the chat viewport keeps a stale width and the
	// scrollbar column drifts.
	lastLeftPanelShown bool

	// Collapsible panel state. Override pointers track explicit user
	// toggles via ctrl+j / ctrl+o: nil means "follow the configured default
	// behavior", non-nil pins the panel to the boolean's value regardless
	// of content or settings. This lets ctrl+j reveal an empty Jobs panel
	// even when there's nothing to show — the prior plain-bool design
	// silently lost that toggle because the auto-hide gate fired first.
	leftPanelOverride *bool
	sidebarOverride   *bool
	// Settings-driven defaults for the panels' baseline visibility,
	// refreshed whenever /settings is loaded or saved.
	showJobsPanelDefault     bool
	showOperatorPanelDefault bool
	leftPanelWidthOverride   int // 0 = use default computed width; >0 = user-resized width

	// Shared spinner animation frame counter.
	spinnerFrame   int
	spinnerRunning bool // true while the spinnerTick loop is live; prevents double-arming

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
		debug:        cfg.Debug,
		chatViewport: vp,
		input:        ta,
		stats: SessionStats{
			Connected: false,
		},
	}

	m.jobs = []service.Job{}
	m.selectedJob = 0
	m.focused = focusChat

	m.loading = true

	m.selectedWorkerSlot = 0
	m.grid.gridFocusCell = 0
	m.grid.gridCols = 1
	m.grid.gridRows = 1

	m.chat.completionMsgIdx = make(map[int]bool)
	m.chat.expandedMsgs = make(map[int]bool)
	m.chat.selectedMsgIdx = -1
	m.chat.expandedReasoning = make(map[int]bool)
	m.chat.collapsedTools = make(map[int]bool)
	m.runtimeSessions = make(map[string]*runtimeSlot)

	// Seed panel-visibility defaults with the same baseline GetSettings
	// returns when no AppConfig is wired. Init() will fetch the persisted
	// settings shortly after; until that round-trip lands, this seeding
	// keeps the operator sidebar visible (the historical default) instead
	// of starting hidden because of the bool zero value.
	m.showOperatorPanelDefault = true
	m.showJobsPanelDefault = false

	return m
}

func (m *Model) Init() tea.Cmd {
	m.spinnerRunning = true // spinnerTick() is always armed at startup
	cmds := []tea.Cmd{
		tea.RequestWindowSize,
		m.fetchModels(),
		m.fetchGraphs(),
		// Pull persisted settings up front so the panel-visibility
		// defaults reflect the user's preferences before the first
		// frame paints. The returned SettingsLoadedMsg also seeds the
		// settingsModal cache, so opening /settings later is instant.
		m.fetchSettings(),
		loadingTick(), // drive the loading screen animation
		spinnerTick(), // drive braille spinner animations
	}

	return tea.Batch(cmds...)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	defer m.syncLeftPanelVisibility()

	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// Operator modal key handling.
		if m.operatorModal.show {
			return m.updateOperatorModal(msg)
		}

		// Catalog modal key handling — intercept all keys when modal is open.
		if m.catalogModal.show {
			return m.updateCatalogModal(msg)
		}

		// MCP modal key handling — intercept all keys when modal is open.
		if m.mcpModal.show {
			return m.updateMCPModal(msg)
		}

		// Skills modal key handling — intercept all keys when modal is open.
		if m.skillsModal.show {
			return m.updateSkillsModal(msg)
		}

		// Jobs modal key handling — intercept all keys when modal is open.
		if m.jobsModal.show {
			return m.updateJobsModal(msg)
		}

		// Blockers selection modal — intercept all keys when open.
		if m.blockersModal.show {
			return m.updateBlockersModal(msg)
		}

		// Settings modal key handling — intercept all keys when modal is open.
		if m.settingsModal.show {
			return m.updateSettingsModal(msg)
		}

		// Presets modal key handling — intercept all keys when modal is open.
		if m.presetsModal.show {
			return m.updatePresetsModal(msg)
		}

		// Graph map modal key handling — intercept all keys when modal is open.
		if m.graphMapModal.show {
			return m.updateGraphMapModal(msg)
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
			// Cycle focus: chat → jobs → blockers → workers → chat.
			// Skip hidden panels.
			// (Tab inside the slash command popup is handled above and returns early.)
			next := m.focused
			for {
				switch next {
				case focusChat:
					next = focusJobs
				case focusJobs:
					next = focusBlockers
				case focusBlockers:
					next = focusWorkers
				case focusWorkers:
					next = focusChat
				default:
					next = focusChat
				}
				// Skip left-panel targets when left panel is hidden or empty.
				if !m.shouldShowLeftPanel() && (next == focusJobs || next == focusBlockers || next == focusWorkers) {
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
			// Reverse cycle: chat → workers → blockers → jobs → chat.
			next := m.focused
			for {
				switch next {
				case focusChat:
					next = focusWorkers
				case focusWorkers:
					next = focusBlockers
				case focusBlockers:
					next = focusJobs
				case focusJobs:
					next = focusChat
				default:
					next = focusChat
				}
				// Skip left-panel targets when left panel is hidden or empty.
				if !m.shouldShowLeftPanel() && (next == focusJobs || next == focusBlockers || next == focusWorkers) {
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
			// Navigate blockers when that panel is focused.
			if m.focused == focusBlockers {
				if m.blockersSel > 0 {
					m.blockersSel--
				}
				return m, nil
			}
			// Navigate worker slots when workers pane is focused.
			if m.focused == focusWorkers {
				if m.selectedWorkerSlot > 0 {
					m.selectedWorkerSlot--
				}
				return m, nil
			}
			// Chat focus + at least one JobResult → walk the result-block
			// selection backward. Blurs the input on first selection so
			// the action keys (w/d/Enter) aren't swallowed by the textarea.
			if m.focused == focusChat && !m.stream.streaming {
				if m.stepBlockSelection(-1) {
					if m.chat.selectedMsgIdx >= 0 {
						m.input.Blur()
					}
					m.updateViewportContent()
					return m, nil
				}
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
			// Navigate blockers when that panel is focused.
			if m.focused == focusBlockers {
				if m.blockersSel < len(m.blockers)-1 {
					m.blockersSel++
				}
				return m, nil
			}
			// Navigate worker slots when workers pane is focused.
			if m.focused == focusWorkers {
				if m.selectedWorkerSlot < maxGridSlots-1 {
					m.selectedWorkerSlot++
				}
				return m, nil
			}
			// Mirror of the Up handler above for symmetry: walk forward
			// through result-block selection, returning to free chat
			// after the newest result.
			if m.focused == focusChat && !m.stream.streaming {
				if m.chat.selectedMsgIdx >= 0 && m.stepBlockSelection(+1) {
					if m.chat.selectedMsgIdx < 0 {
						cmds = append(cmds, m.input.Focus())
					}
					m.updateViewportContent()
					return m, tea.Batch(cmds...)
				}
			}
		case "w":
			// Open the workspace directory of the selected JobResult.
			// The action keys are intentionally only live while a result
			// is selected — typing 'w' in chat normally goes to the input.
			if m.focused == focusChat {
				if res := m.selectedJobResult(); res != nil {
					return m, m.openWorkspaceDir(res.Workspace)
				}
			}
		case "x":
			// Dismiss the selected blocker when the Blockers panel is focused:
			// answer the waiting caller with a cancellation so it stops blocking.
			if m.focused == focusBlockers && m.blockersSel < len(m.blockers) {
				return m, m.dismissBlocker(m.blockers[m.blockersSel].RequestID)
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

		case "ctrl+j":
			// Toggle left panel (Jobs + Workers) visibility. Flip from the
			// currently *effective* state, not the override field — when no
			// override is set, the user's mental model is "the panel I see
			// right now", which may be the auto-hidden empty state.
			next := !m.shouldShowLeftPanel()
			m.leftPanelOverride = &next
			if !next && (m.focused == focusJobs || m.focused == focusWorkers) {
				cmds = append(cmds, m.setFocus(focusChat))
				cmds = append(cmds, m.input.Focus())
			}
			m.resizeComponents()
			return m, tea.Batch(cmds...)

		case "ctrl+o":
			// Toggle right sidebar (Operator stats) visibility. Same
			// effective-state semantics as ctrl+j above.
			next := !m.shouldShowSidebar()
			m.sidebarOverride = &next
			m.resizeComponents()
			return m, tea.Batch(cmds...)

		case "alt+[":
			// Decrease left panel width.
			if m.shouldShowLeftPanel() {
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
			if m.shouldShowLeftPanel() {
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
			// Drop block selection back to free chat. Sits ahead of the
			// grid + stream guards because the user's mental model is
			// "esc = back out of the most-immediate context", and a
			// chat-selected block is more recent than a streaming turn.
			if m.focused == focusChat && (m.selectedJobResult() != nil || m.selectedWorkerStream() != nil) {
				m.chat.selectedMsgIdx = -1
				cmds = append(cmds, m.input.Focus())
				m.updateViewportContent()
				return m, tea.Batch(cmds...)
			}
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
			// Block deep link: Enter on a chat-selected JobResult or
			// WorkerStream jumps into the Jobs modal at that job. Sits
			// before the jobs-pane handler so chat selection wins when
			// the user is in block-selection mode.
			if m.focused == focusChat && !m.stream.streaming {
				if res := m.selectedJobResult(); res != nil {
					return m, m.openJobsModalForJob(res.JobID)
				}
				if ws := m.selectedWorkerStream(); ws != nil {
					return m, m.openJobsModalForWorkerStream(ws)
				}
			}
			// Open jobs modal pre-selected on current job.
			if m.focused == focusJobs {
				dj := m.displayJobs()
				if len(dj) == 0 || m.selectedJob >= len(dj) {
					return m, nil
				}
				m.jobsModal = jobsModalState{
					show:   true,
					jobIdx: m.selectedJob,
				}
				m.loadJobsForModal()
				m.loadJobDetail()
				var tickCmd tea.Cmd
				if !m.spinnerRunning {
					m.spinnerRunning = true
					tickCmd = spinnerTick()
				}
				return m, tickCmd
			}
			// Open the blocker selection modal when the blockers pane is focused.
			if m.focused == focusBlockers {
				if len(m.blockers) == 0 {
					return m, nil
				}
				sel := m.blockersSel
				if sel >= len(m.blockers) {
					sel = 0
				}
				m.blockersModal = blockersModalState{show: true, sel: sel}
				return m, nil
			}
			// Open grid view when workers pane is focused.
			if m.focused == focusWorkers {
				m.grid.showGrid = true
				return m, nil
			}
			// focusOperator, focusChat: handled above or fall through to send.
			// Shift+enter inserts a newline (handled by textarea). Local
			// slash commands execute immediately even during an operator
			// turn; anything else goes to the queue while streaming.
			if strings.TrimSpace(m.input.Value()) != "" {
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
				case "/skills":
					m.input.Reset()
					m.cmdPopup.show = false
					m.skillsModal = skillsModalState{show: true}
					m.reloadSkillsForModal()
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
					var tickCmd tea.Cmd
					if !m.spinnerRunning {
						m.spinnerRunning = true
						tickCmd = spinnerTick()
					}
					return m, tickCmd
				case "/graphmap":
					m.input.Reset()
					m.cmdPopup.show = false
					m.graphMapModal = graphMapModalState{show: true}
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
				case "/operator":
					m.input.Reset()
					m.cmdPopup.show = false
					m.operatorModal = operatorModalState{show: true, loading: true}
					return m, m.fetchConfiguredProviders()
				case "/settings":
					m.input.Reset()
					m.cmdPopup.show = false
					m.settingsModal = settingsModalState{show: true, loading: true}
					return m, m.fetchSettings()
				case "/presets":
					m.input.Reset()
					m.cmdPopup.show = false
					m.presetsModal = presetsModalState{show: true}
					return m, nil
				}

				// Remaining cases send a message to the operator. If a turn
				// is already in progress, queue it for auto-send on done.
				if m.stream.streaming {
					m.chat.queuedMessages = append(m.chat.queuedMessages, text)
					m.input.Reset()
					m.cmdPopup.show = false
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
				// Not a recognized slash command — send to LLM.
				if m.operatorDisabled {
					m.cmdPopup.show = false
					return m, m.addToast("No operator — use /providers", toastWarning)
				}
				m.cmdPopup.show = false
				return m, m.sendMessage()
			}
		}

		// Delegate to textarea only when the chat pane is focused. Typing
		// is allowed even while the operator is streaming (the message
		// will be queued on Enter), but when the user has tabbed over to
		// a side pane like Jobs, keystrokes should not leak into the
		// input box.
		if m.focused == focusChat {
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
		totalPages := m.gridTotalPages(cellsPerPage)
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

	case GraphsMsg:
		if msg.Err != nil {
			slog.Warn("ListGraphs failed; graph-map topology unavailable", "error", msg.Err)
			return m, nil
		}
		if m.graphDefs == nil {
			m.graphDefs = make(map[string]service.GraphDefinition, len(msg.Graphs))
		}
		// Rebuild the cache from scratch so removals are reflected.
		m.graphDefs = make(map[string]service.GraphDefinition, len(msg.Graphs))
		for _, g := range msg.Graphs {
			m.graphDefs[g.ID] = g
		}

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
			name := m.catalogModal.configValues[fieldName]
			// Close the entire modal and show a toast.
			m.catalogModal = catalogModalState{}
			return m, m.addToast("✓ Provider '"+name+"' saved", toastSuccess)
		}

	case OperatorStatusRefreshedMsg:
		if msg.ModelName != "" {
			m.stats.ModelName = msg.ModelName
		}
		if msg.Endpoint != "" {
			m.stats.Endpoint = msg.Endpoint
		}

	case OperatorConfiguredMsg:
		m.operatorModal.loading = false
		if msg.Err != nil {
			m.operatorModal.err = msg.Err
		} else {
			m.operatorModal.providerIDs = msg.ProviderIDs
		}

	case ProviderModelsMsg:
		m.operatorModal.modelsLoading = false
		if msg.Err != nil {
			m.operatorModal.modelsErr = msg.Err
		} else {
			m.operatorModal.models = msg.Models
		}

	case OperatorProviderSetMsg:
		if msg.Err != nil {
			m.operatorModal.err = msg.Err
		} else {
			label := msg.ProviderID
			if msg.Model != "" {
				label = msg.ProviderID + "/" + msg.Model
			}
			m.operatorModal = operatorModalState{}
			m.operatorDisabled = false
			m.stats.Connected = true
			return m, tea.Batch(
				m.addToast("✓ Operator: "+label, toastSuccess),
				m.refreshOperatorStatus(),
				m.fetchModels(),
			)
		}

	case SettingsLoadedMsg:
		m.settingsModal.loading = false
		if msg.Err != nil {
			m.settingsModal.err = msg.Err
		} else {
			m.settingsModal.settings = msg.Settings
			m.settingsModal.dirty = msg.Settings
			m.applyPanelVisibilityDefaults(msg.Settings)
		}

	case SettingsSavedMsg:
		m.settingsModal.saving = false
		if msg.Err != nil {
			m.settingsModal.err = msg.Err
		} else {
			m.settingsModal.settings = msg.Settings
			m.settingsModal.dirty = msg.Settings
			m.applyPanelVisibilityDefaults(msg.Settings)
			return m, m.addToast("✓ Settings saved", toastSuccess)
		}

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
		m.operatorDisabled = msg.OperatorDisabled

		if msg.OperatorDisabled {
			// Operator is not configured — show setup message.
			m.appendEntry(service.ChatEntry{
				Message: service.ChatMessage{
					Role:    service.MessageRoleAssistant,
					Content: "No operator provider is configured. Use `/providers` to add a provider, then `/operator` to select it.",
				},
				Timestamp: time.Now(),
			})
			m.updateViewportContent()
			return m, tea.Batch(cmds...)
		}

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
		// Hydrate pending blockers so the Blockers panel reflects work that's
		// still waiting on the user across a reconnect.
		m.blockers = msg.Blockers
		if m.blockersSel >= len(m.blockers) {
			m.blockersSel = 0
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

	case SessionStartedMsg:
		m.runtimeSessions[msg.SessionID] = &runtimeSlot{
			sessionID:      msg.SessionID,
			workerName:     msg.WorkerName,
			task:           msg.Task,
			jobID:          msg.JobID,
			taskID:         msg.TaskID,
			status:         "active",
			startTime:      time.Now(),
			systemPrompt:   msg.SystemPrompt,
			initialMessage: msg.InitialMessage,
		}
		cmds = append(cmds, m.addToast("🤖 "+msg.WorkerName+" started", toastInfo))
		return m, tea.Batch(cmds...)

	case SessionPromptMsg:
		// Slot may not exist yet if event ordering races (rare). When it
		// arrives later, the slot will already have prompt fields set.
		if slot, ok := m.runtimeSessions[msg.SessionID]; ok {
			slot.systemPrompt = msg.SystemPrompt
			slot.initialMessage = msg.InitialMessage
		}
		return m, nil

	case SessionTextMsg:
		slot, ok := m.runtimeSessions[msg.SessionID]
		if !ok {
			return m, nil
		}
		slot.appendText(msg.Text)
		m.appendWorkerStreamText(slot, msg.Text)
		m.refreshOutputModalIfShowing(msg.SessionID, slot)
		m.updateViewportContent()
		if !m.scroll.userScrolled {
			m.chatViewport.GotoBottom()
		}
		return m, nil

	case SessionMetaMsg:
		// May arrive before or after the slot is created (event ordering);
		// only apply when the slot exists. Graph nodes are the primary emitter.
		if slot, ok := m.runtimeSessions[msg.SessionID]; ok {
			slot.model = msg.Model
			slot.provider = msg.Provider
			slot.temperature = msg.Temperature
			slot.hasTemp = true
			slot.thinking = msg.Thinking
		}
		return m, nil

	case SessionReasoningMsg:
		slot, ok := m.runtimeSessions[msg.SessionID]
		if !ok {
			return m, nil
		}
		slot.reasoning.WriteString(msg.Text)
		m.refreshOutputModalIfShowing(msg.SessionID, slot)
		return m, nil

	case SessionToolCallMsg:
		slot, ok := m.runtimeSessions[msg.SessionID]
		if !ok {
			return m, nil
		}
		if msg.ToolName != "" {
			slot.startTool(msg.ToolID, msg.ToolName, json.RawMessage(msg.ToolInput))
			label := activityLabel(msg.ToolName, json.RawMessage(msg.ToolInput))
			slot.activities = append(slot.activities, activityItem{label: label, toolName: msg.ToolName})
			if len(slot.activities) > 6 {
				slot.activities = slot.activities[len(slot.activities)-6:]
			}
			m.appendWorkerStreamToolCall(slot, msg.ToolID, msg.ToolName, json.RawMessage(msg.ToolInput))
		}
		m.refreshOutputModalIfShowing(msg.SessionID, slot)
		m.updateViewportContent()
		if !m.scroll.userScrolled {
			m.chatViewport.GotoBottom()
		}
		return m, nil

	case SessionToolResultMsg:
		slot, ok := m.runtimeSessions[msg.SessionID]
		if !ok {
			return m, nil
		}
		result := xansi.Strip(msg.ToolOutput)
		if len(result) > 200 {
			result = result[:200] + "..."
		}
		slot.completeTool(msg.CallID, msg.ToolName, result, msg.IsError)
		m.appendWorkerStreamToolResult(slot, msg.CallID, msg.ToolName, result, msg.IsError)
		m.refreshOutputModalIfShowing(msg.SessionID, slot)
		m.updateViewportContent()
		if !m.scroll.userScrolled {
			m.chatViewport.GotoBottom()
		}
		return m, nil

	case SessionDoneMsg:
		slot, ok := m.runtimeSessions[msg.SessionID]
		if !ok {
			return m, nil
		}
		slot.status = msg.Status
		slot.endTime = time.Now()
		m.markWorkerStreamDone(msg.SessionID)
		m.updateViewportContent()
		// Toast reflects the real terminal status rather than always "done":
		// failures and cancellations read very differently to an operator
		// watching a long autonomous run.
		switch msg.Status {
		case "failed":
			cmds = append(cmds, m.addToast("✗ "+msg.WorkerName+" failed", toastError))
		case "cancelled":
			cmds = append(cmds, m.addToast("— "+msg.WorkerName+" cancelled", toastInfo))
		default:
			cmds = append(cmds, m.addToast("🍞 "+msg.WorkerName+" finished", toastSuccess))
		}
		// Note: worker completion is no longer reported back to the operator from
		// the TUI. The server is responsible for routing task completion into the
		// operator's event channel. The TUI is a viewer, not a router.
		return m, tea.Batch(cmds...)

	case GraphNodeStartedMsg:
		// Render graph nodes as pseudo-workers so users can watch each phase
		// (investigate → plan → implement → test → review) light up in the
		// Workers panel. Graph nodes are stateless transformers, not
		// runtime.Sessions, but the panel's rendering is agnostic to that.
		m.runtimeSessions[msg.SessionID] = &runtimeSlot{
			sessionID:  msg.SessionID,
			workerName: "graph:" + msg.Node,
			jobID:      msg.JobID,
			taskID:     msg.TaskID,
			status:     "active",
			startTime:  time.Now(),
			system:     isSystemNode(msg.Node),
		}
		m.recordGraphNodeStarted(msg.JobID, msg.TaskID, msg.Node)
		return m, nil

	case GraphNodeDoneMsg:
		slot, ok := m.runtimeSessions[msg.SessionID]
		if !ok {
			m.recordGraphNodeDone(msg.JobID, msg.TaskID, msg.Node, msg.Status)
			return m, nil
		}
		// Graph nodes don't have a multi-valued status like sessions do;
		// mark them completed unconditionally. The node's semantic status
		// (tests_passed / review_rejected / …) lives on TaskState.Status
		// and drives the router, not the panel icon.
		slot.status = "completed"
		slot.endTime = time.Now()
		m.markWorkerStreamDone(msg.SessionID)
		m.recordGraphNodeDone(msg.JobID, msg.TaskID, msg.Node, msg.Status)
		return m, nil

	case GraphFailedMsg:
		// The graph.failed payload carries a reason but not which node tripped
		// it. Recover the node from recorded per-node state (recordGraphNodeDone
		// marks the failing node PhaseFailed) so the toast can name it.
		short := msg.TaskID
		if len(short) > 8 {
			short = short[:8]
		}
		line := "✗ " + short + " graph failed"
		if node := m.failedGraphNode(msg.TaskID); node != "" {
			line += " at " + node
		}
		if reason := firstLineOf(msg.Error); reason != "" {
			line += ": " + truncateStr(reason, 80)
		}
		cmds = append(cmds, m.addToast(line, toastError))
		return m, tea.Batch(cmds...)

	case tea.MouseClickMsg:
		// Click-to-focus: route clicks to the appropriate panel.
		// Don't steal clicks when any overlay is active.
		if !m.skillsModal.show &&
			!m.mcpModal.show && !m.catalogModal.show && !m.operatorModal.show &&
			!m.grid.showGrid &&
			!m.promptModal.show && !m.outputModal.show && !m.loading {
			if m.shouldShowLeftPanel() && msg.X < m.lpWidth {
				// Clicked left panel — determine which of the three panes was
				// clicked. Pane order (top to bottom): Jobs, Blockers, Workers.
				workersPaneH := m.leftPanelWorkersPaneHeight()
				blockersPaneH := m.leftPanelBlockersPaneHeight()
				workersPaneY := m.height - workersPaneH
				blockersPaneY := workersPaneY - blockersPaneH
				switch {
				case msg.Y >= workersPaneY:
					if m.focused != focusWorkers {
						cmds = append(cmds, m.setFocus(focusWorkers))
						m.input.Blur()
					}
				case msg.Y >= blockersPaneY:
					if m.focused != focusBlockers {
						cmds = append(cmds, m.setFocus(focusBlockers))
						m.input.Blur()
					}
				default:
					if m.focused != focusJobs {
						cmds = append(cmds, m.setFocus(focusJobs))
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
		// Jobs modal: route wheel events to the panel under the cursor so
		// the task list and graph list stay usable with the mouse.
		if m.jobsModal.show {
			m.scrollJobsModal(msg)
			return m, nil
		}
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
		// Re-arm as long as something is animating: operator streaming, any
		// worker running, any displayed job still active/pending, or a
		// sidebar panel whose title rainbow-cycles while focused. Animating
		// indicators should keep moving even when the pane isn't focused.
		needTick := m.stream.streaming
		if !needTick {
			for _, rs := range m.runtimeSessions {
				if rs.status == "active" {
					needTick = true
					break
				}
			}
		}
		if !needTick {
			for _, j := range m.displayJobs() {
				if j.Status == service.JobStatusActive || j.Status == service.JobStatusPending {
					needTick = true
					break
				}
			}
		}
		if !needTick && (m.focused == focusJobs || m.focused == focusWorkers) {
			needTick = true
		}
		// Jobs modal's focused panel also rainbow-cycles its title.
		if !needTick && m.jobsModal.show {
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

	case asyncToastMsg:
		return m, m.addToast(msg.message, msg.level)

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

	case DefinitionsReloadedMsg:
		slog.Info("definitions reloaded from file watcher")
		return m, nil

	case ConnectionLostMsg:
		m.stats.Connected = false
		return m, m.addToast("Server connection lost, reconnecting...", toastWarning)

	case ConnectionRestoredMsg:
		m.stats.Connected = true
		// Refetch the authoritative blocker set: blocker events that fired
		// during the outage were never delivered, so a HITL prompt raised
		// while disconnected would otherwise stay invisible (and resolved
		// ones would linger). Progress state is already resynced by the
		// client's synthetic progress.update.
		svc := m.svc
		resyncBlockers := func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			blockers, err := svc.Operator().Blockers(ctx)
			if err != nil {
				slog.Warn("failed to refetch blockers after reconnect", "error", err)
				return nil
			}
			return BlockersResyncMsg{Blockers: blockers}
		}
		// Refetch chat history too: operator text streamed during the outage
		// was never delivered, so the conversation has a hole the SSE replay
		// ring may not cover (long outages).
		resyncChat := func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			history, err := svc.Operator().History(ctx)
			if err != nil {
				slog.Warn("failed to refetch chat history after reconnect", "error", err)
				return nil
			}
			return ChatResyncMsg{History: history}
		}
		return m, tea.Batch(m.addToast("Server connection restored", toastSuccess), resyncBlockers, resyncChat)

	case BlockersResyncMsg:
		m.blockers = msg.Blockers
		if m.blockersSel >= len(m.blockers) {
			m.blockersSel = 0
		}
		return m, nil

	case ChatResyncMsg:
		// The server's persisted history is authoritative for plain chat
		// messages; locally-derived blocks (job updates/results, worker
		// streams) only exist client-side, so keep them and re-interleave by
		// timestamp.
		var local []service.ChatEntry
		for _, e := range m.chat.entries {
			if e.Kind != service.ChatEntryKindMessage {
				local = append(local, e)
			}
		}
		merged := make([]service.ChatEntry, 0, len(local)+len(msg.History))
		merged = append(merged, msg.History...)
		merged = append(merged, local...)
		sort.SliceStable(merged, func(i, j int) bool {
			return merged[i].Timestamp.Before(merged[j].Timestamp)
		})
		m.chat.entries = merged
		m.updateViewportContent()
		if !m.scroll.userScrolled {
			m.chatViewport.GotoBottom()
		}
		return m, nil

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

	case OperatorToolCallMsg:
		slog.Debug("operator tool call", "tool", msg.Name, "error", msg.IsError)
		// Commit any text streamed before this tool call so the chat stays
		// chronological: text, then the tool indicator, then any following text.
		m.flushOperatorStream()
		content := "`" + msg.Name + "`"
		if r := strings.TrimSpace(msg.Result); r != "" {
			marker := "→ "
			if msg.IsError {
				marker = "✗ "
			}
			content += "\n" + marker + r
		}
		m.appendEntry(service.ChatEntry{
			Message: service.ChatMessage{
				Role:    service.MessageRoleAssistant,
				Content: content,
			},
			Timestamp:  time.Now(),
			ClaudeMeta: "tool-call-indicator",
		})
		m.updateViewportContent()
		if !m.scroll.userScrolled {
			m.chatViewport.GotoBottom()
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
		// If the user queued messages while the operator was busy, send
		// the next one automatically.
		if len(m.chat.queuedMessages) > 0 {
			next := m.chat.queuedMessages[0]
			m.chat.queuedMessages = m.chat.queuedMessages[1:]
			m.input.SetValue(next)
			cmds = append(cmds, m.sendMessage())
		}
		return m, tea.Batch(cmds...)

	case BlockerAddedMsg:
		b := msg.Blocker
		slog.Debug("blocker added", "request_id", b.RequestID, "questions", len(b.Questions), "source", b.Source)
		// A blocker no longer prompts inline. Queue it, record it in the
		// transcript, and toast — the user answers from the Blockers panel on
		// their own schedule, so a stray Enter can't misfire a response.
		m.flushOperatorStream()
		// Ignore duplicates (e.g. a hydrate that races a live event).
		known := false
		for _, existing := range m.blockers {
			if existing.RequestID == b.RequestID {
				known = true
				break
			}
		}
		if !known {
			m.blockers = append(m.blockers, b)
			m.appendEntry(service.ChatEntry{
				Message: service.ChatMessage{
					Role:    service.MessageRoleAssistant,
					Content: "⛔ Blocker · " + m.blockerLabel(b) + " needs input:\n" + promptHistoryContent(b.Questions),
				},
				Timestamp: time.Now(),
			})
			m.updateViewportContent()
			if !m.scroll.userScrolled {
				m.chatViewport.GotoBottom()
			}
			cmds = append(cmds, m.addToast("⛔ Blocker · "+m.blockerLabel(b)+" — "+blockerFirstQuestion(b), toastWarning))
		}
		return m, tea.Batch(cmds...)

	case BlockerResolvedMsg:
		m.removeBlocker(msg.RequestID)
		// If the user happens to be answering this exact blocker when it's
		// resolved elsewhere (another client, or the node was cancelled), drop
		// out of prompt mode so they don't submit into a dead request.
		if m.prompt.promptMode && m.prompt.requestID == msg.RequestID {
			m.prompt = promptModeState{}
			m.input.Reset()
		}
		return m, nil

	case OperatorEventMsg:
		slog.Debug("operator event", "type", msg.Event.Type)
		// All job-scoped events (Job*, Task*) collapse into a single
		// in-place job-update block per job ID. The block mutates as the
		// job progresses and stays at its original chat position.
		dirty := false
		if m.upsertJobUpdateEntry(msg.Event) != nil {
			dirty = true
		}
		// Job completion is also the moment the result block lands —
		// distinct from the in-progress block, sitting *below* it in chat
		// so the conversation history reflects the discrete completion
		// event. Failed/cancelled jobs use the same hook; the renderer
		// branches on Status.
		if msg.Event.Type == service.EventTypeJobCompleted {
			if cmd := m.appendJobResultEntry(msg.Event); cmd != nil {
				cmds = append(cmds, cmd)
			}
			dirty = true
		}
		// A failed task otherwise only nudges the job block's failed-count; the
		// actual reason gets dropped. Surface it as a toast so the operator sees
		// why a node gave up without digging through transcripts.
		if msg.Event.Type == service.EventTypeTaskFailed {
			if p, ok := msg.Event.Payload.(service.TaskFailedPayload); ok {
				short := p.TaskID
				if len(short) > 8 {
					short = short[:8]
				}
				line := "✗ task " + short + " failed"
				if reason := firstLineOf(p.Error); reason != "" {
					line += ": " + truncateStr(reason, 80)
				}
				cmds = append(cmds, m.addToast(line, toastError))
			}
		}
		if dirty {
			m.updateViewportContent()
			if !m.scroll.userScrolled {
				m.chatViewport.GotoBottom()
			}
		}
		return m, tea.Batch(cmds...)

	case progressPollMsg:
		m.progress.jobs = msg.Jobs
		m.progress.tasks = msg.Tasks
		m.progress.reports = msg.Progress
		m.progress.activeSessions = msg.Sessions
		m.progress.feedEntries = msg.FeedEntries
		m.progress.mcpServers = msg.MCPServers
		// Rehydrate the Workers panel from the snapshot. Runtime slots are
		// normally created only from live session.*/graph.node_* events, which
		// the SSE stream doesn't replay — so after a reconnect mid-job the panel
		// would be empty even though work is running. Seed any active graph node
		// or live worker session we don't already have a slot for. Idempotent:
		// existing slots are skipped (and just enriched below).
		for _, gn := range msg.GraphNodes {
			if _, ok := m.runtimeSessions[gn.SessionID]; ok {
				continue
			}
			m.runtimeSessions[gn.SessionID] = &runtimeSlot{
				sessionID:  gn.SessionID,
				workerName: "graph:" + gn.Node,
				jobID:      gn.JobID,
				taskID:     gn.TaskID,
				status:     "active",
				startTime:  gn.StartedAt,
				system:     isSystemNode(gn.Node),
			}
			m.recordGraphNodeStarted(gn.JobID, gn.TaskID, gn.Node)
		}
		for _, snap := range msg.LiveSnapshots {
			if _, ok := m.runtimeSessions[snap.ID]; ok {
				continue
			}
			status := snap.Status
			if status == "" {
				status = "active"
			}
			m.runtimeSessions[snap.ID] = &runtimeSlot{
				sessionID:  snap.ID,
				workerName: snap.WorkerID,
				jobID:      snap.JobID,
				taskID:     snap.TaskID,
				status:     status,
				startTime:  snap.StartTime,
				model:      snap.Model,
				provider:   snap.Provider,
				tokensIn:   snap.TokensIn,
				tokensOut:  snap.TokensOut,
			}
		}
		// Enrich live slots with model/provider/cost the snapshot carries but
		// the session.* event stream drops, so worker cards can show them.
		for _, sess := range msg.Sessions {
			slot, ok := m.runtimeSessions[sess.ID]
			if !ok {
				continue
			}
			slot.model = sess.Model
			slot.provider = sess.Provider
			slot.tokensIn = sess.TokensIn
			slot.tokensOut = sess.TokensOut
			if sess.CostUSD != nil {
				slot.costUSD = *sess.CostUSD
			}
		}
		// Reconcile slots whose terminal events were lost during an SSE
		// outage: a slot still marked active but absent from every active
		// set in the snapshot is dead — without this it spins "streaming"
		// forever and the kill flow offers to kill a finished session.
		alive := make(map[string]bool, len(msg.GraphNodes)+len(msg.LiveSnapshots)+len(msg.Sessions))
		for _, gn := range msg.GraphNodes {
			alive[gn.SessionID] = true
		}
		for _, snap := range msg.LiveSnapshots {
			alive[snap.ID] = true
		}
		for _, sess := range msg.Sessions {
			alive[sess.ID] = true
		}
		// The age guard avoids falsely completing a session that started
		// after this snapshot was taken but before the poll was handled.
		for id, slot := range m.runtimeSessions {
			if slot.status == "active" && !alive[id] && time.Since(slot.startTime) > 5*time.Second {
				slot.status = "completed"
				if slot.endTime.IsZero() {
					slot.endTime = time.Now()
				}
			}
		}
		// Keep m.jobs in sync so the Jobs panel (which reads m.jobs via
		// displayJobs) reflects the latest polled state.
		m.jobs = msg.Jobs
		// Re-render any job-update blocks with the fresh state — the
		// discrete JobCompleted / TaskCompleted events race this update,
		// so the blocks have to catch up here to avoid stale status.
		if m.refreshJobUpdateEntries() {
			m.updateViewportContent()
		}
		m.syncJobsModalFromProgress()
		// Kick the spinner ticker if we see animated state but the tick
		// isn't running — handles TUI reconnect mid-job and any other
		// path where active state arrives without sendMessage arming it.
		if !m.spinnerRunning {
			for _, j := range m.displayJobs() {
				if j.Status == service.JobStatusActive || j.Status == service.JobStatusPending {
					m.spinnerRunning = true
					return m, spinnerTick()
				}
			}
		}
		return m, nil

	case logTailTickMsg:
		return m.handleLogTailTick()

	case logContentMsg:
		m.applyLogContent(msg.lines)
		return m, nil
	}

	return m, tea.Batch(cmds...)
}
