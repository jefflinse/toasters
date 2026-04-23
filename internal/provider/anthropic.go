package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/sse"
)

// anthropicProviderHTTPClient is a shared HTTP client with proper timeouts for
// Anthropic API requests, replacing http.DefaultClient to prevent goroutine
// leaks on slow/unresponsive API servers.
var anthropicProviderHTTPClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

const (
	defaultAnthropicBaseURL = "https://api.anthropic.com"
	defaultAnthropicVersion = "2023-06-01"
)

// authHeaders holds the resolved authentication headers for a request.
type authHeaders struct {
	header string            // primary auth header name (e.g. "x-api-key" or "Authorization")
	value  string            // primary auth header value
	extra  map[string]string // additional headers (e.g. "anthropic-beta")
}

// AnthropicProvider implements Provider for the Anthropic Messages API.
type AnthropicProvider struct {
	name         string
	apiKey       string
	baseURL      string
	version      string
	defaultModel string
	httpClient   *http.Client
	authFunc     func() (*authHeaders, error)
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

// WithAnthropicModel sets the default model used when ChatRequest.Model is empty.
func WithAnthropicModel(model string) AnthropicOption {
	return func(p *AnthropicProvider) {
		p.defaultModel = model
	}
}

// WithAnthropicAuthFunc overrides the default authentication function.
// This is primarily useful for testing.
func WithAnthropicAuthFunc(fn func() (*authHeaders, error)) AnthropicOption {
	return func(p *AnthropicProvider) {
		p.authFunc = fn
	}
}

// NewAnthropic creates a new Anthropic provider.
// If apiKey is non-empty, requests use the x-api-key header.
// If apiKey is empty and no authFunc is provided via WithAnthropicAuthFunc,
// requests will fail with a clear error at request time.
func NewAnthropic(name, apiKey string, opts ...AnthropicOption) *AnthropicProvider {
	p := &AnthropicProvider{
		name:       name,
		apiKey:     apiKey,
		baseURL:    defaultAnthropicBaseURL,
		version:    defaultAnthropicVersion,
		httpClient: anthropicProviderHTTPClient,
	}
	for _, opt := range opts {
		opt(p)
	}

	// Set default authFunc if not overridden by an option.
	if p.authFunc == nil {
		if p.apiKey != "" {
			p.authFunc = func() (*authHeaders, error) {
				return &authHeaders{
					header: "x-api-key",
					value:  p.apiKey,
				}, nil
			}
		} else {
			p.authFunc = func() (*authHeaders, error) {
				return nil, fmt.Errorf("anthropic provider %q requires an API key — set api_key in config or the ANTHROPIC_API_KEY environment variable", p.name)
			}
		}
	}

	return p
}

// Name returns the provider identifier.
func (p *AnthropicProvider) Name() string { return p.name }

// ChatStream sends a chat request and streams events via a channel.
func (p *AnthropicProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	model := req.Model
	if model == "" {
		model = p.defaultModel
	}
	body := anthropicReq{
		Model:     model,
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

	auth, err := p.authFunc()
	if err != nil {
		return nil, fmt.Errorf("resolving credentials: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(auth.header, auth.value)
	for k, v := range auth.extra {
		httpReq.Header.Set(k, v)
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
		_, _ = buf.ReadFrom(io.LimitReader(resp.Body, 1<<20))
		ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("anthropic API error (%d): %s", resp.StatusCode, buf.String())}
		return
	}

	var (
		toolBlocks map[int]*sse.AnthropicToolAccumulator
		inputUsage int
	)

	reader := sse.NewReader(resp.Body)
	for {
		ev, ok := reader.Next(ctx)
		if !ok {
			break
		}

		switch ev.Type {
		case sse.AnthropicMessageStart:
			var parsed sse.AnthropicMessageStartEvent
			if err := json.Unmarshal([]byte(ev.Data), &parsed); err != nil {
				ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("parsing message_start: %w", err)}
				return
			}
			inputUsage = parsed.Message.Usage.InputTokens

		case sse.AnthropicContentBlockStart:
			var parsed sse.AnthropicContentBlockStartEvent
			if err := json.Unmarshal([]byte(ev.Data), &parsed); err != nil {
				ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("parsing content_block_start: %w", err)}
				return
			}
			if parsed.ContentBlock.Type == "tool_use" {
				if toolBlocks == nil {
					toolBlocks = make(map[int]*sse.AnthropicToolAccumulator)
				}
				toolBlocks[parsed.Index] = &sse.AnthropicToolAccumulator{
					ID:   parsed.ContentBlock.ID,
					Name: parsed.ContentBlock.Name,
				}
			}

		case sse.AnthropicContentBlockDelta:
			var parsed sse.AnthropicContentBlockDeltaEvent
			if err := json.Unmarshal([]byte(ev.Data), &parsed); err != nil {
				ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("parsing content_block_delta: %w", err)}
				return
			}
			switch parsed.Delta.Type {
			case "text_delta":
				if parsed.Delta.Text != "" {
					ch <- StreamEvent{Type: EventText, Text: parsed.Delta.Text}
				}
			case "input_json_delta":
				if acc, ok := toolBlocks[parsed.Index]; ok {
					acc.InputBuf.WriteString(parsed.Delta.PartialJSON)
				}
			}

		case sse.AnthropicContentBlockStop:
			// Nothing special needed here.

		case sse.AnthropicMessageDelta:
			var parsed sse.AnthropicMessageDeltaEvent
			if err := json.Unmarshal([]byte(ev.Data), &parsed); err != nil {
				ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("parsing message_delta: %w", err)}
				return
			}

			usage := &Usage{
				InputTokens:  inputUsage,
				OutputTokens: parsed.Usage.OutputTokens,
			}

			if parsed.Delta.StopReason == "tool_use" && len(toolBlocks) > 0 {
				// Emit tool calls sorted by index.
				indices := make([]int, 0, len(toolBlocks))
				for idx := range toolBlocks {
					indices = append(indices, idx)
				}
				slices.Sort(indices)

				for _, idx := range indices {
					acc := toolBlocks[idx]
					ch <- StreamEvent{
						Type: EventToolCall,
						ToolCall: &ToolCall{
							ID:        acc.ID,
							Name:      acc.Name,
							Arguments: json.RawMessage(acc.InputBuf.String()),
						},
					}
				}
				ch <- StreamEvent{Type: EventUsage, Usage: usage}
				ch <- StreamEvent{Type: EventDone}
				return
			}

			ch <- StreamEvent{Type: EventUsage, Usage: usage}

		case sse.AnthropicMessageStop:
			ch <- StreamEvent{Type: EventDone}
			return

		case sse.AnthropicError:
			var parsed sse.AnthropicErrorEvent
			if err := json.Unmarshal([]byte(ev.Data), &parsed); err != nil {
				ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("parsing error event: %w", err)}
				return
			}
			ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("anthropic API error: %s: %s", parsed.Error.Type, parsed.Error.Message)}
			return

		case sse.AnthropicPing:
			// Ignored.
		}
	}

	// Check context cancellation first — it may have caused Next() to return false.
	if ctx.Err() != nil {
		ch <- StreamEvent{Type: EventError, Error: ctx.Err()}
		return
	}

	if err := reader.Err(); err != nil {
		ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("reading stream: %w", err)}
		return
	}

	// Stream ended without message_stop.
	ch <- StreamEvent{Type: EventDone}
}

// Models returns a static list of known Claude models.
func (p *AnthropicProvider) Models(_ context.Context) ([]ModelInfo, error) {
	return []ModelInfo{
		{ID: "claude-opus-4-6", Name: "Claude Opus 4.6", Provider: p.name},
		{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6", Provider: p.name},
		{ID: "claude-haiku-4-5", Name: "Claude Haiku 4.5", Provider: p.name},
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
