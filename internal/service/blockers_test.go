package service

import (
	"context"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/graphexec"
	"github.com/jefflinse/toasters/internal/operator"
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

	svc.BroadcastOperatorPrompt("op-1", []operator.PromptQuestion{{Question: "Q?"}})

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
