package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
)

const subscriberBufSize = 64

// Session represents a running worker conversation.
type Session struct {
	id           string
	workerID     string
	teamName     string // team this worker belongs to (may be empty)
	task         string // short human-readable description of what this worker is doing
	jobID        string
	taskID       string
	prov         provider.Provider
	model        string
	systemPrompt string
	// messages holds the full conversation history (user, assistant, tool messages).
	//
	// Concurrency contract:
	//   - Appended only by Run() (the session's single main goroutine), always
	//     via appendMessage, which holds mu — readers like FinalText() and
	//     InitialMessage() can run concurrently with an active session.
	//   - Run() itself may read without mu (it is the only writer).
	//   - Callers that need a consistent view of the full slice should wait
	//     for Done().
	messages []provider.Message
	tools    []provider.Tool
	toolExec ToolExecutor
	maxTurns int

	// Transcript persistence — store may be nil (no persistence).
	store db.Store
	seq   int // message sequence counter for session_messages

	// Compaction wiring, set by SpawnWorker after newSession. providerName
	// is the registry key ("lmstudio") for exact window lookups; the
	// threshold pointer aliases the Runtime's atomic so /settings changes
	// apply to in-flight sessions at their next turn boundary. All optional
	// (nil disables the pre-flight check; the overflow backstop degrades to
	// estimate-based sizing).
	providerName        string
	ctxWindows          ContextWindowSource
	compactionThreshold *atomic.Int32
	promptEngine        *prompt.Engine
	// compactions counts this session's history compactions; read by
	// Snapshot under mu. compactionSuppressed disarms the pre-flight check
	// when even a compacted history exceeds the budget (floor guard);
	// touched only by Run()'s goroutine.
	compactions          int
	compactionSuppressed bool

	// State — tokensIn/tokensOut use atomic for lock-free reads.
	status    string // "active", "completed", "failed", "cancelled"
	termErr   error  // terminal error from Run(), set under mu before return
	tokensIn  atomic.Int64
	tokensOut atomic.Int64
	// lastInputTokens is the prompt size of the most recent API round-trip —
	// i.e. the session's current context occupancy. Unlike tokensIn (a
	// cumulative sum across every round-trip), this reflects how full the
	// model's context window is right now.
	lastInputTokens atomic.Int64
	startTime       time.Time

	// Observer pattern.
	mu          sync.Mutex
	subscribers []chan SessionEvent
	closed      bool // set to true by closeSubscribers(), under mu

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
		workerID:     opts.WorkerID,
		teamName:     opts.TeamName,
		task:         opts.Task,
		jobID:        opts.JobID,
		taskID:       opts.TaskID,
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
	// Merge the external context with the session's own context.
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)

	// Defers run LIFO. Execution order:
	//   1. termErr setter  — must run FIRST so termErr is visible before done closes
	//   2. close(s.done)   — unblocks SpawnAndWait callers
	//   3. closeSubscribers — closes subscriber channels
	//   4. cancel          — cancels the merged context
	//   5. stop            — stops the AfterFunc watcher
	defer stop()
	defer cancel()
	defer s.closeSubscribers()
	defer close(s.done)
	defer func() {
		if retErr != nil {
			s.mu.Lock()
			s.termErr = retErr
			s.mu.Unlock()
		}
	}()

	// Persist the initial user message (seeded in newSession).
	if len(s.messages) > 0 {
		s.persistMessage(s.messages[0])
	}

	// retriedOverflow arms the context-overflow backstop: on the first
	// classified overflow of a turn, the history is force-compacted and the
	// same turn retried once. Reset after any successful round-trip.
	retriedOverflow := false

	for turn := 0; turn < s.maxTurns; turn++ {
		if ctx.Err() != nil {
			s.setStatus("cancelled")
			s.emit(SessionEvent{SessionID: s.id, Type: SessionEventError, Error: ctx.Err()})
			return ctx.Err()
		}

		// 0. Pre-flight compaction: shrink the history before it can
		// overflow. No-op until the first round-trip reports occupancy.
		s.maybeCompact(ctx)

		// 1. Send messages to LLM.
		eventCh, err := s.prov.ChatStream(ctx, provider.ChatRequest{
			Model:    s.model,
			Messages: s.messages,
			Tools:    s.tools,
			System:   s.systemPrompt,
		})
		if err != nil {
			if !retriedOverflow && provider.IsContextOverflow(err) {
				retriedOverflow = true
				s.forceCompact(ctx)
				turn-- // the retry re-uses this turn's budget slot
				continue
			}
			s.setStatus("failed")
			s.emit(SessionEvent{SessionID: s.id, Type: SessionEventError, Error: err})
			return fmt.Errorf("starting stream: %w", err)
		}

		// 2. Collect response, emitting events to subscribers.
		assistantMsg, toolCalls, err := s.collectResponse(ctx, eventCh)
		if err != nil {
			if ctx.Err() == nil && !retriedOverflow && provider.IsContextOverflow(err) {
				// Providers deliver HTTP errors as stream events, so the
				// overflow backstop lives on this path too: compact once
				// and retry the turn instead of failing terminally.
				retriedOverflow = true
				s.forceCompact(ctx)
				turn--
				continue
			}
			if ctx.Err() != nil {
				s.setStatus("cancelled")
			} else {
				s.setStatus("failed")
			}
			s.emit(SessionEvent{SessionID: s.id, Type: SessionEventError, Error: err})
			return fmt.Errorf("collecting response: %w", err)
		}
		retriedOverflow = false
		// Repair malformed tool-call args (empty/truncated JSON from small
		// local models) before they enter the history — one bad call would
		// otherwise 400 every subsequent request and the session never
		// recovers. Same fix the operator applies to its own turn loop.
		provider.NormalizeToolCallArgs(assistantMsg.ToolCalls)
		provider.NormalizeToolCallArgs(toolCalls)
		s.appendMessage(assistantMsg)
		s.persistMessage(assistantMsg)

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
				slog.Warn("tool execution error", "tool", tc.Name, "error", execErr)
				resultEvent.Error = execErr.Error()
				result = fmt.Sprintf("error: %s", execErr.Error())
			}
			s.emit(SessionEvent{
				SessionID:  s.id,
				Type:       SessionEventToolResult,
				ToolResult: resultEvent,
			})

			// Cap tool results before storing in message history to prevent
			// context window overflow when workers read large files or directory
			// listings. 8KB per result keeps the conversation manageable while
			// still providing meaningful content to the LLM.
			//
			// spawn_worker is exempt: its result is the synthesized output of an
			// entire child worker session, which is typically a concise summary
			// but can legitimately exceed 8KB. Truncating it causes the parent
			// to misinterpret the child's work as incomplete and retry in a loop.
			const maxToolResultBytes = 8 * 1024
			if tc.Name != "spawn_worker" && len(result) > maxToolResultBytes {
				// Walk backward off any multi-byte UTF-8 character so the cut
				// doesn't leave an invalid sequence the provider rejects.
				cut := maxToolResultBytes
				for cut > 0 && !utf8.RuneStart(result[cut]) {
					cut--
				}
				result = result[:cut] + "\n[... truncated: result exceeded 8KB ...]"
			}

			toolMsg := provider.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			}
			s.appendMessage(toolMsg)
			s.persistMessage(toolMsg)
		}

		// 5. Loop — send tool results back to LLM.
	}

	s.setStatus("failed")
	err := fmt.Errorf("max turns (%d) exceeded", s.maxTurns)
	s.emit(SessionEvent{SessionID: s.id, Type: SessionEventError, Error: err})
	return err
}

