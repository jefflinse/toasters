package tui

import (
	"strings"
	"time"
)

// runtimeSlot tracks a runtime worker session for TUI display.
type runtimeSlot struct {
	sessionID  string
	workerName string
	teamName   string // team this worker belongs to (may be empty)
	task       string // short human-readable description of what this worker is doing
	jobID      string
	taskID     string
	status     string // "active", "completed", "failed", "cancelled"
	system     bool   // internal decomposition step; hidden from chat/workers unless --debug

	// items holds typed output blocks (text + tool calls) so the graph
	// pane can render styled, scrollable output. Replaces the previous
	// strings.Builder accumulator. toolItemIdx maps tool call IDs to
	// their position in items so a tool result can patch the matching
	// call without scanning. See slot_output.go.
	items       []outputItem
	toolItemIdx map[string]int

	// contentVersion bumps on every items mutation so the graph pane's
	// glamour render cache (slot_render.go) can detect changes without
	// hashing the whole content. cachedRender holds the most recent
	// styled output for a given width and content version.
	contentVersion       int
	cachedRender         string
	cachedRenderVersion  int
	cachedRenderWidth    int
	cachedRenderAt       time.Time
	cachedRenderTerminal bool // true when the cache was produced after the slot finished — never re-render in that case

	reasoning      strings.Builder // streamed chain-of-thought; only set when the provider emits reasoning events
	startTime      time.Time
	endTime        time.Time      // set when session completes; zero while active
	systemPrompt   string         // the system prompt given to the LLM
	initialMessage string         // the initial user message / task description
	activities     []activityItem // recent tool-call activities; newest appended last, capped at 6

	// Provider/model/cost metrics, copied from the progress snapshot's active
	// sessions on each tick (session.* events don't carry them). Zero/empty
	// until the first snapshot that includes this session.
	model     string
	provider  string
	tokensIn  int64
	tokensOut int64
	costUSD   float64
	// contextTokens is the live context-window occupancy (most recent
	// round-trip's prompt size), sourced from the runtime live snapshot.
	contextTokens int64

	// temperature/thinking are the sampling settings the session runs with.
	// Set from a session.meta event (graph nodes) since they aren't carried
	// in the active-session snapshot. hasTemp distinguishes "0.0 temperature"
	// from "unknown".
	temperature float64
	hasTemp     bool
	thinking    bool
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
