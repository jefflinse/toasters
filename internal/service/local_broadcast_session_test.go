package service

import (
	"context"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/runtime"
)

// TestBroadcastSessionKBNote_EventPayload mirrors the style of
// TestBroadcastOperatorText_EventPayload in local_test.go, verifying
// BroadcastSessionKBNote (the graph-node side of the session.kb display
// side-channel, mirroring BroadcastSessionShellExec) produces the expected
// event type and payload.
func TestBroadcastSessionKBNote_EventPayload(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := svc.subscribe(ctx)

	svc.BroadcastSessionKBNote("sess-1", runtime.KBNote{
		Scope:   "job",
		Op:      "write",
		Source:  "worker-1",
		Preview: "Found the bug (20260101-000000.000-worker-1-found-abc123)",
	})

	select {
	case ev := <-ch:
		if ev.Type != EventTypeSessionKB {
			t.Errorf("Type = %q, want %q", ev.Type, EventTypeSessionKB)
		}
		if ev.SessionID != "sess-1" {
			t.Errorf("SessionID = %q, want %q", ev.SessionID, "sess-1")
		}
		payload, ok := ev.Payload.(SessionKBPayload)
		if !ok {
			t.Fatalf("Payload type = %T, want SessionKBPayload", ev.Payload)
		}
		if payload.Scope != "job" {
			t.Errorf("Scope = %q, want %q", payload.Scope, "job")
		}
		if payload.Op != "write" {
			t.Errorf("Op = %q, want %q", payload.Op, "write")
		}
		if payload.Source != "worker-1" {
			t.Errorf("Source = %q, want %q", payload.Source, "worker-1")
		}
		if payload.Preview != "Found the bug (20260101-000000.000-worker-1-found-abc123)" {
			t.Errorf("Preview = %q, unexpected value", payload.Preview)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

// TestBroadcastSessionKBNote_Search checks the "search" op variant.
func TestBroadcastSessionKBNote_Search(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := svc.subscribe(ctx)

	svc.BroadcastSessionKBNote("sess-2", runtime.KBNote{
		Scope:   "job",
		Op:      "search",
		Source:  "worker-2",
		Preview: `"off-by-one" → 2 hits`,
	})

	select {
	case ev := <-ch:
		payload, ok := ev.Payload.(SessionKBPayload)
		if !ok {
			t.Fatalf("Payload type = %T, want SessionKBPayload", ev.Payload)
		}
		if payload.Op != "search" {
			t.Errorf("Op = %q, want %q", payload.Op, "search")
		}
		if payload.Preview != `"off-by-one" → 2 hits` {
			t.Errorf("Preview = %q, unexpected value", payload.Preview)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}
