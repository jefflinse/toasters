package tui

import (
	"encoding/json"
	"log/slog"
	"sort"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/jefflinse/toasters/internal/service"
)

const (
	inputHeight = 3

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
	outputMdRender *glamour.TermRenderer // separate renderer sized for the fullscreen cockpit overlay
	// jobsPaneMdRender renders worker output in the Jobs modal's graph
	// pane. The pane has its own width (different from the chat and the
	// fullscreen modal) and resizes with the layout, so it gets its own
	// renderer that's reissued when the configured width drifts. See
	// ensureJobsPaneMarkdownRenderer.
	jobsPaneMdRender      *glamour.TermRenderer
	jobsPaneMdRenderWidth int

	// Sub-models grouping related state.
	stream  streamingState
	grid    gridState
	prompt  promptModeState
	cockpit cockpitState

	// Blockers panel: pending ask_user requests queued for the user to answer
	// on their own schedule. blockersSel is the cursor in the panel;
	// blockersModal is the selection dialog opened with Enter.
	blockers      []service.Blocker
	blockersSel   int
	blockersModal blockersModalState

	cmdPopup cmdPopupState
	scroll   scrollState
	progress progressState
	chat     chatState

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

	// lastLeftPanelShown tracks the visibility outcome from the last
	// resizeComponents call so we can re-run the size math when the left
	// panel flips between shown/hidden due to state changes (a job or
	// worker appearing/disappearing) rather than an explicit toggle.
	// Without this the chat viewport keeps a stale width and the
	// scrollbar column drifts.
	lastLeftPanelShown bool

	// Collapsible panel state. The override pointer tracks an explicit user
	// toggle via ctrl+j: nil means "follow the configured default behavior",
	// non-nil pins the panel to the boolean's value regardless of content or
	// settings. This lets ctrl+j reveal an empty left panel even when there's
	// nothing to show — the prior plain-bool design silently lost that toggle
	// because the auto-hide gate fired first.
	leftPanelOverride *bool
	// Settings-driven default for the left panel's baseline visibility,
	// refreshed whenever /settings is loaded or saved.
	showJobsPanelDefault bool
	// fleetDensity is the settings-driven fleet-panel row density ("full" or
	// "compact"), refreshed whenever /settings is loaded or saved.
	fleetDensity           string
	leftPanelWidthOverride int // 0 = use default computed width; >0 = user-resized width

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

	// modelContext maps a model ID to its context-window length, populated from
	// the model list. The fleet pane uses it to size per-worker context bars,
	// since worker sessions carry only the model name, not its context length.
	modelContext map[string]int

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

	// Seed settings-driven defaults with the same baseline GetSettings returns
	// when no AppConfig is wired. Init() fetches the persisted settings shortly
	// after; this seeding just avoids relying on zero values until it lands.
	m.showJobsPanelDefault = false
	m.fleetDensity = "full"

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
		return m.handleKeyPress(msg)

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
		return m.handleModels(msg)

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
			m.applySettings(msg.Settings)
		}

	case SettingsSavedMsg:
		m.settingsModal.saving = false
		if msg.Err != nil {
			m.settingsModal.err = msg.Err
		} else {
			m.settingsModal.settings = msg.Settings
			m.settingsModal.dirty = msg.Settings
			m.applySettings(msg.Settings)
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
		return m.handleAppReady(msg)

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
		m.refreshCockpitAutoTail(msg.SessionID)
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

	case SessionContextMsg:
		// Live context-window occupancy for a graph-node session (their token
		// counts otherwise only reach the DB at completion). Only apply when the
		// slot exists; ordering with graph.node_started isn't guaranteed.
		if slot, ok := m.runtimeSessions[msg.SessionID]; ok {
			slot.contextTokens = msg.ContextTokens
		}
		return m, nil

	case SessionReasoningMsg:
		slot, ok := m.runtimeSessions[msg.SessionID]
		if !ok {
			return m, nil
		}
		slot.reasoning.WriteString(msg.Text)
		m.refreshCockpitAutoTail(msg.SessionID)
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
		m.refreshCockpitAutoTail(msg.SessionID)
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
		m.refreshCockpitAutoTail(msg.SessionID)
		m.updateViewportContent()
		if !m.scroll.userScrolled {
			m.chatViewport.GotoBottom()
		}
		return m, nil

	case SessionDoneMsg:
		return m.handleSessionDone(msg)

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
		return m.handleMouseClick(msg)

	case tea.MouseWheelMsg:
		return m.handleMouseWheel(msg)

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
		return m.handleSpinnerTick(msg)

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
		return m.handleMCPStatus(msg)

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
		return m.handleConnectionRestored(msg)

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
		return m.handleOperatorToolCall(msg)

	case OperatorDoneMsg:
		return m.handleOperatorDone(msg)

	case BlockerAddedMsg:
		return m.handleBlockerAdded(msg)

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
		return m.handleOperatorEvent(msg)

	case progressPollMsg:
		return m.handleProgressPoll(msg)

	case logTailTickMsg:
		return m.handleLogTailTick()

	case logContentMsg:
		m.applyLogContent(msg.lines)
		return m, nil
	}

	return m, tea.Batch(cmds...)
}
