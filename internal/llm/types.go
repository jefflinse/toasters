package llm

// Message represents a single chat message.
type Message struct {
	Role       string     `json:"role"` // "system", "user", or "assistant"
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
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
	Tools         []Tool         `json:"tools,omitempty"`
	Temperature   *float64       `json:"temperature,omitempty"`
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
	Content   string     `json:"content"`
	Role      string     `json:"role,omitempty"`
	Reasoning string     `json:"reasoning,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// Usage holds token usage statistics.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ToolFunction describes the function a tool exposes.
type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// Tool represents a tool available to the LLM.
type Tool struct {
	Type     string       `json:"type"` // always "function"
	Function ToolFunction `json:"function"`
}

// ToolCallFunction holds the function name and accumulated arguments for a tool call.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall represents a single tool call requested by the LLM.
type ToolCall struct {
	Index    int              `json:"index"`
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// StreamResponse carries a single update from the streaming API.
type StreamResponse struct {
	Content   string     // text chunk (may be empty for final message)
	Reasoning string     // reasoning/thinking chunk (chain-of-thought, if supported)
	Done      bool       // true when stream is complete
	Model     string     // model name from response
	Usage     *Usage     // token usage (usually only on final chunk)
	Error     error      // non-nil if something went wrong
	ToolCalls []ToolCall // non-nil when the LLM requested tool calls

	// StopReason carries the stop reason from message_delta (e.g. "end_turn", "tool_use").
	StopReason string
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
