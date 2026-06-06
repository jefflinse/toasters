# Fan-out / Fan-in — Homogeneous Parallel Execution (Design)

Status: **decided, not yet built.** This captures the design conversation that
settled how parallelism enters rhizome and toasters. The rhizome-layer sections
should be lifted into `rhizome/docs/fanout.md` when work starts there; this is the
integrating-repo source of truth in the meantime.

Companion: rhizome's `docs/parallel_execution.md` — the earlier analysis of the
*heterogeneous* (graph-level multi-pointer) feature and why it was deferred. This
doc is the *homogeneous* answer that supersedes "defer everything."

---

## The decision

Build homogeneous fan-out/fan-in as a **node-local combinator** (`rhizome.Fanout`),
**not** graph-level multi-pointer parallelism. Surface it in toasters as a **third
node binding** (`fanout`) alongside `role` and `graph`. Defer heterogeneous-parallel
(concurrent *named* nodes with divergent onward routing) until a real use case forces
it — per the gate rhizome's own `parallel_execution.md` already set.

This is additive (~150 LOC in rhizome), touches neither `graph.go` nor `compiled.go`,
and introduces no breaking changes.

---

## Decisions ratified (2026-06-04)

The API decisions are settled. Start coding against these.

1. **Type parameters** — full `Fanout[S, B, R]`. All three infer from the closures, so
   call sites need no type annotations. **No convenience wrappers** in v1 (a
   `Consensus`/`MapReduce` helper is additive later if call sites prove noisy).
2. **Concurrency** — `WithFanoutConcurrency(n int)`, **local per-fanout** (deadlock-free
   under nesting). The hard ceiling — *"no more than N parallel LLM calls at a time"* —
   is a **Toasters-side guarantee**, enforced as a single shared semaphore acquired
   around the actual provider/LLM call and released immediately after, sized by config.
   It does **not** live in rhizome. rhizome's local cap is only goroutine/memory hygiene.
3. **Dependencies** — **stdlib only**, in both rhizome and mycelium. No `errgroup`/`x/sync`.
   Implement with `sync.WaitGroup` + a buffered-channel semaphore + an index-aligned
   results slice (each goroutine owns one index → no mutex; results land in split order,
   deterministic).
4. **Error semantics** — *default (run-all):* `reduce` is always called with every
   `BranchResult` (errors included). *`WithFanoutCancelOnError` (fail-fast):* first branch
   error cancels siblings and is returned directly; `reduce` is **not** called. Empty
   split → `reduce(ctx, state, nil)`; no special-casing.

---

## Conceptual model: three orthogonal axes

"Parallelism" was conflating three independent things. Keep them separate:

1. **Who authors the graph, and when** — static (authored + compiled) vs dynamic
   (built/extended at runtime).
2. **How many nodes run at once** — sequential (one pointer) vs parallel.
3. **When parallel, same work or different work** — homogeneous vs heterogeneous.

Key consequence: **"dynamic task generation" (the Claude-Code-style on-the-fly task
list) is NOT a mutable graph.** It is dynamic-width *homogeneous* fan-out: a planner
node emits N tasks (data), a fixed map node runs over them. The graph never mutates,
so every rhizome guarantee (compile-time validation, immutable `CompiledGraph`, safe
concurrent `Run`) is preserved. Dynamism lives in the **data** and in **subgraph
composition**, never in graph mutation.

rhizome already does heterogeneous *structure* (a graph is heterogeneous nodes) — it
just runs it *sequentially*. The two missing cells are:

|                         | Sequential        | Parallel                                  |
| ----------------------- | ----------------- | ----------------------------------------- |
| **Homogeneous**         | a loop (have it)  | **`Fanout` — missing, cheap (build this)**|
| **Heterogeneous**       | static graphs (have it) | **concurrent named nodes — missing, expensive (defer)** |

---

## Homogeneous vs heterogeneous — the dividing line

- **Homogeneous**: branch outputs fold into a list and fan in to **one** join.
  Differences between branches (prompt, temperature, model, even which role) are
  **data/config**, not graph structure. "I want to *see* them separately" is an
  observability concern solved by **labels**, not by making them graph nodes.
  → expressible as `Fanout`.
- **Heterogeneous (the expensive feature)**: needed **iff** concurrent branches must
  route **onward to different downstream nodes** — an *asymmetric* fan-in, not a
  single shared join. That is the **one true tell**. Everything else (different work,
  different output, wanting to watch them) does NOT cross the line.

