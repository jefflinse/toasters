package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/graphexec"
)

// drainFor waits up to a second for an event of the given type and returns it.
func drainFor(t *testing.T, ch <-chan Event, want EventType) Event {
	t.Helper()
	for {
		select {
		case ev := <-ch:
			if ev.Type == want {
				return ev
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

func TestBroadcastPrompt_RegistersBlockerAndEmits(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.subscribe(ctx)

	svc.BroadcastPrompt("req-1", []graphexec.PromptQuestion{{Question: "Pick one", Options: []string{"a", "b"}}}, "graph:investigate", "job-1", "task-1")

	ev := drainFor(t, ch, EventTypeBlockerAdded)
	b, ok := ev.Payload.(Blocker)
	if !ok {
		t.Fatalf("payload type = %T, want Blocker", ev.Payload)
	}
	if b.RequestID != "req-1" || b.Source != "graph:investigate" {
		t.Errorf("blocker = %+v, want req-1 / graph:investigate", b)
	}
	if b.JobID != "job-1" || b.TaskID != "task-1" {
		t.Errorf("job/task = %q/%q, want job-1/task-1", b.JobID, b.TaskID)
	}
	if len(b.Questions) != 1 || b.Questions[0].Question != "Pick one" {
		t.Errorf("questions = %v, want one 'Pick one'", b.Questions)
	}
	if b.CreatedAt.IsZero() {
		t.Error("CreatedAt should be stamped")
	}

	got, err := svc.Blockers(context.Background())
	if err != nil {
		t.Fatalf("Blockers: %v", err)
	}
	if len(got) != 1 || got[0].RequestID != "req-1" {
		t.Errorf("Blockers() = %v, want one req-1", got)
	}
}

func TestBroadcastOperatorPrompt_RegistersBlockerEmptySource(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.subscribe(ctx)

	svc.BroadcastOperatorPrompt("op-1", []graphexec.PromptQuestion{{Question: "Q?"}})

	ev := drainFor(t, ch, EventTypeBlockerAdded)
	b := ev.Payload.(Blocker)
	if b.Source != "" {
		t.Errorf("Source = %q, want empty (operator)", b.Source)
	}
	if b.RequestID != "op-1" {
		t.Errorf("RequestID = %q, want op-1", b.RequestID)
	}
}

func TestResolveBlocker_RemovesAndEmitsOnce(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.subscribe(ctx)

	svc.BroadcastPrompt("req-1", []graphexec.PromptQuestion{{Question: "Q?"}}, "graph:n", "job-1", "task-1")
	drainFor(t, ch, EventTypeBlockerAdded)

	svc.ResolveBlocker("req-1")
	ev := drainFor(t, ch, EventTypeBlockerResolved)
	if p, ok := ev.Payload.(BlockerResolvedPayload); !ok || p.RequestID != "req-1" {
		t.Fatalf("resolved payload = %+v, want req-1", ev.Payload)
	}

	got, _ := svc.Blockers(context.Background())
	if len(got) != 0 {
		t.Errorf("Blockers() = %v, want empty after resolve", got)
	}

	// Resolving again is a no-op: it must NOT emit another resolved event.
	svc.ResolveBlocker("req-1")
	select {
	case ev := <-ch:
		if ev.Type == EventTypeBlockerResolved {
			t.Fatal("idempotent ResolveBlocker emitted a second resolved event")
		}
	case <-time.After(100 * time.Millisecond):
		// No event — correct.
	}
}

func TestResolveBlocker_UnknownIsNoop(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.subscribe(ctx)

	svc.ResolveBlocker("never-existed")
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event for unknown resolve: %v", ev.Type)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestBlockers_OrderedByCreatedAt(t *testing.T) {
	t.Parallel()

	svc := newTestService(t)
	svc.BroadcastPrompt("first", []graphexec.PromptQuestion{{Question: "1"}}, "graph:a", "job-1", "task-1")
	// Ensure a distinct, later timestamp.
	time.Sleep(2 * time.Millisecond)
	svc.BroadcastPrompt("second", []graphexec.PromptQuestion{{Question: "2"}}, "graph:b", "job-1", "task-1")

	got, _ := svc.Blockers(context.Background())
	if len(got) != 2 || got[0].RequestID != "first" || got[1].RequestID != "second" {
		t.Errorf("order = %v, want [first second]", got)
	}
}

// newTestServiceWithStore wires a real SQLite store so blocker persistence
// and history can be exercised end to end.
func newTestServiceWithStore(t *testing.T) *LocalService {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { store.Close() }) //nolint:errcheck
	return NewLocal(LocalConfig{
		ConfigDir: t.TempDir(),
		StartTime: time.Now(),
		Store:     store,
	})
}

// TestBlockerHistory_AnsweredDisposition verifies the full outcome path: a
// raised blocker answered via RespondToPrompt lands in history as "answered"
// with the answer text, and the resolved event carries the disposition.
func TestBlockerHistory_AnsweredDisposition(t *testing.T) {
	t.Parallel()

	svc := newTestServiceWithStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.subscribe(ctx)

	// A waiter blocks on the broker like a real graph node would.
	answered := make(chan string, 1)
	go func() {
		resp, err := svc.broker.Ask(context.Background(), "req-1", func() {
			svc.BroadcastPrompt("req-1", []graphexec.PromptQuestion{{Question: "Which?"}}, "graph:plan", "job-1", "")
		})
		if err == nil {
			answered <- resp
		}
		svc.ResolveBlocker("req-1")
	}()

	drainFor(t, ch, EventTypeBlockerAdded)
	if err := svc.RespondToPrompt(context.Background(), "req-1", "the left one"); err != nil {
		t.Fatalf("RespondToPrompt: %v", err)
	}

	ev := drainFor(t, ch, EventTypeBlockerResolved)
	payload := ev.Payload.(BlockerResolvedPayload)
	if payload.Disposition != BlockerDispositionAnswered {
		t.Errorf("resolved disposition = %q, want answered", payload.Disposition)
	}
	if got := <-answered; got != "the left one" {
		t.Errorf("waiter received %q, want the answer", got)
	}

	hist, err := svc.BlockerHistory(context.Background(), 0)
	if err != nil {
		t.Fatalf("BlockerHistory: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("history = %d records, want 1", len(hist))
	}
	rec := hist[0]
	if rec.Disposition != BlockerDispositionAnswered || rec.Answer != "the left one" {
		t.Errorf("record = %q/%q, want answered/'the left one'", rec.Disposition, rec.Answer)
	}
	if rec.Source != "graph:plan" || rec.JobID != "job-1" {
		t.Errorf("attribution = %q/%q, want graph:plan/job-1", rec.Source, rec.JobID)
	}
	if len(rec.Questions) != 1 || rec.Questions[0].Question != "Which?" {
		t.Errorf("questions round-trip failed: %+v", rec.Questions)
	}
	if rec.ResolvedAt.IsZero() {
		t.Error("ResolvedAt should be set")
	}
}

// TestBlockerHistory_DismissedDisposition verifies DismissPrompt records
// "dismissed" (not answered) and delivers a cancellation to the waiter.
func TestBlockerHistory_DismissedDisposition(t *testing.T) {
	t.Parallel()

	svc := newTestServiceWithStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.subscribe(ctx)

	done := make(chan struct{})
	go func() {
		_, _ = svc.broker.Ask(context.Background(), "req-1", func() {
			svc.BroadcastOperatorPrompt("req-1", []graphexec.PromptQuestion{{Question: "Q?"}})
		})
		svc.ResolveBlocker("req-1")
		close(done)
	}()

	drainFor(t, ch, EventTypeBlockerAdded)
	if err := svc.DismissPrompt(context.Background(), "req-1"); err != nil {
		t.Fatalf("DismissPrompt: %v", err)
	}
	<-done

	ev := drainFor(t, ch, EventTypeBlockerResolved)
	if payload := ev.Payload.(BlockerResolvedPayload); payload.Disposition != BlockerDispositionDismissed {
		t.Errorf("resolved disposition = %q, want dismissed", payload.Disposition)
	}

	hist, err := svc.BlockerHistory(context.Background(), 0)
	if err != nil {
		t.Fatalf("BlockerHistory: %v", err)
	}
	if len(hist) != 1 || hist[0].Disposition != BlockerDispositionDismissed {
		t.Fatalf("history = %+v, want one dismissed record", hist)
	}
	if hist[0].Answer != "" {
		t.Errorf("dismissed record answer = %q, want empty", hist[0].Answer)
	}
}

// TestBlockerHistory_CancelledDisposition verifies a waiter whose context
// ends without any response is recorded as "cancelled".
func TestBlockerHistory_CancelledDisposition(t *testing.T) {
	t.Parallel()

	svc := newTestServiceWithStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.subscribe(ctx)

	waitCtx, cancelWait := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_, _ = svc.broker.Ask(waitCtx, "req-1", func() {
			svc.BroadcastOperatorPrompt("req-1", []graphexec.PromptQuestion{{Question: "Q?"}})
		})
		svc.ResolveBlocker("req-1")
		close(done)
	}()

	drainFor(t, ch, EventTypeBlockerAdded)
	cancelWait() // the asker gives up (task killed / shutdown)
	<-done

	ev := drainFor(t, ch, EventTypeBlockerResolved)
	if payload := ev.Payload.(BlockerResolvedPayload); payload.Disposition != BlockerDispositionCancelled {
		t.Errorf("resolved disposition = %q, want cancelled", payload.Disposition)
	}

	hist, err := svc.BlockerHistory(context.Background(), 0)
	if err != nil {
		t.Fatalf("BlockerHistory: %v", err)
	}
	if len(hist) != 1 || hist[0].Disposition != BlockerDispositionCancelled {
		t.Fatalf("history = %+v, want one cancelled record", hist)
	}
}
