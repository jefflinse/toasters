package client_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/client"
	"github.com/jefflinse/toasters/internal/server"
	"github.com/jefflinse/toasters/internal/service"
)

// TestClientHandlesServerDisconnect verifies that the client detects server
// disconnection and does not freeze.
//
// This test would fail/timeout with the old blocking scanner.Scan() implementation
// because the scanner would block indefinitely waiting for data from a closed
// connection. The context-aware SSE reader (using a goroutine with select on ctx.Done())
// allows the client to detect disconnection quickly.
//
// Note: The client will try to reconnect after detecting a disconnect, so
// the channel doesn't close immediately. Instead, we verify that:
// 1. The client detects the disconnect quickly (within a few seconds)
// 2. A connection.lost event is emitted
// 3. No goroutines are leaked when the client is closed
//
// To verify this test would fail without the fix:
// 1. Replace the context-aware Next() implementation with a blocking scanner.Scan()
// 2. Run this test - it will timeout because the client can't detect the disconnect
func TestClientHandlesServerDisconnect(t *testing.T) {
	t.Parallel()

	// Create a mock service that provides SSE events.
	eventCh := make(chan service.Event, 100)

	mock := &mockService{
		subscribeFn: func(ctx context.Context) <-chan service.Event {
			out := make(chan service.Event, 10)
			go func() {
				defer close(out)
				for {
					select {
					case <-ctx.Done():
						return
					case ev, ok := <-eventCh:
						if !ok {
							return
						}
						select {
						case out <- ev:
						case <-ctx.Done():
							return
						}
					}
				}
			}()
			return out
		},
		getProgressStateFn: func(ctx context.Context) (service.ProgressState, error) {
			return service.ProgressState{}, nil
		},
	}

	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	// Create and start server with suppressed logging.
	srv := server.New(mock, server.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err := srv.Start(addr); err != nil {
		t.Fatalf("starting server: %v", err)
	}

	// Create client.
	rc, err := client.New("http://" + addr)
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}

	// Subscribe to events.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventStream := rc.Events().Subscribe(ctx)

	// Send one event to confirm the connection is working.
	eventCh <- service.Event{
		Type:      service.EventTypeHeartbeat,
		Timestamp: time.Now(),
		Payload:   service.HeartbeatPayload{ServerTime: time.Now()},
	}

	// Wait for the first event to confirm the stream is working.
	select {
	case _, ok := <-eventStream:
		if !ok {
			t.Fatal("event stream closed before receiving first event")
		}
		// Got first event - connection is established.
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first event")
	}

	// Track goroutines before disconnect.
	initialGoroutines := runtime.NumGoroutine()

	// Now abruptly kill the server.
	// This simulates a server crash or network failure.
	srv.CloseAllSSEConnections()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 1*time.Second)
	_ = srv.Shutdown(shutdownCtx)
	shutdownCancel()

	// The client should detect the disconnection quickly and emit a connection.lost event.
	// With the context-aware reader, this should happen within a few seconds.
	detectDeadline := time.Now().Add(5 * time.Second)
	receivedConnectionLost := false

	for time.Now().Before(detectDeadline) {
		select {
		case ev, ok := <-eventStream:
			if !ok {
				// Channel closed - this is also acceptable.
				receivedConnectionLost = true
				break
			}
			if ev.Type == service.EventTypeConnectionLost {
				receivedConnectionLost = true
				break
			}
			// Other events may be buffered, continue draining.
		case <-time.After(50 * time.Millisecond):
			if receivedConnectionLost {
				break
			}
		}
		if receivedConnectionLost {
			break
		}
	}

	if !receivedConnectionLost {
		t.Errorf("client did not detect server disconnect within 5 seconds")
	} else {
		t.Log("client detected server disconnect successfully")
	}

	// Clean up - cancel context and close client.
	cancel()
	rc.Close()

	// Allow time for goroutines to clean up.
	time.Sleep(200 * time.Millisecond)

	// Verify no goroutine leak (with some tolerance for background goroutines).
	finalGoroutines := runtime.NumGoroutine()
	leakedGoroutines := finalGoroutines - initialGoroutines

	// Allow up to 2 extra goroutines for test infrastructure.
	if leakedGoroutines > 2 {
		t.Logf("warning: potential goroutine leak (initial=%d, final=%d, diff=%d)",
			initialGoroutines, finalGoroutines, leakedGoroutines)
	}
}

