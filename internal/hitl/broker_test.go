package hitl

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestBroker_AskRespond_RoundTrip(t *testing.T) {
	b := New()
	answer := make(chan string, 1)

	go func() {
		resp, err := b.Ask(context.Background(), "req-1", func() {
			// Respond arrives on a separate goroutine; simulate a tiny
			// delay so we exercise the "Ask is already blocked when
			// Respond fires" ordering rather than the race ordering.
			go func() {
				time.Sleep(10 * time.Millisecond)
				if rerr := b.Respond("req-1", "hello"); rerr != nil {
					t.Errorf("Respond: %v", rerr)
				}
			}()
		})
		if err != nil {
			t.Errorf("Ask: %v", err)
		}
		answer <- resp
	}()

	select {
	case got := <-answer:
		if got != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Ask to return")
	}
}

func TestBroker_RespondBeforeAskBlocks(t *testing.T) {
	// broadcast fires Respond synchronously BEFORE Ask's select runs.
	// The cap-1 buffered channel absorbs the send so the goroutine
	// isn't blocked; then Ask's select consumes it. This exercises the
	// "register-before-broadcast" invariant.
	b := New()
	resp, err := b.Ask(context.Background(), "req-1", func() {
		if err := b.Respond("req-1", "fast"); err != nil {
			t.Errorf("Respond: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if resp != "fast" {
		t.Errorf("got %q, want %q", resp, "fast")
	}
}

func TestBroker_CtxCancel_ClearsPending(t *testing.T) {
	b := New()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := b.Ask(ctx, "req-1", func() {})
		done <- err
	}()

	// Let Ask register and block.
	time.Sleep(10 * time.Millisecond)

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Ask did not unblock on ctx cancel")
	}

	// The pending entry must be gone — a late Respond should report
	// "no pending request" rather than hang or succeed.
	if err := b.Respond("req-1", "too late"); err == nil {
		t.Error("expected Respond error after ctx cancel, got nil")
	}
}

func TestBroker_RespondUnknown(t *testing.T) {
	b := New()
	err := b.Respond("never-registered", "whatever")
	if err == nil {
		t.Fatal("expected error for unknown request ID")
	}
}

func TestBroker_DuplicateRequestID(t *testing.T) {
	b := New()

	done := make(chan error, 1)
	go func() {
		_, err := b.Ask(context.Background(), "req-1", func() {})
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)

	// Second Ask with the same ID must fail rather than clobber the
	// first channel.
	_, err := b.Ask(context.Background(), "req-1", func() {})
	if err == nil {
		t.Fatal("expected error on duplicate request ID")
	}

	// Clean up the still-pending first Ask.
	if rerr := b.Respond("req-1", "done"); rerr != nil {
		t.Fatalf("Respond: %v", rerr)
	}
	if ferr := <-done; ferr != nil {
		t.Errorf("first Ask returned error: %v", ferr)
	}
}

func TestBroker_ConcurrentAsks_DifferentIDs(t *testing.T) {
	b := New()
	const n = 20

	var wg sync.WaitGroup
	results := make([]string, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("req-%d", i)
			resp, err := b.Ask(context.Background(), id, func() {
				go func() {
					_ = b.Respond(id, fmt.Sprintf("answer-%d", i))
				}()
			})
			if err != nil {
				t.Errorf("Ask %s: %v", id, err)
				return
			}
			results[i] = resp
		}(i)
	}

	wg.Wait()
	for i, got := range results {
		want := fmt.Sprintf("answer-%d", i)
		if got != want {
			t.Errorf("results[%d] = %q, want %q", i, got, want)
		}
	}
}

func TestBroker_EmptyRequestID(t *testing.T) {
	b := New()
	_, err := b.Ask(context.Background(), "", func() {})
	if err == nil {
		t.Fatal("expected error for empty request ID")
	}
}

func TestBroker_DoubleRespond(t *testing.T) {
	b := New()

	done := make(chan error, 1)
	go func() {
		_, err := b.Ask(context.Background(), "req-1", func() {})
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)

	if err := b.Respond("req-1", "first"); err != nil {
		t.Fatalf("first Respond: %v", err)
	}
	// Second Respond should fail — pending entry was consumed.
	if err := b.Respond("req-1", "second"); err == nil {
		t.Error("expected error on second Respond, got nil")
	}
	if err := <-done; err != nil {
		t.Errorf("Ask returned error: %v", err)
	}
}
