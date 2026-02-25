package sse

import "strings"

// OpenAIChunk is a single SSE chunk from an OpenAI-compatible streaming response.
type OpenAIChunk struct {
	ID      string         `json:"id"`
	Choices []OpenAIChoice `json:"choices"`
	Model   string         `json:"model"`
	Usage   *OpenAIUsage   `json:"usage,omitempty"`
}

// OpenAIChoice is one completion choice within a chunk.
type OpenAIChoice struct {
	Delta        OpenAIDelta `json:"delta"`
	FinishReason string      `json:"finish_reason"`
}

// OpenAIDelta holds the incremental content for a streaming choice.
type OpenAIDelta struct {
	Content   string           `json:"content"`
	Role      string           `json:"role,omitempty"`
	Reasoning string           `json:"reasoning,omitempty"`
	ToolCalls []OpenAIToolCall `json:"tool_calls,omitempty"`
}

// OpenAIToolCall represents a tool call delta in an OpenAI SSE chunk.
type OpenAIToolCall struct {
	Index    int                    `json:"index,omitempty"`
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type,omitempty"`
	Function OpenAIToolCallFunction `json:"function"`
}

// OpenAIToolCallFunction holds the function name and argument fragment.
type OpenAIToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// OpenAIUsage holds token usage from OpenAI SSE chunks.
type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OpenAIToolAccumulator tracks incremental tool call data from OpenAI SSE chunks.
type OpenAIToolAccumulator struct {
	ID   string
	Name string
	Args strings.Builder
}
