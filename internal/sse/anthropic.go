package sse

import "strings"

// Anthropic SSE event type constants.
const (
	AnthropicMessageStart      = "message_start"
	AnthropicContentBlockStart = "content_block_start"
	AnthropicContentBlockDelta = "content_block_delta"
	AnthropicContentBlockStop  = "content_block_stop"
	AnthropicMessageDelta      = "message_delta"
	AnthropicMessageStop       = "message_stop"
	AnthropicError             = "error"
	AnthropicPing              = "ping"
)

// AnthropicMessageStartEvent is the "message_start" SSE event payload.
type AnthropicMessageStartEvent struct {
	Type    string `json:"type"`
	Message struct {
		Model string         `json:"model"`
		Usage AnthropicUsage `json:"usage"`
	} `json:"message"`
}

// AnthropicContentBlockStartEvent is the "content_block_start" SSE event payload.
type AnthropicContentBlockStartEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"content_block"`
}

// AnthropicContentBlockDeltaEvent is the "content_block_delta" SSE event payload.
type AnthropicContentBlockDeltaEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
		// Thinking carries incremental reasoning text when Type is
		// "thinking_delta". Anthropic models with extended thinking
		// enabled stream chain-of-thought this way.
		Thinking string `json:"thinking,omitempty"`
		// Signature carries a cryptographic signature Anthropic attaches
		// to thinking blocks. Type is "signature_delta". Not exposed to
		// consumers; retained here so parsing doesn't error.
		Signature string `json:"signature,omitempty"`
	} `json:"delta"`
}

// AnthropicMessageDeltaEvent is the "message_delta" SSE event payload.
type AnthropicMessageDeltaEvent struct {
	Type  string `json:"type"`
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage AnthropicUsage `json:"usage"`
}

// AnthropicErrorEvent is the "error" SSE event payload.
type AnthropicErrorEvent struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// AnthropicUsage holds token usage from Anthropic SSE events.
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AnthropicToolAccumulator tracks a tool_use content block being streamed.
// It accumulates the input_json_delta fragments into a complete JSON string.
type AnthropicToolAccumulator struct {
	ID       string
	Name     string
	InputBuf strings.Builder // accumulated input_json_delta fragments
}
