package cmd

import (
	"sync"
	"testing"
	"time"
)

func TestTextBatcher_SingleAdd(t *testing.T) {
	var got string
	b := newTextBatcher(time.Hour, func(text string) {
		got = text
	})

	b.Add("hello")
	b.Flush()

	if got != "hello" {
		t.Errorf("expected %q, got %q", "hello", got)
	}
}

func TestTextBatcher_MultipleAdds(t *testing.T) {
	var got string
	b := newTextBatcher(time.Hour, func(text string) {
		got = text
	})

	b.Add("hello")
	b.Add(" ")
	b.Add("world")
	b.Flush()

	if got != "hello world" {
		t.Errorf("expected %q, got %q", "hello world", got)
	}
}

func TestTextBatcher_TimerFlush(t *testing.T) {
	done := make(chan string, 1)
	b := newTextBatcher(20*time.Millisecond, func(text string) {
		done <- text
	})

	b.Add("auto")

	select {
	case got := <-done:
		if got != "auto" {
			t.Errorf("expected %q, got %q", "auto", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for timer flush")
	}
}

func TestTextBatcher_FlushDrainsBuffer(t *testing.T) {
	var calls int
	b := newTextBatcher(time.Hour, func(text string) {
		calls++
	})

	b.Add("data")
	b.Flush()

	if calls != 1 {
		t.Fatalf("expected 1 flush call, got %d", calls)
	}

	// Second flush should be a no-op since the buffer was drained.
	b.Flush()
	if calls != 1 {
		t.Errorf("expected no additional flush call, got %d total", calls)
	}
}

func TestTextBatcher_FlushWithEmptyBuffer(t *testing.T) {
	var called bool
	b := newTextBatcher(time.Hour, func(text string) {
		called = true
	})

	b.Flush()

	if called {
		t.Error("flush callback should not be called on empty buffer")
	}
}

func TestTextBatcher_ConcurrentAdds(t *testing.T) {
	var mu sync.Mutex
	var got string
	b := newTextBatcher(time.Hour, func(text string) {
		mu.Lock()
		got = text
		mu.Unlock()
	})

	const goroutines = 50
	const perGoroutine = "x"

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			b.Add(perGoroutine)
		}()
	}
	wg.Wait()
	b.Flush()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != goroutines {
		t.Errorf("expected %d characters, got %d", goroutines, len(got))
	}
}
