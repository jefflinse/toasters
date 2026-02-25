// Package provider defines a unified interface for LLM providers with
// channel-based streaming. It supports OpenAI-compatible and Anthropic
// backends behind a common abstraction, with a registry for managing
// multiple configured providers.
package provider

import (
	"context"
	"encoding/json"
	"strings"
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

	// Reasoning carries chain-of-thought text (populated for EventText when
	// the provider supports extended thinking).
	Reasoning string

	// Model carries the model name from the response (may be set on any event).
	Model string

	// StopReason carries the stop reason from message_delta (e.g. "end_turn", "tool_use").
	StopReason string

	// Gateway-specific fields — used only by the Claude CLI subprocess path.
	// These are populated by the gateway/claude.go streaming code and consumed
	// by the TUI. They will be removed when the gateway path is retired.
	Meta             *ClaudeMeta // non-nil only for the claude CLI system/init event
	PendingTool      string      // tool name when a tool_use content_block_start fires
	ClearPendingTool bool        // true when content_block_stop fires (clears PendingTool)
	ExitSummary      string      // final result text from a clean claude result event
	SubagentSpawned  bool        // true when a Task tool call was made
	SubagentResult   string      // non-empty when a tool_result for a subagent arrived
}

// ClaudeMeta carries metadata from the claude CLI system/init event.
type ClaudeMeta struct {
	Model          string
	PermissionMode string
	Version        string
	SessionID      string
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
	ID                  string
	Name                string
	Provider            string
	State               string // "loaded", "not-loaded", "available", etc.
	MaxContextLength    int    // max context window the model supports (0 if unknown)
	LoadedContextLength int    // actual context length configured for the loaded model (0 if unknown or not loaded)
}

// ContextLength returns the effective context length — loaded if available, otherwise max.
func (m ModelInfo) ContextLength() int {
	if m.LoadedContextLength > 0 {
		return m.LoadedContextLength
	}
	return m.MaxContextLength
}

// ChatCompletion is a convenience function that sends a non-streaming request
// by collecting the full stream into a string. It extracts system messages from
// the message list into the ChatRequest.System field.
func ChatCompletion(ctx context.Context, p Provider, msgs []Message) (string, error) {
	req := ChatRequest{}

	var systemParts []string
	for _, m := range msgs {
		if m.Role == "system" {
			if m.Content != "" {
				systemParts = append(systemParts, m.Content)
			}
			continue
		}
		req.Messages = append(req.Messages, m)
	}
	if len(systemParts) > 0 {
		req.System = strings.Join(systemParts, "\n\n")
	}

	eventCh, err := p.ChatStream(ctx, req)
	if err != nil {
		return "", err
	}

	var content strings.Builder
	for ev := range eventCh {
		switch ev.Type {
		case EventText:
			content.WriteString(ev.Text)
		case EventError:
			return "", ev.Error
		case EventDone:
			return strings.TrimSpace(content.String()), nil
		}
	}

	return strings.TrimSpace(content.String()), nil
}
