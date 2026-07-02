package provider

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsContextOverflow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"anthropic prompt too long", &APIError{Provider: "anthropic", StatusCode: 400,
			Body: `{"error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`}, true},
		{"openai code", &APIError{Provider: "openai", StatusCode: 400,
			Body: `{"error":{"code":"context_length_exceeded","message":"..."}}`}, true},
		{"lmstudio phrasing", &APIError{Provider: "LMStudio", StatusCode: 400,
			Body: "The number of tokens to keep from the initial prompt is greater than the context length"}, true},
		{"llamacpp phrasing", &APIError{Provider: "llamacpp", StatusCode: 400,
			Body: "the request exceeds the available context size"}, true},
		{"rate limit not overflow", &APIError{Provider: "anthropic", StatusCode: 429,
			Body: "rate limited, context window notwithstanding"}, false},
		{"server error not overflow", &APIError{Provider: "openai", StatusCode: 500,
			Body: "maximum context length exceeded"}, false},
		{"generic 400", &APIError{Provider: "openai", StatusCode: 400,
			Body: `{"error":{"message":"invalid tool schema"}}`}, false},
		{"wrapped api error", fmt.Errorf("collecting response: %w",
			fmt.Errorf("stream: %w", &APIError{Provider: "a", StatusCode: 400, Body: "prompt is too long"})), true},
		{"legacy string error", errors.New("anthropic API error: invalid_request_error: prompt is too long"), true},
		{"unrelated error", errors.New("connection refused"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsContextOverflow(tt.err); got != tt.want {
				t.Errorf("IsContextOverflow(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
