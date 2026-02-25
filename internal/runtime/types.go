package runtime

import (
	"encoding/json"
	"time"
)

// SpawnOpts configures a new agent session.
type SpawnOpts struct {
	AgentID        string
	ProviderName   string
	Model          string
	SystemPrompt   string
	Tools          []ToolDef
	JobID          string
	TaskID         string
	InitialMessage string
	WorkDir        string
	MaxTurns       int // 0 = use default (50)
	MaxDepth       int // 0 = use default (3); for spawn_agent recursion
}

const (
	defaultMaxTurns = 50
	defaultMaxDepth = 3
)

// ToolDef defines a tool available to an agent.
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema
}

// SessionSnapshot is a read-only view of a session's state.
type SessionSnapshot struct {
	ID        string
	AgentID   string
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
