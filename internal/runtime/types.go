package runtime

import (
	"encoding/json"
	"time"

	"github.com/jefflinse/toasters/internal/tooldef"
)

// SpawnOpts configures a new agent session.
type SpawnOpts struct {
	AgentID         string
	ProviderName    string
	Model           string
	SystemPrompt    string
	Tools           []ToolDef
	DisallowedTools []string     // tool names to deny at CoreTools level (defense-in-depth)
	ToolExecutor    ToolExecutor // optional; overrides default CoreTools when set
	ExtraTools      ToolExecutor // optional; layered on top of CoreTools (overlay with dispatch priority)
	JobID           string
	TaskID          string
	TeamName        string // team this agent belongs to (may be empty)
	Task            string // short human-readable description of what this agent is doing (≤60 chars)
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
	AgentID   string
	TeamName  string // team this agent belongs to (may be empty)
	JobID     string
	TaskID    string
	Status    string
	Model     string
	Provider  string
	StartTime time.Time
	TokensIn  int64
	TokensOut int64
}

// SessionEvent is emitted by a session for observers.
type SessionEvent struct {
	SessionID  string
	Type       SessionEventType
	Text       string
	ToolCall   *ToolCallEvent
	ToolResult *ToolResultEvent
	Error      error
}

// SessionEventType identifies the kind of session event.
type SessionEventType string

const (
	SessionEventText       SessionEventType = "text"
	SessionEventToolCall   SessionEventType = "tool_call"
	SessionEventToolResult SessionEventType = "tool_result"
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
