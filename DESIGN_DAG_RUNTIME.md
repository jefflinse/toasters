# DAG-Based Worker Orchestration Runtime

Status: **brainstorming** — not yet a committed design.

## Motivation

Toasters is being optimized for **local, resource-constrained LLMs running on-device**. This changes the design constraints fundamentally compared to cloud-hosted premium models:

- **Small context windows (4-8K effective)** — can't accumulate long conversation histories. Each worker step needs a fresh, focused context with only its relevant inputs.
- **No cost-per-token** — deep worker hierarchies (depth >> 1) become viable. The "single layer of spawned workers" restriction in mainstream tools is an economic constraint, not an architectural one.
- **Limited inference concurrency** — GPU memory constrains parallel model calls to 1-2 at a time, even though Go can spawn thousands of goroutines.
- **24/7 autonomous execution** — slower inference is fine if orchestration, safeguards, resiliency, and quality checks are reliable enough.

The current architecture has the operator LLM acting as both **planner** (decomposing jobs into tasks) and **executor** (routing between tasks, handling transitions). This works, but:

- Mechanical transitions ("task A done, start task B") waste LLM calls
- Serial-only task execution leaves concurrency on the table
- Single-depth worker spawning limits what a task can express
- Failure recovery is ad-hoc (the `watchTeamLeadForCompletion` safety net)
- Conversation history grows unboundedly within sessions

## Core Idea

**Separate planning from execution.** The operator tier becomes a *compiler* that transforms high-level goals into executable DAGs. A mechanical *executor* runs the DAGs using Go's concurrency primitives.

```
User Goal
    │
    ▼
┌──────────────────────────┐
│  Operator Tier (LLM)     │
│  ┌────────────────────┐  │
│  │ Clarify & solidify │  │
│  │ requirements       │  │
│  └────────┬───────────┘  │
│           ▼              │
│  ┌────────────────────┐  │
│  │ Decompose into     │  │
│  │ Tasks + DAGs       │  │
│  └────────┬───────────┘  │
│           ▼              │
│  ┌────────────────────┐  │
│  │ Assign DAGs to     │  │
│  │ teams              │  │
│  └────────────────────┘  │
└──────────┬───────────────┘
           │  One DAG per Task
           ▼
┌──────────────────────────┐
│  Graph Executor          │
│  (pure Go, no LLM calls │
│   for routing)           │
│                          │
│  Goroutines + channels   │
│  Semaphore for GPU slots │
│  Checkpoint to SQLite    │
└──────────────────────────┘
```

## Design Principle: Structured Contracts, Not Chat

LLM calls are API calls with fuzzy implementations. The orchestration layer should treat them that way.

- **Markdown is for humans. JSON is for the robots.** Inter-node communication uses structured data with defined schemas, not prose or chat transcripts.
- **Every node has an input schema and an output schema.** The executor validates artifacts at edge boundaries before passing them to the next node. A node that produces malformed output is a node that failed — same as a 400 from an API.
- **Structured output from LLMs is the enforcement mechanism.** Local models (e.g., Gemma 4 via llama.cpp / vLLM) support constrained decoding / JSON mode. The node implementation uses this to guarantee the LLM's output conforms to the output schema. No parsing, no "try to extract JSON from markdown" — the model is constrained to produce valid output.
- **The graph is a pipeline of typed transformations.** Each node is `f(InputSchema) -> OutputSchema`. Edges carry typed artifacts. The executor is a type-checked pipeline runner.

This is a deliberate rejection of the "workers chat with each other in natural language" paradigm. Workers don't chat. They receive structured inputs, do work, and produce structured outputs. The LLM is a reasoning component inside the node, not a conversational participant in the graph.

## Worker Response Contract

Every worker node produces exactly one of three response types. No exceptions, no ambiguity.

### 1. Work Completed

The worker had sufficient context, did the work, and produced the requested outcome. Side effects (file writes, shell commands) may have occurred.

```json
{
  "status": "completed",
  "result": { ... }
}
```

The `result` field conforms to the node's declared output schema. The executor validates it before propagating to downstream nodes.

### 2. Additional Context Required

The worker determined it lacks sufficient information to complete the task. Rather than guessing or making a best-effort attempt, it **fails fast** and declares exactly what it needs.

