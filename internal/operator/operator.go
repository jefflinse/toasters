// Package operator implements the operator event loop for coordinating
// LLM-powered agent work. The operator maintains a long-lived conversation
// with an LLM, receives typed events on a buffered channel, and dispatches
// them — forwarding user messages to the LLM and handling routine events
// mechanically.
package operator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

const (
	eventChSize = 64

	defaultSystemPrompt = `You are the operator — an orchestration agent that coordinates work.
When a user sends a message, analyze it and decide how to proceed.
You can consult specialized agents using the consult_agent tool.
Available agents: planner, reviewer.
Be concise and helpful.`
)

// Operator manages the event loop and long-lived operator LLM session.
type Operator struct {
	rt      *runtime.Runtime
	prov    provider.Provider
	model   string
	tools   *operatorTools
	eventCh chan Event
	workDir string

	// Long-lived conversation state. Protected by mu for concurrent access
	// from the event loop goroutine and external callers (e.g. MessageCount).
	mu           sync.Mutex
	systemPrompt string
	messages     []provider.Message
	provTools    []provider.Tool

	// Callbacks.
	OnText  func(text string) // called with streamed text from the operator LLM
	OnEvent func(event Event) // called when the event loop processes an event
}

// Config holds configuration for creating an Operator.
type Config struct {
	Runtime      *runtime.Runtime
	Provider     provider.Provider
	Model        string
	WorkDir      string
	SystemPrompt string // optional; uses default if empty
}

// New creates a new Operator. Call Start to begin processing events.
func New(cfg Config) *Operator {
	systemPrompt := cfg.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt
	}

	tools := newOperatorTools(cfg.Runtime, cfg.Provider.Name(), cfg.Model, cfg.WorkDir)
	provTools := operatorToolsToProviderTools(tools.Definitions())

	return &Operator{
		rt:           cfg.Runtime,
		prov:         cfg.Provider,
		model:        cfg.Model,
		tools:        tools,
		eventCh:      make(chan Event, eventChSize),
		workDir:      cfg.WorkDir,
		systemPrompt: systemPrompt,
		provTools:    provTools,
	}
}

// Send pushes an event into the operator's event channel. Non-blocking if the
// channel has capacity; blocks if the buffer is full.
func (o *Operator) Send(event Event) {
	o.eventCh <- event
}

// Start launches the event loop goroutine. It processes events until ctx is
// cancelled. The goroutine exits cleanly when the context is done.
func (o *Operator) Start(ctx context.Context) {
	go o.run(ctx)
}

// run is the event loop. It blocks on the event channel and dispatches events.
func (o *Operator) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			slog.Info("operator event loop shutting down", "reason", ctx.Err())
			return

		case ev := <-o.eventCh:
			o.handleEvent(ctx, ev)
		}
	}
}

// handleEvent dispatches a single event. User messages go to the LLM;
// routine events are handled mechanically.
func (o *Operator) handleEvent(ctx context.Context, ev Event) {
	// Notify observer.
	if o.OnEvent != nil {
		o.OnEvent(ev)
	}

	switch ev.Type {
	case EventUserMessage:
		payload, ok := ev.Payload.(UserMessagePayload)
		if !ok {
			slog.Error("invalid payload for user_message event", "payload", ev.Payload)
			return
		}
		o.handleUserMessage(ctx, payload)

	case EventTaskCompleted:
		payload, ok := ev.Payload.(TaskCompletedPayload)
		if !ok {
			slog.Error("invalid payload for task_completed event", "payload", ev.Payload)
			return
		}
		slog.Info("task completed", "task_id", payload.TaskID, "summary", payload.Summary)

	case EventTaskFailed:
		payload, ok := ev.Payload.(TaskFailedPayload)
		if !ok {
			slog.Error("invalid payload for task_failed event", "payload", ev.Payload)
			return
		}
		slog.Warn("task failed", "task_id", payload.TaskID, "error", payload.Error)

	case EventBlockerReported:
		payload, ok := ev.Payload.(BlockerReportedPayload)
		if !ok {
			slog.Error("invalid payload for blocker_reported event", "payload", ev.Payload)
			return
		}
		slog.Warn("blocker reported", "agent_id", payload.AgentID, "description", payload.Description)

	default:
		slog.Warn("unknown event type", "type", ev.Type)
	}
}

