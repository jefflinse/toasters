package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	glamourstyles "github.com/charmbracelet/glamour/styles"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/anthropic"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/gateway"
	"github.com/jefflinse/toasters/internal/job"
	"github.com/jefflinse/toasters/internal/llm"
)

const (
	minSidebarWidth = 24
	inputHeight     = 3
	minWidthForBar  = 60

	minLeftPanelWidth    = 22
	minWidthForLeftPanel = 100
)

// Toast notification types.
type toastLevel int

const (
	toastInfo toastLevel = iota
	toastSuccess
	toastWarning
)

type toast struct {
	message   string
	level     toastLevel
	createdAt time.Time
	id        int // unique ID for dismissal
}

// dismissToastMsg is sent after a delay to remove a specific toast.
type dismissToastMsg struct{ id int }

// dismissToast returns a tea.Cmd that fires dismissToastMsg after 3 seconds.
func dismissToast(id int) tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return dismissToastMsg{id: id}
	})
}

// addToast appends a new toast notification and returns a command to auto-dismiss it.
func (m *Model) addToast(message string, level toastLevel) tea.Cmd {
	t := toast{message: message, level: level, createdAt: time.Now(), id: m.nextToastID}
	m.nextToastID++
	m.toasts = append(m.toasts, t)
	// Limit to 5 visible toasts.
	if len(m.toasts) > 5 {
		m.toasts = m.toasts[len(m.toasts)-5:]
	}
	return dismissToast(t.id)
}

// pendingCompletion holds a buffered agent-completion notification that arrived
// while the operator stream was active. It is drained after the stream ends.
type pendingCompletion struct {
	notification string // the pre-built notification message to inject
}

// focusedPanel identifies which panel currently holds keyboard focus.
type focusedPanel int

const (
	focusChat   focusedPanel = iota
	focusJobs   focusedPanel = iota
	focusTeams  focusedPanel = iota
	focusAgents focusedPanel = iota
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
	SystemPromptTokens   int // estimated token count of the system prompt
	LastResponseTime     time.Duration
	ResponseStart        time.Time
	TotalResponses       int           // number of completed responses (for avg calc)
	TotalResponseTime    time.Duration // sum of all response times (for avg calc)
}

// estimateTokens returns a rough token count for a string (~4 chars per token).
func estimateTokens(s string) int {
	n := len(s)
	if n == 0 {
		return 0
	}
	return (n + 3) / 4 // ceiling division
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

// TeamsReloadedMsg is sent by the hot-reload watcher when the teams directory changes.
type TeamsReloadedMsg struct {
	Teams     []agents.Team
	Awareness string
}

// JobsReloadedMsg is sent when the jobs directory changes on disk.
type JobsReloadedMsg struct {
	Jobs []job.Job
}

// AppReadyMsg is sent when the app has finished loading and is ready to start.
type AppReadyMsg struct {
	Awareness string
	Greeting  string // pre-fetched operator greeting; injected immediately on render
}

// loadingTickMsg drives the loading screen animation.
type loadingTickMsg struct{}

// loadingTick returns a command that fires loadingTickMsg after 150ms.
func loadingTick() tea.Cmd {
	return tea.Tick(30*time.Millisecond, func(time.Time) tea.Msg {
		return loadingTickMsg{}
	})
}

// spinnerTickMsg drives the animated braille spinners (streaming cursor + agent heartbeat).
type spinnerTickMsg struct{}

// spinnerChars are the braille frames used for animated spinners.
var spinnerChars = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// spinnerTick returns a command that fires spinnerTickMsg after 80ms.
func spinnerTick() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

// clearFlashMsg is sent after a delay to clear the transient flash status line.
type clearFlashMsg struct{}

// clearFlash returns a command that fires clearFlashMsg once after 1500ms.
func clearFlash() tea.Cmd {
	return tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg {
		return clearFlashMsg{}
	})
}

// scrollbarHideMsg is sent after a delay to hide the scrollbar.
type scrollbarHideMsg struct{}

// scrollbarHideDuration is how long the scrollbar stays visible after scrolling.
const scrollbarHideDuration = 1 * time.Second

// scrollbarHide returns a command that fires scrollbarHideMsg after the hide duration.
func scrollbarHide() tea.Cmd {
	return tea.Tick(scrollbarHideDuration, func(time.Time) tea.Msg {
		return scrollbarHideMsg{}
	})
}

// showScrollbar marks the scrollbar as visible and returns a command to hide it
// after the configured duration. Call this from every scroll-event handler.
func (m *Model) showScrollbar() tea.Cmd {
	m.scrollbarVisible = true
	m.lastScrollTime = time.Now()
	return scrollbarHide()
}

// loadingBarWidth is the number of cells in the bouncing bar track.
const loadingBarWidth = 24

// loadingBarColors are the true-color RGB values the blob cycles through as it bounces.
// Warm amber → orange → red → purple → blue → back, giving a toasty glow effect.
// Each entry is [R, G, B].
var loadingBarColors = [][3]uint8{
	{255, 175, 0},  // amber
	{255, 135, 0},  // orange
	{255, 95, 0},   // deep orange
	{255, 55, 55},  // red-orange
	{220, 50, 120}, // hot pink
	{175, 50, 200}, // purple
	{95, 80, 230},  // blue-purple
	{50, 130, 255}, // blue
	{95, 80, 230},  // blue-purple
	{175, 50, 200}, // purple
	{220, 50, 120}, // hot pink
	{255, 55, 55},  // red-orange
	{255, 95, 0},   // deep orange
	{255, 135, 0},  // orange
}

// fadeColor returns a color.Color that is the given RGB color faded toward
// black by factor (0.0 = original, 1.0 = black).
func fadeColor(r, g, b uint8, factor float64) color.Color {
	fr := uint8(float64(r) * (1.0 - factor))
	fg := uint8(float64(g) * (1.0 - factor))
	fb := uint8(float64(b) * (1.0 - factor))
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", fr, fg, fb))
}

// gradientText applies character-by-character truecolor interpolation from
// color `from` to color `to`, returning a styled string. Each visible
// character gets its own foreground color and bold styling.
func gradientText(text string, from, to [3]uint8) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	if len(runes) == 1 {
		return lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", from[0], from[1], from[2]))).
			Render(string(runes[0]))
	}
	var sb strings.Builder
	n := len(runes) - 1
	for i, r := range runes {
		t := float64(i) / float64(n)
		cr := uint8(float64(from[0])*(1-t) + float64(to[0])*t)
		cg := uint8(float64(from[1])*(1-t) + float64(to[1])*t)
		cb := uint8(float64(from[2])*(1-t) + float64(to[2])*t)
		sb.WriteString(lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", cr, cg, cb))).
			Render(string(r)))
	}
	return sb.String()
}

// numLoadingFrames is the total number of animation frames (ping-pong across the bar).
// The blob travels loadingBarWidth-1 steps right then loadingBarWidth-1 steps left = full cycle.
const numLoadingFrames = (loadingBarWidth - 1) * 2

// loadingMessages are the absurd status messages that cycle during loading.
var loadingMessages = []string{
	"heating elements...",
	"calibrating crispiness...",
	"warming up the slots...",
	"toasting your agents...",
	"achieving optimal browning...",
	"do not put metal in the toaster...",
	"this is fine 🔥",
	"preheating to 450°...",
	"sourcing artisanal bread...",
	"consulting the bread oracle...",
	"buttering the context window...",
	"negotiating with the gluten...",
	"applying light pressure...",
	"waiting for the ding...",
	"checking for even browning...",
	"deploying crumbs...",
	"establishing crust integrity...",
	"syncing with the toaster cloud...",
	"reticulating bread splines...",
	"defrosting the frozen agents...",
	"please do not unplug the toaster...",
	"warming up the second slot...",
	"the toast is a metaphor...",
	"agents are lightly golden...",
	"spreading the jam layer...",
	"calculating optimal ejection velocity...",
	"this will only take a moment (it won't)...",
	"convincing the bread to cooperate...",
	"toasting at a comfortable 72°F...",
	"loading loading loading...",
	"have you tried turning it off and on again...",
	"the crumbs are non-deterministic...",
	"invoking the sandwich protocol...",
	"agents are medium-rare...",
	"almost there (we think)...",
}

// SlotTimeoutPromptExpiredMsg fires when the 1-minute user-response window elapses.
type SlotTimeoutPromptExpiredMsg struct{ SlotID int }

// claudeMetaMsg carries model/mode info parsed from the claude CLI system/init event.
type claudeMetaMsg struct {
	Model          string
	PermissionMode string
	Version        string
	SessionID      string
}

// ToolCallMsg is emitted when the LLM requests one or more tool calls.
type ToolCallMsg struct {
	Calls []llm.ToolCall
}

// AskUserResponseMsg is dispatched when the user submits a response in prompt mode.
type AskUserResponseMsg struct {
	Call   llm.ToolCall
	Result string
}

// TeamsAutoDetectDoneMsg is sent when the LLM coordinator auto-detection finishes.
type TeamsAutoDetectDoneMsg struct {
	teamDir   string // team.Dir, used to match back
	agentName string // matched agent name; empty if no match or error
	err       error
}

// blockerAnswersSubmittedMsg is sent when the user has submitted answers for a blocker.
type blockerAnswersSubmittedMsg struct {
	jobID   string
	blocker *job.Blocker
}

// teamsModalState holds all state for the /teams modal overlay.
type teamsModalState struct {
	show              bool
	teams             []agents.Team   // local copy for the modal; separate from m.teams
	teamIdx           int             // selected team in left panel
	agentIdx          int             // selected agent in right panel (for 'c' key)
	focus             int             // 0=left panel, 1=right panel
	nameInput         string          // text being typed for new team name
	inputMode         bool            // true when typing a new team name
	confirmDelete     bool            // true when delete confirmation is showing
	autoDetectPending map[string]bool // keyed by team.Dir; prevents re-firing
	autoDetecting     bool            // true while LLM call is in flight
}

// leftPanelWidth returns the width of the left panel for the given terminal width.
func leftPanelWidth(termWidth int) int {
	w := termWidth / 4
	if w < minLeftPanelWidth {
		return minLeftPanelWidth
	}
	return w
}

// effectiveLeftPanelWidth returns the left panel width, respecting any user override.
func (m *Model) effectiveLeftPanelWidth() int {
	if m.leftPanelWidthOverride > 0 {
		w := m.leftPanelWidthOverride
		if w < minLeftPanelWidth {
			w = minLeftPanelWidth
		}
		maxW := m.width / 2
		if w > maxW {
			w = maxW
		}
		return w
	}
	return leftPanelWidth(m.width)
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

	llmClient        llm.Provider
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

	jobs         []job.Job
	blockers     map[string]*job.Blocker // keyed by job ID
	selectedJob  int
	selectedTeam int
	focused      focusedPanel

	gateway *gateway.Gateway

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

	confirmTimeout     bool        // true when promptMode is a slot-timeout confirmation
	pendingTimeoutSlot int         // slot index awaiting timeout confirmation
	timeoutPromptTimer *time.Timer // nil when no prompt active (unused; timer is via tea.Tick)

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
	completionMsgIdx map[int]bool // indices of team-completion messages in m.messages
	expandedMsgs     map[int]bool // which completion messages are currently expanded
	selectedMsgIdx   int          // currently selected message index (-1 = none)

	// Collapsible reasoning (thinking) state.
	expandedReasoning map[int]bool // which assistant message indices have reasoning expanded

	// Message timestamps — parallel to m.messages.
	timestamps []time.Time

	// Collapsible tool call/result state — keyed by message index.
	collapsedTools map[int]bool // true = expanded; absent/false = collapsed (default)

	// Shared spinner animation frame counter.
	spinnerFrame int

	// Toast notification state.
	toasts      []toast
	nextToastID int
}

// NewModel returns an initialized root model.
func NewModel(client llm.Provider, claudeCfg config.ClaudeConfig, workspaceDir string, gw *gateway.Gateway, repoRoot string, teamsDir string, teams []agents.Team, awareness string) Model {
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

// waitForAgentUpdate blocks until the gateway signals an output update.
func waitForAgentUpdate(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-ch
		return AgentOutputMsg{}
	}
}

