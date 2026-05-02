// Package service provides the in-process implementation of the Service interface.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/graphexec"
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
	maxMessageLen  = 102400 // 100KB — maximum user message size
	maxPromptLen   = 51200  // 50KB — maximum generation prompt size
	maxResponseLen = 51200  // 50KB — maximum prompt/blocker response size
)

// maxConcurrentOps bounds the number of concurrent async operations (generate,
// promote, detect) that can run simultaneously.
const maxConcurrentOps = 5

// maxHistoryEntries bounds the conversation history kept for reconnect hydration.
const maxHistoryEntries = 1000

// LocalConfig holds the dependencies for LocalService.
type LocalConfig struct {
	// AppConfig is the loaded application config, used for serving and
	// mutating user-editable settings (see GetSettings/UpdateSettings).
	// Optional — if nil, the settings endpoints return defaults and
	// UpdateSettings is a no-op that returns an error.
	AppConfig        *config.Config
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
	OperatorModel    string                     // for OperatorStatus.ModelName
	OperatorEndpoint string                     // for OperatorStatus.Endpoint (LLM provider URL)
	StartTime        time.Time                  // for Health().Uptime
	Catalog          CatalogSource              // optional models.dev catalog; nil disables ListCatalogProviders
	Registry         *provider.Registry         // provider registry for live operator activation
	PromptEngine     *prompt.Engine             // optional; for role-based prompt composition
	GraphExecutor    operator.GraphTaskExecutor // optional; rhizome graph-based task execution
	GraphCatalog     operator.GraphCatalog      // optional; backs query_graphs on the live-activated operator
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
				TaskID:  payload.TaskID,
				JobID:   payload.JobID,
				GraphID: payload.GraphID,
				Title:   payload.Title,
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
				GraphID:         payload.GraphID,
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
				TaskID:  payload.TaskID,
				JobID:   payload.JobID,
				GraphID: payload.GraphID,
				Error:   payload.Error,
			},
		})

	case operator.EventJobComplete:
		payload, ok := ev.Payload.(operator.JobCompletePayload)
		if !ok {
			return
		}
		s.broadcast(Event{
			Type:    EventTypeJobCompleted,
			Payload: s.buildJobCompletedPayload(payload),
		})
	}
}

// buildJobCompletedPayload assembles the rich completion payload by pulling
// together the job row, its tasks, all worker sessions for the job, and a
// listing of files in the workspace whose mtime falls inside the job's
// lifetime. Errors at each stage degrade the payload but never block
// emission — a thin payload still beats no payload for the UI.
func (s *LocalService) buildJobCompletedPayload(payload operator.JobCompletePayload) JobCompletedPayload {
	out := JobCompletedPayload{
		JobID:   payload.JobID,
		Title:   payload.Title,
		Summary: payload.Summary,
		Status:  JobStatusCompleted,
		EndedAt: time.Now(),
	}

	if s.cfg.Store == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if job, err := s.cfg.Store.GetJob(ctx, payload.JobID); err == nil && job != nil {
		out.Workspace = job.WorkspaceDir
		out.StartedAt = job.CreatedAt
		// out.Title comes from the operator payload, which is authoritative
		// for the operator-perceived label; only fall back to the DB when
		// the payload's title is empty.
		if out.Title == "" {
			out.Title = job.Title
		}
	} else if err != nil {
		slog.Warn("job-completed payload: GetJob failed", "job_id", payload.JobID, "error", err)
	}

	if tasks, err := s.cfg.Store.ListTasksForJob(ctx, payload.JobID); err == nil {
		out.TasksTotal = len(tasks)
		for _, t := range tasks {
			switch t.Status {
			case db.TaskStatusCompleted:
				out.TasksCompleted++
			case db.TaskStatusFailed:
				out.TasksFailed++
			}
		}
		// Promote the job's effective status: if any task failed the run
		// wasn't a clean win, even though EventJobComplete fires for any
		// terminal state.
		if out.TasksFailed > 0 {
			out.Status = JobStatusFailed
		}
	} else {
		slog.Warn("job-completed payload: ListTasksForJob failed", "job_id", payload.JobID, "error", err)
	}

	if sessions, err := s.cfg.Store.ListSessionsForJob(ctx, payload.JobID); err == nil {
		for _, sess := range sessions {
			out.TokensIn += sess.TokensIn
			out.TokensOut += sess.TokensOut
			if sess.CostUSD != nil {
				out.CostUSD += *sess.CostUSD
			}
		}
		// Diagnostic: many local-inference servers (LM Studio in
		// particular older builds) ship without `stream_options.include_usage`
		// support, so worker_sessions can land at tokens=0 even when the
		// job clearly produced text. Log the count + aggregate so the
		// user can correlate against `sqlite3 toasters.db "SELECT
		// id,tokens_in,tokens_out FROM worker_sessions WHERE job_id=?"`.
		slog.Debug("job-completed payload: aggregated session tokens",
			"job_id", payload.JobID,
			"sessions", len(sessions),
			"tokens_in", out.TokensIn,
			"tokens_out", out.TokensOut)
	} else {
		slog.Warn("job-completed payload: ListSessionsForJob failed", "job_id", payload.JobID, "error", err)
	}

	if out.Workspace != "" && !out.StartedAt.IsZero() {
		files, extra := walkFilesTouched(out.Workspace, out.StartedAt, out.EndedAt)
		out.FilesTouched = files
		out.FilesTouchedExtra = extra
	}

	return out
}

