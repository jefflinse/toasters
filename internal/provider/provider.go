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
	"encoding/json"
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
	EventText      = mcp.EventText
	EventReasoning = mcp.EventReasoning
	EventToolCall  = mcp.EventToolCall
	EventUsage     = mcp.EventUsage
	EventDone      = mcp.EventDone
	EventError     = mcp.EventError
)

// sendEvent delivers ev to ch unless ctx is cancelled first. Returns false
// when the consumer is gone — the producer must stop. Stream goroutines must
// never send unconditionally: a consumer that returned early (ctx cancel,
// error) stops draining, and an unconditional send would park the goroutine
// and its HTTP connection forever (and leak the Scheduler slot it holds).
func sendEvent(ctx context.Context, ch chan<- StreamEvent, ev StreamEvent) bool {
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// NormalizeToolCallArgs repairs tool-call arguments that aren't valid JSON
// (empty or truncated output from a small model) to an empty object. Invalid
// JSON in an assistant message fails session persistence and gets every
// subsequent request rejected with a 400 by the LLM endpoint — the
// conversation never recovers. Call this on streamed tool calls before they
// enter a message history.
func NormalizeToolCallArgs(tcs []ToolCall) {
	for i := range tcs {
		if len(tcs[i].Arguments) == 0 || !json.Valid(tcs[i].Arguments) {
			tcs[i].Arguments = json.RawMessage("{}")
		}
	}
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
