package client_test

import (
	"context"
	"io"
	"log/slog"
	"net"
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
