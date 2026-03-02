// Package service provides the in-process implementation of the Service interface.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"gopkg.in/yaml.v3"

	"github.com/jefflinse/toasters/internal/agentfmt"
	"github.com/jefflinse/toasters/internal/compose"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/loader"
	"github.com/jefflinse/toasters/internal/mcp"
	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// Compile-time assertion that LocalService satisfies the Service interface.
var _ Service = (*LocalService)(nil)

// Size limits for input validation.
const (
	maxMessageLen = 102400           // 100KB — maximum user message size
	maxPromptLen  = 51200            // 50KB — maximum generation prompt size
	maxCopySize   = 50 * 1024 * 1024 // 50MB — maximum file copy size
)

// maxConcurrentOps bounds the number of concurrent async operations (generate,
// promote, detect) that can run simultaneously.
const maxConcurrentOps = 5

// maxHistoryEntries bounds the conversation history kept for reconnect hydration.
const maxHistoryEntries = 1000

// LocalConfig holds the dependencies for LocalService.
type LocalConfig struct {
	Store         db.Store
	Runtime       *runtime.Runtime
	Operator      *operator.Operator
	MCPManager    *mcp.Manager
	Provider      provider.Provider // operator's LLM provider (for ListModels, generation)
	Composer      *compose.Composer
	Loader        *loader.Loader
	ConfigDir     string
	WorkspaceDir  string
	TeamsDir      string
	OperatorModel string    // for OperatorStatus.ModelName
	StartTime     time.Time // for Health().Uptime
}

// LocalService is the in-process implementation of Service. It delegates to
// existing internal components (db.Store, operator.Operator, runtime.Runtime,
// mcp.Manager, etc.) and multiplexes events from all sources into a single
// channel per subscriber.
type LocalService struct {
	cfg LocalConfig

	// Service lifetime context — cancelled by Shutdown().
	ctx    context.Context
	cancel context.CancelFunc

	// Event stream state.
	mu          sync.Mutex
	subscribers map[uint64]chan Event
	nextSubID   uint64
	seqCounter  uint64 // protected by mu
	startOnce   sync.Once

	// Operator turn correlation.
	turnMu          sync.Mutex
	currentTurnID   string
	pendingResponse strings.Builder // accumulates text during a turn

	// Conversation history for reconnect hydration.
	historyMu sync.Mutex
	history   []ChatEntry

	// asyncSem bounds concurrent async operations (generate, promote, detect).
	asyncSem chan struct{}
}

// localJobService wraps LocalService to implement JobService without conflicting
// with SessionService methods of the same name (List, Get, Cancel).
type localJobService struct{ svc *LocalService }

// localSessionService wraps LocalService to implement SessionService without
// conflicting with JobService methods of the same name (List, Get, Cancel).
type localSessionService struct{ svc *LocalService }

// sanitizeName strips characters that could cause YAML injection when
// interpolated into frontmatter templates. Newlines, carriage returns, and
// null bytes are removed entirely.
func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\x00", "")
	return name
}

// NewLocal creates a new LocalService from the given config.
func NewLocal(cfg LocalConfig) *LocalService {
	if cfg.StartTime.IsZero() {
		cfg.StartTime = time.Now()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &LocalService{
		cfg:         cfg,
		ctx:         ctx,
		cancel:      cancel,
		subscribers: make(map[uint64]chan Event),
		asyncSem:    make(chan struct{}, maxConcurrentOps),
	}
}

// Shutdown cancels the service lifetime context, stopping background goroutines.
func (s *LocalService) Shutdown() { s.cancel() }

// tryAcquireAsync attempts to acquire a slot for an async operation.
// Returns false if the semaphore is full (too many concurrent operations).
func (s *LocalService) tryAcquireAsync() bool {
	select {
	case s.asyncSem <- struct{}{}:
		return true
	default:
		return false
	}
}

// releaseAsync releases a slot after an async operation completes.
func (s *LocalService) releaseAsync() {
	<-s.asyncSem
}

// SetOperator sets the operator on the service after construction. This is
// needed because the operator's callbacks reference the service, creating a
// circular dependency that prevents passing the operator at construction time.
func (s *LocalService) SetOperator(op *operator.Operator) {
	s.cfg.Operator = op
}

// ---------------------------------------------------------------------------
// Sub-interface accessors
// ---------------------------------------------------------------------------

func (s *LocalService) Operator() OperatorService      { return s }
func (s *LocalService) Definitions() DefinitionService { return s }
func (s *LocalService) Jobs() JobService               { return &localJobService{s} }
func (s *LocalService) Sessions() SessionService       { return &localSessionService{s} }
func (s *LocalService) Events() EventService           { return s }
func (s *LocalService) System() SystemService          { return s }

// ---------------------------------------------------------------------------
// Event stream infrastructure
// ---------------------------------------------------------------------------

// subscribe registers a new subscriber and returns its channel. The channel is
// closed when ctx is cancelled. The background goroutines (progress poll,
// heartbeat) are started lazily on the first call.
func (s *LocalService) subscribe(ctx context.Context) <-chan Event {
	ch := make(chan Event, 256)

	s.mu.Lock()
	id := s.nextSubID
	s.nextSubID++
	s.subscribers[id] = ch
	s.mu.Unlock()

	// Start background goroutines on first subscription.
	s.startOnce.Do(func() {
		go s.progressPollLoop()
		go s.heartbeatLoop()
	})

	// Remove subscriber when either the subscriber's context or the service
	// context is cancelled, whichever comes first.
	go func() {
		select {
		case <-ctx.Done():
		case <-s.ctx.Done():
		}
		s.mu.Lock()
		delete(s.subscribers, id)
		close(ch) // close under the same lock to prevent use-after-close
		s.mu.Unlock()
	}()

	return ch
}

// broadcast sends an event to all subscribers. Non-blocking: drops events on
// overflow rather than blocking the caller.
func (s *LocalService) broadcast(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seqCounter++
	ev.Seq = s.seqCounter
	ev.Timestamp = time.Now()
	for _, ch := range s.subscribers {
		select {
		case ch <- ev:
		default:
			// Drop on overflow — slow consumer.
		}
	}
}

// progressPollLoop polls SQLite every 500ms and broadcasts EventTypeProgressUpdate.
func (s *LocalService) progressPollLoop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			state := s.buildProgressState()
			s.broadcast(Event{
				Type:    EventTypeProgressUpdate,
				Payload: ProgressUpdatePayload{State: state},
			})
		}
	}
}

// heartbeatLoop broadcasts EventTypeHeartbeat every 15 seconds.
func (s *LocalService) heartbeatLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.broadcast(Event{
				Type:    EventTypeHeartbeat,
				Payload: HeartbeatPayload{ServerTime: time.Now()},
			})
		}
	}
}

