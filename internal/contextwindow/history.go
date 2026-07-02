package contextwindow

import "github.com/jefflinse/toasters/internal/provider"

// This file holds the shared message-history primitives that both the
// operator's digest handoff and worker-session compaction rely on. They live
// here — the lowest layer that already imports internal/provider — because
// internal/runtime cannot import internal/operator.

// EstimateTokens roughly estimates the token footprint of a message history
// (bytes/4). Used when a provider-reported count isn't available; the next
// round-trip replaces it with the real value.
func EstimateTokens(msgs []provider.Message) int {
	var bytes int
	for _, m := range msgs {
		bytes += len(m.Content) + len(m.ToolCallID)
		for _, tc := range m.ToolCalls {
			bytes += len(tc.Name) + len(tc.Arguments)
		}
	}
	return bytes / 4
}

// TruncateMessages trims a conversation history to at most maxMessages,
// ensuring the window never starts in the middle of a tool-call/result
// exchange. Naive truncation (messages[len-max:]) can split a tool-call from
// its tool-result, corrupting the LLM conversation.
func TruncateMessages(messages []provider.Message, maxMessages int) []provider.Message {
	if len(messages) <= maxMessages {
		return messages
	}
	return TailFromSafeBoundary(messages[len(messages)-maxMessages:])
}

// TailFromSafeBoundary advances a message window to the first tool-pair-safe
// starting point: a user message, or an assistant message with no tool
// calls. If neither exists (tool-heavy autonomous stretches where every
// assistant message carries tool calls), it drops leading orphaned tool
// results so the window opens at an assistant-with-tool-calls message whose
// results immediately follow — pairing stays intact even though the window
// opens mid-turn. A window that is nothing but tool results is unsalvageable
// and returns nil.
func TailFromSafeBoundary(msgs []provider.Message) []provider.Message {
	for i, msg := range msgs {
		if msg.Role == "user" {
			return msgs[i:]
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 {
			return msgs[i:]
		}
	}
	for i, msg := range msgs {
		if msg.Role != "tool" {
			return msgs[i:]
		}
	}
	return nil
}
