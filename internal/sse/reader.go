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

// maxLineSize is the largest single SSE line the reader accepts. The
// bufio.Scanner default of 64KB kills the whole stream (bufio.ErrTooLong)
// on one large event — a full progress snapshot or a session.started
// carrying a big system prompt exceeds it easily, and the event is
// permanently lost across the reconnect. Matches the 10 MiB per-event
// ceiling the client enforces (client/events.go maxSSEEventSize).
const maxLineSize = 10 * 1024 * 1024

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
// underlying reader closes or returns an error. Shutdown needs both halves:
// close the underlying io.Reader (http.Response.Body.Close) to unblock a
// stuck Scan, and call Close to unblock a pump parked on the channel send —
// closing the body alone cannot wake a goroutine blocked delivering an event
// nobody will read.
type Reader struct {
	events chan Event
	done   chan struct{}
	quit   chan struct{}

	closeOnce sync.Once

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
		quit:   make(chan struct{}),
	}
	go rdr.pump(r)
	return rdr
}

// Close signals the pump goroutine to stop. Safe to call multiple times and
// concurrently with Next. Callers that stop consuming before the stream is
// exhausted MUST call Close (typically deferred), or the pump leaks once the
// 16-event buffer fills.
func (r *Reader) Close() {
	r.closeOnce.Do(func() { close(r.quit) })
}

func (r *Reader) pump(src io.Reader) {
	defer close(r.events)
	defer close(r.done)

	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
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

		select {
		case r.events <- Event{Type: eventType, Data: data}:
		case <-r.quit:
			return
		}
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
