package server

import (
	"context"
	"sync"

	"github.com/jefflinse/toasters/internal/service"
)

// eventRingSize is how many recent events the server retains for SSE
// Last-Event-ID resume. At the steady-state event rate (a progress snapshot
// every 500ms plus activity events) this covers the few seconds a client
// needs to reconnect after a blip; anything older is recovered by the
// client's snapshot resync instead.
const eventRingSize = 512

// eventRing is a fixed-size buffer of the most recent service events, kept
// so a reconnecting SSE client can replay what it missed (best-effort —
// events older than the ring are gone, and clients already resynchronize
// state via GetProgressState on reconnect).
type eventRing struct {
	mu  sync.Mutex
	buf []service.Event // oldest first, len <= eventRingSize
}

// add appends ev, evicting the oldest entry when full.
func (r *eventRing) add(ev service.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) >= eventRingSize {
		copy(r.buf, r.buf[1:])
		r.buf = r.buf[:len(r.buf)-1]
	}
	r.buf = append(r.buf, ev)
}

// since returns the buffered events with Seq > seq, oldest first.
func (r *eventRing) since(seq uint64) []service.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Find the first event newer than seq (buf is ordered by Seq).
	i := 0
	for ; i < len(r.buf); i++ {
		if r.buf[i].Seq > seq {
			break
		}
	}
	out := make([]service.Event, len(r.buf)-i)
	copy(out, r.buf[i:])
	return out
}

// recordEvents subscribes to the service event stream for the life of ctx
// and feeds the ring. Runs as a background goroutine started by Start.
func (s *Server) recordEvents(ctx context.Context) {
	ch := s.svc.Events().Subscribe(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			s.eventRing.add(ev)
		}
	}
}