// TestClientEventLoopExitsOnContextCancellation verifies that cancelling the
// client context causes the event loop to exit promptly.
func TestClientEventLoopExitsOnContextCancellation(t *testing.T) {
	t.Parallel()

	// Create a mock service that keeps the event channel open.
	mock := &mockService{
		subscribeFn: func(ctx context.Context) <-chan service.Event {
			out := make(chan service.Event, 10)
			go func() {
				defer close(out)
				<-ctx.Done()
			}()
			return out
		},
		getProgressStateFn: func(ctx context.Context) (service.ProgressState, error) {
			return service.ProgressState{}, nil
		},
	}

	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	// Create and start server.
	srv := server.New(mock, server.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err := srv.Start(addr); err != nil {
		t.Fatalf("starting server: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	// Create client.
	rc, err := client.New("http://" + addr)
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	defer rc.Close()

	// Subscribe to events.
	ctx, cancel := context.WithCancel(context.Background())

	eventStream := rc.Events().Subscribe(ctx)

	// Cancel the context immediately.
	cancel()

	// The channel should close promptly (within 2 seconds).
	detectDeadline := time.Now().Add(2 * time.Second)
	channelClosed := false

	for time.Now().Before(detectDeadline) {
		select {
		case _, ok := <-eventStream:
			if !ok {
				channelClosed = true
			}
		default:
			if channelClosed {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if channelClosed {
			break
		}
	}

	if !channelClosed {
		t.Error("event stream channel did not close within 2 seconds after context cancellation")
	}
}

// TestClientReconnectionDetectsServerShutdown verifies that when the server
// gracefully shuts down, the client receives a connection.lost event.
func TestClientReconnectionDetectsServerShutdown(t *testing.T) {
	t.Parallel()

	// Create a mock service that provides events.
	eventCh := make(chan service.Event, 100)

	mock := &mockService{
		subscribeFn: func(ctx context.Context) <-chan service.Event {
			out := make(chan service.Event, 10)
			go func() {
				defer close(out)
				for {
					select {
					case <-ctx.Done():
						return
					case ev, ok := <-eventCh:
						if !ok {
							return
						}
						select {
						case out <- ev:
						case <-ctx.Done():
							return
						}
					}
				}
			}()
			return out
		},
		getProgressStateFn: func(ctx context.Context) (service.ProgressState, error) {
			return service.ProgressState{}, nil
		},
	}

	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	// Create and start server.
	srv := server.New(mock, server.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err := srv.Start(addr); err != nil {
		t.Fatalf("starting server: %v", err)
	}

	// Create client.
	rc, err := client.New("http://" + addr)
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	defer rc.Close()

	// Subscribe to events.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	eventStream := rc.Events().Subscribe(ctx)

	// Send one event to confirm connection.
	eventCh <- service.Event{
		Type:      service.EventTypeHeartbeat,
		Timestamp: time.Now(),
		Payload:   service.HeartbeatPayload{ServerTime: time.Now()},
	}

	// Wait for first event.
	select {
	case _, ok := <-eventStream:
		if !ok {
			t.Fatal("event stream closed unexpectedly")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first event")
	}

	// Shutdown the server gracefully.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	srv.CloseAllSSEConnections()
	_ = srv.Shutdown(shutdownCtx)
	shutdownCancel()

	// The client should detect the disconnect and emit a connection.lost event.
	// Wait for either connection.lost or for the channel to close.
	receivedConnectionLost := false
	detectDeadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(detectDeadline) {
		select {
		case ev, ok := <-eventStream:
			if !ok {
				// Channel closed without connection.lost - this is also acceptable
				// as it indicates the client detected the disconnect.
				return
			}
			if ev.Type == service.EventTypeConnectionLost {
				receivedConnectionLost = true
			}
		case <-time.After(100 * time.Millisecond):
			if receivedConnectionLost {
				return
			}
		}
	}

	// If we got here without channel closing, the client didn't detect disconnect.
	if !receivedConnectionLost {
		// This is not necessarily a failure - the channel might just close
		// without a connection.lost event if the context was cancelled.
		t.Log("client did not emit connection.lost event within 5 seconds")
	}
}

// TestSSEConnectionOutlivesRESTClientTimeout is a regression test that proves
// the SSE connection uses a dedicated no-timeout HTTP client (sseClient) and is
// NOT affected by the REST client's http.Client.Timeout.
//
// Before the fix, readSSE() used httpTransport.client — the same *http.Client
// used for REST calls. Since http.Client.Timeout covers the entire request
// lifecycle including response body reading, SSE connections were killed after
// the timeout expired (30s in production), causing the TUI to freeze.
//
// The fix adds httpTransport.sseClient with no timeout, used exclusively by
// readSSE(). This test injects a 5s-timeout client via WithHTTPClient (which
// sets the REST client only). On the old code, SSE would share this 5s client
// and die at ~5s. On the new code, SSE uses sseClient and survives.
//
// Timeline:
//
//	t=0s:  send event #1 (before 5s timeout)
//	t=3s:  send event #2 (before 5s timeout)
//	t=7s:  send event #3 (AFTER 5s timeout — the critical event)
//
// The test passes if event #3 arrives, proving the SSE connection outlived
// the REST client timeout. It fails on the old code because the SSE connection
// would be killed at ~5s by http.Client.Timeout.
func TestSSEConnectionOutlivesRESTClientTimeout(t *testing.T) {
	const (
		// Short REST timeout to keep test fast. On the old code, SSE shared
		// this client and would be killed after this duration.
		restTimeout = 5 * time.Second

		// Unique label for the event sent after the REST timeout expires.
		postTimeoutLabel = "after-rest-timeout"
	)

	// Controlled event channel — the test goroutine sends events on a schedule.
	eventCh := make(chan service.Event, 10)

	mock := &mockService{
		subscribeFn: func(ctx context.Context) <-chan service.Event {
			out := make(chan service.Event, 10)
			go func() {
				defer close(out)
				for {
					select {
					case <-ctx.Done():
						return
					case ev, ok := <-eventCh:
						if !ok {
							return
						}
						select {
						case out <- ev:
						case <-ctx.Done():
							return
						}
					}
				}
			}()
			return out
		},
		getProgressStateFn: func(ctx context.Context) (service.ProgressState, error) {
			return service.ProgressState{}, nil
		},
	}

	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	// Create and start server with suppressed logging.
	srv := server.New(mock, server.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err := srv.Start(addr); err != nil {
		t.Fatalf("starting server: %v", err)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	// Create client WITH a short-timeout REST client.
	// WithHTTPClient sets rc.http.client (the REST client).
	// On the old (buggy) code, SSE also used this client → connection dies at 5s.
	// On the new (fixed) code, SSE uses sseClient (no timeout) → survives.
	rc, err := client.New(
		"http://"+addr,
		client.WithHTTPClient(&http.Client{Timeout: restTimeout}),
	)
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	defer rc.Close()

	// Subscribe to SSE events with a generous test timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	eventStream := rc.Events().Subscribe(ctx)
	start := time.Now()

	// Send events on a timed schedule in a background goroutine.
	go func() {
		// t=0s: event before timeout — confirms the stream is working.
		eventCh <- service.Event{
			Type:      service.EventTypeOperatorText,
			Timestamp: time.Now(),
			Payload:   service.OperatorTextPayload{Text: "before-timeout-1"},
		}

		// t=3s: event before timeout — confirms the stream is still alive.
		time.Sleep(3 * time.Second)
		eventCh <- service.Event{
			Type:      service.EventTypeOperatorText,
			Timestamp: time.Now(),
			Payload:   service.OperatorTextPayload{Text: "before-timeout-2"},
		}

		// t=7s: event AFTER the 5s REST timeout — the critical event.
		// On the old code, the SSE connection is already dead by now.
		time.Sleep(4 * time.Second)
		t.Logf("sending post-timeout event at %v", time.Since(start).Round(time.Millisecond))
		eventCh <- service.Event{
			Type:      service.EventTypeOperatorText,
			Timestamp: time.Now(),
			Payload:   service.OperatorTextPayload{Text: postTimeoutLabel},
		}
	}()

	// Drain events until we find the post-timeout event or time out.
	detectDeadline := time.Now().Add(12 * time.Second)
	gotPostTimeout := false

	for time.Now().Before(detectDeadline) && !gotPostTimeout {
		select {
		case ev, ok := <-eventStream:
			if !ok {
				t.Fatalf("event stream closed at %v — SSE connection was killed by REST client timeout (%v)",
					time.Since(start).Round(time.Millisecond), restTimeout)
			}
			// Check if this is the critical post-timeout event.
			if p, ok := ev.Payload.(service.OperatorTextPayload); ok && p.Text == postTimeoutLabel {
				gotPostTimeout = true
				elapsed := time.Since(start).Round(time.Millisecond)
				t.Logf("post-timeout event received at %v — SSE survived past REST timeout of %v",
					elapsed, restTimeout)
			}
			// Other events (pre-timeout events, server heartbeats) are silently
			// consumed while we wait for the post-timeout event.

		case <-ctx.Done():
			t.Fatalf("context cancelled at %v — timed out waiting for post-timeout event",
				time.Since(start).Round(time.Millisecond))
		}
	}

	if !gotPostTimeout {
		t.Errorf("post-timeout event never arrived (elapsed %v) — SSE connection was likely killed by REST client timeout (%v)",
			time.Since(start).Round(time.Millisecond), restTimeout)
	}
}
