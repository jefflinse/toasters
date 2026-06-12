package cmd

import (
	"strings"
	"sync"
	"time"
)

// textBatcher accumulates streamed text tokens and flushes them as a
// single batch at a configurable interval. This prevents high-throughput
// models from flooding the Bubble Tea message queue with per-token messages.
type textBatcher struct {
	mu       sync.Mutex
	buf      strings.Builder
	timer    *time.Timer
	interval time.Duration
	flush    func(string) // called with accumulated text
}

func newTextBatcher(interval time.Duration, flush func(string)) *textBatcher {
	return &textBatcher{
		interval: interval,
		flush:    flush,
	}
}

// Add appends text to the buffer. If no timer is running, starts one.
// Safe for concurrent use.
func (b *textBatcher) Add(text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.WriteString(text)
	if b.timer == nil {
		b.timer = time.AfterFunc(b.interval, b.timerFired)
	}
}

// timerFired is called by the timer goroutine. It drains the buffer
// and calls the flush callback.
//
// flush runs UNDER b.mu (here and in Flush): draining and emitting must be
// one atomic step, or a timer flush racing a turn-done Flush could emit
// batches out of order — drain old text, lose the lock, and deliver it
// after newer text already went out. The callback must not call back into
// the batcher (the wiring sends to a non-blocking broadcast, which is fine).
func (b *textBatcher) timerFired() {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := b.buf.String()
	b.buf.Reset()
	b.timer = nil
	if text != "" {
		b.flush(text)
	}
}

// Flush forces an immediate drain of any buffered text. Call this
// on turn done to ensure no tokens are lost.
func (b *textBatcher) Flush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	text := b.buf.String()
	b.buf.Reset()
	if text != "" {
		b.flush(text)
	}
}