// slotTimeoutPromptCmd fires SlotTimeoutPromptExpiredMsg after 1 minute,
// giving the user a window to respond before auto-continuing.
func slotTimeoutPromptCmd(slotID int) tea.Cmd {
	return tea.Tick(time.Minute, func(time.Time) tea.Msg {
		return SlotTimeoutPromptExpiredMsg{SlotID: slotID}
	})
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// Teams modal key handling — intercept all keys when modal is open.
		if m.teamsModal.show {
			var modalCmds []tea.Cmd

			// When typing a new team name, only esc/enter/backspace have special
			// meaning. Everything else — including named keys like "space" — feeds
			// into the name input via msg.Text (which is the actual typed character
			// for all printable input, unlike msg.String() which returns key names).
			if m.teamsModal.inputMode {
				switch msg.String() {
				case "esc":
					m.teamsModal.inputMode = false
					m.teamsModal.nameInput = ""
				case "enter":
					name := m.teamsModal.nameInput
					valid := name != "" && !strings.ContainsAny(name, `/\.`)
					if valid {
						if err := os.MkdirAll(filepath.Join(m.teamsDir, name), 0755); err != nil {
							log.Printf("teams: failed to create directory %s: %v", name, err)
						}
						m.reloadTeamsForModal()
						for i, t := range m.teamsModal.teams {
							if t.Name == name {
								m.teamsModal.teamIdx = i
								break
							}
						}
					}
					m.teamsModal.inputMode = false
					m.teamsModal.nameInput = ""
				case "backspace":
					if len(m.teamsModal.nameInput) > 0 {
						runes := []rune(m.teamsModal.nameInput)
						m.teamsModal.nameInput = string(runes[:len(runes)-1])
					}
				default:
					// msg.Text is the actual typed character(s); empty for
					// non-printable keys (arrows, function keys, etc.).
					if msg.Text != "" {
						m.teamsModal.nameInput += msg.Text
					}
				}
				return m, tea.Batch(modalCmds...)
			}

			switch msg.String() {
			case "esc":
				if m.teamsModal.confirmDelete {
					m.teamsModal.confirmDelete = false
				} else {
					m.teamsModal.show = false
				}

			case "tab":
				if !m.teamsModal.inputMode {
					if m.teamsModal.focus == 0 {
						m.teamsModal.focus = 1
					} else {
						m.teamsModal.focus = 0
					}
				}

			case "up":
				if m.teamsModal.focus == 0 {
					if m.teamsModal.teamIdx > 0 {
						m.teamsModal.teamIdx--
					}
					m.teamsModal.confirmDelete = false
					m.teamsModal.agentIdx = 0
					if len(m.teamsModal.teams) > 0 {
						modalCmds = append(modalCmds, m.maybeAutoDetectCoordinator(m.teamsModal.teams[m.teamsModal.teamIdx]))
					}
				} else {
					// Right panel: navigate agents (coordinator first, then workers).
					if len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
						team := m.teamsModal.teams[m.teamsModal.teamIdx]
						total := len(team.Workers)
						if team.Coordinator != nil {
							total++
						}
						if m.teamsModal.agentIdx > 0 {
							m.teamsModal.agentIdx--
						}
					}
				}

			case "down":
				if m.teamsModal.focus == 0 {
					if m.teamsModal.teamIdx < len(m.teamsModal.teams)-1 {
						m.teamsModal.teamIdx++
					}
					m.teamsModal.confirmDelete = false
					m.teamsModal.agentIdx = 0
					if len(m.teamsModal.teams) > 0 {
						modalCmds = append(modalCmds, m.maybeAutoDetectCoordinator(m.teamsModal.teams[m.teamsModal.teamIdx]))
					}
				} else {
					// Right panel: navigate agents (coordinator first, then workers).
					if len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
						team := m.teamsModal.teams[m.teamsModal.teamIdx]
						total := len(team.Workers)
						if team.Coordinator != nil {
							total++
						}
						if m.teamsModal.agentIdx < total-1 {
							m.teamsModal.agentIdx++
						}
					}
				}

			case "ctrl+n":
				if m.teamsModal.focus == 0 {
					// Creating a new team is never gated on the selected team's
					// read-only status — you can always create a new user-defined team.
					m.teamsModal.inputMode = true
					m.teamsModal.nameInput = ""
				}

			case "ctrl+d":
				if m.teamsModal.focus == 0 && !m.teamsModal.confirmDelete {
					if len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
						if !isReadOnlyTeam(m.teamsModal.teams[m.teamsModal.teamIdx]) {
							m.teamsModal.confirmDelete = true
						}
					}
				}

			case "enter":
				if m.teamsModal.confirmDelete {
					if len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
						team := m.teamsModal.teams[m.teamsModal.teamIdx]
						_ = os.RemoveAll(team.Dir)
					}
					m.reloadTeamsForModal()
					if m.teamsModal.teamIdx >= len(m.teamsModal.teams) && len(m.teamsModal.teams) > 0 {
						m.teamsModal.teamIdx = len(m.teamsModal.teams) - 1
					} else if len(m.teamsModal.teams) == 0 {
						m.teamsModal.teamIdx = 0
					}
					m.teamsModal.confirmDelete = false
				}

			case "ctrl+k":
				if m.teamsModal.focus == 1 && len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
					team := m.teamsModal.teams[m.teamsModal.teamIdx]
					if !isReadOnlyTeam(team) {
						// Build the ordered agent list: coordinator first, then workers.
						var agentList []agents.Agent
						if team.Coordinator != nil {
							agentList = append(agentList, *team.Coordinator)
						}
						agentList = append(agentList, team.Workers...)
						if m.teamsModal.agentIdx < len(agentList) {
							target := agentList[m.teamsModal.agentIdx]
							_ = agents.SetCoordinator(team.Dir, target.Name)
							m.reloadTeamsForModal()
						}
					}
				}

			}
			return m, tea.Batch(modalCmds...)
		}

		// Blocker modal key handling — intercept all keys when modal is open.
		if m.blockerModal.show {
			b := m.blockerModal.blocker
			if b != nil && len(b.Questions) > 0 {
				q := b.Questions[m.blockerModal.questionIdx]

				switch msg.String() {
				case "esc":
					m.blockerModal.show = false
					m.blockerModal.inputText = ""

				case "up", "k":
					if m.blockerModal.questionIdx > 0 {
						m.blockerModal.questionIdx--
						m.blockerModal.inputText = b.Questions[m.blockerModal.questionIdx].Answer
					}

				case "down", "j":
					if m.blockerModal.questionIdx < len(b.Questions)-1 {
						m.blockerModal.questionIdx++
						m.blockerModal.inputText = b.Questions[m.blockerModal.questionIdx].Answer
					}

				case "1", "2", "3", "4", "5", "6", "7", "8", "9":
					if len(q.Options) > 0 {
						idx, _ := strconv.Atoi(msg.String())
						idx-- // 0-based
						if idx >= 0 && idx < len(q.Options) {
							b.Questions[m.blockerModal.questionIdx].Answer = q.Options[idx]
							// Advance to next question if not on last.
							if m.blockerModal.questionIdx < len(b.Questions)-1 {
								m.blockerModal.questionIdx++
								m.blockerModal.inputText = b.Questions[m.blockerModal.questionIdx].Answer
							}
						}
					} else {
						// Free-form: append digit to input.
						m.blockerModal.inputText += msg.String()
					}

				case "enter":
					// Confirm free-form answer.
					if len(q.Options) == 0 && m.blockerModal.inputText != "" {
						b.Questions[m.blockerModal.questionIdx].Answer = m.blockerModal.inputText
						m.blockerModal.inputText = ""
						if m.blockerModal.questionIdx < len(b.Questions)-1 {
							m.blockerModal.questionIdx++
						}
					}

				case "backspace":
					if len(m.blockerModal.inputText) > 0 {
						runes := []rune(m.blockerModal.inputText)
						m.blockerModal.inputText = string(runes[:len(runes)-1])
					}

				case "s":
					// Submit all answers.
					return m, m.submitBlockerAnswers()

				default:
					// Free-form: append printable chars.
					if len(q.Options) == 0 && len(msg.String()) == 1 {
						m.blockerModal.inputText += msg.String()
					}
				}
			} else {
				// No questions — just allow closing.
				switch msg.String() {
				case "esc", "s":
					m.blockerModal.show = false
				}
			}
			return m, nil
		}

		// Prompt mode key handling — highest priority.
		if m.promptMode {
			allOptions := append(m.promptOptions, "Custom response...")
			switch msg.String() {
			case "up", "k":
				if m.promptSelected > 0 {
					m.promptSelected--
				}
			case "down", "j":
				if m.promptSelected < len(allOptions)-1 {
					m.promptSelected++
				}
			case "enter":
				if !m.promptCustom {
					if m.promptSelected == len(allOptions)-1 {
						// Selected "Custom response..."
						m.promptCustom = true
						m.input.Reset()
						cmds = append(cmds, m.input.Focus())
					} else {
						// Selected a pre-defined option.
						result := allOptions[m.promptSelected]
						call := m.promptPendingCall
						cmds = append(cmds, func() tea.Msg {
							return AskUserResponseMsg{Call: call, Result: result}
						})
					}
				} else {
					// Custom text submitted.
					result := strings.TrimSpace(m.input.Value())
					if result == "" {
						result = "User provided no response."
					}
					call := m.promptPendingCall
					cmds = append(cmds, func() tea.Msg {
						return AskUserResponseMsg{Call: call, Result: result}
					})
				}
			case "esc":
				if m.promptCustom {
					// Go back to option selection.
					m.promptCustom = false
					m.input.Reset()
				} else {
					// Cancel entirely.
					call := m.promptPendingCall
					cmds = append(cmds, func() tea.Msg {
						return AskUserResponseMsg{Call: call, Result: "User cancelled."}
					})
				}
			default:
				if m.promptCustom {
					// Delegate to textarea.
					var inputCmd tea.Cmd
					m.input, inputCmd = m.input.Update(msg)
					cmds = append(cmds, inputCmd)
				}
			}
			return m, tea.Batch(cmds...)
		}

		// When the prompt modal is visible, intercept all keys before any other handling.
		if m.showPromptModal {
			switch msg.String() {
			case "esc", "p", "q":
				m.showPromptModal = false
				return m, nil
			case "up", "k":
				if m.promptModalScroll > 0 {
					m.promptModalScroll--
				}
				return m, nil
			case "down", "j":
				m.promptModalScroll++
				return m, nil
			case "ctrl+u":
				m.promptModalScroll -= 10
				if m.promptModalScroll < 0 {
					m.promptModalScroll = 0
				}
				return m, nil
			case "ctrl+d":
				m.promptModalScroll += 10
				return m, nil
			}
			return m, nil
		}

		// When the output modal is visible, intercept all keys before grid navigation.
		if m.showOutputModal {
			switch msg.String() {
			case "esc", "o", "q":
				m.showOutputModal = false
			case "up", "k":
				if m.outputModalScroll > 0 {
					m.outputModalScroll--
				}
			case "down", "j":
				m.outputModalScroll++
			case "ctrl+u":
				m.outputModalScroll -= 10
				if m.outputModalScroll < 0 {
					m.outputModalScroll = 0
				}
			case "ctrl+d":
				m.outputModalScroll += 10
			}
			return m, tea.Batch(cmds...)
		}

		// When the grid screen is visible, handle navigation and dismiss it.
		if m.showGrid {
			absSlot := m.gridPage*4 + m.gridFocusCell
			switch msg.String() {
			case "ctrl+g", "esc":
				m.showGrid = false
				return m, nil
			case "k", "ctrl+k":
				if m.gateway != nil {
					_ = m.gateway.Kill(absSlot)
				}
				return m, nil
			case "enter":
				if m.gateway != nil {
					slots := m.gateway.Slots()
					snap := slots[absSlot]
					if snap.Active && snap.Output != "" {
						m.showOutputModal = true
						m.outputModalContent = snap.Output
						m.outputModalScroll = 0
					}
				}
				return m, nil
			case "p":
				if m.gateway != nil {
					slots := m.gateway.Slots()
					snap := slots[absSlot]
					if snap.Active && snap.Prompt != "" {
						m.showPromptModal = true
						m.promptModalContent = snap.Prompt
						m.promptModalScroll = 0
					}
				}
				return m, nil
			case "[":
				if m.gridPage > 0 {
					m.gridPage--
				}
				m.gridFocusCell = 0
				return m, nil
			case "]":
				if m.gridPage < 3 {
					m.gridPage++
				}
				m.gridFocusCell = 0
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
			if m.focused == focusChat && !m.streaming && m.selectedMsgIdx >= 0 && m.selectedMsgIdx < len(m.messages) {
				msg := m.messages[m.selectedMsgIdx]
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
				// Find the last assistant message index with reasoning.
				assistantIdx := 0
				lastReasoningIdx := -1
				for _, msg := range m.messages {
					if msg.Role == "assistant" {
						if assistantIdx < len(m.reasoning) && m.reasoning[assistantIdx] != "" {
							lastReasoningIdx = assistantIdx
						}
						assistantIdx++
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
				for i := len(m.messages) - 1; i >= 0; i-- {
					if m.messages[i].Role == "assistant" {
						_ = clipboard.WriteAll(m.messages[i].Content)
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
					m.timestamps = append(m.timestamps, time.Now())
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
			// Open blocker modal when jobs pane is focused and selected job has a blocker.
			if m.focused == focusJobs {
				dj := m.displayJobs()
				if len(dj) == 0 || m.selectedJob >= len(dj) {
					return m, nil
				}
				selectedJob := dj[m.selectedJob]
				if m.hasBlocker(selectedJob) {
					m.blockerModal.show = true
					m.blockerModal.jobID = selectedJob.Frontmatter.ID
					m.blockerModal.blocker = m.blockers[selectedJob.Frontmatter.ID]
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
						m.messages = append(m.messages, llm.Message{Role: "assistant", Content: "No running agents."})
						m.timestamps = append(m.timestamps, time.Now())
						m.reasoning = append(m.reasoning, "")
						m.claudeMeta = append(m.claudeMeta, "")
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
			m.messages = append(m.messages, llm.Message{
				Role:    "assistant",
				Content: m.currentResponse,
			})
			m.timestamps = append(m.timestamps, time.Now())
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
		// The LLM wants to call tools. Execute them synchronously, inject results,
		// then re-invoke the stream for the final answer.
		m.streaming = false

		// Check for kill_slot, assign_team, ask_user, or escalate_to_user — intercept before ExecuteTool.
		for _, call := range msg.Calls {
			if call.Function.Name == "kill_slot" {
				var args struct {
					SlotID int `json:"slot_id"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)

				// Look up slot info for the confirmation message.
				question := fmt.Sprintf("Kill slot %d?", args.SlotID)
				snapshots := m.gateway.Slots()
				if args.SlotID >= 0 && args.SlotID < len(snapshots) {
					snap := snapshots[args.SlotID]
					if snap.AgentName != "" {
						question = fmt.Sprintf("Kill slot %d (%s on %s)?", args.SlotID, snap.AgentName, snap.JobID)
					}
				}

				m.messages = append(m.messages, llm.Message{Role: "assistant", Content: question})
				m.timestamps = append(m.timestamps, time.Now())
				m.reasoning = append(m.reasoning, "")
				m.claudeMeta = append(m.claudeMeta, "kill-confirm")
				m.streaming = false
				m.promptMode = true
				m.confirmKill = true
				m.confirmDispatch = false
				m.pendingKillSlot = args.SlotID
				m.promptPendingCall = call
				m.promptQuestion = question
				m.promptOptions = []string{"Yes, kill", "Cancel"}
				m.promptSelected = 0
				m.promptCustom = false
				m.updateViewportContent()
				if !m.userScrolled {
					m.chatViewport.GotoBottom()
				}
				cmds = append(cmds, m.input.Focus())
				return m, tea.Batch(cmds...)
			}
			if call.Function.Name == "assign_team" {
				var args struct {
					TeamName string `json:"team_name"`
					JobID    string `json:"job_id"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)

				question := fmt.Sprintf("Assign job '%s' to team '%s'?", args.JobID, args.TeamName)
				m.messages = append(m.messages, llm.Message{Role: "assistant", Content: question})
				m.timestamps = append(m.timestamps, time.Now())
				m.reasoning = append(m.reasoning, "")
				m.claudeMeta = append(m.claudeMeta, "dispatch-confirm")
				m.streaming = false
				m.promptMode = true
				m.confirmDispatch = true
				m.changingTeam = false
				m.pendingDispatch = call
				m.promptQuestion = question
				m.promptOptions = []string{"Yes, dispatch", "Change team", "Cancel"}
				m.promptSelected = 0
				m.promptCustom = false
				m.promptPendingCall = call
				m.updateViewportContent()
				if !m.userScrolled {
					m.chatViewport.GotoBottom()
				}
				cmds = append(cmds, m.input.Focus())
				return m, tea.Batch(cmds...)
			}
			if call.Function.Name == "escalate_to_user" {
				var args struct {
					Question string `json:"question"`
					Context  string `json:"context"`
				}
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
					args.Question = "A team has encountered a blocker."
					args.Context = ""
				}
				fullQuestion := args.Question
				if args.Context != "" {
					fullQuestion = args.Question + "\n\n" + args.Context
				}
				m.messages = append(m.messages, llm.Message{
					Role:    "assistant",
					Content: fullQuestion,
				})
				m.timestamps = append(m.timestamps, time.Now())
				m.reasoning = append(m.reasoning, "")
				m.claudeMeta = append(m.claudeMeta, "escalate-prompt")
				m.streaming = false
				m.promptMode = true
				m.promptQuestion = fullQuestion
				m.promptOptions = []string{"Provide answer"}
				m.promptSelected = 0
				m.promptCustom = false
				m.promptPendingCall = call
				m.updateViewportContent()
				if !m.userScrolled {
					m.chatViewport.GotoBottom()
				}
				cmds = append(cmds, m.input.Focus())
				return m, tea.Batch(cmds...)
			}
			if call.Function.Name == "ask_user" {
				// Parse arguments.
				var args struct {
					Question string   `json:"question"`
					Options  []string `json:"options"`
				}
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
					args.Question = "What would you like to do?"
					args.Options = []string{}
				}
				// Render question into chat history as an assistant message.
				m.messages = append(m.messages, llm.Message{
					Role:    "assistant",
					Content: args.Question,
				})
				m.timestamps = append(m.timestamps, time.Now())
				m.reasoning = append(m.reasoning, "")
				m.claudeMeta = append(m.claudeMeta, "ask-user-prompt")
				// Enter prompt mode.
				m.streaming = false
				m.promptMode = true
				m.promptQuestion = args.Question
				m.promptOptions = args.Options
				m.promptSelected = 0
				m.promptCustom = false
				m.promptPendingCall = call
				m.updateViewportContent()
				if !m.userScrolled {
					m.chatViewport.GotoBottom()
				}
				cmds = append(cmds, m.input.Focus())
				return m, tea.Batch(cmds...)
			}
		}

		// Append the assistant "tool call" turn to the conversation.
		// Content is empty for tool-call-only turns; ToolCalls carries the calls.
		assistantMsg := llm.Message{
			Role:      "assistant",
			Content:   "",
			ToolCalls: msg.Calls,
		}
		m.messages = append(m.messages, assistantMsg)
		m.timestamps = append(m.timestamps, time.Now())
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
			m.timestamps = append(m.timestamps, time.Now())
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
			m.timestamps = append(m.timestamps, time.Now())
			// tool messages don't need entries in reasoning/claudeMeta
			// because updateViewportContent only increments assistantIdx for "assistant" role
		}

		// Update the viewport so the user sees the tool call indicators.
		m.updateViewportContent()
		if !m.userScrolled {
			m.chatViewport.GotoBottom()
		}

		// Drain any completion notifications that arrived while we were streaming,
		// so the operator sees them in the next context window.
		// Return values are discarded — drain mutates m.messages in place and
		// startStream below picks up the updated slice directly.
		m.drainPendingCompletions()

		// Re-invoke the stream with the updated messages for the final answer.
		return m, m.startStream(m.messages)

	case AskUserResponseMsg:
		// Handle slot-timeout confirmation flow.
		if m.confirmTimeout {
			m.confirmTimeout = false
			m.promptMode = false
			m.promptOptions = nil
			m.promptSelected = 0
			m.promptPendingCall = llm.ToolCall{}
			switch msg.Result {
			case "Continue (+15m)":
				_ = m.gateway.ExtendSlot(m.pendingTimeoutSlot)
				m.messages = append(m.messages, llm.Message{Role: "assistant", Content: fmt.Sprintf("Slot %d extended by 15m.", m.pendingTimeoutSlot)})
			default: // "Kill"
				_ = m.gateway.Kill(m.pendingTimeoutSlot)
				m.messages = append(m.messages, llm.Message{Role: "assistant", Content: fmt.Sprintf("Slot %d killed.", m.pendingTimeoutSlot)})
			}
			m.timestamps = append(m.timestamps, time.Now())
			m.reasoning = append(m.reasoning, "")
			m.claudeMeta = append(m.claudeMeta, "tool-call-indicator")
			m.updateViewportContent()
			if !m.userScrolled {
				m.chatViewport.GotoBottom()
			}
			return m, m.input.Focus()
		}

		// Handle kill confirmation flow.
		if m.confirmKill {
			m.confirmKill = false
			m.promptMode = false
			m.promptCustom = false
			m.promptOptions = nil
			m.promptSelected = 0

			var result string
			if msg.Result == "Yes, kill" {
				_ = m.gateway.Kill(m.pendingKillSlot)
				result = fmt.Sprintf("killed slot %d", m.pendingKillSlot)
			} else {
				result = "User cancelled the kill."
			}
			m.messages = append(m.messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{m.promptPendingCall}})
			m.timestamps = append(m.timestamps, time.Now())
			m.reasoning = append(m.reasoning, "")
			m.claudeMeta = append(m.claudeMeta, "tool-call-indicator")
			m.messages = append(m.messages, llm.Message{Role: "tool", Content: result, ToolCallID: m.promptPendingCall.ID})
			m.timestamps = append(m.timestamps, time.Now())
			m.updateViewportContent()
			return m, m.startStream(m.messages)
		}

		// Handle dispatch confirmation flow.
		if m.confirmDispatch {
			m.promptMode = false
			m.promptCustom = false
			m.promptOptions = nil
			m.promptSelected = 0

			if m.changingTeam {
				// Second prompt: user selected a new team name.
				m.changingTeam = false
				m.confirmDispatch = false

				// Rewrite the team_name in the pending dispatch args.
				var args map[string]any
				_ = json.Unmarshal([]byte(m.pendingDispatch.Function.Arguments), &args)
				args["team_name"] = msg.Result
				newArgs, _ := json.Marshal(args)
				m.pendingDispatch.Function.Arguments = string(newArgs)

				// Execute the modified assign_team call.
				result, err := llm.ExecuteTool(m.pendingDispatch)
				if err != nil {
					result = fmt.Sprintf("error: %v", err)
				}
				m.messages = append(m.messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{m.pendingDispatch}})
				m.timestamps = append(m.timestamps, time.Now())
				m.reasoning = append(m.reasoning, "")
				m.claudeMeta = append(m.claudeMeta, "tool-call-indicator")
				m.messages = append(m.messages, llm.Message{Role: "tool", Content: result, ToolCallID: m.pendingDispatch.ID})
				m.timestamps = append(m.timestamps, time.Now())
				m.updateViewportContent()
				return m, m.startStream(m.messages)
			}

			switch msg.Result {
			case "Yes, dispatch":
				m.confirmDispatch = false
				result, err := llm.ExecuteTool(m.pendingDispatch)
				if err != nil {
					result = fmt.Sprintf("error: %v", err)
				}
				m.messages = append(m.messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{m.pendingDispatch}})
				m.timestamps = append(m.timestamps, time.Now())
				m.reasoning = append(m.reasoning, "")
				m.claudeMeta = append(m.claudeMeta, "tool-call-indicator")
				m.messages = append(m.messages, llm.Message{Role: "tool", Content: result, ToolCallID: m.pendingDispatch.ID})
				m.timestamps = append(m.timestamps, time.Now())
				m.updateViewportContent()
				return m, m.startStream(m.messages)

			case "Change team":
				// Show second prompt with available team names.
				teamNames := make([]string, len(m.teams))
				for i, t := range m.teams {
					teamNames[i] = t.Name
				}
				m.promptMode = true
				m.confirmDispatch = true
				m.changingTeam = true
				m.promptQuestion = "Select a team:"
				m.promptOptions = teamNames
				m.promptSelected = 0
				m.promptPendingCall = m.pendingDispatch
				m.updateViewportContent()
				return m, m.input.Focus()

			default: // "Cancel" or anything else
				m.confirmDispatch = false
				m.messages = append(m.messages, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{m.pendingDispatch}})
				m.timestamps = append(m.timestamps, time.Now())
				m.reasoning = append(m.reasoning, "")
				m.claudeMeta = append(m.claudeMeta, "tool-call-indicator")
				m.messages = append(m.messages, llm.Message{Role: "tool", Content: "User cancelled the dispatch.", ToolCallID: m.pendingDispatch.ID})
				m.timestamps = append(m.timestamps, time.Now())
				m.updateViewportContent()
				return m, m.startStream(m.messages)
			}
		}

		// Clear prompt mode.
		m.promptMode = false
		m.promptCustom = false
		m.promptQuestion = ""
		m.promptOptions = nil
		m.promptSelected = 0
		m.input.Reset()

		// Inject the tool call + result into message history.
		// First: the assistant turn with the tool call.
		m.messages = append(m.messages, llm.Message{
			Role:      "assistant",
			ToolCalls: []llm.ToolCall{msg.Call},
		})
		m.timestamps = append(m.timestamps, time.Now())
		m.reasoning = append(m.reasoning, "")
		m.claudeMeta = append(m.claudeMeta, "tool-call-indicator")
		// Then: the tool result.
		m.messages = append(m.messages, llm.Message{
			Role:       "tool",
			Content:    msg.Result,
			ToolCallID: msg.Call.ID,
		})
		m.timestamps = append(m.timestamps, time.Now())
		m.updateViewportContent()
		if !m.userScrolled {
			m.chatViewport.GotoBottom()
		}
		// Resume the stream.
		return m, m.startStream(m.messages)

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
			m.timestamps = append(m.timestamps, time.Now())
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
		llm.SetTeams(m.teams)
		if m.hasConversation() {
			m.messages[0].Content = m.systemPrompt
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
			if _, exists := m.blockers[j.Frontmatter.ID]; !exists {
				if b, err := job.ReadBlocker(j.Dir); err == nil && b != nil {
					m.blockers[j.Frontmatter.ID] = b
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
			m.messages = append(m.messages, llm.Message{Role: "assistant", Content: msg.Greeting})
			m.timestamps = append(m.timestamps, time.Now())
			m.reasoning = append(m.reasoning, "")
			m.claudeMeta = append(m.claudeMeta, "")
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
		m.messages = append(m.messages, llm.Message{Role: "assistant", Content: promptText})
		m.timestamps = append(m.timestamps, time.Now())
		m.reasoning = append(m.reasoning, "")
		m.claudeMeta = append(m.claudeMeta, "ask-user-prompt")
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
		m.messages = append(m.messages, llm.Message{Role: "assistant", Content: fmt.Sprintf("Slot %d auto-continued (no response within 1m).", msg.SlotID)})
		m.timestamps = append(m.timestamps, time.Now())
		m.reasoning = append(m.reasoning, "")
		m.claudeMeta = append(m.claudeMeta, "tool-call-indicator")
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
			if _, _, err := m.gateway.SpawnTeam(teamName, j.Frontmatter.ID, spawnPrompt, matchedTeam); err != nil {
				log.Printf("failed to re-spawn team after blocker: %v", err)
			} else {
				return m, spinnerTick() // re-arm spinner for agent heartbeat
			}
		}
		return m, nil

	case AgentOutputMsg:
		// Re-arm the poller.
		if m.agentNotifyCh != nil {
			cmds = append(cmds, waitForAgentUpdate(m.agentNotifyCh))
		}

		if m.gateway != nil {
			slots := m.gateway.Slots()

			// Detect Running→Done transitions and notify the operator LLM.
			for i, snap := range slots {
				wasRunning := m.prevSlotActive[i] && m.prevSlotStatus[i] == gateway.SlotRunning
				isDone := snap.Active && snap.Status == gateway.SlotDone
				if wasRunning && isDone {
					// Build a concise completion notification for the operator.
					outputTail := snap.Output
					const maxTail = 2000
					if len(outputTail) > maxTail {
						outputTail = "…" + outputTail[len(outputTail)-maxTail:]
					}
					var notification string
					if snap.ExitSummary != "" {
						notification = fmt.Sprintf(
							"Team '%s' in slot %d has completed (job: %s).\n\nExit Summary:\n%s\n\nOutput (last 2000 chars):\n%s",
							snap.AgentName, i, snap.JobID, snap.ExitSummary, outputTail,
						)
					} else {
						notification = fmt.Sprintf(
							"Team '%s' in slot %d has completed (job: %s).\n\nOutput (last 2000 chars):\n%s",
							snap.AgentName, i, snap.JobID, outputTail,
						)
					}

					// Toast: agent completed.
					cmds = append(cmds, m.addToast("🍞 "+snap.AgentName+" is done. Extra crispy.", toastSuccess))

					if m.streaming {
						// Buffer the notification — drain it after the current stream ends.
						m.pendingCompletions = append(m.pendingCompletions, pendingCompletion{
							notification: notification,
						})
					} else {
						// Inject immediately and start a new stream.
						m.messages = append(m.messages, llm.Message{
							Role:    "user",
							Content: notification,
						})
						m.timestamps = append(m.timestamps, time.Now())
						// Tag this message as a collapsible completion entry and auto-select it.
						completionIdx := len(m.messages) - 1
						m.completionMsgIdx[completionIdx] = true
						m.selectedMsgIdx = completionIdx
						m.updateViewportContent()
						if !m.userScrolled {
							m.chatViewport.GotoBottom()
						}
						cmds = append(cmds, m.startStream(m.messages))
					}

					// Check for BLOCKER.md and mark first task done — always, not buffered.
					for _, j := range m.jobs {
						if j.Frontmatter.ID == snap.JobID {
							if b, err := job.ReadBlocker(j.Dir); err == nil && b != nil {
								if _, alreadyKnown := m.blockers[j.Frontmatter.ID]; !alreadyKnown {
									cmds = append(cmds, m.addToast("⚠ Blocker on "+j.Frontmatter.ID, toastWarning))
								}
								m.blockers[j.Frontmatter.ID] = b
							}
							// Mark the first task done only on a clean completion.
							if !snap.Killed && snap.ExitSummary != "" {
								if tasks, err := job.ListTasks(j.Dir); err == nil && len(tasks) > 0 {
									if err := job.SetTaskStatus(tasks[0].Dir, job.StatusDone); err != nil {
										log.Printf("failed to mark task done: %v", err)
									}
								}
							} else {
								log.Printf("slot %d completed without clean exit (killed=%v, exitSummary=%q), skipping task auto-mark", i, snap.Killed, snap.ExitSummary)
							}
							break
						}
					}
				}
				// Update tracked state.
				m.prevSlotActive[i] = snap.Active
				m.prevSlotStatus[i] = snap.Status
			}

			// If attached to a slot, update the agent viewport.
			if m.attachedSlot >= 0 {
				snap := slots[m.attachedSlot]
				if snap.Active {
					m.agentViewport.SetContent(m.renderMarkdown(snap.Output))
					m.agentViewport.GotoBottom()
				}
			}
		}
		return m, tea.Batch(cmds...)

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
		// Re-arm only if something is animating: operator streaming or any agent running.
		needTick := m.streaming
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

// renderScrollbar returns a single-column string of scrollbar characters (one per line)
// to be placed alongside the viewport content via lipgloss.JoinHorizontal.
// scrollPercent is 0.0 (top) to 1.0 (bottom).
func renderScrollbar(viewportHeight int, totalLines int, scrollPercent float64) string {
	// Calculate thumb size: proportional to visible/total, minimum 2 lines.
	thumbSize := viewportHeight * viewportHeight / totalLines
	if thumbSize < 2 {
		thumbSize = 2
	}
	if thumbSize > viewportHeight {
		thumbSize = viewportHeight
	}

	// Calculate thumb position.
	trackSpace := viewportHeight - thumbSize
	thumbStart := int(scrollPercent * float64(trackSpace))
	if thumbStart < 0 {
		thumbStart = 0
	}
	if thumbStart > trackSpace {
		thumbStart = trackSpace
	}
	thumbEnd := thumbStart + thumbSize

	thumbStyle := lipgloss.NewStyle().Foreground(ColorPrimary)
	trackStyle := lipgloss.NewStyle().Foreground(ColorBorder)

	lines := make([]string, viewportHeight)
	for i := 0; i < viewportHeight; i++ {
		if i >= thumbStart && i < thumbEnd {
			lines[i] = thumbStyle.Render("█")
		} else {
			lines[i] = trackStyle.Render("░")
		}
	}
	return strings.Join(lines, "\n")
}

// renderToasts renders the toast notification stack as a single string block.
// Newest toasts appear at the top.
func (m *Model) renderToasts() string {
	if len(m.toasts) == 0 {
		return ""
	}
	var lines []string
	// Render newest first (reverse order).
	for i := len(m.toasts) - 1; i >= 0; i-- {
		t := m.toasts[i]
		msg := t.message
		// Truncate message to fit within toast max width (inner ~36 chars after padding).
		maxMsg := 36
		if len([]rune(msg)) > maxMsg {
			msg = string([]rune(msg)[:maxMsg-1]) + "…"
		}
		var rendered string
		switch t.level {
		case toastSuccess:
			rendered = ToastSuccessStyle.Render(msg)
		case toastWarning:
			rendered = ToastWarningStyle.Render(msg)
		default:
			rendered = ToastInfoStyle.Render(msg)
		}
		lines = append(lines, rendered)
	}
	return strings.Join(lines, "\n")
}

// overlayToasts splices the toast block into the top-right corner of the screen.
func overlayToasts(screen string, toastBlock string, screenWidth int) string {
	if toastBlock == "" {
		return screen
	}
	screenLines := strings.Split(screen, "\n")
	toastLines := strings.Split(toastBlock, "\n")

	for i, tl := range toastLines {
		if i >= len(screenLines) {
			break
		}
		toastW := lipgloss.Width(tl)
		screenLineW := lipgloss.Width(screenLines[i])

		if toastW >= screenWidth {
			// Toast is wider than screen — just replace the line.
			screenLines[i] = tl
			continue
		}

		// Pad the screen line if it's shorter than the screen width.
		if screenLineW < screenWidth {
			screenLines[i] = screenLines[i] + strings.Repeat(" ", screenWidth-screenLineW)
		}

		// Right-align: place toast at the right edge.
		// We need to work with the raw string, but ANSI codes make character
		// counting tricky. Use a simpler approach: build the line as
		// [left portion] + [toast].
		leftWidth := screenWidth - toastW
		if leftWidth < 0 {
			leftWidth = 0
		}

		// Truncate the screen line to leftWidth visible characters.
		// Walk runes and ANSI sequences to find the cut point.
		sl := screenLines[i]
		var result strings.Builder
		visible := 0
		inEsc := false
		for _, r := range sl {
			if r == '\x1b' {
				inEsc = true
				result.WriteRune(r)
				continue
			}
			if inEsc {
				result.WriteRune(r)
				if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
					inEsc = false
				}
				continue
			}
			if visible >= leftWidth {
				break
			}
			result.WriteRune(r)
			visible++
		}
		// Pad if we didn't reach leftWidth.
		for visible < leftWidth {
			result.WriteRune(' ')
			visible++
		}
		result.WriteString(tl)
		screenLines[i] = result.String()
	}
	return strings.Join(screenLines, "\n")
}

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

	// Teams modal takes over the full terminal as a centered overlay.
	if m.teamsModal.show {
		teamsView := m.renderTeamsModal()
		v := tea.NewView(teamsView)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// Blocker modal takes over the full terminal as a centered overlay.
	if m.blockerModal.show {
		blockerView := m.renderBlockerModal()
		v := tea.NewView(blockerView)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	// Grid screen takes over the full terminal.
	if m.showGrid {
		gridView := m.renderGrid()
		if m.showPromptModal {
			// Render prompt modal as a centered overlay.
			modalW := m.width * 3 / 4
			modalH := m.height * 3 / 4
			if modalW < 40 {
				modalW = 40
			}
			if modalH < 10 {
				modalH = 10
			}

			// Slice the prompt into lines, apply scroll offset.
			allLines := strings.Split(m.promptModalContent, "\n")
			maxScroll := len(allLines) - modalH + 4
			if maxScroll < 0 {
				maxScroll = 0
			}
			if m.promptModalScroll > maxScroll {
				m.promptModalScroll = maxScroll
			}

			start := m.promptModalScroll
			end := start + modalH - 4 // -4 for title + footer + borders
			if end > len(allLines) {
				end = len(allLines)
			}
			visibleLines := allLines[start:end]

			// Truncate each line to modal inner width.
			innerW := modalW - 4
			truncated := make([]string, len(visibleLines))
			for i, l := range visibleLines {
				if len(l) > innerW {
					truncated[i] = l[:innerW]
				} else {
					truncated[i] = l
				}
			}

			body := strings.Join(truncated, "\n")
			scrollInfo := fmt.Sprintf("line %d/%d", m.promptModalScroll+1, len(allLines))
			footer := DimStyle.Render("↑↓ scroll · Esc to close · " + scrollInfo)

			modalContent := HeaderStyle.Render("Prompt") + "\n\n" + body + "\n\n" + footer

			modalStyle := lipgloss.NewStyle().
				Width(modalW).
				Height(modalH).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorPrimary).
				Padding(0, 2)

			modal := modalStyle.Render(modalContent)

			// Place modal centered over the grid using lipgloss.Place.
			// WithWhitespaceStyle sets the background of the surrounding area.
			overlaid := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal,
				lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))))

			v := tea.NewView(overlaid)
			v.AltScreen = true
			v.MouseMode = tea.MouseModeCellMotion
			return v
		} else if m.showOutputModal {
			// Render output modal as a centered overlay.
			modalW := m.width * 3 / 4
			modalH := m.height * 3 / 4
			if modalW < 40 {
				modalW = 40
			}
			if modalH < 10 {
				modalH = 10
			}

			// Slice the output into lines, apply scroll offset.
			allLines := strings.Split(m.outputModalContent, "\n")
			maxScroll := len(allLines) - modalH + 4
			if maxScroll < 0 {
				maxScroll = 0
			}
			if m.outputModalScroll > maxScroll {
				m.outputModalScroll = maxScroll
			}

			start := m.outputModalScroll
			end := start + modalH - 4 // -4 for title + footer + borders
			if end > len(allLines) {
				end = len(allLines)
			}
			visibleLines := allLines[start:end]

			// Truncate each line to modal inner width.
			innerW := modalW - 4
			truncated := make([]string, len(visibleLines))
			for i, l := range visibleLines {
				if len(l) > innerW {
					truncated[i] = l[:innerW]
				} else {
					truncated[i] = l
				}
			}

			body := strings.Join(truncated, "\n")
			scrollInfo := fmt.Sprintf("line %d/%d", m.outputModalScroll+1, len(allLines))
			footer := DimStyle.Render("↑↓ scroll · Esc to close · " + scrollInfo)

			modalContent := HeaderStyle.Render("Output") + "\n\n" + body + "\n\n" + footer

			modalStyle := lipgloss.NewStyle().
				Width(modalW).
				Height(modalH).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorPrimary).
				Padding(0, 2)

			modal := modalStyle.Render(modalContent)

			// Place modal centered over the grid using lipgloss.Place.
			overlaid := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal,
				lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))))

			v := tea.NewView(overlaid)
			v.AltScreen = true
			v.MouseMode = tea.MouseModeCellMotion
			return v
		}
		v := tea.NewView(gridView)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	showSidebar := m.width >= minWidthForBar && !m.sidebarHidden
	showLeftPanel := m.width >= minWidthForLeftPanel && !m.leftPanelHidden

	sbWidth := sidebarWidth(m.width)
	lpWidth := m.effectiveLeftPanelWidth()

	const columnGap = 1 // consistent gap between adjacent columns

	var mainWidth int
	if showSidebar && showLeftPanel {
		mainWidth = m.width - lpWidth - sbWidth - 2*columnGap
	} else if showSidebar {
		mainWidth = m.width - sbWidth - columnGap
	} else if showLeftPanel {
		mainWidth = m.width - lpWidth - columnGap
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

	// Determine chat content and input area — swapped when attached to an agent slot.
	var chatContent string
	var inputOrStatus string
	if m.attachedSlot >= 0 && m.gateway != nil {
		slots := m.gateway.Slots()
		snap := slots[m.attachedSlot]
		header := fmt.Sprintf("⬡ %s · %s", snap.AgentName, snap.JobID)
		if snap.Status == gateway.SlotDone {
			header += " [done]"
		} else {
			header += " [running]"
		}
		chatContent = m.agentViewport.View()
		inputArea := inputStyle.Width(mainWidth).Render(
			DimStyle.Render(header + "  ·  Esc to detach · d to dismiss"),
		)
		if flashLine != "" {
			inputOrStatus = lipgloss.JoinVertical(lipgloss.Left, flashLine, inputArea)
		} else {
			inputOrStatus = inputArea
		}
	} else {
		chatContent = m.chatViewport.View()

		// Render scrollbar column alongside the chat content.
		// Always reserve the column to prevent layout shifts, but only draw
		// the thumb/track when the user has recently scrolled.
		if m.chatViewport.TotalLineCount() > m.chatViewport.Height() {
			var scrollCol string
			if m.scrollbarVisible {
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
		if m.hasNewMessages && m.userScrolled {
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

		var inputArea string
		if m.promptMode {
			inputArea = m.renderPromptWidget(mainWidth, inputStyle)
		} else {
			inputArea = inputStyle.Width(mainWidth).Render(m.input.View())
		}
		if flashLine != "" {
			inputOrStatus = lipgloss.JoinVertical(lipgloss.Left, flashLine, inputArea)
		} else {
			inputOrStatus = inputArea
		}
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
			label := fmt.Sprintf("[%d] %s · %s", slotIdx, snap.AgentName, snap.JobID)
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

	// Trim chatContent when in prompt option-selection mode to prevent overflow.
	// The prompt widget is taller than the normal input area; subtract the extra lines.
	if m.promptMode && !m.promptCustom {
		allOpts := append(m.promptOptions, "Custom response...")
		// Widget inner content: 1 question + 1 blank + N options + 1 blank + 1 hint = N+4 lines.
		// InputAreaStyle border adds 2 vertical lines. Normal input = inputHeight(3) + 2 = 5 lines.
		promptWidgetHeight := len(allOpts) + 4 + 2
		extraLines := promptWidgetHeight - (inputHeight + 2)
		if extraLines > 0 {
			lines := strings.Split(chatContent, "\n")
			trimTo := len(lines) - extraLines
			if trimTo < 0 {
				trimTo = 0
			}
			chatContent = strings.Join(lines[:trimTo], "\n")
		}
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

	// Build a vertical gap spacer (1-column wide, full terminal height) for
	// consistent spacing between adjacent columns. Each line must contain a
	// space character so JoinHorizontal measures it as 1 column wide.
	gapLines := make([]string, m.height)
	for i := range gapLines {
		gapLines[i] = " "
	}
	gap := strings.Join(gapLines, "\n")

	var content string
	if showLeftPanel && showSidebar {
		sidebar := m.renderSidebar(sbWidth)
		content = lipgloss.JoinHorizontal(lipgloss.Top, leftPanelView, gap, mainColumn, gap, sidebar)
	} else if showLeftPanel {
		content = lipgloss.JoinHorizontal(lipgloss.Top, leftPanelView, gap, mainColumn)
	} else if showSidebar {
		sidebar := m.renderSidebar(sbWidth)
		content = lipgloss.JoinHorizontal(lipgloss.Top, mainColumn, gap, sidebar)
	} else {
		content = mainColumn
	}

	// Overlay toast notifications in the top-right corner.
	if len(m.toasts) > 0 {
		toastBlock := m.renderToasts()
		content = overlayToasts(content, toastBlock, m.width)
	}

	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// renderLoading renders a centered animated loading screen while the app is initializing.
func (m *Model) renderLoading() tea.View {
	msgStyle := DimStyle.Italic(true)

	// Compute blob position: ping-pong across the bar.
	frame := m.loadingFrame % numLoadingFrames
	var blobPos int
	if frame < loadingBarWidth-1 {
		blobPos = frame
	} else {
		blobPos = numLoadingFrames - frame
	}

	// Pick blob color from the palette, cycling with the frame.
	rgb := loadingBarColors[m.loadingFrame%len(loadingBarColors)]
	blobColor := fadeColor(rgb[0], rgb[1], rgb[2], 0.0)

	// Determine direction: moving right when frame < loadingBarWidth-1, left otherwise.
	movingRight := frame < loadingBarWidth-1

	// Trail: 3 cells behind the blob, each progressively faded (25%, 55%, 80% toward black).
	trailFade := [3]float64{0.35, 0.62, 0.82}
	trailPos := [3]int{-1, -1, -1}
	for d := 0; d < 3; d++ {
		var p int
		if movingRight {
			p = blobPos - (d + 1)
		} else {
			p = blobPos + (d + 1)
		}
		if p >= 0 && p < loadingBarWidth {
			trailPos[d] = p
		}
	}

	// Build the bar cell by cell so each position can be styled independently.
	trackStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	blobStyle := lipgloss.NewStyle().Foreground(blobColor).Bold(true)

	var barParts []string
	for i := 0; i < loadingBarWidth; i++ {
		ch := "-"
		if i == blobPos {
			barParts = append(barParts, blobStyle.Render("O"))
			continue
		}
		isTrail := false
		for d, tp := range trailPos {
			if tp == i {
				tc := fadeColor(rgb[0], rgb[1], rgb[2], trailFade[d])
				trailStyle := lipgloss.NewStyle().Foreground(tc)
				barParts = append(barParts, trailStyle.Render(ch))
				isTrail = true
				break
			}
		}
		if !isTrail {
			barParts = append(barParts, trackStyle.Render(ch))
		}
	}

	barStr := strings.Join(barParts, "")

	// Cycle the status message every 24 frames (~720ms at 30ms/frame).
	msgIdx := (m.loadingFrame / 24) % len(loadingMessages)
	statusMsg := msgStyle.Render(loadingMessages[msgIdx])

	// Place each element independently at the center of the screen,
	// stacked vertically. Avoids JoinVertical width-measurement issues
	// with multi-column emoji.
	barLine := lipgloss.Place(m.width, 1, lipgloss.Center, lipgloss.Center, barStr)
	breadLine := lipgloss.Place(m.width, 1, lipgloss.Center, lipgloss.Center, "🍞")
	msgLine := lipgloss.Place(m.width, 1, lipgloss.Center, lipgloss.Center, statusMsg)

	content := lipgloss.JoinVertical(lipgloss.Left,
		strings.Repeat("\n", m.height/2-2),
		barLine,
		breadLine,
		"",
		msgLine,
	)

	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// indentLines prepends each line of s with n spaces.
func indentLines(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n")
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

// toastersStyle returns a Glamour style config based on Dracula with
// code block colors adjusted to match the toasters dark palette.
func toastersStyle() ansi.StyleConfig {
	s := glamourstyles.DraculaStyleConfig

	// Tighten document margin — the chat area already provides padding.
	zero := uint(0)
	s.Document.Margin = &zero

	// Darken code block background to blend with the toasters dark chrome.
	bg := "#1e1e2e"
	s.CodeBlock.Chroma.Background = ansi.StylePrimitive{
		BackgroundColor: &bg,
	}

	return s
}

// ensureMarkdownRenderer creates or recreates the glamour renderer for the current width.
func (m *Model) ensureMarkdownRenderer() {
	w := m.chatViewport.Width() - AssistantMsgIndent
	if w < 1 {
		w = 80
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(toastersStyle()),
		glamour.WithWordWrap(w),
	)
	if err == nil {
		m.mdRender = r
	}
}

// resizeComponents recalculates sizes for viewport and textarea after a resize.
func (m *Model) resizeComponents() {
	showSidebar := m.width >= minWidthForBar && !m.sidebarHidden
	showLeftPanel := m.width >= minWidthForLeftPanel && !m.leftPanelHidden

	sbWidth := sidebarWidth(m.width)
	lpWidth := m.effectiveLeftPanelWidth()

	// Cache for mouse hit-testing.
	m.lpWidth = lpWidth
	m.sbWidth = sbWidth

	const columnGap = 1 // consistent gap between adjacent columns

	var mainWidth int
	if showSidebar && showLeftPanel {
		mainWidth = m.width - lpWidth - sbWidth - 2*columnGap
	} else if showSidebar {
		mainWidth = m.width - sbWidth - columnGap
	} else if showLeftPanel {
		mainWidth = m.width - lpWidth - columnGap
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

	// Agent viewport mirrors chat viewport dimensions.
	m.agentViewport.SetWidth(vpWidth)
	m.agentViewport.SetHeight(vpHeight)

	m.input.SetWidth(mainWidth - InputAreaStyle.GetHorizontalFrameSize())
	m.input.SetHeight(inputHeight)

	m.ensureMarkdownRenderer()
	m.updateViewportContent()
}

// renderPromptWidget renders the prompt mode input area, replacing the normal textarea.
// In option-selection mode (promptCustom == false) it shows a numbered list of choices.
// In custom-text mode (promptCustom == true) it shows the question above the textarea.
// style is the InputAreaStyle variant to use (may have dimmed borders when unfocused).
func (m Model) renderPromptWidget(width int, style lipgloss.Style) string {
	if m.promptCustom {
		// Custom text mode: question header above the normal textarea.
		question := HeaderStyle.Render("? " + m.promptQuestion)
		hint := DimStyle.Render("Enter to submit · Esc to go back")
		inner := lipgloss.JoinVertical(lipgloss.Left, question, m.input.View(), hint)
		return style.Width(width).Render(inner)
	}

	// Option selection mode: numbered list with cursor.
	allOptions := append(m.promptOptions, "Custom response...")

	var rows []string
	for i, opt := range allOptions {
		label := fmt.Sprintf("%d. %s", i+1, opt)
		if i == m.promptSelected {
			rows = append(rows, CmdPopupSelectedStyle.Render("▶ "+label))
		} else {
			rows = append(rows, DimStyle.Render("  "+label))
		}
	}

	question := HeaderStyle.Render("? " + m.promptQuestion)
	optionList := lipgloss.JoinVertical(lipgloss.Left, rows...)
	hint := DimStyle.Render("↑↓ navigate · Enter select · Esc cancel")

	inner := lipgloss.JoinVertical(lipgloss.Left,
		question,
		"",
		optionList,
		"",
		hint,
	)
	return style.Width(width).Render(inner)
}

// updateViewportContent rebuilds the chat history string and sets it on the viewport.
func (m *Model) updateViewportContent() {
	var sb strings.Builder
	contentWidth := m.chatViewport.Width()
	if contentWidth < 1 {
		contentWidth = 40
	}

	// Show welcome message when there's no conversation yet.
	if !m.hasConversation() && !m.streaming {
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
		// Render the art as a single block with color but no per-line padding,
		// so lipgloss.Place can measure and center it correctly as a unit.
		artStyled := lipgloss.NewStyle().Foreground(ColorPrimary).Render(toasterArt)
		tagline := DimStyle.Render("Your personal army of toasters to ") + lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render("get shit done.")
		endpoint := DimStyle.Render("Operator connected to " + m.stats.Endpoint)
		hints := DimStyle.Render("Esc to cancel a response · Ctrl+C to quit.")
		block := lipgloss.JoinVertical(lipgloss.Center, artStyled, "", tagline, endpoint, "", hints)

		vpH := m.chatViewport.Height()
		if vpH < 1 {
			vpH = 24
		}
		// Count how many assistant messages (e.g. greeting) will render below.
		hasGreeting := false
		for _, msg := range m.messages {
			if msg.Role == "assistant" && msg.Content != "" {
				hasGreeting = true
				break
			}
		}
		if hasGreeting {
			// When a greeting follows, center the art horizontally but only
			// use the space it needs so the greeting is visible below.
			blockLines := strings.Count(block, "\n") + 1
			topPad := (vpH - blockLines) / 3 // bias toward upper third
			if topPad < 1 {
				topPad = 1
			}
			sb.WriteString(strings.Repeat("\n", topPad))
			for _, line := range strings.Split(block, "\n") {
				sb.WriteString(lipgloss.PlaceHorizontal(contentWidth, lipgloss.Center, line) + "\n")
			}
			sb.WriteString("\n")
		} else {
			welcome := lipgloss.Place(contentWidth, vpH, lipgloss.Center, lipgloss.Center, block)
			sb.WriteString(welcome)
		}
	}

	assistantIdx := 0
	for i, msg := range m.messages {
		// Timestamp helper — safe even if timestamps slice is short.
		var ts string
		if i < len(m.timestamps) && !m.timestamps[i].IsZero() {
			ts = " · " + m.timestamps[i].Format("3:04 PM")
		}

		switch msg.Role {
		case "user":
			// Completion messages render as collapsible blocks.
			if m.completionMsgIdx[i] {
				firstLine := firstLineOf(msg.Content)
				if m.expandedMsgs[i] {
					hint := ""
					if i == m.selectedMsgIdx {
						hint = DimStyle.Render(" [ctrl+x to collapse]")
					}
					header := DimStyle.Render("▼ "+firstLine) + hint
					sb.WriteString(header + "\n" + renderCompletionBlock(msg.Content) + "\n")
				} else {
					hint := ""
					if i == m.selectedMsgIdx {
						hint = DimStyle.Render(" [ctrl+x to expand]")
					}
					sb.WriteString(DimStyle.Render("▶ "+firstLine) + hint + "\n\n")
				}
				continue
			}
			// Render user message block with optional timestamp.
			blockWidth := contentWidth - UserMsgBlockStyle.GetHorizontalFrameSize()
			if blockWidth < 1 {
				blockWidth = 1
			}
			content := wrapText(msg.Content, blockWidth)
			if ts != "" {
				content += "\n" + DimStyle.Render(ts[3:]) // strip leading " · "
			}
			block := UserMsgBlockStyle.Width(blockWidth).Render(content)
			sb.WriteString(block + "\n\n")
		case "assistant":
			aIndent := strings.Repeat(" ", AssistantMsgIndent)
			// ask-user-prompt and escalate-prompt messages render as a styled question header.
			if assistantIdx < len(m.claudeMeta) && (m.claudeMeta[assistantIdx] == "ask-user-prompt" || m.claudeMeta[assistantIdx] == "escalate-prompt") {
				sb.WriteString(aIndent + HeaderStyle.Render("? "+msg.Content) + "\n\n")
				assistantIdx++
				continue
			}
			// Tool-call indicator messages render as collapsible tool blocks.
			if assistantIdx < len(m.claudeMeta) && m.claudeMeta[assistantIdx] == "tool-call-indicator" {
				if m.collapsedTools[i] {
					// Expanded: show full content.
					hint := ""
					if i == m.selectedMsgIdx {
						hint = DimStyle.Render(" [ctrl+x to collapse]")
					}
					sb.WriteString(aIndent + DimStyle.Render(msg.Content) + hint + "\n\n")
				} else {
					// Collapsed (default): show summary line.
					toolName := extractToolName(msg.Content)
					hint := ""
					if i == m.selectedMsgIdx {
						hint = DimStyle.Render(" [ctrl+x to expand]")
					}
					sb.WriteString(aIndent + DimStyle.Render("⚙ "+toolName+" ▶") + hint + "\n")
				}
				assistantIdx++
				continue
			}
			// Render claude byline (if any) above the response, with timestamp.
			indent := strings.Repeat(" ", AssistantMsgIndent)
			if assistantIdx < len(m.claudeMeta) && m.claudeMeta[assistantIdx] != "" {
				byline := ClaudeBylineStyle.Render("⬡ " + m.claudeMeta[assistantIdx])
				if ts != "" {
					byline += DimStyle.Render(ts)
				}
				sb.WriteString(indent + byline + "\n")
			}
			// Render reasoning trace (if any) above the response — only when expanded.
			if assistantIdx < len(m.reasoning) && m.reasoning[assistantIdx] != "" {
				if m.expandedReasoning[assistantIdx] {
					sb.WriteString(indentLines(renderReasoningBlock(m.reasoning[assistantIdx], contentWidth-AssistantMsgIndent), AssistantMsgIndent))
					sb.WriteString("\n")
				} else {
					sb.WriteString(indent + ReasoningStyle.Render("▶ thinking (press ctrl+t to expand)") + "\n\n")
				}
			}
			sb.WriteString(indentLines(m.renderMarkdown(msg.Content), AssistantMsgIndent) + "\n\n")
			assistantIdx++
		case "tool":
			// Render tool result as a collapsible dimmed block.
			if m.collapsedTools[i] {
				// Expanded: show full content.
				preview := msg.Content
				if len(preview) > 300 {
					preview = preview[:300] + "…"
				}
				hint := ""
				if i == m.selectedMsgIdx {
					hint = DimStyle.Render(" [ctrl+x to collapse]")
				}
				sb.WriteString(DimStyle.Render("⚙ tool result: "+preview) + hint + "\n\n")
			} else {
				// Collapsed (default): show summary line.
				hint := ""
				if i == m.selectedMsgIdx {
					hint = DimStyle.Render(" [ctrl+x to expand]")
				}
				sb.WriteString(DimStyle.Render("⚙ tool result ▶") + hint + "\n")
			}
		}
	}

	// Show streaming response in progress — re-render markdown incrementally.
	if m.streaming {
		streamIndent := strings.Repeat(" ", AssistantMsgIndent)
		// Live reasoning trace while thinking.
		if m.currentReasoning != "" {
			sb.WriteString(indentLines(renderReasoningBlock(m.currentReasoning, contentWidth-AssistantMsgIndent), AssistantMsgIndent))
			sb.WriteString("\n")
		} else {
			sb.WriteString(streamIndent + ReasoningStyle.Render("Thinking...") + "\n\n")
		}
		// Live response content.
		if m.currentResponse != "" {
			sb.WriteString(indentLines(m.renderMarkdown(m.currentResponse), AssistantMsgIndent))
			cursor := string(spinnerChars[m.spinnerFrame%len(spinnerChars)])
			sb.WriteString(StreamingStyle.Render(" " + cursor))
			sb.WriteString("\n\n")
		}
	}

	// Show error if present.
	if m.err != nil {
		sb.WriteString(ErrorStyle.Render("Error: "+m.err.Error()) + "\n\n")
	}

	m.chatViewport.SetContent(sb.String())
}

// hasBlocker reports whether the given job has an unanswered blocker recorded.
func (m Model) hasBlocker(j job.Job) bool {
	b, ok := m.blockers[j.Frontmatter.ID]
	return ok && b != nil && !b.Answered
}

// displayJobs returns the filtered and sorted list of jobs for display in the left panel.
// Rules:
//   - StatusDone jobs completed more than 24 hours ago are hidden.
//   - Sort order: StatusActive first (by Created asc), then StatusPaused (by Created asc),
//     then StatusDone (by Created asc).
func (m Model) displayJobs() []job.Job {
	now := time.Now()
	cutoff := now.Add(-24 * time.Hour)

	var active, paused, done []job.Job
	for _, j := range m.jobs {
		if j.Status == job.StatusDone {
			if j.Completed != "" {
				t, err := time.Parse(time.RFC3339, j.Completed)
				if err == nil && t.Before(cutoff) {
					continue // hide stale completed jobs
				}
			}
			done = append(done, j)
		} else if j.Status == job.StatusPaused {
			paused = append(paused, j)
		} else {
			active = append(active, j)
		}
	}

	// Each group is already in Created-ascending order (job.List sorts by Created).
	result := make([]job.Job, 0, len(active)+len(paused)+len(done))
	result = append(result, active...)
	result = append(result, paused...)
	result = append(result, done...)
	return result
}

// renderLeftPanel builds the left panel with three vertically-stacked sub-panes:
// Jobs (top), Job Detail (middle), and Teams (bottom).
// Each pane has its own rounded border that lights up when focused.
func (m Model) renderLeftPanel(panelWidth, panelHeight int) string {
	// Each pane border adds 2 horizontal (left+right border) + 2 horizontal (left+right padding) = 4.
	paneFrameH := FocusedPaneStyle.GetHorizontalBorderSize() + FocusedPaneStyle.GetHorizontalPadding()
	contentWidth := panelWidth - paneFrameH
	if contentWidth < 1 {
		contentWidth = 1
	}

	// Each pane border adds 2 vertical rows (top + bottom border line).
	paneFrameV := FocusedPaneStyle.GetVerticalBorderSize()
	// 3 panes × 2 rows border = 6 rows of border overhead.
	borderOverhead := 3 * paneFrameV

	// Bottom pane: content-driven height (header + one row per team + optional hint).
	bottomContentH := 1 + len(m.teams) // "Teams" header + one line per team
	if len(m.teams) == 0 {
		bottomContentH = 2 // header + "No teams configured"
	}
	if m.focused == focusTeams && len(m.teams) > 0 {
		bottomContentH++ // hint line
	}

	// Jobs hint line appears when the jobs pane is focused.
	jobsHintH := 0
	if m.focused == focusJobs && len(m.displayJobs()) > 0 {
		jobsHintH = 1
	}

	// Available height for content across all three panes.
	availableH := panelHeight - borderOverhead
	if availableH < 6 {
		availableH = 6
	}

	// Middle pane: fixed 30% of available content height.
	middleContentH := availableH * 30 / 100
	// Top pane gets whatever is left after middle + bottom + jobs hint.
	topContentH := availableH - middleContentH - bottomContentH - jobsHintH
	if topContentH < 3 {
		topContentH = 3
		// Re-derive middleContentH so the total still fits.
		middleContentH = availableH - topContentH - bottomContentH - jobsHintH
		if middleContentH < 2 {
			middleContentH = 2
		}
	}

	displayedJobs := m.displayJobs()

	// --- Top pane: Jobs ---
	var topLines []string
	topLines = append(topLines, gradientText("Jobs", [3]uint8{0, 200, 200}, [3]uint8{175, 50, 200}))
	if len(displayedJobs) == 0 {
		topLines = append(topLines, PlaceholderPaneStyle.Render("No jobs"))
	} else {
		for i, j := range displayedJobs {
			// Job name row with status prefix icon.
			var statusPrefix string
			switch j.Status {
			case job.StatusActive:
				statusPrefix = "▶ "
			case job.StatusPaused:
				statusPrefix = "⏸ "
			case job.StatusDone:
				statusPrefix = "✓ "
			default:
				statusPrefix = "· "
			}
			name := truncateStr(j.Name, contentWidth-len([]rune(statusPrefix))-1)
			selected := i == m.selectedJob
			if selected {
				topLines = append(topLines, JobSelectedStyle.Render(statusPrefix+name))
			} else {
				topLines = append(topLines, JobItemStyle.Render(statusPrefix+name))
			}

			// Child items: only show for active/paused jobs (not done).
			if j.Status != job.StatusDone {
				// Team + status sub-line (from first task).
				if tasks, err := job.ListTasks(j.Dir); err == nil && len(tasks) > 0 {
					t := tasks[0]
					if t.Team != "" {
						var prefix string
						switch t.Status {
						case job.StatusDone:
							prefix = "  ✓ "
						case job.StatusPaused:
							prefix = "  ⏸ "
						default:
							prefix = "  ◆ "
						}
						teamLine := prefix + truncateStr(t.Team, contentWidth-5)
						topLines = append(topLines, DimStyle.Render(teamLine))
					}
				}
				// BLOCKED child (always first if present).
				if m.hasBlocker(j) {
					blockerLine := "  ⚠ BLOCKED"
					topLines = append(topLines, TaskBlockedStyle.Render(blockerLine))
				}

				// Task subitems from TODO.md.
				if todosContent, err := job.ReadTodos(j.Dir); err == nil {
					lines := strings.Split(todosContent, "\n")
					for _, l := range lines {
						if strings.HasPrefix(l, "- [ ] ") {
							task := strings.TrimPrefix(l, "- [ ] ")
							taskLine := "  ○ " + truncateStr(task, contentWidth-5)
							topLines = append(topLines, TaskPendingStyle.Render(taskLine))
						} else if strings.HasPrefix(l, "- [x] ") {
							task := strings.TrimPrefix(l, "- [x] ")
							taskLine := "  ✓ " + truncateStr(task, contentWidth-5)
							topLines = append(topLines, TaskDoneStyle.Render(taskLine))
						}
					}
				}
			}
		}
	}
	// Hint line when jobs pane is focused.
	if m.focused == focusJobs && len(displayedJobs) > 0 {
		dj := displayedJobs
		hint := "↑↓ navigate"
		if m.selectedJob < len(dj) && m.hasBlocker(dj[m.selectedJob]) {
			hint = "Enter → resolve blocker"
		}
		topLines = append(topLines, DimStyle.Render(hint))
	}
	topContent := lipgloss.NewStyle().Height(topContentH + jobsHintH).Render(
		lipgloss.JoinVertical(lipgloss.Left, topLines...),
	)
	topPaneStyle := UnfocusedPaneStyle
	if m.focused == focusJobs {
		topPaneStyle = FocusedPaneStyle
	}
	topPane := topPaneStyle.Width(panelWidth).Render(topContent)

	// --- Middle pane: Job details (always unfocused) ---
	var middleLines []string
	if len(displayedJobs) == 0 || m.selectedJob >= len(displayedJobs) {
		middleLines = append(middleLines, LeftPanelHeaderStyle.Render("Job"))
		middleLines = append(middleLines, PlaceholderPaneStyle.Render("—"))
	} else {
		selectedJob := displayedJobs[m.selectedJob]
		middleLines = append(middleLines, LeftPanelHeaderStyle.Render(truncateStr(selectedJob.Name, contentWidth)))

		// Status badge
		var statusStyle lipgloss.Style
		switch selectedJob.Status {
		case job.StatusActive:
			statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("76"))
		case job.StatusPaused:
			statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("226"))
		default:
			statusStyle = DimStyle
		}
		statusWord := statusStyle.Render(string(selectedJob.Status))
		badge := DimStyle.Render("[") + statusWord + DimStyle.Render("]")
		middleLines = append(middleLines, badge)

		// Description (word-wrapped)
		if selectedJob.Description != "" {
			wrapped := wrapText(selectedJob.Description, contentWidth)
			for _, line := range strings.Split(wrapped, "\n") {
				middleLines = append(middleLines, DimStyle.Render(line))
			}
		}

		// TODO summary
		if todosContent, err := job.ReadTodos(selectedJob.Dir); err == nil {
			lines := strings.Split(todosContent, "\n")
			var pending []string
			doneCount := 0
			for _, l := range lines {
				if strings.HasPrefix(l, "- [ ] ") {
					pending = append(pending, strings.TrimPrefix(l, "- [ ] "))
				} else if strings.HasPrefix(l, "- [x] ") {
					doneCount++
				}
			}
			total := len(pending) + doneCount
			if total > 0 {
				summary := fmt.Sprintf("Tasks: %d/%d done", doneCount, total)
				middleLines = append(middleLines, DimStyle.Render(summary))
				shown := 0
				for _, task := range pending {
					if shown >= 3 {
						break
					}
					middleLines = append(middleLines, DimStyle.Render("· "+truncateStr(task, contentWidth-2)))
					shown++
				}
			}
		}
	}
	middleContent := lipgloss.NewStyle().Height(middleContentH).Render(
		lipgloss.JoinVertical(lipgloss.Left, middleLines...),
	)
	middlePane := UnfocusedPaneStyle.Width(panelWidth).Render(middleContent)

	// --- Bottom pane: Teams ---
	var bottomLines []string
	bottomLines = append(bottomLines, gradientText("Teams", [3]uint8{255, 175, 0}, [3]uint8{0, 200, 200}))
	if len(m.teams) == 0 {
		bottomLines = append(bottomLines, PlaceholderPaneStyle.Render("No teams configured"))
	} else {
		for i, t := range m.teams {
			teamColor := lipgloss.Color("135")
			if t.Coordinator != nil && t.Coordinator.Color != "" {
				teamColor = lipgloss.Color(t.Coordinator.Color)
			}
			prefix := lipgloss.NewStyle().Foreground(teamColor).Render("◆") + " "
			workerCount := fmt.Sprintf("(%d workers)", len(t.Workers))
			name := truncateStr(t.Name, contentWidth-2)
			if m.focused == focusTeams && i == m.selectedTeam {
				line := JobSelectedStyle.Render(prefix + name + " " + workerCount)
				bottomLines = append(bottomLines, line)
			} else {
				line := SidebarValueStyle.Bold(true).Render(prefix+name) + " " + DimStyle.Render(workerCount)
				bottomLines = append(bottomLines, line)
			}
		}
		if m.focused == focusTeams {
			bottomLines = append(bottomLines, DimStyle.Render("Enter → view team details"))
		}
	}
	bottomContent := lipgloss.JoinVertical(lipgloss.Left, bottomLines...)
	bottomPaneStyle := UnfocusedPaneStyle
	if m.focused == focusTeams {
		bottomPaneStyle = FocusedPaneStyle
	}
	bottomPane := bottomPaneStyle.Width(panelWidth).Render(bottomContent)

	inner := lipgloss.JoinVertical(lipgloss.Left, topPane, middlePane, bottomPane)
	return LeftPanelStyle.Width(panelWidth).Height(panelHeight).Render(inner)
}

// renderSidebar builds the right sidebar as two independent bordered panes
// stacked vertically: an operator/stats pane (top, fills remaining space)
// and an agents pane (bottom, auto-sized to content).
func (m Model) renderSidebar(sbWidth int) string {
	paneFrameH := FocusedPaneStyle.GetHorizontalBorderSize() + FocusedPaneStyle.GetHorizontalPadding()
	contentWidth := sbWidth - paneFrameH
	if contentWidth < 1 {
		contentWidth = 1
	}

	// --- Top pane: Operator stats ---
	var sb strings.Builder

	connStatus := ConnectedStyle.Render("connected")
	if !m.stats.Connected {
		connStatus = ErrorStyle.Render("disconnected")
	}
	headerText := gradientText("operator", [3]uint8{255, 175, 0}, [3]uint8{175, 50, 200})
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

	// While streaming, blend in live estimates for the current response.
	liveCompletionTokens := m.stats.CompletionTokens + m.stats.CompletionTokensLive
	liveReasoningTokens := m.stats.ReasoningTokens + m.stats.ReasoningTokensLive

	sb.WriteString(sidebarRow("Messages", fmt.Sprintf("%d", m.stats.MessageCount)))
	sb.WriteString(sidebarRow("Tokens in", fmt.Sprintf("%d", m.stats.PromptTokens)))
	sb.WriteString(sidebarRow("Tokens out", fmt.Sprintf("%d", liveCompletionTokens)))
	sb.WriteString(sidebarRow("Reasoning", fmt.Sprintf("%d", liveReasoningTokens)))

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
	sb.WriteString(renderContextBar(totalTokens, m.stats.SystemPromptTokens, m.stats.ContextLength, contentWidth, m.streaming, m.spinnerFrame))
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

	// --- Bottom pane: Agents (auto-sized to content) ---
	var agentsSB strings.Builder
	agentsSB.WriteString(gradientText("Agents", [3]uint8{50, 130, 255}, [3]uint8{0, 200, 200}))
	agentsSB.WriteString("\n")

	if m.gateway != nil {
		slots := m.gateway.Slots()
		hasAny := false
		for i, snap := range slots {
			if !snap.Active {
				continue
			}
			hasAny = true
			label := snap.AgentName + " · " + snap.JobID
			var statusIcon string
			if snap.Status == gateway.SlotRunning {
				statusIcon = string(spinnerChars[m.spinnerFrame%len(spinnerChars)]) + " "
			} else {
				statusIcon = "✓ "
			}
			line := statusIcon + truncateStr(label, contentWidth-2)
			if m.focused == focusAgents && i == m.selectedAgentSlot {
				agentsSB.WriteString(JobSelectedStyle.Render("🍞 " + truncateStr(label, contentWidth-3)))
			} else if snap.Status == gateway.SlotDone {
				agentsSB.WriteString(DimStyle.Render(statusIcon + truncateStr(label, contentWidth-2)))
			} else {
				agentsSB.WriteString(SidebarValueStyle.Render(line))
			}
			agentsSB.WriteString("\n")
		}
		if !hasAny {
			agentsSB.WriteString(DimStyle.Italic(true).Render("No agents running"))
		}

		var totalAgentIn, totalAgentOut int
		for _, snap := range slots {
			totalAgentIn += snap.InputTokens
			totalAgentOut += snap.OutputTokens
		}
		if totalAgentIn > 0 || totalAgentOut > 0 {
			agentsSB.WriteString("\n")
			agentsSB.WriteString(sidebarRow("Agent ↑ tok", compactNum(totalAgentIn)))
			agentsSB.WriteString(sidebarRow("Agent ↓ tok", compactNum(totalAgentOut)))
			for i, snap := range slots {
				if snap.InputTokens > 0 || snap.OutputTokens > 0 {
					perSlot := fmt.Sprintf("  s%d: ↑%s ↓%s", i, compactNum(snap.InputTokens), compactNum(snap.OutputTokens))
					agentsSB.WriteString(DimStyle.Render(truncateStr(perSlot, contentWidth)))
					agentsSB.WriteString("\n")
				}
			}
		}
	} else {
		agentsSB.WriteString(DimStyle.Italic(true).Render("No agents running"))
	}

	agentsPaneStyle := UnfocusedPaneStyle
	if m.focused == focusAgents {
		agentsPaneStyle = FocusedPaneStyle
	}

	// Ensure the agents pane is at least as tall as the input area so their
	// top borders align across the three columns.
	minAgentsH := inputHeight + InputAreaStyle.GetVerticalFrameSize()
	agentsPane := agentsPaneStyle.Width(sbWidth).Render(agentsSB.String())
	agentsH := lipgloss.Height(agentsPane)
	if agentsH < minAgentsH {
		agentsPane = agentsPaneStyle.Width(sbWidth).Height(minAgentsH).Render(agentsSB.String())
		agentsH = minAgentsH
	}

	// Calculate top pane height so the sidebar fills the terminal exactly.
	// agentsH includes the agents pane's border. Style.Height() sets the
	// outer height (including border/padding), so no extra subtraction needed.
	topContentH := m.height - agentsH
	if topContentH < 3 {
		topContentH = 3
	}

	topPaneStyle := UnfocusedPaneStyle
	topPane := topPaneStyle.Width(sbWidth).Height(topContentH).Render(sb.String())

	return lipgloss.JoinVertical(lipgloss.Left, topPane, agentsPane)
}

// renderGrid renders the 2×2 agent grid screen (4 slots per page, 4 pages total).
func (m *Model) renderGrid() string {
	cellW := m.width / 2
	cellH := (m.height - 1) / 2 // -1 to make room for the hotkey bar

	var cells [4]string
	var slots [gateway.MaxSlots]gateway.SlotSnapshot
	if m.gateway != nil {
		slots = m.gateway.Slots()
	}

	pageOffset := m.gridPage * 4

	for i := 0; i < 4; i++ {
		absIdx := pageOffset + i
		snap := slots[absIdx]
		focused := i == m.gridFocusCell

		innerH := cellH - 2 // top + bottom border
		innerW := cellW - 4 // left + right border + padding
		if innerH < 1 {
			innerH = 1
		}
		if innerW < 1 {
			innerW = 1
		}

		// Determine border color based on agent status.
		var borderColor color.Color
		switch {
		case !snap.Active:
			// Empty/inactive slot — always dim.
			if focused {
				borderColor = ColorPrimary
			} else {
				borderColor = ColorBorder
			}
		case snap.Status == gateway.SlotRunning && snap.PendingTool != "":
			if focused {
				// Bright orange for focused + pending tool.
				borderColor = lipgloss.Color("#ffaf00")
			} else {
				borderColor = ColorStreaming
			}
		case snap.Status == gateway.SlotRunning:
			if focused {
				// Bright green for focused + running.
				borderColor = lipgloss.Color("#5fff5f")
			} else {
				borderColor = ColorConnected
			}
		case snap.Status == gateway.SlotDone:
			if focused {
				borderColor = ColorPrimary
			} else {
				borderColor = ColorDim
			}
		default:
			if focused {
				borderColor = ColorPrimary
			} else {
				borderColor = ColorBorder
			}
		}
		var headerStyle lipgloss.Style
		if focused {
			headerStyle = HeaderStyle
		} else {
			headerStyle = SidebarHeaderStyle
		}

		borderType := lipgloss.RoundedBorder()
		if focused {
			borderType = lipgloss.ThickBorder()
		}

		cellStyle := lipgloss.NewStyle().
			Width(cellW).
			Height(cellH).
			Border(borderType).
			BorderForeground(borderColor).
			Padding(0, 1)

		if !snap.Active {
			emptyContent := DimStyle.Render(fmt.Sprintf("slot %d — empty", absIdx))
			emptyLines := strings.Split(emptyContent, "\n")
			if len(emptyLines) > innerH {
				emptyLines = emptyLines[:innerH]
			}
			cells[i] = cellStyle.Render(strings.Join(emptyLines, "\n"))
			continue
		}

		// 1. Header: statusMark · agent · job · elapsed
		elapsed := time.Since(snap.StartTime).Round(time.Second)
		if snap.Status == gateway.SlotDone && !snap.EndTime.IsZero() {
			elapsed = snap.EndTime.Sub(snap.StartTime).Round(time.Second)
		}
		statusMark := "▶"
		if snap.Status == gateway.SlotDone {
			statusMark = "✓"
		}
		header := fmt.Sprintf("%s %s · %s · %s", statusMark, snap.AgentName, snap.JobID, elapsed)

		// Append mini token usage bar if tokens are present.
		totalTokens := snap.InputTokens + snap.OutputTokens
		if totalTokens > 0 {
			header += " " + miniTokenBar(totalTokens)
		}

		headerLine := headerStyle.Render(truncateStr(header, innerW))

		// 2. Summary (prefer ExitSummary when done)
		summary := snap.Summary
		if snap.Status == gateway.SlotDone && snap.ExitSummary != "" {
			summary = snap.ExitSummary
		}
		if summary == "" {
			summary = snap.AgentName + " on " + snap.JobID
		}
		summaryLine := truncateStr(summary, innerW)

		// 3. Model line (with optional turn count and stop reason)
		modelStr := snap.Model
		if modelStr == "" {
			modelStr = "model: unknown"
		}
		if snap.TurnCount > 0 {
			modelStr += fmt.Sprintf(" · %d turns", snap.TurnCount)
		}
		if snap.StopReason != "" && snap.Status == gateway.SlotDone {
			modelStr += " · stop:" + snap.StopReason
		}
		modelLine := DimStyle.Render(truncateStr(modelStr, innerW))

		// 3b. Token line (only if any tokens recorded)
		var tokenLine string
		if snap.InputTokens > 0 || snap.OutputTokens > 0 {
			tokenLine = DimStyle.Render(truncateStr(
				fmt.Sprintf("↑%s ↓%s", compactNum(snap.InputTokens), compactNum(snap.OutputTokens)),
				innerW,
			))
		}

		// 3c. Version line (only if ClaudeVersion is known)
		var versionLine string
		if snap.ClaudeVersion != "" {
			versionLine = DimStyle.Render(truncateStr("claude v"+snap.ClaudeVersion, innerW))
		}

		// 3d. Session line (only if SessionID is known; truncated to 8 chars)
		var sessionLine string
		if snap.SessionID != "" {
			sid := snap.SessionID
			if len(sid) > 8 {
				sid = sid[:8]
			}
			sessionLine = DimStyle.Render(truncateStr("session: "+sid, innerW))
		}

		// 3e. Subagent line (only if any subagents have been spawned)
		var subagentLine string
		if snap.SubagentsSpawned > 0 {
			subagentStr := fmt.Sprintf("subagents: %d spawned, %d in-flight", snap.SubagentsSpawned, snap.SubagentsInFlight)
			if snap.SubagentsInFlight > 0 {
				subagentLine = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Render(truncateStr(subagentStr, innerW))
			} else {
				subagentLine = DimStyle.Render(truncateStr(subagentStr, innerW))
			}
		}

		// 4. Prompt preview: first 3 non-empty lines of the prompt
		var promptLines []string
		for _, l := range strings.Split(snap.Prompt, "\n") {
			l = strings.TrimSpace(l)
			if l != "" {
				promptLines = append(promptLines, l)
			}
			if len(promptLines) == 3 {
				break
			}
		}
		var promptPreview string
		if len(promptLines) > 0 {
			truncatedLines := make([]string, len(promptLines))
			for j, l := range promptLines {
				truncatedLines[j] = truncateStr(l, innerW)
			}
			promptPreview = DimStyle.Render(strings.Join(truncatedLines, "\n"))
		}
		// Hint for focused cell
		if focused {
			promptPreview += "\n" + DimStyle.Render(truncateStr("p: view full prompt", innerW))
		}

		// 5. Separator
		separator := DimStyle.Render(strings.Repeat("─", innerW))

		// 6. Output: tail of snap.Output to fill remaining height.
		// Budget:
		//   1 header + 1 summary + 1 model + (1 token if present) +
		//   (1 version if present) + (1 session if present) + (1 subagent if present) +
		//   3 prompt + 1 p-hint(focused) + 1 separator
		metaLines := 7 // header + summary + model + 3 prompt + separator
		if focused {
			metaLines++ // p-hint line
		}
		if tokenLine != "" {
			metaLines++ // token line
		}
		if versionLine != "" {
			metaLines++ // version line
		}
		if sessionLine != "" {
			metaLines++ // session line
		}
		if subagentLine != "" {
			metaLines++ // subagent line
		}
		outputH := innerH - metaLines
		if outputH < 0 {
			outputH = 0
		}

		// Build output body lines, prepending special indicators.
		var outputBodyLines []string

		// PendingTool indicator
		if snap.Status == gateway.SlotRunning && snap.PendingTool != "" {
			toolIndicator := lipgloss.NewStyle().Foreground(ColorStreaming).Render(
				truncateStr("⚙ "+snap.PendingTool, innerW),
			)
			outputBodyLines = append(outputBodyLines, toolIndicator)
		}

		// ThinkingOutput indicator
		if snap.ThinkingOutput != "" {
			thinkLine := DimStyle.Render(truncateStr(
				fmt.Sprintf("[thinking: %s chars]", compactNum(len(snap.ThinkingOutput))),
				innerW,
			))
			outputBodyLines = append(outputBodyLines, thinkLine)
		}

		// SubagentOutput indicator — show the last non-empty line of subagent output.
		if snap.SubagentOutput != "" {
			lastSubLine := ""
			for _, sl := range strings.Split(snap.SubagentOutput, "\n") {
				if strings.TrimSpace(sl) != "" {
					lastSubLine = sl
				}
			}
			if lastSubLine == "" {
				lastSubLine = snap.SubagentOutput
			}
			subLine := DimStyle.Render(truncateStr("↳ "+lastSubLine, innerW))
			outputBodyLines = append(outputBodyLines, subLine)
		}

		// Reserve lines for the indicators we just added.
		indicatorLines := len(outputBodyLines)
		tailH := outputH - indicatorLines
		if tailH < 0 {
			tailH = 0
		}

		if snap.Output != "" && tailH > 0 {
			outLines := strings.Split(snap.Output, "\n")
			if len(outLines) > tailH {
				outLines = outLines[len(outLines)-tailH:]
			}
			for j, l := range outLines {
				if len([]rune(l)) > innerW {
					outLines[j] = string([]rune(l)[:innerW])
				}
			}
			outputBodyLines = append(outputBodyLines, outLines...)
		}

		outputBody := strings.Join(outputBodyLines, "\n")

		// Assemble cell content parts.
		parts := []string{
			headerLine,
			summaryLine,
			modelLine,
		}
		if tokenLine != "" {
			parts = append(parts, tokenLine)
		}
		if versionLine != "" {
			parts = append(parts, versionLine)
		}
		if sessionLine != "" {
			parts = append(parts, sessionLine)
		}
		if subagentLine != "" {
			parts = append(parts, subagentLine)
		}
		parts = append(parts, promptPreview, separator, outputBody)

		inner := strings.Join(parts, "\n")

		// Hard-clamp to innerH lines so ANSI content can never overflow the cell budget.
		innerLines := strings.Split(inner, "\n")
		if len(innerLines) > innerH {
			innerLines = innerLines[:innerH]
		}
		inner = strings.Join(innerLines, "\n")

		cells[i] = cellStyle.Render(inner)
	}

	hotkeyBar := DimStyle.Render(fmt.Sprintf(
		"  arrows: navigate   ·   k/ctrl+k: kill   ·   enter: view output   ·   p: view prompt   ·   [/]: page %d/4   ·   ctrl+g / esc: close",
		m.gridPage+1,
	))
	hotkeyBar = lipgloss.NewStyle().Width(m.width).Render(hotkeyBar)

	top := lipgloss.JoinHorizontal(lipgloss.Top, cells[0], cells[1])
	bottom := lipgloss.JoinHorizontal(lipgloss.Top, cells[2], cells[3])
	return lipgloss.JoinVertical(lipgloss.Left, hotkeyBar, top, bottom)
}

// commaInt formats an integer with comma-separated thousands (e.g. 200000 → "200,000").
func commaInt(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + commaInt(-n)
	}
	if len(s) <= 3 {
		return s
	}
	// Insert commas from the right.
	var b strings.Builder
	rem := len(s) % 3
	if rem > 0 {
		b.WriteString(s[:rem])
	}
	for i := rem; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// renderContextBar renders a segmented progress bar showing context window usage.
// The bar has two segments: system prompt tokens (dimmer) and conversation tokens
// (gradient from green → yellow → red). When streaming, conversation cells pulse.
// systemTokens is the estimated token count of the system prompt.
func renderContextBar(used, systemTokens, total, width int, streaming bool, spinnerFrame int) string {
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
		summary = fmt.Sprintf("%s / %s (%.0f%%)", commaInt(used), commaInt(total), pct*100)
	} else {
		summary = fmt.Sprintf("%s / ?", commaInt(used))
	}

	// Calculate system vs conversation segments.
	var sysPct float64
	if total > 0 && systemTokens > 0 {
		sysPct = float64(systemTokens) / float64(total)
		if sysPct > pct {
			sysPct = pct // system can't exceed total used
		}
	}
	sysFilled := int(sysPct * float64(width))
	totalFilled := int(pct * float64(width))
	convFilled := totalFilled - sysFilled
	empty := width - totalFilled

	// Gradient anchors: green → yellow (midpoint) → red.
	type rgb struct{ r, g, b uint8 }
	green := rgb{82, 196, 26}
	yellow := rgb{250, 173, 20}
	red := rgb{245, 34, 45}

	// lerpRGB interpolates between two colors by t in [0,1].
	lerpRGB := func(a, b rgb, t float64) rgb {
		return rgb{
			r: uint8(float64(a.r)*(1-t) + float64(b.r)*t),
			g: uint8(float64(a.g)*(1-t) + float64(b.g)*t),
			b: uint8(float64(a.b)*(1-t) + float64(b.b)*t),
		}
	}

	sysStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("237"))

	var bar strings.Builder

	// System prompt segment — dim solid fill.
	bar.WriteString(sysStyle.Render(strings.Repeat("▓", sysFilled)))

	// Conversation segment — gradient fill.
	for i := range convFilled {
		// t is position across the full bar width.
		var t float64
		if width > 1 {
			t = float64(sysFilled+i) / float64(width-1)
		}
		var c rgb
		if t < 0.5 {
			c = lerpRGB(green, yellow, t*2)
		} else {
			c = lerpRGB(yellow, red, (t-0.5)*2)
		}
		cellChar := "█"
		if streaming && i%2 == spinnerFrame%2 {
			cellChar = "▓"
		}
		bar.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b))).
			Render(cellChar))
	}

	// Empty segment.
	bar.WriteString(emptyStyle.Render(strings.Repeat("░", empty)))

	// Summary line with system/conversation breakdown.
	var detail string
	if systemTokens > 0 {
		convTokens := used - systemTokens
		if convTokens < 0 {
			convTokens = 0
		}
		detail = fmt.Sprintf("sys ~%s · conv ~%s", commaInt(systemTokens), commaInt(convTokens))
	}

	lines := bar.String() + "\n" + DimStyle.Render(summary)
	if detail != "" {
		lines += "\n" + DimStyle.Render(detail)
	}
	return lines
}

// miniTokenBar returns a compact 8-char token usage bar with gradient coloring
// and a compact token count suffix, e.g. "[████░░░░] 45k".
// maxTokens is the reference ceiling (200k).
func miniTokenBar(totalTokens int) string {
	const barWidth = 8
	const maxTokens = 200_000

	pct := float64(totalTokens) / float64(maxTokens)
	if pct > 1 {
		pct = 1
	}
	filled := int(pct * barWidth)
	if filled < 0 {
		filled = 0
	}
	empty := barWidth - filled

	// Gradient anchors: green → yellow (midpoint) → red.
	type rgb struct{ r, g, b uint8 }
	green := rgb{82, 196, 26}
	yellow := rgb{250, 173, 20}
	red := rgb{245, 34, 45}
	lerpRGB := func(a, b rgb, t float64) rgb {
		return rgb{
			r: uint8(float64(a.r)*(1-t) + float64(b.r)*t),
			g: uint8(float64(a.g)*(1-t) + float64(b.g)*t),
			b: uint8(float64(a.b)*(1-t) + float64(b.b)*t),
		}
	}

	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("237"))

	var bar strings.Builder
	bar.WriteString("[")
	for i := range filled {
		var t float64
		if barWidth > 1 {
			t = float64(i) / float64(barWidth-1)
		}
		var c rgb
		if t < 0.5 {
			c = lerpRGB(green, yellow, t*2)
		} else {
			c = lerpRGB(yellow, red, (t-0.5)*2)
		}
		bar.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b))).
			Render("█"))
	}
	bar.WriteString(emptyStyle.Render(strings.Repeat("░", empty)))
	bar.WriteString("] ")
	bar.WriteString(compactNum(totalTokens))

	return bar.String()
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
	m.timestamps = append(m.timestamps, time.Now())
	m.reasoning = append(m.reasoning, "")
	m.claudeMeta = append(m.claudeMeta, "")
	m.stats.MessageCount++
	m.updateViewportContent()
	if !m.userScrolled {
		m.chatViewport.GotoBottom()
	}
}

