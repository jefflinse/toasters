// Package operator implements the operator event loop for coordinating
// LLM-powered agent work. The operator maintains a long-lived conversation
// with an LLM, receives typed events on a buffered channel, and dispatches
// them — forwarding user messages to the LLM and handling routine events
// mechanically.
package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/jefflinse/toasters/internal/compose"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

const (
	eventChSize = 256
	maxMessages = 200
)

// Operator manages the event loop and long-lived operator LLM session.
type Operator struct {
	rt      *runtime.Runtime
	prov    provider.Provider
	model   string
	tools   *operatorTools
	store   db.Store
	eventCh chan Event
	workDir string

	// Long-lived conversation state. Protected by mu for concurrent access
	// from the event loop goroutine and external callers (e.g. MessageCount).
	mu           sync.Mutex
	systemPrompt string
	messages     []provider.Message
	provTools    []provider.Tool

	// Callbacks — set at construction time via Config, immutable after New().
	onText     func(text string) // called with streamed text from the operator LLM
	onEvent    func(event Event) // called when the event loop processes an event
	onTurnDone func()            // called when the operator finishes processing a user message turn
}

// Config holds configuration for creating an Operator.
type Config struct {
	Runtime      *runtime.Runtime
	Provider     provider.Provider
	Model        string
	WorkDir      string
	SystemPrompt string // required; system prompt for the operator LLM session
	Store        db.Store
	Composer     *compose.Composer
	Spawner      runtime.TeamLeadSpawner // spawns team lead sessions on task assignment; may be nil
	OnText       func(text string)       // called with streamed text from the operator LLM
	OnEvent      func(event Event)       // called when the event loop processes an event
	OnTurnDone   func()                  // called when the operator finishes processing a user message turn
}

