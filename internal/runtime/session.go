package runtime

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jefflinse/toasters/internal/provider"
)

const subscriberBufSize = 64

// Session represents a running agent conversation.
type Session struct {
	id           string
	agentID      string
	jobID        string
	prov         provider.Provider
	model        string
	systemPrompt string
	messages     []provider.Message
	tools        []provider.Tool
	toolExec     ToolExecutor
	maxTurns     int

	// State — tokensIn/tokensOut use atomic for lock-free reads.
	status    string // "active", "completed", "failed", "cancelled"
	termErr   error  // terminal error from Run(), set under mu before return
	tokensIn  atomic.Int64
	tokensOut atomic.Int64
	startTime time.Time

	// Observer pattern.
	mu          sync.Mutex
	subscribers []chan SessionEvent

	// Context for cancellation.
	ctx    context.Context
	cancel context.CancelFunc

	// done is closed when Run() exits.
	done chan struct{}
}

func newSession(id string, p provider.Provider, opts SpawnOpts, toolExec ToolExecutor) *Session {
	maxTurns := opts.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}

	ctx, cancel := context.WithCancel(context.Background())

	s := &Session{
		id:           id,
		agentID:      opts.AgentID,
		jobID:        opts.JobID,
		prov:         p,
		model:        opts.Model,
		systemPrompt: opts.SystemPrompt,
		toolExec:     toolExec,
		maxTurns:     maxTurns,
		status:       "active",
		startTime:    time.Now(),
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
	}

	// Convert tool definitions to provider.Tool format.
	if toolExec != nil {
		for _, td := range toolExec.Definitions() {
			s.tools = append(s.tools, provider.Tool{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  td.Parameters,
			})
		}
	}

	// Seed with the initial user message.
	if opts.InitialMessage != "" {
		s.messages = append(s.messages, provider.Message{
			Role:    "user",
			Content: opts.InitialMessage,
		})
	}

	return s
}

// Run executes the conversation loop. It blocks until the conversation
// completes, fails, is cancelled, or exceeds max turns.
func (s *Session) Run(ctx context.Context) (retErr error) {
	defer func() {
		if retErr != nil {
			s.mu.Lock()
			s.termErr = retErr
			s.mu.Unlock()
		}
	}()
	defer close(s.done)
	defer s.closeSubscribers()

	// Merge the external context with the session's own context.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-s.ctx.Done():
			cancel()
		case <-ctx.Done():
		}
	}()

	for turn := 0; turn < s.maxTurns; turn++ {
		if ctx.Err() != nil {
			s.setStatus("cancelled")
			s.emit(SessionEvent{SessionID: s.id, Type: SessionEventError, Error: ctx.Err()})
			return ctx.Err()
		}

		// 1. Send messages to LLM.
		eventCh, err := s.prov.ChatStream(ctx, provider.ChatRequest{
			Model:    s.model,
			Messages: s.messages,
			Tools:    s.tools,
			System:   s.systemPrompt,
		})
		if err != nil {
			s.setStatus("failed")
			s.emit(SessionEvent{SessionID: s.id, Type: SessionEventError, Error: err})
			return fmt.Errorf("starting stream: %w", err)
		}

		// 2. Collect response, emitting events to subscribers.
		assistantMsg, toolCalls, err := s.collectResponse(ctx, eventCh)
		if err != nil {
			if ctx.Err() != nil {
				s.setStatus("cancelled")
			} else {
				s.setStatus("failed")
			}
			s.emit(SessionEvent{SessionID: s.id, Type: SessionEventError, Error: err})
			return fmt.Errorf("collecting response: %w", err)
		}
		s.messages = append(s.messages, assistantMsg)

		// 3. If no tool calls, we're done.
		if len(toolCalls) == 0 {
			s.setStatus("completed")
			s.emit(SessionEvent{SessionID: s.id, Type: SessionEventDone})
			return nil
		}

		// 4. Execute tool calls.
		for _, tc := range toolCalls {
			s.emit(SessionEvent{
				SessionID: s.id,
				Type:      SessionEventToolCall,
				ToolCall:  &ToolCallEvent{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments},
			})

			result, execErr := s.toolExec.Execute(ctx, tc.Name, tc.Arguments)

			resultEvent := &ToolResultEvent{CallID: tc.ID, Name: tc.Name, Result: result}
			if execErr != nil {
				log.Printf("warning: tool %q execution error: %v", tc.Name, execErr)
				resultEvent.Error = execErr.Error()
				result = fmt.Sprintf("error: %s", execErr.Error())
			}
			s.emit(SessionEvent{
				SessionID:  s.id,
				Type:       SessionEventToolResult,
				ToolResult: resultEvent,
			})

			s.messages = append(s.messages, provider.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}

		// 5. Loop — send tool results back to LLM.
	}

	s.setStatus("failed")
	err := fmt.Errorf("max turns (%d) exceeded", s.maxTurns)
	s.emit(SessionEvent{SessionID: s.id, Type: SessionEventError, Error: err})
	return err
}

