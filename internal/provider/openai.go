package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/sse"
)

// OpenAIProvider implements Provider for OpenAI-compatible APIs
// (OpenAI, LM Studio, Ollama, etc.).
type OpenAIProvider struct {
	name         string
	endpoint     string
	apiKey       string
	defaultModel string
	httpClient   *http.Client
}

// NewOpenAI creates a new OpenAI-compatible provider.
func NewOpenAI(name, endpoint, apiKey, defaultModel string) *OpenAIProvider {
	return &OpenAIProvider{
		name:         name,
		endpoint:     strings.TrimRight(endpoint, "/"),
		apiKey:       apiKey,
		defaultModel: defaultModel,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 5 * time.Minute,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
	}
}

// Name returns the provider identifier.
func (p *OpenAIProvider) Name() string { return p.name }

// ChatStream sends a chat request and streams events via a channel.
func (p *OpenAIProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	model := req.Model
	if model == "" {
		model = p.defaultModel
	}

	// Build the request body.
	body := openAIRequest{
		Model:  model,
		Stream: true,
		StreamOptions: &openAIStreamOptions{
			IncludeUsage: true,
		},
	}

	// Convert messages. System prompt goes as a system message.
	if req.System != "" {
		body.Messages = append(body.Messages, openAIMessage{
			Role:    "system",
			Content: req.System,
		})
	}
	for _, m := range req.Messages {
		msg := openAIMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, openAIToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: openAIToolCallFunction{
					Name:      tc.Name,
					Arguments: string(tc.Arguments),
				},
			})
		}
		body.Messages = append(body.Messages, msg)
	}

	// Convert tools.
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, openAITool{
			Type:     "function",
			Function: openAIToolFunction(t),
		})
	}

	if req.Temperature != nil {
		body.Temperature = req.Temperature
	}
	if req.MaxTokens > 0 {
		body.MaxTokens = &req.MaxTokens
	}
	if len(req.Stop) > 0 {
		body.Stop = req.Stop
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/v1/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	ch := make(chan StreamEvent, 8)

	go func() {
		defer close(ch)
		p.streamResponse(ctx, httpReq, len(req.Tools) > 0, ch)
	}()

	return ch, nil
}

func (p *OpenAIProvider) streamResponse(ctx context.Context, req *http.Request, hasTools bool, ch chan<- StreamEvent) {
	resp, err := p.httpClient.Do(req)
	if err != nil {
		ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("sending request: %w", err)}
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("unexpected status %d: %s", resp.StatusCode, resp.Status)}
		return
	}

	var accumulated map[int]*sse.OpenAIToolAccumulator
	if hasTools {
		accumulated = make(map[int]*sse.OpenAIToolAccumulator)
	}

	var lastUsage *Usage

	reader := sse.NewReader(resp.Body)
	for {
		ev, ok := reader.Next(ctx)
		if !ok {
			break
		}

		if ev.Data == "[DONE]" {
			// Emit any accumulated tool calls.
			if hasTools && len(accumulated) > 0 {
				p.emitToolCalls(accumulated, ch)
			}
			if lastUsage != nil {
				ch <- StreamEvent{Type: EventUsage, Usage: lastUsage}
			}
			ch <- StreamEvent{Type: EventDone}
			return
		}

		var chunk openAIChunk
		if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
			ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("parsing chunk: %w", err)}
			return
		}

		if chunk.Usage != nil {
			lastUsage = &Usage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			}
		}

		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]

			// Accumulate tool call deltas.
			if hasTools {
				for _, partial := range choice.Delta.ToolCalls {
					idx := partial.Index
					if _, ok := accumulated[idx]; !ok {
						accumulated[idx] = &sse.OpenAIToolAccumulator{
							ID:   partial.ID,
							Name: partial.Function.Name,
						}
					}
					entry := accumulated[idx]
					entry.Args.WriteString(partial.Function.Arguments)
					if partial.ID != "" && entry.ID == "" {
						entry.ID = partial.ID
					}
					if partial.Function.Name != "" && entry.Name == "" {
						entry.Name = partial.Function.Name
					}
				}

				if choice.FinishReason == "tool_calls" {
					p.emitToolCalls(accumulated, ch)
					if lastUsage != nil {
						ch <- StreamEvent{Type: EventUsage, Usage: lastUsage}
					}
					ch <- StreamEvent{Type: EventDone}
					return
				}
			}

			if choice.Delta.Content != "" {
				ch <- StreamEvent{Type: EventText, Text: choice.Delta.Content}
			}
		}
	}

	// Check context cancellation first.
	if ctx.Err() != nil {
		ch <- StreamEvent{Type: EventError, Error: ctx.Err()}
		return
	}

	if err := reader.Err(); err != nil {
		ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("reading stream: %w", err)}
		return
	}

	// Stream ended without [DONE].
	if hasTools && len(accumulated) > 0 {
		p.emitToolCalls(accumulated, ch)
	}
	if lastUsage != nil {
		ch <- StreamEvent{Type: EventUsage, Usage: lastUsage}
	}
	ch <- StreamEvent{Type: EventDone}
}

