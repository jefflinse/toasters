# Phase 3: Teams & Agents — Roadmap

**Created:** 2026-02-25
**Status:** Planning

---

## Objective

Build a complete teams and agents management system — dynamic agent generation, composable agent definitions, curated teams for specific purposes, shared agents reusable across teams, per-agent provider/model selection, and cost visibility. Consolidate job persistence to SQLite-only.

---

## Pre-Phase 3: Wave 3 Tech Debt ✅

All Wave 3 items completed (2026-02-25).

| Item | Description | Status |
|------|-------------|--------|
| ARCH-H1 | Consolidated Anthropic SSE parsing → shared `internal/sse` package (~400 lines dedup) | ✅ |
| ARCH-H2 | Converged on single `provider.Provider` interface (net -1,041 lines) | ✅ |
| DESIGN-H1 | Decomposed TUI Model → 6 sub-models + `ModelConfig` struct | ✅ |
| DESIGN-M1 | Tool registry pattern for `ExecuteTool` (18 handlers in 5 files) | ✅ |
| MOD-M8 | Migrated 43 `log.Printf` calls to `slog` structured logging | ✅ |

---

## Deliverables

### 3.1 — Teams & Agents Management System (HIGH PRIORITY)

The core Phase 3 deliverable. A complete system for defining, composing, and managing agents and teams.

**Key goals:**
- **Composable agent definitions** — Break agent `.md` files into multiple composable files that get combined and cached as actual agent definitions. This enables dynamic configuration of agents with various properties (personality, tools, constraints, domain knowledge) managed independently.
- **Agent generation** — Ability to generate new agent definitions programmatically or via the operator, not just hand-authored `.md` files.
- **Curated teams** — Create purpose-built teams for specific workflows (e.g., "frontend team", "security audit team", "migration team") with well-defined roles and coordination patterns.
- **Shared agents** — Agents that can be reused across multiple teams without duplication. A "senior Go developer" agent shouldn't need to be redefined for every team that needs one.
- **Per-agent provider/model selection** — Assign specific providers and models to individual agents. A code reviewer might use Claude while a documentation writer uses a cheaper model. This was previously listed as a standalone item but belongs naturally in the teams management system.
- **TUI integration** — The `/teams` command and agents panel should reflect the full management system, not just static file listings.

**Design space to explore:**
- Agent definition layering (base traits + role overlays + team-specific overrides)
- Agent trait/capability libraries (reusable building blocks)
- Team templates vs. fully custom teams
- Hot-reloading of composed definitions (extend existing fsnotify watcher)
- Agent definition caching strategy
- How the operator discovers and selects teams for work assignment
- Whether agents can self-describe their capabilities for dynamic team assembly

### 3.2 — SQLite-Only Job Persistence

Stop dual-writing jobs to markdown files. SQLite is the source of truth.

**What changes:**
- Remove markdown file writes from job creation/update paths
- Remove the `internal/job/` package (or reduce it to read-only for migration)
- Keep any markdown files that serve a purpose outside of job tracking (e.g., if they're used for human-readable project documentation)

**What stays:**
- Agent definition `.md` files — these are configuration, not job state
- Any markdown that's part of the project workspace (README, docs, etc.)

### 3.3 — Cost Estimation (NICE TO HAVE)

Token usage is already tracked per-session with model identity. The `CostUSD *float64` field exists in the DB schema but is never populated.

**What's needed:**
- A pricing table — `map[string]ModelPricing` keyed by model name, containing per-input-token and per-output-token rates. Could be hardcoded for known models, configurable in YAML for custom/local models, or both.
- A ~10-line cost calculation at session completion in `runtime.go` — `cost := pricing.Estimate(provider, model, tokensIn, tokensOut)`
- TUI display — extend the agent session panel to show estimated cost per session and a running total

**Open questions:**
- Where does pricing data live? Config YAML? Hardcoded table? Fetched from provider APIs?
- How to handle models with tiered pricing (e.g., cached vs. uncached input tokens)?
- Should we show cost in real-time (per-poll-cycle) or only at session completion?
- How to handle local models (LM Studio, Ollama) where cost is effectively $0?

---

## Deferred Items

These were considered for Phase 3 but are explicitly deferred to a later phase:

| Item | Reason |
|------|--------|
| OpenAPI-to-MCP bridges | Cool idea, lower priority than teams management |
| Event-driven TUI updates (replace polling) | 500ms polling is sufficient for now |
| MCP server HTTP transport | stdio is sufficient; HTTP needed only for non-Claude external agents |
| MCP resource/prompt support | Only MCP tools are consumed; resources and prompts are future scope |
| Remove Claude CLI subprocess fallback | Keep as-is in case it's still useful |

---

## Delivery Approach

Unlike Phases 1 and 2, Phase 3 has a significant design exploration component (deliverable 3.1). The plan is:

1. **Wave 3 tech debt** — Execute all 5 items first. These are well-defined refactorings with clear acceptance criteria.
2. **Design exploration for 3.1** — Before writing code, explore the agent composition and teams management design space. This may involve prototyping, discussing tradeoffs, and iterating on the data model.
3. **Implementation** — Once the design is settled, break into PRs and execute.
4. **3.2 and 3.3** — Can be done in parallel with or after 3.1, as they're independent.

---

## Open Questions

- What does the agent composition file format look like? YAML layers? Markdown with includes? Something else?
- How do shared agents reference each other? By name? By path? Via a registry?
- Should team definitions be declarative (YAML/config) or imperative (operator creates them at runtime)?
- How much of team assembly should be automated (operator picks agents) vs. curated (human designs teams)?
- What's the caching/invalidation strategy for composed agent definitions?
