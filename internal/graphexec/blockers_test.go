package graphexec

import (
	"context"
	"testing"
	"time"

	"github.com/jefflinse/rhizome"

	"github.com/jefflinse/toasters/internal/hitl"
)

// hasResolveFor reports whether the sink recorded a resolve_blocker event for id.
func hasResolveFor(events []string, id string) bool {
	for _, e := range events {
		if e == "resolve_blocker:"+id {
			return true
		}
	}
	return false
}

func TestInterruptHandler_AskUser_ResolvesOnResponse(t *testing.T) {
	broker := hitl.New()
	sink := &capturingSink{}
	executor := NewExecutor(ExecutorConfig{EventSink: sink, Broker: broker})

	go func() {
		for i := 0; i < 100; i++ {
			if id := sink.lastPromptID(); id != "" {
				_ = broker.Respond(id, "ok")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := executor.interruptHandler(ctx, rhizome.InterruptRequest{
		Node:    "investigate",
		Kind:    InterruptKindAskUser,
		Payload: AskUserPayload{Question: "?"},
	}); err != nil {
		t.Fatalf("handler error: %v", err)
	}

	id := sink.lastPromptID()
	if !hasResolveFor(sink.snapshot(), id) {
		t.Errorf("expected resolve_blocker for %q after response; events = %v", id, sink.snapshot())
	}
}

func TestInterruptHandler_AskUser_ResolvesOnCancel(t *testing.T) {
	broker := hitl.New()
	sink := &capturingSink{}
	executor := NewExecutor(ExecutorConfig{EventSink: sink, Broker: broker})

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel once the prompt has been surfaced so Ask returns ctx.Err().
	go func() {
		for i := 0; i < 200; i++ {
			if sink.lastPromptID() != "" {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()

	_, err := executor.interruptHandler(ctx, rhizome.InterruptRequest{
		Node:    "investigate",
		Kind:    InterruptKindAskUser,
		Payload: AskUserPayload{Question: "?"},
	})
	if err == nil {
		t.Fatal("expected error from cancelled interrupt")
	}

	id := sink.lastPromptID()
	if id == "" {
		t.Fatal("prompt was never surfaced")
	}
	// The deferred ResolveBlocker runs on the cancel path too.
	if !hasResolveFor(sink.snapshot(), id) {
		t.Errorf("expected resolve_blocker for %q after cancel; events = %v", id, sink.snapshot())
	}
}
