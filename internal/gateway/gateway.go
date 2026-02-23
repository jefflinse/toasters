package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/job"
	"github.com/jefflinse/toasters/internal/llm"
)

// MaxSlots is the maximum number of concurrent Claude subprocess slots.
const MaxSlots = 16

// SlotStatus represents the lifecycle state of a slot.
type SlotStatus int

const (
	SlotRunning SlotStatus = iota
	SlotDone
)

// SlotTimeoutMsg is sent to the TUI when a slot's timeout fires.
type SlotTimeoutMsg struct{ SlotID int }

// SlotSnapshot is a lock-free copy of slot state for rendering.
type SlotSnapshot struct {
	Active         bool
	AgentName      string
	JobID          string
	Status         SlotStatus
	StartTime      time.Time
	EndTime        time.Time // zero if still running
	Output         string    // accumulated text output
	Summary        string
	Model          string
	Prompt         string
	InputTokens    int
	OutputTokens   int
	TurnCount      int
	StopReason     string
	PendingTool    string
	ExitSummary    string
	ThinkingOutput string
	SubagentOutput string
	SessionID      string
}

// slot is the internal mutable state (mutex-protected via Gateway).
type slot struct {
	agentName      string
	jobID          string
	status         SlotStatus
	startTime      time.Time
	endTime        time.Time
	output         strings.Builder
	cancel         context.CancelFunc
	summary        string             // one-sentence description of what the agent was asked to do
	model          string             // model name from the system/init event
	sessionID      string             // session ID from the system/init event
	prompt         string             // the full assembled prompt passed to claude
	resetTimer     chan time.Duration // signals the timer goroutine to reset
	inputTokens    int
	outputTokens   int
	turnCount      int
	stopReason     string
	pendingTool    string
	exitSummary    string
	thinkingOutput strings.Builder
	subagentOutput strings.Builder
}

// Gateway manages up to MaxSlots concurrent Claude subprocess slots.
type Gateway struct {
	mu             sync.Mutex
	slots          [MaxSlots]*slot
	claudeCfg      config.ClaudeConfig
	notify         func()               // called on every output update; wired to TUI re-render
	send           func(SlotTimeoutMsg) // called when a slot's timeout fires
	defaultTimeout time.Duration        // per-slot subprocess timeout
}

