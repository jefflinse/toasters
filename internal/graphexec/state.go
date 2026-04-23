// Package graphexec provides rhizome-based graph execution for toasters.
// It defines the state, node builders, and graph templates that replace
// the long-lived session model with bounded, stateless LLM transformers.
package graphexec

import (
	"encoding/json"
	"fmt"
)

// JobType selects which graph template ExecuteTask will run for a job.
// The zero value (JobTypeUnset) routes to BugFixGraph — the full
// investigate → plan → implement → test → review cycle — so untyped jobs
// still exercise the rhizome pipeline end-to-end.
type JobType string

const (
	// JobTypeUnset is the zero value; maps to BugFixGraph as the default.
	JobTypeUnset JobType = ""

	// JobTypeBugFix runs the full investigate → plan → implement → test →
	// review cycle with review-rejection retry (capped at 3 iterations).
	JobTypeBugFix JobType = "bug_fix"

	// JobTypeNewFeature skips investigation (task description is assumed
	// self-sufficient for planning) but otherwise mirrors BugFix.
	JobTypeNewFeature JobType = "new_feature"

	// JobTypePrototype runs implement → test with retry cycles and no
	// investigation/planning/review. For quick iterations.
	JobTypePrototype JobType = "prototype"

	// JobTypeSingleWorker is the escape hatch: one bounded LLM node with
	// the full tool set. Skips the multi-phase cycle entirely.
	JobTypeSingleWorker JobType = "single_worker"
)

// TaskState is the state type for rhizome graphs (the S in Graph[S]).
// It flows through nodes, accumulating structured artifacts at each step.
// Each node receives the full state but builds a fresh LLM prompt from
// only the artifacts it needs — keeping context bounded.
type TaskState struct {
	// Identity.
	JobID  string
	TaskID string

	// Workspace.
	WorkspaceDir string

	// Provider/model config for LLM calls within nodes.
	ProviderName string
	Model        string

	// Artifacts holds structured data produced by nodes. Keys are
	// conventionally namespaced by node (e.g. "investigate.findings",
	// "plan.steps", "review.feedback"). Values are untyped — Artifacts
	// exists to feed the prompt engine with arbitrary string fragments.
	// Edge data-flow between nodes uses NodeOutputs below.
	Artifacts map[string]any

	// Inputs is the graph-level input payload, as raw JSON. Populated by
	// the caller before Execute. Edge expressions that read $graph.input.*
	// resolve against this.
	Inputs json.RawMessage

	// NodeOutputs holds each node's terminal typed output as raw JSON,
	// keyed by node ID. This is the edge data-flow carrier: a downstream
	// node reads an upstream node's output by unmarshaling into its own
	// input struct, and edge expressions ($node.output.field) resolve
	// by decoding the raw JSON. Preserves typing across the state
	// boundary without locking every node into a single Go struct.
	NodeOutputs map[string]json.RawMessage

	// Status is set by nodes to guide conditional routing. Routers
	// inspect this field to decide the next node. Common values:
	// "ok", "tests_failed", "review_rejected", "blocked".
	Status string

	// Err captures error information from node execution. A non-nil
	// Err after a node completes indicates the node's work was
	// unsuccessful (distinct from the node function returning an error,
	// which halts graph execution entirely).
	Err error

	// FinalText holds the last assistant message text from the most
	// recently executed node. Useful for extracting the node's
	// conversational output.
	FinalText string
}

// NewTaskState creates a TaskState with the required identity and workspace fields.
func NewTaskState(jobID, taskID, workspaceDir, providerName, model string) *TaskState {
	return &TaskState{
		JobID:        jobID,
		TaskID:       taskID,
		WorkspaceDir: workspaceDir,
		ProviderName: providerName,
		Model:        model,
		Artifacts:    make(map[string]any),
		NodeOutputs:  make(map[string]json.RawMessage),
	}
}

// SetArtifact stores a named artifact in the state.
func (s *TaskState) SetArtifact(key string, value any) {
	if s.Artifacts == nil {
		s.Artifacts = make(map[string]any)
	}
	s.Artifacts[key] = value
}

// GetArtifact retrieves a named artifact. Returns nil if not found.
func (s *TaskState) GetArtifact(key string) any {
	if s.Artifacts == nil {
		return nil
	}
	return s.Artifacts[key]
}

// GetArtifactString retrieves a named artifact as a string.
// Returns "" if not found or not a string.
func (s *TaskState) GetArtifactString(key string) string {
	v, _ := s.GetArtifact(key).(string)
	return v
}

// SetNodeOutput marshals v and stores it under nodeID. This is the canonical
// way for a node to publish its typed terminal output into state so downstream
// edges and nodes can read it.
func (s *TaskState) SetNodeOutput(nodeID string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshaling output for node %q: %w", nodeID, err)
	}
	if s.NodeOutputs == nil {
		s.NodeOutputs = make(map[string]json.RawMessage)
	}
	s.NodeOutputs[nodeID] = raw
	return nil
}

// GetNodeOutput returns the raw JSON published by nodeID, or nil if the node
// has not run (or produced no output).
func (s *TaskState) GetNodeOutput(nodeID string) json.RawMessage {
	if s.NodeOutputs == nil {
		return nil
	}
	return s.NodeOutputs[nodeID]
}

// UnmarshalNodeOutput decodes the output for nodeID into v. Returns an error
// if the node has not produced an output yet, or if the payload does not fit
// the target type.
func (s *TaskState) UnmarshalNodeOutput(nodeID string, v any) error {
	raw := s.GetNodeOutput(nodeID)
	if len(raw) == 0 {
		return fmt.Errorf("no output recorded for node %q", nodeID)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("unmarshaling output for node %q: %w", nodeID, err)
	}
	return nil
}

// maxSummaryLen caps the length of task/node summary strings stored in
// progress reports and broadcast events. Keeps DB rows and SSE payloads
// bounded when a node's final text is unexpectedly verbose.
const maxSummaryLen = 500

// truncateSummary caps s at maxSummaryLen, appending an ellipsis when
// truncated. Single source of truth so DB rows, broadcast payloads, and
// middleware progress messages agree on the cutoff.
func truncateSummary(s string) string {
	if len(s) <= maxSummaryLen {
		return s
	}
	return s[:maxSummaryLen] + "..."
}