// buildProgressState assembles the current ProgressState from SQLite and the runtime.
func (s *LocalService) buildProgressState() ProgressState {
	if s.cfg.Store == nil {
		return ProgressState{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	var state ProgressState

	// Jobs.
	dbJobs, err := s.cfg.Store.ListJobs(ctx, db.JobFilter{})
	if err != nil {
		dbJobs = nil
	}
	for _, j := range dbJobs {
		state.Jobs = append(state.Jobs, dbJobToService(j))
	}

	// Tasks and progress per job.
	state.Tasks = make(map[string][]Task)
	state.Reports = make(map[string][]ProgressReport)
	for _, j := range dbJobs {
		if ctx.Err() != nil {
			break
		}
		dbTasks, err := s.cfg.Store.ListTasksForJob(ctx, j.ID)
		if err == nil {
			var tasks []Task
			for _, t := range dbTasks {
				tasks = append(tasks, dbTaskToService(t))
			}
			state.Tasks[j.ID] = tasks
		}
		dbProgress, err := s.cfg.Store.GetRecentProgress(ctx, j.ID, 5)
		if err == nil {
			var reports []ProgressReport
			for _, p := range dbProgress {
				reports = append(reports, dbProgressToService(p))
			}
			state.Reports[j.ID] = reports
		}
	}

	// Active sessions from DB.
	dbSessions, err := s.cfg.Store.GetActiveSessions(ctx)
	if err == nil {
		for _, sess := range dbSessions {
			state.ActiveSessions = append(state.ActiveSessions, dbAgentSessionToService(sess))
		}
	}

	// Live snapshots from runtime.
	if s.cfg.Runtime != nil {
		for _, snap := range s.cfg.Runtime.ActiveSessions() {
			state.LiveSnapshots = append(state.LiveSnapshots, runtimeSnapshotToService(snap))
		}
	}

	// Feed entries.
	dbFeed, err := s.cfg.Store.ListRecentFeedEntries(ctx, 50)
	if err == nil {
		for _, fe := range dbFeed {
			state.FeedEntries = append(state.FeedEntries, dbFeedEntryToService(fe))
		}
	}

	return state
}

// ---------------------------------------------------------------------------
// Exported broadcast methods (called by cmd/root.go operator callbacks)
// ---------------------------------------------------------------------------

// BroadcastOperatorText broadcasts an operator.text event. Called from the
// operator's OnText callback.
func (s *LocalService) BroadcastOperatorText(text, reasoning string) {
	s.turnMu.Lock()
	turnID := s.currentTurnID
	s.pendingResponse.WriteString(text)
	s.turnMu.Unlock()

	s.broadcast(Event{
		Type:   EventTypeOperatorText,
		TurnID: turnID,
		Payload: OperatorTextPayload{
			Text:      text,
			Reasoning: reasoning,
		},
	})
}

// BroadcastOperatorEvent broadcasts a service event derived from an operator
// event. Called from the operator's OnEvent callback.
func (s *LocalService) BroadcastOperatorEvent(ev operator.Event) {
	switch ev.Type {
	case operator.EventTaskStarted:
		payload, ok := ev.Payload.(operator.TaskStartedPayload)
		if !ok {
			return
		}
		s.broadcast(Event{
			Type: EventTypeTaskStarted,
			Payload: TaskStartedPayload{
				TaskID: payload.TaskID,
				JobID:  payload.JobID,
				TeamID: payload.TeamID,
				Title:  payload.Title,
			},
		})

	case operator.EventTaskCompleted:
		payload, ok := ev.Payload.(operator.TaskCompletedPayload)
		if !ok {
			return
		}
		s.broadcast(Event{
			Type: EventTypeTaskCompleted,
			Payload: TaskCompletedPayload{
				TaskID:          payload.TaskID,
				JobID:           payload.JobID,
				TeamID:          payload.TeamID,
				Summary:         payload.Summary,
				Recommendations: payload.Recommendations,
				HasNextTask:     payload.HasNextTask,
			},
		})

	case operator.EventTaskFailed:
		payload, ok := ev.Payload.(operator.TaskFailedPayload)
		if !ok {
			return
		}
		s.broadcast(Event{
			Type: EventTypeTaskFailed,
			Payload: TaskFailedPayload{
				TaskID: payload.TaskID,
				JobID:  payload.JobID,
				TeamID: payload.TeamID,
				Error:  payload.Error,
			},
		})

	case operator.EventBlockerReported:
		payload, ok := ev.Payload.(operator.BlockerReportedPayload)
		if !ok {
			return
		}
		s.broadcast(Event{
			Type: EventTypeBlockerReported,
			Payload: BlockerReportedPayload{
				TaskID:      payload.TaskID,
				TeamID:      payload.TeamID,
				AgentID:     payload.AgentID,
				Description: payload.Description,
			},
		})

	case operator.EventJobComplete:
		payload, ok := ev.Payload.(operator.JobCompletePayload)
		if !ok {
			return
		}
		s.broadcast(Event{
			Type: EventTypeJobCompleted,
			Payload: JobCompletedPayload{
				JobID:   payload.JobID,
				Title:   payload.Title,
				Summary: payload.Summary,
			},
		})
	}
}

// BroadcastOperatorDone broadcasts an operator.done event. Called from the
// operator's OnTurnDone callback.
func (s *LocalService) BroadcastOperatorDone(modelName string, tokensIn, tokensOut, reasoningTokens int) {
	s.turnMu.Lock()
	turnID := s.currentTurnID
	s.currentTurnID = ""
	responseText := s.pendingResponse.String()
	s.pendingResponse.Reset()
	s.turnMu.Unlock()

	if responseText != "" {
		s.appendHistory(ChatEntry{
			Message:    ChatMessage{Role: MessageRoleAssistant, Content: responseText},
			Timestamp:  time.Now(),
			ClaudeMeta: fmt.Sprintf("operator · %s", modelName),
		})
	}

	s.broadcast(Event{
		Type:   EventTypeOperatorDone,
		TurnID: turnID,
		Payload: OperatorDonePayload{
			ModelName:       modelName,
			TokensIn:        tokensIn,
			TokensOut:       tokensOut,
			ReasoningTokens: reasoningTokens,
		},
	})
}

// BroadcastDefinitionsReloaded broadcasts a definitions.reloaded event. Called
// from the loader's onChange callback.
func (s *LocalService) BroadcastDefinitionsReloaded() {
	s.broadcast(Event{
		Type:    EventTypeDefinitionsReloaded,
		Payload: nil,
	})
}

// ---------------------------------------------------------------------------
// OperatorService
// ---------------------------------------------------------------------------

// SendMessage sends a user message to the operator event loop and returns a
// turnID for correlating subsequent operator.text and operator.done events.
func (s *LocalService) SendMessage(ctx context.Context, message string) (string, error) {
	if s.cfg.Operator == nil {
		return "", fmt.Errorf("operator not configured")
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
		return "", fmt.Errorf("operator turn already in progress")
	}
	s.currentTurnID = turnID
	s.turnMu.Unlock()

	if err := s.cfg.Operator.Send(ctx, operator.Event{
		Type:    operator.EventUserMessage,
		Payload: operator.UserMessagePayload{Text: message},
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
func (s *LocalService) RespondToPrompt(ctx context.Context, requestID string, response string) error {
	if s.cfg.Operator == nil {
		return fmt.Errorf("operator not configured")
	}
	return s.cfg.Operator.Send(ctx, operator.Event{
		Type: operator.EventUserResponse,
		Payload: operator.UserResponsePayload{
			RequestID: requestID,
			Text:      response,
		},
	})
}

// Status returns the current state of the operator.
func (s *LocalService) Status(_ context.Context) (OperatorStatus, error) {
	s.turnMu.Lock()
	turnID := s.currentTurnID
	s.turnMu.Unlock()

	return OperatorStatus{
		State:         OperatorStateIdle, // deferred: real state tracking requires operator changes
		CurrentTurnID: turnID,
		ModelName:     s.cfg.OperatorModel,
	}, nil
}

// appendHistory appends a ChatEntry to the conversation history, capping at
// maxHistoryEntries by dropping the oldest entries when the limit is exceeded.
func (s *LocalService) appendHistory(entry ChatEntry) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	s.history = append(s.history, entry)
	if len(s.history) > maxHistoryEntries {
		// Drop oldest entries to stay within the limit.
		excess := len(s.history) - maxHistoryEntries
		copy(s.history, s.history[excess:])
		s.history = s.history[:maxHistoryEntries]
	}
}

// History returns the conversation history for the current session.
func (s *LocalService) History(_ context.Context) ([]ChatEntry, error) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	result := make([]ChatEntry, len(s.history))
	copy(result, s.history)
	return result, nil
}

// RespondToBlocker submits the user's answers to a blocker reported by an agent.
func (s *LocalService) RespondToBlocker(ctx context.Context, jobID, taskID string, answers []string) error {
	if s.cfg.Operator == nil {
		return fmt.Errorf("operator not configured")
	}

	// Format the answers into a structured message for the operator.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Blocker response for job %s, task %s:\n", jobID, taskID))
	for i, answer := range answers {
		sb.WriteString(fmt.Sprintf("Answer %d: %s\n", i+1, answer))
	}

	return s.cfg.Operator.Send(ctx, operator.Event{
		Type: operator.EventUserResponse,
		Payload: operator.UserResponsePayload{
			Text: sb.String(),
		},
	})
}

// ---------------------------------------------------------------------------
// DefinitionService — Skills
// ---------------------------------------------------------------------------

// ListSkills returns all skills from the store, ordered by source then name.
func (s *LocalService) ListSkills(ctx context.Context) ([]Skill, error) {
	if s.cfg.Store == nil {
		return nil, fmt.Errorf("store not configured")
	}
	dbSkills, err := s.cfg.Store.ListSkills(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing skills: %w", err)
	}
	skills := make([]Skill, 0, len(dbSkills))
	for _, sk := range dbSkills {
		skills = append(skills, dbSkillToService(sk))
	}
	return skills, nil
}

// GetSkill returns a single skill by ID.
func (s *LocalService) GetSkill(ctx context.Context, id string) (Skill, error) {
	if s.cfg.Store == nil {
		return Skill{}, fmt.Errorf("store not configured")
	}
	sk, err := s.cfg.Store.GetSkill(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Skill{}, fmt.Errorf("getting skill %s: %w", id, ErrNotFound)
		}
		return Skill{}, fmt.Errorf("getting skill %s: %w", id, err)
	}
	return dbSkillToService(sk), nil
}

// CreateSkill writes a template .md file to the user skills directory and
// triggers a definition reload. Returns the created skill.
func (s *LocalService) CreateSkill(ctx context.Context, name string) (Skill, error) {
	name = sanitizeName(name)
	skillsDir := filepath.Join(s.cfg.ConfigDir, "user", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return Skill{}, sanitizeError(fmt.Errorf("creating skills dir: %w", err))
	}

	filename := loader.Slugify(name) + ".md"
	if filename == ".md" {
		return Skill{}, fmt.Errorf("invalid skill name: produces empty filename")
	}
	path := filepath.Join(skillsDir, filename)

	if _, err := os.Stat(path); err == nil {
		return Skill{}, fmt.Errorf("skill file %q already exists", filename)
	}

	template := fmt.Sprintf(`---
name: %s
description: A brief description of what this skill does
tools: []
---

Your skill prompt goes here. This text will be injected into agents that use this skill.
`, name)

	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		return Skill{}, sanitizeError(fmt.Errorf("writing skill file: %w", err))
	}

	if s.cfg.Loader != nil {
		if err := s.cfg.Loader.Load(ctx); err != nil {
			slog.Warn("failed to reload definitions after skill creation", "error", err)
		}
	}

	// Find the created skill by name.
	skills, err := s.ListSkills(ctx)
	if err != nil {
		return Skill{}, sanitizeError(fmt.Errorf("listing skills after creation: %w", err))
	}
	for _, sk := range skills {
		if sk.Name == name {
			return sk, nil
		}
	}
	return Skill{}, fmt.Errorf("skill %q not found after creation", name)
}

// DeleteSkill removes the skill's source file and triggers a reload.
func (s *LocalService) DeleteSkill(ctx context.Context, id string) error {
	sk, err := s.GetSkill(ctx, id)
	if err != nil {
		return err
	}
	if sk.Source == "system" {
		return fmt.Errorf("cannot delete system skill %q", sk.Name)
	}
	if sk.SourcePath == "" {
		return fmt.Errorf("skill %q has no source path", sk.Name)
	}
	allowedDir := filepath.Join(s.cfg.ConfigDir, "user")
	realSkillPath, err := filepath.EvalSymlinks(sk.SourcePath)
	if err != nil {
		return sanitizeError(fmt.Errorf("resolving skill path: %w", err))
	}
	realAllowedDir, err := filepath.EvalSymlinks(allowedDir)
	if err != nil {
		return sanitizeError(fmt.Errorf("resolving allowed dir: %w", err))
	}
	if !strings.HasPrefix(realSkillPath+string(filepath.Separator), realAllowedDir+string(filepath.Separator)) {
		return sanitizeError(fmt.Errorf("skill source path is outside user directory"))
	}
	if err := os.Remove(realSkillPath); err != nil {
		return sanitizeError(fmt.Errorf("removing skill file: %w", err))
	}
	if s.cfg.Loader != nil {
		if err := s.cfg.Loader.Load(ctx); err != nil {
			slog.Warn("failed to reload definitions after skill deletion", "error", err)
		}
	}
	return nil
}

// GenerateSkill asks the LLM to generate a skill definition. Returns an
// operationID immediately; pushes operation.completed or operation.failed when done.
func (s *LocalService) GenerateSkill(ctx context.Context, prompt string) (string, error) {
	if s.cfg.Provider == nil {
		return "", fmt.Errorf("LLM provider not configured")
	}
	if len(prompt) > maxPromptLen {
		return "", fmt.Errorf("prompt too large: %d bytes exceeds maximum %d", len(prompt), maxPromptLen)
	}

	uuidVal, err := uuid.NewV4()
	if err != nil {
		return "", fmt.Errorf("generating operation ID: %w", err)
	}
	operationID := uuidVal.String()

	if !s.tryAcquireAsync() {
		return "", fmt.Errorf("too many concurrent operations (max %d)", maxConcurrentOps)
	}

	go func() {
		defer s.releaseAsync()

		genCtx, genCancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer genCancel()
		content, genErr := s.generateSkillContent(genCtx, prompt)
		if s.ctx.Err() != nil {
			return // service shutting down
		}
		if genErr != nil {
			s.broadcast(Event{
				Type:        EventTypeOperationFailed,
				OperationID: operationID,
				Payload: OperationFailedPayload{
					Kind:  "generate_skill",
					Error: sanitizeErrorString(genErr),
				},
			})
			return
		}

		path, writeErr := s.writeGeneratedSkillFile(content)
		if s.ctx.Err() != nil {
			return // service shutting down
		}
		if writeErr != nil {
			s.broadcast(Event{
				Type:        EventTypeOperationFailed,
				OperationID: operationID,
				Payload: OperationFailedPayload{
					Kind:  "generate_skill",
					Error: sanitizeErrorString(writeErr),
				},
			})
			return
		}

		if s.cfg.Loader != nil {
			if err := s.cfg.Loader.Load(s.ctx); err != nil {
				slog.Warn("failed to reload definitions after skill generation", "error", err)
			}
		}

		if s.ctx.Err() != nil {
			return // service shutting down
		}
		slog.Info("generated skill file", "path", path)
		s.broadcast(Event{
			Type:        EventTypeOperationCompleted,
			OperationID: operationID,
			Payload: OperationCompletedPayload{
				Kind: "generate_skill",
				Result: OperationResult{
					OperationID: operationID,
					Content:     content,
				},
			},
		})
	}()

	return operationID, nil
}

// generateSkillContent calls the LLM to generate a skill definition.
func (s *LocalService) generateSkillContent(ctx context.Context, prompt string) (string, error) {
	systemPrompt := `You are generating a Toasters skill definition file. Output ONLY the raw .md file content with no explanation, preamble, or code fences.

A skill file has this format:
---
name: skill-name
description: Brief description of what this skill provides
tools:
  - tool_name_1
  - tool_name_2
---

# Skill Name

Detailed instructions for the agent using this skill. This is the system prompt content that will be injected when this skill is active.

## Guidelines
- ...`

	userMsg := fmt.Sprintf("The user wants a skill for: %s\n\nOutput ONLY the .md file content starting with ---.", prompt)

	msgs := []provider.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMsg},
	}

	content, err := provider.ChatCompletion(ctx, s.cfg.Provider, msgs)
	if err != nil {
		return "", fmt.Errorf("LLM call failed: %w", err)
	}

	content = stripCodeFences(content)

	if _, err := agentfmt.ParseBytes([]byte(content), agentfmt.DefSkill); err != nil {
		return "", fmt.Errorf("generated content is not a valid skill definition: %w", err)
	}

	return content, nil
}

