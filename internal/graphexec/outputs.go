package graphexec

import "encoding/json"

// Typed output structs and their JSON schemas for each role's terminal
// `complete` tool call. The schema is forwarded verbatim to the provider's
// tool-call validation, so the model is required to produce a payload that
// unmarshals into the matching struct.

// FindingsOutput is the investigator's structured output.
type FindingsOutput struct {
	Summary string `json:"summary"`
}

var findingsSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "summary": {
      "type": "string",
      "description": "Investigation findings. Include file paths, function names, and observed behavior — enough for the planner to make decisions without re-investigating."
    }
  },
  "required": ["summary"]
}`)

// PlanOutput is the planner's structured output.
type PlanOutput struct {
	Summary string `json:"summary"`
}

var planSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "summary": {
      "type": "string",
      "description": "Implementation plan as a short numbered list of concrete steps. Each step should be actionable by the implementer without further clarification."
    }
  },
  "required": ["summary"]
}`)

// ImplementOutput is the implementer's structured output.
type ImplementOutput struct {
	Summary string `json:"summary"`
}

var implementSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "summary": {
      "type": "string",
      "description": "Summary of changes made: files touched, intent of each change, any notable deviations from the plan."
    }
  },
  "required": ["summary"]
}`)

// TestOutput is the tester's structured output. Passed drives graph
// routing (tests_passed → review, tests_failed → implement).
type TestOutput struct {
	Passed  bool   `json:"passed"`
	Summary string `json:"summary"`
}

var testSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "passed": {
      "type": "boolean",
      "description": "true when every test you ran passed; false if any test failed."
    },
    "summary": {
      "type": "string",
      "description": "Test results. When failing, include the failing output so the implementer can address it in the next round."
    }
  },
  "required": ["passed", "summary"]
}`)

// ReviewOutput is the reviewer's structured output. Approved drives graph
// routing (approved → End, rejected → implement).
type ReviewOutput struct {
	Approved bool   `json:"approved"`
	Feedback string `json:"feedback"`
}

var reviewSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "approved": {
      "type": "boolean",
      "description": "true when the implementation satisfies the plan and needs no further revision."
    },
    "feedback": {
      "type": "string",
      "description": "Concrete feedback. When rejecting, describe what needs to change — the feedback is passed to the next implementation round."
    }
  },
  "required": ["approved", "feedback"]
}`)

// WorkOutput is the single-worker graph's structured output.
type WorkOutput struct {
	Output string `json:"output"`
}

var workSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "output": {
      "type": "string",
      "description": "A short summary of what was accomplished."
    }
  },
  "required": ["output"]
}`)

// Status values that nodes set on TaskState so conditional edges can
// route. Centralized here so templates and node builders agree on the
// vocabulary.
const (
	StatusTestsPassed    = "tests_passed"
	StatusTestsFailed    = "tests_failed"
	StatusReviewApproved = "review_approved"
	StatusReviewRejected = "review_rejected"
	StatusCompleted      = "completed"
)
