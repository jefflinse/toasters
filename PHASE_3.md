# Phase 3: Teams & Agents — Roadmap

**Created:** 2026-02-25
**Status:** Planning

---

## Objective

Build a complete teams and agents management system — dynamic agent generation, composable agent definitions, curated teams for specific purposes, shared agents reusable across teams, and per-agent provider/model selection. Consolidate job persistence to SQLite-only.

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

### 3.1 — SQLite-Only Job Persistence

Stop dual-writing jobs to markdown files. SQLite is the source of truth.

**What changes:**
- Remove markdown file writes from job creation/update paths
- Remove the `internal/job/` package (or reduce it to read-only for migration)
- Keep any markdown files that serve a purpose outside of job tracking (e.g., if they're used for human-readable project documentation)

**What stays:**
- Agent definition `.md` files — these are configuration, not job state
- Any markdown that's part of the project workspace (README, docs, etc.)

### 3.2 — Teams & Agents Management System (HIGH PRIORITY)

The core Phase 3 deliverable. A complete system for defining, composing, and managing agents and teams. **See `PHASE_3_DESIGN.md` for the full design document** with architecture, file formats, directory layout, composition rules, and all design decisions.

**Summary of the design:**
- **Three-layer composition**: Skills → Agents → Teams. Skills are reusable capability building blocks. Agents compose skills into personas. Teams assemble agents with a lead, culture document, and coordination rules.
- **System team**: Toasters itself runs as a team — the operator is the lead, with internal agents (planner, scheduler, blocker handler) as workers. Uses the same infrastructure as user teams. Fully hackable.
- **Operator model**: A code-driven event loop handles routine operations mechanically (task assignment, DB updates, feed posts). The operator LLM is only consulted for user messages and situations requiring decisions. The operator is reactive, not omniscient.
- **Activity feed**: The chat window evolves into a chronological activity feed showing system events (task started/completed, job done) interleaved with LLM interactions (operator responses, system agent consultations). Other TUI views read from DB directly.
- **Job execution**: The operator delegates planning to a team, then the scheduler creates tasks from the plan and assigns them to teams. Tasks execute serially (parallelism deferred). Task outcomes can spawn new tasks.
- **Agent definition superset**: Agent definitions are a superset of Claude Code and OpenCode fields, enabling lossless import and lossy export with warnings. Requires switching to proper YAML frontmatter parsing.
- **File-based source of truth**: All definitions are `.md` files with YAML frontmatter. DB is a runtime cache rebuilt from files. TUI CRUD writes files.
- **Config layout**: `~/.config/toasters/system/` (toasters internals) + `~/.config/toasters/user/` (user content). No CWD-scoped config.
- **Auto-team detection**: `~/.claude/agents` and `~/.config/opencode/agents` (user-level) symlinked as auto-teams, promotable to managed teams.
- **Tool boundaries**: System agents get orchestration tools (create jobs/tasks). User agents get work tools (filesystem, shell). Enforced separation via distinct tool names (`consult_agent` vs `spawn_agent`).

---

## Deferred Items

These were considered for Phase 3 but are explicitly deferred to Phase 4:

| Item | Reason | Destination |
|------|--------|-------------|
| Cost estimation | Nice-to-have, not core to teams/agents work | Phase 4 |
| OpenAPI-to-MCP bridges | Cool idea, lower priority than teams management | Phase 4 |
| Event-driven TUI updates (replace polling) | 500ms polling is sufficient for now | Phase 4 |
| MCP server HTTP transport | stdio is sufficient; HTTP needed only for non-Claude external agents | Phase 4 |
| MCP resource/prompt support | Only MCP tools are consumed; resources and prompts are future scope | Phase 4 |
| Remove Claude CLI subprocess fallback | Keep as-is in case it's still useful | Phase 4 |

---

## Delivery Approach

Phase 3 has a significant design exploration component (deliverable 3.2). The plan is:

1. **3.1 (SQLite-only)** — Execute first. Well-defined, low risk, quick win.
2. **Design exploration for 3.2** — Before writing code, explore the agent composition and teams management design space. This may involve prototyping, discussing tradeoffs, and iterating on the data model.
3. **Implementation of 3.2** — Once the design is settled, break into PRs and execute.

---

## Open Questions

All original open questions have been resolved in the design doc. See `PHASE_3_DESIGN.md` Decisions Log (#1–#36) for the full record.

- ~~What does the agent composition file format look like?~~ → `.md` with YAML frontmatter, superset of CC + OC fields (Decisions #1, #30, #31)
- ~~How do shared agents reference each other?~~ → By name, resolved team-local → shared → built-in (Decision #10)
- ~~Should team definitions be declarative or imperative?~~ → Declarative (file-based, Decision #2)
- ~~How much of team assembly should be automated vs. curated?~~ → Curated by default, AI generation available via TUI flow
- ~~What's the caching/invalidation strategy?~~ → DB is runtime cache, rebuilt from files on startup, fsnotify for live updates (Decision #2)
