package service

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/provider"
)

// SetGraphExecutor sets the graph executor on the service after construction.
// Required when startup-time operator creation is skipped (no operator
// provider configured yet) and the operator is instead created live via
// startOperator after the user selects a provider in the TUI.
func (s *LocalService) SetGraphExecutor(g operator.GraphTaskExecutor) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.graphExec = g
}

// SetOperator sets the operator on the service after construction. This is
// needed because the operator's callbacks reference the service, creating a
// circular dependency that prevents passing the operator at construction time.
func (s *LocalService) SetOperator(op *operator.Operator) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.op = op
}

// currentProvider returns the active LLM provider client, honoring live
// operator activation under opMu.
func (s *LocalService) currentProvider() provider.Provider {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.opProvider
}

// operatorInfo returns the active operator's model name and endpoint for
// status display.
func (s *LocalService) operatorInfo() (model, endpoint string) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.opModel, s.opEndpoint
}

// currentGraphExecutor returns the graph executor, honoring post-construction
// wiring via SetGraphExecutor.
func (s *LocalService) currentGraphExecutor() operator.GraphTaskExecutor {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.graphExec
}

// currentDefaults returns the default provider id and model for dispatching
// graph tasks. These follow the operator's provider on live activation.
func (s *LocalService) currentDefaults() (providerID, model string) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.defaultProvider, s.defaultModel
}

// currentOperator returns the currently active operator, if any, honoring
// live activation/replacement under opMu.
func (s *LocalService) currentOperator() *operator.Operator {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.op
}

// SendMessage sends a user message to the operator event loop and returns a
// turnID for correlating subsequent operator.text and operator.done events.
func (s *LocalService) SendMessage(ctx context.Context, message string) (string, error) {
	op := s.currentOperator()
	if op == nil {
		return "", Unavailablef("operator not configured")
	}
	if len(message) > maxMessageLen {
		return "", fmt.Errorf("message too large: %d bytes exceeds maximum %d", len(message), maxMessageLen)
	}

	uuidVal, err := uuid.NewV4()
	if err != nil {
		return "", fmt.Errorf("generating turn ID: %w", err)
	}
	turnID := uuidVal.String()

	s.turnMu.Lock()
	if s.currentTurnID != "" {
		s.turnMu.Unlock()
		return "", Conflictf("operator turn already in progress")
	}
	s.currentTurnID = turnID
	s.turnMu.Unlock()

	if err := op.Send(ctx, operator.Event{
		Type:    operator.EventUserMessage,
		Payload: operator.UserMessagePayload{Text: message, TurnID: turnID},
	}); err != nil {
		s.turnMu.Lock()
		s.currentTurnID = ""
		s.turnMu.Unlock()
		return "", fmt.Errorf("sending message to operator: %w", err)
	}

	s.appendHistory(ChatEntry{
		Message:   ChatMessage{Role: MessageRoleUser, Content: message},
		Timestamp: time.Now(),
	})

	return turnID, nil
}

// RespondToPrompt sends the user's answer to an active ask_user prompt.
// Routed through the shared HITL broker, so it works for both operator
// prompts and graph-node interrupts without the service needing to know
// which path is waiting.
func (s *LocalService) RespondToPrompt(_ context.Context, requestID string, response string) error {
	if len(response) > maxResponseLen {
		return fmt.Errorf("response too large: %d bytes exceeds maximum %d", len(response), maxResponseLen)
	}
	return s.broker.Respond(requestID, response)
}

// Status returns the current state of the operator.
func (s *LocalService) Status(_ context.Context) (OperatorStatus, error) {
	if s.currentOperator() == nil {
		return OperatorStatus{
			State: OperatorStateDisabled,
		}, nil
	}

	s.turnMu.Lock()
	turnID := s.currentTurnID
	s.turnMu.Unlock()

	state := OperatorStateIdle
	if turnID != "" {
		state = OperatorStateStreaming
	}

	model, endpoint := s.operatorInfo()
	return OperatorStatus{
		State:         state,
		CurrentTurnID: turnID,
		ModelName:     model,
		Endpoint:      endpoint,
	}, nil
}

// appendHistory persists a ChatEntry to the chat_entries table so that the
// conversation survives a server restart. If the store is unavailable the
// entry is silently dropped — chat history is best-effort, not load-bearing.
func (s *LocalService) appendHistory(entry ChatEntry) {
	if s.cfg.Store == nil {
		return
	}
	dbEntry := &db.ChatEntry{
		Timestamp: entry.Timestamp,
		Role:      string(entry.Message.Role),
		Content:   entry.Message.Content,
		Reasoning: entry.Reasoning,
		Meta:      entry.ClaudeMeta,
	}
	// AppendChatEntry takes its own short-lived context so a slow caller can't
	// stall on a transient DB write.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.cfg.Store.AppendChatEntry(ctx, dbEntry); err != nil {
		slog.Warn("failed to persist chat entry", "error", err)
	}
}

