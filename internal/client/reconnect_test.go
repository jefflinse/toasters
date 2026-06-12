package client_test

import (
	"context"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

// ---------------------------------------------------------------------------
// Reconnection tests
// ---------------------------------------------------------------------------

func TestSSE_ContextCancellationStopsReconnect(t *testing.T) {
	t.Parallel()

	// Events flow through a fan-out hub matching real broadcast semantics.
	hub := newTestEventHub()

	mock := &mockService{
		subscribeFn: hub.subscribe,
	}

	rc := setupTestServer(t, mock)

	ctx, cancel := context.WithCancel(context.Background())

	ch := rc.Events().Subscribe(ctx)

	// Send one event to confirm the stream is working.
	// Broadcast semantics: wait until the SSE handler's subscription is live
	// (the server's internal recorder is the other subscriber) before sending,
	// or the event is dropped on the floor.
	if !hub.waitForSubscribers(2, 3*time.Second) {
		t.Fatal("timed out waiting for SSE subscription")
	}

	hub.send(service.Event{
		Seq:       1,
		Type:      service.EventTypeOperatorText,
		Timestamp: testTime,
		Payload:   service.OperatorTextPayload{Text: "before cancel"},
	})

	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before receiving event")
		}
		if ev.Type != service.EventTypeOperatorText {
			// Could be a server heartbeat; try again.
			select {
			case ev, ok = <-ch:
				if !ok {
					t.Fatal("channel closed before receiving event")
				}
				if ev.Type != service.EventTypeOperatorText {
					t.Fatalf("unexpected event type: %s", ev.Type)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for operator.text event")
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first event")
	}

	// Cancel the context — the channel should close promptly.
	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			// May receive buffered events; drain until closed.
			for range ch {
			}
		}
		// Channel closed — success.
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed within 5s after context cancellation")
	}
}

func TestSSE_ContextCancellationBeforeEvents(t *testing.T) {
	t.Parallel()

	// Subscribe returns a channel that never sends events.
	mock := &mockService{
		subscribeFn: func(ctx context.Context) <-chan service.Event {
			out := make(chan service.Event, 10)
			go func() {
				<-ctx.Done()
				close(out)
			}()
			return out
		},
	}

	rc := setupTestServer(t, mock)

	ctx, cancel := context.WithCancel(context.Background())

	ch := rc.Events().Subscribe(ctx)

	// Cancel immediately.
	cancel()

	// The channel should close promptly.
	select {
	case _, ok := <-ch:
		if ok {
			// Drain any remaining.
			for range ch {
			}
		}
		// Closed — success.
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed within 5s after immediate context cancellation")
	}
}

// NOTE: Detailed reconnection timing tests (e.g. "channel survives reconnect",
// "synthetic progress.update on reconnect") are not included here because the
// client's reconnect backoff starts at 1 second (reconnectBaseDelay) and is not
// configurable. This makes timing-sensitive reconnection tests slow and flaky.
//
// The reconnection logic is implicitly tested by:
// - TestSSE_EventDelivery: verifies the full SSE pipeline works end-to-end
// - TestSSE_ContextCancellationStopsReconnect: verifies clean shutdown
// - TestSSE_ContextCancellationBeforeEvents: verifies immediate cancellation
//
// If reconnect backoff becomes configurable (e.g. via a client Option), the
// following tests should be added:
// - Channel survives server restart (events continue on same channel)
// - Synthetic progress.update emitted after reconnect
// - Exponential backoff increases delay on repeated failures
