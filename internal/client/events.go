package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"time"

	"github.com/jefflinse/toasters/internal/service"
	"github.com/jefflinse/toasters/internal/sse"
)

const (
	sseChannelBuffer = 256
	sseBasePath      = "/api/v1/events"

	// Reconnect backoff parameters.
	reconnectBaseDelay = 1 * time.Second
	reconnectMaxDelay  = 30 * time.Second
)

// remoteEventService implements service.EventService over SSE with
// auto-reconnection and exponential backoff.
type remoteEventService struct{ c *RemoteClient }

// Subscribe returns a channel that delivers all service events from the
// server's SSE stream. The channel survives reconnects — when the SSE
// connection drops, the background goroutine reconnects with exponential
// backoff and emits a synthetic progress.update event to resynchronize
// client state. The channel is closed when ctx is cancelled.
func (s *remoteEventService) Subscribe(ctx context.Context) <-chan service.Event {
	ch := make(chan service.Event, sseChannelBuffer)
	go s.eventLoop(ctx, ch)
	return ch
}

// eventLoop is the top-level reconnect loop. It connects to the SSE stream,
// reads events until the connection drops, then reconnects with exponential
// backoff. Exits when ctx is cancelled.
func (s *remoteEventService) eventLoop(ctx context.Context, ch chan<- service.Event) {
	defer close(ch)
	defer s.c.connected.Store(false)

	delay := reconnectBaseDelay

	for {
		// Check context before connecting.
		if ctx.Err() != nil {
			return
		}

		// Connect and read from the SSE stream until it ends.
		err := s.readSSE(ctx, ch)

		// If context was cancelled, exit cleanly.
		if ctx.Err() != nil {
			return
		}

		// Connection dropped — start reconnect.
		slog.Warn("SSE connection lost, reconnecting...", "error", err)
		s.c.connected.Store(false)

		// Backoff loop: try to reconnect with exponential backoff + jitter.
		for {
			if ctx.Err() != nil {
				return
			}

			// Wait with 10% jitter.
			jitter := time.Duration(rand.Int63n(int64(delay / 10)))
			timer := time.NewTimer(delay + jitter)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}

			slog.Info("attempting SSE reconnect", "delay", delay)

			// On successful reconnect, emit synthetic progress.update so the
			// TUI can rebuild its state from the current server snapshot.
			if err := s.emitReconnectState(ctx, ch); err != nil {
				slog.Warn("reconnect state fetch failed", "error", err)
				delay = min(delay*2, reconnectMaxDelay)
				continue
			}

			// Reset backoff and break to outer loop to resume SSE reading.
			delay = reconnectBaseDelay
			break
		}
	}
}

// readSSE connects to the SSE endpoint and reads events until the stream ends
// or ctx is cancelled. It sets connected=true on successful connection and
// sends parsed events to ch with non-blocking sends.
func (s *remoteEventService) readSSE(ctx context.Context, ch chan<- service.Event) error {
	// Build SSE request manually — we need Accept: text/event-stream and
	// don't want the httpTransport's JSON decoding.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.c.baseURL+sseBasePath, nil)
	if err != nil {
		return fmt.Errorf("creating SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := s.c.http.client.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to SSE: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SSE endpoint returned status %d", resp.StatusCode)
	}

	s.c.connected.Store(true)
	slog.Info("SSE connection established")

	reader := sse.NewReader(resp.Body)
	for {
		ev, ok := reader.Next(ctx)
		if !ok {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if reader.Err() != nil {
				return fmt.Errorf("SSE reader error: %w", reader.Err())
			}
			return fmt.Errorf("SSE stream ended")
		}

		// Parse the JSON envelope.
		var envelope sseEvent
		if err := json.Unmarshal([]byte(ev.Data), &envelope); err != nil {
			slog.Warn("failed to parse SSE envelope", "error", err, "data", ev.Data)
			continue
		}

		// Parse the typed payload.
		payload, err := parseSSEPayload(envelope.Type, envelope.Payload)
		if err != nil {
			slog.Warn("failed to parse SSE payload", "error", err, "type", envelope.Type)
			continue
		}

		// Construct service event.
		svcEvent := service.Event{
			Seq:         envelope.Seq,
			Type:        service.EventType(envelope.Type),
			Timestamp:   envelope.Timestamp,
			TurnID:      envelope.TurnID,
			SessionID:   envelope.SessionID,
			OperationID: envelope.OperationID,
			Payload:     payload,
		}

		// Non-blocking send — drop if channel is full to prevent blocking
		// the SSE reader goroutine.
		select {
		case ch <- svcEvent:
		default:
			slog.Warn("dropping SSE event, channel full", "type", envelope.Type, "seq", envelope.Seq)
		}
	}
}

// emitReconnectState fetches the current progress state via REST and emits a
// synthetic progress.update event so the TUI can rebuild its state after
// reconnection.
func (s *remoteEventService) emitReconnectState(ctx context.Context, ch chan<- service.Event) error {
	ps, err := s.c.system.GetProgressState(ctx)
	if err != nil {
		return fmt.Errorf("fetching progress state: %w", err)
	}

	slog.Info("SSE reconnected, emitting synthetic progress.update")

	// Emit synthetic progress.update event.
	select {
	case ch <- service.Event{
		Type:      service.EventTypeProgressUpdate,
		Timestamp: time.Now(),
		Payload:   service.ProgressUpdatePayload{State: ps},
	}:
	default:
		slog.Warn("dropping synthetic progress.update, channel full")
	}

	return nil
}