// ---------------------------------------------------------------------------
// DefinitionService — Agents
// ---------------------------------------------------------------------------

// ListAgents returns all agents ordered: shared → team-local → system.
func (s *LocalService) ListAgents(ctx context.Context) ([]Agent, error) {
	if s.cfg.Store == nil {
		return nil, fmt.Errorf("store not configured")
	}
	dbAgents, err := s.cfg.Store.ListAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}

	var shared, teamLocal, system []*db.Agent
	for _, a := range dbAgents {
		switch {
		case a.Source == "system":
			system = append(system, a)
		case a.TeamID != "":
			teamLocal = append(teamLocal, a)
		default:
			shared = append(shared, a)
		}
	}

	// Sort team-local by "team/agent" composite key.
	sortAgentsByTeamKey(teamLocal)

	ordered := append(append(shared, teamLocal...), system...)
	agents := make([]Agent, 0, len(ordered))
	for _, a := range ordered {
		agents = append(agents, dbAgentToService(a))
	}
	return agents, nil
}

// GetAgent returns a single agent by ID.
func (s *LocalService) GetAgent(ctx context.Context, id string) (Agent, error) {
	if s.cfg.Store == nil {
		return Agent{}, fmt.Errorf("store not configured")
	}
	a, err := s.cfg.Store.GetAgent(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Agent{}, fmt.Errorf("getting agent %s: %w", id, ErrNotFound)
		}
		return Agent{}, fmt.Errorf("getting agent %s: %w", id, err)
	}
	return dbAgentToService(a), nil
}

// CreateAgent writes a template .md file to the user agents directory and
// triggers a reload. Returns the created agent.
func (s *LocalService) CreateAgent(ctx context.Context, name string) (Agent, error) {
	name = sanitizeName(name)
	agentsDir := filepath.Join(s.cfg.ConfigDir, "user", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return Agent{}, sanitizeError(fmt.Errorf("creating agents dir: %w", err))
	}

	filename := loader.Slugify(name) + ".md"
	if filename == ".md" {
		return Agent{}, fmt.Errorf("invalid agent name: produces empty filename")
	}
	path := filepath.Join(agentsDir, filename)

	if _, err := os.Stat(path); err == nil {
		return Agent{}, fmt.Errorf("agent file %q already exists", filename)
	}

	template := fmt.Sprintf(`---
name: %s
description: A brief description of what this agent does
mode: worker
skills: []
---

Your agent system prompt goes here.
`, name)

	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		return Agent{}, sanitizeError(fmt.Errorf("writing agent file: %w", err))
	}

	if s.cfg.Loader != nil {
		if err := s.cfg.Loader.Load(ctx); err != nil {
			slog.Warn("failed to reload definitions after agent creation", "error", err)
		}
	}

	// Find the created agent by name.
	agents, err := s.ListAgents(ctx)
	if err != nil {
		return Agent{}, sanitizeError(fmt.Errorf("listing agents after creation: %w", err))
	}
	for _, a := range agents {
		if a.Name == name {
			return a, nil
		}
	}
	return Agent{}, fmt.Errorf("agent %q not found after creation", name)
}

// DeleteAgent removes the agent's source file and triggers a reload.
// Only user-owned shared agents (Source == "user", TeamID == "") can be deleted.
func (s *LocalService) DeleteAgent(ctx context.Context, id string) error {
	a, err := s.GetAgent(ctx, id)
	if err != nil {
		return err
	}
	if a.Source != "user" || a.TeamID != "" {
		return fmt.Errorf("cannot delete agent %q: only user-owned shared agents can be deleted", a.Name)
	}
	if a.SourcePath == "" {
		return fmt.Errorf("agent %q has no source path", a.Name)
	}
	allowedDir := filepath.Join(s.cfg.ConfigDir, "user")
	realAgentPath, err := filepath.EvalSymlinks(a.SourcePath)
	if err != nil {
		return sanitizeError(fmt.Errorf("resolving agent path: %w", err))
	}
	realAllowedDir, err := filepath.EvalSymlinks(allowedDir)
	if err != nil {
		return sanitizeError(fmt.Errorf("resolving allowed dir: %w", err))
	}
	if !strings.HasPrefix(realAgentPath+string(filepath.Separator), realAllowedDir+string(filepath.Separator)) {
		return sanitizeError(fmt.Errorf("agent source path is outside user directory"))
	}
	if err := os.Remove(realAgentPath); err != nil {
		return sanitizeError(fmt.Errorf("removing agent file: %w", err))
	}
	if s.cfg.Loader != nil {
		if err := s.cfg.Loader.Load(ctx); err != nil {
			slog.Warn("failed to reload definitions after agent deletion", "error", err)
		}
	}
	return nil
}

// AddSkillToAgent appends the named skill to the agent's .md file.
func (s *LocalService) AddSkillToAgent(ctx context.Context, agentID string, skillName string) error {
	a, err := s.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if a.SourcePath == "" {
		return fmt.Errorf("cannot add skill: agent source file unknown")
	}
	if a.Source == "system" {
		return fmt.Errorf("cannot add skill to system agent %q", a.Name)
	}

	realSrc, err := filepath.EvalSymlinks(a.SourcePath)
	if err != nil {
		return sanitizeError(fmt.Errorf("resolving agent path: %w", err))
	}
	realAllowed, err := filepath.EvalSymlinks(s.cfg.ConfigDir)
	if err != nil {
		return sanitizeError(fmt.Errorf("resolving config dir: %w", err))
	}
	if !strings.HasPrefix(realSrc+string(filepath.Separator), realAllowed+string(filepath.Separator)) {
		return sanitizeError(fmt.Errorf("agent source path is outside config directory"))
	}

	def, err := agentfmt.ParseAgent(realSrc)
	if err != nil {
		return sanitizeError(fmt.Errorf("parsing agent file: %w", err))
	}

	def.Skills = append(def.Skills, skillName)

	if err := writeAgentFile(realSrc, def); err != nil {
		return sanitizeError(fmt.Errorf("writing agent file: %w", err))
	}

	if s.cfg.Loader != nil {
		if err := s.cfg.Loader.Load(ctx); err != nil {
			slog.Warn("failed to reload definitions after adding skill to agent", "error", err)
		}
	}
	return nil
}

// GenerateAgent asks the LLM to generate an agent definition. Returns an
// operationID immediately; pushes operation.completed or operation.failed when done.
func (s *LocalService) GenerateAgent(ctx context.Context, prompt string) (string, error) {
	if s.cfg.Provider == nil {
		return "", fmt.Errorf("LLM provider not configured")
	}
	if len(prompt) > maxPromptLen {
		return "", fmt.Errorf("prompt too large: %d bytes exceeds maximum %d", len(prompt), maxPromptLen)
	}

	uuidVal, err := uuid.NewV4()
	if err != nil {
		return "", fmt.Errorf("generating operation ID: %w", err)
	}
	operationID := uuidVal.String()

	if !s.tryAcquireAsync() {
		return "", fmt.Errorf("too many concurrent operations (max %d)", maxConcurrentOps)
	}

	go func() {
		defer s.releaseAsync()

		genCtx, genCancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer genCancel()
		content, genErr := s.generateAgentContent(genCtx, prompt)
		if s.ctx.Err() != nil {
			return // service shutting down
		}
		if genErr != nil {
			s.broadcast(Event{
				Type:        EventTypeOperationFailed,
				OperationID: operationID,
				Payload: OperationFailedPayload{
					Kind:  "generate_agent",
					Error: sanitizeErrorString(genErr),
				},
			})
			return
		}

		_, _, writeErr := s.writeGeneratedAgentFile(content)
		if s.ctx.Err() != nil {
			return // service shutting down
		}
		if writeErr != nil {
			s.broadcast(Event{
				Type:        EventTypeOperationFailed,
				OperationID: operationID,
				Payload: OperationFailedPayload{
					Kind:  "generate_agent",
					Error: sanitizeErrorString(writeErr),
				},
			})
			return
		}

		if s.cfg.Loader != nil {
			if err := s.cfg.Loader.Load(s.ctx); err != nil {
				slog.Warn("failed to reload definitions after agent generation", "error", err)
			}
		}

		if s.ctx.Err() != nil {
			return // service shutting down
		}
		s.broadcast(Event{
			Type:        EventTypeOperationCompleted,
			OperationID: operationID,
			Payload: OperationCompletedPayload{
				Kind: "generate_agent",
				Result: OperationResult{
					OperationID: operationID,
					Content:     content,
				},
			},
		})
	}()

	return operationID, nil
}

// generateAgentContent calls the LLM to generate an agent definition.
func (s *LocalService) generateAgentContent(ctx context.Context, prompt string) (string, error) {
	systemPrompt := `You are generating a Toasters agent definition file. Output ONLY the raw .md file content with no explanation, preamble, or code fences.

A Toasters agent file has this format:
---
name: agent-name
description: What this agent does
mode: worker
model: claude-sonnet-4-5
skills:
  - skill-name
tools:
  - Read
  - Write
  - Bash
---

# Agent Name

Detailed system prompt for this agent. Describe its persona, responsibilities, and how it should behave.`

	userMsg := fmt.Sprintf("The user wants an agent for: %s\n\nOutput ONLY the .md file content starting with ---.", prompt)

	msgs := []provider.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMsg},
	}

	content, err := provider.ChatCompletion(ctx, s.cfg.Provider, msgs)
	if err != nil {
		return "", fmt.Errorf("LLM call failed: %w", err)
	}

	content = stripCodeFences(content)

	if _, err := agentfmt.ParseBytes([]byte(content), agentfmt.DefAgent); err != nil {
		return "", fmt.Errorf("generated content is not a valid agent definition: %w", err)
	}

	return content, nil
}