// New returns an initialized Gateway with all slots nil.
func New(claudeCfg config.ClaudeConfig, notify func()) *Gateway {
	timeout := time.Duration(claudeCfg.SlotTimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	return &Gateway{
		claudeCfg:      claudeCfg,
		notify:         notify,
		send:           func(SlotTimeoutMsg) {},
		defaultTimeout: timeout,
	}
}

// SpawnTeam starts a Claude subprocess for a team coordinator in a free slot.
// It returns the slot index, a boolean indicating whether the team was already
// running (idempotent duplicate call), and an error if no slot is available.
func (g *Gateway) SpawnTeam(teamName, jobID, task string, team agents.Team) (slotID int, alreadyRunning bool, err error) {
	g.mu.Lock()

	// Find a free slot: first nil slot, then first done slot.
	idx := -1
	for i, s := range g.slots {
		if s == nil {
			idx = i
			break
		}
	}
	if idx == -1 {
		for i, s := range g.slots {
			if s != nil && s.status == SlotDone {
				idx = i
				break
			}
		}
	}
	if idx == -1 {
		g.mu.Unlock()
		return -1, false, fmt.Errorf("all slots busy")
	}

	// Check for an already-running slot with the same team+job.
	for i, s := range g.slots {
		if s != nil && s.status == SlotRunning &&
			s.agentName == teamName && s.jobID == jobID {
			g.mu.Unlock()
			return i, true, nil
		}
	}

	g.mu.Unlock()

	// Build permission args from the team coordinator.
	var permissionArgs []string
	if team.Coordinator != nil {
		permissionArgs = team.Coordinator.ClaudePermissionArgs()
	} else {
		permissionArgs = []string{"--dangerously-skip-permissions"}
	}

	// Build --agents JSON from team workers.
	var agentsJSON string
	if len(team.Workers) > 0 {
		agentsMap := make(map[string]map[string]string, len(team.Workers))
		for _, w := range team.Workers {
			agentsMap[w.Name] = map[string]string{
				"description": w.Description,
				"prompt":      w.Body,
			}
		}
		if data, err := json.Marshal(agentsMap); err == nil {
			agentsJSON = string(data)
		}
	}

	// Read job context outside the lock (I/O).
	configDir, _ := config.Dir()
	jobDir := filepath.Join(job.JobsDir(configDir), jobID)
	overview, _ := job.ReadOverview(jobDir)
	todos, _ := job.ReadTodos(jobDir)

	// Assemble prompt.
	var sb strings.Builder
	sb.WriteString(agents.BuildTeamCoordinatorPrompt(team))
	sb.WriteString("\n\n---\n\n## Job Context\n\n### OVERVIEW.md\n")
	sb.WriteString(overview)
	sb.WriteString("\n\n### TODO.md\n")
	sb.WriteString(todos)

	// Inject BLOCKER.md if present — provides full blocker context and user responses.
	blockerPath := filepath.Join(jobDir, "BLOCKER.md")
	if blockerData, err := os.ReadFile(blockerPath); err == nil && len(blockerData) > 0 {
		sb.WriteString("\n\n## Blocker Resolution\n\nA blocker was previously encountered on this job. The following file contains the blocker details and the user's responses. Address the blocker using the provided answers before continuing with the job.\n\n")
		sb.WriteString(string(blockerData))
	}

	if task != "" {
		sb.WriteString("\n\n---\n\n## Task\n")
		sb.WriteString(task)
	}
	prompt := sb.String()

	// Build summary: "teamName on jobID" optionally with ": task", max 80 chars.
	summary := teamName + " on " + jobID
	if task != "" {
		summary += ": " + task
	}
	if len(summary) > 80 {
		summary = summary[:80]
	}

	// Create the slot and assign it.
	ctx, cancel := context.WithCancel(context.Background())
	s := &slot{
		agentName:  teamName,
		jobID:      jobID,
		status:     SlotRunning,
		startTime:  time.Now(),
		cancel:     cancel,
		summary:    summary,
		prompt:     prompt,
		resetTimer: make(chan time.Duration, 1),
	}

	g.mu.Lock()
	g.slots[idx] = s
	g.mu.Unlock()

	// Start the timer goroutine that fires SlotTimeoutMsg after defaultTimeout.
	go func() {
		timer := time.NewTimer(g.defaultTimeout)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				g.mu.Lock()
				stillRunning := g.slots[idx] == s && s.status == SlotRunning
				sendFn := g.send
				g.mu.Unlock()
				if !stillRunning {
					return
				}
				sendFn(SlotTimeoutMsg{SlotID: idx})
				// Wait for a reset signal or context cancellation.
				select {
				case d := <-s.resetTimer:
					timer.Reset(d)
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Start the subprocess goroutine.
	go func() {
		ch := spawnClaudeStream(ctx, prompt, g.claudeCfg, permissionArgs, agentsJSON)
		for resp := range ch {
			switch {
			case resp.Meta != nil:
				g.mu.Lock()
				s.model = resp.Meta.Model
				s.sessionID = resp.Meta.SessionID
				g.mu.Unlock()
				g.notify()
			case resp.Content != "":
				g.mu.Lock()
				s.output.WriteString(resp.Content)
				g.mu.Unlock()
				g.notify()
			case resp.Reasoning != "":
				g.mu.Lock()
				s.thinkingOutput.WriteString(resp.Reasoning)
				g.mu.Unlock()
				g.notify()
			case resp.PendingTool != "":
				g.mu.Lock()
				s.pendingTool = resp.PendingTool
				g.mu.Unlock()
				g.notify()
			case resp.ClearPendingTool:
				g.mu.Lock()
				s.pendingTool = ""
				g.mu.Unlock()
				g.notify()
			case resp.ExitSummary != "":
				g.mu.Lock()
				s.exitSummary = resp.ExitSummary
				g.mu.Unlock()
				g.notify()
			case resp.StopReason != "" || resp.Usage != nil:
				g.mu.Lock()
				if resp.StopReason != "" {
					s.stopReason = resp.StopReason
				}
				if resp.Usage != nil {
					if resp.Usage.PromptTokens > 0 {
						s.inputTokens += resp.Usage.PromptTokens
					}
					if resp.Usage.CompletionTokens > 0 {
						s.outputTokens += resp.Usage.CompletionTokens
						s.turnCount++
					}
				}
				g.mu.Unlock()
			case resp.Done || resp.Error != nil:
				g.mu.Lock()
				s.status = SlotDone
				s.endTime = time.Now()
				g.mu.Unlock()
				g.notify()
			}
		}

		// After stream closes, read REPORT.md from the job dir.
		reportPath := filepath.Join(jobDir, "REPORT.md")
		if reportData, err := os.ReadFile(reportPath); err == nil {
			g.mu.Lock()
			s.output.WriteString("\n\n---\n\n## Team Report\n\n")
			s.output.WriteString(string(reportData))
			g.mu.Unlock()
			g.notify()
		}
	}()

	return idx, false, nil
}

// SetNotify replaces the notify callback. Safe to call after New.
func (g *Gateway) SetNotify(fn func()) {
	g.mu.Lock()
	g.notify = fn
	g.mu.Unlock()
}

// SetSend replaces the send callback used to deliver SlotTimeoutMsg to the TUI.
func (g *Gateway) SetSend(fn func(SlotTimeoutMsg)) {
	g.mu.Lock()
	g.send = fn
	g.mu.Unlock()
}

// ExtendSlot resets the timeout timer for a running slot by another defaultTimeout duration.
func (g *Gateway) ExtendSlot(slotID int) error {
	if slotID < 0 || slotID >= MaxSlots {
		return fmt.Errorf("slot ID %d out of range (0-%d)", slotID, MaxSlots-1)
	}
	g.mu.Lock()
	s := g.slots[slotID]
	if s == nil || s.status != SlotRunning {
		g.mu.Unlock()
		return fmt.Errorf("slot %d is not running", slotID)
	}
	d := g.defaultTimeout
	g.mu.Unlock()
	select {
	case s.resetTimer <- d:
	default:
	}
	return nil
}

// Slots returns a snapshot of all slot states.
func (g *Gateway) Slots() [MaxSlots]SlotSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()

	var snapshots [MaxSlots]SlotSnapshot
	for i, s := range g.slots {
		if s == nil {
			snapshots[i] = SlotSnapshot{Active: false}
			continue
		}
		snapshots[i] = SlotSnapshot{
			Active:         true,
			AgentName:      s.agentName,
			JobID:          s.jobID,
			Status:         s.status,
			StartTime:      s.startTime,
			EndTime:        s.endTime,
			Output:         s.output.String(),
			Summary:        s.summary,
			Model:          s.model,
			Prompt:         s.prompt,
			InputTokens:    s.inputTokens,
			OutputTokens:   s.outputTokens,
			TurnCount:      s.turnCount,
			StopReason:     s.stopReason,
			PendingTool:    s.pendingTool,
			ExitSummary:    s.exitSummary,
			ThinkingOutput: s.thinkingOutput.String(),
			SubagentOutput: s.subagentOutput.String(),
			SessionID:      s.sessionID,
		}
	}
	return snapshots
}

// Dismiss clears a completed slot so it can be reused. Returns an error if
// the slot is out of range or still running.
func (g *Gateway) Dismiss(slotID int) error {
	if slotID < 0 || slotID >= MaxSlots {
		return fmt.Errorf("slot ID %d out of range (0-%d)", slotID, MaxSlots-1)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	s := g.slots[slotID]
	if s == nil || s.status == SlotRunning {
		return fmt.Errorf("cannot dismiss a running slot")
	}

	g.slots[slotID] = nil
	return nil
}

// SlotSummaries returns a summary of all non-idle slots for operator visibility.
func (g *Gateway) SlotSummaries() []llm.GatewaySlot {
	snapshots := g.Slots()
	var summaries []llm.GatewaySlot
	for i, snap := range snapshots {
		if !snap.Active {
			continue
		}
		elapsed := ""
		if !snap.StartTime.IsZero() {
			if snap.EndTime.IsZero() {
				elapsed = time.Since(snap.StartTime).Round(time.Second).String()
			} else {
				elapsed = snap.EndTime.Sub(snap.StartTime).Round(time.Second).String()
			}
		}
		status := "running"
		if snap.Status == SlotDone {
			status = "done"
		}
		summaries = append(summaries, llm.GatewaySlot{
			Index:   i,
			Team:    snap.AgentName,
			JobID:   snap.JobID,
			Status:  status,
			Elapsed: elapsed,
		})
	}
	return summaries
}

// Kill cancels a running slot's subprocess and marks it done.
// Returns an error if the slot is out of range or not running.
func (g *Gateway) Kill(slotID int) error {
	if slotID < 0 || slotID >= MaxSlots {
		return fmt.Errorf("slot ID %d out of range (0-%d)", slotID, MaxSlots-1)
	}

	g.mu.Lock()
	s := g.slots[slotID]
	if s == nil || s.status != SlotRunning {
		g.mu.Unlock()
		return fmt.Errorf("slot %d is not running", slotID)
	}
	s.cancel()
	s.status = SlotDone
	s.endTime = time.Now()
	s.output.WriteString("\n[killed]")
	g.mu.Unlock() // unlock BEFORE calling notify to avoid deadlock

	g.notify()
	return nil
}

// --- Claude subprocess streaming (inlined from internal/tui/claude.go) ---

// claudeInitEvent is the first line emitted by `claude --output-format stream-json`.
type claudeInitEvent struct {
	Type              string `json:"type"`
	Subtype           string `json:"subtype"`
	Model             string `json:"model"`
	PermissionMode    string `json:"permissionMode"`
	ClaudeCodeVersion string `json:"claude_code_version"`
	SessionID         string `json:"session_id"`
}

// claudeInnerEvent is the inner event shape nested inside stream_event wrappers.
type claudeInnerEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Message struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"content_block"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// claudeContentBlock is one element of an assistant message's content array.
type claudeContentBlock struct {
	Type     string `json:"type"`     // "text", "tool_use", or "thinking"
	Text     string `json:"text"`     // for type="text"
	Name     string `json:"name"`     // for type="tool_use"
	Input    any    `json:"input"`    // for type="tool_use"
	Thinking string `json:"thinking"` // for type="thinking"
}

// claudeAssistantMessage is the message field inside a top-level "assistant" event.
type claudeAssistantMessage struct {
	Content []claudeContentBlock `json:"content"`
}

// claudeToolResultBlock is one element of a user message's content array,
// carrying the result of a tool call back to the model.
type claudeToolResultBlock struct {
	Type      string          `json:"type"`        // "tool_result"
	ToolUseID string          `json:"tool_use_id"` // matches the tool_use block ID
	Content   json.RawMessage `json:"content"`     // string or []content_block
}

// claudeUserMessage is the message field inside a top-level "user" event,
// typically carrying tool results from subagent calls.
type claudeUserMessage struct {
	Role    string                  `json:"role"`
	Content []claudeToolResultBlock `json:"content"`
}

// claudeOuterEvent is the top-level shape of a JSON line emitted by
// `claude --output-format stream-json`.
type claudeOuterEvent struct {
	Type    string                 `json:"type"`
	Event   claudeInnerEvent       `json:"event"`
	Message claudeAssistantMessage `json:"message"` // for type="assistant"
	Result  string                 `json:"result"`
	IsError bool                   `json:"is_error"`
}

// claudeUserOuterEvent is the top-level shape for type="user" events, which
// carry tool results back from subagent calls.
type claudeUserOuterEvent struct {
	Type    string            `json:"type"`
	Message claudeUserMessage `json:"message"`
}

// spawnClaudeStream launches the claude CLI as a subprocess and returns a
// channel that delivers streamed response chunks. The channel is closed when
// the stream ends, either normally or due to an error.
//
// permissionArgs are per-agent Claude CLI permission flags (e.g.
// ["--dangerously-skip-permissions"] or ["--allowedTools", "Read,Bash,..."]).
// If non-empty they take precedence over claudeCfg.PermissionMode.
func spawnClaudeStream(ctx context.Context, prompt string, claudeCfg config.ClaudeConfig, permissionArgs []string, agentsJSON string) <-chan llm.StreamResponse {
	ch := make(chan llm.StreamResponse, 64)

	go func() {
		defer close(ch)

		args := []string{
			"--print",
			"--output-format", "stream-json",
			"--include-partial-messages",
		}
		if claudeCfg.DefaultModel != "" {
			args = append(args, "--model", claudeCfg.DefaultModel)
		}
		if agentsJSON != "" {
			args = append(args, "--agents", agentsJSON)
		}
		// Use per-agent permission args if provided, otherwise fall back to config.
		if len(permissionArgs) > 0 {
			args = append(args, permissionArgs...)
		} else if claudeCfg.PermissionMode != "" {
			args = append(args, "--permission-mode", claudeCfg.PermissionMode)
		} else {
			args = append(args, "--dangerously-skip-permissions")
		}

		// Always pass the prompt via stdin rather than as a positional argument.
		// --allowedTools is greedy and consumes subsequent positional args as tool
		// names, so stdin is the only safe delivery mechanism regardless of which
		// permission flags are in use.
		cmd := exec.CommandContext(ctx, claudeCfg.Path, args...)
		cmd.Stdin = strings.NewReader(prompt)

		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			ch <- llm.StreamResponse{Error: fmt.Errorf("opening claude stderr pipe: %w", err)}
			return
		}

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			ch <- llm.StreamResponse{Error: fmt.Errorf("opening claude stdout pipe: %w", err)}
			return
		}

		if err := cmd.Start(); err != nil {
			ch <- llm.StreamResponse{Error: fmt.Errorf("starting claude: %w", err)}
			return
		}

		var stderrBuf strings.Builder
		var stderrWg sync.WaitGroup
		stderrWg.Add(1)
		go func() {
			defer stderrWg.Done()
			io.Copy(&stderrBuf, stderrPipe)
		}()

		done := false
		firstLine := true
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			// The very first non-empty line is always the system/init event.
			if firstLine {
				firstLine = false
				var init claudeInitEvent
				if err := json.Unmarshal([]byte(line), &init); err == nil &&
					init.Type == "system" && init.Subtype == "init" {
					ch <- llm.StreamResponse{Meta: &llm.ClaudeMeta{
						Model:          init.Model,
						PermissionMode: init.PermissionMode,
						Version:        init.ClaudeCodeVersion,
						SessionID:      init.SessionID,
					}}
					continue
				}
				// Not a system/init line — fall through to normal parsing below.
			}

			var event claudeOuterEvent
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				// Malformed line — skip silently.
				continue
			}

			switch event.Type {
			case "stream_event":
				switch event.Event.Type {
				case "content_block_delta":
					// Only capture streaming text deltas — tool input deltas are
					// assembled server-side and surfaced via the "assistant" event.
					if event.Event.Delta.Type == "text_delta" && event.Event.Delta.Text != "" {
						ch <- llm.StreamResponse{Content: event.Event.Delta.Text}
					}
				case "content_block_start":
					// Notify when a tool_use block begins so the TUI can show which
					// tool is currently executing.
					if event.Event.ContentBlock.Type == "tool_use" && event.Event.ContentBlock.Name != "" {
						ch <- llm.StreamResponse{PendingTool: event.Event.ContentBlock.Name}
					}
				case "content_block_stop":
					// Clear the pending tool signal.
					ch <- llm.StreamResponse{ClearPendingTool: true}
				case "message_start":
					// Accumulate input token count from the opening message event.
					if event.Event.Message.Usage.InputTokens > 0 {
						ch <- llm.StreamResponse{Usage: &llm.Usage{
							PromptTokens: event.Event.Message.Usage.InputTokens,
						}}
					}
				case "message_delta":
					// Accumulate output tokens and capture stop reason.
					if event.Event.Delta.StopReason != "" || event.Event.Usage.OutputTokens > 0 {
						ch <- llm.StreamResponse{
							StopReason: event.Event.Delta.StopReason,
							Usage: &llm.Usage{
								CompletionTokens: event.Event.Usage.OutputTokens,
							},
						}
					}
				case "message_stop":
					// No-op: message_stop just signals end of a turn; handled by
					// the "assistant" event that follows.
				}
			case "assistant":
				// Top-level assistant event: emitted after each model turn with
				// the full accumulated message. Extract text blocks and emit
				// tool-call summaries so slot output shows what the agent did.
				for _, block := range event.Message.Content {
					switch block.Type {
					case "text":
						if block.Text != "" {
							// Text already streamed via stream_event deltas above;
							// emit two newlines to separate turns cleanly.
							ch <- llm.StreamResponse{Content: "\n\n"}
						}
					case "tool_use":
						ch <- llm.StreamResponse{Content: "\n" + formatToolUse(block.Name, block.Input) + "\n"}
					case "thinking":
						if block.Thinking != "" {
							ch <- llm.StreamResponse{Reasoning: block.Thinking}
						}
					}
				}
			case "user":
				// User events carry tool results back from subagent calls.
				var userEvent claudeUserOuterEvent
				if err := json.Unmarshal([]byte(line), &userEvent); err == nil {
					for _, block := range userEvent.Message.Content {
						if block.Type != "tool_result" {
							continue
						}
						// Content can be a plain string or an array of content blocks.
						var text string
						if err := json.Unmarshal(block.Content, &text); err != nil {
							// Try array of {type, text} blocks.
							var blocks []struct {
								Type string `json:"type"`
								Text string `json:"text"`
							}
							if err := json.Unmarshal(block.Content, &blocks); err == nil {
								var sb strings.Builder
								for _, b := range blocks {
									if b.Type == "text" && b.Text != "" {
										sb.WriteString(b.Text)
									}
								}
								text = sb.String()
							}
						}
						if text != "" {
							ch <- llm.StreamResponse{Content: "\n[subagent result]\n" + text + "\n"}
						}
					}
				}
			case "result":
				done = true
				if event.IsError {
					ch <- llm.StreamResponse{Error: fmt.Errorf("claude error: %s", event.Result)}
				} else {
					if event.Result != "" {
						ch <- llm.StreamResponse{ExitSummary: event.Result}
					}
					ch <- llm.StreamResponse{Done: true}
				}
			}
		}

		stderrWg.Wait() // ensure stderr is fully read before Wait
		_ = cmd.Wait()

		if stderrStr := strings.TrimSpace(stderrBuf.String()); stderrStr != "" {
			ch <- llm.StreamResponse{Content: "\n[stderr]: " + stderrStr}
		}

		if !done {
			ch <- llm.StreamResponse{Done: true}
		}
	}()

	return ch
}