```json
{
  "status": "needs_context",
  "required": [
    {"key": "auth_pattern", "description": "How does the existing codebase handle API authentication?"},
    {"key": "db_schema", "description": "Schema definition for the users table"}
  ]
}
```

The executor routes this upward: the parent node can either satisfy the request (re-invoke with enriched inputs) or propagate `needs_context` further up the graph. This bubbles naturally until a node can provide the answer or it reaches the root and becomes a question to the user.

**No side effects are committed on `needs_context`.** See "Side Effects and Isolation" below.

### 3. Error

The worker encountered an unrecoverable failure — tool access denied, catastrophic runtime error, etc. This is not "I don't know how" (that's `needs_context`), this is "something broke."

```json
{
  "status": "error",
  "error": {"code": "tool_failed", "message": "...", "detail": { ... }}
}
```

The executor checks the node's retry policy. If retriable, re-run. If not, escalate per the failure model.

### Why These Three and Only Three

This is the same contract as any well-designed API: success (200), client needs to fix the request (4xx), or server failure (5xx). The worker is the server. The parent is the client. The executor is the HTTP framework routing between them.

By constraining workers to this envelope, the executor can make routing decisions mechanically — no LLM call needed to interpret what a worker "meant" by its output. `status` is a discriminator. The graph advances on `completed`, retries on `needs_context`, and escalates on `error`.

## Side Effects and Isolation

Worker nodes that perform coding tasks produce side effects (writing files, running commands). These side effects must be scoped to prevent dirty state on non-success outcomes. Same principle as a database transaction: do your work, and the coordinator decides whether to commit or rollback.

### Two-layer isolation model

Side-effect isolation uses two complementary mechanisms: **Afero filesystem overlays** for Go-level file I/O and **git scratch branches** for shell tool execution.

#### Layer 1: Afero overlay (in-process file I/O)

Each worker node receives an Afero `CopyOnWriteFs`: reads fall through to the real filesystem, writes are captured in an in-memory overlay.

```go
base := afero.NewBasePathFs(                       // sandbox to workspace
    afero.NewReadOnlyFs(afero.NewOsFs()),           // real FS, read-only
    workspacePath,
)
overlay := afero.NewMemMapFs()                      // in-memory write layer
workerFs := afero.NewCopyOnWriteFs(base, overlay)   // union: reads fall through, writes captured
```

This provides:

- **Instant rollback.** On `needs_context` or `error`, discard the `MemMapFs`. No cleanup, no branches to delete, no disk I/O.
- **Changeset as artifact.** The overlay *is* the diff — walk it to enumerate exactly what the worker wrote. This can flow as a structured artifact to downstream nodes (quality gates, review nodes) without running `git diff`.
- **Workspace sandboxing.** `BasePathFs` restricts the worker's view to its designated workspace. It literally cannot see or touch files outside that boundary. Critical for 24/7 autonomous execution.
- **Fan-out conflict detection.** Parallel workers each get their own overlay on the same base. After fan-in, the executor diffs overlays against each other *before* flushing to disk. If two workers wrote the same file, the conflict is detected at the overlay level and escalated — no git merge to unwind.

#### Layer 2: Git scratch branch (shell tool execution)

Shell tools (`go build`, `go test`, linters, etc.) operate on the real filesystem — they can't read an Afero overlay. For workers that need to compile or test, the executor also creates a git scratch branch.

**Workflow when shell tools are needed:**

1. Worker writes files through Afero overlay
2. Before shell tool execution, executor flushes the overlay to the scratch branch on disk
3. Shell tool runs against real filesystem on the scratch branch
4. On `completed`: scratch branch merged forward (fast-forward or squash)
5. On `needs_context` or `error`: scratch branch discarded

**Workflow when shell tools are NOT needed** (e.g., pure file generation, analysis, review):

1. Worker writes files through Afero overlay
2. On `completed`: overlay flushed to disk
3. On `needs_context` or `error`: overlay discarded (zero-cost)

The executor decides which workflow to use based on the node's declared tool requirements. Nodes that only need file read/write stay entirely in the Afero layer. Nodes that need shell execution get the full git branch treatment.

#### Testing

The entire isolation model is testable without touching disk. In tests, replace `OsFs` with a second `MemMapFs` prepopulated with fixture files. The executor, overlay logic, conflict detection, and flush/discard paths all exercise against in-memory filesystems.

### Fan-out isolation

When multiple workers run in parallel (fan-out), each gets its own overlay (and scratch branch, if needed) forked from the same base. The fan-in node is responsible for merging. If workers modify the same files, the fan-in merge may conflict — this is a graph-level error, not a node-level error, and gets escalated to the operator for DAG revision.

### Open questions (isolation)

- **Flush granularity**: should the overlay flush to disk once before the first shell tool call, or incrementally as the worker writes? Incremental is more complex but avoids a large flush blocking tool execution.
- **Non-filesystem side effects**: file I/O is covered, but what about network calls, database writes, or other side effects? These are out of scope for Afero. For now, assume workers with non-FS side effects declare them and the executor handles them case-by-case.
- **Overlay size limits**: a worker that generates large files could exhaust memory in the `MemMapFs`. May need a `MemMapFs` with a size budget, falling back to a temp-dir-backed FS if exceeded.

## Primitives

### Artifact Schemas

Artifacts are typed, schema-validated data that flows between nodes.

```go
// Schema defines the expected shape of artifacts at an edge boundary.
type Schema struct {
    Fields []FieldDef // name, type, required, description
}

// Artifacts is a validated map of named values.
type Artifacts struct {
    data   map[string]any
    schema *Schema // nil = unvalidated (escape hatch, not default)
}

func (a *Artifacts) Validate() error { ... }
```

Each node declares its contract:

```go
// WorkerResult is the universal response envelope.
type WorkerResult struct {
    Status   Status          // completed | needs_context | error
    Result   *Artifacts      // non-nil only when Status == completed
    Required []ContextNeed   // non-nil only when Status == needs_context
    Error    *WorkerError    // non-nil only when Status == error
}

type ContextNeed struct {
    Key         string // machine-readable identifier
    Description string // what the worker needs and why
}

type NodeDef struct {
    Name         string
    InputSchema  *Schema
    OutputSchema *Schema       // validates Result when Status == completed
    Resources    ResourceReqs
    RetryPolicy  *RetryPolicy  // max attempts, backoff, retriable error codes
    Run          func(ctx context.Context, in Artifacts) WorkerResult
}
```

The executor validates `OutputSchema` against `Result` only on `completed`. A `completed` response with malformed output is promoted to `error` — not silent corruption downstream. The `Run` function never returns a Go `error` — all failure modes are expressed through `WorkerResult`.

### Node

A unit of work with a typed contract. The key abstraction — must be generic enough to extract as a library.

An LLM worker session is one implementation. Others include:
- Shell commands (compile, test, lint)
- Pure Go functions (file transforms, artifact merging)
- Quality gates (review output against requirements)
- Human-in-the-loop checkpoints

### Edge

A directed connection from one node's output to another node's input. May be:
- **Unconditional** — always traverse
- **Conditional** — traverse based on predicate over source node's output artifacts
- **Cycle** — back-edge for retry/revision loops (e.g., "review rejected → re-implement")

### Graph

A directed graph of nodes and edges. One graph per Task. Supports:
- Fan-out (one node feeds multiple parallel successors)
- Fan-in (multiple predecessors must complete before a node starts)
- Cycles (for quality-gate retry loops)
- Conditional branching

### Executor

Runs a graph to completion. Responsibilities:
- Topological scheduling (respecting edges and fan-in)
- Concurrency control via semaphore (separate pools for "needs GPU" vs "CPU-only")
- Checkpoint/resume: persist node state to SQLite at each boundary
- Scoped rollback on failure (roll back to failed node's predecessor, not root)
- Partial result preservation (completed subgraphs are not re-executed on resume)
- Timeout/budget enforcement per node and per graph
- Escalation to operator on unrecoverable failure

## How This Maps to Go Concurrency

| Concept | Go primitive |
|---|---|
| Node execution | Goroutine |
| Edge (data flow) | Channel carrying `Artifacts` |
| Fan-out | Spawn N goroutines, each receiving from source channel |
| Fan-in | `sync.WaitGroup` + merge artifacts |
| Conditional edge | `switch` on artifacts after node returns |
| Cycle (retry) | Loop in executor, re-run node with feedback artifacts |
| GPU concurrency limit | `semaphore.Weighted` (from `golang.org/x/sync`) |
| Cancellation | `context.WithCancel` propagated through subgraph |
| Checkpoint | Write `NodeState` to SQLite at completion boundary |

## Context Window as a Design Driver

The DAG structure directly addresses context window constraints:

- **Each node gets a fresh context.** Input artifacts are structured data, not conversation history. A node receives "here are the files to modify, here's the requirement, here's the test output from the previous node" — not a 50-turn conversation.
- **Graph edges are natural compression boundaries.** The output of a node is a structured artifact (file paths, test results, summaries), not a raw LLM transcript. This is forced context compression at every step.
- **Deep hierarchies don't compound context.** A node at depth 5 has the same context budget as a node at depth 1 — it just receives its specific inputs.

## Quality Gates and Cycles

With local models, explicit verification steps are critical. The graph should support patterns like:

```
┌──────────┐     ┌──────────┐     ┌──────────┐
│ Implement │────▶│   Test   │────▶│  Review  │
└──────────┘     └──────────┘     └────┬─────┘
      ▲                                │
      │          rejected              │
      └────────────────────────────────┘
                 (cycle with feedback)
```

The review node outputs either `{approved: true}` or `{approved: false, feedback: "..."}`. On rejection, the executor routes back to the implementation node with the feedback injected as input artifacts. This is a conditional back-edge — a first-class graph primitive, not an ad-hoc retry.

## Failure Model

Deep hierarchies require structured failure handling:

- **Node failure** — executor checks retry policy. If retriable, re-run with same inputs. If not, mark failed and check graph-level policy.
- **Subgraph failure** — roll back to the subgraph entry point. Completed sibling subgraphs are preserved.
- **Graph failure** — escalate to operator with structured error context. Operator can revise the DAG and resubmit, or fail the Task.
- **Deadlock detection** — executor tracks which nodes are blocked on which inputs. If a cycle has no node making progress, break it and escalate.

## Resource-Aware Scheduling

Nodes declare resource requirements:

```go
type ResourceReqs struct {
    NeedsGPU    bool  // requires a model inference slot
    MaxMemoryMB int   // optional memory budget hint
}
```

The executor maintains separate semaphores:
- **GPU slots** — typically 1-2 for local inference. LLM worker nodes acquire a slot before starting.
- **CPU workers** — higher limit. Shell commands, file operations, test runs.

This means a graph with 5 nodes can have 1 LLM node running + 4 test/compile nodes running simultaneously, fully utilizing the machine.

## Library Extraction Boundary

The generic runtime (potential OSS library):
- Graph definition (nodes, edges, conditions)
- Executor (scheduling, concurrency, checkpointing)
- Node interface
- Artifact types
- Resource semaphores
- Checkpoint/resume protocol

Toasters-specific (stays in this repo):
- Operator tier (LLM-driven planning/decomposition)
- LLM worker node implementation
- Tool definitions and executors
- Team/worker/skill definitions
- TUI, server, SSE
- Provider abstraction

## Open Questions

- **How does the operator construct DAGs?** Tool calls (`define_dag` with nodes/edges)? Structured output parsed into a graph? A DSL in the task description?
- **How granular are nodes?** "Implement auth middleware" (goal-oriented, LLM figures out how) vs. "edit file X, run test Y" (prescriptive, LLM not needed). Probably the former — nodes are goals, not instructions.
- **What happens to the team lead role?** Possibly dissolves into the graph executor. Or becomes a lightweight error-recovery node. Or stays as a "coordinator node" that can dynamically extend the graph.
- **How do cycles interact with checkpointing?** If a review→implement cycle runs 3 times, which checkpoint is authoritative?
- **Inter-graph communication?** If Task A's graph produces artifacts needed by Task B's graph, how does that flow? Through the operator? Through a shared artifact store?
- ~~**What is the artifact type system?** Fully untyped (`map[string]any`)? Tagged unions? Schema-validated?~~ **Resolved: schema-validated.** Every node declares input/output schemas. The executor validates at edge boundaries. LLM nodes use constrained decoding to guarantee schema conformance.
