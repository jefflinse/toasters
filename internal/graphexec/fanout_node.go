package graphexec

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jefflinse/rhizome"

	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/workspace"
)

// fanoutBranchInput is the per-branch input type (B) for rhizome.Fanout. It
// pairs a forked state with the branch's NodeFunc (each branch may have its own
// role/temperature/model) and a stable label used for the branch's session
// identity and TUI attribution.
type fanoutBranchInput struct {
	state *TaskState
	label string
	fn    rhizome.NodeFunc[*TaskState]
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

	// Resolve the branch specs into a uniform list. The count form is N copies
	// of one spec; the branches form is the explicit list. Each spec may carry
	// per-branch temperature/thinking/model overrides applied via branchConfig.
	specs := f.Branches
	if len(specs) == 0 {
		specs = make([]FanoutBranch, f.Count)
		for i := range specs {
			specs[i] = *f.Branch
		}
	}

	// registry.Build is the existence gate (errors when a role is neither
	// registered nor in the prompt engine). The prompt-engine lookup is only
	// for the access decision; an unresolved role defaults to read-only.
	// Isolation is needed when ANY branch writes — a writer makes the shared
	// workspace unsafe for every concurrent branch.
	branchFns := make([]rhizome.NodeFunc[*TaskState], len(specs))
	isWrite := false
	for i, spec := range specs {
		fn, err := registry.Build(spec.Role, fanoutID, spec.Slots, branchConfig(cfg, spec))
		if err != nil {
			return nil, nil, fmt.Errorf("fanout node %q: branch %d (role %q): %w", fanoutID, i, spec.Role, err)
		}
		branchFns[i] = fn
		if role := roleByName(cfg, spec.Role); role != nil && !isReadOnlyAccess(normalizeAccess(role.Access)) {
			isWrite = true
		}
	}
	count := len(specs)

	var judgeRole *prompt.Role
	var judgeFn rhizome.NodeFunc[*TaskState]
	if f.Reduce.Role != "" {
		judgeRole = roleByName(cfg, f.Reduce.Role) // for merge-mode schema only; may be nil
		jfn, jerr := registry.Build(f.Reduce.Role, fanoutID+".judge", nil, cfg)
		if jerr != nil {
			return nil, nil, fmt.Errorf("fanout node %q: reduce role: %w", fanoutID, jerr)
		}
		judgeFn = jfn
	}

	// A mechanical collect can't pick a single winner to promote, so it cannot
	// drive a fan-out whose branches write.
	if isWrite && f.Reduce.Strategy == ReduceCollect {
		return nil, nil, fmt.Errorf("fanout node %q: reduce strategy %q cannot select a branch to promote for write branches; use first_success, majority, or a reduce role", fanoutID, ReduceCollect)
	}

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
			inputs[i] = fanoutBranchInput{state: fork, label: fmt.Sprintf("%s#%d", fanoutID, i), fn: branchFns[i]}
		}
		return inputs, nil
	}

	branch := func(ctx context.Context, in fanoutBranchInput) (json.RawMessage, error) {
		bctx := branchContext(ctx, in.label, in.state)
		emitNodeStarted(bctx, in.state, in.label)
		out, runErr := in.fn(bctx, in.state)
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
	// validation: the common branch role when all branches share one (else
	// none); collect produces a wrapper shape (none); a read-only LLM reducer
	// merges into the judge's output.
	schemaRole := commonBranchRole(cfg, specs)
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

// judgeMaxAttempts bounds how many times an LLM reduce role is retried before
// falling back to a mechanical pick. A flaky local judge should not throw away
// the (expensive) branch work by failing — and retrying — the whole node.
const judgeMaxAttempts = 2

// reduceByRole runs an LLM reducer role over the branch outputs. For write-role
// branches it is a selection judge: its output carries a "winner" index (the
// branch to promote) and the node's output is that winning branch's output. For
// read-only branches it is an aggregator: the node's output is the judge's own
// merged output.
//
// The judge is retried up to judgeMaxAttempts times; if it still fails to
// produce a usable verdict, reduceByRole falls back to first_success so the
// successful branch outputs are not discarded.
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
	for attempt := 1; attempt <= judgeMaxAttempts; attempt++ {
		judgeState := s.clone()
		judgeState.SetArtifact(candidatesArtifact, string(candsJSON))

		jctx := branchContext(ctx, judgeLabel, judgeState)
		emitNodeStarted(jctx, judgeState, judgeLabel)
		judged, runErr := judgeFn(jctx, judgeState)
		emitNodeCompleted(jctx, judgeState, judgeLabel, judged, runErr)

		if out, winner, ok := interpretJudge(isWrite, judged, runErr, judgeLabel, results); ok {
			return out, winner, nil
		}
		slog.Warn("fanout judge attempt failed",
			"fanout", fanoutID, "attempt", attempt, "max", judgeMaxAttempts, "error", runErr)
	}

	// The judge is unreliable; keep the branch work rather than failing the
	// node (which would re-run every branch) — fall back to a mechanical pick.
	slog.Warn("fanout judge unreliable after retries; falling back to first_success",
		"fanout", fanoutID)
	return reduceFirstSuccess(results)
}

// interpretJudge extracts the node output and winning branch index from a judge
// run, or ok=false when the run errored or produced an unusable verdict (no
// output, a missing/invalid winner, or a winner pointing at a failed branch).
func interpretJudge(isWrite bool, judged *TaskState, runErr error, judgeLabel string, results []rhizome.BranchResult[json.RawMessage]) (json.RawMessage, int, bool) {
	if runErr != nil || judged == nil {
		return nil, -1, false
	}
	judgeOut := judged.GetNodeOutput(judgeLabel)
	if len(judgeOut) == 0 {
		return nil, -1, false
	}
	if !isWrite {
		// Aggregator: the merged output is the node's output.
		return judgeOut, -1, true
	}
	wv, err := extractField(judgeOut, "winner")
	if err != nil {
		return nil, -1, false
	}
	winner, ok := asIndex(wv)
	if !ok {
		return nil, -1, false
	}
	for _, r := range results {
		if r.Index == winner {
			if r.Err != nil {
				return nil, -1, false
			}
			return r.Value, winner, true
		}
	}
	return nil, -1, false
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

// branchConfig returns a copy of cfg with the branch spec's per-branch
// overrides applied. Temperature/Thinking become top-precedence overrides (they
// beat role frontmatter); Model replaces the graph model when set.
func branchConfig(cfg TemplateConfig, spec FanoutBranch) TemplateConfig {
	bcfg := cfg
	if spec.Temperature != nil {
		bcfg.TemperatureOverride = spec.Temperature
	}
	if spec.Thinking != nil {
		bcfg.ThinkingOverride = spec.Thinking
	}
	if strings.TrimSpace(spec.Model) != "" {
		bcfg.Model = spec.Model
	}
	return bcfg
}

// commonBranchRole returns the role shared by every branch spec, or nil when
// the branches use different roles (their outputs may have different schemas,
// so the node's output schema is not a single role's).
func commonBranchRole(cfg TemplateConfig, specs []FanoutBranch) *prompt.Role {
	if len(specs) == 0 {
		return nil
	}
	first := specs[0].Role
	for _, s := range specs[1:] {
		if s.Role != first {
			return nil
		}
	}
	return roleByName(cfg, first)
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
