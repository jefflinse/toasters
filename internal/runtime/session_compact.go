package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/contextwindow"
	"github.com/jefflinse/toasters/internal/provider"
)

const (
	// compactKeepTurns is how many recent assistant turns stay verbatim
	// through a compaction — the live working set of the conversation.
	compactKeepTurns = 4

	// summaryMaxTokens bounds the tier-2 summary response.
	summaryMaxTokens = 500

	// summaryTimeout bounds the tier-2 one-shot call; it runs on the
	// session's own goroutine, so this is the ceiling on how long a
	// compaction can stall the turn loop.
	summaryTimeout = 30 * time.Second

	// summaryInputMaxBytes caps the transcript fed to the summary call so
	// it fits in any plausible window.
	summaryInputMaxBytes = 16 * 1024

	// elidedStubPrefix marks tool results whose contents were removed by a
	// tier-1 compaction. Also the idempotence guard: already-stubbed
	// results are never re-elided.
	elidedStubPrefix = "[elided tool result"
)

// summaryFallbackPrompt is used when no worker-compaction role is available
// from the prompt engine — a worker with an overflowing history still needs
// relief, unlike the operator's narrative (which is optional garnish over
// the Go digest).
const summaryFallbackPrompt = "You are a worker whose conversation history is being " +
	"compacted to fit its context window. Summarize, in at most 300 words: the progress " +
	"made so far, decisions taken, files or artifacts touched, and what remains to be " +
	"done. Do not restate the task itself."

// maybeCompact runs the pre-flight compaction check: when the session's live
// context occupancy has crossed the threshold, compact the history before
// the next request. Called at the top of each Run() turn; disabled when the
// threshold is 0, the window is unknown, or no occupancy has been reported
// yet (turn 0).
func (s *Session) maybeCompact(ctx context.Context) {
	if s.compactionThreshold == nil || s.ctxWindows == nil {
		return
	}
	threshold := int(s.compactionThreshold.Load())
	if threshold <= 0 {
		return
	}
	window := s.ctxWindows.Window(s.providerName, s.model)
	if window <= 0 {
		return
	}
	occupancy := int(s.lastInputTokens.Load())
	if occupancy <= 0 {
		return
	}
	if occupancy*100 < window*threshold {
		// Back under threshold (compaction landed, or the window grew):
		// the floor guard can re-arm.
		s.compactionSuppressed = false
		return
	}
	if s.compactionSuppressed {
		return
	}
	s.compact(ctx, occupancy, window, threshold)
}

// forceCompact is the overflow backstop: the provider rejected the request
// as too large, so compact regardless of threshold/window knowledge and let
// the caller retry once. beforeTokens uses the byte estimate (not the stale
// provider-reported occupancy) so the tier heuristic compares like scales.
func (s *Session) forceCompact(ctx context.Context) {
	// window/threshold 0/0: compact unconditionally, tiers decided by size.
	s.compact(ctx, contextwindow.EstimateTokens(s.messages), 0, 0)
}

