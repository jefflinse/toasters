package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Message represents a single chat message.
type Message struct {
	Role    string `json:"role"` // "system", "user", or "assistant"
	Content string `json:"content"`
}

// StreamOptions controls streaming behavior.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// ChatRequest is the request body for /v1/chat/completions.
type ChatRequest struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	Stream        bool           `json:"stream"`
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
}

// ChatCompletionChunk is a single SSE chunk from the streaming response.
type ChatCompletionChunk struct {
	ID      string   `json:"id"`
	Choices []Choice `json:"choices"`
	Model   string   `json:"model"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Choice is one completion choice within a chunk.
type Choice struct {
	Delta        Delta  `json:"delta"`
	FinishReason string `json:"finish_reason"`
}

// Delta holds the incremental content for a streaming choice.
type Delta struct {
	Content   string `json:"content"`
	Role      string `json:"role,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
}

// Usage holds token usage statistics.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamResponse carries a single update from the streaming API.
type StreamResponse struct {
	Content   string // text chunk (may be empty for final message)
	Reasoning string // reasoning/thinking chunk (chain-of-thought, if supported)
	Done      bool   // true when stream is complete
	Model     string // model name from response
	Usage     *Usage // token usage (usually only on final chunk)
	Error     error  // non-nil if something went wrong
}

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
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{},
		model:      model,
	}
}

// BaseURL returns the configured base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// ChatCompletionStream sends messages and returns a channel that delivers
// streamed response chunks. The channel is closed when the stream ends,
// either normally or due to an error.
func (c *Client) ChatCompletionStream(ctx context.Context, messages []Message) <-chan StreamResponse {
	ch := make(chan StreamResponse, 1)

	go func() {
		defer close(ch)
		c.streamCompletion(ctx, messages, ch)
	}()

	return ch
}

func (c *Client) streamCompletion(ctx context.Context, messages []Message, ch chan<- StreamResponse) {
	reqBody := ChatRequest{
		Model:    c.model,
		Messages: messages,
		Stream:   true,
		StreamOptions: &StreamOptions{
			IncludeUsage: true,
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		ch <- StreamResponse{Error: fmt.Errorf("marshaling request: %w", err)}
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		ch <- StreamResponse{Error: fmt.Errorf("creating request: %w", err)}
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		ch <- StreamResponse{Error: fmt.Errorf("sending request: %w", err)}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		ch <- StreamResponse{Error: fmt.Errorf("unexpected status %d: %s", resp.StatusCode, resp.Status)}
		return
	}

	var lastUsage *Usage
	var lastModel string
	sawStop := false

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		// Check for context cancellation between lines.
		if ctx.Err() != nil {
			ch <- StreamResponse{Error: ctx.Err()}
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
			ch <- StreamResponse{Done: true, Model: lastModel, Usage: lastUsage}
			return
		}

		var chunk ChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			ch <- StreamResponse{Error: fmt.Errorf("parsing chunk: %w", err)}
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

			if choice.Delta.Reasoning != "" {
				ch <- StreamResponse{
					Reasoning: choice.Delta.Reasoning,
					Model:     chunk.Model,
				}
			}

			if choice.Delta.Content != "" {
				ch <- StreamResponse{
					Content: choice.Delta.Content,
					Model:   chunk.Model,
				}
			}

			if choice.FinishReason == "stop" {
				sawStop = true
				// Don't return yet — with stream_options.include_usage, the
				// server sends a final chunk with usage data after the stop
				// chunk. Keep reading until [DONE].
			}
		} else if sawStop && chunk.Usage != nil {
			// This is the usage-only chunk that arrives after finish_reason=stop
			// when stream_options.include_usage is true. Usage is already
			// captured above; keep reading for [DONE].
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- StreamResponse{Error: fmt.Errorf("reading stream: %w", err)}
		return
	}

	// Stream ended without [DONE] — treat as done.
	ch <- StreamResponse{Done: true, Model: lastModel, Usage: lastUsage}
}

// ModelInfo holds metadata about an available model.
type ModelInfo struct {
	ID                  string
	State               string // "loaded", "not-loaded", etc.
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
func (c *Client) FetchModels(ctx context.Context) ([]ModelInfo, error) {
	models, err := c.fetchLMStudioModels(ctx)
	if err == nil {
		return models, nil
	}

	// Fall back to standard OpenAI endpoint.
	return c.fetchOpenAIModels(ctx)
}

func (c *Client) fetchLMStudioModels(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v0/models", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, resp.Status)
	}

	var result lmStudioModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding models response: %w", err)
	}

	models := make([]ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, ModelInfo{
			ID:                  m.ID,
			State:               m.State,
			MaxContextLength:    m.MaxContextLength,
			LoadedContextLength: m.LoadedContextLength,
		})
	}

	return models, nil
}

func (c *Client) fetchOpenAIModels(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, resp.Status)
	}

	var result openAIModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding models response: %w", err)
	}

	models := make([]ModelInfo, len(result.Data))
	for i, m := range result.Data {
		models[i] = ModelInfo{ID: m.ID}
	}

	return models, nil
}
