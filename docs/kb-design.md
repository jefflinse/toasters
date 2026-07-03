# Knowledge Base (shared vector memory) — design

Status: **proposal** (2026-07-03). Two governance decisions are still open — see
[Open decisions](#open-decisions). Defaults below reflect the recommended choices.

## Motivation

Today a worker's only memory is its own conversation context; when a graph node
finishes, what it learned evaporates. Nothing is shared across a fleet mid-job,
and nothing survives a job. The knowledge base gives the orchestrator a durable,
searchable memory that stateless workers reach through tool calls — a direct
extension of the project's spine: **Go owns the state; the LLM is a stateless
tool invoked with accumulated context.** A KB entry is state Go owns; retrieval
is a pure function of a query embedding.

Two scopes:

- **`job`** — shared working memory for one fleet. Workers on job J write findings
  and read each other's, without threading everything through the operator.
- **`global`** — institutional memory that survives jobs (conventions, resolved
  gotchas, environment facts).

A worker in job J searching sees **global + job-J entries only**. Other jobs are
invisible — that isolation boundary is the containment guarantee.

## Non-goals (v1)

- Approximate-nearest-neighbor indexes (HNSW/IVF). Brute-force cosine over the
  full candidate set is sub-millisecond at the thousands-of-vectors scale we
  operate at; ANN is a later optimization gated on real volume.
- Cross-provider embedding portability. Entries are ranked only against the
  current embedding model (see [Model changes](#embedding-model-changes)).
- Semantic dedup. v1 dedups on exact content hash only.
- A CGo / `sqlite-vec` dependency. The pure-Go `modernc.org/sqlite` build is a
  hard constraint; vectors are ranked in Go, not SQL.

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
per-role, and its value is measured, not assumed (see the eval in Phase 2). If the
eval shows it doesn't help, it costs nothing to leave a role's `kb_*` tools
un-advertised. In short: typed handoff is the skeleton; the KB is optional
scratch/reference memory hanging off it, not a second, untyped dataflow graph.

## Observability (events)

A worker reading or writing *shared cross-worker memory* is at least as
display-worthy as a shell command, and the codebase already surfaces tool activity
through display side-channels that flow into the service event stream / SSE
(`FileChangeNotifier`, `ShellExecNotifier`, `WorkerSpawnNotifier` in
`internal/runtime/tools.go`). KB activity follows the same pattern: a `KBNotifier`
emits a structured `session.kb` event on write (scope, job, source, content
preview) and optionally on search (query, hit count). This feeds the `/kb` browser,
lets the operator see writes land (and is one way the operator/TUI learns which
entries exist to promote — ties into B2), and honors the project's "emit through
the service, not a side channel" convention rather than leaving KB activity as an
undifferentiated raw tool-result line.

## Architecture

Everything hangs off integration points that already exist (anchors are current
as of this writing):

| Piece | Location | Mirrors |
|---|---|---|
| Embeddings capability | `internal/provider`: `EmbeddingProvider` iface + `*OpenAIProvider.Embed()` | `fetchOpenAIModels` (`openai.go:510`) |
| Embedder (registry resolution) | `internal/kb` | provider lookup at `runtime.go:107` |
| Store + schema | `internal/db/kb_store.go` + `migrations/018_kb.sql` | `graph_checkpoints` (`checkpoints.go:22`) |
| Cosine ranking | `internal/kb` | — |
| Worker tools | `kb_search`/`kb_write` in `CoreTools` | `report_task_progress` (`tools.go:247`) |
| Operator tools | `kb_write_global`/`kb_promote` in `internal/operator` | operator tool set |
| Config | `KB KBConfig` on `Config` | `OperatorConfig` (`config.go:92`) |

### Data model

`migrations/018_kb.sql`:

```sql
CREATE TABLE IF NOT EXISTS kb_entries (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,            -- 'job' | 'global'
    job_id          TEXT,                     -- set iff scope='job'
    content         TEXT NOT NULL,
    content_hash    TEXT NOT NULL,            -- dedup key (sha256 of content)
    embedding       BLOB NOT NULL,            -- little-endian float32[dim]
    dim             INTEGER NOT NULL,
    model           TEXT NOT NULL,            -- embedding model that produced it
    source_session  TEXT,                     -- provenance (Go-stamped)
    source_role     TEXT,
    metadata_json   TEXT,                     -- optional model-supplied tags
    created_at      TIMESTAMP NOT NULL,
    ttl_expires_at  TIMESTAMP                 -- NULL = never expires (global)
);
CREATE INDEX IF NOT EXISTS idx_kb_scope_job ON kb_entries(scope, job_id);
-- COALESCE(job_id,'') because global rows have job_id=NULL, and SQLite treats
-- NULLs as distinct in a UNIQUE index — without this, global dedup silently
-- never fires (review S5).
CREATE UNIQUE INDEX IF NOT EXISTS idx_kb_dedup
    ON kb_entries(scope, COALESCE(job_id,''), content_hash);
```

`scope`, `job_id`, `source_session`, `source_role`, `model`, `created_at`, and
`ttl_expires_at` are **always set by Go**, never by the model. `metadata_json`
is the only model-influenced column and is treated as untrusted display data.

### Store (`internal/db/kb_store.go`)

Mirrors the checkpoint pattern — a thin view over the shared single-writer
`*sql.DB`, all SQL local to the file:

```go
type KBStore struct { db *sql.DB }
func (s *SQLiteStore) KBStore() *KBStore { return &KBStore{db: s.db} }

func (k *KBStore) Insert(ctx, entry KBEntry) error            // ON CONFLICT dedup no-op
func (k *KBStore) Candidates(ctx, scope, jobID, model) ([]KBEntry, error) // filtered rows for ranking
func (k *KBStore) PurgeExpired(ctx, now) (int, error)          // lazy TTL sweep
func (k *KBStore) DeleteByJob(ctx, jobID) error                // for the purge-on-completion option
```

`Candidates` returns rows matching `(scope='global' OR job_id=?)`, `model=<current>`,
and `(ttl_expires_at IS NULL OR ttl_expires_at > now)`. Ranking happens in Go.

### Ranking (`internal/kb`)

```go
func rank(query []float32, cands []KBEntry, k int) []Scored
```

Decode each `embedding` BLOB to `[]float32`, cosine-similarity against the query,
partial-sort top-k. Embeddings are L2-normalized at write time so cosine is a dot
product. Pure, table-testable, no I/O.

`rank` **must assert `len(query) == entry.dim`** and skip (or error on) mismatches
(review S4): model-name filtering catches a renamed model but not same-name /
different-dim (re-quant, two servers advertising `nomic-embed-text` at different
dims). A length mismatch otherwise panics or silently produces garbage similarity.

Decoded **global** embeddings are cached in memory (invalidated on any global
write) so every search across the fleet doesn't re-load and re-decode the full
global BLOB set from the single-writer connection (review S2). Job candidate sets
are small and loaded per search.

### Embeddings (`internal/provider`)

The mycelium `Provider` interface is chat-only, so embeddings ride on a new
optional interface implemented by the concrete OpenAI-compatible provider:

```go
type EmbeddingProvider interface {
    Embed(ctx context.Context, model string, inputs []string) ([][]float32, error)
}
```

`*OpenAIProvider.Embed` POSTs to `embeddingsURL(endpoint)` (a new helper mirroring
`modelsURL`, producing `/v1/embeddings`) with `Authorization: Bearer`, exactly like
`fetchOpenAIModels`.

**Resolution through the scheduler-wrapped registry (corrected — see B1 in the
review).** Every provider in the registry is wrapped in `*provider.Scheduler`
(`cmd/serve.go:563`), and `registry.Get` returns the chat-only `Provider`
interface. A type-assert straight to `EmbeddingProvider` therefore **always
fails** — the concrete `*OpenAIProvider` is unreachable behind the scheduler. So:

- `*Scheduler` gains an `Embed()` method that **acquires a scheduling slot** and
  proxies to `s.inner.(EmbeddingProvider)` (type-asserting the wrapped provider,
  which the scheduler *can* reach). `*Scheduler` thus implements `EmbeddingProvider`,
  and `registry.Get(name).(EmbeddingProvider)` succeeds.
- To avoid head-of-line blocking against chat generation on a single-slot local
  backend, **the embedding model is configured as its own provider entry** —
  `KBConfig.Provider` names a dedicated provider (its own endpoint/model, its own
  scheduler + semaphore). Embed calls then contend only with other embed calls,
  never with worker/operator generation. This is the deliberate capacity choice:
  respect the semaphore (don't fire unbounded POSTs at a single slot) but isolate
  the embed workload onto its own scheduler so the cost is bounded and off the
  generation path.

Embeddings stay **local** — `nomic-embed-text` or similar via LM Studio/Ollama —
with zero cloud dependency, consistent with the local-model campaign. If the
configured provider doesn't implement `EmbeddingProvider`, or `KBConfig` is unset,
the KB tools are simply not advertised (feature is opt-in). Runtime failures
(provider down, model not loaded) are handled per [Failure behavior](#failure-behavior),
not silently swallowed.

### Config

```go
type KBConfig struct {
    Provider string `mapstructure:"provider"` // dedicated embed provider (own scheduler)
    Model    string `mapstructure:"model"`    // e.g. "nomic-embed-text"
    TopK     int    `mapstructure:"top_k"`    // default 5
}
```

Added to `Config` with `viper.SetDefault`s. When `Provider`/`Model` are unset the
KB tools are simply not advertised — the feature is opt-in.

## Tool surface

### Worker tools (`CoreTools`)

- **`kb_search(query string, top_k? int)`** — available to all workers. Embeds the
  query, ranks global + current-job candidates, returns top-k. Each result is
  `{id, content(truncated ~1.5KB), score, source_role, age}`. With k=5 the payload
  is ~7.5KB, under the 8KB `maxToolResultBytes` cap (`session.go:281`) by design —
  no exemption needed, and bounded context is what local models want anyway. The
  `id` is included so a promotion flow has something to reference (see B2).
- **`kb_write(content string, tags? []string)`** — writes a **job-scoped** entry,
  stamped with `ct.jobID`/`ct.sessionID`/role by Go. **Rejected when `ct.jobID == ""`**
  (same guard pattern as job-bound progress at `tools.go:247`). Exact-content-hash
  duplicates are a silent no-op. Can be denied per-role via the existing denylist.

### Operator tools (`internal/operator`) — global governance

- **`kb_search(query, scope?)`** — operator-facing read that returns entry **ids**
  across global + any job (the operator isn't confined to one job's scope). This is
  the mechanism by which the operator *discovers* an id to promote — without it,
  `kb_promote` has no reachable argument (see B2).
- **`kb_write_global(content string)`** — operator-authored institutional memory.
- **`kb_promote(entry_id string)`** — copies a vetted job entry (id from the search
  above, or from the `/kb` TUI) into global scope.

This is the [default governance](#open-decisions): workers can never write global
directly; a rogue or confused worker can pollute only its own job's scratch memory.
Global memory is curated by the operator or a human via the `/kb` browser. Note the
two containment caveats surfaced in review: **global reads are shared across all
jobs** (a poisoned global entry reaches every worker — so the *write* gate is the
only barrier, and promotion must be deliberate), and **`metadata_json` is
model-supplied** (treated as untrusted display text, never as a scope/authority
signal).

## Lifecycle & growth control

**Revised default (review): job scope is pure scratch.** `DeleteByJob` fires on a
job's terminal transition — **completed *or* cancelled** — using hook points that
already exist. No `ttl_expires_at` column, no lazy-purge DELETE scan on the write
path (which would add latency on the single-writer connection). This is simpler,
bounds growth trivially, and cleanly handles cancellation (which the TTL variant
left lingering for the full TTL window).

TTL / persist-for-forensics becomes a **later** option, added only if real demand
for post-job inspection appears — at which point it's an additive column, not a
v1 commitment. Global entries never expire regardless.

### Failure behavior

The embedding provider being unavailable (backend busy, model evicted from VRAM) is
the *common* case on a local box, not an edge — so it's handled explicitly, not
swallowed:

- **`kb_write` failure** must not silently lose a finding. The write is buffered and
  retried (bounded), and a failure after retries surfaces to the operator via a KB
  event (below) rather than vanishing.
- **`kb_search` failure** returns an explicit "memory unavailable" result (not an
  empty result that reads as "nothing known"), so a node's behavior when the KB is
  down is legible rather than a silent non-deterministic divergence.
- Neither failure aborts the worker; the graph's typed edges remain the correctness
  contract (see [Relationship to the typed graph](#relationship-to-the-typed-graph)).

## Poisoning & confinement (the real risk)

Retrieval quality is a tuning problem; **write integrity is a security problem**,
so the design spends its complexity there:

1. **Go owns provenance.** Scope, job, session, role, model, timestamps are all
   Go-stamped. The model cannot claim a different scope or forge authorship.
2. **Workers can't reach global** (default). Blast radius of a bad worker write =
   its own job's TTL'd scratch memory.
3. **Job isolation on read.** Job J never sees job K's entries, so cross-job
   contamination is impossible even before governance.
4. **Model filtering on read.** Entries from a different embedding model are
   excluded from ranking, so a model swap can't surface garbage-similarity hits.

## Phasing

Each phase is independently shippable and testable; 0–1 have no user-visible
surface (lowest risk), following the project's per-batch branch → test → PR flow.

- **Phase 0 — Embeddings.** `EmbeddingProvider` + `*OpenAIProvider.Embed()` +
  `*Scheduler.Embed()` proxy, `internal/kb` embedder resolving a dedicated embed
  provider from the registry, `KBConfig`. Tests hit a fake `/v1/embeddings` server.
- **Phase 1 — Store + ranking.** `018_kb.sql` (no TTL column), `KBStore`
  (`Insert`/`Candidates`/`DeleteByJob`), cosine `rank` with the dim-guard and
  global-embedding cache. Unit tests.
- **Phase 2 — Worker tools + eval.** `kb_search` + job-scoped `kb_write` in
  `CoreTools`, `KBNotifier` events, result formatting under the cap, denylist
  gating, `DeleteByJob` on job-terminal. **Plus a minimal eval harness** (fixed
  queries, known-relevant entries, precision@k) so retrieval value is *falsifiable*
  before more surface is built — the core question is whether injecting top-k hits
  into a small-context local loop helps or crowds out task context (review S6).
- **Gate:** only proceed past here if the Phase-2 eval shows the core loop earns
  its keep. Retrieval quality is not deferred as "tuning" — it's the go/no-go.
- **Phase 3 — Global governance.** `kb_write_global` / operator `kb_search` /
  `kb_promote` operator tools. Built only after Phase 2 proves job-scoped memory
  helps at all.
- **Phase 4 — TUI `/kb` browser** (optional): inspect, prune, promote from the UI.

## Open decisions

Two forks branch the schema/governance; recorded here with the recommended default
and awaiting confirmation:

1. **Global writes** — *default: operator-only + promotion.* Alternatives:
   role-gated worker writes (trust moves into role definitions), or open writes
   (rejected — poisoning risk on a 24/7 fleet).
2. **Job-KB lifecycle** — *default (revised by review): pure scratch — `DeleteByJob`
   on terminal state (completed or cancelled).* Alternatives: persist + TTL (adds a
   column + purge scan; keeps a forensic window), or persist forever (durable
   artifact, unbounded growth). TTL was the original default but is deferred as
   additive rather than baked into the v1 schema.

## Architecture review (2026-07-03)

An independent architecture-critic pass verified the doc's anchors and stress-tested
the design. Its findings are folded into the sections above; recorded here as a
trail:

- **B1 (blocker, fixed):** the embedder could never resolve a provider — the registry
  returns the scheduler-wrapped chat-only interface, so the `EmbeddingProvider`
  assertion always failed. Fixed by proxying `Embed()` through `*Scheduler` and
  running the embed model as its own provider entry. See [Embeddings](#embeddings-internalprovider).
- **B2 (blocker, fixed):** the operator had no way to obtain an `entry_id` to
  `kb_promote`. Fixed by adding `id` to search results and an operator-facing
  `kb_search`. See [Operator tools](#operator-tools-internaloperator--global-governance).
- **S1 (should-fix, addressed):** untyped shared memory reintroduces coupling the
  typed graph removed — now justified explicitly in
  [Relationship to the typed graph](#relationship-to-the-typed-graph).
- **S2 (should-fix, addressed):** single-writer serialization + unbounded global
  re-scan → global-embedding in-memory cache; the risk is head-of-line blocking, not
  races.
- **S3 (should-fix, addressed):** embedding-provider-down is the common local case →
  [Failure behavior](#failure-behavior).
- **S4/S5 (should-fix, fixed in schema):** dim-guard in `rank`; `COALESCE(job_id,'')`
  in the dedup index.
- **S6 (should-fix, addressed):** retrieval value is unfalsifiable without an eval →
  eval added to Phase 2 as the go/no-go gate.
- **S7 (should-fix, addressed):** KB activity was invisible → `KBNotifier` events in
  [Observability](#observability-events).
- **Cut:** TTL machinery deferred (lifecycle above); global-governance surface gated
  behind the Phase-2 eval.

## Library-extraction note

If `internal/graphexec` is ever pulled out as a standalone OSS library, the KB
should sit behind a small repository interface (like the checkpoint store) so the
executor stays LLM- and storage-agnostic. The embedder already resolves through the
provider registry, so it's decoupled from any specific model.
```
