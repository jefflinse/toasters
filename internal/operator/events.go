package operator

import (
	"context"
	"log/slog"
)

// trySendEvent sends an event to the operator event channel. It blocks until
// the event is accepted or the context is cancelled.
//
// IMPORTANT: This function must NOT be called from the event loop goroutine
// (the sole reader of eventCh) when the buffer could be full, as this would
// self-deadlock. The event loop goroutine should handle such events inline
// (see checkJobComplete and assignTask for the pattern).
//
// Note: events may be dropped during shutdown when the context is cancelled.
// This is acceptable because the DB state (task status, progress reports) is
// already persisted before the event is sent. Events are for operator awareness
// only — the persistent state is consistent regardless.
func trySendEvent(ctx context.Context, ch chan<- Event, ev Event) {
	select {
	case ch <- ev:
	case <-ctx.Done():
		slog.Warn("event send cancelled", "type", ev.Type, "error", ctx.Err())
	}
}

// EventType identifies the kind of event sent to the operator event loop.
type EventType string

const (
	EventUserMessage     EventType = "user_message"
	EventTaskStarted     EventType = "task_started"
	EventTaskCompleted   EventType = "task_completed"
	EventTaskFailed      EventType = "task_failed"
	EventBlockerReported EventType = "blocker_reported"
	EventProgressUpdate  EventType = "progress_update"
	EventJobComplete     EventType = "job_complete"
	EventNewTaskRequest  EventType = "new_task_request"
	EventUserResponse    EventType = "user_response" // response to an ask_user prompt
)

// Event is a typed message sent to the operator event loop.
type Event struct {
	Type    EventType
	Payload any
}

// UserMessagePayload carries a user's text message.
type UserMessagePayload struct {
	Text string
}

// TaskStartedPayload carries info about a task that just started.
type TaskStartedPayload struct {
	TaskID string
	JobID  string
	TeamID string
	Title  string
}

// TaskCompletedPayload carries the result of a completed task.
type TaskCompletedPayload struct {
	TaskID          string
	JobID           string
	TeamID          string
	Summary         string
	Recommendations string // follow-up recommendations from the team
	HasNextTask     bool   // whether there's a queued next task
}

// TaskFailedPayload carries information about a failed task.
type TaskFailedPayload struct {
	TaskID string
	JobID  string
	TeamID string
	Error  string
}

// BlockerReportedPayload carries a blocker report from an agent.
type BlockerReportedPayload struct {
	TaskID      string
	TeamID      string
	AgentID     string
	Description string
}

// ProgressUpdatePayload carries a progress report from a team.
type ProgressUpdatePayload struct {
	TaskID  string
	AgentID string
	Message string
}

// JobCompletePayload carries info about a completed job.
type JobCompletePayload struct {
	JobID   string
	Title   string
	Summary string
}

// NewTaskRequestPayload carries a team's recommendation for a new task.
type NewTaskRequestPayload struct {
	JobID       string
	TeamID      string
	Description string
	Reason      string
}

// UserResponsePayload carries the user's response to an ask_user prompt.
type UserResponsePayload struct {
	Text      string
	RequestID string // correlates with the ask_user request
}
