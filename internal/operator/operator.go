// Package operator implements the operator event loop for coordinating
// LLM-powered worker work. The operator maintains a long-lived conversation
// with an LLM, receives typed events on a buffered channel, and dispatches
// them — forwarding user messages to the LLM and handling routine events
// mechanically.
package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/graphexec"
	"github.com/jefflinse/toasters/internal/hitl"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

const (
	eventChSize = 256
	maxMessages = 200

	// defaultAskUserTimeout is how long ask_user waits for an answer before
	// returning a no-response message. Long enough that an attended user has
	// every chance to answer; short enough that an unattended overnight run
	// stalls for minutes, not until someone wakes up.
	defaultAskUserTimeout = 10 * time.Minute
)

// Operator manages the event loop and long-lived operator LLM session.
type Operator struct {
	rt          *runtime.Runtime
	prov        provider.Provider
	model       string
	tools       *operatorTools
	store       db.Store
	eventCh     chan Event
	workDir     string
	sessionFile string // path for persisting the operator conversation (may be empty)

	// Long-lived conversation state. Protected by mu for concurrent access
	// from the event loop goroutine and external callers (e.g. MessageCount).
	mu           sync.Mutex
	systemPrompt string
	messages     []provider.Message
	provTools    []provider.Tool
	// turnID is the service-assigned ID of the user turn currently being
	// processed; empty between turns and for system-initiated turns. Threaded
	// into the OnText/OnReasoning/OnTurnDone callbacks so the service can
	// distinguish user turns from internal ones.
	turnID string

	// broker coordinates ask_user prompts. Shared with graph nodes —
	// responses come back via service.LocalService.RespondToPrompt, which
	// delegates to broker.Respond, routing to whichever path is waiting.
	broker *hitl.Broker

	// askUserTimeout bounds how long promptUser blocks waiting for an
	// answer. ask_user runs synchronously on the event-loop goroutine, so
	// without a deadline one unanswered prompt freezes all event processing
	// (task completions, failures, new messages) for the life of the
	// process — fatal for unattended operation.
	askUserTimeout time.Duration

	// Callbacks — set at construction time via Config, immutable after New().
	onText      func(turnID, text string)                                            // called with streamed text from the operator LLM
	onReasoning func(turnID, text string)                                            // called with streamed reasoning chunks; optional
	onEvent     func(event Event)                                                    // called when the event loop processes an event
	onTurnDone  func(turnID string, tokensIn, tokensOut, reasoningTokens int)        // called when the operator finishes processing a turn
	onPrompt    func(requestID string, questions []graphexec.PromptQuestion)         // called when the operator calls ask_user
	onResolve   func(requestID string)                                               // called when an ask_user request finishes (answered or cancelled)
	onToolCall  func(name string, args json.RawMessage, result string, isError bool) // called after each operator tool executes
}

// Config holds configuration for creating an Operator.
type Config struct {
	Runtime                *runtime.Runtime
	Provider               provider.Provider
	Model                  string
	WorkDir                string
	SystemPrompt           string // required; system prompt for the operator LLM session
	Store                  db.Store
	SystemEventBroadcaster SystemEventBroadcaster // optional; for broadcasting service events from system tools
	GraphExecutor          GraphTaskExecutor      // required for task execution — tasks dispatch through the graph engine
	GraphCatalog           GraphCatalog           // optional; backs the query_graphs system tool
	Broker                 *hitl.Broker           // required for ask_user; shared with the graph executor so responses route to whichever path is waiting
	PromptEngine           *prompt.Engine         // prompt engine for role-based prompt composition
	DefaultProvider        string                 // default provider for system workers
	DefaultModel           string                 // default model for system workers
	// OnText / OnReasoning are called with streamed text and reasoning
	// chunks from the operator LLM. turnID is the user turn the text belongs
	// to (from UserMessagePayload.TurnID); empty for system-initiated turns.
	OnText      func(turnID, text string)
	OnReasoning func(turnID, text string)
	OnEvent     func(event Event) // called when the event loop processes an event
	// OnTurnDone is called when the operator finishes processing a turn.
	// turnID matches the OnText/OnReasoning calls of the same turn (empty
	// for system-initiated turns). tokensIn / tokensOut / reasoningTokens
	// are the totals across all LLM round-trips that occurred during the
	// turn (the operator may make several when tool calls are involved).
	// reasoningTokens is 0 for providers that don't surface them.
	OnTurnDone  func(turnID string, tokensIn, tokensOut, reasoningTokens int)
	OnPrompt    func(requestID string, questions []graphexec.PromptQuestion)         // called when the operator calls ask_user
	OnResolve   func(requestID string)                                               // called when an ask_user request finishes (answered or cancelled)
	OnToolCall  func(name string, args json.RawMessage, result string, isError bool) // called after each operator tool executes
	SessionFile string                                                               // path to persist the operator conversation (e.g. ~/.config/toasters/sessions/operator.json)
	// LifetimeCtx is the service-level lifetime context, threaded into
	// SystemTools so detached graph dispatch goroutines are cancelled on
	// service Shutdown rather than living forever.
	LifetimeCtx context.Context
	// AskUserTimeout bounds how long an ask_user prompt waits for an answer
	// before resolving with a no-response message so the event loop can
	// keep processing. Zero means defaultAskUserTimeout.
	AskUserTimeout time.Duration
}

