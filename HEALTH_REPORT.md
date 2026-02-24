# Codebase Health Report — Toasters

**Date:** 2026-02-24
**Scope:** Full codebase health audit
**Go version:** 1.25.0

---

## Summary

| Metric | Value |
|---|---|
| **Overall health** | **Improved** (was "Needs Attention") |
| **Build / Vet** | ✅ Clean |
| **Tests** | ✅ All pass (5 packages) |
| **Test coverage** | ⚠️ 12.1% overall |
| **Known vulnerabilities** | ❓ Unable to verify (govulncheck version mismatch) |
| **Lint findings** | **0** (was 26) |
| **Outdated dependencies** | Partially addressed (`x/crypto`, `x/net`, `x/sync`, `x/term`, `x/text` updated; Charm v2 pre-release still pending) |
| **go mod tidy** | ✅ Clean |

**Update (2026-02-24):** A comprehensive code health refactoring pass resolved all critical, high-priority, and low-priority lint/code findings (items #1–6, #13–15). The codebase now has 0 lint findings, proper HTTP timeouts, safe permission defaults, no global mutable state in the tool system, and a shared `internal/claude` package eliminating type duplication. Remaining items are structural tech debt (#8–12, #16–18) deferred to future work. See resolution status on each finding below.

The project builds, vets, and passes all existing tests cleanly. The remaining risks are: (1) very low test coverage masking potential bugs, (2) significant structural debt concentrated in a single 5,324-line file, and (3) Charm v2 libraries pinned to pre-release versions. These are "the system is fragile and hard to evolve safely" issues, not "the system is broken" issues.

---

## Critical Issues (fix immediately)

### 1. HTTP client has no timeout — potential indefinite hang
- **Location:** `internal/llm/client.go:132`
- **Issue:** `&http.Client{}` with zero-value timeout means requests to LM Studio will block forever if the server stops responding. In a TUI application, this silently freezes the operator LLM goroutine with no user-visible feedback and no way to recover short of killing the process.
- **Remediation:** Add a timeout or use a `Transport` with `DialContext` timeouts for streaming endpoints.
- **Status:** ✅ Resolved (2026-02-24) — Added `Transport` with 30s connect timeout and 5min response header timeout.

### 2. `--dangerously-skip-permissions` as default fallback
- **Location:** `internal/tui/claude.go:109`, `internal/gateway/gateway.go:609`
- **Issue:** When no `claude.permission_mode` is configured, both the direct Claude subprocess and the gateway fall back to `--dangerously-skip-permissions`. This grants Claude CLI unrestricted filesystem and shell access. A user who simply omits the config key gets the most permissive mode with no warning.
- **Remediation:** Either require `permission_mode` to be set explicitly and fail with a clear error if missing, or default to a restrictive mode and log a warning.
- **Status:** ✅ Resolved (2026-02-24) — Both `internal/tui/claude.go` and `internal/gateway/gateway.go` now default to `--permission-mode plan` with a warning log when `claude.permission_mode` is not configured.

### 3. Outdated `golang.org/x/crypto` and `golang.org/x/net`
- **Location:** `go.mod` (`x/net v0.33.0`, transitive `x/crypto v0.31.0`)
- **Issue:** These packages are 15+ minor versions behind. They frequently receive security patches. The `x/net/html` package is used directly in `internal/llm/tools.go` for web content parsing — a common attack surface.
- **Remediation:** `go get golang.org/x/net@latest golang.org/x/crypto@latest && go mod tidy`
- **Status:** ✅ Resolved (2026-02-24) — Updated `x/crypto` v0.31→v0.48, `x/net` v0.33→v0.50, `x/sync` v0.18→v0.19, `x/term` v0.31→v0.40, `x/text` v0.28→v0.34.

---

## High Priority (fix before next release)

### 4. 19 unchecked error returns (errcheck findings)
- **Locations:**
  - `internal/agents/agents.go` — 6 unchecked errors on `w.Close()`, `tmp.Close()`, `os.Remove()`
  - `internal/gateway/gateway.go` — 1 unchecked `io.Copy()` error
  - `internal/job/blocker.go` — 1 unchecked `f.Close()` error
  - `internal/job/job.go` — 3 unchecked errors on `f.Close()`, `tmp.Close()`
  - `internal/llm/client.go` — 3 unchecked `resp.Body.Close()` errors
  - Additional unchecked errors in `internal/anthropic/`, `cmd/` (5 more)
- **Issue:** Unchecked `Close()` on writable files is the most dangerous — data may not be flushed to disk. Unchecked `resp.Body.Close()` can leak connections.
- **Status:** ✅ Resolved (2026-02-24) — All 19 unchecked error returns fixed across 8 files.

### 5. Package-level mutable global state in `internal/llm/tools.go`
- **Location:** `internal/llm/tools.go:39-51` (`activeGateway`, `activeTeams`, `activeWorkspaceDir`)
- **Issue:** Three package-level variables mutated via setter functions create hidden coupling. Makes tool execution logic untestable in isolation and creates a concurrency risk.
- **Remediation:** Introduce a `ToolExecutor` struct that holds these dependencies as fields.
- **Status:** ✅ Resolved (2026-02-24) — Replaced globals with a `ToolExecutor` struct using dependency injection. Setter functions removed. All call sites updated.

### 6. Duplicated Claude CLI stream types and parsing
- **Location:** `internal/tui/claude.go:16-83` and `internal/gateway/gateway.go:499-567`
- **Issue:** Types like `claudeInitEvent`, `claudeInnerEvent`, `claudeContentBlock`, etc. are defined identically in both files. Stream parsing logic is also duplicated.
- **Remediation:** Extract shared types and parsing into a new `internal/claude` package.
- **Status:** ✅ Resolved (2026-02-24) — Created `internal/claude/stream.go` with exported types (`InitEvent`, `InnerEvent`, `ContentBlock`, `AssistantMessage`, `ToolResultBlock`, `UserMessage`, `UserOuterEvent`, `OuterEvent`). Duplicated definitions removed from both files.

### 7. Vulnerability scan gap
- **Issue:** `govulncheck` could not run due to a Go version mismatch.
- **Remediation:** Install a compatible govulncheck binary and run it.
- **Status:** ⚠️ Open — requires compatible govulncheck binary for Go 1.25.0.

---

## Medium Priority (address in next sprint)

### 8. God file: `internal/tui/model.go` (5,324 lines)
- **Issue:** Contains the entire Bubble Tea model struct (50+ fields), all Update logic, all View rendering, and ~100 helper functions. #1 barrier to maintainability and primary reason test coverage for `internal/tui` is only 5.5%.
- **Remediation:** Incrementally extract: message management, modal rendering, grid view, key handling.
- **Status:** 📋 Open — deferred to future work (XL effort).

### 9. Parallel slices should be a struct
- **Location:** `internal/tui/model.go` — `m.messages`, `m.timestamps`, `m.reasoning`, `m.claudeMeta`
- **Issue:** Four parallel slices that must always be appended in lockstep across 32 append sites. Index-out-of-bounds panic risk.
- **Remediation:** Replace with a `ChatEntry` struct and single `entries []ChatEntry` slice.
- **Status:** 📋 Open — deferred to future work (L effort).

### 10. Duplicated frontmatter parsing (4 implementations)
- **Locations:** `internal/job/job.go`, `internal/job/task.go`, `internal/job/blocker.go`, `internal/agents/agents.go`
- **Issue:** Four different approaches to the same `---`-delimited frontmatter format.
- **Remediation:** Create a shared `internal/frontmatter` package with a single generic parser.
- **Status:** 📋 Open — deferred to future work (M effort).

### 11. `internal/llm` package has too many responsibilities
- **Location:** `internal/llm/` — `client.go` (624 lines), `tools.go` (709 lines)
- **Issue:** Package is the API client, type system, tool registry, tool executor, gateway interface, and HTML converter.
- **Remediation:** Split into `internal/llm` (types), `internal/llm/openai` (client), `internal/operator/tools` (tool execution).
- **Status:** 📋 Open — deferred to future work (M effort). Note: global state in `tools.go` was eliminated via `ToolExecutor` struct (see #5).

### 12. macOS-only Keychain integration with no platform guard
- **Location:** `internal/anthropic/client.go:24-26`
- **Issue:** Shells out to macOS `security` CLI. Fails with unhelpful error on other platforms.
- **Remediation:** Add build tag or runtime `GOOS` check with clear error message.
- **Status:** 📋 Open — deferred to future work (S effort).

---

## Low Priority (tech debt backlog)

### 13. Unused code (2 findings)
- `internal/tui/model.go:506` — unused field `timeoutPromptTimer`
- `internal/tui/model.go:5280` — unused function `hasCollapsibleMessages()`
- **Status:** ✅ Resolved (2026-02-24) — Removed unused field and function.

### 14. Ineffectual assignment
- `internal/tui/model.go:761` — `total` is computed but never used
- **Status:** ✅ Resolved (2026-02-24) — Removed ineffectual `total` variable.

### 15. Staticcheck style findings (9 issues)
- SA9003: Empty branches in `internal/llm/client.go` (2 issues)
- QF1012: Use `fmt.Fprintf` instead of `WriteString(fmt.Sprintf(...))` (3 issues)
- QF1008: Redundant embedded field selector (3 issues)
- S1017: Use `strings.TrimPrefix` (1 issue)
- **Status:** ✅ Resolved (2026-02-24) — All staticcheck findings fixed: empty branches eliminated, `fmt.Fprintf` used, embedded field selectors simplified, `strings.TrimPrefix` used.

### 16. Duplicated modal rendering logic
- Prompt modal and output modal rendering share nearly identical dimension/scroll/styling code.
- **Status:** 📋 Open — deferred to future work.

### 17. Charm v2 libraries pinned to pre-release versions
- `bubbles/v2 v2.0.0-rc.1`, `bubbletea/v2 v2.0.0-rc.2`, `lipgloss/v2 v2.0.0-beta.3`
- Stable v2.0.0 releases are now available.
- **Status:** 📋 Open — deferred to future work (M effort).

### 18. Test coverage gaps in critical packages
- `cmd/` — 0%, `internal/anthropic/` — 0%, `internal/config/` — 0%
- `internal/gateway/` — 8.5%, `internal/llm/` — 2.9%
- **Status:** 📋 Open — deferred to future work (L effort). Target 40% before Phase 1 completion.

---

## Positive Observations

- **Clean build and vet** — zero warnings across the entire codebase
- **All tests pass** — existing test suite is green with no flaky tests
- **Clean `go mod tidy`** — no unused or phantom dependencies
- **Consistent error wrapping** — `fmt.Errorf("context: %w", err)` used correctly throughout
- **Good `context.Context` threading** — subprocess management properly threads context for cancellation
- **Well-structured agent discovery** — `internal/agents/` has best coverage (53.7%) and clean design with hot-reloading
- **Clean package dependency graph** — no circular dependencies
- **Thoughtful comments** — meaningful comments explaining *why* decisions were made

---

## Recommended Action Plan

| Priority | Effort | Item | Description | Status |
|---|---|---|---|---|
| **This week** | S | #1 | Add HTTP client timeout | ✅ Done |
| **This week** | S | #2 | Fix `--dangerously-skip-permissions` default | ✅ Done |
| **This week** | S | #3 | Update `x/crypto` and `x/net` | ✅ Done |
| **This week** | S | #7 | Run govulncheck | ⚠️ Open |
| **This week** | M | #4 | Fix errcheck findings | ✅ Done |
| **Next sprint** | M | #5 | Eliminate global state in tools.go | ✅ Done |
| **Next sprint** | M | #6 | Extract shared Claude stream types | ✅ Done |
| **Next sprint** | L | #9 | Replace parallel slices with struct | 📋 Open |
| **Next sprint** | M | #10 | Unify frontmatter parsing | 📋 Open |
| **Ongoing** | XL | #8 | Break up model.go | 📋 Open |
| **Backlog** | S | #13, #14, #15 | Dead code and lint fixes | ✅ Done |
| **Backlog** | M | #17 | Update Charm v2 to stable | 📋 Open |
| **Backlog** | L | #18 | Increase test coverage | 📋 Open |
