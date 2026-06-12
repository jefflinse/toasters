package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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

	// Per-connection sequence counter.
	var seq uint64

	reqID := requestIDFromContext(ctx)
	slog.Info("SSE client connected", "request_id", reqID)
	defer slog.Info("SSE client disconnected", "request_id", reqID)

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
			seq++
			if err := writeSSEEvent(w, rc, seq, ev); err != nil {
				slog.Debug("SSE write error", "error", err, "request_id", reqID)
				return
			}
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
func writeSSEEvent(w http.ResponseWriter, rc *http.ResponseController, seq uint64, ev service.Event) error {
	// Fresh deadline per write — events may be arbitrarily far apart, so an
	// absolute deadline set once at connect would expire on healthy streams.
	_ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
	wirePayload := EventPayloadToWire(ev)

	envelope := SSEEvent{
		Seq:         seq,
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
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, ev.Type, data); err != nil {
		return fmt.Errorf("writing SSE event: %w", err)
	}
	if err := rc.Flush(); err != nil {
		return fmt.Errorf("flushing SSE event: %w", err)
	}
	return nil
}
