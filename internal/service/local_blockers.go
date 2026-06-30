package service

import (
	"context"
	"sort"
	"time"

	"github.com/jefflinse/toasters/internal/graphexec"
)

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

// addBlocker stores a pending blocker and emits EventTypeBlockerAdded. The
// CreatedAt timestamp is stamped here so the queue can be ordered.
func (s *LocalService) addBlocker(b Blocker) {
	b.CreatedAt = time.Now()
	s.blockerMu.Lock()
	s.blockers[b.RequestID] = b
	s.blockerMu.Unlock()
	s.broadcast(Event{Type: EventTypeBlockerAdded, Payload: b})
}

// ResolveBlocker removes a pending blocker and emits EventTypeBlockerResolved.
// It is idempotent: resolving an unknown or already-resolved request is a
// no-op and emits nothing. Called at the broker.Ask return site (both the
// graph-node and operator paths) so a blocker is cleared whether the user
// answered or the waiting caller's context was cancelled.
func (s *LocalService) ResolveBlocker(requestID string) {
	s.blockerMu.Lock()
	_, existed := s.blockers[requestID]
	delete(s.blockers, requestID)
	s.blockerMu.Unlock()
	if !existed {
		return
	}
	s.broadcast(Event{Type: EventTypeBlockerResolved, Payload: BlockerResolvedPayload{RequestID: requestID}})
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
