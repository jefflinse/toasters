package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/llm"
)

// Compile-time check that Client satisfies llm.Provider.
var _ llm.Provider = (*Client)(nil)

// Client talks to an OpenAI-compatible API (e.g. LM Studio).
type Client struct {
	baseURL    string
	httpClient *http.Client
	model      string
}

// NewClient creates a Client pointing at the given base URL.
// The model parameter may be empty — LM Studio will use whatever model is loaded.
func NewClient(baseURL string, model string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
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
		model: model,
	}
}

// BaseURL returns the configured base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// ChatCompletionStream sends messages and returns a channel that delivers
// streamed response chunks. The channel is closed when the stream ends,
// either normally or due to an error.
func (c *Client) ChatCompletionStream(ctx context.Context, messages []llm.Message, temperature float64) <-chan llm.StreamResponse {
	ch := make(chan llm.StreamResponse, 1)

	go func() {
		defer close(ch)
		c.streamCompletion(ctx, messages, temperature, ch)
	}()

	return ch
}

// ChatCompletionStreamWithTools is like ChatCompletionStream but sends tool definitions
// to the LLM, enabling tool calling.
func (c *Client) ChatCompletionStreamWithTools(ctx context.Context, messages []llm.Message, tools []llm.Tool, temperature float64) <-chan llm.StreamResponse {
	ch := make(chan llm.StreamResponse, 1)
	go func() {
		defer close(ch)
		c.streamCompletionWithTools(ctx, messages, tools, temperature, ch)
	}()
	return ch
}

func (c *Client) streamCompletion(ctx context.Context, messages []llm.Message, temperature float64, ch chan<- llm.StreamResponse) {
	reqBody := llm.ChatRequest{
		Model:    c.model,
		Messages: messages,
		Stream:   true,
		StreamOptions: &llm.StreamOptions{
			IncludeUsage: true,
		},
	}
	if temperature > 0 {
		reqBody.Temperature = &temperature
	}
	c.doStream(ctx, reqBody, ch)
}

func (c *Client) streamCompletionWithTools(ctx context.Context, messages []llm.Message, tools []llm.Tool, temperature float64, ch chan<- llm.StreamResponse) {
	reqBody := llm.ChatRequest{
		Model:    c.model,
		Messages: messages,
		Stream:   true,
		StreamOptions: &llm.StreamOptions{
			IncludeUsage: true,
		},
		Tools: tools,
	}
	if temperature > 0 {
		reqBody.Temperature = &temperature
	}
	c.doStream(ctx, reqBody, ch)
}

// doStream executes a streaming chat completion request and delivers parsed
// SSE chunks to ch. When reqBody.Tools is non-empty, tool call deltas are
// accumulated and emitted on finish_reason="tool_calls".
func (c *Client) doStream(ctx context.Context, reqBody llm.ChatRequest, ch chan<- llm.StreamResponse) {
	body, err := json.Marshal(reqBody)
	if err != nil {
		ch <- llm.StreamResponse{Error: fmt.Errorf("marshaling request: %w", err)}
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		ch <- llm.StreamResponse{Error: fmt.Errorf("creating request: %w", err)}
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		ch <- llm.StreamResponse{Error: fmt.Errorf("sending request: %w", err)}
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		ch <- llm.StreamResponse{Error: fmt.Errorf("unexpected status %d: %s", resp.StatusCode, resp.Status)}
		return
	}

	var lastUsage *llm.Usage
	var lastModel string

	// Only allocate the tool call accumulator when tools are present.
	hasTools := len(reqBody.Tools) > 0
	var accumulated map[int]*llm.ToolCall
	if hasTools {
		accumulated = make(map[int]*llm.ToolCall)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		// Check for context cancellation between lines.
		if ctx.Err() != nil {
			ch <- llm.StreamResponse{Error: ctx.Err()}
			return
		}

		line := scanner.Text()

		// Skip blank lines (SSE event separators).
		if line == "" {
			continue
		}

		// We only care about data lines.
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		// Strip the "data:" prefix. Handle both "data: " and "data:" (no space).
		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimSpace(data)

		if data == "[DONE]" {
			ch <- llm.StreamResponse{Done: true, Model: lastModel, Usage: lastUsage}
			return
		}

		var chunk llm.ChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			ch <- llm.StreamResponse{Error: fmt.Errorf("parsing chunk: %w", err)}
			return
		}

		if chunk.Model != "" {
			lastModel = chunk.Model
		}
		if chunk.Usage != nil {
			lastUsage = chunk.Usage
		}

		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]

			// Accumulate tool call deltas when tools are in play.
			if hasTools {
				for _, partial := range choice.Delta.ToolCalls {
					idx := partial.Index
					if _, ok := accumulated[idx]; !ok {
						accumulated[idx] = &llm.ToolCall{
							Index: idx,
							ID:    partial.ID,
							Type:  partial.Type,
							Function: llm.ToolCallFunction{
								Name: partial.Function.Name,
							},
						}
					}
					entry := accumulated[idx]
					entry.Function.Arguments += partial.Function.Arguments
					if partial.ID != "" && entry.ID == "" {
						entry.ID = partial.ID
					}
					if partial.Function.Name != "" && entry.Function.Name == "" {
						entry.Function.Name = partial.Function.Name
					}
				}
			}

			if choice.Delta.Reasoning != "" {
				ch <- llm.StreamResponse{
					Reasoning: choice.Delta.Reasoning,
					Model:     chunk.Model,
				}
			}

			if choice.Delta.Content != "" {
				ch <- llm.StreamResponse{
					Content: choice.Delta.Content,
					Model:   chunk.Model,
				}
			}

			if hasTools && choice.FinishReason == "tool_calls" {
				// Collect accumulated tool calls sorted by index.
				indices := make([]int, 0, len(accumulated))
				for idx := range accumulated {
					indices = append(indices, idx)
				}
				sort.Ints(indices)
				calls := make([]llm.ToolCall, 0, len(indices))
				for _, idx := range indices {
					calls = append(calls, *accumulated[idx])
				}
				ch <- llm.StreamResponse{ToolCalls: calls, Done: true}
				return
			}

			// On finish_reason=stop, don't return yet — with
			// stream_options.include_usage, the server sends a final
			// chunk with usage data after the stop chunk. Keep reading
			// until [DONE].
		}
		// Chunks with no choices (e.g. usage-only after stop) are handled
		// by the Usage capture above; keep reading for [DONE].
	}

	if err := scanner.Err(); err != nil {
		ch <- llm.StreamResponse{Error: fmt.Errorf("reading stream: %w", err)}
		return
	}

	// Stream ended without [DONE] — treat as done.
	ch <- llm.StreamResponse{Done: true, Model: lastModel, Usage: lastUsage}
}

