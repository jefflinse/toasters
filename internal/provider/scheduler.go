package provider

import (
	"context"
)

// Scheduler wraps a Provider with a bounded FIFO queue for ChatStream calls.
// Callers block in ChatStream until a slot frees up; each call holds its slot
// for the full duration of the streamed response — the slot is released only
// when the underlying stream closes (EventDone, EventError, or ctx cancel).
//
// This is Toasters' mechanism for respecting LLM-backend concurrency limits.
// A local LLM should get capacity 1 (serialize everything); cloud providers
// can be configured higher. Mycelium and rhizome are unaware — to them a
// Scheduler is just a Provider.
//
// Isolating the operator from workers is done by configuring two separate
// providers (same endpoint, distinct IDs) so each gets its own scheduler;
// there is no shared-queue priority lane.
type Scheduler struct {
	inner Provider
	sem   chan struct{}
}

// NewScheduler wraps p with a bounded FIFO queue of capacity n. n <= 0 is
// normalized to 1.
func NewScheduler(p Provider, capacity int) *Scheduler {
	if capacity <= 0 {
		capacity = 1
	}
	return &Scheduler{
		inner: p,
		sem:   make(chan struct{}, capacity),
	}
}

// Name returns the inner provider's Name.
func (s *Scheduler) Name() string { return s.inner.Name() }

// Models proxies to the inner provider without scheduling — model listing is
// cheap and orthogonal to LLM capacity.
func (s *Scheduler) Models(ctx context.Context) ([]ModelInfo, error) {
	return s.inner.Models(ctx)
}

// Capacity reports the configured concurrency cap.
func (s *Scheduler) Capacity() int { return cap(s.sem) }

// InFlight reports the current number of in-flight chat calls (acquired
// slots). Intended for diagnostics.
func (s *Scheduler) InFlight() int { return len(s.sem) }

// ChatStream acquires a slot, delegates to the inner provider, and releases
// the slot when the returned stream closes.
func (s *Scheduler) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	src, err := s.inner.ChatStream(ctx, req)
	if err != nil {
		<-s.sem
		return nil, err
	}

	out := make(chan StreamEvent, 8)
	go func() {
		defer func() {
			<-s.sem
			close(out)
		}()
		for ev := range src {
			select {
			case out <- ev:
			case <-ctx.Done():
				// Drain source so the inner goroutine can exit. The inner
				// producer should observe ctx cancellation and close src
				// on its own; we keep draining until it does.
				for range src {
				}
				return
			}
		}
	}()
	return out, nil
}

var _ Provider = (*Scheduler)(nil)