// newSession resets the conversation and all session statistics.
// initMessages resets m.messages and seeds it with the system prompt as message[0]
// (if a system prompt is set). Call this at startup and on /new.
func (m *Model) initMessages() {
	m.messages = nil
	m.timestamps = nil
	if m.systemPrompt != "" {
		m.messages = []llm.Message{{Role: "system", Content: m.systemPrompt}}
		m.timestamps = append(m.timestamps, time.Now())
		m.stats.SystemPromptTokens = estimateTokens(m.systemPrompt)
	} else {
		m.stats.SystemPromptTokens = 0
	}
	m.completionMsgIdx = make(map[int]bool)
	m.expandedMsgs = make(map[int]bool)
	m.selectedMsgIdx = -1
	m.expandedReasoning = make(map[int]bool)
	m.collapsedTools = make(map[int]bool)
	m.confirmDispatch = false
	m.changingTeam = false
	m.pendingDispatch = llm.ToolCall{}
	m.confirmKill = false
	m.pendingKillSlot = 0
	m.confirmTimeout = false
	m.pendingTimeoutSlot = 0
}

// hasConversation reports whether the conversation contains at least one user
// message (i.e. the welcome art should be hidden). Assistant-only messages
// (e.g. the startup greeting) are shown alongside the art.
func (m *Model) hasConversation() bool {
	for _, msg := range m.messages {
		if msg.Role == "user" {
			return true
		}
	}
	return false
}