// New creates a new Operator. Call Start to begin processing events.
func New(cfg Config) (*Operator, error) {
	if cfg.SystemPrompt == "" {
		return nil, fmt.Errorf("operator: SystemPrompt is required")
	}

	// Create SystemTools for system workers to use. The event channel is the
	// operator's own channel so system worker actions (e.g. assign_task) flow
	// back through the operator event loop.
	eventCh := make(chan Event, eventChSize)
	var systemTools *SystemTools
	if cfg.Store != nil {
		systemTools = NewSystemTools(SystemToolsConfig{
			Store:           cfg.Store,
			PromptEngine:    cfg.PromptEngine,
			DefaultProvider: cfg.DefaultProvider,
			DefaultModel:    cfg.DefaultModel,
			EventCh:         eventCh,
			WorkDir:         cfg.WorkDir,
			Broadcaster:     cfg.SystemEventBroadcaster,
			GraphExecutor:   cfg.GraphExecutor,
			GraphCatalog:    cfg.GraphCatalog,
			LifetimeCtx:     cfg.LifetimeCtx,
		})
	}

	tools := newOperatorTools(cfg.Runtime, cfg.PromptEngine, cfg.DefaultProvider, cfg.DefaultModel, cfg.Store, systemTools, cfg.WorkDir)
	provTools := operatorToolsToProviderTools(tools.Definitions())

	op := &Operator{
		rt:           cfg.Runtime,
		prov:         cfg.Provider,
		model:        cfg.Model,
		tools:        tools,
		store:        cfg.Store,
		eventCh:      eventCh,
		workDir:      cfg.WorkDir,
		sessionFile:  cfg.SessionFile,
		systemPrompt: cfg.SystemPrompt,
		provTools:    provTools,
		broker:       cfg.Broker,
		onText:       cfg.OnText,
		onReasoning:  cfg.OnReasoning,
		onEvent:      cfg.OnEvent,
		onTurnDone:   cfg.OnTurnDone,
		onPrompt:     cfg.OnPrompt,
		onResolve:    cfg.OnResolve,
		onToolCall:   cfg.OnToolCall,
	}
	op.askUserTimeout = cfg.AskUserTimeout
	if op.askUserTimeout <= 0 {
		op.askUserTimeout = defaultAskUserTimeout
	}

	// Wire the ask_user prompt function into the tool executor.
	tools.promptUser = op.promptUser

	// Restore the persisted conversation so a server restart (or live
	// provider activation) doesn't wipe the operator's working memory of
	// jobs that still exist in the database.
	op.loadSession()

	return op, nil
}

// loadSession restores the conversation from the session file written by
// persistSession, if one exists. The system prompt is NOT restored — the
// freshly composed one from Config wins, so prompt updates take effect on
// restart. Corrupt or unreadable files log a warning and start fresh.
func (o *Operator) loadSession() {
	if o.sessionFile == "" {
		return
	}
	data, err := os.ReadFile(o.sessionFile)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("failed to read operator session file; starting fresh",
				"path", o.sessionFile, "error", err)
		}
		return
	}

	var sess operatorSession
	if err := json.Unmarshal(data, &sess); err != nil {
		slog.Warn("operator session file corrupt; starting fresh",
			"path", o.sessionFile, "error", err)
		return
	}

	msgs := make([]provider.Message, 0, len(sess.Messages))
	for _, m := range sess.Messages {
		msgs = append(msgs, provider.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
		})
	}
	msgs = truncateMessages(msgs, maxMessages)
	msgs = trimIncompleteTail(msgs)
	if len(msgs) == 0 {
		return
	}

	o.mu.Lock()
	o.messages = msgs
	o.mu.Unlock()
	slog.Info("restored operator conversation",
		"messages", len(msgs), "persisted_at", sess.UpdatedAt)
}

// trimIncompleteTail drops trailing messages left by a crash mid-turn so the
// restored history ends at a provider-valid boundary: a user message or an
// assistant message without tool calls. A trailing assistant message whose
// tool results were never recorded (or a partially recorded tool round)
// would get the first post-restart request rejected with a 400.
func trimIncompleteTail(msgs []provider.Message) []provider.Message {
	for len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		if last.Role == "user" || (last.Role == "assistant" && len(last.ToolCalls) == 0) {
			break
		}
		msgs = msgs[:len(msgs)-1]
	}
	return msgs
}

