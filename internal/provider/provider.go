// Package provider defines a unified interface for LLM providers with
// channel-based streaming. It supports OpenAI-compatible and Anthropic
// backends behind a common abstraction, with a registry for managing
// multiple configured providers.
package provider

import (
	"context"
	"encoding/json"
)

// Provider abstracts LLM provider differences behind a common streaming interface.
type Provider interface {
	// ChatStream sends messages and streams the response via a channel.
	// The channel is always closed by the provider when done.
	// An event with Type EventError is terminal -- no more events follow (except channel close).
	// An event with Type EventDone signals successful completion.
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)

	// Models returns available models from this provider.
	Models(ctx context.Context) ([]ModelInfo, error)

	// Name returns the provider identifier (e.g. "anthropic", "openai", "lmstudio").
	Name() string
}

// EventType identifies the kind of stream event.
type EventType string

const (
	EventText     EventType = "text"
	EventToolCall EventType = "tool_call"
	EventUsage    EventType = "usage"
	EventDone     EventType = "done"
	EventError    EventType = "error"
)

// StreamEvent is a discriminated union for streaming events.
type StreamEvent struct {
	Type     EventType
	Text     string    // populated for EventText
	ToolCall *ToolCall // populated for EventToolCall (complete tool call)
	Usage    *Usage    // populated for EventUsage
	Error    error     // populated for EventError
}

// ChatRequest contains all parameters for a chat completion request.
type ChatRequest struct {
	Model       string
	Messages    []Message
	Tools       []Tool
	System      string // system prompt
	MaxTokens   int
	Temperature *float64
	Stop        []string
}

// Message represents a chat message.
type Message struct {
	Role       string // "system", "user", "assistant", "tool"
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string // for tool result messages
}

// ToolCall represents a tool invocation by the LLM.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Tool defines a tool available to the LLM.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema
}

// Usage tracks token consumption.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// ModelInfo describes an available model.
type ModelInfo struct {
	ID       string
	Name     string
	Provider string
}
