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

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/llm"
	"github.com/jefflinse/toasters/internal/workeffort"
)

// MaxSlots is the maximum number of concurrent Claude subprocess slots.
const MaxSlots = 4

// SlotStatus represents the lifecycle state of a slot.
type SlotStatus int

const (
	SlotRunning SlotStatus = iota
	SlotDone
)

// SlotSnapshot is a lock-free copy of slot state for rendering.
type SlotSnapshot struct {
	Active       bool
	AgentName    string
	WorkEffortID string
	Status       SlotStatus
	StartTime    time.Time
	EndTime      time.Time // zero if still running
	Output       string    // accumulated text output
	Summary      string
	Model        string
	Prompt       string
}

// slot is the internal mutable state (mutex-protected via Gateway).
type slot struct {
	agentName    string
	workEffortID string
	status       SlotStatus
	startTime    time.Time
	endTime      time.Time
	output       strings.Builder
	cancel       context.CancelFunc
	summary      string // one-sentence description of what the agent was asked to do
	model        string // model name from the system/init event
	prompt       string // the full assembled prompt passed to claude
}

// Gateway manages up to MaxSlots concurrent Claude subprocess slots.
type Gateway struct {
	mu             sync.Mutex
	slots          [MaxSlots]*slot
	claudeCfg      config.ClaudeConfig
	repoRoot       string        // path to repo root (agents/ dir is repoRoot/agents/)
	notify         func()        // called on every output update; wired to TUI re-render
	defaultTimeout time.Duration // per-slot subprocess timeout
}

// New returns an initialized Gateway with all slots nil.
func New(claudeCfg config.ClaudeConfig, repoRoot string, notify func()) *Gateway {
	return &Gateway{
		claudeCfg:      claudeCfg,
		repoRoot:       repoRoot,
		notify:         notify,
		defaultTimeout: 5 * time.Minute,
	}
}

// Spawn starts a new Claude subprocess in a free slot. It returns the slot
// index on success, or -1 and an error if no slot is available.
func (g *Gateway) Spawn(agentName, workEffortID, task string) (int, error) {
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
		return -1, fmt.Errorf("all slots busy")
	}

	// Read agent file.
	agentPath := filepath.Join(g.repoRoot, "agents", agentName+".md")
	agentData, err := os.ReadFile(agentPath)
	if err != nil {
		g.mu.Unlock()
		return -1, fmt.Errorf("reading agent file %s: %w", agentPath, err)
	}

	g.mu.Unlock()

	// Read work effort context outside the lock (I/O).
	configDir, _ := config.Dir()
	weDir := filepath.Join(workeffort.WorkEffortsDir(configDir), workEffortID)
	overview, _ := workeffort.ReadOverview(weDir)
	todos, _ := workeffort.ReadTodos(weDir)

	// Assemble prompt.
	var sb strings.Builder
	sb.WriteString(string(agentData))
	sb.WriteString("\n\n---\n\n## Work Effort Context\n\n### OVERVIEW.md\n")
	sb.WriteString(overview)
	sb.WriteString("\n\n### TODO.md\n")
	sb.WriteString(todos)
	if task != "" {
		sb.WriteString("\n\n---\n\n## Task\n")
		sb.WriteString(task)
	}
	prompt := sb.String()

	// Build summary: "agentName on workEffortID" optionally with ": task", max 80 chars.
	summary := agentName + " on " + workEffortID
	if task != "" {
		summary += ": " + task
	}
	if len(summary) > 80 {
		summary = summary[:80]
	}

	// Create the slot and assign it.
	ctx, cancel := context.WithTimeout(context.Background(), g.defaultTimeout)
	s := &slot{
		agentName:    agentName,
		workEffortID: workEffortID,
		status:       SlotRunning,
		startTime:    time.Now(),
		cancel:       cancel,
		summary:      summary,
		prompt:       prompt,
	}

	g.mu.Lock()
	g.slots[idx] = s
	g.mu.Unlock()

	// Start the subprocess goroutine.
	go func() {
		ch := spawnClaudeStream(ctx, prompt, g.claudeCfg)
		for resp := range ch {
			switch {
			case resp.Meta != nil:
				g.mu.Lock()
				s.model = resp.Meta.Model
				g.mu.Unlock()
			case resp.Content != "":
				g.mu.Lock()
				s.output.WriteString(resp.Content)
				g.mu.Unlock()
				g.notify()
			case resp.Done || resp.Error != nil:
				g.mu.Lock()
				s.status = SlotDone
				s.endTime = time.Now()
				g.mu.Unlock()
				g.notify()
			}
		}
	}()

	return idx, nil
}

// SetNotify replaces the notify callback. Safe to call after New.
func (g *Gateway) SetNotify(fn func()) {
	g.mu.Lock()
	g.notify = fn
	g.mu.Unlock()
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
			Active:       true,
			AgentName:    s.agentName,
			WorkEffortID: s.workEffortID,
			Status:       s.status,
			StartTime:    s.startTime,
			EndTime:      s.endTime,
			Output:       s.output.String(),
			Summary:      s.summary,
			Model:        s.model,
			Prompt:       s.prompt,
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
}

// claudeInnerEvent is the inner event shape nested inside stream_event wrappers.
type claudeInnerEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

// claudeOuterEvent is the top-level shape of a JSON line emitted by
// `claude --output-format stream-json`.
type claudeOuterEvent struct {
	Type    string           `json:"type"`
	Event   claudeInnerEvent `json:"event"`
	Result  string           `json:"result"`
	IsError bool             `json:"is_error"`
}

// spawnClaudeStream launches the claude CLI as a subprocess and returns a
// channel that delivers streamed response chunks. The channel is closed when
// the stream ends, either normally or due to an error.
func spawnClaudeStream(ctx context.Context, prompt string, claudeCfg config.ClaudeConfig) <-chan llm.StreamResponse {
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
		if claudeCfg.PermissionMode != "" {
			args = append(args, "--permission-mode", claudeCfg.PermissionMode)
		}
		args = append(args, prompt)

		cmd := exec.CommandContext(ctx, claudeCfg.Path, args...)

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
				if event.Event.Type == "content_block_delta" &&
					event.Event.Delta.Type == "text_delta" &&
					event.Event.Delta.Text != "" {
					ch <- llm.StreamResponse{Content: event.Event.Delta.Text}
				}
			case "result":
				done = true
				if event.IsError {
					ch <- llm.StreamResponse{Error: fmt.Errorf("claude error: %s", event.Result)}
				} else {
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
