package provider

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProvider is a controllable Provider used to probe scheduler behavior.
// Each ChatStream call blocks in the goroutine until its control channel
// is signaled, letting tests orchestrate concurrent callers deterministically.
type fakeProvider struct {
	mu       sync.Mutex
	active   int32
	maxSeen  int32
	starts   chan string // signals "a call entered ChatStream inner"
	release  chan string // tests read names here to release specific calls
	blockers map[string]chan struct{}
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{
		starts:   make(chan string, 16),
		release:  make(chan string, 16),
		blockers: make(map[string]chan struct{}),
	}
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Models(_ context.Context) ([]ModelInfo, error) {
	return nil, nil
}

func (f *fakeProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	name := req.Model // use Model as a per-call label
	f.mu.Lock()
	block, ok := f.blockers[name]
	if !ok {
		block = make(chan struct{})
		f.blockers[name] = block
	}
	f.mu.Unlock()

	cur := atomic.AddInt32(&f.active, 1)
	for {
		prev := atomic.LoadInt32(&f.maxSeen)
		if cur <= prev || atomic.CompareAndSwapInt32(&f.maxSeen, prev, cur) {
			break
		}
	}
	f.starts <- name

	ch := make(chan StreamEvent, 1)
	go func() {
		defer close(ch)
		defer atomic.AddInt32(&f.active, -1)
		select {
		case <-block:
			ch <- StreamEvent{Type: EventDone}
		case <-ctx.Done():
			ch <- StreamEvent{Type: EventError, Error: ctx.Err()}
		}
	}()
	return ch, nil
}

func (f *fakeProvider) finish(name string) {
	f.mu.Lock()
	c, ok := f.blockers[name]
	if !ok {
		c = make(chan struct{})
		f.blockers[name] = c
	}
	f.mu.Unlock()
	close(c)
}

// drain consumes the scheduler's channel until it closes.
func drain(ch <-chan StreamEvent) {
	for range ch {
	}
}

func TestScheduler_CapacityBoundsConcurrency(t *testing.T) {
	fake := newFakeProvider()
	sch := NewScheduler(fake, 2)

	var wg sync.WaitGroup
	for _, name := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, err := sch.ChatStream(context.Background(), ChatRequest{Model: name})
			if err != nil {
				t.Errorf("ChatStream(%s): %v", name, err)
				return
			}
			drain(ch)
		}()
	}

	// Wait until first 2 calls are in-flight.
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case name := <-fake.starts:
			seen[name] = true
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for first 2 calls; got %v", seen)
		}
	}

	// Give the runtime a moment; confirm only 2 are active.
	time.Sleep(20 * time.Millisecond)
	if in := sch.InFlight(); in != 2 {
		t.Errorf("InFlight = %d, want 2", in)
	}
	if max := atomic.LoadInt32(&fake.maxSeen); max > 2 {
		t.Errorf("maxSeen = %d, scheduler allowed more than 2 concurrent calls", max)
	}

	// Release all. As each finishes, the next queued call proceeds.
	for _, name := range []string{"a", "b", "c", "d"} {
		fake.finish(name)
	}
	wg.Wait()

	// All 4 calls should have been served; max concurrency should have
	// stayed at 2.
	if max := atomic.LoadInt32(&fake.maxSeen); max != 2 {
		t.Errorf("maxSeen = %d after completion, want 2", max)
	}
}

func TestScheduler_AcquireCancelledByContext(t *testing.T) {
	fake := newFakeProvider()
	sch := NewScheduler(fake, 1)

	// First call takes the only slot.
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		ch, err := sch.ChatStream(context.Background(), ChatRequest{Model: "first"})
		if err != nil {
			t.Errorf("first ChatStream: %v", err)
			return
		}
		drain(ch)
	}()

	// Wait for first call to be in-flight.
	select {
	case <-fake.starts:
	case <-time.After(time.Second):
		t.Fatal("first call never started")
	}

	// Second call should block on acquire; cancel ctx and observe error.
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, err := sch.ChatStream(ctx, ChatRequest{Model: "second"})
		resultCh <- err
	}()

	// Give the goroutine a moment to block in acquire.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-resultCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second call did not unblock on ctx cancel")
	}

	// Release first so its goroutine exits cleanly.
	fake.finish("first")
	<-firstDone
}

func TestScheduler_SequentialCallsReleaseSlot(t *testing.T) {
	fake := newFakeProvider()
	sch := NewScheduler(fake, 1)

	for _, name := range []string{"a", "b", "c"} {
		ch, err := sch.ChatStream(context.Background(), ChatRequest{Model: name})
		if err != nil {
			t.Fatalf("ChatStream(%s): %v", name, err)
		}
		<-fake.starts
		fake.finish(name)
		drain(ch)
		if in := sch.InFlight(); in != 0 {
			t.Errorf("after %s: InFlight = %d, want 0", name, in)
		}
	}
}

func TestScheduler_InnerErrorReleasesSlot(t *testing.T) {
	errInner := errors.New("boom")
	failing := providerFunc(func(_ context.Context, _ ChatRequest) (<-chan StreamEvent, error) {
		return nil, errInner
	})
	sch := NewScheduler(failing, 1)

	_, err := sch.ChatStream(context.Background(), ChatRequest{})
	if !errors.Is(err, errInner) {
		t.Fatalf("err = %v, want errInner", err)
	}
	if in := sch.InFlight(); in != 0 {
		t.Errorf("InFlight after error = %d, want 0", in)
	}
}

func TestScheduler_ZeroCapacityNormalisedToOne(t *testing.T) {
	sch := NewScheduler(newFakeProvider(), 0)
	if got := sch.Capacity(); got != 1 {
		t.Errorf("Capacity = %d, want 1", got)
	}
}

// providerFunc adapts a func to the Provider interface.
type providerFunc func(context.Context, ChatRequest) (<-chan StreamEvent, error)

func (f providerFunc) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	return f(ctx, req)
}
func (providerFunc) Models(_ context.Context) ([]ModelInfo, error) { return nil, nil }
func (providerFunc) Name() string                                  { return "func" }