func (m *Model) newSession() {
	m.systemPrompt = agents.BuildOperatorPrompt(m.teams, m.awareness)
	m.initMessages()
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
	m.userScrolled = false
	m.updateViewportContent()
	m.chatViewport.GotoBottom()
	m.input.Focus()
}

// drainPendingCompletions injects any buffered agent-completion notifications
// into m.messages and clears the buffer. It returns the updated message slice
// and true if any notifications were drained (indicating a new stream should start).
func (m *Model) drainPendingCompletions() ([]llm.Message, bool) {
	if len(m.pendingCompletions) == 0 {
		return m.messages, false
	}
	for _, pc := range m.pendingCompletions {
		m.messages = append(m.messages, llm.Message{Role: "user", Content: pc.notification})
		m.timestamps = append(m.timestamps, time.Now())
	}
	m.pendingCompletions = nil
	return m.messages, true
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

	var temperature float64

	client := m.llmClient
	return tea.Batch(
		func() tea.Msg {
			ch := client.ChatCompletionStreamWithTools(ctx, msgs, llm.AvailableTools, temperature)
			return streamStartedMsg{ch: ch}
		},
		spinnerTick(), // re-arm spinner animation for streaming cursor
	)
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
	m.timestamps = append(m.timestamps, time.Now())
	m.stats.MessageCount++
	m.err = nil
	m.userScrolled = false
	m.hasNewMessages = false

	m.updateViewportContent()
	m.chatViewport.GotoBottom()

	return m.startStream(m.messages)
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
	m.timestamps = append(m.timestamps, time.Now())
	m.stats.MessageCount++
	m.streaming = true
	m.currentResponse = ""
	m.currentReasoning = ""
	m.err = nil
	m.userScrolled = false
	m.hasNewMessages = false
	m.stats.ResponseStart = time.Now()

	m.updateViewportContent()
	m.chatViewport.GotoBottom()

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelStream = cancel

	ch := streamClaudeResponse(ctx, prompt, m.claudeCfg)
	return tea.Batch(
		func() tea.Msg {
			return streamStartedMsg{ch: ch}
		},
		spinnerTick(), // re-arm spinner animation for streaming cursor
	)
}

