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

// ChatRequest is the request body for /v1/chat/completions.
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
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
	Content string `json:"content"`
	Role    string `json:"role,omitempty"`
}

// Usage holds token usage statistics.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamResponse carries a single update from the streaming API.
type StreamResponse struct {
	Content string // text chunk (may be empty for final message)
	Done    bool   // true when stream is complete
	Model   string // model name from response
	Usage   *Usage // token usage (usually only on final chunk)
	Error   error  // non-nil if something went wrong
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

			if choice.Delta.Content != "" {
				ch <- StreamResponse{
					Content: choice.Delta.Content,
					Model:   chunk.Model,
				}
			}

			if choice.FinishReason == "stop" {
				ch <- StreamResponse{Done: true, Model: lastModel, Usage: lastUsage}
				return
			}
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- StreamResponse{Error: fmt.Errorf("reading stream: %w", err)}
		return
	}

	// Stream ended without [DONE] or finish_reason — treat as done.
	ch <- StreamResponse{Done: true, Model: lastModel, Usage: lastUsage}
}

// modelsResponse is the response from /v1/models.
type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// FetchModels returns the list of model IDs available on the server.
func (c *Client) FetchModels(ctx context.Context) ([]string, error) {
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

	var result modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding models response: %w", err)
	}

	models := make([]string, len(result.Data))
	for i, m := range result.Data {
		models[i] = m.ID
	}

	return models, nil
}
