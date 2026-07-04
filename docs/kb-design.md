# Knowledge Base — design

Status: **proposal**, revised 2026-07-03 after design feedback. This revision
introduces the three-scope model and the **storage split** (files for job scope,
vectors for system/user) that supersedes the earlier vector-for-everything design.
The near-term execution plan (job notes first) lives in the plan file
`~/.claude/plans/linear-plotting-feigenbaum.md`; this doc is the authoritative
design it points back to.

## Motivation

Today a worker's only memory is its own conversation context; when a graph node
finishes, what it learned evaporates. Nothing is shared across a fleet mid-job, and
nothing survives a job. The Knowledge Base gives the orchestrator durable,
inspectable memory that stateless workers reach through tool calls — a direct
extension of the project's spine: **Go owns the state; the LLM is a stateless tool
invoked with accumulated context.**

## Scopes

Three scopes, each with a distinct owner, audience, and lifetime:

| Scope | Written by | Read by | Lifetime |
|---|---|---|---|
| **system** | Go (auto-populated) | operator | as long as the fact holds |
| **user** | human (Knowledge screen) + operator (promotion) | operator **and workers** | durable, survives jobs |
| **job** | workers (their own job) | workers (their own job) + operator | with the job workspace |

- **system** — what toasters itself, independent of the user, remembers about the
  running system so the operator can give a better experience: available
  providers/models, defined roles, endpoints, hardware/context limits, environment
  facts. **Go-populated and read-only to the LLMs** — the operator reads it to reason
  about its own capabilities; nothing an LLM says writes here.
- **user** — durable facts *the user* wants to persist beyond any job: instructions,
  heuristics, preferences, conventions. Authored by the human (via the Knowledge
  screen) or promoted by the operator from a job note. Read by the operator **and by
  workers** (heuristics/instructions are often exactly what a worker needs).
- **job** — shared working memory for one fleet. Workers on job J write findings and
  read each other's, without threading everything through the operator. Isolated:
  job J never sees job K's notes.

## Storage model (the key decision)

Scope drives storage — they are **not** one mechanism:

- **system + user → a system-wide vector store** (semantic search). These are
  low-churn, durable, and benefit from embedding recall. Being system-wide (not
  per-job) is what makes a pluggable `VectorStore` backend worthwhile.
- **job → Markdown files in the job workspace.** Not vectors. This is deliberate:
  - **Portable** — copy a job workspace to another machine running a *different or
    absent* embedding model and the notes still work. Vectors don't travel; files do.
  - **Model-agnostic & non-brittle** — job memory isn't tied to an embedding model's
    identity or dimensionality.
  - **Inspectable & organizable** — plain `.md` you can `cat`, grep, and diff.
  - **Concurrency-safe** — see the write model below; independent files sidestep the
    single-writer SQLite contention that a hot per-worker write path would create.

**Hybrid job retrieval (best of both).** Files are always the source of truth.
*If* an embedding model is configured, job notes are *also* encoded into a local
vector index for semantic recall, with structural (list/read/grep) search as the
always-available fallback. The index is a **rebuildable cache** over the files — it
doesn't need to travel (re-encode on the new machine, or fall back to structural).
This is an additive upgrade to the same `job_notes_search` tool, not a rewrite, and
lands **after** the file-based version proves out.

## Relationship to the typed graph

The project deliberately moved to *typed, deterministic wiring* between nodes
(roles declare output schemas, edges are typed, decision tools were retired). A
fuzzy vector store that any worker queries — silently injecting ~7.5KB of
maybe-relevant natural language into an agent loop — is, on its face, exactly the
implicit cross-worker channel the graph model replaced. This tension is real and
worth stating rather than papering over with "Go owns the state."

