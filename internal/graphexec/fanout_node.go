package graphexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jefflinse/rhizome"

	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/workspace"
)

// fanoutBranchInput is the per-branch input type (B) for rhizome.Fanout. It
// pairs a forked state with a stable label used for the branch's session
// identity and TUI attribution.
type fanoutBranchInput struct {
	state *TaskState
	label string
}

// fanoutCandidate is one branch's output presented to an LLM reducer role.
// Index is the branch's split index — the value a selection judge echoes back
// as the winner, and the index of the workspace to promote.
type fanoutCandidate struct {
	Index  int             `json:"index"`
	Output json.RawMessage `json:"output"`
}

// candidatesArtifact is the artifact key under which an LLM reducer role
// receives the JSON array of branch candidates.
const candidatesArtifact = "fanout.candidates"

// buildFanoutNode compiles a Fanout definition into a rhizome NodeFunc. It also
// returns the role whose output schema describes the node's output (so routers
// reading $node.output.field can be validated at compile time), or nil when the
// output is not a single role's schema (the collect strategy).
func buildFanoutNode(graphID string, n Node, cfg TemplateConfig, registry *RoleRegistry) (rhizome.NodeFunc[*TaskState], *prompt.Role, error) {
	f := n.Fanout
	fanoutID := n.ID

	// registry.Build is the existence gate (errors when a role is neither
	// registered nor in the prompt engine). The prompt-engine lookup here is
	// only for the access decision; an unresolved role defaults to read-only.
	branchRole := roleByName(cfg, f.Branch.Role)
	isWrite := branchRole != nil && !isReadOnlyAccess(normalizeAccess(branchRole.Access))

	branchFn, err := registry.Build(f.Branch.Role, fanoutID, f.Branch.Slots, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("fanout node %q: branch: %w", fanoutID, err)
	}

	var judgeRole *prompt.Role
	var judgeFn rhizome.NodeFunc[*TaskState]
	if f.Reduce.Role != "" {
		judgeRole = roleByName(cfg, f.Reduce.Role) // for merge-mode schema only; may be nil
		judgeFn, err = registry.Build(f.Reduce.Role, fanoutID+".judge", nil, cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("fanout node %q: reduce role: %w", fanoutID, err)
		}
	}

	// A mechanical collect can't pick a single winner to promote, so it cannot
	// drive a write-role fan-out.
	if isWrite && f.Reduce.Strategy == ReduceCollect {
		return nil, nil, fmt.Errorf("fanout node %q: reduce strategy %q cannot select a branch to promote for write role %q; use first_success, majority, or a reduce role", fanoutID, ReduceCollect, f.Branch.Role)
	}

	count := f.Count

	// Per-execution isolation handle, set by split and consumed after the
	// fan-out returns. Safe because a compiled node executes sequentially
	// within a single task's graph run (each task compiles its own graph;
	// loop re-entry is sequential), so split and the cleanup defer never race.
	var iso struct {
		dirs    []string
		cleanup func()
	}

	split := func(_ context.Context, s *TaskState) ([]fanoutBranchInput, error) {
		iso.dirs, iso.cleanup = nil, nil
		if isWrite {
			dirs, cleanup, err := workspace.Isolate(s.WorkspaceDir, count)
			if err != nil {
				return nil, fmt.Errorf("isolate workspace: %w", err)
			}
			iso.dirs, iso.cleanup = dirs, cleanup
		}
		inputs := make([]fanoutBranchInput, count)
		for i := range count {
			fork := s.clone()
			if isWrite {
				fork.WorkspaceDir = iso.dirs[i]
			}
			inputs[i] = fanoutBranchInput{state: fork, label: fmt.Sprintf("%s#%d", fanoutID, i)}
		}
		return inputs, nil
	}

	branch := func(ctx context.Context, in fanoutBranchInput) (json.RawMessage, error) {
		bctx := branchContext(ctx, in.label, in.state)
		emitNodeStarted(bctx, in.state, in.label)
		out, runErr := branchFn(bctx, in.state)
		emitNodeCompleted(bctx, in.state, in.label, out, runErr)
		if runErr != nil {
			return nil, runErr
		}
		raw := out.GetNodeOutput(in.label)
		if len(raw) == 0 {
			return nil, fmt.Errorf("branch %q produced no output", in.label)
		}
		return raw, nil
	}

	reduce := func(ctx context.Context, s *TaskState, results []rhizome.BranchResult[json.RawMessage]) (*TaskState, error) {
		out, winner, err := reduceBranches(ctx, cfg, s, fanoutID, f, isWrite, judgeFn, results)
		if err != nil {
			return s, err
		}
		if isWrite && winner >= 0 && winner < len(iso.dirs) {
			if err := workspace.Promote(iso.dirs[winner], s.WorkspaceDir); err != nil {
				return s, fmt.Errorf("promote winning branch %d: %w", winner, err)
			}
		}
		return applyOutput(ctx, s, fanoutID, out)
	}

	var opts []rhizome.FanoutOption
	if f.MaxParallel > 0 {
		opts = append(opts, rhizome.WithFanoutConcurrency(f.MaxParallel))
	}
	if f.OnError == OnErrorFailFast {
		opts = append(opts, rhizome.WithFanoutCancelOnError())
	}
	fanoutFn := rhizome.Fanout(split, branch, reduce, opts...)

	node := func(ctx context.Context, s *TaskState) (*TaskState, error) {
		defer func() {
			if iso.cleanup != nil {
				iso.cleanup()
			}
		}()
		return fanoutFn(ctx, s)
	}

	// The role whose schema describes this node's output, for router
	// validation: collect produces a wrapper shape (none); a read-only LLM
	// reducer merges into the judge's output; everything else promotes/returns
	// a branch output.
	schemaRole := branchRole
	switch {
	case f.Reduce.Strategy == ReduceCollect:
		schemaRole = nil
	case f.Reduce.Role != "" && !isWrite:
		schemaRole = judgeRole
	}
	return node, schemaRole, nil
}

