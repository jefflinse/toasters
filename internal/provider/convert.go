package provider

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jefflinse/toasters/internal/llm"
)

// MessageFromLLM converts an llm.Message to a provider.Message.
func MessageFromLLM(m llm.Message) Message {
	msg := Message{
		Role:       m.Role,
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
	}
	for _, tc := range m.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, ToolCallFromLLM(tc))
	}
	return msg
}

// MessageToLLM converts a provider.Message to an llm.Message.
func MessageToLLM(m Message) llm.Message {
	msg := llm.Message{
		Role:       m.Role,
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
	}
	for _, tc := range m.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, ToolCallToLLM(tc))
	}
	return msg
}

// ToolFromLLM converts an llm.Tool to a provider.Tool.
func ToolFromLLM(t llm.Tool) Tool {
	var params json.RawMessage
	if t.Function.Parameters != nil {
		// llm.Tool.Function.Parameters is `any`, so marshal it.
		b, err := json.Marshal(t.Function.Parameters)
		if err == nil {
			params = b
		}
	}
	return Tool{
		Name:        t.Function.Name,
		Description: t.Function.Description,
		Parameters:  params,
	}
}

// ToolToLLM converts a provider.Tool to an llm.Tool.
func ToolToLLM(t Tool) llm.Tool {
	var params any
	if len(t.Parameters) > 0 {
		_ = json.Unmarshal(t.Parameters, &params)
	}
	return llm.Tool{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		},
	}
}

// ToolCallFromLLM converts an llm.ToolCall to a provider.ToolCall.
func ToolCallFromLLM(tc llm.ToolCall) ToolCall {
	return ToolCall{
		ID:        tc.ID,
		Name:      tc.Function.Name,
		Arguments: json.RawMessage(tc.Function.Arguments),
	}
}

// ToolCallToLLM converts a provider.ToolCall to an llm.ToolCall.
func ToolCallToLLM(tc ToolCall) llm.ToolCall {
	return llm.ToolCall{
		ID:   tc.ID,
		Type: "function",
		Function: llm.ToolCallFunction{
			Name:      tc.Name,
			Arguments: string(tc.Arguments),
		},
	}
}

// LLMProviderAdapter wraps a provider.Provider to satisfy the llm.Provider interface.
type LLMProviderAdapter struct {
	provider Provider
	endpoint string
}

// Compile-time check that LLMProviderAdapter satisfies llm.Provider.
var _ llm.Provider = (*LLMProviderAdapter)(nil)

// NewLLMProviderAdapter creates an adapter that wraps a provider.Provider.
func NewLLMProviderAdapter(p Provider, endpoint string) *LLMProviderAdapter {
	return &LLMProviderAdapter{provider: p, endpoint: endpoint}
}

// BaseURL returns the endpoint URL.
func (a *LLMProviderAdapter) BaseURL() string {
	return a.endpoint
}

// ChatCompletionStream sends messages and returns a channel of llm.StreamResponse.
func (a *LLMProviderAdapter) ChatCompletionStream(ctx context.Context, messages []llm.Message, temperature float64) <-chan llm.StreamResponse {
	ch := make(chan llm.StreamResponse, 1)

	go func() {
		defer close(ch)
		a.doStream(ctx, messages, nil, temperature, ch)
	}()

	return ch
}

// ChatCompletionStreamWithTools sends messages with tools and returns a channel of llm.StreamResponse.
func (a *LLMProviderAdapter) ChatCompletionStreamWithTools(ctx context.Context, messages []llm.Message, tools []llm.Tool, temperature float64) <-chan llm.StreamResponse {
	ch := make(chan llm.StreamResponse, 1)

	go func() {
		defer close(ch)
		a.doStream(ctx, messages, tools, temperature, ch)
	}()

	return ch
}

func (a *LLMProviderAdapter) doStream(ctx context.Context, messages []llm.Message, tools []llm.Tool, temperature float64, ch chan<- llm.StreamResponse) {
	req := ChatRequest{}

	// Extract system messages.
	var systemParts []string
	for _, m := range messages {
		if m.Role == "system" {
			if m.Content != "" {
				systemParts = append(systemParts, m.Content)
			}
			continue
		}
		req.Messages = append(req.Messages, MessageFromLLM(m))
	}
	if len(systemParts) > 0 {
		req.System = strings.Join(systemParts, "\n\n")
	}

	for _, t := range tools {
		req.Tools = append(req.Tools, ToolFromLLM(t))
	}

	// Always pass temperature through — 0.0 is a meaningful value (deterministic).
	// The llm.Provider interface uses float64 (not *float64), so we can't
	// distinguish "not set" from "zero" at this layer.
	req.Temperature = &temperature

	eventCh, err := a.provider.ChatStream(ctx, req)
	if err != nil {
		ch <- llm.StreamResponse{Error: err}
		return
	}

	var collectedToolCalls []llm.ToolCall
	var lastUsage *llm.Usage

	for ev := range eventCh {
		switch ev.Type {
		case EventText:
			ch <- llm.StreamResponse{Content: ev.Text}

		case EventToolCall:
			if ev.ToolCall != nil {
				collectedToolCalls = append(collectedToolCalls, ToolCallToLLM(*ev.ToolCall))
			}

		case EventUsage:
			if ev.Usage != nil {
				lastUsage = &llm.Usage{
					PromptTokens:     ev.Usage.InputTokens,
					CompletionTokens: ev.Usage.OutputTokens,
					TotalTokens:      ev.Usage.InputTokens + ev.Usage.OutputTokens,
				}
			}

		case EventDone:
			resp := llm.StreamResponse{Done: true, Usage: lastUsage}
			if len(collectedToolCalls) > 0 {
				resp.ToolCalls = collectedToolCalls
			}
			ch <- resp
			return

		case EventError:
			ch <- llm.StreamResponse{Error: ev.Error}
			return
		}
	}

	// Channel closed without EventDone — treat as done.
	resp := llm.StreamResponse{Done: true, Usage: lastUsage}
	if len(collectedToolCalls) > 0 {
		resp.ToolCalls = collectedToolCalls
	}
	ch <- resp
}

// ChatCompletion sends a non-streaming request by collecting the full stream.
func (a *LLMProviderAdapter) ChatCompletion(ctx context.Context, msgs []llm.Message) (string, error) {
	req := ChatRequest{}

	var systemParts []string
	for _, m := range msgs {
		if m.Role == "system" {
			if m.Content != "" {
				systemParts = append(systemParts, m.Content)
			}
			continue
		}
		req.Messages = append(req.Messages, MessageFromLLM(m))
	}
	if len(systemParts) > 0 {
		req.System = strings.Join(systemParts, "\n\n")
	}

	eventCh, err := a.provider.ChatStream(ctx, req)
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

// FetchModels returns available models.
func (a *LLMProviderAdapter) FetchModels(ctx context.Context) ([]llm.ModelInfo, error) {
	models, err := a.provider.Models(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]llm.ModelInfo, 0, len(models))
	for _, m := range models {
		out = append(out, llm.ModelInfo{ID: m.ID})
	}
	return out, nil
}
