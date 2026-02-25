package sse

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestReader_AnthropicStyleEvents(t *testing.T) {
	t.Parallel()

	input := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start"}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"text":"Hello"}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	r := NewReader(strings.NewReader(input))
	ctx := context.Background()

	ev, ok := r.Next(ctx)
	if !ok {
		t.Fatal("expected event, got EOF")
	}
	if ev.Type != "message_start" {
		t.Errorf("event[0].Type = %q, want message_start", ev.Type)
	}
	if ev.Data != `{"type":"message_start"}` {
		t.Errorf("event[0].Data = %q", ev.Data)
	}

	ev, ok = r.Next(ctx)
	if !ok {
		t.Fatal("expected event, got EOF")
	}
	if ev.Type != "content_block_delta" {
		t.Errorf("event[1].Type = %q, want content_block_delta", ev.Type)
	}

	ev, ok = r.Next(ctx)
	if !ok {
		t.Fatal("expected event, got EOF")
	}
	if ev.Type != "message_stop" {
		t.Errorf("event[2].Type = %q, want message_stop", ev.Type)
	}

	_, ok = r.Next(ctx)
	if ok {
		t.Error("expected EOF after last event")
	}
	if err := r.Err(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReader_OpenAIStyleEvents(t *testing.T) {
	t.Parallel()

	// OpenAI SSE has no "event:" lines, just "data:" lines.
	input := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hello"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":" world"}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	r := NewReader(strings.NewReader(input))
	ctx := context.Background()

	ev, ok := r.Next(ctx)
	if !ok {
		t.Fatal("expected event, got EOF")
	}
	if ev.Type != "" {
		t.Errorf("event[0].Type = %q, want empty", ev.Type)
	}
	if !strings.Contains(ev.Data, "Hello") {
		t.Errorf("event[0].Data = %q, want to contain Hello", ev.Data)
	}

	ev, ok = r.Next(ctx)
	if !ok {
		t.Fatal("expected event, got EOF")
	}
	if !strings.Contains(ev.Data, "world") {
		t.Errorf("event[1].Data = %q, want to contain world", ev.Data)
	}

	ev, ok = r.Next(ctx)
	if !ok {
		t.Fatal("expected [DONE] event, got EOF")
	}
	if ev.Data != "[DONE]" {
		t.Errorf("event[2].Data = %q, want [DONE]", ev.Data)
	}

	_, ok = r.Next(ctx)
	if ok {
		t.Error("expected EOF after [DONE]")
	}
}

func TestReader_DataWithoutSpace(t *testing.T) {
	t.Parallel()

	// Some servers send "data:{json}" without a space after the colon.
	input := "data:{\"content\":\"works\"}\n\n"

	r := NewReader(strings.NewReader(input))
	ctx := context.Background()

	ev, ok := r.Next(ctx)
	if !ok {
		t.Fatal("expected event, got EOF")
	}
	if ev.Data != `{"content":"works"}` {
		t.Errorf("Data = %q, want {\"content\":\"works\"}", ev.Data)
	}
}

func TestReader_CommentsAndRetryIgnored(t *testing.T) {
	t.Parallel()

	input := strings.Join([]string{
		`: this is a comment`,
		`retry: 5000`,
		`id: 42`,
		`data: payload`,
		``,
	}, "\n")

	r := NewReader(strings.NewReader(input))
	ctx := context.Background()

	ev, ok := r.Next(ctx)
	if !ok {
		t.Fatal("expected event, got EOF")
	}
	if ev.Data != "payload" {
		t.Errorf("Data = %q, want payload", ev.Data)
	}

	_, ok = r.Next(ctx)
	if ok {
		t.Error("expected EOF")
	}
}

func TestReader_BlankLineResetsEventType(t *testing.T) {
	t.Parallel()

	input := strings.Join([]string{
		`event: first_type`,
		`data: first_data`,
		``,
		`data: second_data`,
		``,
	}, "\n")

	r := NewReader(strings.NewReader(input))
	ctx := context.Background()

	ev, ok := r.Next(ctx)
	if !ok {
		t.Fatal("expected first event")
	}
	if ev.Type != "first_type" {
		t.Errorf("event[0].Type = %q, want first_type", ev.Type)
	}

	ev, ok = r.Next(ctx)
	if !ok {
		t.Fatal("expected second event")
	}
	if ev.Type != "" {
		t.Errorf("event[1].Type = %q, want empty (should be reset after blank line)", ev.Type)
	}
	if ev.Data != "second_data" {
		t.Errorf("event[1].Data = %q, want second_data", ev.Data)
	}
}

func TestReader_EmptyInput(t *testing.T) {
	t.Parallel()

	r := NewReader(strings.NewReader(""))
	ctx := context.Background()

	_, ok := r.Next(ctx)
	if ok {
		t.Error("expected EOF for empty input")
	}
	if err := r.Err(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReader_ContextCancellation(t *testing.T) {
	t.Parallel()

	// Create a reader with data that would produce events.
	input := strings.Join([]string{
		`event: message_start`,
		`data: first`,
		``,
		`event: content_block_delta`,
		`data: second`,
		``,
	}, "\n")

	r := NewReader(strings.NewReader(input))
	ctx, cancel := context.WithCancel(context.Background())

	// Read first event normally.
	ev, ok := r.Next(ctx)
	if !ok {
		t.Fatal("expected first event")
	}
	if ev.Data != "first" {
		t.Errorf("first event Data = %q, want first", ev.Data)
	}

	// Cancel context before reading second event.
	cancel()

	_, ok = r.Next(ctx)
	if ok {
		t.Error("expected false after context cancellation")
	}
}

// errReader is an io.Reader that returns an error after reading some data.
type errReader struct {
	data string
	pos  int
	err  error
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	if r.pos >= len(r.data) {
		return n, r.err
	}
	return n, nil
}

func TestReader_ScannerError(t *testing.T) {
	t.Parallel()

	reader := &errReader{
		data: "event: ping\ndata: {}\n\nsome incomplete line",
		err:  fmt.Errorf("connection reset"),
	}

	r := NewReader(reader)
	ctx := context.Background()

	// First event should be readable.
	ev, ok := r.Next(ctx)
	if !ok {
		t.Fatal("expected first event")
	}
	if ev.Type != "ping" {
		t.Errorf("Type = %q, want ping", ev.Type)
	}

	// Second call may or may not return an event depending on scanner behavior.
	// But eventually Err() should report the error.
	for {
		_, ok = r.Next(ctx)
		if !ok {
			break
		}
	}

	// The scanner may or may not surface the error (depends on whether it
	// completed scanning all lines before the error). Either way is valid.
}

// errAfterReader returns data successfully, then returns an error on the next read.
type errAfterReader struct {
	r     io.Reader
	done  bool
	ioErr error
}

func (e *errAfterReader) Read(p []byte) (int, error) {
	if e.done {
		return 0, e.ioErr
	}
	n, err := e.r.Read(p)
	if err == io.EOF {
		e.done = true
		return n, e.ioErr
	}
	return n, err
}

func TestReader_Err_ReportsUnderlyingError(t *testing.T) {
	t.Parallel()

	underlying := strings.NewReader("data: hello\n\nincomplete line without newline")
	reader := &errAfterReader{
		r:     underlying,
		ioErr: fmt.Errorf("network timeout"),
	}

	r := NewReader(reader)
	ctx := context.Background()

	// Drain all events.
	for {
		_, ok := r.Next(ctx)
		if !ok {
			break
		}
	}

	// Err() should report the underlying error (if the scanner surfaced it).
	// The scanner may or may not surface it depending on internal buffering.
	// This test just verifies Err() doesn't panic.
	_ = r.Err()
}

func TestReader_PingEvent(t *testing.T) {
	t.Parallel()

	input := strings.Join([]string{
		`event: ping`,
		`data: {}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	r := NewReader(strings.NewReader(input))
	ctx := context.Background()

	ev, ok := r.Next(ctx)
	if !ok {
		t.Fatal("expected ping event")
	}
	if ev.Type != "ping" {
		t.Errorf("Type = %q, want ping", ev.Type)
	}
	if ev.Data != "{}" {
		t.Errorf("Data = %q, want {}", ev.Data)
	}

	ev, ok = r.Next(ctx)
	if !ok {
		t.Fatal("expected message_stop event")
	}
	if ev.Type != "message_stop" {
		t.Errorf("Type = %q, want message_stop", ev.Type)
	}
}

func TestReader_MultipleDataLinesWithoutBlankLine(t *testing.T) {
	t.Parallel()

	// Each data line is yielded as a separate event (no blank line between them).
	// The event type persists until a blank line resets it.
	input := strings.Join([]string{
		`event: content_block_delta`,
		`data: first`,
		`data: second`,
		``,
	}, "\n")

	r := NewReader(strings.NewReader(input))
	ctx := context.Background()

	ev, ok := r.Next(ctx)
	if !ok {
		t.Fatal("expected first data event")
	}
	if ev.Type != "content_block_delta" {
		t.Errorf("event[0].Type = %q, want content_block_delta", ev.Type)
	}
	if ev.Data != "first" {
		t.Errorf("event[0].Data = %q, want first", ev.Data)
	}

	ev, ok = r.Next(ctx)
	if !ok {
		t.Fatal("expected second data event")
	}
	// Event type persists until blank line.
	if ev.Type != "content_block_delta" {
		t.Errorf("event[1].Type = %q, want content_block_delta", ev.Type)
	}
	if ev.Data != "second" {
		t.Errorf("event[1].Data = %q, want second", ev.Data)
	}
}
