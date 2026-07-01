package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"time"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/graphexec"
)

// blockerOutcome is the answer/disposition recorded when a response is
// delivered through the broker, consumed by ResolveBlocker for persistence
// and the resolved event.
type blockerOutcome struct {
	disposition string
	answer      string
}

// BroadcastPrompt registers a blocker for a HITL question that originated
// inside a graph node (via rhizome.Interrupt) and emits EventTypeBlockerAdded.
// Source is typically "graph:<node>" so the client can attribute the asker.
// The operator's own ask_user path goes through BroadcastOperatorPrompt; both
// funnel into the same blocker queue without forking the event type.
func (s *LocalService) BroadcastPrompt(requestID string, questions []graphexec.PromptQuestion, source, jobID, taskID string) {
	qs := make([]PromptQuestion, len(questions))
	for i, q := range questions {
		qs[i] = PromptQuestion{Question: q.Question, Options: q.Options}
	}
	s.addBlocker(Blocker{RequestID: requestID, Source: source, JobID: jobID, TaskID: taskID, Questions: qs})
}

// BroadcastOperatorPrompt registers a blocker for an ask_user round raised by
// the operator (Source empty) and emits EventTypeBlockerAdded. It is the
// operator's OnPrompt callback, wired from BOTH the boot path (cmd/serve.go)
// and live activation (startOperator) so a missing wire on one path can't
// silently swallow operator prompts.
func (s *LocalService) BroadcastOperatorPrompt(requestID string, questions []graphexec.PromptQuestion) {
	qs := make([]PromptQuestion, len(questions))
	for i, q := range questions {
		qs[i] = PromptQuestion{Question: q.Question, Options: q.Options}
	}
	s.addBlocker(Blocker{RequestID: requestID, Questions: qs})
}

// addBlocker stores a pending blocker, persists it for history, and emits
// EventTypeBlockerAdded. The CreatedAt timestamp is stamped here so the queue
// can be ordered.
func (s *LocalService) addBlocker(b Blocker) {
	b.CreatedAt = time.Now()
	s.blockerMu.Lock()
	s.blockers[b.RequestID] = b
	s.blockerMu.Unlock()
	s.persistBlockerAdded(b)
	s.broadcast(Event{Type: EventTypeBlockerAdded, Payload: b})
}

// recordBlockerOutcome remembers how a blocker's answer was delivered
// (answered vs dismissed) so the ResolveBlocker call that follows the
// waiter's return can persist the outcome. Recorded BEFORE the broker
// delivers the response — the waiter may resolve immediately on delivery —
// and cleared again (clearBlockerOutcome) if delivery fails.
func (s *LocalService) recordBlockerOutcome(requestID, disposition, answer string) {
	s.blockerMu.Lock()
	s.blockerOutcomes[requestID] = blockerOutcome{disposition: disposition, answer: answer}
	s.blockerMu.Unlock()
}

// clearBlockerOutcome drops a provisionally recorded outcome after a failed
// broker delivery, so a later genuine resolution isn't mislabeled.
func (s *LocalService) clearBlockerOutcome(requestID string) {
	s.blockerMu.Lock()
	delete(s.blockerOutcomes, requestID)
	s.blockerMu.Unlock()
}

// ResolveBlocker removes a pending blocker, persists its outcome, and emits
// EventTypeBlockerResolved. It is idempotent: resolving an unknown or
// already-resolved request is a no-op and emits nothing. Called at the
// broker.Ask return site (both the graph-node and operator paths) so a
// blocker is cleared whether the user answered or the waiting caller's
// context was cancelled. The disposition comes from the outcome recorded by
// RespondToPrompt/DismissPrompt; no recorded outcome means the waiter went
// away on its own (cancelled).
func (s *LocalService) ResolveBlocker(requestID string) {
	s.blockerMu.Lock()
	_, existed := s.blockers[requestID]
	delete(s.blockers, requestID)
	outcome, hasOutcome := s.blockerOutcomes[requestID]
	delete(s.blockerOutcomes, requestID)
	s.blockerMu.Unlock()
	if !existed {
		return
	}
	if !hasOutcome {
		outcome = blockerOutcome{disposition: BlockerDispositionCancelled}
	}
	s.persistBlockerResolved(requestID, outcome)
	s.broadcast(Event{Type: EventTypeBlockerResolved, Payload: BlockerResolvedPayload{
		RequestID:   requestID,
		Disposition: outcome.disposition,
	}})
}

// Blockers returns a snapshot of the pending blockers, ordered oldest-first.
// Clients call this on connect/reconnect to hydrate the Blockers panel.
func (s *LocalService) Blockers(_ context.Context) ([]Blocker, error) {
	s.blockerMu.Lock()
	out := make([]Blocker, 0, len(s.blockers))
	for _, b := range s.blockers {
		out = append(out, b)
	}
	s.blockerMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// BlockerHistory returns resolved blockers, newest-first, up to limit
// (a non-positive limit applies the store default). Pending blockers are
// not included — those come from Blockers().
func (s *LocalService) BlockerHistory(ctx context.Context, limit int) ([]BlockerRecord, error) {
	if s.cfg.Store == nil {
		return nil, nil
	}
	rows, err := s.cfg.Store.ListBlockerHistory(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]BlockerRecord, 0, len(rows))
	for _, r := range rows {
		rec := BlockerRecord{
			Blocker: Blocker{
				RequestID: r.RequestID,
				Source:    r.Source,
				JobID:     r.JobID,
				TaskID:    r.TaskID,
				CreatedAt: r.CreatedAt,
			},
			ResolvedAt:  r.ResolvedAt,
			Disposition: r.Disposition,
			Answer:      r.Answer,
		}
		if err := json.Unmarshal([]byte(r.Questions), &rec.Questions); err != nil {
			slog.Warn("failed to decode blocker questions", "request_id", r.RequestID, "error", err)
		}
		out = append(out, rec)
	}
	return out, nil
}

// persistBlockerAdded records a raised blocker for history. Persistence is
// best-effort: the pending queue and events are the operational path, so a
// write failure only costs history, not the blocker itself.
func (s *LocalService) persistBlockerAdded(b Blocker) {
	if s.cfg.Store == nil {
		return
	}
	questions, err := json.Marshal(b.Questions)
	if err != nil {
		slog.Warn("failed to encode blocker questions", "request_id", b.RequestID, "error", err)
		questions = []byte("[]")
	}
	if err := s.cfg.Store.CreateBlocker(context.Background(), &db.BlockerRecord{
		RequestID: b.RequestID,
		Source:    b.Source,
		JobID:     b.JobID,
		TaskID:    b.TaskID,
		Questions: string(questions),
		CreatedAt: b.CreatedAt,
	}); err != nil {
		slog.Warn("failed to persist blocker", "request_id", b.RequestID, "error", err)
	}
}

// persistBlockerResolved records a blocker's outcome for history. Best-effort,
// same as persistBlockerAdded.
func (s *LocalService) persistBlockerResolved(requestID string, outcome blockerOutcome) {
	if s.cfg.Store == nil {
		return
	}
	if err := s.cfg.Store.ResolveBlockerRecord(context.Background(),
		requestID, outcome.disposition, outcome.answer, time.Now()); err != nil {
		slog.Warn("failed to persist blocker resolution", "request_id", requestID, "error", err)
	}
}