// ---------------------------------------------------------------------------
// DefinitionService — Teams
// ---------------------------------------------------------------------------

// ListTeams returns all non-system teams as TeamView values.
func (s *LocalService) ListTeams(ctx context.Context) ([]TeamView, error) {
	if s.cfg.Store == nil {
		return nil, fmt.Errorf("store not configured")
	}
	return s.buildTeamViews(ctx)
}

// GetTeam returns a single team as a TeamView by team ID.
func (s *LocalService) GetTeam(ctx context.Context, id string) (TeamView, error) {
	if s.cfg.Store == nil {
		return TeamView{}, fmt.Errorf("store not configured")
	}
	views, err := s.buildTeamViews(ctx)
	if err != nil {
		return TeamView{}, err
	}
	for _, tv := range views {
		if tv.Team.ID == id {
			return tv, nil
		}
	}
	return TeamView{}, fmt.Errorf("team %s: %w", id, ErrNotFound)
}

// CreateTeam creates a new team directory and triggers a reload.
func (s *LocalService) CreateTeam(ctx context.Context, name string) (TeamView, error) {
	name = sanitizeName(name)
	slug := loader.Slugify(name)
	if slug == "" {
		return TeamView{}, fmt.Errorf("invalid team name %q: produces empty slug", name)
	}
	if err := os.MkdirAll(s.cfg.TeamsDir, 0o755); err != nil {
		return TeamView{}, sanitizeError(fmt.Errorf("creating teams dir: %w", err))
	}
	realTeamsDir, err := filepath.EvalSymlinks(s.cfg.TeamsDir)
	if err != nil {
		return TeamView{}, sanitizeError(fmt.Errorf("resolving teams dir: %w", err))
	}
	teamDir := filepath.Join(realTeamsDir, slug)
	if !strings.HasPrefix(teamDir+string(filepath.Separator), realTeamsDir+string(filepath.Separator)) {
		return TeamView{}, fmt.Errorf("team name %q escapes teams directory", name)
	}
	if err := os.MkdirAll(teamDir, 0o755); err != nil {
		return TeamView{}, sanitizeError(fmt.Errorf("creating team directory: %w", err))
	}

	teamMDContent := fmt.Sprintf("---\nname: %s\ndescription: \nlead: \n---\n", name)
	if err := os.WriteFile(filepath.Join(teamDir, "team.md"), []byte(teamMDContent), 0o644); err != nil {
		return TeamView{}, sanitizeError(fmt.Errorf("writing team.md: %w", err))
	}

	if err := os.MkdirAll(filepath.Join(teamDir, "agents"), 0o755); err != nil {
		return TeamView{}, sanitizeError(fmt.Errorf("creating agents directory: %w", err))
	}

	if s.cfg.Loader != nil {
		if err := s.cfg.Loader.Load(ctx); err != nil {
			slog.Warn("failed to reload definitions after team creation", "error", err)
		}
	}

	views, err := s.buildTeamViews(ctx)
	if err != nil {
		return TeamView{}, sanitizeError(err)
	}
	for _, tv := range views {
		if tv.Team.Name == name {
			return tv, nil
		}
	}
	return TeamView{}, fmt.Errorf("team %q not found after creation", name)
}

// DeleteTeam deletes the team directory. Writes a dismiss marker for auto-teams.
func (s *LocalService) DeleteTeam(ctx context.Context, id string) error {
	tv, err := s.GetTeam(ctx, id)
	if err != nil {
		return err
	}

	if isServiceReadOnlyTeam(tv) {
		return fmt.Errorf("cannot delete read-only team %q", tv.Team.Name)
	}
	if isServiceSystemTeam(tv, s.cfg.ConfigDir) {
		return fmt.Errorf("cannot delete system team %q", tv.Team.Name)
	}

	// Write dismiss marker for auto-teams so bootstrap doesn't re-create them.
	if isServiceAutoTeam(tv) {
		dismissedDir := filepath.Join(s.cfg.TeamsDir, ".dismissed")
		if err := os.MkdirAll(dismissedDir, 0o755); err != nil {
			slog.Warn("failed to create dismiss marker directory", "error", err)
		} else if err := os.WriteFile(filepath.Join(dismissedDir, filepath.Base(tv.Dir())), nil, 0o644); err != nil {
			slog.Warn("failed to write dismiss marker", "team", tv.Team.Name, "error", err)
		}
	}

	// Validate that team dir is under the expected teams directory before deletion.
	realTeamDir, err1 := filepath.EvalSymlinks(tv.Dir())
	realTeamsDir, err2 := filepath.EvalSymlinks(s.cfg.TeamsDir)
	if err1 == nil && err2 == nil && strings.HasPrefix(realTeamDir, realTeamsDir+string(filepath.Separator)) {
		if err := os.RemoveAll(realTeamDir); err != nil {
			return sanitizeError(fmt.Errorf("removing team directory: %w", err))
		}
	} else {
		slog.Error("refusing to delete team outside teams directory", "dir", tv.Dir(), "teamsDir", s.cfg.TeamsDir)
		return fmt.Errorf("team directory is outside the teams directory")
	}

	if s.cfg.Loader != nil {
		if err := s.cfg.Loader.Load(ctx); err != nil {
			slog.Warn("failed to reload definitions after team deletion", "error", err)
		}
	}
	return nil
}

// AddAgentToTeam adds the given agent to the team.
func (s *LocalService) AddAgentToTeam(ctx context.Context, teamID string, agentID string) error {
	tv, err := s.GetTeam(ctx, teamID)
	if err != nil {
		return err
	}
	if isServiceReadOnlyTeam(tv) {
		return fmt.Errorf("cannot add agent to read-only team %q", tv.Team.Name)
	}

	a, err := s.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if a.SourcePath == "" {
		return fmt.Errorf("cannot add agent: source file unknown")
	}

	realSrc, err := filepath.EvalSymlinks(a.SourcePath)
	if err != nil {
		return sanitizeError(fmt.Errorf("resolving agent path: %w", err))
	}
	realAllowed, err := filepath.EvalSymlinks(s.cfg.ConfigDir)
	if err != nil {
		return sanitizeError(fmt.Errorf("resolving config dir: %w", err))
	}
	if !strings.HasPrefix(realSrc+string(filepath.Separator), realAllowed+string(filepath.Separator)) {
		return sanitizeError(fmt.Errorf("agent source path is outside config directory"))
	}

	teamMDPath := filepath.Join(tv.Dir(), "team.md")

	// Parse the existing team.md (or create a minimal one if absent).
	teamDef, err := agentfmt.ParseTeam(teamMDPath)
	if err != nil {
		teamDef = &agentfmt.TeamDef{Name: tv.Team.Name}
	}

	// Append the agent name if not already present.
	alreadyListed := false
	for _, n := range teamDef.Agents {
		if n == a.Name {
			alreadyListed = true
			break
		}
	}
	if !alreadyListed {
		teamDef.Agents = append(teamDef.Agents, a.Name)
	}

	if err := writeTeamFile(teamMDPath, teamDef); err != nil {
		return sanitizeError(fmt.Errorf("writing team.md: %w", err))
	}

	// Copy the agent's source file into the team's agents directory.
	agentsDir := filepath.Join(tv.Dir(), "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return sanitizeError(fmt.Errorf("creating agents directory: %w", err))
	}

	slug := loader.Slugify(a.Name)
	if slug == "" {
		slug = loader.Slugify(a.ID)
	}
	destPath := filepath.Join(agentsDir, slug+".md")

	if err := copyFile(realSrc, destPath); err != nil {
		return sanitizeError(fmt.Errorf("copying agent file: %w", err))
	}

	if s.cfg.Loader != nil {
		if err := s.cfg.Loader.Load(ctx); err != nil {
			slog.Warn("failed to reload definitions after adding agent to team", "error", err)
		}
	}
	return nil
}

// SetCoordinator updates the team so that the named agent is the coordinator.
func (s *LocalService) SetCoordinator(ctx context.Context, teamID string, agentName string) error {
	tv, err := s.GetTeam(ctx, teamID)
	if err != nil {
		return err
	}
	if isServiceReadOnlyTeam(tv) {
		return fmt.Errorf("cannot set coordinator on read-only team %q", tv.Team.Name)
	}

	if err := setCoordinator(tv.Dir(), agentName); err != nil {
		return sanitizeError(err)
	}

	if s.cfg.Loader != nil {
		if err := s.cfg.Loader.Load(ctx); err != nil {
			slog.Warn("failed to reload definitions after setting coordinator", "error", err)
		}
	}
	return nil
}

// PromoteTeam promotes an auto-detected team to a fully managed team.
// Returns an operationID immediately; pushes operation.completed or operation.failed when done.
func (s *LocalService) PromoteTeam(ctx context.Context, teamID string) (string, error) {
	tv, err := s.GetTeam(ctx, teamID)
	if err != nil {
		return "", err
	}
	if !isServiceAutoTeam(tv) {
		return "", fmt.Errorf("team %q is not an auto-team", tv.Team.Name)
	}
	if isServiceSystemTeam(tv, s.cfg.ConfigDir) {
		return "", fmt.Errorf("cannot promote system team %q", tv.Team.Name)
	}

	uuidVal, err := uuid.NewV4()
	if err != nil {
		return "", fmt.Errorf("generating operation ID: %w", err)
	}
	operationID := uuidVal.String()

	if !s.tryAcquireAsync() {
		return "", fmt.Errorf("too many concurrent operations (max %d)", maxConcurrentOps)
	}

	go func() {
		defer s.releaseAsync()

		var promoteErr error
		if isServiceReadOnlyTeam(tv) {
			promoteErr = s.promoteReadOnlyAutoTeam(tv)
		} else {
			promoteErr = promoteMarkerAutoTeam(tv)
		}

		if s.ctx.Err() != nil {
			return // service shutting down
		}

		if promoteErr != nil {
			s.broadcast(Event{
				Type:        EventTypeOperationFailed,
				OperationID: operationID,
				Payload: OperationFailedPayload{
					Kind:  "promote_team",
					Error: sanitizeErrorString(promoteErr),
				},
			})
			return
		}

		if s.cfg.Loader != nil {
			if err := s.cfg.Loader.Load(s.ctx); err != nil {
				slog.Warn("failed to reload definitions after team promotion", "error", err)
			}
		}

		if s.ctx.Err() != nil {
			return // service shutting down
		}
		s.broadcast(Event{
			Type:        EventTypeOperationCompleted,
			OperationID: operationID,
			Payload: OperationCompletedPayload{
				Kind: "promote_team",
				Result: OperationResult{
					OperationID: operationID,
					Content:     tv.Team.Name,
				},
			},
		})
	}()

	return operationID, nil
}