// sendAnthropicMessage appends the user prompt to history and starts a direct
// Anthropic API stream using OAuth credentials from the macOS Keychain.
func (m *Model) sendAnthropicMessage(prompt string) tea.Cmd {
	m.input.Blur()
	m.filteredCmds = nil
	m.selectedCmdIdx = 0

	m.messages = append(m.messages, llm.Message{
		Role:    "user",
		Content: "/anthropic " + prompt,
	})
	m.timestamps = append(m.timestamps, time.Now())
	m.stats.MessageCount++
	m.streaming = true
	m.currentResponse = ""
	m.currentReasoning = ""
	m.err = nil
	m.userScrolled = false
	m.hasNewMessages = false
	m.stats.ResponseStart = time.Now()

	m.updateViewportContent()
	m.chatViewport.GotoBottom()

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelStream = cancel

	ch := anthropic.StreamMessage(ctx, anthropic.DefaultModel, prompt)
	return tea.Batch(
		func() tea.Msg {
			return streamStartedMsg{ch: ch}
		},
		spinnerTick(),
	)
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
				SessionID:      resp.Meta.SessionID,
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
	s := msg.Model + " · " + msg.PermissionMode + " mode"
	if msg.Version != "" {
		s += " · claude v" + msg.Version
	}
	if msg.SessionID != "" {
		short := msg.SessionID
		if len(short) > 8 {
			short = short[:8]
		}
		s += " · session: " + short
	}
	return s
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

// isReadOnlyTeam returns true if the team's directory is one of the well-known
// auto-detected read-only directories (~/.opencode/agents, ~/.claude/agents).
func isReadOnlyTeam(team agents.Team) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	readOnlyDirs := []string{
		filepath.Join(home, ".opencode", "agents"),
		filepath.Join(home, ".claude", "agents"),
	}
	for _, d := range readOnlyDirs {
		if team.Dir == d {
			return true
		}
	}
	return false
}