// walkFilesTouched returns the files inside dir whose mtime falls within
// [startedAt, endedAt+grace]. A small forward grace window covers the
// race between the last file write and the completion event firing. The
// listing is bounded so an over-eager scan in a huge workspace can't
// stall the broadcast loop or blow the SSE event size.
func walkFilesTouched(dir string, startedAt, endedAt time.Time) ([]FileTouch, int) {
	const (
		maxFiles  = 200
		graceWin  = 2 * time.Second
		hardLimit = 5000 // walk-time guard: stop entirely after this many entries
	)
	if dir == "" {
		return nil, 0
	}
	endWindow := endedAt.Add(graceWin)
	var (
		out   []FileTouch
		extra int
		seen  int
	)
	walkErr := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Ignore individual file errors (permission denied, broken
			// symlinks); they shouldn't poison the whole listing.
			return nil
		}
		if d.IsDir() {
			// Skip the workspace-internal cache dirs that almost always
			// pollute file-touch lists with uninteresting churn.
			name := d.Name()
			if path != dir && (name == ".git" || name == "node_modules" || name == ".toasters") {
				return filepath.SkipDir
			}
			return nil
		}
		seen++
		if seen > hardLimit {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		mtime := info.ModTime()
		if mtime.Before(startedAt) || mtime.After(endWindow) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			rel = path
		}
		if len(out) >= maxFiles {
			extra++
			return nil
		}
		out = append(out, FileTouch{
			Path:  rel,
			Size:  info.Size(),
			IsNew: !mtime.Before(startedAt) && info.ModTime().Equal(infoBirthFallback(info)),
		})
		return nil
	})
	if walkErr != nil {
		slog.Debug("walkFilesTouched: WalkDir returned error", "dir", dir, "error", walkErr)
	}
	// Stable ordering: alphabetical by relative path so the displayed
	// list doesn't reshuffle every render.
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, extra
}