Diagnostic to keep: *if all branches rejoin at one reducer/aggregator, it's
homogeneous and `Fanout` handles it.* The day a branch needs a different next node
based on what it found, that's the signal to build the heterogeneous executor.

---

## Why a combinator, not an executor change

rhizome's `parallel_execution.md` priced graph-level fan-out at "roughly doubles the
codebase," via five problem areas. A homogeneous fan-out as a `NodeFunc[S]` makes all
five evaporate, because from the outer graph's view it is still **one node, one
pointer**:

1. **Graph model** — no new edge type; `AddNode` a fanout like any node.
2. **State fork/merge** — handled inside the combinator by explicit `split`/`reduce`;
   merge is localized **at the join** (the choice already preferred in
   `parallel_execution.md` over LangGraph's schema-spread reducers).
3. **Executor as scheduler** — no; the `errgroup` lives inside the combinator,
   `execute()` stays a `for` loop.
4. **Error/cancellation policy** — a fan-out option, scoped to the one node.
5. **Cycle detection across branches** — N/A: if a branch is a subgraph `Run`, each
   gets its own fresh `execCounts`; the outer graph sees one node execution.

---

## rhizome API (library layer)

```go
// BranchResult carries one branch's outcome so reduce can decide what "success"
// means — essential for voting/quorum over unreliable models.
type BranchResult[R any] struct {
    Index int
    Value R
    Err   error
}

// Fanout returns a NodeFunc[S] that splits S into N items, runs `branch` over each
// concurrently (bounded), and folds the results back into S.
func Fanout[S, B, R any](
    split  func(context.Context, S) ([]B, error),
    branch func(context.Context, B) (R, error),
    reduce func(context.Context, S, []BranchResult[R]) (S, error),
    opts ...FanoutOption,
) NodeFunc[S]

func WithFanoutConcurrency(n int) FanoutOption   // semaphore over branch goroutines
func WithFanoutCancelOnError() FanoutOption      // opt-in fail-fast
```

Semantics / rules:

- **Real concurrency.** All branches launch as goroutines via `errgroup`; "homogeneous"
  describes *what* runs, not *how* — they run in parallel.
- **Default is run-all-then-reduce, NOT fail-fast.** For an ensemble of flaky local
  models you want to let some branches fail and still reduce over the survivors.
  Fail-fast is opt-in.
- **`branch` is `NodeFunc`-shaped**, so pass `subgraph.Run`. This gives per-branch
  retry/timeout/recover middleware and arbitrary nesting **for free**.
- **Fan-in is a barrier**: `reduce` waits for all branches. Quorum / early-exit
  (cancel stragglers once K succeed) is an explicit opt-in policy, not free.
- **`split` must hand out independent payloads** — two branches mutating a shared
  pointer is a data race the combinator cannot prevent.

---

## toasters surface (consumer layer)

A node becomes a third kind of binding — `fanout` — beside `role` and `graph`. It
still writes `NodeOutputs[id]`, so `exit:` and every `$node.output.field` router keep
working unchanged.

```yaml
- id: implement
  fanout:
    # SPLIT — one of:
    count: 3                          #   N identical branches (consensus)
    # branches: [ {role: coder, temperature: 0.2}, {role: coder, temperature: 0.8} ]
    #                                 #   explicit list; per-branch role/graph/temp/model/slots
    # over: $plan.output.subtasks     #   dynamic width: one branch per array element

    branch:                           # used with count/over (single binding)
      role: coder                     #   role: ... or graph: ... (graph => nesting)
      slots: { toolchain: "{{ globals.task.toolchain }}" }

    reduce:                           # FAN-IN — one of:
      strategy: vote                  #   built-in fold: vote / collect / first-success
      # role: judge                   #   an LLM reducer node (needed for prose / judgment)

    max_parallel: 2                   # LOCAL goroutine cap; defaults to global runtime cap
    quorum: 2
    on_error: continue                # continue | fail_fast
```

Compilation: a `fanout` node compiles to one `rhizome.Fanout(...)`:
- **split** ← `count` copies, or `over` via the existing `parsePath` against
  `NodeOutputs`, or the explicit `branches` list.
- **branch** ← the same role builder used for `RoleNode` (or a subgraph `Run`),
  inheriting per-branch middleware.
- **reduce** ← a built-in Go fold over `[]BranchResult`, or a reducer `RoleNode` whose
  input is the collected branch outputs and whose output is schema-validated.

Important semantic: **`vote`/`quorum` only work on structured, comparable outputs**
(e.g. a `review-decision {approved: bool}`). Prose (a coder `summary`) cannot be
mechanically voted — use `reduce: role: <judge>`. Fan-in is an explicit decision.

Typed contracts chain through cleanly:
`branch-output` → reducer consumes `[]branch-output` → node output = reducer output.

---

## Nesting = subgraph composition

A fanout's `branch` can be a subgraph; that subgraph can contain its own fanout.
Recursion is free because each fanout is one opaque node to its parent.

Worked example — `review` fans over lenses {security, perf, style}; each lens runs
the **same reusable inner graph** that itself fans out over two temperatures and
consolidates:

```yaml
# dual-temp-review.yaml (inner, reusable; parameterized by lens via input)
id: dual-temp-review
entry: review
exit: review
input_schema: { lens: string }
nodes:
  - id: review
    fanout:
      branches:
        - { role: code-reviewer, temperature: 0.2 }
        - { role: code-reviewer, temperature: 0.8 }
      slots: { toolchain: "{{ globals.task.toolchain }}", lens: "{{ input.lens }}" }
      reduce: { role: findings-consolidator }   # merge near-dup findings, same lens
      max_parallel: 2
edges:
  - { from: review, to: end }
```

```yaml
# outer node, in new-feature.yaml
- id: review
  fanout:
    over: $globals.review_lenses
    branch:
      graph: dual-temp-review                    # branch is a subgraph (nesting point)
      input: { lens: "{{ item }}" }              # fanned item populates subgraph input
    reduce: { role: review-aggregator }          # assemble 3 lenses → review-decision
    max_parallel: 3
```

Two reducers, different jobs: `findings-consolidator` dedupes near-duplicate findings
from the same lens at two temperatures; `review-aggregator` assembles different
concerns into a decision.

---

## Concurrency under nesting — CRITICAL

Nesting **multiplies** concurrency: outer `max_parallel: 3` × inner `max_parallel: 2`
= up to **6** concurrent model calls in `review`. Two levels and the M4 OOMs.

Per-fanout `max_parallel` is a **local** cap (bounds goroutines within one fanout); it
does **not** compose into a global ceiling. The real hardware ceiling must be a
**single global semaphore enforced where the model call actually happens** —
toasters' runtime/session layer — threaded through the entire execution tree:

- **rhizome `max_parallel`** bounds branch *goroutines* locally (so a 500-item `over`
  doesn't spawn 500 goroutines).
- **toasters runtime** owns the global concurrent-**model-call** cap
  (`runtime.max_parallel_branches`). Every provider call passes through it regardless
  of fan-out depth. Goroutines are cheap; gate the expensive thing.

Per-node `max_parallel` should **default to the global cap** so graphs stay portable:
M4 today → M5 Ultra later is a one-config-value bump, not a graph rewrite. This global
gate is exactly the mechanism the "granular local nodes" thesis depends on — it's what
lets you fan out fearlessly.

---

## vs LangGraph (orientation)

LangGraph made the opposite bet: parallelism **in the executor** (Pregel super-steps +
channel reducers). Native homogeneous *and* heterogeneous parallel, but pays with a
heavier execution model, merge logic smeared across the state schema (a foot-gun), and
weaker static typing (Python). `Fanout` ≈ **LangGraph's `Send` map-reduce, Go-native
and compile-checked, with merge-at-the-join — minus the super-step machinery and minus
native heterogeneous parallel.** The right 90% subset for a legible one-person Go
project. The only scenario that genuinely favors LangGraph is asymmetric onward
routing (the heterogeneous tell above).

---

## Build order

1. **rhizome `Fanout` primitive — DONE** (branch `feat/fanout`, uncommitted).
   `Fanout[S, B, R](split, branch, reduce, opts...)` + `BranchResult[R]` +
   `WithFanoutConcurrency(n)` + `WithFanoutCancelOnError()`, stdlib-only
   (`sync.WaitGroup` + buffered-channel semaphore + index-aligned results). Branch
   panics recover into `BranchResult.Err` wrapping `ErrNodePanic`. 13 table tests:
   order preservation, concurrency bound (incl. serial-at-1 and parallel-when-
   unbounded), run-all error collection, cancel-on-error without reduce, empty
   split, split error, caller cancellation, subgraph-as-branch, panic recovery in
   both modes. Full suite race-clean. This is the generic foundation; the toasters
   YAML surface below compiles down to it.
2. **Live with it on a real workload** before going further (honor the
   `parallel_execution.md` gate).
3. `over` (dynamic-width map) + `{{ item }}` binding + `reduce: role` aggregators.
4. `branch.graph` nesting + the **global concurrency gate** in the runtime.
5. Heterogeneous-parallel **only if** a real asymmetric-routing case appears, designed
   against that concrete case.

## Toasters integration — "workspace isolation first" (IMPLEMENTED, 2026-06-05)

Built and verified (24 packages pass, race-clean). Note: toasters `go.mod` carries a
temporary `replace github.com/jefflinse/rhizome => ../rhizome` until rhizome's `Fanout`
is tagged/released; remove it and bump the require then. Not yet committed.

Decision: build per-branch workspace isolation up front so write-role fan-out
(consensus *coders*, not just read-only reviewers) works. Key finding driving this:
the `ToolExecutor` is scoped to one `WorkspaceDir` per task (`executor.go`), and all
branches share it — so parallel write roles would clobber each other's files.

Phased (all done):

- **Phase A — tools-by-workspace.** Replace `TemplateConfig.ToolExecutor` (fixed) with
  `ToolExecutorFor(workspaceDir string) runtime.ToolExecutor`. `RoleNode` scopes tools
  to `state.WorkspaceDir`. Isolation then reduces to "a forked branch sets its own
  `WorkspaceDir`," and tool scoping follows. One production call site (`nodes.go:42`).
- **Phase B — `internal/workspace` primitive.** `Isolate(base, n)` (git
  `worktree add --detach` when base is a repo, else recursive copy), `Promote(winner,
  base)` (mirror winner's working files back over base), cleanup. Unit-tested.
- **Phase C — `fanout` node.** Config (`count`, `branch.role`, `reduce`, `max_parallel`,
  `quorum`, `on_error`) + compiler + validation. **Isolation is gated on the branch
  role's `access:`** — read-only branches share the workspace (no copy); write/test
  branches isolate per branch and promote the winner. split forks `TaskState` (+ isolated
  `WorkspaceDir` when writing); branch runs the role scoped to its fork; reduce selects a
  winner, promotes its workspace, cleans up. Per-branch session identity threaded through
  `NodeContext` so the TUI doesn't interleave branches.
- **Phase D — graphs + tests.** `new-feature.yaml` *is* the fan-out pipeline now (so
  feature work actually exercises it — graph selection is by description/tag, and a
  separate demo graph just sat dormant next to the plain one). `implement` is a 2-coder
  consensus with a `code-judge` reduce role; `review` is a 3-reviewer `majority` on
  `approved`. Backed by the `code-judge` role + `branch-selection` schema. The
  `new-feature` run-through test drives the 8-call fan-out sequence.

  Note on runtime delivery: bundled defaults are embedded (`defaults/embed.go`) and
  **user-level** defaults are seeded to `~/.config/toasters/user/` only on first run
  (system files re-sync every start; user files don't, to protect customizations). So a
  `defaults/user/...` change reaches a running install only after a rebuild **and** a
  fresh `~/.config/toasters` (wipe-and-reseed).

---

## Spec: per-branch overrides — diverse consensus (next increment)

**Motivation.** Today a fan-out is N *identical* branches (same role, same temperature),
so consensus is *redundant* — N copies of the same call, useful mainly against
nondeterminism. Per-branch overrides make it *diverse*: branches that differ by
temperature, model, or even role, then judged/voted. Diversity is what makes an
ensemble worth more than one call — especially on local models, where a temperature
spread genuinely explores different solutions.

Headline use case: "fan out implement to 3 coders at temperatures 0.2 / 0.7 / 1.1,
judge picks the best."

### Config

`FanoutBranch` gains override fields, usable in **both** fan-out forms:

```go
type FanoutBranch struct {
    Role        string            `yaml:"role"`
    Slots       map[string]string `yaml:"slots,omitempty"`
    Temperature *float64          `yaml:"temperature,omitempty"` // nil = role/global default
    Thinking    *bool             `yaml:"thinking,omitempty"`    // nil = role/global default
    Model       string            `yaml:"model,omitempty"`       // "" = inherit cfg.Model
}
```

Two mutually-exclusive forms on `Fanout`:
- `count: N` + `branch: <spec>` — N identical branches (the spec's overrides apply to all N).
- `branches: [<spec>, …]` — an explicit per-branch list (new field `Branches []FanoutBranch`).

```yaml
implement:
  fanout:
    branches:
      - { role: coder, temperature: 0.2 }
      - { role: coder, temperature: 0.7 }
      - { role: coder, temperature: 1.1 }
    reduce: { role: code-judge }
    max_parallel: 2
```

### Override precedence (the one real seam)

`branch spec > role frontmatter > graph-global default`. Implemented by adding
`TemperatureOverride *float64` and `ThinkingOverride *bool` to `TemplateConfig`, applied
with top precedence in `effectiveWorkerDefaults`:

```go
if cfg.ThinkingOverride != nil    { thinking = *cfg.ThinkingOverride }
if cfg.TemperatureOverride != nil { temperature = *cfg.TemperatureOverride }
```

`Model` needs no new field — `RoleNode` already uses `cfg.Model` directly (no role-level
model override exists), so a per-branch `cfg.Model` is already authoritative. Each branch
gets a shallow `TemplateConfig` copy with its overrides set; `tunedProvider` already
injects temperature/thinking per `ChatStream`, so there is **no provider-layer change**.

### Compiler / execution

- Normalize both forms to `[]resolvedBranch{role, slots, cfg}`: `count`+`branch` → N copies
  of one spec; `branches` → the list as-is.
- Build a `NodeFunc` per resolved branch via `registry.Build(role, fanoutID, slots, branchCfg)`.
- `fanoutBranchInput` carries its function: `{state, label, fn}`. `split` assigns each input
  its branch's `fn` + forked state (+ isolated workspace when writing); the `branch` closure
  just calls `in.fn`.
- **Isolation:** `isWrite = ANY branch is write-access` → isolate all branches, promote the
  winner's workspace. (Same-role diverse-temp = all write = isolate all — the common case.)
- **Router schema:** if all branches share one role, register that role's output schema for
  `$node.output.field` validation; mixed roles → skip (like `collect`).

### Validation

- Exactly one of `{count>=1 with branch}` or `{branches non-empty}`; both set → error.
- Each branch's `role` required.
- `temperature`, if set, in `[0, 2]`.
- `reduce` / `quorum` / `max_parallel` / `on_error` unchanged.

### Reduce

Unchanged — the judge/vote now sees genuinely *diverse* candidates, which is the point.
(Also makes mechanical `majority` more meaningful: a temperature spread over a structured
decision yields real votes rather than near-identical outputs.)

### Tests

- `branches` form compiles + runs; each branch's effective temperature equals its override
  (assert via a stub provider that records `req.Temperature` per call).
- `count`+`branch` with a `temperature` applies it to all N.
- Override beats role frontmatter (a role with a frontmatter temperature is overridden).
- Mixed-role branches: isolation = any-write; schema validation skipped.
- Validation: both forms set → error; empty branch role → error; temp out of `[0,2]` → error.

### Decisions (recommended)

1. **Include `model` and `thinking` overrides now**, not just `temperature` — nearly free
   given the seam, and unlocks cross-model ensembles (a local coder + a cloud coder, judged).
2. **Support mixed-role branches** (isolation any-write, schema validation skipped) rather
   than restricting to same-role — the headline case is same-role, but mixed is free here.
3. **Validate `temperature` ∈ [0, 2]** at parse time for a clear early error.

### Effort

Moderate, bounded, **no rhizome change**: definition types + validation, `buildFanoutNode`
refactor (resolve branch list, per-branch cfg + `NodeFunc`, any-write isolation, per-branch
`fanoutBranchInput.fn`), `TemplateConfig` override fields + `effectiveWorkerDefaults`
precedence, tests.

Reducers v1 (mechanical): `collect` (wrap branch outputs in `{branches:[…]}`),
`majority` (vote on a `key:` field, honoring `quorum`), `first_success`. Code-consensus
can't use `majority` (no voting on diffs) → use `first_success` or a `key`-scored pick;
an LLM `reduce.role` judge is the next increment, so the reducer interface is shaped to
drop it in. `over` dynamic-width and `branch.graph` nesting remain later increments.

---

## Open wiring question (settle before/while coding step 3–4)

How the fanned `{{ item }}` populates a subgraph branch's **input** — i.e. how the
forked per-branch `TaskState` carries the item into the inner graph's input namespace.
This is the one new seam between fanout and subgraph; everything else reuses existing
machinery (`state.go`, `nodes.go`, `parsePath`, the role builders).

One ergonomic fork also pending: is `fanout` a third **node binding** (as written
here), or a distinct top-level construct? Decided: node binding — keeps the compiler
and validator changes minimal and slots into the existing `role`/`graph` switch.
