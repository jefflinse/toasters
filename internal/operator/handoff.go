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

	// narrativeTimeout bounds the one-shot narrative call. It runs on the
	// sole event-loop goroutine, so this is the ceiling on how long a
	// handoff can stall event processing — kept tight for that reason.
	narrativeTimeout = 30 * time.Second

	// stripMaxMessages caps how much of the transcript feeds the narrative
	// call — the most recent exchanges carry the live intent.
	stripMaxMessages = 40

	// stripMaxBytes caps the stripped transcript's total size. Message
	// count alone doesn't bound it: sessions near their context limit are
	// exactly where 40 messages can still approach the full window, and
	// the narrative call must fit comfortably alongside its prompt.
	stripMaxBytes = 16 * 1024

	// handoffHeader opens every seeded handoff message. stripTranscript
	// uses it to recognize (and skip) synthetic seeds so a second handoff's
	// narrative doesn't mine the previous digest as "user intent".
	handoffHeader = "# Operator handoff"
)

// maybeCompact performs a digest handoff when the operator's live context
// occupancy has crossed the compaction threshold. It runs only on the
// event-loop goroutine, between events, so it can never interleave a tool
// round. Disabled when the threshold is 0, the context window is unknown,
// no occupancy has been reported yet, or there is no session file to
// archive to (archiving before mutation is the handoff's safety contract).
func (o *Operator) maybeCompact(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	threshold := int(o.compactionThreshold.Load())
	if threshold <= 0 || o.ctxWindows == nil || o.sessionFile == "" {
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
		// Below threshold again (a real turn shrank the count, the window
		// grew, or the threshold rose): compaction is viable once more.
		o.compactionSuppressed = false
		return
	}
	if o.compactionSuppressed {
		return
	}

	o.compact(ctx, occupancy, window, threshold)
}

// compact performs the handoff: archive the session, build the Go state
// digest and the LLM narrative, seed a fresh history, persist, and emit
// EventCompaction. Archive failure aborts the handoff (retried at the next
// event boundary); narrative failure degrades to a digest-only handoff — the
// digest is Go-owned state and must never depend on an LLM call succeeding.
func (o *Operator) compact(ctx context.Context, beforeTokens, window, threshold int) {
	archiveFile, err := o.archiveSession()
	if err != nil {
		slog.Error("operator handoff aborted: archiving session failed", "error", err)
		return
	}

	digest := buildDigest(ctx, o.store, o.now)
	narrative := o.buildNarrative(ctx)

	var b strings.Builder
	b.WriteString(handoffHeader + "\n\n")
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

	// Floor guard: when the orchestration state itself doesn't fit under
	// the budget (small loaded window, busy state), a handoff can't help —
	// without this, every subsequent turn would pay the full digest +
	// narrative + archive cost for zero forward progress. Suppress until
	// occupancy drops below threshold on its own.
	if estimatedAfter*100 >= window*threshold {
		o.compactionSuppressed = true
		slog.Warn("operator handoff cannot fit under the compaction threshold; suppressing until state shrinks",
			"estimated_after_tokens", estimatedAfter, "window", window, "threshold_pct", threshold)
	}

	o.mu.Lock()
	o.messages = seeded
	// The estimate stands in until the next round-trip reports the real
	// occupancy. It also keeps maybeCompact from re-triggering on the old
	// count at the next event boundary.
	o.lastContextTokens = estimatedAfter
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
	dir := filepath.Join(filepath.Dir(o.sessionFile), "archive")
	// RFC3339 with ':' replaced so names sort lexicographically and stay
	// filesystem-portable. Timestamps have one-second resolution, so a
	// second handoff in the same second gets a -N suffix rather than
	// silently overwriting the first archive.
	stamp := o.now().UTC().Format("2006-01-02T15-04-05Z")
	name := "operator-" + stamp + ".json"
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			// Name is free (or the dir is unusable — writeSessionFile will
			// surface that as the real error; only err == nil means taken).
			break
		}
		name = fmt.Sprintf("operator-%s-%d.json", stamp, i)
	}
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
	transcript := stripTranscript(msgs, stripMaxMessages, stripMaxBytes)
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

// stripTranscript renders the most recent messages as plain text for the
// narrative call: user/assistant text is kept, tool calls are reduced to
// their names, and tool results are elided entirely — the narrative is about
// intent, and the digest already carries the state the results described.
// Bounded by both message count and total bytes (newest kept), and synthetic
// handoff seeds are skipped so a prior handoff's digest is never mined as
// user intent.
func stripTranscript(msgs []provider.Message, maxMsgs, maxBytes int) string {
	if len(msgs) > maxMsgs {
		msgs = msgs[len(msgs)-maxMsgs:]
	}
	// Render each message, then keep the newest lines that fit the byte
	// budget — the most recent exchanges carry the live intent.
	var lines []string
	for _, m := range msgs {
		switch m.Role {
		case "user":
			if strings.HasPrefix(m.Content, handoffHeader) {
				continue
			}
			lines = append(lines, "USER: "+m.Content)
		case "assistant":
			if m.Content != "" {
				lines = append(lines, "OPERATOR: "+m.Content)
			}
			for _, tc := range m.ToolCalls {
				lines = append(lines, "OPERATOR: [called "+tc.Name+"]")
			}
		}
		// Tool results (role "tool") are elided.
	}
	total := 0
	start := len(lines)
	for start > 0 && total+len(lines[start-1])+1 <= maxBytes {
		start--
		total += len(lines[start]) + 1
	}
	return strings.TrimSpace(strings.Join(lines[start:], "\n"))
}
