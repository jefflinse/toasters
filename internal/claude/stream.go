package claude

import "encoding/json"

// InitEvent is the first line emitted by `claude --output-format stream-json`.
// It carries model and permission metadata for the session.
type InitEvent struct {
	Type              string `json:"type"`
	Subtype           string `json:"subtype"`
	Model             string `json:"model"`
	PermissionMode    string `json:"permissionMode"`
	ClaudeCodeVersion string `json:"claude_code_version"`
	SessionID         string `json:"session_id"`
}

// InnerEvent is the inner event shape nested inside stream_event wrappers.
type InnerEvent struct {
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

// ContentBlock is one element of an assistant message's content array.
type ContentBlock struct {
	Type     string `json:"type"`     // "text", "tool_use", or "thinking"
	ID       string `json:"id"`       // tool use ID (for type="tool_use")
	Name     string `json:"name"`     // for type="tool_use"
	Input    any    `json:"input"`    // for type="tool_use"
	Text     string `json:"text"`     // for type="text"
	Thinking string `json:"thinking"` // for type="thinking"
}

// AssistantMessage is the message field inside a top-level "assistant" event.
type AssistantMessage struct {
	Content []ContentBlock `json:"content"`
}

// ToolResultBlock is one element of a user message's content array,
// carrying the result of a tool call back to the model.
type ToolResultBlock struct {
	Type      string          `json:"type"`        // "tool_result"
	ToolUseID string          `json:"tool_use_id"` // matches the tool_use block ID
	Content   json.RawMessage `json:"content"`     // string or []content_block
}

// UserMessage is the message field inside a top-level "user" event,
// typically carrying tool results from subagent calls.
type UserMessage struct {
	Role    string            `json:"role"`
	Content []ToolResultBlock `json:"content"`
}

// UserOuterEvent is the top-level shape for type="user" events, which
// carry tool results back from subagent calls.
type UserOuterEvent struct {
	Type    string      `json:"type"`
	Message UserMessage `json:"message"`
}

// OuterEvent is the top-level shape of a JSON line emitted by
// `claude --output-format stream-json`. Content deltas arrive wrapped in a
// "stream_event" envelope; terminal results arrive at the top level.
type OuterEvent struct {
	Type    string           `json:"type"`
	Event   InnerEvent       `json:"event"`
	Message AssistantMessage `json:"message"` // for type="assistant"
	Result  string           `json:"result"`
	IsError bool             `json:"is_error"`
}