// appendMessage adds a message to the history under mu so FinalText and
// InitialMessage can read concurrently while the session is active.
func (s *Session) appendMessage(msg provider.Message) {
	s.mu.Lock()
	s.messages = append(s.messages, msg)
	s.mu.Unlock()
}

// persistMessage writes a message to the session_messages table.
// Non-fatal: logs a warning on failure.
func (s *Session) persistMessage(msg provider.Message) {
	if s.store == nil {
		return
	}
	s.seq++
	sm := &db.SessionMessage{
		SessionID:  s.id,
		Seq:        s.seq,
		Role:       msg.Role,
		Content:    msg.Content,
		ToolCallID: msg.ToolCallID,
	}
	if len(msg.ToolCalls) > 0 {
		if data, err := json.Marshal(msg.ToolCalls); err == nil {
			sm.ToolCalls = string(data)
		}
	}
	if err := s.store.AppendSessionMessage(context.Background(), sm); err != nil {
		slog.Warn("failed to persist session message", "session", s.id, "seq", s.seq, "error", err)
	}
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
					// The most recent round-trip's prompt size is the current
					// context occupancy; overwrite rather than accumulate.
					s.lastInputTokens.Store(int64(ev.Usage.InputTokens))
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
// If the session has already completed, the returned channel is closed
// immediately so callers do not block forever.
func (s *Session) Subscribe() <-chan SessionEvent {
	ch := make(chan SessionEvent, subscriberBufSize)
	s.mu.Lock()
	defer s.mu.Unlock()
	// Check s.closed under mu. closeSubscribers() sets closed=true before
	// closing channels, so this flag is the authoritative signal that no
	// further closes will happen. Using s.done would leave a window between
	// closeSubscribers() running and close(s.done) executing where a new
	// subscriber could be appended and never closed.
	if s.closed {
		close(ch)
		return ch
	}
	s.subscribers = append(s.subscribers, ch)
	return ch
}

// Snapshot returns a read-only view of the session state.
// Safe to call at any time (acquires mu for mutable fields).
// After <-Done(), the returned values are final.
func (s *Session) Snapshot() SessionSnapshot {
	s.mu.Lock()
	status := s.status
	compactions := s.compactions
	s.mu.Unlock()

	return SessionSnapshot{
		ID:                   s.id,
		WorkerID:             s.workerID,
		TeamName:             s.teamName,
		JobID:                s.jobID,
		TaskID:               s.taskID,
		Status:               status,
		Model:                s.model,
		Provider:             s.prov.Name(),
		StartTime:            s.startTime,
		TokensIn:             s.tokensIn.Load(),
		TokensOut:            s.tokensOut.Load(),
		CurrentContextTokens: s.lastInputTokens.Load(),
		Compactions:          compactions,
	}
}

// FinalText returns the last assistant message text (for spawn_worker results).
// Safe to call at any time (acquires mu), but the result is only meaningful
// after <-Done() closes, since Run() may still be appending messages.
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
// Safe to call at any time (acquires mu). The initial message is set at
// construction and never modified, so the value is stable even before Done().
func (s *Session) InitialMessage() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) > 0 && s.messages[0].Role == "user" {
		return s.messages[0].Content
	}
	return ""
}

// Task returns the short human-readable description of what this worker is doing.
// task is set once at construction and never mutated; no lock is needed.
func (s *Session) Task() string { return s.task }

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
	s.closed = true
	for _, ch := range s.subscribers {
		close(ch)
	}
	s.subscribers = nil
	s.mu.Unlock()
}
