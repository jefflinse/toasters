# Context Compaction & Operator Handoff — Design

Status: agreed design, not yet implemented.

## Problem

Neither the operator nor worker sessions manage context in terms of tokens.

- **Operator** (`internal/operator/operator.go`): history is capped at 200
  messages (`maxMessages`, operator.go:30) — oldest messages are silently
  discarded with no summary, losing orchestration memory. Nothing is
  token-aware: the operator tracks the provider-reported prompt size of the
  most recent round-trip (`lastIn`) but never compares it to the model's
  context window. A history heavy with tool results can overflow a local
  model's window long before 200 messages.
- **Workers** (`internal/runtime/session.go`): tool results are truncated
  (8KB built-in, 16KB MCP) and turns are capped at 50, but there is no
  history compaction and no overflow handling. Any provider error —
  including context-length-exceeded — terminally fails the session
  (session.go:161–183). On small loaded contexts (LM Studio) this is a
  routine failure mode, unacceptable for 24/7 autonomy.
- **Context-window knowledge is uneven**: LM Studio reports
  `MaxContextLength`/`LoadedContextLength` (provider/openai.go:479–494);
  the models.dev catalog has `Limit.Context` (modelsdev/catalog.go); the
  Anthropic provider reports nothing (provider/anthropic.go:425–431). A
  percentage threshold has no denominator without a resolution chain.

## Decisions

1. **Operator: digest-based handoff**, not in-place summarization. Go owns
   the state — jobs, tasks, blockers, and graph state live in SQLite and
   the service layer. On compaction the session is retired and a successor
   is seeded with a handoff document that is mostly **Go-generated**
   (mechanical state digest) plus a short **LLM-written narrative** of
   intent and in-flight reasoning.
2. **Narrative via a fresh one-shot call on the same model.** Only one
   model can be assumed available (single loaded model in LM Studio). The
   call is stateless — not part of the operator session, no tools — fed a
   stripped transcript (tool results elided), with a tight output budget.
   The digest carries the facts; the narrative carries only intent.
3. **Threshold trigger only.** No interval trigger — time is a weak proxy
   for token growth. Defaults: **operator 50%**, **workers 70%** of the
   resolved context window. `0` disables.
4. **Archive before handoff.** The operator's pre-handoff history is
   archived to disk for debugging; worker pre-compaction messages are kept
   in SQLite (flagged, not deleted).
5. **Workers: tiered compaction**, cheapest first — mechanical tool-result
   elision, then summarize-and-continue — plus a retry-once backstop on
   provider context-overflow errors.
6. **Configurable in `/settings`** following the flat-scalar pattern
   (PR #43 sidebar_side wiring).

## Design

### 1. Context-window resolution

A resolver answering `ContextWindow(providerName, modelID) int` (0 =
unknown), living server-side where both the runtime and operator can reach
it. Precedence:

1. **Provider-reported loaded context** — LM Studio's
   `LoadedContextLength` is ground truth (a 128k model loaded at 8k
   overflows at 8k). `service/types.go:566–569` already prefers loaded
   over max when mapping `ModelInfo`; the resolver reuses that.
2. **Provider YAML override** — new optional `context_window` key in
   provider definitions, for servers that don't report (llama.cpp
   `/props` support can come later) and for pinning Anthropic models.
3. **models.dev catalog** — `ContextLimit` by model ID
   (modelsdev/adapter.go:45). Covers Anthropic and most cloud models.
4. **Unknown (0)** — compaction is disabled for that session and the TUI
   bar falls back to a raw token count, as today.

Resolved values are cached and refreshed whenever provider model lists are
fetched. The resolved window is also added to session/operator DTOs so the
TUI stops guessing client-side — this fixes percentage bars for Anthropic
workers as a side effect.

### 2. Operator handoff

**Trigger.** The operator is a single-goroutine event loop
(`run`/`handleEvent`), so there are no concurrent turns. After each
completed turn, compute `lastIn / window`; if ≥ threshold, perform the
handoff inside the loop before processing the next event. Also checked
once after session restore, since a restored history may already be over
threshold.

**Steps** (all under the existing message mutex, inside the run loop):

1. **Archive.** Copy the current session to
   `~/.config/toasters/sessions/archive/operator-<RFC3339>.json` before
   any mutation. Retain the most recent N archives (start with 20,
   hardcoded; make configurable only if asked for).
2. **Go digest.** A deterministic renderer over state the operator already
   queries (same handles used by `assignNextTask`/`checkJobComplete`):
   active jobs with graph/task states, pending blockers with ages,
   in-flight worker sessions, recently completed/failed jobs. Rendered as
   markdown. This is the load-bearing memory; it must never depend on an
   LLM call succeeding.
3. **Narrative.** Strip the history — drop tool result contents, keep
   user/assistant text and tool-call names — and make one stateless
   provider call on the operator's own provider/model asking for a short
   handoff note (open intent, decisions made, anything not reconstructible
   from state). Prompted via a new system role definition
   (`operator-handoff` under `defaults/system/roles/`) so it composes and
   overrides like every other prompt. Small max_tokens (~500). **On
   failure: log, proceed digest-only.**
4. **Seed successor.** Replace `o.messages` with a single user-role
   handoff message: a header stating this session resumes from a handoff,
   then digest, then narrative. System prompt is unchanged (composed once
   at startup, as today). Persist via the existing atomic write.
