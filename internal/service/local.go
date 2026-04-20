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
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"gopkg.in/yaml.v3"

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/hitl"
	"github.com/jefflinse/toasters/internal/loader"
	"github.com/jefflinse/toasters/internal/mcp"
	"github.com/jefflinse/toasters/internal/mdfmt"
	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// Compile-time assertion that LocalService satisfies the Service interface.
var _ Service = (*LocalService)(nil)

// CatalogSource provides access to the models.dev catalog data.
// Implemented by modelsdev.Client.
type CatalogSource interface {
	ProvidersSorted(ctx context.Context) ([]CatalogSourceProvider, error)
}

// CatalogSourceProvider is the provider shape expected from the catalog source.
type CatalogSourceProvider struct {
	ID   string
	Name string
	API  string
	Doc  string
	Env  []string

	Models []CatalogSourceModel
}

// CatalogSourceModel is the model shape expected from the catalog source.
type CatalogSourceModel struct {
	ID               string
	Name             string
	Family           string
	ToolCall         bool
	Reasoning        bool
	StructuredOutput bool
	OpenWeights      bool
	ContextLimit     int
	OutputLimit      int
	InputCost        float64
	OutputCost       float64
}

// Size limits for input validation.
const (
	maxMessageLen     = 102400           // 100KB — maximum user message size
	maxPromptLen      = 51200            // 50KB — maximum generation prompt size
	maxResponseLen    = 51200            // 50KB — maximum prompt/blocker response size
	maxBlockerAnswers = 50               // maximum number of blocker answers
	maxCopySize       = 50 * 1024 * 1024 // 50MB — maximum file copy size
)

// maxConcurrentOps bounds the number of concurrent async operations (generate,
// promote, detect) that can run simultaneously.
const maxConcurrentOps = 5

// maxHistoryEntries bounds the conversation history kept for reconnect hydration.
const maxHistoryEntries = 1000

// LocalConfig holds the dependencies for LocalService.
type LocalConfig struct {
	Store            db.Store
	Runtime          *runtime.Runtime
	Operator         *operator.Operator
	MCPManager       *mcp.Manager
	Provider         provider.Provider // operator's LLM provider (for ListModels, generation)
	DefaultProvider  string            // default provider for system agents and team leads
	DefaultModel     string            // default model for system agents and team leads
	Loader           *loader.Loader
	ConfigDir        string
	WorkspaceDir     string
	TeamsDir         string
	OperatorModel    string                     // for OperatorStatus.ModelName
	OperatorEndpoint string                     // for OperatorStatus.Endpoint (LLM provider URL)
	StartTime        time.Time                  // for Health().Uptime
	Catalog          CatalogSource              // optional models.dev catalog; nil disables ListCatalogProviders
	Registry         *provider.Registry         // provider registry for live operator activation
	PromptEngine     *prompt.Engine             // optional; for role-based prompt composition
	GraphExecutor    operator.GraphTaskExecutor // optional; rhizome graph-based task execution
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

	// asyncSem bounds concurrent async operations (generate, promote, detect).
	asyncSem chan struct{}

	// Operator lifecycle — for live activation.
	opMu     sync.Mutex
	opCancel context.CancelFunc // cancels the running operator; nil if no operator

	// broker coordinates HITL prompt/response for both the operator's
	// ask_user tool and any graph node that calls rhizome.Interrupt.
	broker *hitl.Broker
}

// localJobService wraps LocalService to implement JobService without conflicting
// with SessionService methods of the same name (List, Get, Cancel).
type localJobService struct{ svc *LocalService }

// localSessionService wraps LocalService to implement SessionService without
// conflicting with JobService methods of the same name (List, Get, Cancel).
type localSessionService struct{ svc *LocalService }

// nameReplacer strips characters that could cause YAML injection when
// interpolated into frontmatter templates.
var nameReplacer = strings.NewReplacer(
	"\n", "",
	"\r", "",
	"\x00", "",
	":", "",
	"#", "",
	"\"", "",
	"'", "",
	"{", "",
	"}", "",
	"[", "",
	"]", "",
	"|", "",
	">", "",
)

// sanitizeName strips characters that could cause YAML injection when
// interpolated into frontmatter templates.
func sanitizeName(name string) string {
	return nameReplacer.Replace(name)
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
		broker:      hitl.New(),
	}
}

// Broker exposes the HITL broker so the operator and graph executor can
// register pending prompts with a single shared coordinator.
func (s *LocalService) Broker() *hitl.Broker { return s.broker }

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

// safeGo launches fn in a goroutine with panic recovery. If fn panics,
// the stack trace is logged and an operation.failed event is broadcast.
func (s *LocalService) safeGo(operationID, kind string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				slog.Error("panic in async operation",
					"operation_id", operationID,
					"kind", kind,
					"panic", fmt.Sprintf("%v", r),
					"stack", string(stack),
				)
				s.broadcast(Event{
					Type:        EventTypeOperationFailed,
					OperationID: operationID,
					Payload: OperationFailedPayload{
						Kind:  kind,
						Error: "internal error: unexpected panic",
					},
				})
			}
		}()
		fn()
	}()
}

