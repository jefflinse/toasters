package operator

// EventType identifies the kind of event sent to the operator event loop.
type EventType string

const (
	EventUserMessage     EventType = "user_message"
	EventTaskCompleted   EventType = "task_completed"
	EventTaskFailed      EventType = "task_failed"
	EventBlockerReported EventType = "blocker_reported"
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

// TaskCompletedPayload carries the result of a completed task.
type TaskCompletedPayload struct {
	TaskID  string
	Summary string
}

// TaskFailedPayload carries information about a failed task.
type TaskFailedPayload struct {
	TaskID string
	Error  string
}

// BlockerReportedPayload carries a blocker report from an agent.
type BlockerReportedPayload struct {
	AgentID     string
	Description string
}