// infoBirthFallback returns the file's modification time as a stand-in for
// a creation timestamp on platforms that don't expose btime. The TUI uses
// IsNew only as a hint, so the heuristic doesn't need to be precise.
func infoBirthFallback(info os.FileInfo) time.Time {
	return info.ModTime()
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
// new job is persisted. Also kicks off coarse-decompose automatically when
// the job has a description and the graph executor is wired.
func (s *LocalService) BroadcastJobCreated(jobID, title, description string) {
	s.broadcast(Event{
		Type: EventTypeJobCreated,
		Payload: JobCreatedPayload{
			JobID:       jobID,
			Title:       title,
			Description: description,
		},
	})
	if strings.TrimSpace(description) != "" {
		s.dispatchCoarseDecompose(jobID, title, description)
	}
}

// BroadcastTaskCreated broadcasts a task.created event. Implements
// operator.SystemEventBroadcaster — called by SystemTools.createTask after the
// new task is persisted. When the new task has no graph_id set, the service
// automatically dispatches fine-decompose to pick one.
func (s *LocalService) BroadcastTaskCreated(taskID, jobID, title, graphID string) {
	s.broadcast(Event{
		Type: EventTypeTaskCreated,
		Payload: TaskCreatedPayload{
			TaskID:  taskID,
			JobID:   jobID,
			Title:   title,
			GraphID: graphID,
		},
	})
	if graphID == "" {
		s.dispatchFineDecomposeForTask(taskID)
	}
}

// dispatchFineDecomposeForTask resolves the parent task and its job
// before handing off to dispatchFineDecompose. Separated out so
// BroadcastTaskCreated stays terse and only pays DB cost when the task
// actually needs decomposition.
//
// Defers fine-decompose for tasks with unmet predecessors. Fine-decompose
// inputs include the task title plus job context — running it before
// predecessors complete means the decomposer can't incorporate their
// summaries when picking a graph. The retro-trigger in BroadcastTaskCompleted
// re-runs this for every task as it becomes ready.
func (s *LocalService) dispatchFineDecomposeForTask(taskID string) {
	if s.cfg.Store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	task, err := s.cfg.Store.GetTask(ctx, taskID)
	if err != nil {
		slog.Warn("fine-decompose lookup failed; task missing",
			"task_id", taskID, "error", err)
		return
	}
	if !s.taskIsReady(ctx, task) {
		slog.Info("fine-decompose deferred; predecessors incomplete",
			"task_id", taskID, "job_id", task.JobID)
		return
	}
	job, err := s.cfg.Store.GetJob(ctx, task.JobID)
	if err != nil {
		slog.Warn("fine-decompose lookup failed; job missing",
			"task_id", taskID, "error", err)
		return
	}
	s.dispatchFineDecompose(task, job)
}

// dispatchFineDecomposeForReadyTasks scans the job's ready tasks and
// kicks off fine-decompose for any that don't yet have a graph_id. Called
// after a real task completes so newly-unblocked tasks pick a graph at
// the moment their dependencies' summaries are available.
func (s *LocalService) dispatchFineDecomposeForReadyTasks(jobID string) {
	if s.cfg.Store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	ready, err := s.cfg.Store.GetReadyTasks(ctx, jobID)
	if err != nil {
		slog.Warn("failed to list ready tasks after completion",
			"job_id", jobID, "error", err)
		return
	}
	for _, t := range ready {
		if t.GraphID != "" {
			continue
		}
		s.dispatchFineDecomposeForTask(t.ID)
	}
}

// taskIsReady reports whether a task's predecessors are all complete.
// Wraps GetReadyTasks(jobID) and scans the result rather than adding a
// per-task store query.
func (s *LocalService) taskIsReady(ctx context.Context, task *db.Task) bool {
	ready, err := s.cfg.Store.GetReadyTasks(ctx, task.JobID)
	if err != nil {
		slog.Warn("readiness check failed; assuming task is ready",
			"task_id", task.ID, "error", err)
		return true
	}
	for _, r := range ready {
		if r.ID == task.ID {
			return true
		}
	}
	return false
}

// BroadcastTaskAssigned broadcasts a task.assigned event. Implements
// operator.SystemEventBroadcaster — called by SystemTools.assignTask after a
// task has been pre-assigned or dispatched to a graph.
func (s *LocalService) BroadcastTaskAssigned(taskID, jobID, graphID, title string) {
	s.broadcast(Event{
		Type: EventTypeTaskAssigned,
		Payload: TaskAssignedPayload{
			TaskID:  taskID,
			JobID:   jobID,
			GraphID: graphID,
			Title:   title,
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

// BroadcastSessionPrompt emits a session.prompt event so the TUI can
// populate the system prompt and initial message on an existing session
// slot. Graph nodes call this once their prompt has been composed and
// before the LLM starts streaming.
func (s *LocalService) BroadcastSessionPrompt(sessionID, systemPrompt, initialMessage string) {
	if sessionID == "" {
		return
	}
	s.broadcast(Event{
		Type:      EventTypeSessionPrompt,
		SessionID: sessionID,
		Payload: SessionPromptPayload{
			SessionID:      sessionID,
			SystemPrompt:   systemPrompt,
			InitialMessage: initialMessage,
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

// BroadcastSessionToolCall emits a session.tool_call event for a graph
// node, reusing the same event type the runtime emits for worker
// sessions so the TUI renders graph activity identically.
func (s *LocalService) BroadcastSessionToolCall(sessionID, callID, name string, args json.RawMessage) {
	if name == "" {
		return
	}
	s.broadcast(Event{
		Type:      EventTypeSessionToolCall,
		SessionID: sessionID,
		Payload: SessionToolCallPayload{
			ToolCall: ToolCall{
				ID:        callID,
				Name:      name,
				Arguments: args,
			},
		},
	})
}

// BroadcastSessionReasoning emits a session.reasoning event for a
// graph node. Routes through its own event type so the TUI can style
// reasoning differently from plain output text.
func (s *LocalService) BroadcastSessionReasoning(sessionID, text string) {
	if text == "" {
		return
	}
	s.broadcast(Event{
		Type:      EventTypeSessionReasoning,
		SessionID: sessionID,
		Payload:   SessionReasoningPayload{Text: text},
	})
}

// BroadcastSessionToolResult emits a session.tool_result event for a
// graph node. CallID may be empty — mycelium's tool-result events do
// not carry the originating call id, and the TUI tolerates an empty
// CallID (it only uses the string for optional pairing).
func (s *LocalService) BroadcastSessionToolResult(sessionID, callID, name, result, errMsg string) {
	s.broadcast(Event{
		Type:      EventTypeSessionToolResult,
		SessionID: sessionID,
		Payload: SessionToolResultPayload{
			Result: ToolCallResult{
				CallID: callID,
				Name:   name,
				Result: result,
				Error:  errMsg,
			},
		},
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
// after a graph finishes successfully. When the completed graph is a
// decomposition graph, the service consumes the output itself and creates
// follow-up tasks in the database instead of forwarding to the operator.
func (s *LocalService) BroadcastTaskCompleted(jobID, taskID, graphID, summary string, output json.RawMessage, hasNextTask bool) {
	if s.handleDecompositionCompleted(jobID, taskID, graphID, output) {
		return
	}
	// Fan out fine-decompose to tasks that became ready due to this
	// completion. Tasks already pre-assigned to a graph (i.e. fine-decompose
	// already ran) are advanced by the operator's assignNextTask path.
	s.dispatchFineDecomposeForReadyTasks(jobID)
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
			GraphID:     graphID,
			Summary:     summary,
			HasNextTask: hasNextTask,
		},
	}); err != nil {
		slog.Warn("failed to forward task_completed to operator",
			"task_id", taskID, "job_id", jobID, "error", err)
	}
}

// BroadcastTaskFailed signals task failure to the operator's event loop so it
// can consult the blocker-handler.
func (s *LocalService) BroadcastTaskFailed(jobID, taskID, graphID, errMsg string) {
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
			TaskID:  taskID,
			JobID:   jobID,
			GraphID: graphID,
			Error:   errMsg,
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

// handleDecompositionCompleted consumes task-completion events for the
// two decomposition graphs. Returns true when it fully handled the event
// (caller should not forward to the operator). Returns false when the
// completed graph is not a decomposition graph — the caller continues
// with its normal forwarding path.
func (s *LocalService) handleDecompositionCompleted(jobID, taskID, graphID string, output json.RawMessage) bool {
	if !isDecompositionGraph(graphID) {
		return false
	}
	slog.Info("decomposition graph completed",
		"graph_id", graphID, "job_id", jobID, "task_id", taskID, "output_bytes", len(output))
	s.consumeDecompositionOutput(graphID, taskID, output)
	return true
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

	if _, err := mdfmt.ParseBytes([]byte(content)); err != nil {
		return "", fmt.Errorf("generated content is not a valid skill definition: %w", err)
	}

	return content, nil
}

// ---------------------------------------------------------------------------
// DefinitionService — Workers
// ---------------------------------------------------------------------------

// ListGraphs returns all loaded graph definitions, ordered by id.
func (s *LocalService) ListGraphs(_ context.Context) ([]GraphDefinition, error) {
	if s.cfg.GraphCatalog == nil {
		return nil, nil
	}
	defs := s.cfg.GraphCatalog.Graphs()
	out := make([]GraphDefinition, 0, len(defs))
	for _, d := range defs {
		out = append(out, graphexecDefinitionToService(d))
	}
	return out, nil
}

// GetGraph returns a single graph definition by id.
func (s *LocalService) GetGraph(_ context.Context, id string) (GraphDefinition, error) {
	if s.cfg.GraphCatalog == nil {
		return GraphDefinition{}, fmt.Errorf("getting graph %s: %w", id, ErrNotFound)
	}
	for _, d := range s.cfg.GraphCatalog.Graphs() {
		if d.ID == id {
			return graphexecDefinitionToService(d), nil
		}
	}
	return GraphDefinition{}, fmt.Errorf("getting graph %s: %w", id, ErrNotFound)
}

// graphexecDefinitionToService converts a graphexec.Definition (the YAML-loaded
// internal shape) to a service.GraphDefinition (the TUI-facing DTO). The edge
// conversion expands routers into one conditional edge per branch so renderers
// can draw each branch distinctly.
func graphexecDefinitionToService(d *graphexec.Definition) GraphDefinition {
	out := GraphDefinition{
		ID:          d.ID,
		Name:        d.Name,
		Description: d.Description,
		Tags:        append([]string(nil), d.Tags...),
		Entry:       d.Entry,
		Exit:        d.Exit,
	}
	for _, n := range d.Nodes {
		out.Nodes = append(out.Nodes, n.ID)
	}
	for _, e := range d.Edges {
		if e.Router == nil {
			out.Edges = append(out.Edges, GraphEdge{
				From: e.From,
				To:   mapEndSentinel(e.To),
				Kind: GraphEdgeStatic,
			})
			continue
		}
		for _, b := range e.Router.Branches {
			out.Edges = append(out.Edges, GraphEdge{
				From:  e.From,
				To:    mapEndSentinel(b.To),
				Kind:  GraphEdgeConditional,
				Label: fmt.Sprintf("%v", b.When),
			})
		}
		if e.Router.Default != "" {
			out.Edges = append(out.Edges, GraphEdge{
				From:  e.From,
				To:    mapEndSentinel(e.Router.Default),
				Kind:  GraphEdgeConditional,
				Label: "default",
			})
		}
	}
	return out
}

// mapEndSentinel maps the YAML "end" sentinel to the empty string so renderers
// treat it as a terminal edge without depending on graphexec's constant.
func mapEndSentinel(to string) string {
	if to == graphexec.EndNode {
		return ""
	}
	return to
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
	case db.JobStatusActive, db.JobStatusPending, db.JobStatusSettingUp:
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

// GetSettings returns the current user-editable runtime settings. Values are
// sourced from the in-memory config; if no config is wired (tests), sensible
// defaults are returned.
func (s *LocalService) GetSettings(_ context.Context) (Settings, error) {
	if s.cfg.AppConfig == nil {
		return Settings{
			CoarseGranularity:          config.ValidGranularity("coarse", ""),
			FineGranularity:            config.ValidGranularity("fine", ""),
			WorkerThinkingEnabled:      false,
			WorkerTemperature:          0.1,
			ShowJobsPanelByDefault:     false,
			ShowOperatorPanelByDefault: true,
		}, nil
	}
	return Settings{
		CoarseGranularity:          config.ValidGranularity("coarse", s.cfg.AppConfig.CoarseGranularity),
		FineGranularity:            config.ValidGranularity("fine", s.cfg.AppConfig.FineGranularity),
		WorkerThinkingEnabled:      s.cfg.AppConfig.WorkerThinkingEnabled,
		WorkerTemperature:          s.cfg.AppConfig.WorkerTemperature,
		ShowJobsPanelByDefault:     s.cfg.AppConfig.ShowJobsPanelByDefault,
		ShowOperatorPanelByDefault: s.cfg.AppConfig.ShowOperatorPanelByDefault,
	}, nil
}

// UpdateSettings validates, persists, and applies the given settings.
// Persistence writes to config.yaml in place; applying refreshes the prompt
// engine so new worker runs pick up the change immediately. Every granularity
// lever is validated before any write, so a bad value on one field leaves
// the rest untouched.
func (s *LocalService) UpdateSettings(_ context.Context, next Settings) error {
	if s.cfg.AppConfig == nil {
		return fmt.Errorf("settings unavailable: no app config loaded")
	}
	type lever struct {
		kind     string // "coarse" or "fine"
		yamlKey  string
		incoming string
		set      func(string)
	}
	levers := []lever{
		{
			kind:     "coarse",
			yamlKey:  "coarse_granularity",
			incoming: next.CoarseGranularity,
			set:      func(v string) { s.cfg.AppConfig.CoarseGranularity = v },
		},
		{
			kind:     "fine",
			yamlKey:  "fine_granularity",
			incoming: next.FineGranularity,
			set:      func(v string) { s.cfg.AppConfig.FineGranularity = v },
		},
	}

	// Validate first, write second — so an invalid field doesn't leave
	// config.yaml half-updated.
	for _, l := range levers {
		if normalized := config.ValidGranularity(l.kind, l.incoming); normalized != l.incoming {
			return fmt.Errorf("invalid %s %q", l.yamlKey, l.incoming)
		}
	}

	for _, l := range levers {
		if err := config.SetTopLevelScalar(s.cfg.ConfigDir, l.yamlKey, l.incoming); err != nil {
			return fmt.Errorf("persisting %s: %w", l.yamlKey, err)
		}
		l.set(l.incoming)
		if s.cfg.PromptEngine != nil {
			if err := prompt.ApplyGranularity(s.cfg.PromptEngine, l.kind, l.incoming); err != nil {
				slog.Warn("failed to refresh granularity instruction", "kind", l.kind, "error", err)
			}
		}
	}

	// Worker defaults: validate and persist as their native YAML types so
	// viper round-trips them as bool/float on next load.
	if next.WorkerTemperature < 0 || next.WorkerTemperature > 2 {
		return fmt.Errorf("invalid worker_temperature %v (must be in [0, 2])", next.WorkerTemperature)
	}
	if err := config.SetTopLevelValue(s.cfg.ConfigDir, "worker_thinking_enabled", next.WorkerThinkingEnabled); err != nil {
		return fmt.Errorf("persisting worker_thinking_enabled: %w", err)
	}
	if err := config.SetTopLevelValue(s.cfg.ConfigDir, "worker_temperature", next.WorkerTemperature); err != nil {
		return fmt.Errorf("persisting worker_temperature: %w", err)
	}
	s.cfg.AppConfig.WorkerThinkingEnabled = next.WorkerThinkingEnabled
	s.cfg.AppConfig.WorkerTemperature = next.WorkerTemperature
	if applier, ok := s.cfg.GraphExecutor.(workerDefaultsApplier); ok {
		applier.SetWorkerDefaults(next.WorkerThinkingEnabled, next.WorkerTemperature)
	}

	// Panel visibility defaults: pure UI prefs, no live engine to refresh.
	if err := config.SetTopLevelValue(s.cfg.ConfigDir, "show_jobs_panel_by_default", next.ShowJobsPanelByDefault); err != nil {
		return fmt.Errorf("persisting show_jobs_panel_by_default: %w", err)
	}
	if err := config.SetTopLevelValue(s.cfg.ConfigDir, "show_operator_panel_by_default", next.ShowOperatorPanelByDefault); err != nil {
		return fmt.Errorf("persisting show_operator_panel_by_default: %w", err)
	}
	s.cfg.AppConfig.ShowJobsPanelByDefault = next.ShowJobsPanelByDefault
	s.cfg.AppConfig.ShowOperatorPanelByDefault = next.ShowOperatorPanelByDefault

	return nil
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

	textFlush := func(text string) {
		s.BroadcastOperatorText(text, "")
	}
	reasoningFlush := func(text string) {
		s.BroadcastOperatorText("", text)
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
		GraphExecutor:          s.cfg.GraphExecutor,
		GraphCatalog:           s.cfg.GraphCatalog,
		Broker:                 s.broker,
		PromptEngine:           s.cfg.PromptEngine,
		DefaultProvider:        s.cfg.DefaultProvider,
		DefaultModel:           s.cfg.DefaultModel,
		OnText: func(text string) {
			batcher.Add(text)
		},
		OnReasoning: func(text string) {
			reasoningBatcher.Add(text)
		},
		OnEvent: func(event operator.Event) {
			s.BroadcastOperatorEvent(event)
		},
		OnTurnDone: func(tokensIn, tokensOut, reasoningTokens int) {
			reasoningBatcher.Flush()
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
		GraphID:         t.GraphID,
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
	// State is a TUI affordance indicating the loaded/preferred model. The
	// upstream mycelium ModelInfo no longer carries it; until a replacement
	// signal is wired through, this field is left empty. The TUI degrades
	// to showing the first model rather than the loaded one.
	return ModelInfo{
		ID:                  m.ID,
		Name:                m.Name,
		Provider:            m.Provider,
		MaxContextLength:    m.MaxContextLength,
		LoadedContextLength: m.LoadedContextLength,
	}
}

// writeGeneratedSkillFile writes LLM-generated skill content to the user skills directory.
func (s *LocalService) writeGeneratedSkillFile(content string) (string, error) {
	skillsDir := filepath.Join(s.cfg.ConfigDir, "user", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return "", fmt.Errorf("creating skills dir: %w", err)
	}

	slug := "generated-skill"
	if skillDef, err := mdfmt.ParseBytes([]byte(content)); err == nil && skillDef.Name != "" {
		nameSlug := loader.Slugify(skillDef.Name)
		if nameSlug != "" {
			slug = nameSlug
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
