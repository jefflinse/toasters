package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/service"
)

// The SSE response must carry Content-Type: text/event-stream in the
// committed headers. Pre-fix, a probe Flush() implicitly wrote a 200 before
// any headers were set, so spec-compliant consumers (browser EventSource)
// rejected the stream (C18).
func TestSSE_HeadersPrecedeBody(t *testing.T) {
	t.Parallel()

	mockSvc := newMockService()
	mockSvc.events.ch = make(chan service.Event, 1)
	srv := New(mockSvc)

	// One event, then a closed channel so the handler exits after writing it.
	mockSvc.events.ch <- service.Event{Type: service.EventTypeHeartbeat, Seq: 1}
	close(mockSvc.events.ch)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	rec := httptest.NewRecorder()
	srv.events(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	if !rec.Flushed {
		t.Error("response was never flushed")
	}

	// Wire framing: id / event / data lines followed by a blank line.
	body := rec.Body.String()
	if !strings.Contains(body, "id: 1\n") {
		t.Errorf("body missing id line: %q", body)
	}
	if !strings.Contains(body, "event: "+string(service.EventTypeHeartbeat)+"\n") {
		t.Errorf("body missing event line: %q", body)
	}
	dataIdx := strings.Index(body, "data: ")
	if dataIdx == -1 {
		t.Fatalf("body missing data line: %q", body)
	}
	dataLine := body[dataIdx+len("data: "):]
	dataLine = dataLine[:strings.Index(dataLine, "\n")]
	var envelope SSEEvent
	if err := json.Unmarshal([]byte(dataLine), &envelope); err != nil {
		t.Fatalf("data line is not valid JSON: %v\n%q", err, dataLine)
	}
	if envelope.Type != string(service.EventTypeHeartbeat) {
		t.Errorf("envelope type = %q, want heartbeat", envelope.Type)
	}
	if !strings.Contains(body, "\n\n") {
		t.Errorf("event not terminated by blank line: %q", body)
	}
}

// noFlushWriter hides the recorder's Flusher so the handler sees a
// non-streaming ResponseWriter.
type noFlushWriter struct {
	rec *httptest.ResponseRecorder
}

func (w *noFlushWriter) Header() http.Header         { return w.rec.Header() }
func (w *noFlushWriter) Write(b []byte) (int, error) { return w.rec.Write(b) }
func (w *noFlushWriter) WriteHeader(code int)        { w.rec.WriteHeader(code) }

// When the ResponseWriter can't stream, the handler must still deliver a
// proper JSON error — possible only because nothing was written before the
// probe Flush failed.
func TestSSE_StreamingUnsupportedReturnsJSONError(t *testing.T) {
	t.Parallel()

	mockSvc := newMockService()
	srv := New(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	rec := httptest.NewRecorder()
	srv.events(&noFlushWriter{rec: rec}, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var resp ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("error body is not valid JSON: %v\n%q", err, rec.Body.String())
	}
	if resp.Error.Code != "internal_error" {
		t.Errorf("error code = %q, want internal_error", resp.Error.Code)
	}
}

// A reconnecting client presenting Last-Event-ID gets the buffered events it
// missed, in order, and live events it already received via replay are not
// duplicated (C23: SSE resume).
func TestSSE_LastEventIDResume(t *testing.T) {
	t.Parallel()

	mockSvc := newMockService()
	mockSvc.events.ch = make(chan service.Event, 4)
	srv := New(mockSvc)

	// The ring holds seqs 1..5 from before the client's blip.
	for i := uint64(1); i <= 5; i++ {
		srv.eventRing.add(service.Event{Type: service.EventTypeHeartbeat, Seq: i})
	}

	// The live subscription delivers a stale event (4, also replayed from the
	// ring) and a genuinely new one (6).
	mockSvc.events.ch <- service.Event{Type: service.EventTypeHeartbeat, Seq: 4}
	mockSvc.events.ch <- service.Event{Type: service.EventTypeHeartbeat, Seq: 6}
	close(mockSvc.events.ch)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	req.Header.Set("Last-Event-ID", "3")
	rec := httptest.NewRecorder()
	srv.events(rec, req)

	body := rec.Body.String()
	var ids []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "id: ") {
			ids = append(ids, strings.TrimPrefix(line, "id: "))
		}
	}
	want := []string{"4", "5", "6"}
	if len(ids) != len(want) {
		t.Fatalf("event ids = %v, want %v\nbody:\n%s", ids, want, body)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("event ids = %v, want %v", ids, want)
		}
	}
}

// The ring evicts oldest entries at capacity and since() respects ordering.
func TestEventRing_EvictionAndSince(t *testing.T) {
	t.Parallel()

	var r eventRing
	for i := uint64(1); i <= eventRingSize+10; i++ {
		r.add(service.Event{Seq: i})
	}
	all := r.since(0)
	if len(all) != eventRingSize {
		t.Fatalf("ring holds %d events, want %d", len(all), eventRingSize)
	}
	if all[0].Seq != 11 || all[len(all)-1].Seq != eventRingSize+10 {
		t.Fatalf("ring range = [%d, %d], want [11, %d]", all[0].Seq, all[len(all)-1].Seq, eventRingSize+10)
	}
	tail := r.since(eventRingSize + 8)
	if len(tail) != 2 || tail[0].Seq != eventRingSize+9 {
		t.Fatalf("since() returned %d events starting at %d", len(tail), tail[0].Seq)
	}
}
