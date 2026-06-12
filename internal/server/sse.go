package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

// maxSSEConns is the maximum number of concurrent SSE connections.
// Generous for a single-user tool; prevents goroutine/FD exhaustion.
const maxSSEConns = 10

// events handles the SSE event stream endpoint (GET /api/v1/events).
// It subscribes to the service event stream and fans out events to the
// HTTP response in SSE format.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if s.sseConns.Add(1) > maxSSEConns {
		s.sseConns.Add(-1)
		writeError(w, http.StatusTooManyRequests, "too_many_requests",
			"too many SSE connections")
		return
	}
	defer s.sseConns.Add(-1)

	// Set SSE headers BEFORE the first Flush — flushing commits the response,
	// so headers set afterwards are silently dropped and spec-compliant
	// consumers (e.g. browser EventSource) reject the stream for missing
	// Content-Type: text/event-stream.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

	// Use ResponseController to access Flush through middleware wrappers.
	// This traverses Unwrap() chains to find the underlying http.Flusher.
	// When no flusher exists, Flush returns ErrNotSupported without writing
	// anything, so the error response below is still deliverable. When it
	// succeeds, it commits the 200 + SSE headers — that IS the stream start.
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		w.Header().Del("Content-Type")
		w.Header().Del("Cache-Control")
		w.Header().Del("Connection")
		w.Header().Del("X-Accel-Buffering")
		writeError(w, http.StatusInternalServerError, "internal_error",
			"streaming not supported")
		return
	}

	// Create a cancellable context for this SSE connection.
	ctx, cancel := context.WithCancel(r.Context())

	// Register this connection for cleanup during shutdown.
	conn := &sseConn{cancel: cancel}
	s.sseConnTracker.mu.Lock()
	s.sseConnTracker.conns[conn] = struct{}{}
	s.sseConnTracker.mu.Unlock()

	defer func() {
		cancel()
		s.sseConnTracker.mu.Lock()
		delete(s.sseConnTracker.conns, conn)
		s.sseConnTracker.mu.Unlock()
	}()

	// Subscribe to the service event stream. The subscription is cancelled
	// when the client disconnects (ctx is cancelled).
	ch := s.svc.Events().Subscribe(ctx)

	reqID := requestIDFromContext(ctx)
	slog.Info("SSE client connected", "request_id", reqID)
	defer slog.Info("SSE client disconnected", "request_id", reqID)

	// lastSent is the global service sequence number of the last event
	// written to this connection — events carry their service-assigned Seq
	// on the wire (id: line + envelope), so clients can dedupe and resume.
	var lastSent uint64

	// Last-Event-ID resume: replay buffered events the client missed during
	// a reconnect blip. Best-effort — anything older than the ring is
	// recovered by the client's snapshot resync. The subscription above is
	// already live, so events landing in both the ring and the channel are
	// deduped by the lastSent guard in the loop below.
	if lastID := r.Header.Get("Last-Event-ID"); lastID != "" {
		if seq, err := strconv.ParseUint(lastID, 10, 64); err == nil {
			missed := s.eventRing.since(seq)
			slog.Info("SSE resume", "request_id", reqID, "last_event_id", seq, "replayed", len(missed))
			for _, ev := range missed {
				if err := writeSSEEvent(w, rc, ev); err != nil {
					slog.Debug("SSE replay write error", "error", err, "request_id", reqID)
					return
				}
				lastSent = ev.Seq
			}
		}
	}

	// Heartbeats are produced by LocalService.heartbeatLoop and arrive on the
	// subscription channel like any other event. No SSE-side ticker is needed
	// (a previous version had one, which delivered double heartbeats).
	for {
		select {
		case <-ctx.Done():
			return

		case ev, ok := <-ch:
			if !ok {
				// Channel closed — subscriber removed.
				return
			}
			// Seq 0 means the producer didn't assign one (synthetic/test
			// events) — deliver those unconditionally.
			if ev.Seq != 0 && ev.Seq <= lastSent {
				continue // already delivered during replay
			}
			if err := writeSSEEvent(w, rc, ev); err != nil {
				slog.Debug("SSE write error", "error", err, "request_id", reqID)
				return
			}
			lastSent = ev.Seq
		}
	}
}

// sseWriteTimeout bounds each individual SSE event write. A stalled client
// (full TCP send buffer, half-dead connection) would otherwise park the
// handler in a kernel write indefinitely, defeating graceful shutdown.
// Rolling per-write deadlines keep idle-but-healthy connections alive
// regardless of event spacing.
const sseWriteTimeout = 30 * time.Second

// writeSSEEvent writes a single SSE event to the response writer and flushes.
// The wire seq is the event's GLOBAL service-assigned sequence number — a
// per-connection counter would make Last-Event-ID resume and client-side
// dedupe impossible.
func writeSSEEvent(w http.ResponseWriter, rc *http.ResponseController, ev service.Event) error {
	// Fresh deadline per write — events may be arbitrarily far apart, so an
	// absolute deadline set once at connect would expire on healthy streams.
	_ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
	wirePayload := EventPayloadToWire(ev)

	envelope := SSEEvent{
		Seq:         ev.Seq,
		Type:        string(ev.Type),
		Timestamp:   ev.Timestamp,
		TurnID:      ev.TurnID,
		SessionID:   ev.SessionID,
		OperationID: ev.OperationID,
		Payload:     wirePayload,
	}

	data, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshaling SSE event: %w", err)
	}

	// Write SSE format: id, event, data, blank line.
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, data); err != nil {
		return fmt.Errorf("writing SSE event: %w", err)
	}
	if err := rc.Flush(); err != nil {
		return fmt.Errorf("flushing SSE event: %w", err)
	}
	return nil
}