// GenerateTeam asks the LLM to generate a team definition. Returns an
// operationID immediately; pushes operation.completed or operation.failed when done.
func (s *LocalService) GenerateTeam(ctx context.Context, prompt string) (string, error) {
	if s.cfg.Provider == nil {
		return "", fmt.Errorf("LLM provider not configured")
	}
	if len(prompt) > maxPromptLen {
		return "", fmt.Errorf("prompt too large: %d bytes exceeds maximum %d", len(prompt), maxPromptLen)
	}
	if s.cfg.Store == nil {
		return "", fmt.Errorf("store not configured")
	}

	// Capture available agents for the goroutine.
	listCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	dbAgents, err := s.cfg.Store.ListAgents(listCtx)
	cancel()
	if err != nil {
		return "", fmt.Errorf("listing agents for team generation: %w", err)
	}

	uuidVal, err := uuid.NewV4()
	if err != nil {
		return "", fmt.Errorf("generating operation ID: %w", err)
	}
	operationID := uuidVal.String()

	agentsCopy := make([]*db.Agent, len(dbAgents))
	copy(agentsCopy, dbAgents)

	if !s.tryAcquireAsync() {
		return "", fmt.Errorf("too many concurrent operations (max %d)", maxConcurrentOps)
	}

	go func() {
		defer s.releaseAsync()

		genCtx, genCancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer genCancel()
		teamMD, agentNames, genErr := s.generateTeamContent(genCtx, prompt, agentsCopy)
		if s.ctx.Err() != nil {
			return // service shutting down
		}
		if genErr != nil {
			s.broadcast(Event{
				Type:        EventTypeOperationFailed,
				OperationID: operationID,
				Payload: OperationFailedPayload{
					Kind:  "generate_team",
					Error: sanitizeErrorString(genErr),
				},
			})
			return
		}

		writeErr := s.writeGeneratedTeamFiles(teamMD, agentNames, agentsCopy)
		if s.ctx.Err() != nil {
			return // service shutting down
		}
		if writeErr != nil {
			s.broadcast(Event{
				Type:        EventTypeOperationFailed,
				OperationID: operationID,
				Payload: OperationFailedPayload{
					Kind:  "generate_team",
					Error: sanitizeErrorString(writeErr),
				},
			})
			return
		}

		if s.cfg.Loader != nil {
			if err := s.cfg.Loader.Load(s.ctx); err != nil {
				slog.Warn("failed to reload definitions after team generation", "error", err)
			}
		}

		if s.ctx.Err() != nil {
			return // service shutting down
		}
		s.broadcast(Event{
			Type:        EventTypeOperationCompleted,
			OperationID: operationID,
			Payload: OperationCompletedPayload{
				Kind: "generate_team",
				Result: OperationResult{
					OperationID: operationID,
					Content:     teamMD,
					AgentNames:  agentNames,
				},
			},
		})
	}()

	return operationID, nil
}

