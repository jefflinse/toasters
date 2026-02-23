package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"

	"github.com/jefflinse/toasters/internal/agents"
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

// focusedPanel identifies which panel currently holds keyboard focus.
type focusedPanel int

const (
	focusChat   focusedPanel = iota
	focusJobs   focusedPanel = iota
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
}

// loadingTickMsg drives the loading screen animation.
type loadingTickMsg struct{}

// loadingTick returns a command that fires loadingTickMsg after 150ms.
func loadingTick() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
		return loadingTickMsg{}
	})
}

// loadingFrames are the spinner characters that orbit the 🍞 emoji.
var loadingFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// numLoadingFrames is the total number of animation frames.
var numLoadingFrames = len(loadingFrames)

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
}

// SlotTimeoutPromptExpiredMsg fires when the 1-minute user-response window elapses.
type SlotTimeoutPromptExpiredMsg struct{ SlotID int }

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

	jobs        []job.Job
	selectedJob int
	focused     focusedPanel

	gateway *gateway.Gateway

	teams        []agents.Team // available teams
	teamsDir     string        // path to the configured teams directory
	awareness    string        // team-awareness content used to build the operator prompt
	systemPrompt string        // assembled at startup; prepended to every LLM call
	repoRoot     string        // path to repo root (for /claude slash command path)

	// Teams modal state.
	teamsModal teamsModalState

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

	userScrolled bool // true when user has manually scrolled up; suppresses auto-scroll

	// prevSlotActive/Status track the last-seen state of each gateway slot so
	// AgentOutputMsg can detect Running→Done transitions and notify the operator.
	prevSlotActive [gateway.MaxSlots]bool
	prevSlotStatus [gateway.MaxSlots]gateway.SlotStatus

	// Collapsible completion message state.
	completionMsgIdx map[int]bool // indices of team-completion messages in m.messages
	expandedMsgs     map[int]bool // which completion messages are currently expanded
	selectedMsgIdx   int          // currently selected message index (-1 = none)

	// Collapsible reasoning (thinking) state.
	expandedReasoning map[int]bool // which assistant message indices have reasoning expanded
}

