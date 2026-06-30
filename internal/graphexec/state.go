// Package graphexec provides rhizome-based graph execution for toasters.
// It defines the state, node builders, and graph templates that replace
// the long-lived session model with bounded, stateless LLM transformers.
package graphexec

import (
	"encoding/json"
	"fmt"
	"maps"
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

	// WorkspaceBase is the task's canonical workspace. It equals
	// WorkspaceDir except inside a fan-out branch, where WorkspaceDir is
	// the branch's isolated copy. Tool executors alias absolute paths
	// under WorkspaceBase into WorkspaceDir so instructions and upstream
	// artifacts that leak the real workspace path (task descriptions, plan
	// output) keep working inside the branch — otherwise models hit
	// "escapes working directory" on valid-looking paths and route around
	// the guard with shell, defeating isolation.
	WorkspaceBase string

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

	// Status is an optional routing-outcome label on a node that completed
	// normally (surfaced in progress reports and node-completed events).
	// Current production nodes don't set it — routing decisions flow
	// through NodeOutputs and edge expressions instead. Node failure is
	// signaled by returning an error, never by this field.
	Status string

	// FinalText holds the last assistant message text from the most
	// recently executed node. Useful for extracting the node's
	// conversational output.
	FinalText string

	// ExitNode is the node id whose output is the graph's terminal
	// output. Set by ExecuteTask from Definition.Exit; the executor
	// reads NodeOutputs[ExitNode] and surfaces it on task-completion
	// events so auto-dispatch consumers (decomposition) can read the
	// graph's structured output without re-running it.
	ExitNode string
}

// NewTaskState creates a TaskState with the required identity and workspace fields.
func NewTaskState(jobID, taskID, workspaceDir, providerName, model string) *TaskState {
	return &TaskState{
		JobID:         jobID,
		TaskID:        taskID,
		WorkspaceDir:  workspaceDir,
		WorkspaceBase: workspaceDir,
		ProviderName:  providerName,
		Model:         model,
		Artifacts:     make(map[string]any),
		NodeOutputs:   make(map[string]json.RawMessage),
	}
}

// MarshalBinary serializes the full state as JSON so rhizome can checkpoint it
// after each node — TaskState satisfies rhizome.Snapshotter. Every field is
// exported and JSON-friendly (strings, json.RawMessage, and a map[string]any
// of prompt fragments), so this is a straight round-trip with no
// hand-maintained schema to drift out of sync with the struct.
func (s *TaskState) MarshalBinary() ([]byte, error) {
	return json.Marshal(s)
}

// UnmarshalBinary restores state produced by MarshalBinary. rhizome calls it
// on a fresh *TaskState during Resume, then continues from the next node.
func (s *TaskState) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, s)
}

// clone returns an independent copy of the state safe for a fan-out branch to
// mutate concurrently: the Artifacts and NodeOutputs maps (and the Inputs
// slice) are deep-copied so branches never share mutable backing storage.
// Scalar fields are copied by value.
func (s *TaskState) clone() *TaskState {
	if s == nil {
		return nil
	}
	cp := *s
	if s.Artifacts != nil {
		cp.Artifacts = make(map[string]any, len(s.Artifacts))
		maps.Copy(cp.Artifacts, s.Artifacts)
	}
	if s.NodeOutputs != nil {
		cp.NodeOutputs = make(map[string]json.RawMessage, len(s.NodeOutputs))
		for k, v := range s.NodeOutputs {
			b := make(json.RawMessage, len(v))
			copy(b, v)
			cp.NodeOutputs[k] = b
		}
	}
	if s.Inputs != nil {
		b := make(json.RawMessage, len(s.Inputs))
		copy(b, s.Inputs)
		cp.Inputs = b
	}
	return &cp
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