func (p *OpenAIProvider) emitToolCalls(accumulated map[int]*sse.OpenAIToolAccumulator, ch chan<- StreamEvent) {
	indices := make([]int, 0, len(accumulated))
	for idx := range accumulated {
		indices = append(indices, idx)
	}
	slices.Sort(indices)

	for _, idx := range indices {
		acc := accumulated[idx]
		ch <- StreamEvent{
			Type: EventToolCall,
			ToolCall: &ToolCall{
				ID:        acc.ID,
				Name:      acc.Name,
				Arguments: json.RawMessage(acc.Args.String()),
			},
		}
	}

	// Clear accumulated so we don't emit again.
	clear(accumulated)
}

// Models returns available models. Tries LM Studio endpoint first, falls back to OpenAI.
func (p *OpenAIProvider) Models(ctx context.Context) ([]ModelInfo, error) {
	models, err := p.fetchLMStudioModels(ctx)
	if err == nil {
		return models, nil
	}
	return p.fetchOpenAIModels(ctx)
}

func (p *OpenAIProvider) fetchLMStudioModels(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"/api/v0/models", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	models := make([]ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, ModelInfo{
			ID:       m.ID,
			Name:     m.ID,
			Provider: p.name,
		})
	}
	return models, nil
}

func (p *OpenAIProvider) fetchOpenAIModels(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	models := make([]ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, ModelInfo{
			ID:       m.ID,
			Name:     m.ID,
			Provider: p.name,
		})
	}
	return models, nil
}

// --- OpenAI API types ---

type openAIRequest struct {
	Model         string               `json:"model"`
	Messages      []openAIMessage      `json:"messages"`
	Stream        bool                 `json:"stream"`
	StreamOptions *openAIStreamOptions `json:"stream_options,omitempty"`
	Tools         []openAITool         `json:"tools,omitempty"`
	Temperature   *float64             `json:"temperature,omitempty"`
	MaxTokens     *int                 `json:"max_tokens,omitempty"`
	Stop          []string             `json:"stop,omitempty"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type openAIToolCall struct {
	Index    int                    `json:"index,omitempty"`
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type,omitempty"`
	Function openAIToolCallFunction `json:"function"`
}

type openAIToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openAIChunk struct {
	ID      string         `json:"id"`
	Choices []openAIChoice `json:"choices"`
	Model   string         `json:"model"`
	Usage   *openAIUsage   `json:"usage,omitempty"`
}

type openAIChoice struct {
	Delta        openAIDelta `json:"delta"`
	FinishReason string      `json:"finish_reason"`
}

type openAIDelta struct {
	Content   string           `json:"content"`
	Role      string           `json:"role,omitempty"`
	ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