// compact performs tiered history compaction. Tier 1 elides aged tool-result
// contents (structure untouched). If the result still exceeds the budget —
// or the caller has no window to measure against — tier 2 replaces the
// history with the original task, a summary of progress, and the recent
// tail. The turn budget is unaffected; only the message history changes.
func (s *Session) compact(ctx context.Context, beforeTokens, window, threshold int) {
	overBudget := func(estimate int) bool {
		if window <= 0 || threshold <= 0 {
			// Backstop mode: no measurable budget, so tier 2 runs whenever
			// elision freed less than a quarter of the estimated size.
			// beforeTokens is estimate-scale here (see forceCompact), so
			// the comparison is like-for-like.
			return estimate*4 > beforeTokens*3
		}
		return estimate*100 >= window*threshold
	}

	elided, elidedCount := elideToolResults(s.messages, compactKeepTurns)
	newHistory := elided
	estimate := contextwindow.EstimateTokens(elided)
	tier := 1

	var supersededSeq int
	if overBudget(estimate) {
		tier = 2
		summary := s.summarizeHistory(ctx, elided)
		newHistory = rebuildHistory(elided, summary, compactKeepTurns)
		estimate = contextwindow.EstimateTokens(newHistory)
		// Everything persisted so far predates the rebuilt history; the
		// summary + marker rows appended below carry the live state.
		supersededSeq = s.seq
	} else if elidedCount == 0 {
		// Nothing to elide and still under budget: nothing to do. (Without
		// this, a threshold crossed by pure text growth would "compact"
		// no-op every turn until suppression kicked in.)
		if overBudget(contextwindow.EstimateTokens(s.messages)) {
			s.compactionSuppressed = true
			slog.Warn("worker compaction found nothing to elide; suppressing until occupancy drops",
				"session", s.id)
		}
		return
	}

	s.mu.Lock()
	s.messages = newHistory
	s.compactions++
	s.mu.Unlock()
	// The estimate stands in for occupancy until the next round-trip
	// reports the real value; it also keeps maybeCompact from re-firing on
	// the stale count.
	s.lastInputTokens.Store(int64(estimate))

	// Floor guard: when even the compacted history exceeds the budget, do
	// not retry every turn — it costs a summary call each time for no
	// forward progress.
	if overBudget(estimate) {
		s.compactionSuppressed = true
		slog.Warn("worker compaction cannot fit under the threshold; suppressing until occupancy drops",
			"session", s.id, "estimated_tokens", estimate, "window", window, "threshold_pct", threshold)
	}

	if tier == 2 {
		if summaryMsg := summaryMessage(newHistory); summaryMsg != "" {
			s.persistMessage(provider.Message{Role: "user", Content: summaryMsg})
		}
		if s.store != nil && supersededSeq > 0 {
			if err := s.store.MarkSessionMessagesSuperseded(context.Background(), s.id, supersededSeq); err != nil {
				slog.Warn("failed to flag superseded transcript rows", "session", s.id, "error", err)
			}
		}
	}
	s.persistMessage(provider.Message{
		Role: "system",
		Content: fmt.Sprintf("[compacted (tier %d): %s → ~%s tokens; %d tool result(s) elided]",
			tier, formatTokens(beforeTokens), formatTokens(estimate), elidedCount),
	})

	slog.Info("worker session compacted history",
		"session", s.id, "tier", tier, "before_tokens", beforeTokens,
		"estimated_after_tokens", estimate, "elided", elidedCount)
	s.emit(SessionEvent{SessionID: s.id, Type: SessionEventCompaction, Compaction: &CompactionEvent{
		Tier:                 tier,
		BeforeTokens:         beforeTokens,
		EstimatedAfterTokens: estimate,
	}})
}

// elideToolResults returns a copy of msgs in which tool-result contents
// older than the last keepTurns assistant turns are replaced with a short
// stub. Messages are never removed or reordered, so tool-call/result pairing
// cannot break. spawn_worker results are exempt (same reason as the 8KB
// truncation exemption: a child's synthesized output is load-bearing), as
// are already-elided stubs. Returns the new slice and how many results were
// elided.
func elideToolResults(msgs []provider.Message, keepTurns int) ([]provider.Message, int) {
	protect := protectedTailStart(msgs, keepTurns)

	// Map tool-call IDs to names — tool messages carry only the call ID.
	toolName := make(map[string]string)
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			toolName[tc.ID] = tc.Name
		}
	}

	out := make([]provider.Message, len(msgs))
	copy(out, msgs)
	elided := 0
	for i := 0; i < protect; i++ {
		m := out[i]
		if m.Role != "tool" {
			continue
		}
		name := toolName[m.ToolCallID]
		if name == "spawn_worker" || strings.HasPrefix(m.Content, elidedStubPrefix) {
			continue
		}
		if name == "" {
			name = "unknown"
		}
		out[i].Content = fmt.Sprintf("%s: %s]", elidedStubPrefix, name)
		elided++
	}
	return out, elided
}