// formatToolUse returns a compact one-line annotation for a Claude tool call,
// surfacing the most useful parameter for each known tool.
func formatToolUse(name string, input any) string {
	m, _ := input.(map[string]any)

	switch name {
	case "Read":
		if p, _ := m["file_path"].(string); p != "" {
			return fmt.Sprintf("[tool: Read] %s", p)
		}
	case "Write":
		if p, _ := m["file_path"].(string); p != "" {
			return fmt.Sprintf("[tool: Write] %s", p)
		}
	case "Edit", "MultiEdit":
		if p, _ := m["file_path"].(string); p != "" {
			return fmt.Sprintf("[tool: Edit] %s", p)
		}
	case "Bash":
		if cmd, _ := m["command"].(string); cmd != "" {
			if len(cmd) > 72 {
				cmd = cmd[:72] + "…"
			}
			return fmt.Sprintf("[tool: Bash] %s", cmd)
		}
	case "Task":
		if desc, _ := m["description"].(string); desc != "" {
			return fmt.Sprintf("[tool: Task] %s", desc)
		}
	case "Glob":
		if p, _ := m["pattern"].(string); p != "" {
			return fmt.Sprintf("[tool: Glob] %s", p)
		}
	case "Grep":
		if p, _ := m["pattern"].(string); p != "" {
			return fmt.Sprintf("[tool: Grep] %s", p)
		}
	case "WebFetch":
		if u, _ := m["url"].(string); u != "" {
			return fmt.Sprintf("[tool: WebFetch] %s", u)
		}
	case "TodoWrite":
		return "[tool: TodoWrite]"
	case "TodoRead":
		return "[tool: TodoRead]"
	case "LS":
		if p, _ := m["path"].(string); p != "" {
			return fmt.Sprintf("[tool: LS] %s", p)
		}
	}

	return fmt.Sprintf("[tool: %s]", name)
}