5. **Emit** `EventTypeOperatorCompaction` on the service event stream
   (before-tokens, estimated after-tokens, archive filename). SSE clients
   get it for free.

The `maxMessages = 200` discard remains as a last-resort backstop but
should effectively never fire once handoff works.

### 3. Worker compaction

**Pre-flight guard** in the session loop, before each `ChatStream`: if
`lastInputTokens / window ≥ threshold` (skip on turn 0 — no reading yet):

- **Tier 1 — tool-result elision.** For messages older than the last K
  turns (start K=4), replace tool-result *content* with a stub:
  `[elided tool result: <tool name>]`. Messages are never removed, so
  tool-call/result pairing cannot break. `spawn_worker` results are
  exempt, consistent with the existing truncation exemption
  (session.go:227–230) — a child's synthesized output is load-bearing.
- **Tier 2 — summarize-and-continue.** Estimate post-elision size
  (bytes/4); if still over threshold, make a stateless one-shot call on
  the session's own model: "summarize progress, decisions, and what
  remains." New history = original task message + summary (user role) +
  last K turns verbatim, split at a tool-pair-safe boundary (reuse the
  boundary logic pattern from the operator's `truncateMessages`).

**Overflow backstop.** Best-effort classification of context-length errors
in the provider layer (`provider.IsContextOverflow(err)`): Anthropic
returns a 400 `invalid_request_error` with a recognizable message;
OpenAI-compatible servers use code `context_length_exceeded`; llama.cpp
variants matched loosely. On overflow: force Tier 1 + Tier 2 once, retry
the request once, then fail as today. Classification being imperfect is
acceptable — the pre-flight guard is primary and works without it.

Compaction does not reset the 50-turn budget; turns and tokens are
separate limits. Each compaction emits a session-scoped service event and
flags (not deletes) the superseded messages in `session_messages` so
transcript debugging keeps working.

### 4. Settings

Two new flat scalars, following the sidebar_side pattern end-to-end
(config.go schema + viper default + `Valid*()`; `Settings` DTO in
service/types.go; modal rows in settings_modal.go; persist + live-apply in
local_system.go `UpdateSettings`):

| key | default | range |
|---|---|---|
| `operator_compaction_threshold` | 50 | 0 (off), 30–90 |
| `worker_compaction_threshold` | 70 | 0 (off), 30–90 |

Live-apply: the operator reads the value at turn boundaries via a getter
refreshed by `UpdateSettings` (same shape as the prompt-engine granularity
refresh); the runtime/graphexec worker default refreshes like
`worker_temperature`. Modal rows cycle off/30/40/50/60/70/80/90.

### 5. TUI context bars

All fleet surfaces share `renderMiniContextBar` (tui/panels.go:518), so
one change lands everywhere.

- **Signature** gains `threshold float64` (0 = no marker, legacy colors).
- **Tick:** the cell at `threshold × barWidth` renders as `│` — dim over
  the empty region, contrasting once the fill passes it. Operator rows
  tick at 50%, worker rows at 70%, visibly different positions.
- **Threshold-relative coloring** replaces the hardcoded 60%/85%
  breakpoints when a threshold is set: green below threshold, yellow at or
  above (compaction pending), red at threshold + 15 points — which now
  genuinely means something is wrong (compaction disabled, failing, or
  floor too high).
- **Compaction trace:** on compaction events, the row's activity line
  shows `↳ compacted 52% → ~18%`, and sessions that have compacted show a
  small `↺n` after the percent label. Counts derive from events
  client-side initially; move into the session DTO if restore-correctness
  matters later.

`fleetMember` gains a `threshold` field populated from settings (already
fetched at startup and refreshed on `SettingsSavedMsg`).

## Testing

- Resolver precedence table (loaded > override > catalog > unknown).
- Elision preserves tool-call/result pairing; spawn_worker exemption;
  Tier 2 boundary safety (property-style, mirroring truncateMessages
  tests).
- Digest renderer golden test against a seeded service state.
- Handoff under `-race`: events arriving during handoff are processed
  after seeding, never interleaved.
- Settings persistence mirroring `TestUpdateSettings_PersistsSidebarSide`.
- `context_bar_test.go`: tick position, threshold coloring, zero-threshold
  legacy behavior.
- Integration: fake provider with a tiny reported window drives both the
  operator handoff and worker tiers, plus the overflow-retry backstop.

## PR breakdown

1. **Context-window resolution chain** + resolved window in session/
   operator DTOs. No behavior change; Anthropic bars gain percentages.
2. **Settings + bar treatment**: both thresholds end-to-end, tick and
   threshold-relative coloring. (Tick briefly renders before compaction
   exists — acceptable between PRs.)
3. **Operator handoff**: archive, digest, narrative role, seeding, event,
   activity trace.
4. **Worker compaction**: elision, summarize-and-continue, overflow
   backstop, session events, message flagging.

PRs 3 and 4 are independent once 1 and 2 land.

## Deferred / future

- llama.cpp `/props total_slots`-style context discovery.
- Go-generated digest sections for worker Tier 2 (workers are ephemeral;
  their task input is already the "digest").
- Per-role worker thresholds.
- Archive retention setting.
