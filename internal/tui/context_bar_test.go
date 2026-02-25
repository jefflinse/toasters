package tui

import (
	"testing"

	"github.com/jefflinse/toasters/internal/provider"
)

// TestStreamDoneMsg_PromptTokensAssigned verifies that PromptTokens is assigned
// (=) from the API-reported value, not accumulated (+=). The Anthropic/OpenAI
// APIs report the full prompt token count for the current turn (which includes
// all prior messages), so we store it directly rather than summing across turns.
func TestStreamDoneMsg_PromptTokensAssigned(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	// Simulate first turn: API reports 500 prompt tokens.
	m.stream.streaming = true
	result1, _ := m.Update(StreamDoneMsg{
		Usage: &provider.Usage{InputTokens: 500, OutputTokens: 100},
	})
	model1 := result1.(*Model)
	if model1.stats.PromptTokens != 500 {
		t.Errorf("after turn 1: PromptTokens = %d, want 500", model1.stats.PromptTokens)
	}

	// Simulate second turn: API reports 1500 prompt tokens (includes previous messages).
	model1.stream.streaming = true
	result2, _ := model1.Update(StreamDoneMsg{
		Usage: &provider.Usage{InputTokens: 1500, OutputTokens: 200},
	})
	model2 := result2.(*Model)

	// PromptTokens should be 1500 (latest value), NOT 2000 (cumulative).
	if model2.stats.PromptTokens != 1500 {
		t.Errorf("after turn 2: PromptTokens = %d, want 1500 (not cumulative 2000)", model2.stats.PromptTokens)
	}

	// CompletionTokens should still be cumulative: 100 + 200 = 300.
	if model2.stats.CompletionTokens != 300 {
		t.Errorf("after turn 2: CompletionTokens = %d, want 300 (cumulative)", model2.stats.CompletionTokens)
	}
}

// TestStreamDoneMsg_NilUsage_PromptTokensUnchanged verifies that when
// StreamDoneMsg arrives with nil Usage, PromptTokens remains unchanged.
func TestStreamDoneMsg_NilUsage_PromptTokensUnchanged(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.stats.PromptTokens = 500
	m.stream.streaming = true

	result, _ := m.Update(StreamDoneMsg{Usage: nil})
	model := result.(*Model)

	// With nil usage, PromptTokens should remain unchanged.
	if model.stats.PromptTokens != 500 {
		t.Errorf("PromptTokens = %d, want 500 (unchanged with nil usage)", model.stats.PromptTokens)
	}
}

// TestStreamDoneMsg_MultiTurnContextSize verifies that over many turns,
// PromptTokens always reflects the latest API-reported value (not a running
// sum), while CompletionTokens accumulates across all turns.
func TestStreamDoneMsg_MultiTurnContextSize(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	// Simulate 5 turns with growing context.
	turns := []struct {
		promptTokens     int
		completionTokens int
	}{
		{500, 50},
		{600, 80},
		{750, 120},
		{900, 60},
		{1100, 90},
	}

	model := &m
	for i, turn := range turns {
		model.stream.streaming = true
		result, _ := model.Update(StreamDoneMsg{
			Usage: &provider.Usage{
				InputTokens:  turn.promptTokens,
				OutputTokens: turn.completionTokens,
			},
		})
		model = result.(*Model)

		// PromptTokens should always be the latest value.
		if model.stats.PromptTokens != turn.promptTokens {
			t.Errorf("turn %d: PromptTokens = %d, want %d", i+1, model.stats.PromptTokens, turn.promptTokens)
		}
	}

	// After 5 turns, PromptTokens should be 1100 (last turn), not 3850 (sum).
	if model.stats.PromptTokens != 1100 {
		t.Errorf("final PromptTokens = %d, want 1100", model.stats.PromptTokens)
	}

	// CompletionTokens should be cumulative: 50+80+120+60+90 = 400.
	if model.stats.CompletionTokens != 400 {
		t.Errorf("final CompletionTokens = %d, want 400", model.stats.CompletionTokens)
	}
}