// NewModel returns an initialized root model.
func NewModel(client *llm.Client, claudeCfg config.ClaudeConfig, configDir string, gw *gateway.Gateway, repoRoot string, teamsDir string, teams []agents.Team, awareness string) Model {
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

	jobs, _ := job.List(configDir)
	m.jobs = jobs
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
			// Cycle focus: chat → jobs → agents → chat.
			// (Tab inside the slash command popup is handled above and returns early.)
			switch m.focused {
			case focusChat:
				m.focused = focusJobs
				m.input.Blur()
				return m, nil
			case focusJobs:
				m.focused = focusAgents
				return m, nil
			case focusAgents:
				m.focused = focusChat
				return m, m.input.Focus()
			}

		case "up":
			// Navigate jobs when that panel is focused.
			if m.focused == focusJobs && len(m.jobs) > 0 {
				if m.selectedJob > 0 {
					m.selectedJob--
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
			// Navigate completion messages when chat is focused and not streaming.
			if m.focused == focusChat && !m.streaming && len(m.completionMsgIdx) > 0 {
				if m.selectedMsgIdx > 0 {
					m.selectedMsgIdx--
				}
				m.updateViewportContent()
				return m, nil
			}

		case "down":
			// Navigate jobs when that panel is focused.
			if m.focused == focusJobs && len(m.jobs) > 0 {
				if m.selectedJob < len(m.jobs)-1 {
					m.selectedJob++
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
			// Navigate completion messages when chat is focused and not streaming.
			if m.focused == focusChat && !m.streaming && len(m.completionMsgIdx) > 0 {
				if m.selectedMsgIdx < len(m.messages)-1 {
					m.selectedMsgIdx++
				}
				m.updateViewportContent()
				return m, nil
			}

		case "x":
			// Toggle expand/collapse on the selected completion message when chat is focused.
			if m.focused == focusChat && !m.streaming && m.selectedMsgIdx >= 0 && m.completionMsgIdx[m.selectedMsgIdx] {
				m.expandedMsgs[m.selectedMsgIdx] = !m.expandedMsgs[m.selectedMsgIdx]
				m.updateViewportContent()
				return m, nil
			}

		case "t":
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
		}
		cmds = append(cmds, m.input.Focus())

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
		if !m.userScrolled {
			m.chatViewport.GotoBottom()
		}

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
			m.reasoning = append(m.reasoning, "")
			m.claudeMeta = append(m.claudeMeta, "tool-call-indicator")
			m.messages = append(m.messages, llm.Message{Role: "tool", Content: result, ToolCallID: m.promptPendingCall.ID})
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
				m.reasoning = append(m.reasoning, "")
				m.claudeMeta = append(m.claudeMeta, "tool-call-indicator")
				m.messages = append(m.messages, llm.Message{Role: "tool", Content: result, ToolCallID: m.pendingDispatch.ID})
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
				m.reasoning = append(m.reasoning, "")
				m.claudeMeta = append(m.claudeMeta, "tool-call-indicator")
				m.messages = append(m.messages, llm.Message{Role: "tool", Content: result, ToolCallID: m.pendingDispatch.ID})
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
				m.reasoning = append(m.reasoning, "")
				m.claudeMeta = append(m.claudeMeta, "tool-call-indicator")
				m.messages = append(m.messages, llm.Message{Role: "tool", Content: "User cancelled the dispatch.", ToolCallID: m.pendingDispatch.ID})
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
		m.reasoning = append(m.reasoning, "")
		m.claudeMeta = append(m.claudeMeta, "tool-call-indicator")
		// Then: the tool result.
		m.messages = append(m.messages, llm.Message{
			Role:       "tool",
			Content:    msg.Result,
			ToolCallID: msg.Call.ID,
		})
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
		m.awareness = msg.Awareness
		m.systemPrompt = agents.BuildOperatorPrompt(m.teams, m.awareness)
		llm.SetTeams(m.teams)
		if m.hasConversation() {
			m.messages[0].Content = m.systemPrompt
		} else {
			m.initMessages()
		}
		return m, tea.Batch(cmds...)

	case JobsReloadedMsg:
		m.jobs = msg.Jobs
		if m.selectedJob >= len(m.jobs) {
			if len(m.jobs) > 0 {
				m.selectedJob = len(m.jobs) - 1
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
				if wasRunning && isDone && !m.streaming {
					// Build a concise completion notification for the operator.
					outputTail := snap.Output
					const maxTail = 2000
					if len(outputTail) > maxTail {
						outputTail = "…" + outputTail[len(outputTail)-maxTail:]
					}
					notification := fmt.Sprintf(
						"Team '%s' in slot %d has completed (job: %s).\n\nOutput:\n%s",
						snap.AgentName, i, snap.JobID, outputTail,
					)
					m.messages = append(m.messages, llm.Message{
						Role:    "user",
						Content: notification,
					})
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

	case tea.MouseWheelMsg:
		// Forward mouse wheel events to viewport for scroll support.
		var cmd tea.Cmd
		m.chatViewport, cmd = m.chatViewport.Update(msg)
		cmds = append(cmds, cmd)
		// Track whether user has scrolled away from the bottom.
		if m.chatViewport.AtBottom() {
			m.userScrolled = false
		} else {
			m.userScrolled = true
		}

	case loadingTickMsg:
		if m.loading {
			m.loadingFrame = (m.loadingFrame + 1) % numLoadingFrames
			return m, loadingTick()
		}
		return m, nil
	}

	return m, tea.Batch(cmds...)
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
		header := fmt.Sprintf("⬡ %s · %s", snap.AgentName, snap.JobID)
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
		if m.promptMode {
			inputOrStatus = m.renderPromptWidget(mainWidth)
		} else {
			inputOrStatus = InputAreaStyle.Width(mainWidth).Render(m.input.View())
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

// renderLoading renders a centered animated loading screen while the app is initializing.
func (m *Model) renderLoading() tea.View {
	spinnerStyle := lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	breadStyle := lipgloss.NewStyle().Foreground(ColorStreaming).Bold(true)
	msgStyle := DimStyle.Italic(true)

	spinner := spinnerStyle.Render(loadingFrames[m.loadingFrame%numLoadingFrames])
	bread := breadStyle.Render("🍞")

	// Cycle the status message every 4 frames (~600ms).
	msgIdx := (m.loadingFrame / 4) % len(loadingMessages)
	statusMsg := msgStyle.Render(loadingMessages[msgIdx])

	// Compose: spinner + bread on one line, message below.
	spinLine := spinner + "  " + bread
	block := lipgloss.JoinVertical(lipgloss.Center, spinLine, "", statusMsg)

	content := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, block)
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

// renderPromptWidget renders the prompt mode input area, replacing the normal textarea.
// In option-selection mode (promptCustom == false) it shows a numbered list of choices.
// In custom-text mode (promptCustom == true) it shows the question above the textarea.
func (m Model) renderPromptWidget(width int) string {
	if m.promptCustom {
		// Custom text mode: question header above the normal textarea.
		question := HeaderStyle.Render("? " + m.promptQuestion)
		hint := DimStyle.Render("Enter to submit · Esc to go back")
		inner := lipgloss.JoinVertical(lipgloss.Left, question, m.input.View(), hint)
		return InputAreaStyle.Width(width).Render(inner)
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
	return InputAreaStyle.Width(width).Render(inner)
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
	for i, msg := range m.messages {
		switch msg.Role {
		case "user":
			// Completion messages render as collapsible blocks.
			if m.completionMsgIdx[i] {
				firstLine := firstLineOf(msg.Content)
				if m.expandedMsgs[i] {
					hint := ""
					if i == m.selectedMsgIdx {
						hint = DimStyle.Render(" [x to collapse]")
					}
					header := DimStyle.Render("▼ "+firstLine) + hint
					sb.WriteString(header + "\n" + renderCompletionBlock(msg.Content) + "\n")
				} else {
					hint := ""
					if i == m.selectedMsgIdx {
						hint = DimStyle.Render(" [x to expand]")
					}
					sb.WriteString(DimStyle.Render("▶ "+firstLine) + hint + "\n\n")
				}
				continue
			}
			blockWidth := contentWidth - UserMsgBlockStyle.GetHorizontalFrameSize()
			if blockWidth < 1 {
				blockWidth = 1
			}
			block := UserMsgBlockStyle.Width(blockWidth).Render(wrapText(msg.Content, blockWidth))
			sb.WriteString(block + "\n\n")
		case "assistant":
			// ask-user-prompt and escalate-prompt messages render as a styled question header.
			if assistantIdx < len(m.claudeMeta) && (m.claudeMeta[assistantIdx] == "ask-user-prompt" || m.claudeMeta[assistantIdx] == "escalate-prompt") {
				sb.WriteString(HeaderStyle.Render("? "+msg.Content) + "\n\n")
				assistantIdx++
				continue
			}
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
			// Render reasoning trace (if any) above the response — only when expanded.
			if assistantIdx < len(m.reasoning) && m.reasoning[assistantIdx] != "" {
				if m.expandedReasoning[assistantIdx] {
					sb.WriteString(renderReasoningBlock(m.reasoning[assistantIdx], contentWidth))
					sb.WriteString("\n")
				} else {
					sb.WriteString(ReasoningStyle.Render("▶ thinking (press t to expand)") + "\n\n")
				}
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

	// --- Top pane: Jobs ---
	var topLines []string
	topLines = append(topLines, LeftPanelHeaderStyle.Render("Jobs"))
	if len(m.jobs) == 0 {
		topLines = append(topLines, PlaceholderPaneStyle.Render("No jobs"))
	} else {
		for i, j := range m.jobs {
			name := truncateStr(j.Name, contentWidth-3)
			if i == m.selectedJob {
				topLines = append(topLines, JobSelectedStyle.Render("🍞 "+name))
			} else {
				topLines = append(topLines, JobItemStyle.Render("   "+name))
			}
		}
	}
	topPane := lipgloss.NewStyle().Height(topH).Render(
		lipgloss.JoinVertical(lipgloss.Left, topLines...),
	)

	// --- Middle pane: DAG ---
	selectedName := ""
	if len(m.jobs) > 0 && m.selectedJob < len(m.jobs) {
		selectedName = m.jobs[m.selectedJob].Name
	}
	middlePane := lipgloss.NewStyle().Height(middleH).Render(
		lipgloss.JoinVertical(lipgloss.Left,
			LeftPanelHeaderStyle.Render(truncateStr(selectedName, contentWidth)),
			PlaceholderPaneStyle.Render("—"),
		),
	)

	// --- Bottom pane: Teams ---
	var bottomLines []string
	bottomLines = append(bottomLines, LeftPanelHeaderStyle.Render("Teams"))
	if len(m.teams) == 0 {
		bottomLines = append(bottomLines, PlaceholderPaneStyle.Render("No teams configured"))
	} else {
		for _, t := range m.teams {
			teamColor := lipgloss.Color("135")
			if t.Coordinator != nil && t.Coordinator.Color != "" {
				teamColor = lipgloss.Color(t.Coordinator.Color)
			}
			prefix := lipgloss.NewStyle().Foreground(teamColor).Render("◆") + " "
			workerCount := fmt.Sprintf("(%d workers)", len(t.Workers))
			name := truncateStr(t.Name, contentWidth-2)
			line := SidebarValueStyle.Bold(true).Render(prefix+name) + " " + DimStyle.Render(workerCount)
			bottomLines = append(bottomLines, line)
		}
	}
	bottomPane := lipgloss.NewStyle().Height(bottomH).Render(
		lipgloss.JoinVertical(lipgloss.Left, bottomLines...),
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
			label := snap.AgentName + " · " + snap.JobID
			var statusIcon string
			if snap.Status == gateway.SlotRunning {
				statusIcon = "▶ "
			} else {
				statusIcon = "✓ "
			}
			line := statusIcon + truncateStr(label, contentWidth-2)
			if m.focused == focusAgents && i == m.selectedAgentSlot {
				sb.WriteString(JobSelectedStyle.Render("🍞 " + truncateStr(label, contentWidth-3)))
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

		// Aggregated agent token stats.
		var totalAgentIn, totalAgentOut int
		for _, snap := range slots {
			totalAgentIn += snap.InputTokens
			totalAgentOut += snap.OutputTokens
		}
		if totalAgentIn > 0 || totalAgentOut > 0 {
			sb.WriteString("\n")
			sb.WriteString(sidebarRow("Agent ↑ tok", compactNum(totalAgentIn)))
			sb.WriteString(sidebarRow("Agent ↓ tok", compactNum(totalAgentOut)))
			for i, snap := range slots {
				if snap.InputTokens > 0 || snap.OutputTokens > 0 {
					perSlot := fmt.Sprintf("  s%d: ↑%s ↓%s", i, compactNum(snap.InputTokens), compactNum(snap.OutputTokens))
					sb.WriteString(DimStyle.Render(truncateStr(perSlot, contentWidth)))
					sb.WriteString("\n")
				}
			}
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

		var borderColor color.Color
		if focused {
			borderColor = ColorPrimary
		} else {
			borderColor = ColorBorder
		}
		var headerStyle lipgloss.Style
		if focused {
			headerStyle = HeaderStyle
		} else {
			headerStyle = SidebarHeaderStyle
		}

		cellStyle := lipgloss.NewStyle().
			Width(cellW).
			Height(cellH).
			Border(lipgloss.RoundedBorder()).
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
		//   3 prompt + 1 p-hint(focused) + 1 separator
		// = 8 (unfocused, no token) / 9 (unfocused, token) /
		//   9 (focused, no token) / 10 (focused, token)
		metaLines := 7 // header + summary + model + 3 prompt + separator
		if focused {
			metaLines++ // p-hint line
		}
		if tokenLine != "" {
			metaLines++ // token line
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

		// SubagentOutput indicator
		if snap.SubagentOutput != "" {
			subLine := DimStyle.Render(truncateStr(
				fmt.Sprintf("[subagent: %s chars]", compactNum(len(snap.SubagentOutput))),
				innerW,
			))
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
	if !m.userScrolled {
		m.chatViewport.GotoBottom()
	}
}

// newSession resets the conversation and all session statistics.
// initMessages resets m.messages and seeds it with the system prompt as message[0]
// (if a system prompt is set). Call this at startup and on /new.
func (m *Model) initMessages() {
	m.messages = nil
	if m.systemPrompt != "" {
		m.messages = []llm.Message{{Role: "system", Content: m.systemPrompt}}
	}
	m.completionMsgIdx = make(map[int]bool)
	m.expandedMsgs = make(map[int]bool)
	m.selectedMsgIdx = -1
	m.expandedReasoning = make(map[int]bool)
	m.confirmDispatch = false
	m.changingTeam = false
	m.pendingDispatch = llm.ToolCall{}
	m.confirmKill = false
	m.pendingKillSlot = 0
	m.confirmTimeout = false
	m.pendingTimeoutSlot = 0
}

// hasConversation reports whether the conversation contains at least one user or
// assistant message (i.e. the welcome screen should be hidden).
func (m *Model) hasConversation() bool {
	for _, msg := range m.messages {
		if msg.Role == "user" || msg.Role == "assistant" {
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
	return func() tea.Msg {
		ch := client.ChatCompletionStreamWithTools(ctx, msgs, llm.AvailableTools, temperature)
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
	m.userScrolled = false

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
	m.stats.MessageCount++
	m.streaming = true
	m.currentResponse = ""
	m.currentReasoning = ""
	m.err = nil
	m.userScrolled = false
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