// reloadTeamsForModal refreshes m.teamsModal.teams from disk.
func (m *Model) reloadTeamsForModal() {
	discovered, _ := agents.DiscoverTeams(m.teamsDir)
	auto := agents.AutoDetectTeams()
	m.teamsModal.teams = append(discovered, auto...)
}

// maybeAutoDetectCoordinator fires an LLM call to pick a coordinator for team
// if the team has no coordinator, is not read-only, and hasn't been attempted yet.
func (m *Model) maybeAutoDetectCoordinator(team agents.Team) tea.Cmd {
	if isReadOnlyTeam(team) {
		return nil
	}
	if team.Coordinator != nil {
		return nil
	}
	allAgents := team.Workers // no coordinator, so all agents are workers
	if len(allAgents) == 0 {
		return nil
	}
	if m.teamsModal.autoDetectPending == nil {
		m.teamsModal.autoDetectPending = make(map[string]bool)
	}
	if m.teamsModal.autoDetectPending[team.Dir] {
		return nil
	}
	m.teamsModal.autoDetectPending[team.Dir] = true
	m.teamsModal.autoDetecting = true

	// Capture values for the goroutine.
	client := m.llmClient
	teamDir := team.Dir
	agentsCopy := make([]agents.Agent, len(allAgents))
	copy(agentsCopy, allAgents)

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var sb strings.Builder
		sb.WriteString("Given these agents, which one is best suited to be the team coordinator? Respond with just the agent name, nothing else.\n\nAgents:\n")
		for _, a := range agentsCopy {
			desc := a.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(&sb, "- %s: %s\n", a.Name, desc)
		}

		msgs := []llm.Message{{Role: "user", Content: sb.String()}}
		result, err := client.ChatCompletion(ctx, msgs)
		if err != nil {
			return TeamsAutoDetectDoneMsg{teamDir: teamDir, err: err}
		}

		// Match result to an agent name (case-insensitive, trimmed).
		result = strings.TrimSpace(result)
		for _, a := range agentsCopy {
			if strings.EqualFold(result, a.Name) {
				return TeamsAutoDetectDoneMsg{teamDir: teamDir, agentName: a.Name}
			}
		}
		// No match.
		return TeamsAutoDetectDoneMsg{teamDir: teamDir}
	}
}