// History returns the most recent maxHistoryEntries chat entries from SQLite,
// in chronological order (oldest first). Used by clients to hydrate the chat
// view on connect.
func (s *LocalService) History(ctx context.Context) ([]ChatEntry, error) {
	if s.cfg.Store == nil {
		return nil, nil
	}
	dbEntries, err := s.cfg.Store.ListRecentChatEntries(ctx, maxHistoryEntries)
	if err != nil {
		return nil, fmt.Errorf("listing chat entries: %w", err)
	}
	out := make([]ChatEntry, 0, len(dbEntries))
	for _, e := range dbEntries {
		out = append(out, ChatEntry{
			Message: ChatMessage{
				Role:    MessageRole(e.Role),
				Content: e.Content,
			},
			Timestamp:  e.Timestamp,
			Reasoning:  e.Reasoning,
			ClaudeMeta: e.Meta,
		})
	}
	return out, nil
}

// workerDefaultsApplier is the optional surface a graph executor exposes to
// receive runtime updates of the global worker temperature/thinking
// defaults. *graphexec.Executor satisfies it; tests that pass a mock
// executor without this method silently skip the live update.
type workerDefaultsApplier interface {
	SetWorkerDefaults(thinkingEnabled bool, temperature float64)
}

// startOperator creates and starts a new operator, replacing any existing one.
func (s *LocalService) startOperator(p provider.Provider, providerID, model string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	// Stop existing operator if running.
	if s.opCancel != nil {
		s.opCancel()
		s.opCancel = nil
	}

	// A user turn queued on the old operator's event channel will never
	// produce an OnTurnDone, so release the SendMessage gate here — otherwise
	// every future SendMessage returns "turn already in progress" until the
	// server restarts.
	s.turnMu.Lock()
	s.currentTurnID = ""
	s.pendingResponse.Reset()
	s.pendingReasoning.Reset()
	s.turnMu.Unlock()

	// Compose the operator system prompt via the prompt engine.
	var systemPrompt string
	if s.cfg.PromptEngine != nil {
		composed, err := s.cfg.PromptEngine.Compose("operator", nil, nil)
		if err != nil {
			slog.Warn("failed to compose operator for live activation", "error", err)
		} else {
			systemPrompt = composed
		}
	}
	if systemPrompt == "" {
		systemPrompt = "You are the Toasters operator."
	}

	// activeTurn tracks the turn ID the operator is currently streaming so
	// timer-driven batch flushes stamp text with the right turn. Turns are
	// serial and OnTurnDone flushes both batchers before clearing it, so a
	// batch never straddles a turn boundary.
	var activeTurn atomic.Value
	activeTurn.Store("")
	textFlush := func(text string) {
		turnID, _ := activeTurn.Load().(string)
		s.BroadcastOperatorText(turnID, text, "")
	}
	reasoningFlush := func(text string) {
		turnID, _ := activeTurn.Load().(string)
		s.BroadcastOperatorText(turnID, "", text)
	}
	batcher := newTextBatcher(16*time.Millisecond, textFlush)
	reasoningBatcher := newTextBatcher(16*time.Millisecond, reasoningFlush)

	op, err := operator.New(operator.Config{
		Runtime:                s.cfg.Runtime,
		Provider:               p,
		Model:                  model,
		WorkDir:                s.cfg.WorkspaceDir,
		Store:                  s.cfg.Store,
		SystemPrompt:           systemPrompt,
		SessionFile:            filepath.Join(s.cfg.ConfigDir, "sessions", "operator.json"),
		SystemEventBroadcaster: s,
		// opMu is held for the whole of startOperator, so these are direct
		// field reads — the accessors would deadlock.
		GraphExecutor:   s.graphExec,
		GraphCatalog:    s.cfg.GraphCatalog,
		Broker:          s.broker,
		PromptEngine:    s.cfg.PromptEngine,
		DefaultProvider: s.defaultProvider,
		DefaultModel:    s.defaultModel,
		LifetimeCtx:     s.ctx,
		OnText: func(turnID, text string) {
			activeTurn.Store(turnID)
			batcher.Add(text)
		},
		OnReasoning: func(turnID, text string) {
			activeTurn.Store(turnID)
			reasoningBatcher.Add(text)
		},
		OnEvent: func(event operator.Event) {
			s.BroadcastOperatorEvent(event)
		},
		OnTurnDone: func(turnID string, tokensIn, tokensOut, reasoningTokens int) {
			reasoningBatcher.Flush()
			batcher.Flush()
			activeTurn.Store("")
			s.BroadcastOperatorDone(turnID, model, tokensIn, tokensOut, reasoningTokens)
		},
		OnPrompt:   s.BroadcastOperatorPrompt,
		OnResolve:  s.ResolveBlocker,
		OnToolCall: s.BroadcastOperatorToolCall,
	})
	if err != nil {
		return fmt.Errorf("creating operator: %w", err)
	}

	// Update service state (still under opMu).
	s.op = op
	s.opModel = model
	s.opProvider = p

	// Look up endpoint for sidebar display.
	if s.cfg.Loader != nil {
		for _, pc := range s.cfg.Loader.Providers() {
			if pc.Key() == providerID {
				s.opEndpoint = pc.Endpoint
				break
			}
		}
	}

	// Start the operator event loop.
	opCtx, opCancel := context.WithCancel(s.ctx)
	s.opCancel = opCancel
	op.Start(opCtx)

	slog.Info("operator started live", "provider", providerID, "model", model)
	return nil
}