// reduceBranches dispatches to the configured reducer and returns the node's
// output, the winning branch index (-1 when there is no single winner), and an
// error. A winner >= 0 is the index of the workspace to promote for write
// branches.
func reduceBranches(ctx context.Context, cfg TemplateConfig, s *TaskState, fanoutID string, f *Fanout, isWrite bool, judgeFn rhizome.NodeFunc[*TaskState], results []rhizome.BranchResult[json.RawMessage]) (json.RawMessage, int, error) {
	if f.Reduce.Role != "" {
		return reduceByRole(ctx, s, fanoutID, isWrite, judgeFn, results)
	}
	switch f.Reduce.Strategy {
	case ReduceCollect:
		return reduceCollect(results)
	case ReduceFirstSuccess:
		return reduceFirstSuccess(results)
	case ReduceMajority:
		return reduceMajority(results, f.Reduce.Key, f.Quorum)
	default:
		return nil, -1, fmt.Errorf("unknown reduce strategy %q", f.Reduce.Strategy)
	}
}

// reduceCollect wraps every successful branch output in {"branches":[…]}.
func reduceCollect(results []rhizome.BranchResult[json.RawMessage]) (json.RawMessage, int, error) {
	outs := make([]json.RawMessage, 0, len(results))
	for _, r := range results {
		if r.Err == nil {
			outs = append(outs, r.Value)
		}
	}
	if len(outs) == 0 {
		return nil, -1, fmt.Errorf("collect: all %d branches failed", len(results))
	}
	wrapped, err := json.Marshal(map[string]any{"branches": outs})
	if err != nil {
		return nil, -1, fmt.Errorf("collect: marshal: %w", err)
	}
	return wrapped, -1, nil
}

// reduceFirstSuccess returns the first branch (in split order) that did not
// error.
func reduceFirstSuccess(results []rhizome.BranchResult[json.RawMessage]) (json.RawMessage, int, error) {
	for i, r := range results {
		if r.Err == nil {
			return r.Value, i, nil
		}
	}
	return nil, -1, fmt.Errorf("first_success: all %d branches failed", len(results))
}

// reduceMajority votes on the value at key across successful branch outputs.
// The plurality value wins; quorum (when > 0) is the minimum agreement count.
// The winner is the first branch carrying the winning value, and its full
// output becomes the node's output.
func reduceMajority(results []rhizome.BranchResult[json.RawMessage], key string, quorum int) (json.RawMessage, int, error) {
	type tally struct {
		count   int
		winner  int
		winnerV json.RawMessage
	}
	tallies := map[string]*tally{}
	order := []string{}
	for i, r := range results {
		if r.Err != nil {
			continue
		}
		v, err := extractField(r.Value, key)
		if err != nil {
			return nil, -1, fmt.Errorf("majority: branch %d: %w", i, err)
		}
		vj, err := json.Marshal(v)
		if err != nil {
			return nil, -1, fmt.Errorf("majority: branch %d: encode %q: %w", i, key, err)
		}
		k := string(vj)
		t, ok := tallies[k]
		if !ok {
			t = &tally{winner: i, winnerV: r.Value}
			tallies[k] = t
			order = append(order, k)
		}
		t.count++
	}
	if len(order) == 0 {
		return nil, -1, fmt.Errorf("majority: all %d branches failed", len(results))
	}
	// Pick the highest count; ties broken by first-seen order (deterministic).
	best := tallies[order[0]]
	for _, k := range order[1:] {
		if tallies[k].count > best.count {
			best = tallies[k]
		}
	}
	if quorum > 0 && best.count < quorum {
		return nil, -1, fmt.Errorf("majority: winning value on %q had %d votes, below quorum %d", key, best.count, quorum)
	}
	return best.winnerV, best.winner, nil
}

