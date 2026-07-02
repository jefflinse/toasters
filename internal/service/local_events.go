package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jefflinse/toasters/internal/db"
)

// subscribe registers a new subscriber and returns its channel. The channel is
// closed when ctx is cancelled. The background goroutines (progress poll,
// heartbeat) are started lazily on the first call.
func (s *LocalService) subscribe(ctx context.Context) <-chan Event {
	ch := make(chan Event, 256)

	s.mu.Lock()
	id := s.nextSubID
	s.nextSubID++
	s.subscribers[id] = ch
	s.mu.Unlock()

	// Start background goroutines on first subscription.
	s.startOnce.Do(func() {
		go s.progressPollLoop()
		go s.heartbeatLoop()
	})

	// Remove subscriber when either the subscriber's context or the service
	// context is cancelled, whichever comes first.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic in subscriber cleanup", "panic", fmt.Sprintf("%v", r))
			}
		}()
		select {
		case <-ctx.Done():
		case <-s.ctx.Done():
		}
		s.mu.Lock()
		delete(s.subscribers, id)
		close(ch) // close under the same lock to prevent use-after-close
		s.mu.Unlock()
	}()

	return ch
}

// broadcast sends an event to all subscribers. Non-blocking: drops events on
// overflow rather than blocking the caller.
func (s *LocalService) broadcast(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seqCounter++
	ev.Seq = s.seqCounter
	ev.Timestamp = time.Now()
	for _, ch := range s.subscribers {
		select {
		case ch <- ev:
		default:
			// Drop on overflow — slow consumer.
		}
	}
}

// progressPollLoop periodically broadcasts a full progress snapshot to keep
// the panel views in sync with SQLite.
//
// As of Phase 4, the chat/feed area is driven by dedicated push events
// (job.created, task.created, task.assigned, task.started, task.completed,
// etc.) so the user sees real-time activity even between poll ticks. The
// poll continues to drive the Jobs / Tasks / Feed panels because they read
// from a snapshot rather than a stream.
//
// TODO(future): replace this loop entirely with broadcasts on every state
// change site (DB-side updates, MCP status changes, feed inserts) and delete
// the EventTypeProgressUpdate event type. The current 500ms cadence is fine
// for a single-user local tool, but it creates 2 SSE messages per second per
// connected client even when nothing is happening.
func (s *LocalService) progressPollLoop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			// Skip the work entirely if no one is listening. buildProgressState
			// touches the DB and runtime on every tick; with no subscribers it's
			// pure waste.
			s.mu.Lock()
			n := len(s.subscribers)
			s.mu.Unlock()
			if n == 0 {
				continue
			}

			// A tick that ran out of budget produced a partial snapshot;
			// broadcasting it would make panels "lose" jobs/tasks until the
			// next healthy tick. Skip it — the next tick retries.
			ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
			state, complete := s.buildProgressState(ctx)
			cancel()
			if !complete {
				slog.Debug("progress snapshot incomplete; skipping broadcast")
				continue
			}
			s.broadcast(Event{
				Type:    EventTypeProgressUpdate,
				Payload: ProgressUpdatePayload{State: state},
			})
		}
	}
}

// heartbeatLoop broadcasts EventTypeHeartbeat every 15 seconds.
func (s *LocalService) heartbeatLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.broadcast(Event{
				Type:    EventTypeHeartbeat,
				Payload: HeartbeatPayload{ServerTime: time.Now()},
			})
		}
	}
}

// progressSnapshotJobLimit bounds how many jobs a progress snapshot covers.
// The poll runs every 500ms; without a bound, months of accumulated jobs
// (each with a tasks + progress query) would blow the tick budget on every
// tick. Jobs are listed newest-first, so the bound drops only the oldest.
const progressSnapshotJobLimit = 100

// buildProgressState assembles the current ProgressState from SQLite and the
// runtime. complete is false when the context expired (or the job listing
// failed) before the snapshot was fully assembled — callers on the broadcast
// path should drop incomplete snapshots rather than present them as
// authoritative.
func (s *LocalService) buildProgressState(ctx context.Context) (state ProgressState, complete bool) {
	if s.cfg.Store == nil {
		return ProgressState{}, true
	}

	complete = true

	// Jobs (newest first, bounded).
	dbJobs, err := s.cfg.Store.ListJobs(ctx, db.JobFilter{Limit: progressSnapshotJobLimit})
	if err != nil {
		dbJobs = nil
		complete = false
	}
	for _, j := range dbJobs {
		state.Jobs = append(state.Jobs, dbJobToService(j))
	}

	// Tasks and progress per job.
	state.Tasks = make(map[string][]Task)
	state.Reports = make(map[string][]ProgressReport)
	for _, j := range dbJobs {
		if ctx.Err() != nil {
			break
		}
		dbTasks, err := s.cfg.Store.ListTasksForJob(ctx, j.ID)
		if err == nil {
			var tasks []Task
			for _, t := range dbTasks {
				tasks = append(tasks, dbTaskToService(t))
			}
			state.Tasks[j.ID] = tasks
		}
		dbProgress, err := s.cfg.Store.GetRecentProgress(ctx, j.ID, 5)
		if err == nil {
			var reports []ProgressReport
			for _, p := range dbProgress {
				reports = append(reports, dbProgressToService(p))
			}
			state.Reports[j.ID] = reports
		}
	}

	// Active sessions from DB.
	dbSessions, err := s.cfg.Store.GetActiveSessions(ctx)
	if err == nil {
		for _, sess := range dbSessions {
			state.ActiveSessions = append(state.ActiveSessions, dbWorkerSessionToService(sess))
		}
	}

	// Live snapshots from runtime.
	if s.cfg.Runtime != nil {
		state.LiveSnapshots = append(state.LiveSnapshots,
			s.sessionSnapshotsToService(s.cfg.Runtime.ActiveSessions())...)
	}

	// Active graph nodes (not runtime sessions — tracked here for reconnect).
	s.graphNodeMu.Lock()
	for _, gn := range s.activeGraphNodes {
		state.ActiveGraphNodes = append(state.ActiveGraphNodes, gn)
	}
	s.graphNodeMu.Unlock()
	sort.Slice(state.ActiveGraphNodes, func(i, j int) bool {
		return state.ActiveGraphNodes[i].StartedAt.Before(state.ActiveGraphNodes[j].StartedAt)
	})

	// Feed entries.
	dbFeed, err := s.cfg.Store.ListRecentFeedEntries(ctx, 50)
	if err == nil {
		for _, fe := range dbFeed {
			state.FeedEntries = append(state.FeedEntries, dbFeedEntryToService(fe))
		}
	}

	// MCP servers.
	if s.cfg.MCPManager != nil {
		for _, ss := range s.cfg.MCPManager.Servers() {
			state.MCPServers = append(state.MCPServers, mcpServerStatusToService(ss))
		}
	}

	if ctx.Err() != nil {
		complete = false
	}
	return state, complete
}

// Subscribe returns a channel that delivers all service events in order.
func (s *LocalService) Subscribe(ctx context.Context) <-chan Event {
	return s.subscribe(ctx)
}