// renderTeamsModal renders the full-screen teams management modal.
func (m *Model) renderTeamsModal() string {
	teams := m.teamsModal.teams

	// Modal dimensions: use most of the terminal.
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

	// Inner width after modal border + padding (border=2, padding=2 each side).
	innerW := modalW - TeamsModalStyle.GetHorizontalFrameSize()
	if innerW < 10 {
		innerW = 10
	}

	// Left panel: ~32 chars inner content.
	leftInnerW := 30
	leftPanelW := leftInnerW + TeamsPanelStyle.GetHorizontalFrameSize()
	if leftPanelW > innerW/2 {
		leftPanelW = innerW / 2
		leftInnerW = leftPanelW - TeamsPanelStyle.GetHorizontalFrameSize()
	}

	// Right panel: remaining width.
	rightPanelW := innerW - leftPanelW - 1 // -1 for spacing
	rightInnerW := rightPanelW - TeamsPanelStyle.GetHorizontalFrameSize()
	if rightInnerW < 5 {
		rightInnerW = 5
	}

	// Panel inner height (subtract border + footer line).
	footerLines := 1
	panelH := modalH - TeamsModalStyle.GetVerticalFrameSize() - footerLines - 1
	if panelH < 5 {
		panelH = 5
	}
	panelInnerH := panelH - TeamsPanelStyle.GetVerticalFrameSize()
	if panelInnerH < 3 {
		panelInnerH = 3
	}

	// --- Left panel: team list ---
	var leftLines []string
	for i, t := range teams {
		var icon string
		if t.Coordinator != nil {
			icon = "◆"
		} else {
			icon = "■"
		}
		name := truncateStr(t.Name, leftInnerW-4)
		line := fmt.Sprintf(" %s %s", icon, name)
		if isReadOnlyTeam(t) {
			line += " 🔒"
		}
		if i == m.teamsModal.teamIdx {
			line = TeamsSelectedStyle.Width(leftInnerW).Render(line)
		} else if isReadOnlyTeam(t) {
			line = TeamsReadOnlyStyle.Render(line)
		}
		leftLines = append(leftLines, line)
	}

	// Input mode: show name-entry prompt at the bottom.
	if m.teamsModal.inputMode {
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, DimStyle.Render("> New team name:"))
		cursor := m.teamsModal.nameInput + "█"
		leftLines = append(leftLines, "  "+cursor)
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
	if m.teamsModal.focus == 0 {
		leftPanel = TeamsFocusedPanel.Width(leftPanelW).Height(panelH).Render(leftContent)
	} else {
		leftPanel = TeamsPanelStyle.Width(leftPanelW).Height(panelH).Render(leftContent)
	}

	// --- Right panel: team detail ---
	var rightLines []string
	if len(teams) == 0 {
		rightLines = append(rightLines, DimStyle.Render("No teams configured."))
		rightLines = append(rightLines, DimStyle.Render("Press [Ctrl+N] to create one."))
	} else if m.teamsModal.teamIdx < len(teams) {
		team := teams[m.teamsModal.teamIdx]

		// Header.
		rightLines = append(rightLines, HeaderStyle.Render(truncateStr(team.Name, rightInnerW)))
		rightLines = append(rightLines, DimStyle.Render(strings.Repeat("─", rightInnerW)))

		// Coordinator line.
		coordName := "(none)"
		if team.Coordinator != nil {
			coordName = team.Coordinator.Name
		}
		coordLine := "Coordinator: " + coordName
		if m.teamsModal.autoDetecting {
			coordLine += DimStyle.Render(" [detecting...]")
		}
		rightLines = append(rightLines, coordLine)
		rightLines = append(rightLines, "")

		// Build ordered agent list for right panel: coordinator first, then workers.
		var agentList []agents.Agent
		if team.Coordinator != nil {
			agentList = append(agentList, *team.Coordinator)
		}
		agentList = append(agentList, team.Workers...)

		// Workers section — scroll a window around the selected agent so long
		// lists don't get clipped by the panel height.
		rightLines = append(rightLines, fmt.Sprintf("Workers (%d)", len(team.Workers)))
		// How many lines are left for agents after header rows (name, divider,
		// coordinator, blank, workers-header = 5 lines) and optional confirm (2).
		confirmExtra := 0
		if m.teamsModal.confirmDelete {
			confirmExtra = 2
		}
		agentAreaH := panelInnerH - 5 - confirmExtra
		if agentAreaH < 1 {
			agentAreaH = 1
		}
		// Compute scroll offset so selected agent is always visible.
		scrollOffset := 0
		if len(agentList) > agentAreaH {
			scrollOffset = m.teamsModal.agentIdx - agentAreaH/2
			if scrollOffset < 0 {
				scrollOffset = 0
			}
			if scrollOffset > len(agentList)-agentAreaH {
				scrollOffset = len(agentList) - agentAreaH
			}
		}
		visibleAgents := agentList
		if scrollOffset > 0 || len(agentList) > agentAreaH {
			end := scrollOffset + agentAreaH
			if end > len(agentList) {
				end = len(agentList)
			}
			visibleAgents = agentList[scrollOffset:end]
		}
		for vi, a := range visibleAgents {
			i := vi + scrollOffset
			prefix := "  ■ "
			if team.Coordinator != nil && i == 0 {
				prefix = "  ◆ " // coordinator marker
			}
			line := prefix + truncateStr(a.Name, rightInnerW-4)
			if m.teamsModal.focus == 1 && i == m.teamsModal.agentIdx {
				line = TeamsSelectedStyle.Width(rightInnerW).Render(line)
			}
			rightLines = append(rightLines, line)
		}

		// Delete confirmation.
		if m.teamsModal.confirmDelete {
			rightLines = append(rightLines, "")
			rightLines = append(rightLines, TeamsWarningStyle.Render(
				fmt.Sprintf("⚠ Delete '%s'? [Enter] confirm  [Esc] cancel", truncateStr(team.Name, rightInnerW-30)),
			))
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
	if m.teamsModal.focus == 1 {
		rightPanel = TeamsFocusedPanel.Width(rightPanelW).Height(panelH).Render(rightContent)
	} else {
		rightPanel = TeamsPanelStyle.Width(rightPanelW).Height(panelH).Render(rightContent)
	}

	// Join panels horizontally.
	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, " ", rightPanel)

	// Footer with key hints — dim read-only-gated keys when team is read-only.
	readOnly := len(teams) > 0 && m.teamsModal.teamIdx < len(teams) && isReadOnlyTeam(teams[m.teamsModal.teamIdx])
	nHint := "[Ctrl+N] New"
	dHint := "[Ctrl+D] Delete"
	cHint := "[Ctrl+K] Set Coordinator"
	if readOnly {
		nHint = DimStyle.Render(nHint)
		dHint = DimStyle.Render(dHint)
		cHint = DimStyle.Render(cHint)
	}
	footer := lipgloss.JoinHorizontal(lipgloss.Left,
		nHint, "  ", dHint, "  ", cHint, "  ",
		DimStyle.Render("[Tab] Switch"), "  ",
		DimStyle.Render("[Esc] Close"),
	)

	inner := lipgloss.JoinVertical(lipgloss.Left, panels, footer)

	modal := TeamsModalStyle.Width(modalW).Render(inner)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}

// jobByID returns the job with the given ID, or false if not found.
func (m *Model) jobByID(id string) (job.Job, bool) {
	for _, j := range m.jobs {
		if j.Frontmatter.ID == id {
			return j, true
		}
	}
	return job.Job{}, false
}

// submitBlockerAnswers writes the answered blocker questions to BLOCKER.md
// and emits a blockerAnswersSubmittedMsg to the event loop.
func (m *Model) submitBlockerAnswers() tea.Cmd {
	// Capture values for the closure.
	b := m.blockerModal.blocker
	jobID := m.blockerModal.jobID
	return func() tea.Msg {
		j, ok := m.jobByID(jobID)
		if !ok {
			return nil
		}
		if err := job.WriteBlockerAnswers(j.Dir, b); err != nil {
			log.Printf("failed to write blocker answers: %v", err)
		}
		return blockerAnswersSubmittedMsg{jobID: jobID, blocker: b}
	}
}

// renderBlockerModal renders the full-screen blocker Q&A modal.
func (m *Model) renderBlockerModal() string {
	if !m.blockerModal.show || m.blockerModal.blocker == nil {
		return ""
	}
	b := m.blockerModal.blocker

	modalW := m.width - 8
	if modalW > 90 {
		modalW = 90
	}
	if modalW < 40 {
		modalW = 40
	}
	modalH := m.height - 6
	if modalH > 30 {
		modalH = 30
	}
	if modalH < 10 {
		modalH = 10
	}
	innerW := modalW - 4 // account for border + padding

	// Header.
	header := lipgloss.NewStyle().Bold(true).Foreground(ColorStreaming).Render(
		fmt.Sprintf("⚠  Blocker: %s", b.BlockerSummary),
	)

	var sections []string

	// Context (truncated to 6 lines).
	if b.Context != "" {
		lines := strings.Split(b.Context, "\n")
		if len(lines) > 6 {
			lines = lines[:6]
			lines = append(lines, DimStyle.Render("..."))
		}
		sections = append(sections, HeaderStyle.Render("Context"))
		sections = append(sections, strings.Join(lines, "\n"))
	}

	// Questions.
	sections = append(sections, HeaderStyle.Render(fmt.Sprintf("Questions (%d total)", len(b.Questions))))
	for i, q := range b.Questions {
		prefix := "  "
		qStyle := DimStyle
		if i == m.blockerModal.questionIdx {
			prefix = "▶ "
			qStyle = lipgloss.NewStyle() // normal weight for current
		}

		qLine := prefix + qStyle.Render(fmt.Sprintf("%d. %s", i+1, q.Text))
		sections = append(sections, qLine)

		if i == m.blockerModal.questionIdx {
			if len(q.Options) > 0 {
				for oi, opt := range q.Options {
					optStyle := DimStyle
					if q.Answer == opt {
						optStyle = lipgloss.NewStyle().Foreground(ColorConnected).Bold(true)
					}
					sections = append(sections, optStyle.Render(fmt.Sprintf("    [%d] %s", oi+1, opt)))
				}
			} else {
				// Free-form input line.
				inputVal := m.blockerModal.inputText
				if q.Answer != "" && inputVal == "" {
					inputVal = q.Answer
				}
				sections = append(sections, fmt.Sprintf("    > %s_", inputVal))
			}
		} else if q.Answer != "" {
			sections = append(sections, DimStyle.Render(fmt.Sprintf("    ✓ %s", q.Answer)))
		}
	}

	// Footer hints.
	var footerHint string
	if len(b.Questions) > 0 && len(b.Questions[m.blockerModal.questionIdx].Options) > 0 {
		footerHint = "[1-9] select  [↑↓] navigate  [s] submit  [Esc] close"
	} else {
		footerHint = "[Enter] confirm  [↑↓] navigate  [s] submit  [Esc] close"
	}
	footer := DimStyle.Render(footerHint)

	allParts := append([]string{header, ""}, append(sections, "", footer)...)
	body := lipgloss.NewStyle().Width(innerW).Render(
		lipgloss.JoinVertical(lipgloss.Left, allParts...),
	)

	modal := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorStreaming).
		Padding(1, 2).
		Width(modalW).
		Height(modalH).
		Render(body)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}