// reduceByRole runs an LLM reducer role over the branch outputs. For write-role
// branches it is a selection judge: its output must carry a "winner" index
// (the branch to promote), and the node's output is that winning branch's
// output. For read-only branches it is an aggregator: the node's output is the
// judge's own merged output.
func reduceByRole(ctx context.Context, s *TaskState, fanoutID string, isWrite bool, judgeFn rhizome.NodeFunc[*TaskState], results []rhizome.BranchResult[json.RawMessage]) (json.RawMessage, int, error) {
	cands := make([]fanoutCandidate, 0, len(results))
	for _, r := range results {
		if r.Err == nil {
			cands = append(cands, fanoutCandidate{Index: r.Index, Output: r.Value})
		}
	}
	if len(cands) == 0 {
		return nil, -1, fmt.Errorf("reduce role: all %d branches failed", len(results))
	}
	candsJSON, err := json.Marshal(cands)
	if err != nil {
		return nil, -1, fmt.Errorf("reduce role: marshal candidates: %w", err)
	}

	judgeLabel := fanoutID + ".judge"
	judgeState := s.clone()
	judgeState.SetArtifact(candidatesArtifact, string(candsJSON))

	jctx := branchContext(ctx, judgeLabel, judgeState)
	emitNodeStarted(jctx, judgeState, judgeLabel)
	judged, runErr := judgeFn(jctx, judgeState)
	emitNodeCompleted(jctx, judgeState, judgeLabel, judged, runErr)
	if runErr != nil {
		return nil, -1, fmt.Errorf("reduce role: %w", runErr)
	}
	judgeOut := judged.GetNodeOutput(judgeLabel)
	if len(judgeOut) == 0 {
		return nil, -1, fmt.Errorf("reduce role: judge produced no output")
	}

	if !isWrite {
		// Aggregator: the merged output is the node's output.
		return judgeOut, -1, nil
	}

	// Selection judge: read the winning branch index and promote/return it.
	wv, err := extractField(judgeOut, "winner")
	if err != nil {
		return nil, -1, fmt.Errorf("reduce role: judge output must include a \"winner\" index: %w", err)
	}
	winner, ok := asIndex(wv)
	if !ok {
		return nil, -1, fmt.Errorf("reduce role: judge \"winner\" must be an integer index, got %T", wv)
	}
	for _, r := range results {
		if r.Index == winner {
			if r.Err != nil {
				return nil, -1, fmt.Errorf("reduce role: judge chose failed branch %d", winner)
			}
			return r.Value, winner, nil
		}
	}
	return nil, -1, fmt.Errorf("reduce role: judge chose out-of-range branch %d", winner)
}

// asIndex coerces a JSON-decoded number into a non-negative branch index.
func asIndex(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		i := int(n)
		if float64(i) == n && i >= 0 {
			return i, true
		}
	case json.Number:
		if i, err := n.Int64(); err == nil && i >= 0 {
			return int(i), true
		}
	}
	return 0, false
}

// roleByName resolves a role from the prompt engine, or nil when no engine is
// configured (tests that stub roles through the registry).
func roleByName(cfg TemplateConfig, name string) *prompt.Role {
	if cfg.PromptEngine == nil {
		return nil
	}
	return cfg.PromptEngine.Role(name)
}

// normalizeAccess lowercases and trims a role's access value for comparison.
func normalizeAccess(a string) string {
	return strings.ToLower(strings.TrimSpace(a))
}

// branchContext derives a per-branch (or per-judge) NodeContext from the
// fan-out node's context so each sub-session gets its own identity and the
// TUI does not interleave them.
func branchContext(ctx context.Context, label string, state *TaskState) context.Context {
	sessionID := "graph:" + state.TaskID + ":" + label
	if nc := NodeContextFromContext(ctx); nc != nil {
		child := *nc
		child.Node = label
		child.SessionID = sessionID
		return context.WithValue(ctx, nodeContextKey{}, &child)
	}
	return context.WithValue(ctx, nodeContextKey{}, &NodeContext{
		JobID:     state.JobID,
		TaskID:    state.TaskID,
		Node:      label,
		SessionID: sessionID,
	})
}

// emitNodeStarted / emitNodeCompleted broadcast per-branch lifecycle events so
// each branch shows up as its own unit in the TUI. No-ops without a sink.
func emitNodeStarted(ctx context.Context, state *TaskState, label string) {
	if nc := NodeContextFromContext(ctx); nc != nil && nc.Sink != nil {
		nc.Sink.BroadcastGraphNodeStarted(state.JobID, state.TaskID, label)
	}
}

func emitNodeCompleted(ctx context.Context, state *TaskState, label string, out *TaskState, err error) {
	nc := NodeContextFromContext(ctx)
	if nc == nil || nc.Sink == nil {
		return
	}
	status := "completed"
	switch {
	case err != nil:
		status = "failed"
	case out != nil && out.Status != "":
		status = out.Status
	}
	nc.Sink.BroadcastGraphNodeCompleted(state.JobID, state.TaskID, label, status)
}
