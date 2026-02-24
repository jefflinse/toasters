package llm

import "context"

// Provider is the interface that any LLM backend must implement to serve
// as the operator's language model. Both the local (LM Studio / OpenAI-compatible)
// client and the Anthropic client satisfy this interface.
type Provider interface {
	// ChatCompletionStream sends messages and returns a channel of streamed responses.
	ChatCompletionStream(ctx context.Context, messages []Message, temperature float64) <-chan StreamResponse

	// ChatCompletionStreamWithTools is like ChatCompletionStream but includes tool definitions.
	ChatCompletionStreamWithTools(ctx context.Context, messages []Message, tools []Tool, temperature float64) <-chan StreamResponse

	// ChatCompletion sends a one-shot (non-streaming) request and returns the text response.
	ChatCompletion(ctx context.Context, msgs []Message) (string, error)

	// FetchModels returns metadata about available models on the backend.
	FetchModels(ctx context.Context) ([]ModelInfo, error)

	// BaseURL returns a display-friendly identifier for the backend (endpoint URL or provider name).
	BaseURL() string
}
