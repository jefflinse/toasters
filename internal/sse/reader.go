// Package sse provides shared Server-Sent Events (SSE) parsing for both
// Anthropic and OpenAI streaming APIs. It handles the wire protocol (reading
// lines, parsing data:/event: fields) and defines the JSON event types that
// appear in the SSE data payloads.
package sse

import (
	"bufio"
	"context"
	"io"
	"strings"
	"sync"
)

// Event represents a single SSE event parsed from the stream.
// EventType is the value from the "event:" line (empty if none was sent).
// Data is the value from the "data:" line.
type Event struct {
	Type string // from "event: <type>" line; empty if no event line preceded the data
	Data string // from "data: <payload>" line
}

// Reader reads SSE events from an io.Reader. It handles the SSE wire protocol:
// - "event: <type>" lines set the event type
// - "data: <payload>" lines carry the event data
// - Blank lines delimit events (resetting the event type)
// - All other lines (comments, retry:, id:, etc.) are ignored
//
// For Anthropic SSE, events always have an "event:" line before the "data:" line.
// For OpenAI SSE, events only have "data:" lines (no "event:" line).
//
// Reader runs a single background goroutine that pumps events into a channel;
// callers read via Next, and the background goroutine exits cleanly when the
// underlying reader closes or returns an error. To unblock a stuck Scan call
// (for example, after the caller's ctx is cancelled), close the underlying
// io.Reader — typically via http.Response.Body.Close() in the caller.
type Reader struct {
	events chan Event
	done   chan struct{}

	mu  sync.Mutex
	err error
}

// NewReader creates a new SSE reader from the given io.Reader and starts the
// background pump goroutine. The goroutine exits when the reader is exhausted
// or returns an error; callers should close the underlying reader to unblock
// it during shutdown.
func NewReader(r io.Reader) *Reader {
	rdr := &Reader{
		events: make(chan Event, 16),
		done:   make(chan struct{}),
	}
	go rdr.pump(r)
	return rdr
}

func (r *Reader) pump(src io.Reader) {
	defer close(r.events)
	defer close(r.done)

	scanner := bufio.NewScanner(src)
	var eventType string
	for scanner.Scan() {
		line := scanner.Text()

		// Blank line = end of SSE event block.
		if line == "" {
			eventType = ""
			continue
		}

		// Capture the event type.
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		// We only process data lines.
		// Handle both "data: " (with space) and "data:" (without space).
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimSpace(data)

		r.events <- Event{Type: eventType, Data: data}
	}

	if err := scanner.Err(); err != nil {
		r.mu.Lock()
		r.err = err
		r.mu.Unlock()
	}
}

// Next reads the next SSE event from the stream. It returns the event and true
// if an event was read, or a zero Event and false if the stream ended or ctx
// was cancelled. Cancellation is checked first, so a cancelled ctx will always
// return false even if events remain buffered.
func (r *Reader) Next(ctx context.Context) (Event, bool) {
	if ctx.Err() != nil {
		return Event{}, false
	}
	select {
	case <-ctx.Done():
		return Event{}, false
	case ev, ok := <-r.events:
		return ev, ok
	}
}

// Err returns the first non-EOF error encountered by the underlying scanner.
// Safe to call only after Next returns false.
func (r *Reader) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}
