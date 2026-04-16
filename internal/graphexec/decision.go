package graphexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/jefflinse/toasters/internal/runtime"
)

// DecisionOutcome describes a single decision tool. When the LLM invokes the
// named tool, the executor sets TaskState.Status to the given value and stores
// the tool argument as an artifact. This mirrors the team-lead pattern where
// terminal decisions are tool calls (internal/operator/team_tools.go) rather
// than parsed text — robust, typed, and immune to substring-matching bugs.
type DecisionOutcome struct {
	ToolName    string // e.g. "decide_approved"
	Description string // tool description shown to the LLM
	Status      string // value assigned to state.Status when invoked
	ArtifactKey string // optional; artifact key for the decision message
	ArgField    string // JSON field holding the decision text; defaults to "message"
}

// decisionExecutor is a runtime.ToolExecutor that exposes decision tools
// scoped to a single TaskState. One instance per node invocation.
type decisionExecutor struct {
	state    *TaskState
	outcomes []DecisionOutcome
	decided  atomic.Bool
}

func newDecisionExecutor(state *TaskState, outcomes ...DecisionOutcome) *decisionExecutor {
	return &decisionExecutor{state: state, outcomes: outcomes}
}

func (d *decisionExecutor) Definitions() []runtime.ToolDef {
	defs := make([]runtime.ToolDef, 0, len(d.outcomes))
	for _, o := range d.outcomes {
		field := o.ArgField
		if field == "" {
			field = "message"
		}
		defs = append(defs, runtime.ToolDef{
			Name:        o.ToolName,
			Description: o.Description,
			Parameters: json.RawMessage(fmt.Sprintf(`{
				"type": "object",
				"properties": {
					"%s": {"type": "string", "description": "Explanation accompanying this decision."}
				},
				"required": ["%s"]
			}`, field, field)),
		})
	}
	return defs
}

func (d *decisionExecutor) Execute(_ context.Context, name string, args json.RawMessage) (string, error) {
	for _, o := range d.outcomes {
		if o.ToolName != name {
			continue
		}
		field := o.ArgField
		if field == "" {
			field = "message"
		}
		var parsed map[string]any
		if len(args) > 0 {
			if err := json.Unmarshal(args, &parsed); err != nil {
				return "", fmt.Errorf("parsing %s args: %w", name, err)
			}
		}
		msg, _ := parsed[field].(string)
		d.state.Status = o.Status
		if o.ArtifactKey != "" {
			d.state.SetArtifact(o.ArtifactKey, msg)
		}
		d.decided.Store(true)
		return fmt.Sprintf("Decision recorded: %s", o.Status), nil
	}
	return "", fmt.Errorf("%w: %s", runtime.ErrUnknownTool, name)
}

// Decided reports whether any decision tool was invoked during this node.
// Used by node wrappers to enforce the "must decide" safety net — matches
// runtime.watchTeamLeadForCompletion's force-fail on missing terminal action.
func (d *decisionExecutor) Decided() bool {
	return d.decided.Load()
}

// mergedTools unions two runtime.ToolExecutors. Execute tries primary first;
// if primary returns ErrUnknownTool, falls back to secondary. Definitions are
// concatenated. Used to combine a node's allowed tools with its decision tools.
type mergedTools struct {
	primary, secondary runtime.ToolExecutor
}

func mergeTools(primary, secondary runtime.ToolExecutor) runtime.ToolExecutor {
	return &mergedTools{primary: primary, secondary: secondary}
}

func (m *mergedTools) Definitions() []runtime.ToolDef {
	return append(m.primary.Definitions(), m.secondary.Definitions()...)
}

func (m *mergedTools) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	result, err := m.primary.Execute(ctx, name, args)
	if errors.Is(err, runtime.ErrUnknownTool) {
		return m.secondary.Execute(ctx, name, args)
	}
	return result, err
}

// Compile-time interface check.
var _ runtime.ToolExecutor = (*decisionExecutor)(nil)
var _ runtime.ToolExecutor = (*mergedTools)(nil)