// protectedTailStart returns the index where the protected zone begins: the
// keepTurns-th assistant message from the end. Everything at or after the
// index stays verbatim. Index 0 (protect everything) when the history has
// fewer assistant turns than keepTurns. The first message (the task) is
// always protected by its position — elision only touches role "tool".
func protectedTailStart(msgs []provider.Message, keepTurns int) int {
	turns := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			turns++
			if turns == keepTurns {
				return i
			}
		}
	}
	return 0
}

// rebuildHistory constructs the tier-2 history: the original task message,
// an optional summary of everything being dropped, and the recent tail
// split at a tool-pair-safe boundary.
func rebuildHistory(msgs []provider.Message, summary string, keepTurns int) []provider.Message {
	if len(msgs) == 0 {
		return msgs
	}
	tail := contextwindow.TailFromSafeBoundary(msgs[protectedTailStart(msgs, keepTurns):])

	out := make([]provider.Message, 0, len(tail)+2)
	out = append(out, msgs[0])
	if summary != "" {
		out = append(out, provider.Message{
			Role: "user",
			Content: "[Compaction summary — earlier conversation was compacted to fit the " +
				"context window. Progress so far:]\n\n" + summary,
		})
	}
	// The tail may begin with the task message itself on short histories —
	// don't duplicate it.
	if len(tail) > 0 && len(msgs) > 0 && len(tail) == len(msgs) {
		tail = tail[1:]
	}
	return append(out, tail...)
}

// summarizeHistory asks the session's own model for a progress summary via a
// stateless one-shot. Returns "" on any failure — tier 2 then degrades to
// task + tail without a summary. Never recurses into compaction.
func (s *Session) summarizeHistory(ctx context.Context, msgs []provider.Message) string {
	system := summaryFallbackPrompt
	if s.promptEngine != nil {
		if composed, err := s.promptEngine.Compose("worker-compaction", nil, nil); err == nil && composed != "" {
			system = composed
		}
	}

	transcript := renderTranscript(msgs, summaryInputMaxBytes)
	if transcript == "" {
		return ""
	}

	callCtx, cancel := context.WithTimeout(ctx, summaryTimeout)
	defer cancel()
	summary, err := provider.Complete(callCtx, s.prov, provider.ChatRequest{
		Model:     s.model,
		System:    system,
		MaxTokens: summaryMaxTokens,
		Messages: []provider.Message{{
			Role:    "user",
			Content: "Conversation being compacted:\n\n" + transcript,
		}},
	})
	if err != nil {
		slog.Warn("worker compaction summary failed; continuing without one",
			"session", s.id, "error", err)
		return ""
	}
	return summary
}

// renderTranscript renders messages as plain text for the summary call,
// keeping the newest lines within maxBytes. Tool results are reduced to
// their stubs or first line — the summary is about trajectory, not data.
func renderTranscript(msgs []provider.Message, maxBytes int) string {
	var lines []string
	for _, m := range msgs {
		switch m.Role {
		case "user":
			lines = append(lines, "TASK/INPUT: "+m.Content)
		case "assistant":
			if m.Content != "" {
				lines = append(lines, "WORKER: "+m.Content)
			}
			for _, tc := range m.ToolCalls {
				lines = append(lines, "WORKER: [called "+tc.Name+"]")
			}
		case "tool":
			first, _, _ := strings.Cut(m.Content, "\n")
			if len(first) > 200 {
				first = first[:200] + "…"
			}
			lines = append(lines, "RESULT: "+first)
		}
	}
	total := 0
	start := len(lines)
	for start > 0 && total+len(lines[start-1])+1 <= maxBytes {
		start--
		total += len(lines[start]) + 1
	}
	return strings.TrimSpace(strings.Join(lines[start:], "\n"))
}

// summaryMessage extracts the compaction-summary message from a rebuilt
// history for transcript persistence, if present.
func summaryMessage(msgs []provider.Message) string {
	for _, m := range msgs {
		if m.Role == "user" && strings.HasPrefix(m.Content, "[Compaction summary") {
			return m.Content
		}
	}
	return ""
}

// formatTokens renders a token count compactly (e.g. "41.2k").
func formatTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}
