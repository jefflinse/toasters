// Package provider hosts the concrete LLM provider implementations
// (Anthropic, OpenAI-compatible) plus the Registry used to resolve
// providers by name at runtime.
//
// The wire-level interface and types (Provider, ChatRequest, Message,
// StreamEvent, ToolCall, Usage, ModelInfo) are defined by
// github.com/jefflinse/mycelium/provider. This package re-exports them
// as aliases so existing callers — and this package's own concrete
// implementations — keep working unchanged; callers may also import
// mycelium/provider directly. The alias layer is intentional: it keeps
// mycelium as the single source of truth for the wire types while this
// package remains the home of Toasters-specific provider plumbing
// (Registry, ProviderConfig, concrete HTTP clients).
package provider

import (
	"context"
	"strings"

	mcp "github.com/jefflinse/mycelium/provider"
)

// Type aliases — mycelium/provider is authoritative.
type (
	Provider    = mcp.Provider
	ChatRequest = mcp.ChatRequest
	Message     = mcp.Message
	ToolCall    = mcp.ToolCall
	// Tool is an alias for mycelium's ToolDef so call sites may keep
	// writing provider.Tool{...}. Name mismatch is intentional — Toasters
	// historically called this Tool; mycelium calls it ToolDef.
	Tool        = mcp.ToolDef
	Usage       = mcp.Usage
	ModelInfo   = mcp.ModelInfo
	StreamEvent = mcp.StreamEvent
	EventType   = mcp.EventType
)

// Stream event type constants — re-exported from mycelium so callers can
// keep writing provider.EventText etc.
const (
	EventText     = mcp.EventText
	EventToolCall = mcp.EventToolCall
	EventUsage    = mcp.EventUsage
	EventDone     = mcp.EventDone
	EventError    = mcp.EventError
)

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
