package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

// events handles the SSE event stream endpoint (GET /api/v1/events).
// It subscribes to the service event stream and fans out events to the
// HTTP response in SSE format.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	// Use ResponseController to access Flush through middleware wrappers.
	// This traverses Unwrap() chains to find the underlying http.Flusher.
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error",
			"streaming not supported")
		return
	}

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()

	// Subscribe to the service event stream. The subscription is cancelled
	// when the client disconnects (r.Context() is cancelled).
	ctx := r.Context()
	ch := s.svc.Events().Subscribe(ctx)

	// Per-connection sequence counter.
	var seq uint64

	// Heartbeat timer — 15 seconds.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	reqID := requestIDFromContext(ctx)
	slog.Info("SSE client connected", "request_id", reqID)
	defer slog.Info("SSE client disconnected", "request_id", reqID)

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

		case <-heartbeat.C:
			seq++
			hbEvent := service.Event{
				Type:      service.EventTypeHeartbeat,
				Timestamp: time.Now(),
				Payload:   service.HeartbeatPayload{ServerTime: time.Now()},
			}
			if err := writeSSEEvent(w, rc, seq, hbEvent); err != nil {
				slog.Debug("SSE heartbeat write error", "error", err, "request_id", reqID)
				return
			}
		}
	}
}

// writeSSEEvent writes a single SSE event to the response writer and flushes.
func writeSSEEvent(w http.ResponseWriter, rc *http.ResponseController, seq uint64, ev service.Event) error {
	wirePayload := eventPayloadToWire(ev)

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
