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
type Reader struct {
	scanner   *bufio.Scanner
	eventType string
}

// NewReader creates a new SSE reader from the given io.Reader.
func NewReader(r io.Reader) *Reader {
	return &Reader{
		scanner: bufio.NewScanner(r),
	}
}

// Next reads the next SSE event from the stream. It returns the event and true
// if an event was read, or a zero Event and false if the stream ended.
// The ctx parameter is used for cancellation.
//
// Next blocks until a complete event (data line) is available, the stream ends,
// or ctx is cancelled.
func (r *Reader) Next(ctx context.Context) (Event, bool) {
	for {
		// Check context before attempting to read
		if ctx.Err() != nil {
			return Event{}, false
		}

		// Use a goroutine to make scanner.Scan() cancellable
		type scanResult struct {
			ok   bool
			line string
		}

		resultCh := make(chan scanResult, 1)
		go func() {
			if r.scanner.Scan() {
				resultCh <- scanResult{ok: true, line: r.scanner.Text()}
			} else {
				resultCh <- scanResult{ok: false}
			}
		}()

		// Wait for either the scan to complete or context cancellation
		select {
		case <-ctx.Done():
			return Event{}, false
		case result := <-resultCh:
			if !result.ok {
				return Event{}, false
			}

			line := result.line

			// Blank line = end of SSE event block.
			if line == "" {
				r.eventType = ""
				continue
			}

			// Capture the event type.
			if strings.HasPrefix(line, "event: ") {
				r.eventType = strings.TrimPrefix(line, "event: ")
				continue
			}

			// We only process data lines.
			// Handle both "data: " (with space) and "data:" (without space).
			if !strings.HasPrefix(line, "data:") {
				continue
			}

			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimSpace(data)

			ev := Event{
				Type: r.eventType,
				Data: data,
			}
			return ev, true
		}
	}
}

// Err returns the first non-EOF error encountered by the underlying scanner.
func (r *Reader) Err() error {
	return r.scanner.Err()
}