// firstLineOf returns the first non-empty line of s.
func firstLineOf(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return s
}

// renderCompletionBlock renders the full content of a completion message as a
// dimmed indented block, skipping the first line (already shown in the header).
func renderCompletionBlock(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(DimStyle.Render("  "+line) + "\n")
	}
	return sb.String()
}

// hasCollapsibleMessages reports whether there are any collapsible messages
// (completion messages, tool-call indicators, or tool results) in the conversation.
func (m *Model) hasCollapsibleMessages() bool {
	if len(m.completionMsgIdx) > 0 {
		return true
	}
	assistantIdx := 0
	for _, msg := range m.messages {
		if msg.Role == "tool" {
			return true
		}
		if msg.Role == "assistant" {
			if assistantIdx < len(m.claudeMeta) && m.claudeMeta[assistantIdx] == "tool-call-indicator" {
				return true
			}
			assistantIdx++
		}
	}
	return false
}

// isToolCallIndicatorIdx reports whether message at index i is a tool-call indicator.
// It checks the claudeMeta parallel slice (indexed by assistant message count, not i).
// For simplicity, we walk the messages to find the assistantIdx for message i.
func (m *Model) isToolCallIndicatorIdx(i int) bool {
	assistantIdx := 0
	for j := 0; j < i; j++ {
		if m.messages[j].Role == "assistant" {
			assistantIdx++
		}
	}
	return assistantIdx < len(m.claudeMeta) && m.claudeMeta[assistantIdx] == "tool-call-indicator"
}

// extractToolName extracts the tool/function name from a tool-call indicator
// message like "⚙ calling `function_name`…". Returns the name or a fallback.
func extractToolName(content string) string {
	// Look for backtick-delimited name.
	start := strings.Index(content, "`")
	if start >= 0 {
		end := strings.Index(content[start+1:], "`")
		if end >= 0 {
			return content[start+1 : start+1+end]
		}
	}
	return "tool call"
}
