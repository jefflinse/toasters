package operator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/provider"
)

const (
	// archiveKeep is how many pre-handoff session archives are retained.
	archiveKeep = 20

	// narrativeMaxTokens bounds the handoff narrative — a paragraph or two
	// of intent. The Go digest carries the facts.
	narrativeMaxTokens = 500

	// narrativeTimeout bounds the one-shot narrative call so a wedged
	// provider can't stall the event loop indefinitely mid-handoff.
	narrativeTimeout = 60 * time.Second

	// stripMaxMessages caps how much of the transcript feeds the narrative
	// call — the most recent exchanges carry the live intent.
	stripMaxMessages = 40
)

// maybeCompact performs a digest handoff when the operator's live context
// occupancy has crossed the compaction threshold. It runs only on the
// event-loop goroutine, between events, so it can never interleave a tool
// round. Disabled when the threshold is 0, the context window is unknown, or
// no occupancy has been reported yet.
func (o *Operator) maybeCompact(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	threshold := int(o.compactionThreshold.Load())
	if threshold <= 0 || o.ctxWindows == nil {
		return
	}
	providerName := o.providerID
	if providerName == "" {
		providerName = o.prov.Name()
	}
	window := o.ctxWindows.Window(providerName, o.model)
	if window <= 0 {
		return
	}

	o.mu.Lock()
	occupancy := o.lastContextTokens
	msgCount := len(o.messages)
	o.mu.Unlock()
	if occupancy <= 0 || msgCount <= 1 {
		return
	}
	if occupancy*100 < window*threshold {
		return
	}

	o.compact(ctx, occupancy, window)
}

// compact performs the handoff: archive the session, build the Go state
// digest and the LLM narrative, seed a fresh history, persist, and emit
// EventCompaction. Archive failure aborts the handoff (retried at the next
// event boundary); narrative failure degrades to a digest-only handoff — the
// digest is Go-owned state and must never depend on an LLM call succeeding.
func (o *Operator) compact(ctx context.Context, beforeTokens, window int) {
	archiveFile, err := o.archiveSession()
	if err != nil {
		slog.Error("operator handoff aborted: archiving session failed", "error", err)
		return
	}

	digest := buildDigest(ctx, o.store, o.now)
	narrative := o.buildNarrative(ctx)

	var b strings.Builder
	b.WriteString("# Operator handoff\n\n")
	b.WriteString("This session resumes from a digest handoff: the previous conversation ")
	b.WriteString("reached its context budget and was archived. The orchestration state ")
	b.WriteString("below is authoritative (Go-owned, from the database); use your tools ")
	b.WriteString("to look up anything not covered.\n\n")
	b.WriteString(digest)
	if narrative != "" {
		b.WriteString("\n\n## Handoff note from the previous session\n\n")
		b.WriteString(narrative)
	}
	handoff := b.String()

	seeded := []provider.Message{{Role: "user", Content: handoff}}
	estimatedAfter := estimateTokens(seeded)

	o.mu.Lock()
	o.messages = seeded
	// The estimate stands in until the next round-trip reports the real
	// occupancy. It also keeps maybeCompact from re-triggering on the old
	// count at the next event boundary.
	o.lastContextTokens = estimatedAfter
	o.compactionCount++
	o.mu.Unlock()
	o.persistSession()

	slog.Info("operator performed digest handoff",
		"before_tokens", beforeTokens, "estimated_after_tokens", estimatedAfter,
		"window", window, "archive", archiveFile)
	o.postFeedEntry(ctx, db.FeedEntrySystemEvent,
		fmt.Sprintf("Operator compacted its session (%d → ~%d tokens); previous conversation archived as %s.",
			beforeTokens, estimatedAfter, archiveFile), "")

	if o.onEvent != nil {
		o.onEvent(Event{Type: EventCompaction, Payload: CompactionPayload{
			BeforeTokens:         beforeTokens,
			EstimatedAfterTokens: estimatedAfter,
			ArchiveFile:          archiveFile,
		}})
	}
}

// archiveSession writes the current session snapshot to the archive
// directory next to the session file and prunes old archives. Returns the
// archive basename.
func (o *Operator) archiveSession() (string, error) {
	if o.sessionFile == "" {
		return "", fmt.Errorf("no session file configured")
	}
	// RFC3339 with ':' replaced so names sort lexicographically and stay
	// filesystem-portable.
	name := "operator-" + o.now().UTC().Format("2006-01-02T15-04-05Z") + ".json"
	dir := filepath.Join(filepath.Dir(o.sessionFile), "archive")
	if err := o.writeSessionFile(filepath.Join(dir, name)); err != nil {
		return "", err
	}
	pruneArchives(dir, archiveKeep)
	return name, nil
}

// pruneArchives removes all but the newest keep operator archives from dir.
// Names embed a sortable UTC timestamp, so lexicographic order is age order.
func pruneArchives(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "operator-") && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return
	}
	sort.Strings(names)
	for _, name := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			slog.Warn("failed to prune operator archive", "file", name, "error", err)
		}
	}
}

// buildNarrative makes a one-shot, stateless call on the operator's own
// model asking the previous session to write a short handoff note. Returns
// "" on any failure — the handoff proceeds digest-only.
func (o *Operator) buildNarrative(ctx context.Context) string {
	if o.promptEngine == nil {
		return ""
	}
	system, err := o.promptEngine.Compose("operator-handoff", nil, nil)
	if err != nil || system == "" {
		slog.Warn("handoff narrative skipped: no operator-handoff role", "error", err)
		return ""
	}

	o.mu.Lock()
	msgs := make([]provider.Message, len(o.messages))
	copy(msgs, o.messages)
	o.mu.Unlock()
	transcript := stripTranscript(msgs, stripMaxMessages)
	if transcript == "" {
		return ""
	}

	callCtx, cancel := context.WithTimeout(ctx, narrativeTimeout)
	defer cancel()
	narrative, err := provider.Complete(callCtx, o.prov, provider.ChatRequest{
		Model:     o.model,
		System:    system,
		MaxTokens: narrativeMaxTokens,
		Messages: []provider.Message{{
			Role:    "user",
			Content: "Transcript of the session being handed off:\n\n" + transcript,
		}},
	})
	if err != nil {
		slog.Warn("handoff narrative skipped: completion failed", "error", err)
		return ""
	}
	return narrative
}

// stripTranscript renders the most recent maxMsgs messages as plain text for
// the narrative call: user/assistant text is kept, tool calls are reduced to
// their names, and tool results are elided entirely — the narrative is about
// intent, and the digest already carries the state the results described.
func stripTranscript(msgs []provider.Message, maxMsgs int) string {
	if len(msgs) > maxMsgs {
		msgs = msgs[len(msgs)-maxMsgs:]
	}
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case "user":
			b.WriteString("USER: " + m.Content + "\n")
		case "assistant":
			if m.Content != "" {
				b.WriteString("OPERATOR: " + m.Content + "\n")
			}
			for _, tc := range m.ToolCalls {
				b.WriteString("OPERATOR: [called " + tc.Name + "]\n")
			}
		}
		// Tool results (role "tool") are elided.
	}
	return strings.TrimSpace(b.String())
}