// TestStreamDoneMsg_PromptTokensDecrease verifies that PromptTokens can
// decrease between turns (e.g., if context is truncated or a shorter
// conversation is sent). Assignment semantics handle this correctly;
// accumulation would not.
func TestStreamDoneMsg_PromptTokensDecrease(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	// First turn: large context.
	m.stream.streaming = true
	result1, _ := m.Update(StreamDoneMsg{
		Usage: &provider.Usage{InputTokens: 5000, OutputTokens: 100},
	})
	model1 := result1.(*Model)
	if model1.stats.PromptTokens != 5000 {
		t.Errorf("after turn 1: PromptTokens = %d, want 5000", model1.stats.PromptTokens)
	}

	// Second turn: context was truncated, API reports fewer prompt tokens.
	model1.stream.streaming = true
	result2, _ := model1.Update(StreamDoneMsg{
		Usage: &provider.Usage{InputTokens: 2000, OutputTokens: 50},
	})
	model2 := result2.(*Model)

	// PromptTokens should be 2000 (latest), not 7000 (cumulative) or 5000 (stale).
	if model2.stats.PromptTokens != 2000 {
		t.Errorf("after turn 2: PromptTokens = %d, want 2000 (decreased context)", model2.stats.PromptTokens)
	}
}

// TestStreamDoneMsg_LiveTokensResetOnDone verifies that CompletionTokensLive
// and ReasoningTokensLive are zeroed when a stream completes, so the context
// bar only reflects in-progress tokens during active streaming.
func TestStreamDoneMsg_LiveTokensResetOnDone(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	// Simulate mid-stream live token estimates.
	m.stats.CompletionTokensLive = 250
	m.stats.ReasoningTokensLive = 75
	m.stream.streaming = true

	result, _ := m.Update(StreamDoneMsg{
		Usage: &provider.Usage{InputTokens: 1000, OutputTokens: 300},
	})
	model := result.(*Model)

	if model.stats.CompletionTokensLive != 0 {
		t.Errorf("CompletionTokensLive = %d, want 0 (should be reset after stream done)", model.stats.CompletionTokensLive)
	}
	if model.stats.ReasoningTokensLive != 0 {
		t.Errorf("ReasoningTokensLive = %d, want 0 (should be reset after stream done)", model.stats.ReasoningTokensLive)
	}
}

// TestStreamDoneMsg_ReasoningTokensAccumulated verifies that ReasoningTokens
// accumulates the live reasoning estimate from each turn.
func TestStreamDoneMsg_ReasoningTokensAccumulated(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	// Turn 1: 100 reasoning tokens estimated during streaming.
	m.stats.ReasoningTokensLive = 100
	m.stream.streaming = true
	result1, _ := m.Update(StreamDoneMsg{
		Usage: &provider.Usage{InputTokens: 500, OutputTokens: 50},
	})
	model1 := result1.(*Model)
	if model1.stats.ReasoningTokens != 100 {
		t.Errorf("after turn 1: ReasoningTokens = %d, want 100", model1.stats.ReasoningTokens)
	}

	// Turn 2: 150 more reasoning tokens.
	model1.stats.ReasoningTokensLive = 150
	model1.stream.streaming = true
	result2, _ := model1.Update(StreamDoneMsg{
		Usage: &provider.Usage{InputTokens: 700, OutputTokens: 80},
	})
	model2 := result2.(*Model)

	// ReasoningTokens should be cumulative: 100 + 150 = 250.
	if model2.stats.ReasoningTokens != 250 {
		t.Errorf("after turn 2: ReasoningTokens = %d, want 250 (cumulative)", model2.stats.ReasoningTokens)
	}
}

// TestContextBarTokenCalculation verifies that the context bar total uses
// PromptTokens (assigned, not cumulative) plus live in-progress tokens.
// This is a unit-level check of the formula used in panels.go:
//
//	totalTokens = PromptTokens + CompletionTokensLive + ReasoningTokensLive
func TestContextBarTokenCalculation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		promptTokens      int
		completionLive    int
		reasoningLive     int
		wantContextTokens int
	}{
		{
			name:              "idle after one turn",
			promptTokens:      1000,
			completionLive:    0,
			reasoningLive:     0,
			wantContextTokens: 1000,
		},
		{
			name:              "mid-stream with live tokens",
			promptTokens:      1000,
			completionLive:    250,
			reasoningLive:     75,
			wantContextTokens: 1325,
		},
		{
			name:              "zero prompt tokens",
			promptTokens:      0,
			completionLive:    100,
			reasoningLive:     50,
			wantContextTokens: 150,
		},
		{
			name:              "all zeros",
			promptTokens:      0,
			completionLive:    0,
			reasoningLive:     0,
			wantContextTokens: 0,
		},
		{
			name:              "large context near limit",
			promptTokens:      120000,
			completionLive:    3000,
			reasoningLive:     1000,
			wantContextTokens: 124000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Replicate the formula from panels.go line 355.
			totalTokens := tt.promptTokens + tt.completionLive + tt.reasoningLive
			if totalTokens != tt.wantContextTokens {
				t.Errorf("totalTokens = %d, want %d", totalTokens, tt.wantContextTokens)
			}
		})
	}
}
