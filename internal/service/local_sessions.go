package service

import (
	"context"
	"fmt"
)

// List returns all currently active worker sessions as snapshots.
func (s *localSessionService) List(_ context.Context) ([]SessionSnapshot, error) {
	if s.svc.cfg.Runtime == nil {
		return nil, nil
	}
	runtimeSnaps := s.svc.cfg.Runtime.ActiveSessions()
	snaps := make([]SessionSnapshot, 0, len(runtimeSnaps))
	for _, rs := range runtimeSnaps {
		snaps = append(snaps, runtimeSnapshotToService(rs))
	}
	return snaps, nil
}

// Get returns a full SessionDetail for the given session ID.
func (s *localSessionService) Get(_ context.Context, id string) (SessionDetail, error) {
	if s.svc.cfg.Runtime == nil {
		return SessionDetail{}, Unavailablef("runtime not configured")
	}
	sess, ok := s.svc.cfg.Runtime.GetSession(id)
	if !ok {
		return SessionDetail{}, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}

	snap := sess.Snapshot()
	return SessionDetail{
		Snapshot:       runtimeSnapshotToService(snap),
		SystemPrompt:   sess.SystemPrompt(),
		InitialMessage: sess.InitialMessage(),
		Output:         sess.FinalText(),
		Activities:     nil, // deferred to Step 1.3
		WorkerName:     snap.WorkerID,
		Task:           sess.Task(),
	}, nil
}

// Cancel cancels the session with the given ID.
func (s *localSessionService) Cancel(_ context.Context, id string) error {
	if s.svc.cfg.Runtime == nil {
		return Unavailablef("runtime not configured")
	}
	return s.svc.cfg.Runtime.CancelSession(id)
}
