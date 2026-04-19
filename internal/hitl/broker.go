// Package hitl provides a small request/response broker for human-in-the-loop
// prompts. Both the operator (its ask_user tool) and graph nodes (via
// rhizome.Interrupt) register pending prompts with a single broker, and the
// client's response flows back through the same delivery path — so there is
// one coordination point and one TUI surface regardless of who asked.
//
// The broker owns no event-emission concerns: callers pass a broadcast
// callback to Ask. This keeps the package free of service-layer types and
// lets each caller choose the event shape they emit (OperatorPromptPayload
// with Source set, for example).
package hitl

import (
	"context"
	"fmt"
	"sync"
)

// Broker coordinates pending human-in-the-loop prompts keyed by request ID.
// A single Broker instance serves both the operator's ask_user path and any
// graph-node interrupt that needs user input. Safe for concurrent use.
type Broker struct {
	mu      sync.Mutex
	pending map[string]chan string
}

// New returns an empty broker.
func New() *Broker {
	return &Broker{pending: make(map[string]chan string)}
}

// Ask registers requestID, invokes broadcast (so subscribers see the prompt),
// and blocks until Respond is called for the same requestID or ctx is done.
//
// broadcast is invoked after the channel is registered, so a response that
// arrives immediately cannot slip past the wait. If broadcast panics or
// fails, the pending entry is cleaned up — callers should therefore ensure
// broadcast is non-blocking.
//
// On ctx cancellation the pending entry is removed; a late Respond for the
// same ID will return an error.
func (b *Broker) Ask(ctx context.Context, requestID string, broadcast func()) (string, error) {
	if requestID == "" {
		return "", fmt.Errorf("hitl: requestID is required")
	}

	ch := make(chan string, 1)
	b.mu.Lock()
	if _, exists := b.pending[requestID]; exists {
		b.mu.Unlock()
		return "", fmt.Errorf("hitl: duplicate request ID %q", requestID)
	}
	b.pending[requestID] = ch
	b.mu.Unlock()

	if broadcast != nil {
		broadcast()
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, requestID)
		b.mu.Unlock()
		return "", ctx.Err()
	}
}

// Respond delivers text to whoever is waiting on requestID. Returns an error
// if no pending request matches (already answered, timed out, or never
// registered). The send is non-blocking — Ask uses a buffered cap-1 channel.
func (b *Broker) Respond(requestID, text string) error {
	b.mu.Lock()
	ch, ok := b.pending[requestID]
	if ok {
		delete(b.pending, requestID)
	}
	b.mu.Unlock()

	if !ok {
		return fmt.Errorf("hitl: no pending request %q", requestID)
	}
	ch <- text
	return nil
}
