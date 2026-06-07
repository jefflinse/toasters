# Detecting & Preventing LLM Degeneration

Local quantized models (the kind toasters targets for 24/7 autonomy) periodically
go "off the rails": the sampler falls into a repetition basin and the output
collapses into recombined fragments of its own recent text — a death loop. This
is costly for unattended runs: a worker can spin forever, burning tokens and
wall-clock, producing nothing usable.

Because **Go owns the token stream**, the orchestrator is the natural place to
supervise the model. "The orchestrator is the memory, not the model" extends to
"the orchestrator is the watchdog." This doc captures the full menu of
approaches across three layers — **prevent / detect / recover** — and tracks
what's implemented vs. parked.

## Status

| Item | Layer | Status |
|------|-------|--------|
| Anti-repetition sampler defaults (repeat_penalty + DRY) | Prevent | **Done** — `internal/provider/openai.go`, local endpoints only. (Briefly suspected of breaking tool calling, but that was actually a missing operator `OnPrompt` wire on the boot path — see note below.) |
| Per-provider / per-role sampler config (YAML) | Prevent | Parked |
| Schema/grammar-constrained decoding for typed nodes | Prevent | Parked |
| Streaming loop detector (compression + n-gram) | Detect | Parked |
| No-progress / budget guard | Detect | Parked |
| Logprob entropy-collapse signal | Detect | Parked |
| Off-rails → retry-with-escalation in graphexec | Recover | Parked |
| Circuit breaker → HITL surface | Recover | Parked |
| Degeneration event logging to SQLite | Measure | Parked |

## 1. Prevent (sampling + decoding)

Most degeneration is a **sampling-config** problem, not a model-capability one.

- **Anti-repetition samplers (`repeat_penalty` + DRY)** *(implemented)* — local
  OpenAI-compatible endpoints (llama.cpp / LM Studio / Ollama) get
  `repeat_penalty 1.1`, `repeat_last_n 64`, and DRY (`dry_multiplier 0.8`,
  `dry_base 1.75`, `dry_allowed_length 2`, `dry_penalty_last_n -1`). Gated to
  local endpoints because cloud APIs (OpenAI, z.ai) reject unknown request
  fields with a 400. See `samplingParams` / `defaultLocalSampling` /
  `isLocalEndpoint`.

  > **Watch the tool-calling interaction.** These penalize repeated tokens, and
  > tool-call JSON has legitimately repeated tokens (field names, braces,
  > quotes). They *were* briefly blamed for the operator emitting prose and not
  > calling `ask_user` — but the real cause was a missing `OnPrompt` wire on the
  > boot operator path (the prompt was never broadcast). With that fixed, the
  > samplers are back on. If a future tool-calling regression appears on a small
  > quant, this is the first thing to A/B — and the scoped fix is to disable DRY
  > per role for tool-calling phases (keep it for text-only review/summarize).
- **Standard penalties** — `frequency_penalty` / `presence_penalty` are accepted
  by *all* OpenAI-compatible APIs (including cloud) and could be applied
  universally as a milder, portable backstop.
- **Per-provider / per-role config** — promote the sampler defaults into the
  provider YAML (and optionally per-role overrides) so they're tunable without a
  rebuild. Currently endpoint-gated defaults only.
- **Grammar / schema-constrained decoding** — the deeper win. graphexec nodes
  already declare **typed output schemas**; constraining generation to the schema
  (GBNF grammar in llama.cpp, or JSON-schema-constrained decoding) makes salad
  *physically impossible* and guarantees parseable output. The security-review
  node that produced the original death loop was emitting free-form prose; forced
  into `{findings: [...]}` the failure mode disappears. Bigger lift — phase 3.
- **Context hygiene & caps** — tight, typed worker prompts (which the
  "Go owns state" architecture already favors), bounded context windows, max
  tokens, and well-chosen stop sequences all reduce the odds.

## 2. Detect (streaming watchdog)

A `StreamGuard` wrapping the provider stream in `runtime/session.go`: tokens pass
through, cheap detectors run, and it can cancel the context with a typed reason
(`LoopDetected`, `LowEntropy`, `BudgetExceeded`, `SchemaTimeout`). Keep it
LLM-agnostic so it travels with a future graphexec/runtime library extraction.

Signals, by bang-for-buck:

- **Compression ratio** — zlib-compress the last ~1KB of output; if
  `compressed/original` drops below a threshold, it's repetitive. ~5 lines,
  catches loops reliably.
- **Rolling n-gram repeat** — hash sliding windows (~100 chars); if any window
  recurs more than K times in the recent buffer, it's looping. O(1) amortized.
- **No-progress budget** — for agentic workers: "emitted N tokens, made 0 tool
  calls / 0 state mutations." Go can see this directly. A reviewer narrating for
  4000 tokens without producing its structured finding is off-rails even without
  strict repetition.
- **Logprob entropy collapse** — if the endpoint returns logprobs, a sustained
  near-zero distribution entropy means the model is stuck. Richer but
  provider-dependent.

## 3. Recover (when tripped)

- **Abort + retry with escalated params** — graphexec already retries nodes; add
  `OffRails` as a retry trigger that bumps repetition penalty / changes seed /
  lowers temperature. Don't truncate-and-continue — the looped context poisons
  further generation; kill it.
- **Circuit breaker → HITL** — after K degenerations on a model, surface through
  the `ask_user` broker: "this model keeps looping; try a bigger model or adjust
  sampling."
- **Measurement** — log every trip to SQLite (sessions are already persisted),
  tagged by model / role / params, so degeneration becomes a tunable signal
  rather than a vibe.

## Recommended sequence

1. **DRY + repetition-penalty defaults** *(done)* — near-zero code, likely
   eliminates the bulk of local-model loops.
2. **`StreamGuard`** with compression + n-gram detectors feeding graphexec's
   retry path — model-agnostic watchdog.
3. **Schema-constrained decoding** for typed nodes — the structural fix.