// New creates a new Operator. Call Start to begin processing events.
func New(cfg Config) (*Operator, error) {
	if cfg.SystemPrompt == "" {
		return nil, fmt.Errorf("operator: SystemPrompt is required")
	}

	// Create SystemTools for system agents to use. The event channel is the
	// operator's own channel so system agent actions (e.g. assign_task) flow
	// back through the operator event loop.
	eventCh := make(chan Event, eventChSize)
	var systemTools *SystemTools
	if cfg.Store != nil && cfg.Composer != nil {
		systemTools = NewSystemTools(cfg.Store, cfg.Composer, eventCh, cfg.Spawner, cfg.WorkDir)
	}

	tools := newOperatorTools(cfg.Runtime, cfg.Composer, cfg.Store, systemTools, cfg.WorkDir)
	provTools := operatorToolsToProviderTools(tools.Definitions())

	return &Operator{
		rt:           cfg.Runtime,
		prov:         cfg.Provider,
		model:        cfg.Model,
		tools:        tools,
		store:        cfg.Store,
		eventCh:      eventCh,
		workDir:      cfg.WorkDir,
		systemPrompt: cfg.SystemPrompt,
		provTools:    provTools,
		onText:       cfg.OnText,
		onEvent:      cfg.OnEvent,
		onTurnDone:   cfg.OnTurnDone,
	}, nil
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
		content := fmt.Sprintf("⚡ %s started task: %s", payload.TeamID, payload.Title)
		o.postFeedEntry(ctx, db.FeedEntryTaskStarted, content, payload.JobID)
		slog.Info("task started", "task_id", payload.TaskID, "job_id", payload.JobID, "team_id", payload.TeamID, "title", payload.Title)

	case EventTaskCompleted:
		// Conditional: mechanical if next task queued; LLM if recommendations or job may be done.
		payload, ok := ev.Payload.(TaskCompletedPayload)
		if !ok {
			slog.Error("invalid payload for task_completed event", "payload", ev.Payload)
			return
		}
		content := fmt.Sprintf("✅ %s completed task: %s", payload.TeamID, payload.Summary)
		o.postFeedEntry(ctx, db.FeedEntryTaskCompleted, content, payload.JobID)
		slog.Info("task completed", "task_id", payload.TaskID, "job_id", payload.JobID, "team_id", payload.TeamID, "summary", payload.Summary)

		if payload.HasNextTask {
			// Mechanical: assign the next ready task.
			o.assignNextTask(ctx, payload.JobID)
		} else if payload.Recommendations != "" {
			// LLM: consult scheduler about recommendations.
			msg := fmt.Sprintf("Task %q completed by team %s. Summary: %s\n\nThe team recommends: %s\n\nPlease decide how to proceed with these recommendations.",
				payload.TaskID, payload.TeamID, payload.Summary, payload.Recommendations)
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
		content := fmt.Sprintf("❌ %s failed task: %s", payload.TeamID, payload.Error)
		o.postFeedEntry(ctx, db.FeedEntryTaskFailed, content, payload.JobID)
		slog.Warn("task failed", "task_id", payload.TaskID, "job_id", payload.JobID, "team_id", payload.TeamID, "error", payload.Error)

		msg := fmt.Sprintf("Task %q assigned to team %s has failed with error: %s\n\nPlease decide how to proceed.",
			payload.TaskID, payload.TeamID, payload.Error)
		o.sendToLLM(ctx, msg)

	case EventBlockerReported:
		// LLM: need to triage the blocker.
		payload, ok := ev.Payload.(BlockerReportedPayload)
		if !ok {
			slog.Error("invalid payload for blocker_reported event", "payload", ev.Payload)
			return
		}
		content := fmt.Sprintf("🚫 %s reported blocker: %s", payload.TeamID, payload.Description)
		o.postFeedEntry(ctx, db.FeedEntryBlockerReported, content, "")
		slog.Warn("blocker reported", "task_id", payload.TaskID, "team_id", payload.TeamID, "agent_id", payload.AgentID, "description", payload.Description)

		msg := fmt.Sprintf("Team %s (task %q) reported a blocker: %s\n\nPlease triage this blocker and decide how to resolve it. Consider consulting the blocker-handler agent.",
			payload.TeamID, payload.TaskID, payload.Description)
		o.sendToLLM(ctx, msg)

	case EventProgressUpdate:
		// Mechanical: DB update already done by tool handler. Debug log only.
		payload, ok := ev.Payload.(ProgressUpdatePayload)
		if !ok {
			slog.Error("invalid payload for progress_update event", "payload", ev.Payload)
			return
		}
		slog.Debug("progress update", "task_id", payload.TaskID, "agent_id", payload.AgentID, "message", payload.Message)

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
		content := fmt.Sprintf("Team %s requests new task: %s (reason: %s)", payload.TeamID, payload.Description, payload.Reason)
		o.postFeedEntry(ctx, db.FeedEntrySystemEvent, content, payload.JobID)
		slog.Info("new task request", "job_id", payload.JobID, "team_id", payload.TeamID, "description", payload.Description, "reason", payload.Reason)

		msg := fmt.Sprintf("Team %s recommends creating a new task for job %s: %s\n\nReason: %s\n\nPlease decide whether to create this task and assign it.",
			payload.TeamID, payload.JobID, payload.Description, payload.Reason)
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
		return
	}
	if len(readyTasks) == 0 {
		slog.Info("no ready tasks to assign", "job_id", jobID)
		return
	}

	task := readyTasks[0]
	if task.TeamID == "" {
		// No pre-assigned team — need LLM to decide.
		msg := fmt.Sprintf("Task %q (id: %s) is ready but has no team assigned. Please assign it to an appropriate team.",
			task.Title, task.ID)
		o.sendToLLM(ctx, msg)
		return
	}

	// Use SystemTools to assign the task (handles spawning, status update, event).
	if o.tools == nil || o.tools.systemTools == nil {
		slog.Warn("cannot assign next task: no system tools configured")
		return
	}

	args, err := json.Marshal(map[string]string{
		"task_id": task.ID,
		"team_id": task.TeamID,
	})
	if err != nil {
		slog.Error("failed to marshal assign_task args", "error", err)
		return
	}

	if _, err := o.tools.systemTools.Execute(ctx, "assign_task", args); err != nil {
		slog.Error("failed to assign next task", "task_id", task.ID, "team_id", task.TeamID, "error", err)
	}
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
	defer o.emitTurnDone()

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
	// Prevent unbounded growth of conversation history. Keep the most recent
	// messages to stay within LLM context window limits.
	if len(o.messages) > maxMessages {
		o.messages = o.messages[len(o.messages)-maxMessages:]
	}
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
	if o.onText != nil {
		o.onText(text)
	}
}

// emitTurnDone calls the OnTurnDone callback if set.
func (o *Operator) emitTurnDone() {
	if o.onTurnDone != nil {
		o.onTurnDone()
	}
}

// MessageCount returns the number of messages in the operator's conversation
// history. Useful for testing that the session is long-lived.
func (o *Operator) MessageCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.messages)
}