// ChatCompletion sends a one-shot (non-streaming) chat completion request and
// returns the trimmed text content of the first choice. Intended for quick
// classification tasks where streaming is unnecessary.
func (c *Client) ChatCompletion(ctx context.Context, msgs []llm.Message) (string, error) {
	type chatCompletionResponse struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	reqBody := struct {
		Model    string        `json:"model"`
		Messages []llm.Message `json:"messages"`
		Stream   bool          `json:"stream"`
	}{
		Model:    c.model,
		Messages: msgs,
		Stream:   false,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("chat completion: marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("chat completion: creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("chat completion: sending request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("chat completion: reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("chat completion: status %d: %s", resp.StatusCode, respBody)
	}

	var result chatCompletionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("chat completion: decoding response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("chat completion: no choices in response")
	}

	return strings.TrimSpace(result.Choices[0].Message.Content), nil
}

// lmStudioModelsResponse is the response from LM Studio's /api/v0/models.
type lmStudioModelsResponse struct {
	Data []struct {
		ID                  string `json:"id"`
		State               string `json:"state"`
		MaxContextLength    int    `json:"max_context_length,omitempty"`
		LoadedContextLength int    `json:"loaded_context_length,omitempty"`
	} `json:"data"`
}

// openAIModelsResponse is the response from the standard /v1/models.
type openAIModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// FetchModels returns metadata about available models on the server.
// Tries the LM Studio-specific endpoint first for richer metadata, then
// falls back to the standard OpenAI endpoint.
func (c *Client) FetchModels(ctx context.Context) ([]llm.ModelInfo, error) {
	models, err := c.fetchLMStudioModels(ctx)
	if err == nil {
		return models, nil
	}

	// Fall back to standard OpenAI endpoint.
	return c.fetchOpenAIModels(ctx)
}

func (c *Client) fetchLMStudioModels(ctx context.Context) ([]llm.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v0/models", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, resp.Status)
	}

	var result lmStudioModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding models response: %w", err)
	}

	models := make([]llm.ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, llm.ModelInfo{
			ID:                  m.ID,
			State:               m.State,
			MaxContextLength:    m.MaxContextLength,
			LoadedContextLength: m.LoadedContextLength,
		})
	}

	return models, nil
}

func (c *Client) fetchOpenAIModels(ctx context.Context) ([]llm.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, resp.Status)
	}

	var result openAIModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding models response: %w", err)
	}

	models := make([]llm.ModelInfo, len(result.Data))
	for i, m := range result.Data {
		models[i] = llm.ModelInfo{ID: m.ID}
	}

	return models, nil
}