// collectResponse reads from the event channel, accumulates text and tool calls,
// emits text events to subscribers, and tracks usage.
func (s *Session) collectResponse(ctx context.Context, eventCh <-chan provider.StreamEvent) (provider.Message, []provider.ToolCall, error) {
	var textBuf strings.Builder
	var toolCalls []provider.ToolCall

	for {
		select {
		case <-ctx.Done():
			return provider.Message{}, nil, ctx.Err()

		case ev, ok := <-eventCh:
			if !ok {
				// Channel closed — return what we have.
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
				s.emit(SessionEvent{SessionID: s.id, Type: SessionEventText, Text: ev.Text})

			case provider.EventToolCall:
				if ev.ToolCall != nil {
					toolCalls = append(toolCalls, *ev.ToolCall)
				}

			case provider.EventUsage:
				if ev.Usage != nil {
					s.tokensIn.Add(int64(ev.Usage.InputTokens))
					s.tokensOut.Add(int64(ev.Usage.OutputTokens))
				}

			case provider.EventError:
				return provider.Message{}, nil, ev.Error

			case provider.EventDone:
				// Continue reading until channel closes.
			}
		}
	}
}

// Subscribe returns a channel that receives events for this session.
// Buffer size 64. Non-blocking sends — slow subscribers miss events.
func (s *Session) Subscribe() <-chan SessionEvent {
	ch := make(chan SessionEvent, subscriberBufSize)
	s.mu.Lock()
	s.subscribers = append(s.subscribers, ch)
	s.mu.Unlock()
	return ch
}

// Snapshot returns a read-only view of the session state.
func (s *Session) Snapshot() SessionSnapshot {
	s.mu.Lock()
	status := s.status
	s.mu.Unlock()

	return SessionSnapshot{
		ID:        s.id,
		AgentID:   s.agentID,
		JobID:     s.jobID,
		Status:    status,
		Model:     s.model,
		Provider:  s.prov.Name(),
		StartTime: s.startTime,
		TokensIn:  s.tokensIn.Load(),
		TokensOut: s.tokensOut.Load(),
	}
}

// FinalText returns the last assistant message text (for spawn_agent results).
func (s *Session) FinalText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.messages) - 1; i >= 0; i-- {
		if s.messages[i].Role == "assistant" && s.messages[i].Content != "" {
			return s.messages[i].Content
		}
	}
	return ""
}

// TermErr returns the terminal error from Run(), if any. Safe for concurrent use.
func (s *Session) TermErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.termErr
}

// Cancel cancels the session's context.
func (s *Session) Cancel() {
	s.cancel()
}

// Done returns a channel that is closed when the session's Run() exits.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// ID returns the session identifier.
func (s *Session) ID() string {
	return s.id
}

// SystemPrompt returns the system prompt given to the LLM.
func (s *Session) SystemPrompt() string {
	return s.systemPrompt
}

// InitialMessage returns the initial user message, if any.
func (s *Session) InitialMessage() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) > 0 && s.messages[0].Role == "user" {
		return s.messages[0].Content
	}
	return ""
}

// emit sends an event to all subscribers. Non-blocking — slow subscribers miss events.
func (s *Session) emit(ev SessionEvent) {
	s.mu.Lock()
	subs := make([]chan SessionEvent, len(s.subscribers))
	copy(subs, s.subscribers)
	s.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
			// Slow subscriber — drop event.
		}
	}
}

func (s *Session) setStatus(status string) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
}

func (s *Session) closeSubscribers() {
	s.mu.Lock()
	for _, ch := range s.subscribers {
		close(ch)
	}
	s.subscribers = nil
	s.mu.Unlock()
}