// Send pushes an event into the operator's event channel. It blocks until the
// event is accepted or the context is cancelled.
func (o *Operator) Send(ctx context.Context, event Event) error {
	select {
	case o.eventCh <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Start launches the event loop goroutine. It processes events until ctx is
// cancelled. The goroutine exits cleanly when the context is done.
func (o *Operator) Start(ctx context.Context) {
	go o.run(ctx)
}

// run is the event loop. It blocks on the event channel and dispatches events.
func (o *Operator) run(ctx context.Context) {
	// Re-dispatch any jobs left mid-flight by a previous shutdown before
	// consuming live events, so an interrupted job resumes instead of sitting
	// 'active' forever. This runs on the event-loop goroutine, so it is
	// serialized with all later event handling — no races on assignNextTask.
	o.recoverInterrupted(ctx)

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

// handleEvent dispatches a single event. User messages and decision-requiring
// events go to the LLM; routine events are handled mechanically.
func (o *Operator) handleEvent(ctx context.Context, ev Event) {
	// Notify observer.
	if o.onEvent != nil {
		o.onEvent(ev)
	}

	switch ev.Type {
	case EventUserMessage:
		payload, ok := ev.Payload.(UserMessagePayload)
		if !ok {
			slog.Error("invalid payload for user_message event", "payload", ev.Payload)
			return
		}
		o.postFeedEntry(ctx, db.FeedEntryUserMessage, payload.Text, "")
		o.handleUserMessage(ctx, payload)

	case EventTaskStarted:
		// Mechanical: update feed, log.
		payload, ok := ev.Payload.(TaskStartedPayload)
		if !ok {
			slog.Error("invalid payload for task_started event", "payload", ev.Payload)
			return
		}
		content := fmt.Sprintf("⚡ %s started task: %s", payload.GraphID, payload.Title)
		o.postFeedEntry(ctx, db.FeedEntryTaskStarted, content, payload.JobID)
		slog.Info("task started", "task_id", payload.TaskID, "job_id", payload.JobID, "graph_id", payload.GraphID, "title", payload.Title)

	case EventTaskCompleted:
		// Conditional: mechanical if next task queued; LLM if recommendations or job may be done.
		payload, ok := ev.Payload.(TaskCompletedPayload)
		if !ok {
			slog.Error("invalid payload for task_completed event", "payload", ev.Payload)
			return
		}
		content := fmt.Sprintf("✅ %s completed task: %s", payload.GraphID, payload.Summary)
		o.postFeedEntry(ctx, db.FeedEntryTaskCompleted, content, payload.JobID)
		slog.Info("task completed", "task_id", payload.TaskID, "job_id", payload.JobID, "graph_id", payload.GraphID, "summary", payload.Summary)

		if payload.HasNextTask {
			// Mechanical: assign the next ready task.
			o.assignNextTask(ctx, payload.JobID)
		} else if payload.Recommendations != "" {
			// LLM: consult scheduler about recommendations.
			msg := fmt.Sprintf("Task %q completed by team %s. Summary: %s\n\nThe team recommends: %s\n\n"+
				"Decide how to proceed. If a recommendation is worth acting on, call create_task "+
				"with this job's ID to queue it as follow-up work; if it needs a human decision, "+
				"use ask_user; otherwise summarize via surface_to_user.",
				payload.TaskID, payload.GraphID, payload.Summary, payload.Recommendations)
			o.sendToLLM(ctx, msg)
		} else {
			// No next task and no recommendations — check if job is done.
			o.checkJobComplete(ctx, payload.JobID)
		}

	case EventTaskFailed:
		// LLM: need to decide next steps.
		payload, ok := ev.Payload.(TaskFailedPayload)
		if !ok {
			slog.Error("invalid payload for task_failed event", "payload", ev.Payload)
			return
		}
		// Tasks that depend on this one can never become ready while it sits
		// failed (GetReadyTasks requires completed dependencies) — without
		// surfacing them, the failure silently stalls the rest of the job.
		blocked := o.blockedDependents(ctx, payload.TaskID)

		content := fmt.Sprintf("❌ %s failed task: %s", payload.GraphID, payload.Error)
		if len(blocked) > 0 {
			content += fmt.Sprintf(" (blocks %d queued task(s): %s)", len(blocked), strings.Join(blocked, "; "))
		}
		o.postFeedEntry(ctx, db.FeedEntryTaskFailed, content, payload.JobID)
		slog.Warn("task failed", "task_id", payload.TaskID, "job_id", payload.JobID, "graph_id", payload.GraphID, "error", payload.Error, "blocked_dependents", len(blocked))

		msg := fmt.Sprintf("Task %q (graph %s) failed with error: %s\n\n"+
			"Decide how to proceed. If this looks transient or fixable — an environment, "+
			"dependency, or build issue, or something a clearer instruction would resolve — "+
			"prefer retry_task with this task_id to re-run it in place. Do NOT create a new "+
			"job to redo work that is already partly done. If a human decision is needed, use "+
			"ask_user or surface_to_user. Only create a new job when the work genuinely needs "+
			"to be re-scoped from scratch.",
			payload.TaskID, payload.GraphID, payload.Error)
		if len(blocked) > 0 {
			msg += fmt.Sprintf("\n\nIMPORTANT: %d queued task(s) depend on this task and cannot "+
				"start until it succeeds: %s. Leaving it failed stalls them indefinitely.",
				len(blocked), strings.Join(blocked, "; "))
		}
		o.sendToLLM(ctx, msg)

	case EventProgressUpdate:
		// Mechanical: DB update already done by tool handler. Debug log only.
		payload, ok := ev.Payload.(ProgressUpdatePayload)
		if !ok {
			slog.Error("invalid payload for progress_update event", "payload", ev.Payload)
			return
		}
		slog.Debug("progress update", "task_id", payload.TaskID, "worker_id", payload.WorkerID, "message", payload.Message)

	case EventJobComplete:
		// Mechanical: mark job done, post feed entry, notify user.
		payload, ok := ev.Payload.(JobCompletePayload)
		if !ok {
			slog.Error("invalid payload for job_complete event", "payload", ev.Payload)
			return
		}
		if o.store != nil {
			if err := o.store.UpdateJobStatus(ctx, payload.JobID, db.JobStatusCompleted); err != nil {
				slog.Error("failed to mark job complete", "job_id", payload.JobID, "error", err)
			}
		}
		content := fmt.Sprintf("🎉 Job complete: %s", payload.Title)
		o.postFeedEntry(ctx, db.FeedEntryJobComplete, content, payload.JobID)
		slog.Info("job complete", "job_id", payload.JobID, "title", payload.Title, "summary", payload.Summary)
		o.emitText(fmt.Sprintf("\n%s\n", content))

	case EventNewTaskRequest:
		// LLM: scheduler decides whether to create the task.
		payload, ok := ev.Payload.(NewTaskRequestPayload)
		if !ok {
			slog.Error("invalid payload for new_task_request event", "payload", ev.Payload)
			return
		}
		content := fmt.Sprintf("Graph %s requests new task: %s (reason: %s)", payload.GraphID, payload.Description, payload.Reason)
		o.postFeedEntry(ctx, db.FeedEntrySystemEvent, content, payload.JobID)
		slog.Info("new task request", "job_id", payload.JobID, "graph_id", payload.GraphID, "description", payload.Description, "reason", payload.Reason)

		msg := fmt.Sprintf("Graph %s recommends creating a new task for job %s: %s\n\nReason: %s\n\n"+
			"Decide whether this work is worth doing. If yes, call create_task with this job_id — "+
			"the framework picks a graph and starts the task when it becomes ready. Do NOT create "+
			"a new job for follow-up work on an existing job. If the decision belongs to a human, "+
			"use ask_user; otherwise surface_to_user to explain why you declined.",
			payload.GraphID, payload.JobID, payload.Description, payload.Reason)
		o.sendToLLM(ctx, msg)

	case EventUserResponse:
		// LLM: relay the user's response.
		payload, ok := ev.Payload.(UserResponsePayload)
		if !ok {
			slog.Error("invalid payload for user_response event", "payload", ev.Payload)
			return
		}
		o.postFeedEntry(ctx, db.FeedEntryUserMessage, payload.Text, "")
		slog.Info("user response", "request_id", payload.RequestID, "text", payload.Text)

		msg := payload.Text
		if payload.RequestID != "" {
			msg = fmt.Sprintf("[Response to request %s] %s", payload.RequestID, payload.Text)
		}
		o.sendToLLM(ctx, msg)

	default:
		slog.Warn("unknown event type", "type", ev.Type)
	}
}

// postFeedEntry creates a feed entry in the database. If the store is nil,
// the entry is silently skipped.
func (o *Operator) postFeedEntry(ctx context.Context, entryType db.FeedEntryType, content string, jobID string) {
	if o.store == nil {
		return
	}
	entry := &db.FeedEntry{
		EntryType: entryType,
		Content:   content,
		JobID:     jobID,
	}
	if err := o.store.CreateFeedEntry(ctx, entry); err != nil {
		slog.Warn("failed to create feed entry", "type", entryType, "error", err)
	}
}

// sendToLLM wraps an event notification as a user message and sends it to the
// operator LLM for decision-making. This is distinct from handleUserMessage —
// it injects system-generated context into the conversation.
func (o *Operator) sendToLLM(ctx context.Context, message string) {
	o.handleUserMessage(ctx, UserMessagePayload{Text: message})
}

// blockedDependents returns the titles of pending tasks that declare a
// dependency on taskID. While the dependency sits failed they can never
// become ready, so callers surface them alongside the failure.
func (o *Operator) blockedDependents(ctx context.Context, taskID string) []string {
	if o.store == nil {
		return nil
	}
	deps, err := o.store.ListTaskDependents(ctx, taskID)
	if err != nil {
		slog.Warn("failed to list task dependents", "task_id", taskID, "error", err)
		return nil
	}
	var titles []string
	for _, t := range deps {
		if t.Status == db.TaskStatusPending {
			titles = append(titles, t.Title)
		}
	}
	return titles
}

// reportAdvanceFailure surfaces a failure in the mechanical task-advance
// path. assignNextTask is the only thing that moves a job forward after a
// completion, so a silent failure here stalls the whole pipeline: post a
// feed entry for visibility and consult the LLM so the problem reaches the
// user instead of dying in a log line.
func (o *Operator) reportAdvanceFailure(ctx context.Context, jobID, detail string) {
	o.postFeedEntry(ctx, db.FeedEntrySystemEvent, fmt.Sprintf("⚠️ %s", detail), jobID)
	msg := fmt.Sprintf("The framework failed to advance job %s to its next task: %s\n\n"+
		"Nothing retries this automatically — the job is stalled until someone acts. "+
		"Inspect the job with query_job, then surface the problem to the user with "+
		"surface_to_user (or ask_user if a decision is needed), explaining what failed "+
		"and what could fix it.",
		jobID, detail)
	o.sendToLLM(ctx, msg)
}

// assignNextTask finds the next ready task for a job and assigns it using
// the SystemTools. If no ready tasks exist or the store is unavailable,
// it logs and returns.
func (o *Operator) assignNextTask(ctx context.Context, jobID string) {
	if o.store == nil {
		slog.Warn("cannot assign next task: no store configured")
		return
	}

	readyTasks, err := o.store.GetReadyTasks(ctx, jobID)
	if err != nil {
		slog.Error("failed to get ready tasks", "job_id", jobID, "error", err)
		o.reportAdvanceFailure(ctx, jobID, fmt.Sprintf("could not determine the next ready task: %s", err))
		return
	}
	if len(readyTasks) == 0 {
		slog.Info("no ready tasks to assign", "job_id", jobID)
		return
	}

	// Graph assignment is the service's job: tasks without a graph are picked
	// up by fine-decompose (dispatchFineDecomposeForReadyTasks runs just before
	// this handler fires) and re-advanced once it completes. The operator only
	// dispatches tasks that already have a graph — it has no tool to assign one,
	// so prompting the LLM here would just produce confused prose about graphs
	// it cannot act on. Skip graphless tasks and dispatch the first graphed one.
	var task *db.Task
	for i := range readyTasks {
		if readyTasks[i].GraphID != "" {
			task = readyTasks[i]
			break
		}
	}
	if task == nil {
		slog.Info("no graphed ready tasks to assign; fine-decompose in flight",
			"job_id", jobID, "ready", len(readyTasks))
		return
	}

	// Use SystemTools to assign the task (handles dispatch, status update, event).
	if o.tools == nil || o.tools.systemTools == nil {
		slog.Warn("cannot assign next task: no system tools configured")
		return
	}

	args, err := json.Marshal(map[string]string{
		"task_id":  task.ID,
		"graph_id": task.GraphID,
	})
	if err != nil {
		slog.Error("failed to marshal assign_task args", "error", err)
		return
	}

	if _, err := o.tools.systemTools.Execute(ctx, "assign_task", args); err != nil {
		slog.Error("failed to assign next task", "task_id", task.ID, "graph_id", task.GraphID, "error", err)
		o.reportAdvanceFailure(ctx, jobID, fmt.Sprintf(
			"could not start task %q on graph %q: %s", task.Title, task.GraphID, err))
	}
}

// recoverInterrupted re-dispatches jobs left active by a previous shutdown.
// ReconcileInterrupted (run at boot, before the operator exists) reset their
// in-flight tasks back to 'pending'; now that dispatch is live we kick each
// active job's pipeline back into motion so it resumes from the next ready
// task. Without this, a crash mid-job leaves the job 'active' with a ready
// task that nothing ever assigns — the silent forever-stalled state.
func (o *Operator) recoverInterrupted(ctx context.Context) {
	if o.store == nil {
		return
	}
	active := db.JobStatusActive
	jobs, err := o.store.ListJobs(ctx, db.JobFilter{Status: &active})
	if err != nil {
		slog.Error("recovery: failed to list active jobs", "error", err)
		return
	}
	for _, job := range jobs {
		// Only resume jobs that actually have a graphed task ready to
		// dispatch. A job that's active but has nothing ready (awaiting
		// fine-decompose, or effectively complete) needs no kick and no
		// spurious feed entry. This mirrors assignNextTask's own filter:
		// graphless tasks are picked up by fine-decompose, not here.
		ready, err := o.store.GetReadyTasks(ctx, job.ID)
		if err != nil {
			slog.Error("recovery: failed to check ready tasks", "job_id", job.ID, "error", err)
			continue
		}
		if !hasGraphedTask(ready) {
			continue
		}
		slog.Info("recovery: resuming job interrupted by previous shutdown",
			"job_id", job.ID, "title", job.Title)
		o.postFeedEntry(ctx, db.FeedEntrySystemEvent,
			fmt.Sprintf("♻️ Resumed job after server restart: %s", job.Title), job.ID)
		o.assignNextTask(ctx, job.ID)
	}
}

// hasGraphedTask reports whether any task in the slice has a graph assigned.
func hasGraphedTask(tasks []*db.Task) bool {
	for _, t := range tasks {
		if t.GraphID != "" {
			return true
		}
	}
	return false
}

// checkJobComplete checks if all tasks for a job are done. If so, it sends
// an EventJobComplete to the event loop.
func (o *Operator) checkJobComplete(ctx context.Context, jobID string) {
	if o.store == nil {
		return
	}

	tasks, err := o.store.ListTasksForJob(ctx, jobID)
	if err != nil {
		slog.Error("failed to list tasks for job completion check", "job_id", jobID, "error", err)
		return
	}

	if len(tasks) == 0 {
		return // No tasks yet — not complete.
	}

	// Check if all tasks are in a terminal state (completed, failed, cancelled).
	for _, task := range tasks {
		switch task.Status {
		case db.TaskStatusCompleted, db.TaskStatusFailed, db.TaskStatusCancelled:
			continue
		default:
			// Still have active/pending tasks.
			return
		}
	}

	// All tasks are done — look up the job for the title.
	job, err := o.store.GetJob(ctx, jobID)
	if err != nil {
		slog.Error("failed to get job for completion", "job_id", jobID, "error", err)
		return
	}

	// Handle job completion inline rather than sending to eventCh, because
	// checkJobComplete runs on the event loop goroutine (the sole reader).
	// Sending to eventCh from the reader would self-deadlock if the buffer is full.
	o.handleEvent(ctx, Event{
		Type: EventJobComplete,
		Payload: JobCompletePayload{
			JobID:   jobID,
			Title:   job.Title,
			Summary: "All tasks completed",
		},
	})
}

// handleUserMessage sends a user message to the operator LLM and processes
// the response, including any tool calls. This drives the operator's
// conversation turn by turn.
func (o *Operator) handleUserMessage(ctx context.Context, payload UserMessagePayload) {
	// Record which turn is streaming so emitText/emitReasoning stamp their
	// callbacks with it. The event loop is serial, so turns never overlap.
	o.mu.Lock()
	o.turnID = payload.TurnID
	o.mu.Unlock()

	// Aggregate token usage across all LLM round-trips in this turn so the
	// emitted OnTurnDone reflects the full cost of handling the user message.
	var totalIn, totalOut int
	defer func() {
		o.mu.Lock()
		o.turnID = ""
		o.mu.Unlock()
		o.emitTurnDone(payload.TurnID, totalIn, totalOut, 0)
	}()

	// Append user message to the long-lived conversation.
	o.appendMessage(provider.Message{
		Role:    "user",
		Content: payload.Text,
	})

	// Run the operator's turn — may involve multiple LLM round-trips if the
	// LLM makes tool calls. Bounded so a model that keeps emitting tool calls,
	// or keeps emitting malformed ones that fail validation, can't spin forever
	// (small local models repeatedly mis-format ask_user/surface_to_user and
	// would otherwise loop indefinitely on the validation error).
	const (
		maxTurnRounds              = 25 // LLM round-trips per user message
		maxConsecutiveFailedRounds = 3  // rounds in which every tool call errored
	)
	var (
		round             int
		consecutiveFailed int
		lastToolErr       string
	)
	for {
		if ctx.Err() != nil {
			return
		}
		round++
		if round > maxTurnRounds {
			slog.Warn("operator turn hit round cap", "max", maxTurnRounds)
			o.emitText(fmt.Sprintf("[stopped after %d tool rounds without finishing this turn]", maxTurnRounds))
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

		assistantMsg, toolCalls, usage, err := o.collectResponse(ctx, eventCh)
		if err != nil {
			slog.Error("operator response collection failed", "error", err)
			o.emitText(fmt.Sprintf("[operator error: %s]", err))
			return
		}
		totalIn += usage.InputTokens
		totalOut += usage.OutputTokens

		// Small local models sometimes emit tool calls with empty or malformed
		// JSON arguments. Left as-is, that invalid JSON poisons the message
		// history and the next request to the endpoint fails with a 400. Repair
		// it to an empty object so the tool handler sees valid args (and reports
		// its own "missing field" error) and the history stays serializable.
		provider.NormalizeToolCallArgs(assistantMsg.ToolCalls)
		provider.NormalizeToolCallArgs(toolCalls)
		o.appendMessage(assistantMsg)

		// No tool calls — the operator's turn is done.
		if len(toolCalls) == 0 {
			return
		}

		// Execute tool calls and feed results back.
		failed := 0
		for _, tc := range toolCalls {
			slog.Info("operator tool call", "tool", tc.Name, "id", tc.ID)

			result, execErr := o.tools.Execute(ctx, tc.Name, tc.Arguments)

			if execErr != nil {
				slog.Warn("operator tool execution error", "tool", tc.Name, "error", execErr)
				result = fmt.Sprintf("error: %s", execErr.Error())
				failed++
				lastToolErr = execErr.Error()
			}

			// Surface the tool call to subscribers (the TUI renders it as a
			// collapsible indicator) so the operator's work between text
			// segments isn't an invisible pause.
			if o.onToolCall != nil {
				o.onToolCall(tc.Name, tc.Arguments, result, execErr != nil)
			}

			o.appendMessage(provider.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}

		// Circuit breaker: if every tool call in this round failed, count it.
		// A run of all-failed rounds means the model is stuck (typically
		// re-emitting a malformed call against the same schema) — stop and
		// surface the last error rather than burn the model in a tight loop.
		if failed == len(toolCalls) {
			consecutiveFailed++
			if consecutiveFailed >= maxConsecutiveFailedRounds {
				slog.Warn("operator turn aborted: consecutive failed tool rounds",
					"rounds", consecutiveFailed, "last_error", lastToolErr)
				o.emitText(fmt.Sprintf("[stopped after %d straight rounds of failed tool calls — last error: %s]", consecutiveFailed, lastToolErr))
				return
			}
		} else {
			consecutiveFailed = 0
		}

		// Loop — send tool results back to LLM for the next response.
	}
}

// promptUser implements the ask_user tool. Delegates to the shared HITL
// broker, emitting the operator-prompt event via the onPrompt callback so
// the TUI still sees the prompt the same way it always did. Called from
// the event loop goroutine — the response arrives on a separate goroutine
// (HTTP handler → LocalService.RespondToPrompt → broker.Respond), so no
// deadlock.
//
// The wait is bounded by askUserTimeout: this blocks the event loop, so an
// unanswered prompt must resolve eventually or the whole operator freezes.
// On timeout the tool returns a no-response message (not an error) so the
// LLM continues the turn with its best judgment.
func (o *Operator) promptUser(ctx context.Context, requestID string, questions []graphexec.PromptQuestion) (string, error) {
	if o.broker == nil {
		return "", fmt.Errorf("operator: no HITL broker configured")
	}
	broadcast := func() {
		if o.onPrompt != nil {
			o.onPrompt(requestID, questions)
		}
	}
	// Resolve the blocker once Ask returns, whether the user answered, the
	// wait timed out, or the turn's context was cancelled. Mirrors the
	// graph-node path so the Blockers queue stays consistent regardless of
	// which side raised the request.
	if o.onResolve != nil {
		defer o.onResolve(requestID)
	}

	askCtx, cancel := context.WithTimeout(ctx, o.askUserTimeout)
	defer cancel()
	answer, err := o.broker.Ask(askCtx, requestID, broadcast)
	if err != nil {
		// Distinguish "the user never answered" from real cancellation
		// (shutdown / operator replacement), which must still propagate.
		if errors.Is(askCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			slog.Warn("ask_user timed out without a response",
				"request_id", requestID, "timeout", o.askUserTimeout)
			return fmt.Sprintf(
				"[No response: the user did not answer within %s. Proceed with your best judgment, "+
					"state any assumptions you make in your reply, and defer irreversible actions that "+
					"depend on this answer.]", o.askUserTimeout), nil
		}
		return "", err
	}
	return answer, nil
}

// appendMessage adds a message to the conversation history under lock
// and persists the full session to disk.
func (o *Operator) appendMessage(msg provider.Message) {
	o.mu.Lock()
	o.messages = append(o.messages, msg)
	o.messages = truncateMessages(o.messages, maxMessages)
	o.mu.Unlock()

	o.persistSession()
}

// operatorSession is the JSON structure written to the session file.
type operatorSession struct {
	SystemPrompt string            `json:"system_prompt"`
	Model        string            `json:"model"`
	Provider     string            `json:"provider"`
	Tools        []string          `json:"tools"`
	Messages     []operatorMessage `json:"messages"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

type operatorMessage struct {
	Role       string              `json:"role"`
	Content    string              `json:"content"`
	ToolCalls  []provider.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string              `json:"tool_call_id,omitempty"`
}

// persistSession writes the full operator conversation to the session file.
// Non-fatal: logs a warning on failure.
func (o *Operator) persistSession() {
	if o.sessionFile == "" {
		return
	}

	o.mu.Lock()
	msgs := make([]provider.Message, len(o.messages))
	copy(msgs, o.messages)
	o.mu.Unlock()

	var toolNames []string
	for _, t := range o.provTools {
		toolNames = append(toolNames, t.Name)
	}

	session := operatorSession{
		SystemPrompt: o.systemPrompt,
		Model:        o.model,
		Provider:     o.prov.Name(),
		Tools:        toolNames,
		UpdatedAt:    time.Now(),
	}
	for _, m := range msgs {
		session.Messages = append(session.Messages, operatorMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
		})
	}

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		slog.Warn("failed to marshal operator session", "error", err)
		return
	}

	// Atomic write: write to temp file, then rename.
	dir := filepath.Dir(o.sessionFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("failed to create session directory", "dir", dir, "error", err)
		return
	}
	tmp := o.sessionFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		slog.Warn("failed to write operator session", "path", tmp, "error", err)
		return
	}
	if err := os.Rename(tmp, o.sessionFile); err != nil {
		slog.Warn("failed to rename operator session file", "error", err)
	}
}

// truncateMessages trims the conversation history to at most maxMessages,
// ensuring the window never starts in the middle of a tool-call/result
// exchange. Naive truncation (messages[len-max:]) can split a tool-call from
// its tool-result, corrupting the LLM conversation. This function walks
// forward from the start of the tail window to find the first safe boundary:
// a user message or an assistant message with no tool calls.
func truncateMessages(messages []provider.Message, maxMessages int) []provider.Message {
	if len(messages) <= maxMessages {
		return messages
	}

	tail := messages[len(messages)-maxMessages:]

	// Walk forward to find the first complete exchange boundary.
	// A safe start is:
	// - A user message
	// - An assistant message with no tool calls
	// Skip orphaned tool results (role=tool) and assistant messages with
	// tool calls whose results might be before the window.
	for i, msg := range tail {
		if msg.Role == "user" {
			return tail[i:]
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 {
			return tail[i:]
		}
	}

	// No safe boundary anywhere in the window — plausible during tool-heavy
	// autonomous stretches where every assistant message carries tool calls.
	// Without a fallback the window would start with orphaned tool results,
	// the provider would reject every request, and (because this runs on
	// every append) the conversation would never self-heal. Drop leading
	// orphaned tool results so the window starts at an
	// assistant-with-tool-calls message whose results immediately follow —
	// the pairing stays intact even though the window opens mid-turn.
	for i, msg := range tail {
		if msg.Role != "tool" {
			return tail[i:]
		}
	}

	// Degenerate: the entire window is tool results. Nothing salvageable.
	return nil
}

// collectResponse reads from the event channel, accumulates text and tool
// calls, streams text to the OnText callback, and aggregates usage tokens.
// The returned Usage is the per-call total — handleUserMessage sums these
// across all round-trips in a turn before reporting via OnTurnDone.
func (o *Operator) collectResponse(ctx context.Context, eventCh <-chan provider.StreamEvent) (provider.Message, []provider.ToolCall, provider.Usage, error) {
	var textBuf strings.Builder
	var toolCalls []provider.ToolCall
	var usage provider.Usage

	for {
		select {
		case <-ctx.Done():
			return provider.Message{}, nil, usage, ctx.Err()

		case ev, ok := <-eventCh:
			if !ok {
				msg := provider.Message{
					Role:      "assistant",
					Content:   textBuf.String(),
					ToolCalls: toolCalls,
				}
				return msg, toolCalls, usage, nil
			}

			switch ev.Type {
			case provider.EventText:
				textBuf.WriteString(ev.Text)
				o.emitText(ev.Text)

			case provider.EventReasoning:
				// Reasoning is surfaced but not folded into the
				// assistant message — matches the mycelium/provider
				// contract and every upstream API's behavior.
				o.emitReasoning(ev.Text)

			case provider.EventToolCall:
				if ev.ToolCall != nil {
					toolCalls = append(toolCalls, *ev.ToolCall)
				}

			case provider.EventError:
				return provider.Message{}, nil, usage, ev.Error

			case provider.EventDone:
				// Continue reading until channel closes.

			case provider.EventUsage:
				if ev.Usage != nil {
					usage.InputTokens += ev.Usage.InputTokens
					usage.OutputTokens += ev.Usage.OutputTokens
				}
			}
		}
	}
}

// currentTurn returns the ID of the turn currently being processed, or ""
// between turns and during system-initiated turns.
func (o *Operator) currentTurn() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.turnID
}

// emitText calls the OnText callback if set.
func (o *Operator) emitText(text string) {
	if o.onText != nil {
		o.onText(o.currentTurn(), text)
	}
}

// emitReasoning calls the OnReasoning callback if set.
func (o *Operator) emitReasoning(text string) {
	if o.onReasoning != nil {
		o.onReasoning(o.currentTurn(), text)
	}
}

// emitTurnDone calls the OnTurnDone callback if set.
func (o *Operator) emitTurnDone(turnID string, tokensIn, tokensOut, reasoningTokens int) {
	if o.onTurnDone != nil {
		o.onTurnDone(turnID, tokensIn, tokensOut, reasoningTokens)
	}
}

// MessageCount returns the number of messages in the operator's conversation
// history. Useful for testing that the session is long-lived.
func (o *Operator) MessageCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.messages)
}
