package runtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jefflinse/toasters/internal/tooldef"
)

// SpawnOpts configures a new worker session.
type SpawnOpts struct {
	WorkerID        string
	ProviderName    string
	Model           string
	SystemPrompt    string
	Tools           []ToolDef
	DisallowedTools []string     // tool names to deny at CoreTools level (defense-in-depth)
	ToolExecutor    ToolExecutor // optional; overrides default CoreTools when set
	ExtraTools      ToolExecutor // optional; layered on top of CoreTools (overlay with dispatch priority)
	JobID           string
	TaskID          string
	TeamName        string // team this worker belongs to (may be empty)
	Task            string // short human-readable description of what this worker is doing (≤60 chars)
	InitialMessage  string
	WorkDir         string
	MaxTurns        int  // 0 = use default (50)
	MaxDepth        int  // 0 = use default (1); coordinators may spawn workers, workers may not spawn further
	Depth           int  // current spawn depth (set by parent)
	Hidden          bool // when true, OnSessionStarted is not called (internal/system sessions)
}

const (
	defaultMaxTurns = 50
	defaultMaxDepth = 1
)

// ToolDef is an alias for tooldef.ToolDef, the shared tool definition type.
type ToolDef = tooldef.ToolDef

// MCPCaller is an alias for tooldef.MCPCaller, the shared MCP dispatch interface.
type MCPCaller = tooldef.MCPCaller

// SessionSnapshot is a read-only view of a session's state.
type SessionSnapshot struct {
	ID        string
	WorkerID  string
	TeamName  string // team this worker belongs to (may be empty)
	JobID     string
	TaskID    string
	Status    string
	Model     string
	Provider  string
	StartTime time.Time
	TokensIn  int64
	TokensOut int64
	// CurrentContextTokens is the prompt size of the most recent round-trip —
	// the session's live context-window occupancy (0 if none reported yet).
	CurrentContextTokens int64
}

// SessionEvent is emitted by a session for observers.
type SessionEvent struct {
	SessionID  string
	Type       SessionEventType
	Text       string
	ToolCall   *ToolCallEvent
	ToolResult *ToolResultEvent
	FileChange *FileChange
	Error      error
}

// SessionEventType identifies the kind of session event.
type SessionEventType string

const (
	SessionEventText       SessionEventType = "text"
	SessionEventToolCall   SessionEventType = "tool_call"
	SessionEventToolResult SessionEventType = "tool_result"
	SessionEventFileChange SessionEventType = "file_change"
	SessionEventDone       SessionEventType = "done"
	SessionEventError      SessionEventType = "error"
)

// ToolCallEvent describes a tool invocation by the LLM.
type ToolCallEvent struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ToolResultEvent describes the result of a tool execution.
type ToolResultEvent struct {
	CallID string
	Name   string
	Result string
	Error  string
}

// FileChange describes a file mutation performed by a built-in file tool
// (write_file / edit_file). It exists for display: the diff is shown to the
// user but deliberately kept OUT of the tool result string returned to the
// LLM, which already knows what it wrote and shouldn't spend context on it.
type FileChange struct {
	ToolName string // "write_file" or "edit_file"
	Path     string // path as the model passed it (pre-resolution)
	Diff     string // unified diff body (hunks only, no ---/+++ header), capped
	Added    int    // total lines added (across the whole change, not the capped diff)
	Removed  int    // total lines removed
	Created  bool   // true when write_file created a new file
	Truncated bool  // true when Diff was capped server-side
}

// FileChangeNotifier receives FileChange notifications from CoreTools as a
// display side-channel. The ctx is the one passed to Execute, so callers that
// stash per-invocation identity in ctx (graphexec's NodeContext) can recover
// it at notification time.
type FileChangeNotifier func(ctx context.Context, fc FileChange)
