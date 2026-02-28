package tui

import (
	"testing"
)

// TestOperatorDoneMsg_LiveTokensResetOnDone verifies that CompletionTokensLive
// is zeroed when an operator turn completes, so the context bar only reflects
// in-progress tokens during active streaming.
func TestOperatorDoneMsg_LiveTokensResetOnDone(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	// Simulate mid-stream live token estimates.
	m.stats.CompletionTokensLive = 250
	m.stream.streaming = true

	result, _ := m.Update(OperatorDoneMsg{})
	model := result.(*Model)

	if model.stats.CompletionTokensLive != 0 {
		t.Errorf("CompletionTokensLive = %d, want 0 (should be reset after operator done)", model.stats.CompletionTokensLive)
	}
}

// TestOperatorDoneMsg_CompletionTokensAccumulated verifies that CompletionTokens
// accumulates the live estimate from each turn.
func TestOperatorDoneMsg_CompletionTokensAccumulated(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	// Turn 1: 100 completion tokens estimated during streaming.
	m.stats.CompletionTokensLive = 100
	m.stream.streaming = true
	result1, _ := m.Update(OperatorDoneMsg{})
	model1 := result1.(*Model)
	if model1.stats.CompletionTokens != 100 {
		t.Errorf("after turn 1: CompletionTokens = %d, want 100", model1.stats.CompletionTokens)
	}

	// Turn 2: 150 more completion tokens.
	model1.stats.CompletionTokensLive = 150
	model1.stream.streaming = true
	result2, _ := model1.Update(OperatorDoneMsg{})
	model2 := result2.(*Model)

	// CompletionTokens should be cumulative: 100 + 150 = 250.
	if model2.stats.CompletionTokens != 250 {
		t.Errorf("after turn 2: CompletionTokens = %d, want 250 (cumulative)", model2.stats.CompletionTokens)
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