// SetGraphExecutor sets the graph executor on the service after construction.
// Required when startup-time operator creation is skipped (no operator
// provider configured yet) and the operator is instead created live via
// startOperator after the user selects a provider in the TUI.
func (s *LocalService) SetGraphExecutor(g operator.GraphTaskExecutor) {
	s.cfg.GraphExecutor = g
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
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic in subscriber cleanup", "panic", fmt.Sprintf("%v", r))
			}
		}()
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

// progressPollLoop periodically broadcasts a full progress snapshot to keep
// the panel views in sync with SQLite.
//
// As of Phase 4, the chat/feed area is driven by dedicated push events
// (job.created, task.created, task.assigned, task.started, task.completed,
// etc.) so the user sees real-time activity even between poll ticks. The
// poll continues to drive the Jobs / Tasks / Feed panels because they read
// from a snapshot rather than a stream.
//
// TODO(future): replace this loop entirely with broadcasts on every state
// change site (DB-side updates, MCP status changes, feed inserts) and delete
// the EventTypeProgressUpdate event type. The current 500ms cadence is fine
// for a single-user local tool, but it creates 2 SSE messages per second per
// connected client even when nothing is happening.
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
			state.ActiveSessions = append(state.ActiveSessions, dbWorkerSessionToService(sess))
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

	// MCP servers.
	if s.cfg.MCPManager != nil {
		for _, ss := range s.cfg.MCPManager.Servers() {
			state.MCPServers = append(state.MCPServers, mcpServerStatusToService(ss))
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
				WorkerID:    payload.WorkerID,
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

// BroadcastJobCreated broadcasts a job.created event. Implements
// operator.SystemEventBroadcaster — called by SystemTools.createJob after the
// new job is persisted.
func (s *LocalService) BroadcastJobCreated(jobID, title, description string) {
	s.broadcast(Event{
		Type: EventTypeJobCreated,
		Payload: JobCreatedPayload{
			JobID:       jobID,
			Title:       title,
			Description: description,
		},
	})
}

// BroadcastTaskCreated broadcasts a task.created event. Implements
// operator.SystemEventBroadcaster — called by SystemTools.createTask after the
// new task is persisted.
func (s *LocalService) BroadcastTaskCreated(taskID, jobID, title, teamID string) {
	s.broadcast(Event{
		Type: EventTypeTaskCreated,
		Payload: TaskCreatedPayload{
			TaskID: taskID,
			JobID:  jobID,
			Title:  title,
			TeamID: teamID,
		},
	})
}

// BroadcastTaskAssigned broadcasts a task.assigned event. Implements
// operator.SystemEventBroadcaster — called by SystemTools.assignTask after a
// task has been pre-assigned or activated for a team.
func (s *LocalService) BroadcastTaskAssigned(taskID, jobID, teamID, title string) {
	s.broadcast(Event{
		Type: EventTypeTaskAssigned,
		Payload: TaskAssignedPayload{
			TaskID: taskID,
			JobID:  jobID,
			TeamID: teamID,
			Title:  title,
		},
	})
}

// BroadcastGraphNodeStarted broadcasts a graph.node_started event.
func (s *LocalService) BroadcastGraphNodeStarted(jobID, taskID, node string) {
	s.broadcast(Event{
		Type: EventTypeGraphNodeStarted,
		Payload: GraphNodeStartedPayload{
			JobID:  jobID,
			TaskID: taskID,
			Node:   node,
		},
	})
}

// BroadcastGraphNodeCompleted broadcasts a graph.node_completed event.
func (s *LocalService) BroadcastGraphNodeCompleted(jobID, taskID, node, status string) {
	s.broadcast(Event{
		Type: EventTypeGraphNodeCompleted,
		Payload: GraphNodeCompletedPayload{
			JobID:  jobID,
			TaskID: taskID,
			Node:   node,
			Status: status,
		},
	})
}

// BroadcastGraphCompleted broadcasts a graph.completed event.
func (s *LocalService) BroadcastGraphCompleted(jobID, taskID, summary string) {
	s.broadcast(Event{
		Type: EventTypeGraphCompleted,
		Payload: GraphCompletedPayload{
			JobID:   jobID,
			TaskID:  taskID,
			Summary: summary,
		},
	})
}

// BroadcastGraphFailed broadcasts a graph.failed event.
func (s *LocalService) BroadcastGraphFailed(jobID, taskID, errMsg string) {
	s.broadcast(Event{
		Type: EventTypeGraphFailed,
		Payload: GraphFailedPayload{
			JobID:  jobID,
			TaskID: taskID,
			Error:  errMsg,
		},
	})
}

// BroadcastSessionText broadcasts a session.text event for an arbitrary
// session id. Used by graph nodes (which synthesize session ids of the form
// "graph:<TaskID>:<Node>") to stream LLM text through the same pipeline as
// worker sessions.
func (s *LocalService) BroadcastSessionText(sessionID, text string) {
	if text == "" {
		return
	}
	s.broadcast(Event{
		Type:      EventTypeSessionText,
		SessionID: sessionID,
		Payload:   SessionTextPayload{Text: text},
	})
}

// BroadcastPrompt emits an OperatorPromptPayload with Source populated so the
// TUI can surface a HITL question that originated from a graph node (via
// rhizome.Interrupt). The operator's own ask_user path emits the same event
// with an empty Source; both travel through the existing OperatorPrompt
// pipeline without forking the event type.
func (s *LocalService) BroadcastPrompt(requestID, question string, options []string, source string) {
	s.broadcast(Event{
		Type: EventTypeOperatorPrompt,
		Payload: OperatorPromptPayload{
			RequestID: requestID,
			Question:  question,
			Options:   options,
			Source:    source,
		},
	})
}

// BroadcastTaskCompleted signals task completion to the operator's event loop
// so it can advance to the next ready task. Called by graphexec.Executor
// after a graph finishes successfully — mirrors what team_tools.completeTask
// does on the team-lead path.
func (s *LocalService) BroadcastTaskCompleted(jobID, taskID, teamID, summary string, hasNextTask bool) {
	op := s.currentOperator()
	if op == nil {
		slog.Warn("graph task completed but operator unavailable; next task will not auto-advance",
			"task_id", taskID, "job_id", jobID)
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	if err := op.Send(ctx, operator.Event{
		Type: operator.EventTaskCompleted,
		Payload: operator.TaskCompletedPayload{
			TaskID:      taskID,
			JobID:       jobID,
			TeamID:      teamID,
			Summary:     summary,
			HasNextTask: hasNextTask,
		},
	}); err != nil {
		slog.Warn("failed to forward task_completed to operator",
			"task_id", taskID, "job_id", jobID, "error", err)
	}
}

// BroadcastTaskFailed signals task failure to the operator's event loop so it
// can consult the blocker-handler. Mirrors team_lead's force-fail pathway.
func (s *LocalService) BroadcastTaskFailed(jobID, taskID, teamID, errMsg string) {
	op := s.currentOperator()
	if op == nil {
		slog.Warn("graph task failed but operator unavailable",
			"task_id", taskID, "job_id", jobID)
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	if err := op.Send(ctx, operator.Event{
		Type: operator.EventTaskFailed,
		Payload: operator.TaskFailedPayload{
			TaskID: taskID,
			JobID:  jobID,
			TeamID: teamID,
			Error:  errMsg,
		},
	}); err != nil {
		slog.Warn("failed to forward task_failed to operator",
			"task_id", taskID, "job_id", jobID, "error", err)
	}
}

// currentOperator returns the currently active operator, if any, honoring
// live activation/replacement under opMu.
func (s *LocalService) currentOperator() *operator.Operator {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.cfg.Operator
}

// BroadcastSessionStarted bridges a runtime session into the unified service
// event stream. It emits session.started immediately, then spawns a goroutine
// that subscribes to the session's events and re-emits them as session.text /
// session.tool_call / session.tool_result events. When the session terminates,
// it emits a final session.done event.
//
// This is the only path by which worker session activity reaches subscribers
// (TUI clients, SSE clients). It must be wired to runtime.Runtime.OnSessionStarted
// during server startup.
func (s *LocalService) BroadcastSessionStarted(sess *runtime.Session) {
	snap := sess.Snapshot()
	sessionID := snap.ID

	// Subscribe BEFORE emitting session.started so that any events the session
	// produces between SpawnAgent's OnSessionStarted invocation and the start
	// of Run() are captured. Subscribe is safe to call before Run() begins.
	events := sess.Subscribe()

	s.broadcast(Event{
		Type:      EventTypeSessionStarted,
		SessionID: sessionID,
		Payload: SessionStartedPayload{
			SessionID:      sessionID,
			WorkerName:     snap.WorkerID,
			TeamName:       snap.TeamName,
			Task:           sess.Task(),
			JobID:          snap.JobID,
			TaskID:         snap.TaskID,
			SystemPrompt:   sess.SystemPrompt(),
			InitialMessage: sess.InitialMessage(),
		},
	})

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic in session event forwarder",
					"session_id", sessionID,
					"panic", fmt.Sprintf("%v", r),
					"stack", string(debug.Stack()))
			}
		}()

		// Batch session.text events with a 16ms window. The runtime emits one
		// SessionEventText per token, which for a fast model can be 100+ events
		// per second. Each broadcast turns into a wire message, an SSE write,
		// and (on the client) a Bubble Tea Msg that has to traverse an
		// unbuffered prog.Send. Without batching, the TUI's main loop falls
		// behind and the Subscribe channel fills, dropping events and freezing
		// the UI. The operator already does this for its own text — see
		// cmd/batcher.go — and the same fix applies here.
		const textBatchWindow = 16 * time.Millisecond
		var textBuf strings.Builder
		var textTimer *time.Timer
		var textTimerCh <-chan time.Time
		flushText := func() {
			if textBuf.Len() == 0 {
				return
			}
			s.broadcast(Event{
				Type:      EventTypeSessionText,
				SessionID: sessionID,
				Payload:   SessionTextPayload{Text: textBuf.String()},
			})
			textBuf.Reset()
			if textTimer != nil {
				textTimer.Stop()
				textTimer = nil
				textTimerCh = nil
			}
		}
		armTimer := func() {
			if textTimer != nil {
				return
			}
			textTimer = time.NewTimer(textBatchWindow)
			textTimerCh = textTimer.C
		}

		for {
			select {
			case ev, ok := <-events:
				if !ok {
					// Subscribe channel closed — session has terminated. Flush
					// any pending text and emit session.done.
					flushText()
					finalSnap := sess.Snapshot()
					s.broadcast(Event{
						Type:      EventTypeSessionDone,
						SessionID: sessionID,
						Payload: SessionDonePayload{
							WorkerName: finalSnap.WorkerID,
							JobID:      finalSnap.JobID,
							TaskID:     finalSnap.TaskID,
							Status:     finalSnap.Status,
							FinalText:  sess.FinalText(),
						},
					})
					return
				}
				switch ev.Type {
				case runtime.SessionEventText:
					textBuf.WriteString(ev.Text)
					armTimer()
				case runtime.SessionEventToolCall:
					if ev.ToolCall == nil {
						continue
					}
					// Flush pending text before structural events so the
					// ordering observed by clients matches what the model
					// emitted.
					flushText()
					s.broadcast(Event{
						Type:      EventTypeSessionToolCall,
						SessionID: sessionID,
						Payload: SessionToolCallPayload{
							ToolCall: ToolCall{
								ID:        ev.ToolCall.ID,
								Name:      ev.ToolCall.Name,
								Arguments: ev.ToolCall.Arguments,
							},
						},
					})
				case runtime.SessionEventToolResult:
					if ev.ToolResult == nil {
						continue
					}
					flushText()
					s.broadcast(Event{
						Type:      EventTypeSessionToolResult,
						SessionID: sessionID,
						Payload: SessionToolResultPayload{
							Result: ToolCallResult{
								CallID: ev.ToolResult.CallID,
								Name:   ev.ToolResult.Name,
								Result: ev.ToolResult.Result,
								Error:  ev.ToolResult.Error,
							},
						},
					})
				}

			case <-textTimerCh:
				flushText()
			}
		}
	}()
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
	if s.cfg.Operator == nil {
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

	return OperatorStatus{
		State:         state,
		CurrentTurnID: turnID,
		ModelName:     s.cfg.OperatorModel,
		Endpoint:      s.cfg.OperatorEndpoint,
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

// RespondToBlocker submits the user's answers to a blocker reported by an agent.
func (s *LocalService) RespondToBlocker(ctx context.Context, jobID, taskID string, answers []string) error {
	// Validate number of answers before checking operator configuration.
	if len(answers) > maxBlockerAnswers {
		return fmt.Errorf("too many answers: %d exceeds maximum %d", len(answers), maxBlockerAnswers)
	}

	// Validate each answer size before checking operator configuration.
	for i, answer := range answers {
		if len(answer) > maxResponseLen {
			return fmt.Errorf("answer %d too large: %d bytes exceeds maximum %d", i, len(answer), maxResponseLen)
		}
	}

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

	s.safeGo(operationID, "generate_skill", func() {
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
	})

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

	if _, err := mdfmt.ParseBytes([]byte(content), mdfmt.DefSkill); err != nil {
		return "", fmt.Errorf("generated content is not a valid skill definition: %w", err)
	}

	return content, nil
}

// ---------------------------------------------------------------------------
// DefinitionService — Workers
// ---------------------------------------------------------------------------

// ListWorkers returns all workers ordered: shared → team-local → system.
func (s *LocalService) ListWorkers(ctx context.Context) ([]Worker, error) {
	if s.cfg.Store == nil {
		return nil, fmt.Errorf("store not configured")
	}
	dbWorkers, err := s.cfg.Store.ListWorkers(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing workers: %w", err)
	}

	var shared, teamLocal, system []*db.Worker
	for _, w := range dbWorkers {
		switch {
		case w.Source == "system":
			system = append(system, w)
		case w.TeamID != "":
			teamLocal = append(teamLocal, w)
		default:
			shared = append(shared, w)
		}
	}

	// Sort team-local by "team/worker" composite key.
	sortWorkersByTeamKey(teamLocal)

	ordered := append(append(shared, teamLocal...), system...)
	workers := make([]Worker, 0, len(ordered))
	for _, w := range ordered {
		workers = append(workers, dbWorkerToService(w))
	}
	return workers, nil
}

// GetWorker returns a single worker by ID.
func (s *LocalService) GetWorker(ctx context.Context, id string) (Worker, error) {
	if s.cfg.Store == nil {
		return Worker{}, fmt.Errorf("store not configured")
	}
	w, err := s.cfg.Store.GetWorker(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Worker{}, fmt.Errorf("getting worker %s: %w", id, ErrNotFound)
		}
		return Worker{}, fmt.Errorf("getting worker %s: %w", id, err)
	}
	return dbWorkerToService(w), nil
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

// SetCoordinator updates the team so that the named worker is the coordinator.
func (s *LocalService) SetCoordinator(ctx context.Context, teamID string, workerName string) error {
	tv, err := s.GetTeam(ctx, teamID)
	if err != nil {
		return err
	}
	if isServiceReadOnlyTeam(tv) {
		return fmt.Errorf("cannot set coordinator on read-only team %q", tv.Team.Name)
	}

	if err := setCoordinator(tv.Dir(), workerName); err != nil {
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

	s.safeGo(operationID, "promote_team", func() {
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
	})

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

	// Capture available workers for the goroutine.
	listCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	dbWorkers, err := s.cfg.Store.ListWorkers(listCtx)
	cancel()
	if err != nil {
		return "", fmt.Errorf("listing workers for team generation: %w", err)
	}

	uuidVal, err := uuid.NewV4()
	if err != nil {
		return "", fmt.Errorf("generating operation ID: %w", err)
	}
	operationID := uuidVal.String()

	workersCopy := make([]*db.Worker, len(dbWorkers))
	copy(workersCopy, dbWorkers)

	if !s.tryAcquireAsync() {
		return "", fmt.Errorf("too many concurrent operations (max %d)", maxConcurrentOps)
	}

	s.safeGo(operationID, "generate_team", func() {
		defer s.releaseAsync()

		genCtx, genCancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer genCancel()
		teamMD, agentNames, genErr := s.generateTeamContent(genCtx, prompt, workersCopy)
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

		writeErr := s.writeGeneratedTeamFiles(teamMD, agentNames, workersCopy)
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
	})

	return operationID, nil
}

// generateTeamContent calls the LLM to generate a team definition.
func (s *LocalService) generateTeamContent(ctx context.Context, prompt string, workers []*db.Worker) (teamMD string, agentNames []string, err error) {
	var agentList strings.Builder
	for _, a := range workers {
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

	if _, err := mdfmt.ParseBytes([]byte(result.TeamMD), mdfmt.DefTeam); err != nil {
		return "", nil, fmt.Errorf("generated team_md is not a valid team definition: %w", err)
	}

	return result.TeamMD, result.AgentNames, nil
}

// writeGeneratedTeamFiles writes the team directory, team.md, and copies worker files.
func (s *LocalService) writeGeneratedTeamFiles(teamMD string, agentNames []string, allWorkers []*db.Worker) error {
	parsed, err := mdfmt.ParseBytes([]byte(teamMD), mdfmt.DefTeam)
	if err != nil {
		return fmt.Errorf("parsing generated team.md: %w", err)
	}
	teamDef, ok := parsed.(*mdfmt.TeamDef)
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

	// Build name→worker map for fast lookup.
	workerByName := make(map[string]*db.Worker, len(allWorkers))
	for _, w := range allWorkers {
		workerByName[w.Name] = w
	}

	for _, name := range agentNames {
		w, found := workerByName[name]
		if !found {
			slog.Warn("generated team references unknown worker, skipping", "worker", name)
			continue
		}
		if w.SourcePath == "" {
			slog.Warn("generated team worker has no source path, skipping", "worker", name)
			continue
		}
		workerSlug := loader.Slugify(w.Name)
		if workerSlug == "" {
			workerSlug = loader.Slugify(w.ID)
		}
		destPath := filepath.Join(agentsSubDir, workerSlug+".md")
		if err := copyFile(w.SourcePath, destPath); err != nil {
			slog.Warn("failed to copy worker file for generated team", "worker", name, "error", err)
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
	workers := make([]Worker, len(tv.Workers))
	copy(workers, tv.Workers)
	teamDir := tv.Dir()

	if !s.tryAcquireAsync() {
		return "", fmt.Errorf("too many concurrent operations (max %d)", maxConcurrentOps)
	}

	s.safeGo(operationID, "detect_coordinator", func() {
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
		sb.WriteString("Given these workers, which one is best suited to be the team coordinator? Respond with just the worker name, nothing else.\n\nWorkers:\n")
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

		// Match result to a worker name (case-insensitive, trimmed).
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
	})

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

// List returns all currently active worker sessions as snapshots.
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
		WorkerName:     snap.WorkerID,
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

// GetLogs returns the contents of the application log file.
func (s *LocalService) GetLogs(_ context.Context) (string, error) {
	logPath := filepath.Join(s.cfg.ConfigDir, "toasters.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading log file: %w", err)
	}
	return string(data), nil
}

// ListCatalogProviders returns the full provider/model catalog from models.dev.
func (s *LocalService) ListCatalogProviders(ctx context.Context) ([]CatalogProvider, error) {
	if s.cfg.Catalog == nil {
		return nil, nil
	}
	provs, err := s.cfg.Catalog.ProvidersSorted(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing catalog providers: %w", err)
	}
	result := make([]CatalogProvider, 0, len(provs))
	for _, p := range provs {
		cp := CatalogProvider{
			ID:   p.ID,
			Name: p.Name,
			API:  p.API,
			Doc:  p.Doc,
			Env:  p.Env,
		}
		for _, m := range p.Models {
			cp.Models = append(cp.Models, CatalogModel{
				ID:               m.ID,
				Name:             m.Name,
				Family:           m.Family,
				ToolCall:         m.ToolCall,
				Reasoning:        m.Reasoning,
				StructuredOutput: m.StructuredOutput,
				OpenWeights:      m.OpenWeights,
				ContextLimit:     m.ContextLimit,
				OutputLimit:      m.OutputLimit,
				InputCost:        m.InputCost,
				OutputCost:       m.OutputCost,
			})
		}
		result = append(result, cp)
	}
	return result, nil
}

// AddProvider appends a new provider to config.yaml.
func (s *LocalService) AddProvider(_ context.Context, req AddProviderRequest) error {
	if req.ID == "" {
		return fmt.Errorf("provider ID is required")
	}
	if req.Name == "" {
		return fmt.Errorf("provider name is required")
	}
	if req.Type == "" {
		return fmt.Errorf("provider type is required")
	}
	switch req.Type {
	case "openai", "local", "anthropic":
	default:
		return fmt.Errorf("invalid provider type %q (must be openai, local, or anthropic)", req.Type)
	}

	return config.AddProvider(s.cfg.ConfigDir, config.ProviderEntry{
		ID:       req.ID,
		Name:     req.Name,
		Type:     req.Type,
		Endpoint: req.Endpoint,
		APIKey:   req.APIKey,
	})
}

// UpdateProvider overwrites an existing provider YAML file.
func (s *LocalService) UpdateProvider(_ context.Context, req AddProviderRequest) error {
	if req.ID == "" {
		return fmt.Errorf("provider ID is required")
	}
	return config.UpdateProvider(s.cfg.ConfigDir, config.ProviderEntry{
		ID:       req.ID,
		Name:     req.Name,
		Type:     req.Type,
		Endpoint: req.Endpoint,
		APIKey:   req.APIKey,
	})
}

// ListConfiguredProviderIDs returns the IDs of locally configured providers.
func (s *LocalService) ListConfiguredProviderIDs(_ context.Context) ([]string, error) {
	if s.cfg.Loader == nil {
		return nil, nil
	}
	provs := s.cfg.Loader.Providers()
	ids := make([]string, 0, len(provs))
	for _, p := range provs {
		ids = append(ids, p.Key())
	}
	return ids, nil
}

// SetOperatorProvider updates the operator provider ID in config.yaml and
// starts the operator live if a provider with that ID is in the registry.
func (s *LocalService) SetOperatorProvider(_ context.Context, providerID string, model string) error {
	if err := config.SetOperatorProvider(s.cfg.ConfigDir, providerID, model); err != nil {
		return err
	}

	// Update default provider/model so team leads spawned after this change
	// inherit the operator's provider.
	s.cfg.DefaultProvider = providerID
	s.cfg.DefaultModel = model

	// Attempt live activation.
	if s.cfg.Registry == nil {
		return nil
	}
	p, ok := s.cfg.Registry.Get(providerID)
	if !ok {
		slog.Warn("operator provider saved but not in registry yet; restart to activate", "provider", providerID)
		return nil
	}

	return s.startOperator(p, providerID, model)
}

// ListProviderModels returns models from a specific configured provider.
func (s *LocalService) ListProviderModels(ctx context.Context, providerID string) ([]ModelInfo, error) {
	if s.cfg.Registry == nil {
		return nil, fmt.Errorf("no provider registry")
	}
	p, ok := s.cfg.Registry.Get(providerID)
	if !ok {
		return nil, fmt.Errorf("provider %q not found", providerID)
	}
	provModels, err := p.Models(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing models for %q: %w", providerID, err)
	}
	models := make([]ModelInfo, 0, len(provModels))
	for _, m := range provModels {
		models = append(models, providerModelInfoToService(m))
	}
	return models, nil
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

	// Compose the operator system prompt via the prompt engine.
	var systemPrompt string
	if s.cfg.PromptEngine != nil {
		composed, err := s.cfg.PromptEngine.Compose("operator", nil)
		if err != nil {
			slog.Warn("failed to compose operator for live activation", "error", err)
		} else {
			systemPrompt = composed
		}
	}
	if systemPrompt == "" {
		systemPrompt = "You are the Toasters operator."
	}

	textFlush := func(text string) {
		s.BroadcastOperatorText(text, "")
	}
	batcher := newTextBatcher(16*time.Millisecond, textFlush)

	op, err := operator.New(operator.Config{
		Runtime:                s.cfg.Runtime,
		Provider:               p,
		Model:                  model,
		WorkDir:                s.cfg.WorkspaceDir,
		Store:                  s.cfg.Store,
		SystemPrompt:           systemPrompt,
		SessionFile:            filepath.Join(s.cfg.ConfigDir, "sessions", "operator.json"),
		SystemEventBroadcaster: s,
		GraphExecutor:          s.cfg.GraphExecutor,
		Broker:                 s.broker,
		PromptEngine:           s.cfg.PromptEngine,
		DefaultProvider:        s.cfg.DefaultProvider,
		DefaultModel:           s.cfg.DefaultModel,
		OnText: func(text string) {
			batcher.Add(text)
		},
		OnEvent: func(event operator.Event) {
			s.BroadcastOperatorEvent(event)
		},
		OnTurnDone: func(tokensIn, tokensOut, reasoningTokens int) {
			batcher.Flush()
			s.BroadcastOperatorDone(model, tokensIn, tokensOut, reasoningTokens)
		},
		OnPrompt: func(requestID, question string, options []string) {
			s.broadcast(Event{
				Type: EventTypeOperatorPrompt,
				Payload: OperatorPromptPayload{
					RequestID: requestID,
					Question:  question,
					Options:   options,
				},
			})
		},
	})
	if err != nil {
		return fmt.Errorf("creating operator: %w", err)
	}

	// Update service state.
	s.cfg.Operator = op
	s.cfg.OperatorModel = model
	s.cfg.Provider = p

	// Look up endpoint for sidebar display.
	if s.cfg.Loader != nil {
		for _, pc := range s.cfg.Loader.Providers() {
			if pc.Key() == providerID {
				s.cfg.OperatorEndpoint = pc.Endpoint
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
		WorkerID:        t.WorkerID,
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
		WorkerID:  p.WorkerID,
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

func dbWorkerToService(w *db.Worker) Worker {
	var tools, disallowedTools, skills []string
	if len(w.Tools) > 0 {
		_ = json.Unmarshal(w.Tools, &tools)
	}
	if len(w.DisallowedTools) > 0 {
		_ = json.Unmarshal(w.DisallowedTools, &disallowedTools)
	}
	if len(w.Skills) > 0 {
		_ = json.Unmarshal(w.Skills, &skills)
	}
	return Worker{
		ID:              w.ID,
		Name:            w.Name,
		Description:     w.Description,
		Mode:            w.Mode,
		Model:           w.Model,
		Provider:        w.Provider,
		Temperature:     w.Temperature,
		SystemPrompt:    w.SystemPrompt,
		Tools:           tools,
		DisallowedTools: disallowedTools,
		Skills:          skills,
		PermissionMode:  w.PermissionMode,
		MaxTurns:        w.MaxTurns,
		Color:           w.Color,
		Hidden:          w.Hidden,
		Disabled:        w.Disabled,
		Source:          w.Source,
		SourcePath:      w.SourcePath,
		TeamID:          w.TeamID,
		CreatedAt:       w.CreatedAt,
		UpdatedAt:       w.UpdatedAt,
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
		LeadWorker:  t.LeadWorker,
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

func dbWorkerSessionToService(s *db.WorkerSession) WorkerSession {
	return WorkerSession{
		ID:        s.ID,
		WorkerID:  s.WorkerID,
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
		WorkerID:  snap.WorkerID,
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
		tv.IsReadOnly = isServiceReadOnlyTeam(tv)
		// Defense-in-depth: IsSystem is always false here since system teams
		// (Source == "system") are filtered above. The computation is kept as
		// a safety net in case ListTeams is ever called without the filter, or
		// if a non-"system" source team is placed under the system directory.
		tv.IsSystem = isServiceSystemTeam(tv, s.cfg.ConfigDir)

		teamWorkers, err := s.cfg.Store.ListTeamWorkers(ctx, team.ID)
		if err != nil {
			slog.Warn("failed to list team workers", "team", team.Name, "error", err)
			views = append(views, tv)
			continue
		}

		for _, tw := range teamWorkers {
			worker, err := s.cfg.Store.GetWorker(ctx, tw.WorkerID)
			if err != nil {
				slog.Warn("failed to get worker", "workerID", tw.WorkerID, "error", err)
				continue
			}
			svcWorker := dbWorkerToService(worker)
			if tw.Role == "lead" {
				tv.Coordinator = &svcWorker
			} else {
				tv.Workers = append(tv.Workers, svcWorker)
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
		filepath.Join(home, ".opencode", "agents"),
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

	if err := os.MkdirAll(targetAgentsDir, 0o755); err != nil {
		return fmt.Errorf("creating target directory %s: %w", targetAgentsDir, err)
	}

	var agentNames []string
	for _, path := range matches {
		stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		safeFilename := loader.Slugify(stem)
		if safeFilename == "" {
			slog.Warn("skipping agent with unsluggable filename", "stem", stem)
			continue
		}
		destPath := filepath.Join(targetAgentsDir, safeFilename+".md")
		if err := copyFile(path, destPath); err != nil {
			_ = os.RemoveAll(targetDir)
			return fmt.Errorf("copying agent file %s: %w", path, err)
		}
		agentNames = append(agentNames, stem)
	}
	if len(agentNames) == 0 {
		_ = os.RemoveAll(targetDir)
		return fmt.Errorf("no agent files could be copied from %s", agentsSourceDir)
	}

	lead := ""
	if tv.Coordinator != nil {
		lead = tv.Coordinator.Name
	}

	source := filepath.Base(filepath.Dir(tv.Dir())) + "/" + filepath.Base(tv.Dir())

	teamDef := &mdfmt.TeamDef{
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

	slog.Info("promoted read-only auto-team to managed team", "team", tv.Team.Name, "target", targetDir, "agents", len(agentNames))
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

	// Read file contents before removing the symlink.
	type agentContent struct {
		filename string
		data     []byte
		stem     string
	}
	var contents []agentContent
	for _, path := range matches {
		stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("skipping unreadable agent during promotion", "path", path, "error", err)
			continue
		}
		contents = append(contents, agentContent{filename: filepath.Base(path), data: data, stem: stem})
	}
	if len(contents) == 0 {
		return fmt.Errorf("no readable agent files found in %s", agentsSymlink)
	}

	if err := os.Remove(agentsSymlink); err != nil {
		return fmt.Errorf("removing agents symlink %s: %w", agentsSymlink, err)
	}
	if err := os.MkdirAll(agentsSymlink, 0o755); err != nil {
		return fmt.Errorf("creating agents directory %s: %w", agentsSymlink, err)
	}

	var agentNames []string
	for _, ac := range contents {
		safeFilename := loader.Slugify(ac.stem)
		if safeFilename == "" {
			slog.Warn("skipping agent with unsluggable filename", "stem", ac.stem)
			continue
		}
		agentPath := filepath.Join(agentsSymlink, safeFilename+".md")
		if err := os.WriteFile(agentPath, ac.data, 0o644); err != nil {
			_ = os.RemoveAll(agentsSymlink)
			return fmt.Errorf("writing agent file %s: %w", agentPath, err)
		}
		agentNames = append(agentNames, ac.stem)
	}

	lead := ""
	if tv.Coordinator != nil {
		lead = tv.Coordinator.Name
	}

	teamDef := &mdfmt.TeamDef{
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

	slog.Info("promoted bootstrap auto-team in-place", "team", tv.Team.Name, "dir", tv.Dir(), "agents", len(agentNames))
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
		agentFiles = append(agentFiles, agentFile{path: p, name: stem})
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
	teamDef, parseErr := mdfmt.ParseTeam(teamMDPath)
	if parseErr != nil {
		teamDef = &mdfmt.TeamDef{Name: filepath.Base(teamDir)}
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

// writeTeamFile writes a TeamDef as a toasters-format .md file.
func writeTeamFile(path string, def *mdfmt.TeamDef) error {
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
func writeTeamFileTo(path string, def *mdfmt.TeamDef) error {
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
	if parsed, err := mdfmt.ParseBytes([]byte(content), mdfmt.DefSkill); err == nil {
		if skillDef, ok := parsed.(*mdfmt.SkillDef); ok && skillDef.Name != "" {
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

// sortWorkersByTeamKey sorts workers by the composite "team/worker" key.
func sortWorkersByTeamKey(workers []*db.Worker) {
	for i := 1; i < len(workers); i++ {
		for j := i; j > 0; j-- {
			ka := workers[j-1].TeamID + "/" + workers[j-1].Name
			kb := workers[j].TeamID + "/" + workers[j].Name
			if ka > kb {
				workers[j-1], workers[j] = workers[j], workers[j-1]
			} else {
				break
			}
		}
	}
}
