package contextwindow

import (
	"encoding/json"
	"testing"

	"github.com/jefflinse/toasters/internal/provider"
)

func TestEstimateTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msgs []provider.Message
		want int
	}{
		{"nil", nil, 0},
		{"content only", []provider.Message{{Content: "abcdefgh"}}, 2}, // 8/4
		{"counts tool call id", []provider.Message{{Content: "abcd", ToolCallID: "efgh"}}, 2},
		{"counts tool call name and args", []provider.Message{{
			ToolCalls: []provider.ToolCall{{Name: "ab", Arguments: json.RawMessage("cdefgh")}},
		}}, 2},
		{"sums across messages", []provider.Message{
			{Content: "abcdefgh"}, {Content: "abcdefgh"},
		}, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := EstimateTokens(tt.msgs); got != tt.want {
				t.Errorf("EstimateTokens = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTruncateMessages_SafeBoundary(t *testing.T) {
	t.Parallel()

	msgs := []provider.Message{
		{Role: "user", Content: "old"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "x"}}},
		{Role: "tool", ToolCallID: "c1"},
		{Role: "user", Content: "recent"},
		{Role: "assistant", Content: "reply"},
	}

	// Under the cap: untouched.
	if got := TruncateMessages(msgs, 10); len(got) != len(msgs) {
		t.Errorf("under-cap truncation changed length: %d", len(got))
	}

	// Cap of 4 would open the window at the orphaned tool result; the safe
	// boundary walk must advance to the user message.
	got := TruncateMessages(msgs, 4)
	if len(got) != 2 || got[0].Content != "recent" {
		t.Errorf("truncated window = %+v, want [recent, reply]", got)
	}
}

func TestTailFromSafeBoundary(t *testing.T) {
	t.Parallel()

	t.Run("orphaned tool results dropped to assistant-with-calls", func(t *testing.T) {
		t.Parallel()
		msgs := []provider.Message{
			{Role: "tool", ToolCallID: "orphan"},
			{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "x"}}},
			{Role: "tool", ToolCallID: "c1"},
		}
		got := TailFromSafeBoundary(msgs)
		if len(got) != 2 || got[0].Role != "assistant" {
			t.Errorf("tail = %+v, want to open at the assistant-with-calls", got)
		}
	})

	t.Run("all tool results is unsalvageable", func(t *testing.T) {
		t.Parallel()
		msgs := []provider.Message{
			{Role: "tool", ToolCallID: "a"},
			{Role: "tool", ToolCallID: "b"},
		}
		if got := TailFromSafeBoundary(msgs); got != nil {
			t.Errorf("tail = %+v, want nil", got)
		}
	})
}