The reconciliation: **the KB is an advisory hint channel, never part of the
correctness contract.** Typed edges remain the sole deterministic dataflow; a node
must produce its schema-valid output whether or not the KB returned anything (see
[Failure behavior](#failure-behavior) — a KB outage degrades to "memory
unavailable", and the node proceeds on its typed inputs). The KB does not wire
nodes together, does not gate edges, and carries no authority. It is opt-in
per-role, and its value is measured, not assumed (see the eval). If the eval shows
it doesn't help, it costs nothing to leave a role's `kb_*` tools un-advertised. In
short: typed handoff is the skeleton; the KB is optional scratch/reference memory
hanging off it, not a second, untyped dataflow graph.

**Kill switch.** The extension of "advisory, never the contract" is that the whole
feature can be turned off. `kb.enabled` (config, default **on** for now during
build/testing) simply stops advertising the KB tools and hides the Knowledge
screen. toasters runs identically with the KB disabled.

## Observability

KB activity is surfaced through the service event stream, following the project's
display side-channel convention (`FileChangeNotifier`, `ShellExecNotifier`,
`WorkerSpawnNotifier` in `internal/runtime/tools.go`) — never a raw tool-result
line. A `KBNotifier` emits a structured **`session.kb`** event on **both write and
query** (scope, job, op, source, preview/hit-count). This lets the operator and the
TUI see memory activity as it happens, and feeds the Knowledge screen.

**Knowledge screen.** A full-screen TUI view (mirroring the `ctrl+g` nodes screen,
bound to a free key such as `ctrl+k`) with two parts: a browsable/editable view of
**user + system facts** (the vector store — and the place the human *authors* user
facts and promotes job notes upward), and a read/inspect view of the current job's
**notes** (the files). This screen is the human's write surface for user scope,
which closes the governance loop. Because the TUI is a remote client, it reads notes
through a **service read path** (server reads the files/store, returns DTOs), not by
touching the filesystem directly.

---

# Part A — Job notes (files) · built first

The lowest-risk, most portable piece; no embeddings required. This is the first
epic (see plan file for the PR-by-PR breakdown).

### Location

Notes live at a **constant relative path under `workDir`**: `.toasters/notes/`.
`workDir` already *is* the per-job directory `<workspace_dir>/<job-uuid>/` (created
at `internal/operator/system_tools.go:337`), shared by every worker on the job. The
path is **not** derived from `ct.jobID` — that's empty in the graph-node path
(`internal/graphexec/executor.go:341` never sets session context); the fixed subdir
resolved through `resolvePath` lands in whatever per-job `workDir` is active. The
dot-namespace keeps notes out of the worker's actual deliverables and leaves room
for a co-located `.toasters/notes.db` vector index later.

### Write model: immutable entries

Each write mints a **new** timestamped, role-stamped file, so concurrent fleet
writers never clobber each other:

`<UTC-yyyymmdd-hhmmss.mmm>-<role-or-workerid>-<slug>-<6hex>.md`

(sub-second + random suffix guarantees uniqueness). `slug` = sanitized title.
Notes are immutable — to revise, write a new one; retrieval surfaces the latest.

### Tools (`CoreTools`)

Three dedicated tools (not raw file access — the tool abstraction is what gives us
control, events, and the future semantic upgrade), mirroring existing file
handlers and reusing the `resolvePath` (`internal/runtime/tools.go:518`) confinement
choke point and `displayPath` (`:591`, no absolute-path leaks):

- **`job_note_write(title, content)`** — mirror `writeFile` (`:785`); creates the
  stamped file; fires `KBNotifier`; returns `note saved as <id>`.
- **`job_notes_search(query, top_k?)`** — structural now (grep over `.toasters/notes/`,
  mirroring `grepFiles` `:1019`); returns **top-k (default 5), snippet-truncated**
  `{id, title, snippet, age}` to stay under the 8KB `maxToolResultBytes` cap
  (`internal/runtime/session.go:281`). Empty query → most-recent (acts as list).
  Later: vector-rank when an embedding model is configured — same tool.
- **`job_note_read(id)`** — mirror `readFile` (`:725`); `resolvePath` rejects
  traversal in `id`.

Registered in the `Execute` switch (`:223`) and `Definitions()` (`:315`), gated on
`kb.enabled`; included in the base tool set every worker role gets
(`toolsForRole`, `internal/runtime/nodes.go:~454`), deniable per-role via the
existing denylist.

### Lifecycle

Job notes are files in the job workspace, so their lifetime **follows the
workspace** — they persist as long as the job dir exists and travel when it's
copied. No separate purge machinery. (Fan-out caveat: branch workers run in isolated
workspace copies, so a branch's notes are branch-local until the winning branch is
promoted back — normal file semantics via `internal/workspace`.)

---

# Part B — Vector store (system + user) · later epic

Semantic memory for the two durable scopes. Depends on an embedding capability that
doesn't exist yet.

### Embeddings seam (`internal/provider`)

The mycelium `Provider` interface is chat-only, so embeddings ride on a new optional
interface implemented by the concrete OpenAI-compatible provider:

```go
type EmbeddingProvider interface {
    Embed(ctx context.Context, model string, inputs []string) ([][]float32, error)
}
```

`*OpenAIProvider.Embed` POSTs to `/v1/embeddings` (a helper mirroring `modelsURL`),
like `fetchOpenAIModels` (`openai.go:510`).

**Resolution through the scheduler-wrapped registry (review B1).** Every provider is
wrapped in `*provider.Scheduler` (`cmd/serve.go:563`); `registry.Get` returns the
chat-only `Provider`, so a direct `.(EmbeddingProvider)` assertion **always fails**.
Fix: `*Scheduler` gains an `Embed()` that acquires a slot and proxies to
`s.inner.(EmbeddingProvider)`, so `registry.Get(name).(EmbeddingProvider)` succeeds.
The embed model is configured as **its own provider entry** (`KBConfig.Provider`) —
own endpoint/model, own scheduler + semaphore — so embed calls contend only with
other embeds, never with worker/operator generation on a single-slot local backend.
Embeddings stay local (`nomic-embed-text` etc.), zero cloud dependency.

**Configuration (how the embed model is selected).** Explicit config, **not
auto-detected** — the OpenAI-compatible `/v1/models` endpoint carries no
embedding-capability flag, so discovery is unreliable. `kb.provider` names a
provider entry (same registry as operator/workers); `kb.model` names the embedding
model. On startup, if both are set, a one-shot capability probe POSTs to
`/v1/embeddings` and **logs a warning if it fails**, so misconfig surfaces at boot,
not at first `kb_search`. Unset ⇒ vector features off, job notes still work
structurally. (Optional convenience: if `provider` set but `model` empty, pick the
first `/models` id matching an embed heuristic and log the choice.)

### Store — behind an interface (backend-swappable)

Because the vector store is **system-wide** (not per-job), it's worth an interface so
the SQLite default can give way to a real vector DB later:

```go
type VectorStore interface {
    Insert(ctx, entry VectorEntry) error
    Search(ctx, scope, queryVec []float32, k int) ([]Scored, error)
    // ...
}
```

Default impl: pure-Go **brute-force cosine over float32 BLOBs** in the shared
`toasters.db` (`internal/db`, mirroring the `graph_checkpoints` thin-view pattern) —
**no CGo / sqlite-vec** (hard `modernc.org/sqlite` constraint). Sub-millisecond at
the thousands-of-vectors scale of durable facts. Decoded embeddings are cached in
memory (invalidated on write) so searches don't re-scan the BLOB set on the
single-writer connection (review S2). `rank` asserts `len(query) == entry.dim` and
skips mismatches (review S4). Entries are Go-stamped with scope/source/model/time;
`model` is stored so a model swap filters cleanly.

### Config

```go
type KBConfig struct {
    Enabled  bool   `mapstructure:"enabled"`  // kill switch, default true
    Provider string `mapstructure:"provider"` // dedicated embed provider (own scheduler)
    Model    string `mapstructure:"model"`    // e.g. "nomic-embed-text"
    TopK     int    `mapstructure:"top_k"`    // default 5
}
```

`Enabled` gates the whole feature (job notes included). `Provider`/`Model` gate the
vector features specifically — unset ⇒ system/user semantic search and the hybrid
job index are simply not available; job notes still work structurally.

### Tool & governance surface

- **Workers** — `kb_search(query)` reads **user** facts (+ their own job's notes via
  the hybrid path). Workers **cannot** write user or system scope.
- **Operator** — `kb_search(query, scope?)` reads any scope (returns ids);
  `kb_write_user(content)` authors durable user facts; `kb_promote(id)` copies a
  vetted job note up to user scope. (The `id` in search results is what makes
  promotion reachable — review B2.)
- **system** — Go-populated, read-only to all LLMs.
- **Human** — authors/edits user facts via the Knowledge screen.

Governance: **operator-only writes to durable scope** (workers never write user or
system). A rogue worker can pollute only its own job's notes. Two caveats carried
from review: **user reads are shared across all jobs/workers** (so the *write* gate
is the only barrier — promotion must be deliberate), and any model-supplied metadata
is treated as untrusted display text, never as an authority signal.

### Value gate (eval)

Before building governance surface, a **minimal eval** (fixed queries,
known-relevant entries, precision@k) establishes that injecting top-k hits into a
small-context local loop *helps* rather than crowding out task context. Retrieval
quality is the go/no-go, not deferred as "tuning."

---

## Failure behavior

The embedding provider being unavailable (backend busy, model evicted from VRAM) is
the *common* case on a local box, not an edge:

- **Writes** must not silently lose data. Job-note writes are just files (no embed on
  the write path in the file model), so they're durable regardless; vector-index
  encoding is best-effort and retried, and the file remains the source of truth.
- **Search** returns an explicit "memory unavailable" (not an empty result that reads
  as "nothing known"), and the hybrid path falls back to structural grep so job
  search keeps working even with no embedder.
- Neither aborts the worker; typed edges remain the correctness contract.

## Sequencing

1. **Job notes (files)** — Part A. Three PRs: tools → `session.kb` events → Knowledge
   screen (+ service read path). See the plan file.
2. **Vector store (system/user)** — Part B. Embeddings seam → `VectorStore` +
   SQLite impl → operator/worker tools + governance → eval gate.
3. **Hybrid job index** — encode job notes into a local vector index when an embed
   model is configured; structural grep remains the fallback.

## Open decisions

1. **Durable-scope writes** — **RESOLVED: operator-only + promotion.** Workers never
   write user/system; the human authors user facts via the Knowledge screen; the
   operator promotes job notes upward.
2. **Job-notes lifecycle** — **RESOLVED: follows the workspace** (notes persist in the
   job dir, travel when copied, no separate purge). Larger workspace-management
   changes are planned separately; this interim behavior is sufficient.

## Architecture review trail (2026-07-03)

An independent architecture-critic pass on the *earlier* (vector-for-everything)
design produced the findings below. The **storage split in this revision** (files
for job, vectors only for durable system/user) dissolved several of them for the
job scope — noted inline:

- **B1 (blocker, fixed):** embedder couldn't resolve a provider through the
  scheduler-wrapped registry. Fixed via `*Scheduler.Embed()` + dedicated embed
  provider. *Still applies* to the vector store (Part B).
- **B2 (blocker, fixed):** operator had no `id` to promote. Fixed via ids in search
  results + operator `kb_search`. *Still applies* to promotion.
- **S1 (addressed):** untyped-memory-vs-typed-graph tension — justified in
  [Relationship to the typed graph](#relationship-to-the-typed-graph).
- **S2 (addressed / narrowed):** single-writer scan → in-memory embedding cache.
  *Now scopes to the vector store only*; job writes are files, off the DB entirely.
- **S3 (addressed):** embedder-down is the common local case →
  [Failure behavior](#failure-behavior); job search degrades to structural grep.
- **S4/S5 (narrowed):** dim-guard + dedup-index concerns *apply only to the vector
  store now*; the file-based job scope has neither embeddings nor a dedup index (the
  immutable-entry write model makes clobbering impossible by construction).
- **S6 (addressed):** eval as the go/no-go gate before governance surface.
- **S7 (addressed):** KB activity invisible → `session.kb` events on write **and**
  query + the Knowledge screen.

## Library-extraction note

If `internal/graphexec` is ever pulled out as a standalone OSS library, the vector
store should sit behind its `VectorStore` interface (already the plan) and job notes
behind a small notes-repository interface, so the executor stays LLM- and
storage-agnostic.
