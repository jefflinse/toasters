package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

const (
	defaultAnthropicBaseURL = "https://api.anthropic.com"
	defaultAnthropicVersion = "2023-06-01"
)

// AnthropicProvider implements Provider for the Anthropic Messages API.
type AnthropicProvider struct {
	name       string
	apiKey     string
	baseURL    string
	version    string
	httpClient *http.Client
}

// AnthropicOption configures an AnthropicProvider.
type AnthropicOption func(*AnthropicProvider)

// WithAnthropicBaseURL sets a custom base URL (e.g. for proxies or testing).
func WithAnthropicBaseURL(url string) AnthropicOption {
	return func(p *AnthropicProvider) {
		p.baseURL = strings.TrimRight(url, "/")
	}
}

// WithAnthropicVersion sets the anthropic-version header value.
func WithAnthropicVersion(version string) AnthropicOption {
	return func(p *AnthropicProvider) {
		p.version = version
	}
}

// NewAnthropic creates a new Anthropic provider.
func NewAnthropic(name, apiKey string, opts ...AnthropicOption) *AnthropicProvider {
	p := &AnthropicProvider{
		name:       name,
		apiKey:     apiKey,
		baseURL:    defaultAnthropicBaseURL,
		version:    defaultAnthropicVersion,
		httpClient: http.DefaultClient,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the provider identifier.
func (p *AnthropicProvider) Name() string { return p.name }

// ChatStream sends a chat request and streams events via a channel.
func (p *AnthropicProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	body := anthropicReq{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		Stream:    true,
	}
	if body.MaxTokens == 0 {
		body.MaxTokens = 4096
	}

	if req.System != "" {
		body.System = req.System
	}

	if req.Temperature != nil {
		body.Temperature = req.Temperature
	}

	// Convert messages to Anthropic format.
	body.Messages = convertMessagesToAnthropic(req.Messages)

	// Convert tools.
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, anthropicToolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("x-api-key", p.apiKey)
	}
	httpReq.Header.Set("anthropic-version", p.version)

	ch := make(chan StreamEvent, 8)

	go func() {
		defer close(ch)
		p.streamResponse(ctx, httpReq, ch)
	}()

	return ch, nil
}

// convertMessagesToAnthropic converts provider.Message slices to Anthropic's format.
func convertMessagesToAnthropic(msgs []Message) []anthropicMsg {
	var out []anthropicMsg

	for _, m := range msgs {
		switch m.Role {
		case "system":
			// System messages are handled via the top-level system field.
			continue

		case "assistant":
			if len(m.ToolCalls) > 0 {
				var blocks []any
				if m.Content != "" {
					blocks = append(blocks, map[string]any{
						"type": "text",
						"text": m.Content,
					})
				}
				for _, tc := range m.ToolCalls {
					var input any
					if err := json.Unmarshal(tc.Arguments, &input); err != nil {
						input = map[string]any{}
					}
					blocks = append(blocks, map[string]any{
						"type":  "tool_use",
						"id":    tc.ID,
						"name":  tc.Name,
						"input": input,
					})
				}
				b, _ := json.Marshal(blocks)
				out = append(out, anthropicMsg{Role: "assistant", Content: b})
			} else if m.Content != "" {
				b, _ := json.Marshal(m.Content)
				out = append(out, anthropicMsg{Role: "assistant", Content: b})
			}

		case "tool":
			// Tool results use role "user" with tool_result content blocks.
			block := map[string]any{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Content,
			}
			// Batch consecutive tool results into a single user message.
			if len(out) > 0 && out[len(out)-1].Role == "user" {
				var existing []any
				if err := json.Unmarshal(out[len(out)-1].Content, &existing); err == nil && len(existing) > 0 {
					if first, ok := existing[0].(map[string]any); ok && first["type"] == "tool_result" {
						existing = append(existing, block)
						b, _ := json.Marshal(existing)
						out[len(out)-1].Content = b
						continue
					}
				}
			}
			b, _ := json.Marshal([]any{block})
			out = append(out, anthropicMsg{Role: "user", Content: b})

		case "user":
			if m.Content != "" {
				b, _ := json.Marshal(m.Content)
				out = append(out, anthropicMsg{Role: "user", Content: b})
			}
		}
	}

	return out
}

func (p *AnthropicProvider) streamResponse(ctx context.Context, req *http.Request, ch chan<- StreamEvent) {
	resp, err := p.httpClient.Do(req)
	if err != nil {
		ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("sending request: %w", err)}
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("anthropic API error (%d): %s", resp.StatusCode, buf.String())}
		return
	}

	var (
		eventType  string
		toolBlocks map[int]*anthropicToolAccum
		inputUsage int
	)

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if ctx.Err() != nil {
			ch <- StreamEvent{Type: EventError, Error: ctx.Err()}
			return
		}

		line := scanner.Text()

		if line == "" {
			eventType = ""
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		switch eventType {
		case "message_start":
			var ev anthropicMessageStartEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("parsing message_start: %w", err)}
				return
			}
			inputUsage = ev.Message.Usage.InputTokens

		case "content_block_start":
			var ev anthropicContentBlockStartEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("parsing content_block_start: %w", err)}
				return
			}
			if ev.ContentBlock.Type == "tool_use" {
				if toolBlocks == nil {
					toolBlocks = make(map[int]*anthropicToolAccum)
				}
				toolBlocks[ev.Index] = &anthropicToolAccum{
					id:   ev.ContentBlock.ID,
					name: ev.ContentBlock.Name,
				}
			}

		case "content_block_delta":
			var ev anthropicContentBlockDeltaEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("parsing content_block_delta: %w", err)}
				return
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					ch <- StreamEvent{Type: EventText, Text: ev.Delta.Text}
				}
			case "input_json_delta":
				if acc, ok := toolBlocks[ev.Index]; ok {
					acc.inputBuf.WriteString(ev.Delta.PartialJSON)
				}
			}

		case "content_block_stop":
			// Nothing special needed here.

		case "message_delta":
			var ev anthropicMessageDeltaEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("parsing message_delta: %w", err)}
				return
			}

			usage := &Usage{
				InputTokens:  inputUsage,
				OutputTokens: ev.Usage.OutputTokens,
			}

			if ev.Delta.StopReason == "tool_use" && len(toolBlocks) > 0 {
				// Emit tool calls sorted by index.
				indices := make([]int, 0, len(toolBlocks))
				for idx := range toolBlocks {
					indices = append(indices, idx)
				}
				sort.Ints(indices)

				for _, idx := range indices {
					acc := toolBlocks[idx]
					ch <- StreamEvent{
						Type: EventToolCall,
						ToolCall: &ToolCall{
							ID:        acc.id,
							Name:      acc.name,
							Arguments: json.RawMessage(acc.inputBuf.String()),
						},
					}
				}
				ch <- StreamEvent{Type: EventUsage, Usage: usage}
				ch <- StreamEvent{Type: EventDone}
				return
			}

			ch <- StreamEvent{Type: EventUsage, Usage: usage}

		case "message_stop":
			ch <- StreamEvent{Type: EventDone}
			return

		case "error":
			var ev anthropicErrorEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("parsing error event: %w", err)}
				return
			}
			ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("anthropic API error: %s: %s", ev.Error.Type, ev.Error.Message)}
			return

		case "ping":
			// Ignored.
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("reading stream: %w", err)}
		return
	}

	// Stream ended without message_stop.
	ch <- StreamEvent{Type: EventDone}
}

// Models returns a static list of known Claude models.
func (p *AnthropicProvider) Models(_ context.Context) ([]ModelInfo, error) {
	return []ModelInfo{
		{ID: "claude-sonnet-4-20250514", Name: "Claude Sonnet 4", Provider: p.name},
		{ID: "claude-haiku-4-20250414", Name: "Claude Haiku 4", Provider: p.name},
		{ID: "claude-opus-4-20250514", Name: "Claude Opus 4", Provider: p.name},
	}, nil
}

// --- Anthropic API types ---

type anthropicReq struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Stream      bool               `json:"stream"`
	Messages    []anthropicMsg     `json:"messages"`
	System      string             `json:"system,omitempty"`
	Tools       []anthropicToolDef `json:"tools,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
}

type anthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type anthropicToolAccum struct {
	id       string
	name     string
	inputBuf strings.Builder
}

type anthropicMessageStartEvent struct {
	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type anthropicContentBlockStartEvent struct {
	Index        int `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"content_block"`
}

type anthropicContentBlockDeltaEvent struct {
	Index int `json:"index"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
	} `json:"delta"`
}

type anthropicMessageDeltaEvent struct {
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type anthropicErrorEvent struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}
