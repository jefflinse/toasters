package client_test

import (
	"context"
	"sync"
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

// testEventHub fans events out to every subscriber, matching the real
// service's broadcast semantics. Earlier test mocks forwarded one shared
// channel, delivering each event to whichever subscriber read first — which
// broke once the server gained a second internal subscriber (the SSE
// Last-Event-ID replay recorder) competing with the SSE handler.
type testEventHub struct {
	mu   sync.Mutex
	subs map[int]chan service.Event
	next int
}

func newTestEventHub() *testEventHub {
	return &testEventHub{subs: make(map[int]chan service.Event)}
}

// subscribe registers a new subscriber whose channel closes when ctx ends.
// Matches the service.EventService Subscribe signature.
func (h *testEventHub) subscribe(ctx context.Context) <-chan service.Event {
	out := make(chan service.Event, 100)
	h.mu.Lock()
	id := h.next
	h.next++
	h.subs[id] = out
	h.mu.Unlock()
	go func() {
		<-ctx.Done()
		h.mu.Lock()
		delete(h.subs, id)
		close(out)
		h.mu.Unlock()
	}()
	return out
}

// send broadcasts ev to all current subscribers (non-blocking, drop on full —
// same contract as LocalService.broadcast).
func (h *testEventHub) send(ev service.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// waitForSubscribers blocks until at least n subscribers are registered (or
// the timeout passes). Broadcast semantics mean an event sent before the SSE
// handler subscribes is simply gone, so tests must wait for the connection
// to be live before sending. Note the server's internal event recorder
// counts as one subscriber.
func (h *testEventHub) waitForSubscribers(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		got := len(h.subs)
		h.mu.Unlock()
		if got >= n {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