// handleUserMessage sends a user message to the operator LLM and processes
// the response, including any tool calls. This drives the operator's
// conversation turn by turn.
func (o *Operator) handleUserMessage(ctx context.Context, payload UserMessagePayload) {
	// Append user message to the long-lived conversation.
	o.appendMessage(provider.Message{
		Role:    "user",
		Content: payload.Text,
	})

	// Run the operator's turn — may involve multiple LLM round-trips if the
	// LLM makes tool calls.
	for {
		if ctx.Err() != nil {
			return
		}

		// Snapshot messages under lock for the ChatStream call.
		o.mu.Lock()
		msgs := make([]provider.Message, len(o.messages))
		copy(msgs, o.messages)
		o.mu.Unlock()

		eventCh, err := o.prov.ChatStream(ctx, provider.ChatRequest{
			Model:    o.model,
			Messages: msgs,
			Tools:    o.provTools,
			System:   o.systemPrompt,
		})
		if err != nil {
			slog.Error("operator ChatStream failed", "error", err)
			o.emitText(fmt.Sprintf("[operator error: %s]", err))
			return
		}

		assistantMsg, toolCalls, err := o.collectResponse(ctx, eventCh)
		if err != nil {
			slog.Error("operator response collection failed", "error", err)
			o.emitText(fmt.Sprintf("[operator error: %s]", err))
			return
		}
		o.appendMessage(assistantMsg)

		// No tool calls — the operator's turn is done.
		if len(toolCalls) == 0 {
			return
		}

		// Execute tool calls and feed results back.
		for _, tc := range toolCalls {
			slog.Info("operator tool call", "tool", tc.Name, "id", tc.ID)

			result, execErr := o.tools.Execute(ctx, tc.Name, tc.Arguments)

			if execErr != nil {
				slog.Warn("operator tool execution error", "tool", tc.Name, "error", execErr)
				result = fmt.Sprintf("error: %s", execErr.Error())
			}

			o.appendMessage(provider.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}

		// Loop — send tool results back to LLM for the next response.
	}
}

// appendMessage adds a message to the conversation history under lock.
func (o *Operator) appendMessage(msg provider.Message) {
	o.mu.Lock()
	o.messages = append(o.messages, msg)
	o.mu.Unlock()
}

// collectResponse reads from the event channel, accumulates text and tool
// calls, and streams text to the OnText callback.
func (o *Operator) collectResponse(ctx context.Context, eventCh <-chan provider.StreamEvent) (provider.Message, []provider.ToolCall, error) {
	var textBuf strings.Builder
	var toolCalls []provider.ToolCall

	for {
		select {
		case <-ctx.Done():
			return provider.Message{}, nil, ctx.Err()

		case ev, ok := <-eventCh:
			if !ok {
				msg := provider.Message{
					Role:      "assistant",
					Content:   textBuf.String(),
					ToolCalls: toolCalls,
				}
				return msg, toolCalls, nil
			}

			switch ev.Type {
			case provider.EventText:
				textBuf.WriteString(ev.Text)
				o.emitText(ev.Text)

			case provider.EventToolCall:
				if ev.ToolCall != nil {
					toolCalls = append(toolCalls, *ev.ToolCall)
				}

			case provider.EventError:
				return provider.Message{}, nil, ev.Error

			case provider.EventDone:
				// Continue reading until channel closes.

			case provider.EventUsage:
				// Track usage if needed in the future.
			}
		}
	}
}

// emitText calls the OnText callback if set.
func (o *Operator) emitText(text string) {
	if o.OnText != nil {
		o.OnText(text)
	}
}

// MessageCount returns the number of messages in the operator's conversation
// history. Useful for testing that the session is long-lived.
func (o *Operator) MessageCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.messages)
}
