package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/db"
)

// digestRecentJobs is how many recently finished jobs the digest lists.
const digestRecentJobs = 5

// buildDigest renders the orchestration state a successor operator session
// needs, straight from the database — jobs, tasks, in-flight sessions, and
// pending blockers. It is the load-bearing half of a handoff: every section
// degrades independently on query failure (the LLM narrative is garnish, the
// digest must always produce something), and the output is deterministic
// given a fixed store and clock.
func buildDigest(ctx context.Context, store db.Store, now func() time.Time) string {
	var b strings.Builder
	b.WriteString("## Orchestration state\n")
	if store == nil {
		b.WriteString("\n(state store unavailable)\n")
		return b.String()
	}

	writeActiveJobs(ctx, &b, store)
	writePendingBlockers(ctx, &b, store, now)
	writeActiveSessions(ctx, &b, store, now)
	writeRecentJobs(ctx, &b, store)

	return strings.TrimRight(b.String(), "\n")
}

// writeActiveJobs renders every non-terminal job with its task rollup.
func writeActiveJobs(ctx context.Context, b *strings.Builder, store db.Store) {
	var active []*db.Job
	for _, status := range []db.JobStatus{db.JobStatusActive, db.JobStatusSettingUp, db.JobStatusPending, db.JobStatusPaused} {
		jobs, err := store.ListJobs(ctx, db.JobFilter{Status: &status})
		if err != nil {
			slog.Warn("handoff digest: listing jobs failed", "status", status, "error", err)
			fmt.Fprintf(b, "\n(job listing for status %q unavailable)\n", status)
			continue
		}
		active = append(active, jobs...)
	}

	b.WriteString("\n### Jobs in flight\n\n")
	if len(active) == 0 {
		b.WriteString("None.\n")
		return
	}
	for _, j := range active {
		fmt.Fprintf(b, "- **%s** (`%s`, %s): %s\n", j.Title, j.ID, j.Status, oneLine(j.Description))
		tasks, err := store.ListTasksForJob(ctx, j.ID)
		if err != nil {
			slog.Warn("handoff digest: listing tasks failed", "job", j.ID, "error", err)
			b.WriteString("  - (task listing unavailable)\n")
			continue
		}
		for _, t := range tasks {
			line := fmt.Sprintf("  - [%s] %s (`%s`)", t.Status, t.Title, t.ID)
			// Failure reasons are exactly what a successor must not lose.
			if t.Status == db.TaskStatusFailed && t.Summary != "" {
				line += " — " + oneLine(t.Summary)
			}
			b.WriteString(line + "\n")
		}
	}
}

// writePendingBlockers renders unresolved ask_user questions with their ages.
func writePendingBlockers(ctx context.Context, b *strings.Builder, store db.Store, now func() time.Time) {
	blockers, err := store.ListPendingBlockers(ctx)
	if err != nil {
		slog.Warn("handoff digest: listing pending blockers failed", "error", err)
		b.WriteString("\n### Pending questions to the user\n\n(unavailable)\n")
		return
	}
	if len(blockers) == 0 {
		return
	}
	b.WriteString("\n### Pending questions to the user\n\n")
	for _, r := range blockers {
		age := now().Sub(r.CreatedAt).Round(time.Minute)
		source := r.Source
		if source == "" {
			source = "operator"
		}
		fmt.Fprintf(b, "- [%s, waiting %s] %s\n", source, age, oneLine(blockerQuestions(r.Questions)))
	}
}

// writeActiveSessions renders in-flight worker sessions.
func writeActiveSessions(ctx context.Context, b *strings.Builder, store db.Store, now func() time.Time) {
	sessions, err := store.GetActiveSessions(ctx)
	if err != nil {
		slog.Warn("handoff digest: listing active sessions failed", "error", err)
		b.WriteString("\n### Workers in flight\n\n(unavailable)\n")
		return
	}
	if len(sessions) == 0 {
		return
	}
	b.WriteString("\n### Workers in flight\n\n")
	for _, s := range sessions {
		elapsed := now().Sub(s.StartedAt).Round(time.Second)
		fmt.Fprintf(b, "- %s on task `%s` (job `%s`, running %s)\n", s.WorkerID, s.TaskID, s.JobID, elapsed)
	}
}

// writeRecentJobs renders the most recently finished jobs for continuity.
func writeRecentJobs(ctx context.Context, b *strings.Builder, store db.Store) {
	var recent []*db.Job
	for _, status := range []db.JobStatus{db.JobStatusCompleted, db.JobStatusFailed} {
		jobs, err := store.ListJobs(ctx, db.JobFilter{Status: &status, Limit: digestRecentJobs})
		if err != nil {
			slog.Warn("handoff digest: listing recent jobs failed", "status", status, "error", err)
			continue
		}
		recent = append(recent, jobs...)
	}
	if len(recent) == 0 {
		return
	}
	b.WriteString("\n### Recently finished jobs\n\n")
	for _, j := range recent {
		fmt.Fprintf(b, "- **%s** (`%s`, %s)\n", j.Title, j.ID, j.Status)
	}
}

// blockerQuestions flattens a blocker's questions JSON ([{question, options}])
// into readable text, falling back to the raw JSON on parse failure.
func blockerQuestions(raw string) string {
	var qs []struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal([]byte(raw), &qs); err != nil || len(qs) == 0 {
		return raw
	}
	parts := make([]string, 0, len(qs))
	for _, q := range qs {
		parts = append(parts, q.Question)
	}
	return strings.Join(parts, " | ")
}

// oneLine collapses text to a single trimmed line so digest bullets stay
// bullets even when descriptions contain newlines.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