// generateTeamContent calls the LLM to generate a team definition.
func (s *LocalService) generateTeamContent(ctx context.Context, prompt string, agents []*db.Agent) (teamMD string, agentNames []string, err error) {
	var agentList strings.Builder
	for _, a := range agents {
		desc := a.Description
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(&agentList, "- %s: %s\n", a.Name, desc)
	}

	systemPrompt := fmt.Sprintf(`You are generating a Toasters team definition. Output ONLY a JSON object with no explanation, preamble, or code fences.

A team.md file has this format:
---
name: team-name
description: What this team does
coordinator: lead-agent-name
---

# Team Name

Team culture and working norms. How agents on this team should collaborate.

Available agents that can be assigned to this team:
%s
Output ONLY a JSON object in this exact format:
{"team_md": "<the full team.md content>", "agent_names": ["agent1", "agent2"]}

The agent_names must be names from the available agents list above. Choose 2-5 agents that best fit the team's purpose.`, agentList.String())

	userMsg := fmt.Sprintf("The user wants a team for: %s", prompt)

	msgs := []provider.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMsg},
	}

	raw, err := provider.ChatCompletion(ctx, s.cfg.Provider, msgs)
	if err != nil {
		return "", nil, fmt.Errorf("LLM call failed: %w", err)
	}

	raw = stripCodeFences(raw)

	var result struct {
		TeamMD     string   `json:"team_md"`
		AgentNames []string `json:"agent_names"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return "", nil, fmt.Errorf("parsing LLM JSON response: %w", err)
	}

	if result.TeamMD == "" {
		return "", nil, fmt.Errorf("LLM returned empty team_md")
	}

	if _, err := agentfmt.ParseBytes([]byte(result.TeamMD), agentfmt.DefTeam); err != nil {
		return "", nil, fmt.Errorf("generated team_md is not a valid team definition: %w", err)
	}

	return result.TeamMD, result.AgentNames, nil
}

// writeGeneratedTeamFiles writes the team directory, team.md, and copies agent files.
func (s *LocalService) writeGeneratedTeamFiles(teamMD string, agentNames []string, allAgents []*db.Agent) error {
	parsed, err := agentfmt.ParseBytes([]byte(teamMD), agentfmt.DefTeam)
	if err != nil {
		return fmt.Errorf("parsing generated team.md: %w", err)
	}
	teamDef, ok := parsed.(*agentfmt.TeamDef)
	if !ok || teamDef.Name == "" {
		return fmt.Errorf("generated team.md missing team name")
	}

	slug := loader.Slugify(teamDef.Name)
	teamDir := filepath.Join(s.cfg.TeamsDir, slug)
	agentsSubDir := filepath.Join(teamDir, "agents")

	// Path traversal check: ensure the team directory stays within TeamsDir.
	realTeamsDir, err := filepath.EvalSymlinks(s.cfg.TeamsDir)
	if err != nil {
		return fmt.Errorf("resolving teams dir: %w", err)
	}
	// teamDir doesn't exist yet, so EvalSymlinks on the parent.
	realTeamDirParent, err := filepath.EvalSymlinks(filepath.Dir(teamDir))
	if err != nil {
		return fmt.Errorf("resolving team dir parent: %w", err)
	}
	candidateDir := filepath.Join(realTeamDirParent, filepath.Base(teamDir))
	if !strings.HasPrefix(candidateDir+string(filepath.Separator), realTeamsDir+string(filepath.Separator)) {
		return fmt.Errorf("team name escapes teams directory")
	}

	if _, err := os.Stat(teamDir); err == nil {
		return fmt.Errorf("team directory already exists: %s", slug)
	}

	if err := os.MkdirAll(agentsSubDir, 0o755); err != nil {
		return fmt.Errorf("creating team directory: %w", err)
	}

	if err := os.WriteFile(filepath.Join(teamDir, "team.md"), []byte(teamMD), 0o644); err != nil {
		_ = os.RemoveAll(teamDir)
		return fmt.Errorf("writing team.md: %w", err)
	}

	// Build name→agent map for fast lookup.
	agentByName := make(map[string]*db.Agent, len(allAgents))
	for _, a := range allAgents {
		agentByName[a.Name] = a
	}

	for _, name := range agentNames {
		a, found := agentByName[name]
		if !found {
			slog.Warn("generated team references unknown agent, skipping", "agent", name)
			continue
		}
		if a.SourcePath == "" {
			slog.Warn("generated team agent has no source path, skipping", "agent", name)
			continue
		}
		agentSlug := loader.Slugify(a.Name)
		if agentSlug == "" {
			agentSlug = loader.Slugify(a.ID)
		}
		destPath := filepath.Join(agentsSubDir, agentSlug+".md")
		if err := copyFile(a.SourcePath, destPath); err != nil {
			slog.Warn("failed to copy agent file for generated team", "agent", name, "error", err)
		}
	}

	return nil
}

// DetectCoordinator asks the LLM to pick the best coordinator for the team.
// Returns an operationID immediately; pushes operation.completed or operation.failed when done.
func (s *LocalService) DetectCoordinator(ctx context.Context, teamID string) (string, error) {
	if s.cfg.Provider == nil {
		return "", fmt.Errorf("LLM provider not configured")
	}

	tv, err := s.GetTeam(ctx, teamID)
	if err != nil {
		return "", err
	}
	if isServiceReadOnlyTeam(tv) {
		return "", fmt.Errorf("cannot detect coordinator for read-only team %q", tv.Team.Name)
	}

	uuidVal, err := uuid.NewV4()
	if err != nil {
		return "", fmt.Errorf("generating operation ID: %w", err)
	}
	operationID := uuidVal.String()

	// Capture workers for the goroutine.
	workers := make([]Agent, len(tv.Workers))
	copy(workers, tv.Workers)
	teamDir := tv.Dir()

	if !s.tryAcquireAsync() {
		return "", fmt.Errorf("too many concurrent operations (max %d)", maxConcurrentOps)
	}

	go func() {
		defer s.releaseAsync()

		if len(workers) == 0 {
			if s.ctx.Err() != nil {
				return // service shutting down
			}
			s.broadcast(Event{
				Type:        EventTypeOperationCompleted,
				OperationID: operationID,
				Payload: OperationCompletedPayload{
					Kind: "detect_coordinator",
					Result: OperationResult{
						OperationID: operationID,
						Content:     "",
					},
				},
			})
			return
		}

		var sb strings.Builder
		sb.WriteString("Given these agents, which one is best suited to be the team coordinator? Respond with just the agent name, nothing else.\n\nAgents:\n")
		for _, a := range workers {
			desc := a.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(&sb, "- %s: %s\n", a.Name, desc)
		}

		msgs := []provider.Message{{Role: "user", Content: sb.String()}}
		llmCtx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		result, err := provider.ChatCompletion(llmCtx, s.cfg.Provider, msgs)
		cancel()
		if s.ctx.Err() != nil {
			return // service shutting down
		}
		if err != nil {
			s.broadcast(Event{
				Type:        EventTypeOperationFailed,
				OperationID: operationID,
				Payload: OperationFailedPayload{
					Kind:  "detect_coordinator",
					Error: sanitizeErrorString(err),
				},
			})
			return
		}

		// Match result to an agent name (case-insensitive, trimmed).
		result = strings.TrimSpace(result)
		detectedName := ""
		for _, a := range workers {
			if strings.EqualFold(result, a.Name) {
				detectedName = a.Name
				break
			}
		}

		// If a match was found, set the coordinator.
		if detectedName != "" {
			if err := setCoordinator(teamDir, detectedName); err != nil {
				slog.Warn("failed to set detected coordinator", "team", teamDir, "agent", detectedName, "error", err)
			} else if s.cfg.Loader != nil {
				if err := s.cfg.Loader.Load(s.ctx); err != nil {
					slog.Warn("failed to reload definitions after coordinator detection", "error", err)
				}
			}
		}

		if s.ctx.Err() != nil {
			return // service shutting down
		}
		s.broadcast(Event{
			Type:        EventTypeOperationCompleted,
			OperationID: operationID,
			Payload: OperationCompletedPayload{
				Kind: "detect_coordinator",
				Result: OperationResult{
					OperationID: operationID,
					Content:     detectedName,
				},
			},
		})
	}()

	return operationID, nil
}

// ---------------------------------------------------------------------------
// JobService (via localJobService)
// ---------------------------------------------------------------------------

// List returns jobs matching the given filter.
func (s *localJobService) List(ctx context.Context, filter *JobListFilter) ([]Job, error) {
	if s.svc.cfg.Store == nil {
		return nil, fmt.Errorf("store not configured")
	}
	dbFilter := db.JobFilter{}
	if filter != nil {
		if filter.Status != nil {
			status := db.JobStatus(*filter.Status)
			dbFilter.Status = &status
		}
		if filter.Type != nil {
			dbFilter.Type = filter.Type
		}
		dbFilter.Limit = filter.Limit
		dbFilter.Offset = filter.Offset
	}
	dbJobs, err := s.svc.cfg.Store.ListJobs(ctx, dbFilter)
	if err != nil {
		return nil, fmt.Errorf("listing jobs: %w", err)
	}
	jobs := make([]Job, 0, len(dbJobs))
	for _, j := range dbJobs {
		jobs = append(jobs, dbJobToService(j))
	}
	return jobs, nil
}

// ListAll returns all jobs regardless of status.
func (s *localJobService) ListAll(ctx context.Context) ([]Job, error) {
	if s.svc.cfg.Store == nil {
		return nil, fmt.Errorf("store not configured")
	}
	dbJobs, err := s.svc.cfg.Store.ListAllJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing all jobs: %w", err)
	}
	jobs := make([]Job, 0, len(dbJobs))
	for _, j := range dbJobs {
		jobs = append(jobs, dbJobToService(j))
	}
	return jobs, nil
}

// Get returns a JobDetail for the given job ID.
func (s *localJobService) Get(ctx context.Context, id string) (JobDetail, error) {
	if s.svc.cfg.Store == nil {
		return JobDetail{}, fmt.Errorf("store not configured")
	}
	dbJob, err := s.svc.cfg.Store.GetJob(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return JobDetail{}, fmt.Errorf("getting job %s: %w", id, ErrNotFound)
		}
		return JobDetail{}, fmt.Errorf("getting job %s: %w", id, err)
	}

	dbTasks, err := s.svc.cfg.Store.ListTasksForJob(ctx, id)
	if err != nil {
		slog.Warn("failed to list tasks for job detail", "job", id, "error", err)
	}

	dbProgress, err := s.svc.cfg.Store.GetRecentProgress(ctx, id, 5)
	if err != nil {
		slog.Warn("failed to get progress for job detail", "job", id, "error", err)
	}

	tasks := make([]Task, 0, len(dbTasks))
	for _, t := range dbTasks {
		tasks = append(tasks, dbTaskToService(t))
	}

	reports := make([]ProgressReport, 0, len(dbProgress))
	for _, p := range dbProgress {
		reports = append(reports, dbProgressToService(p))
	}

	return JobDetail{
		Job:      dbJobToService(dbJob),
		Tasks:    tasks,
		Progress: reports,
	}, nil
}

// Cancel cancels the job with the given ID.
func (s *localJobService) Cancel(ctx context.Context, id string) error {
	if s.svc.cfg.Store == nil {
		return fmt.Errorf("store not configured")
	}
	dbJob, err := s.svc.cfg.Store.GetJob(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return fmt.Errorf("getting job %s: %w", id, ErrNotFound)
		}
		return fmt.Errorf("getting job %s: %w", id, err)
	}

	switch dbJob.Status {
	case db.JobStatusActive, db.JobStatusPending, db.JobStatusSettingUp, db.JobStatusDecomposing:
		// cancellable
	default:
		return fmt.Errorf("job %s cannot be cancelled (status: %s)", id, dbJob.Status)
	}

	if err := s.svc.cfg.Store.UpdateJobStatus(ctx, id, db.JobStatusCancelled); err != nil {
		return fmt.Errorf("cancelling job %s: %w", id, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// SessionService (via localSessionService)
// ---------------------------------------------------------------------------

// List returns all currently active agent sessions as snapshots.
func (s *localSessionService) List(_ context.Context) ([]SessionSnapshot, error) {
	if s.svc.cfg.Runtime == nil {
		return nil, nil
	}
	runtimeSnaps := s.svc.cfg.Runtime.ActiveSessions()
	snaps := make([]SessionSnapshot, 0, len(runtimeSnaps))
	for _, rs := range runtimeSnaps {
		snaps = append(snaps, runtimeSnapshotToService(rs))
	}
	return snaps, nil
}

// Get returns a full SessionDetail for the given session ID.
func (s *localSessionService) Get(_ context.Context, id string) (SessionDetail, error) {
	if s.svc.cfg.Runtime == nil {
		return SessionDetail{}, fmt.Errorf("runtime not configured")
	}
	sess, ok := s.svc.cfg.Runtime.GetSession(id)
	if !ok {
		return SessionDetail{}, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}

	snap := sess.Snapshot()
	return SessionDetail{
		Snapshot:       runtimeSnapshotToService(snap),
		SystemPrompt:   sess.SystemPrompt(),
		InitialMessage: sess.InitialMessage(),
		Output:         sess.FinalText(),
		Activities:     nil, // deferred to Step 1.3
		AgentName:      snap.AgentID,
		TeamName:       snap.TeamName,
		Task:           sess.Task(),
	}, nil
}

// Cancel cancels the session with the given ID.
func (s *localSessionService) Cancel(_ context.Context, id string) error {
	if s.svc.cfg.Runtime == nil {
		return fmt.Errorf("runtime not configured")
	}
	return s.svc.cfg.Runtime.CancelSession(id)
}

// ---------------------------------------------------------------------------
// EventService
// ---------------------------------------------------------------------------

// Subscribe returns a channel that delivers all service events in order.
func (s *LocalService) Subscribe(ctx context.Context) <-chan Event {
	return s.subscribe(ctx)
}

// ---------------------------------------------------------------------------
// SystemService
// ---------------------------------------------------------------------------

// Health returns the current health status of the service.
func (s *LocalService) Health(_ context.Context) (HealthStatus, error) {
	return HealthStatus{
		Status:  "ok",
		Version: "0.1.0",
		Uptime:  time.Since(s.cfg.StartTime),
	}, nil
}

// ListModels returns all models available from the configured LLM provider.
func (s *LocalService) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if s.cfg.Provider == nil {
		return nil, fmt.Errorf("LLM provider not configured")
	}
	provModels, err := s.cfg.Provider.Models(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}
	models := make([]ModelInfo, 0, len(provModels))
	for _, m := range provModels {
		models = append(models, providerModelInfoToService(m))
	}
	return models, nil
}

// ListMCPServers returns the connection status for all configured MCP servers.
func (s *LocalService) ListMCPServers(_ context.Context) ([]MCPServerStatus, error) {
	if s.cfg.MCPManager == nil {
		return nil, nil
	}
	statuses := s.cfg.MCPManager.Servers()
	result := make([]MCPServerStatus, 0, len(statuses))
	for _, ss := range statuses {
		result = append(result, mcpServerStatusToService(ss))
	}
	return result, nil
}

// ConfigDir returns the configuration directory path. This is a local-only
// method, not part of the Service interface and not exposed over HTTP.
func (s *LocalService) ConfigDir() string {
	return s.cfg.ConfigDir
}

// Slugify converts a human-readable name into a filesystem-safe slug.
// This is a client-side utility, not exposed over HTTP.
func Slugify(name string) string {
	return loader.Slugify(name)
}

// GetProgressState returns the current full progress state snapshot.
func (s *LocalService) GetProgressState(_ context.Context) (ProgressState, error) {
	return s.buildProgressState(), nil
}

// ---------------------------------------------------------------------------
// Type mapping helpers
// ---------------------------------------------------------------------------

func dbJobToService(j *db.Job) Job {
	return Job{
		ID:           j.ID,
		Title:        j.Title,
		Description:  j.Description,
		Type:         j.Type,
		Status:       JobStatus(j.Status),
		WorkspaceDir: j.WorkspaceDir,
		CreatedAt:    j.CreatedAt,
		UpdatedAt:    j.UpdatedAt,
		Metadata:     j.Metadata,
	}
}

func dbTaskToService(t *db.Task) Task {
	return Task{
		ID:              t.ID,
		JobID:           t.JobID,
		Title:           t.Title,
		Status:          TaskStatus(t.Status),
		AgentID:         t.AgentID,
		TeamID:          t.TeamID,
		ParentID:        t.ParentID,
		SortOrder:       t.SortOrder,
		CreatedAt:       t.CreatedAt,
		UpdatedAt:       t.UpdatedAt,
		Summary:         t.Summary,
		ResultSummary:   t.ResultSummary,
		Recommendations: t.Recommendations,
		Metadata:        t.Metadata,
	}
}

func dbProgressToService(p *db.ProgressReport) ProgressReport {
	return ProgressReport{
		ID:        p.ID,
		JobID:     p.JobID,
		TaskID:    p.TaskID,
		AgentID:   p.AgentID,
		Status:    p.Status,
		Message:   p.Message,
		CreatedAt: p.CreatedAt,
	}
}

func dbSkillToService(sk *db.Skill) Skill {
	var tools []string
	if len(sk.Tools) > 0 {
		_ = json.Unmarshal(sk.Tools, &tools)
	}
	return Skill{
		ID:          sk.ID,
		Name:        sk.Name,
		Description: sk.Description,
		Tools:       tools,
		Prompt:      sk.Prompt,
		Source:      sk.Source,
		SourcePath:  sk.SourcePath,
		CreatedAt:   sk.CreatedAt,
		UpdatedAt:   sk.UpdatedAt,
	}
}

func dbAgentToService(a *db.Agent) Agent {
	var tools, disallowedTools, skills []string
	if len(a.Tools) > 0 {
		_ = json.Unmarshal(a.Tools, &tools)
	}
	if len(a.DisallowedTools) > 0 {
		_ = json.Unmarshal(a.DisallowedTools, &disallowedTools)
	}
	if len(a.Skills) > 0 {
		_ = json.Unmarshal(a.Skills, &skills)
	}
	return Agent{
		ID:              a.ID,
		Name:            a.Name,
		Description:     a.Description,
		Mode:            a.Mode,
		Model:           a.Model,
		Provider:        a.Provider,
		Temperature:     a.Temperature,
		SystemPrompt:    a.SystemPrompt,
		Tools:           tools,
		DisallowedTools: disallowedTools,
		Skills:          skills,
		PermissionMode:  a.PermissionMode,
		MaxTurns:        a.MaxTurns,
		Color:           a.Color,
		Hidden:          a.Hidden,
		Disabled:        a.Disabled,
		Source:          a.Source,
		SourcePath:      a.SourcePath,
		TeamID:          a.TeamID,
		CreatedAt:       a.CreatedAt,
		UpdatedAt:       a.UpdatedAt,
	}
}

func dbTeamToService(t *db.Team) Team {
	var skills []string
	if len(t.Skills) > 0 {
		_ = json.Unmarshal(t.Skills, &skills)
	}
	return Team{
		ID:          t.ID,
		Name:        t.Name,
		Description: t.Description,
		LeadAgent:   t.LeadAgent,
		Skills:      skills,
		Provider:    t.Provider,
		Model:       t.Model,
		Culture:     t.Culture,
		Source:      t.Source,
		SourcePath:  t.SourcePath,
		IsAuto:      t.IsAuto,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	}
}

func dbAgentSessionToService(s *db.AgentSession) AgentSession {
	return AgentSession{
		ID:        s.ID,
		AgentID:   s.AgentID,
		JobID:     s.JobID,
		TaskID:    s.TaskID,
		Status:    SessionStatus(s.Status),
		Model:     s.Model,
		Provider:  s.Provider,
		TokensIn:  s.TokensIn,
		TokensOut: s.TokensOut,
		StartedAt: s.StartedAt,
		EndedAt:   s.EndedAt,
		CostUSD:   s.CostUSD,
	}
}

func runtimeSnapshotToService(snap runtime.SessionSnapshot) SessionSnapshot {
	return SessionSnapshot{
		ID:        snap.ID,
		AgentID:   snap.AgentID,
		TeamName:  snap.TeamName,
		JobID:     snap.JobID,
		TaskID:    snap.TaskID,
		Status:    snap.Status,
		Model:     snap.Model,
		Provider:  snap.Provider,
		StartTime: snap.StartTime,
		TokensIn:  snap.TokensIn,
		TokensOut: snap.TokensOut,
	}
}

func dbFeedEntryToService(fe *db.FeedEntry) FeedEntry {
	return FeedEntry{
		ID:        fe.ID,
		JobID:     fe.JobID,
		EntryType: FeedEntryType(fe.EntryType),
		Content:   fe.Content,
		Metadata:  fe.Metadata,
		CreatedAt: fe.CreatedAt,
	}
}

func mcpServerStatusToService(ss mcp.ServerStatus) MCPServerStatus {
	var state MCPServerState
	switch ss.State {
	case mcp.ServerConnected:
		state = MCPServerStateConnected
	case mcp.ServerFailed:
		state = MCPServerStateFailed
	default:
		state = MCPServerStateFailed
	}

	tools := make([]MCPToolInfo, 0, len(ss.Tools))
	for _, t := range ss.Tools {
		tools = append(tools, MCPToolInfo{
			NamespacedName: t.NamespacedName,
			OriginalName:   t.OriginalName,
			ServerName:     t.ServerName,
			Description:    t.Description,
			InputSchema:    t.InputSchema,
		})
	}

	return MCPServerStatus{
		Name:      ss.Name,
		Transport: ss.Transport,
		State:     state,
		Error:     ss.Error,
		ToolCount: ss.ToolCount,
		Tools:     tools,
	}
}

func providerModelInfoToService(m provider.ModelInfo) ModelInfo {
	return ModelInfo{
		ID:                  m.ID,
		Name:                m.Name,
		Provider:            m.Provider,
		State:               m.State,
		MaxContextLength:    m.MaxContextLength,
		LoadedContextLength: m.LoadedContextLength,
	}
}

// buildTeamViews queries the store to build TeamView slices for all non-system teams.
func (s *LocalService) buildTeamViews(ctx context.Context) ([]TeamView, error) {
	dbTeams, err := s.cfg.Store.ListTeams(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing teams: %w", err)
	}

	var views []TeamView
	for _, team := range dbTeams {
		if team.Source == "system" {
			continue
		}
		tv := TeamView{Team: dbTeamToService(team)}

		teamAgents, err := s.cfg.Store.ListTeamAgents(ctx, team.ID)
		if err != nil {
			slog.Warn("failed to list team agents", "team", team.Name, "error", err)
			views = append(views, tv)
			continue
		}

		for _, ta := range teamAgents {
			agent, err := s.cfg.Store.GetAgent(ctx, ta.AgentID)
			if err != nil {
				slog.Warn("failed to get agent", "agentID", ta.AgentID, "error", err)
				continue
			}
			svcAgent := dbAgentToService(agent)
			if ta.Role == "lead" {
				tv.Coordinator = &svcAgent
			} else {
				tv.Workers = append(tv.Workers, svcAgent)
			}
		}
		views = append(views, tv)
	}
	return views, nil
}

// ---------------------------------------------------------------------------
// Team classification helpers (adapted from tui/team_view.go)
// ---------------------------------------------------------------------------

var (
	cachedHomeDir     string
	cachedHomeDirOnce sync.Once
)

// GetHomeDir returns the current user's home directory, cached after the first call.
// This is a client-side utility used by team classification helpers.
func GetHomeDir() string {
	cachedHomeDirOnce.Do(func() {
		cachedHomeDir, _ = os.UserHomeDir()
	})
	return cachedHomeDir
}

// isServiceReadOnlyTeam returns true if the team's directory is one of the
// well-known auto-detected read-only directories.
func isServiceReadOnlyTeam(tv TeamView) bool {
	home := GetHomeDir()
	if home == "" {
		return false
	}
	readOnlyDirs := []string{
		filepath.Join(home, ".config", "opencode", "agents"),
		filepath.Join(home, ".claude", "agents"),
	}
	for _, d := range readOnlyDirs {
		if tv.Dir() == d {
			return true
		}
	}
	return false
}

// isServiceSystemTeam returns true if the team's directory is under the system directory.
func isServiceSystemTeam(tv TeamView, configDir string) bool {
	systemDir := filepath.Join(configDir, "system")
	return strings.HasPrefix(tv.Dir(), systemDir+string(filepath.Separator))
}

// isServiceAutoTeam returns true if the team is auto-detected.
func isServiceAutoTeam(tv TeamView) bool {
	if isServiceReadOnlyTeam(tv) {
		return true
	}
	if tv.IsAuto() {
		return true
	}
	_, err := os.Stat(filepath.Join(tv.Dir(), ".auto-team"))
	return err == nil
}

// ---------------------------------------------------------------------------
// Team promotion helpers (adapted from tui/teams_modal.go)
// ---------------------------------------------------------------------------

// promoteReadOnlyAutoTeam handles promotion of legacy read-only auto-teams.
func (s *LocalService) promoteReadOnlyAutoTeam(tv TeamView) error {
	userTeamsDir := filepath.Join(s.cfg.ConfigDir, "user", "teams")

	slug := loader.Slugify(tv.Team.Name)
	if slug == "" {
		return fmt.Errorf("team name %q produces empty slug", tv.Team.Name)
	}
	if err := os.MkdirAll(userTeamsDir, 0o755); err != nil {
		return fmt.Errorf("creating user teams dir: %w", err)
	}
	realUserTeamsDir, err := filepath.EvalSymlinks(userTeamsDir)
	if err != nil {
		return fmt.Errorf("resolving user teams dir: %w", err)
	}
	targetDir := filepath.Join(realUserTeamsDir, slug)
	if !strings.HasPrefix(targetDir+string(filepath.Separator), realUserTeamsDir+string(filepath.Separator)) {
		return fmt.Errorf("team name %q escapes teams directory", tv.Team.Name)
	}
	targetAgentsDir := filepath.Join(targetDir, "agents")

	if _, err := os.Stat(targetDir); err == nil {
		return fmt.Errorf("team directory %q already exists", targetDir)
	}

	// For read-only teams, Dir IS the agents directory.
	agentsSourceDir := tv.Dir()

	matches, err := filepath.Glob(filepath.Join(agentsSourceDir, "*.md"))
	if err != nil {
		return fmt.Errorf("globbing agent files in %s: %w", agentsSourceDir, err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no agent files found in %s", agentsSourceDir)
	}

	type parsedAgent struct {
		stem string
		def  *agentfmt.AgentDef
	}
	var parsed []parsedAgent
	for _, path := range matches {
		defType, def, err := agentfmt.ParseFile(path)
		if err != nil {
			slog.Warn("skipping unparseable agent during promotion", "path", path, "error", err)
			continue
		}
		if defType != agentfmt.DefAgent {
			slog.Warn("skipping non-agent file during promotion", "path", path, "type", defType)
			continue
		}
		agentDef, ok := def.(*agentfmt.AgentDef)
		if !ok {
			slog.Warn("unexpected type for agent definition", "path", path)
			continue
		}
		stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		parsed = append(parsed, parsedAgent{stem: stem, def: agentDef})
	}
	if len(parsed) == 0 {
		return fmt.Errorf("no valid agent definitions found in %s", agentsSourceDir)
	}

	if err := os.MkdirAll(targetAgentsDir, 0o755); err != nil {
		return fmt.Errorf("creating target directory %s: %w", targetAgentsDir, err)
	}

	var agentNames []string
	for _, pa := range parsed {
		safeFilename := loader.Slugify(pa.stem)
		if safeFilename == "" {
			safeFilename = loader.Slugify(pa.def.Name)
		}
		if safeFilename == "" {
			slog.Warn("skipping agent with unsluggable filename", "stem", pa.stem)
			continue
		}
		agentPath := filepath.Join(targetAgentsDir, safeFilename+".md")
		if err := writeAgentFile(agentPath, pa.def); err != nil {
			_ = os.RemoveAll(targetDir)
			return fmt.Errorf("writing agent file %s: %w", agentPath, err)
		}
		agentNames = append(agentNames, pa.def.Name)
	}

	lead := ""
	if tv.Coordinator != nil {
		lead = tv.Coordinator.Name
	}

	source := filepath.Base(filepath.Dir(tv.Dir())) + "/" + filepath.Base(tv.Dir())

	teamDef := &agentfmt.TeamDef{
		Name:        tv.Team.Name,
		Description: fmt.Sprintf("Promoted from %s", source),
		Lead:        lead,
		Agents:      agentNames,
	}
	teamMDPath := filepath.Join(targetDir, "team.md")
	if err := writeTeamFile(teamMDPath, teamDef); err != nil {
		_ = os.RemoveAll(targetDir)
		return fmt.Errorf("writing team.md: %w", err)
	}

	slog.Info("promoted read-only auto-team to managed team", "team", tv.Team.Name, "target", targetDir, "agents", len(parsed))
	return nil
}

// promoteMarkerAutoTeam handles in-place promotion of bootstrap auto-teams.
func promoteMarkerAutoTeam(tv TeamView) error {
	agentsSymlink := filepath.Join(tv.Dir(), "agents")

	matches, err := filepath.Glob(filepath.Join(agentsSymlink, "*.md"))
	if err != nil {
		return fmt.Errorf("globbing agent files in %s: %w", agentsSymlink, err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no agent files found in %s", agentsSymlink)
	}

	type parsedAgent struct {
		stem string
		def  *agentfmt.AgentDef
	}
	var parsed []parsedAgent
	for _, path := range matches {
		defType, def, err := agentfmt.ParseFile(path)
		if err != nil {
			slog.Warn("skipping unparseable agent during promotion", "path", path, "error", err)
			continue
		}
		if defType != agentfmt.DefAgent {
			slog.Warn("skipping non-agent file during promotion", "path", path, "type", defType)
			continue
		}
		agentDef, ok := def.(*agentfmt.AgentDef)
		if !ok {
			slog.Warn("unexpected type for agent definition", "path", path)
			continue
		}
		stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		parsed = append(parsed, parsedAgent{stem: stem, def: agentDef})
	}
	if len(parsed) == 0 {
		return fmt.Errorf("no valid agent definitions found in %s", agentsSymlink)
	}

	if err := os.Remove(agentsSymlink); err != nil {
		return fmt.Errorf("removing agents symlink %s: %w", agentsSymlink, err)
	}
	if err := os.MkdirAll(agentsSymlink, 0o755); err != nil {
		return fmt.Errorf("creating agents directory %s: %w", agentsSymlink, err)
	}

	var agentNames []string
	for _, pa := range parsed {
		safeFilename := loader.Slugify(pa.stem)
		if safeFilename == "" {
			safeFilename = loader.Slugify(pa.def.Name)
		}
		if safeFilename == "" {
			slog.Warn("skipping agent with unsluggable filename", "stem", pa.stem)
			continue
		}
		agentPath := filepath.Join(agentsSymlink, safeFilename+".md")
		if err := writeAgentFile(agentPath, pa.def); err != nil {
			_ = os.RemoveAll(agentsSymlink)
			return fmt.Errorf("writing agent file %s: %w", agentPath, err)
		}
		agentNames = append(agentNames, pa.def.Name)
	}

	lead := ""
	if tv.Coordinator != nil {
		lead = tv.Coordinator.Name
	}

	teamDef := &agentfmt.TeamDef{
		Name:        tv.Team.Name,
		Description: fmt.Sprintf("Promoted from %s", tv.Team.Name),
		Lead:        lead,
		Agents:      agentNames,
	}
	teamMDPath := filepath.Join(tv.Dir(), "team.md")
	if err := writeTeamFile(teamMDPath, teamDef); err != nil {
		return fmt.Errorf("writing team.md: %w", err)
	}

	if err := os.Remove(filepath.Join(tv.Dir(), ".auto-team")); err != nil {
		slog.Warn("failed to remove .auto-team marker", "dir", tv.Dir(), "error", err)
	}

	slog.Info("promoted bootstrap auto-team in-place", "team", tv.Team.Name, "dir", tv.Dir(), "agents", len(parsed))
	return nil
}

// ---------------------------------------------------------------------------
// SetCoordinator helper (adapted from tui/team_view.go)
// ---------------------------------------------------------------------------

// setCoordinator updates a team so that exactly one agent is the coordinator.
func setCoordinator(teamDir, agentName string) error {
	agentsDir := filepath.Join(teamDir, "agents")
	matches, err := filepath.Glob(filepath.Join(agentsDir, "*.md"))
	if err != nil {
		return fmt.Errorf("globbing agent files in %s: %w", agentsDir, err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no agent files found in %s", agentsDir)
	}

	needle := strings.ToLower(agentName)
	type agentFile struct {
		path string
		name string
	}
	var agentFiles []agentFile
	for _, p := range matches {
		stem := strings.TrimSuffix(filepath.Base(p), ".md")
		name := stem
		if defType, def, parseErr := agentfmt.ParseFile(p); parseErr == nil && defType == agentfmt.DefAgent {
			if agentDef, ok := def.(*agentfmt.AgentDef); ok && agentDef.Name != "" {
				name = agentDef.Name
			}
		}
		agentFiles = append(agentFiles, agentFile{path: p, name: name})
	}

	found := false
	for _, af := range agentFiles {
		if strings.ToLower(af.name) == needle {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("agent %q not found in %s", agentName, agentsDir)
	}

	// Update team.md's lead: field. If team.md is missing or malformed, create a minimal one.
	teamMDPath := filepath.Join(teamDir, "team.md")
	teamDef, parseErr := agentfmt.ParseTeam(teamMDPath)
	if parseErr != nil {
		teamDef = &agentfmt.TeamDef{Name: filepath.Base(teamDir)}
	}
	teamDef.Lead = agentName
	if writeErr := writeTeamFileTo(teamMDPath, teamDef); writeErr != nil {
		return fmt.Errorf("updating team.md lead: %w", writeErr)
	}

	// Rewrite mode: in each agent file.
	for _, af := range agentFiles {
		targetMode := "worker"
		if strings.ToLower(af.name) == needle {
			targetMode = "primary"
		}

		data, err := os.ReadFile(af.path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", af.path, err)
		}

		newContent := rewriteMode(string(data), targetMode)

		tmp, err := os.CreateTemp(agentsDir, "agent-*.md.tmp")
		if err != nil {
			return fmt.Errorf("creating temp file in %s: %w", agentsDir, err)
		}
		tmpName := tmp.Name()

		if _, err := tmp.WriteString(newContent); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
			return fmt.Errorf("writing temp file %s: %w", tmpName, err)
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("closing temp file %s: %w", tmpName, err)
		}
		if err := os.Rename(tmpName, af.path); err != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("renaming %s to %s: %w", tmpName, af.path, err)
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// File-writing helpers (adapted from tui/teams_modal.go and tui/agents_modal.go)
// ---------------------------------------------------------------------------

// writeAgentFile writes an AgentDef as a toasters-format .md file.
func writeAgentFile(path string, def *agentfmt.AgentDef) error {
	fm, err := yaml.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshaling agent frontmatter: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(bytes.TrimRight(fm, "\n"))
	sb.WriteString("\n---\n")
	if def.Body != "" {
		sb.WriteString(def.Body)
		sb.WriteString("\n")
	}

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// writeTeamFile writes a TeamDef as a toasters-format .md file.
func writeTeamFile(path string, def *agentfmt.TeamDef) error {
	fm, err := yaml.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshaling team frontmatter: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(bytes.TrimRight(fm, "\n"))
	sb.WriteString("\n---\n")
	if def.Body != "" {
		sb.WriteString(def.Body)
		sb.WriteString("\n")
	}

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// writeTeamFileTo writes a TeamDef as a toasters-format .md file (alias for writeTeamFile).
func writeTeamFileTo(path string, def *agentfmt.TeamDef) error {
	return writeTeamFile(path, def)
}

// rewriteMode returns content with the frontmatter mode: field set to mode.
func rewriteMode(content, mode string) string {
	const delim = "---"
	modeLine := "mode: " + mode

	if !strings.HasPrefix(content, delim+"\n") {
		return delim + "\n" + modeLine + "\n" + delim + "\n" + content
	}

	rest := content[len(delim)+1:]
	closingIdx := strings.Index(rest, "\n"+delim)
	if closingIdx < 0 {
		return delim + "\n" + modeLine + "\n" + delim + "\n" + content
	}

	fmBlock := rest[:closingIdx]
	afterClose := rest[closingIdx+1+len(delim):]

	lines := strings.Split(fmBlock, "\n")
	modeFound := false
	for i, line := range lines {
		if strings.HasPrefix(line, "mode:") {
			lines[i] = modeLine
			modeFound = true
			break
		}
	}
	if !modeFound {
		lines = append(lines, modeLine)
	}

	var sb strings.Builder
	sb.WriteString(delim + "\n")
	sb.WriteString(strings.Join(lines, "\n"))
	sb.WriteString("\n" + delim)
	sb.WriteString(afterClose)
	return sb.String()
}

// copyFile copies the file at src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}

	out, err := os.Create(dst)
	if err != nil {
		_ = in.Close()
		return fmt.Errorf("creating destination file: %w", err)
	}

	n, err := io.Copy(out, io.LimitReader(in, maxCopySize+1))
	if err != nil {
		_ = in.Close()
		_ = out.Close()
		return fmt.Errorf("copying file contents: %w", err)
	}
	if n > maxCopySize {
		_ = in.Close()
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("source file too large: %d bytes exceeds maximum %d", n, maxCopySize)
	}

	if err := in.Close(); err != nil {
		_ = out.Close()
		return fmt.Errorf("closing source file: %w", err)
	}

	return out.Close()
}

// writeGeneratedSkillFile writes LLM-generated skill content to the user skills directory.
func (s *LocalService) writeGeneratedSkillFile(content string) (string, error) {
	skillsDir := filepath.Join(s.cfg.ConfigDir, "user", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return "", fmt.Errorf("creating skills dir: %w", err)
	}

	slug := "generated-skill"
	if parsed, err := agentfmt.ParseBytes([]byte(content), agentfmt.DefSkill); err == nil {
		if skillDef, ok := parsed.(*agentfmt.SkillDef); ok && skillDef.Name != "" {
			nameSlug := loader.Slugify(skillDef.Name)
			if nameSlug != "" {
				slug = nameSlug
			}
		}
	}

	path := filepath.Join(skillsDir, slug+".md")
	if _, err := os.Stat(path); err == nil {
		found := false
		for i := 2; i < 1000; i++ {
			candidate := filepath.Join(skillsDir, fmt.Sprintf("%s-%d.md", slug, i))
			if _, err := os.Stat(candidate); os.IsNotExist(err) {
				path = candidate
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("too many skill files with slug %q", slug)
		}
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("writing skill file: %w", err)
	}
	return path, nil
}

// writeGeneratedAgentFile writes LLM-generated agent content to the user agents directory.
func (s *LocalService) writeGeneratedAgentFile(content string) (string, string, error) {
	agentsDir := filepath.Join(s.cfg.ConfigDir, "user", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return "", "", fmt.Errorf("creating agents dir: %w", err)
	}

	slug := "generated-agent"
	agentName := ""
	if parsed, err := agentfmt.ParseBytes([]byte(content), agentfmt.DefAgent); err == nil {
		if agentDef, ok := parsed.(*agentfmt.AgentDef); ok && agentDef.Name != "" {
			agentName = agentDef.Name
			nameSlug := loader.Slugify(agentDef.Name)
			if nameSlug != "" {
				slug = nameSlug
			}
		}
	}
	if agentName == "" {
		agentName = slug
	}

	path := filepath.Join(agentsDir, slug+".md")
	if _, err := os.Stat(path); err == nil {
		found := false
		for i := 2; i < 1000; i++ {
			candidate := filepath.Join(agentsDir, fmt.Sprintf("%s-%d.md", slug, i))
			if _, err := os.Stat(candidate); os.IsNotExist(err) {
				path = candidate
				found = true
				break
			}
		}
		if !found {
			return "", "", fmt.Errorf("too many agent files with slug %q", slug)
		}
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", "", fmt.Errorf("writing agent file: %w", err)
	}
	return path, agentName, nil
}

// stripCodeFences removes markdown code fences from LLM output.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		idx := strings.Index(s, "\n")
		if idx != -1 {
			s = s[idx+1:]
		}
	}
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// ---------------------------------------------------------------------------
// Sort helpers
// ---------------------------------------------------------------------------

// sortAgentsByTeamKey sorts agents by the composite "team/agent" key.
func sortAgentsByTeamKey(agents []*db.Agent) {
	for i := 1; i < len(agents); i++ {
		for j := i; j > 0; j-- {
			ka := agents[j-1].TeamID + "/" + agents[j-1].Name
			kb := agents[j].TeamID + "/" + agents[j].Name
			if ka > kb {
				agents[j-1], agents[j] = agents[j], agents[j-1]
			} else {
				break
			}
		}
	}
}
