# Codebase Health Report — Toasters

**Date:** 2026-02-24
**Scope:** Full codebase health audit
**Go version:** 1.25.0

---

## Summary

| Metric | Value |
|---|---|
| **Overall health** | **Excellent** (was "Good") |
| **Build / Vet** | ✅ Clean |
| **Tests** | ✅ All pass (10 packages, 300+ tests) |
| **Test coverage** | ✅ 42.9% overall (target 40% met) |
| **Known vulnerabilities** | ✅ None found (govulncheck clean) |
| **Lint findings** | **0** (was 26) |
| **Outdated dependencies** | ✅ All addressed — Charm v2 updated to stable v2.0.0, all `x/` packages current |
| **go mod tidy** | ✅ Clean |

**Update (2026-02-24):** All findings from the original audit are now resolved. The complete history:

- All critical, high-priority, and medium-priority structural findings resolved.
- `model.go` was split from 5,310 lines into 11 focused files. `internal/llm` was split into three sub-packages.
- Items #9 (parallel slices → ChatEntry), #10 (unified frontmatter parsing), and #12 (Keychain platform guard) resolved.
- Items #16 (modal dedup), #17 (Charm v2 stable), #19 (pointer capture fix), #20 (stream dedup), #21 (orchestration package), #22 (client tests) all resolved.
- Item #18 (test coverage): raised from 12.1% to 42.9%, exceeding the 40% target. 300+ tests across 10 packages.

All findings are now resolved — the codebase has a clean bill of health.

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
- **Status:** ✅ Resolved (2026-02-24) — `govulncheck ./...` reports no vulnerabilities found.

---

## Medium Priority (address in next sprint)

### 8. God file: `internal/tui/model.go` (5,324 lines)
- **Issue:** Contains the entire Bubble Tea model struct (50+ fields), all Update logic, all View rendering, and ~100 helper functions. #1 barrier to maintainability and primary reason test coverage for `internal/tui` is only 5.5%.
- **Remediation:** Incrementally extract: message management, modal rendering, grid view, key handling.
- **Status:** ✅ Resolved (2026-02-24) — Split into 11 focused files: `model.go` (1,200 lines), `view.go`, `grid.go`, `panels.go`, `teams_modal.go`, `blocker_modal.go`, `streaming.go`, `messages.go`, `prompt.go`, `helpers.go`, `update.go`. Added 7 test files with 155+ test cases. `internal/tui` coverage increased from 5.5% to 15.1%.

### 9. Parallel slices should be a struct
- **Location:** `internal/tui/model.go` — `m.messages`, `m.timestamps`, `m.reasoning`, `m.claudeMeta`
- **Issue:** Four parallel slices that must always be appended in lockstep across 32 append sites. Index-out-of-bounds panic risk.
- **Remediation:** Replace with a `ChatEntry` struct and single `entries []ChatEntry` slice.
- **Status:** ✅ Resolved (2026-02-24) — Replaced with `ChatEntry` struct and `appendEntry()` helper.

### 10. Duplicated frontmatter parsing (4 implementations)
- **Locations:** `internal/job/job.go`, `internal/job/task.go`, `internal/job/blocker.go`, `internal/agents/agents.go`
- **Issue:** Four different approaches to the same `---`-delimited frontmatter format.
- **Remediation:** Create a shared `internal/frontmatter` package with a single generic parser.
- **Status:** ✅ Resolved (2026-02-24) — Created `internal/frontmatter` package with `Split()` and `Parse()`. All four consumers updated.

### 11. `internal/llm` package has too many responsibilities
- **Location:** `internal/llm/` — `client.go` (624 lines), `tools.go` (709 lines)
- **Issue:** Package is the API client, type system, tool registry, tool executor, gateway interface, and HTML converter.
- **Remediation:** Split into `internal/llm` (types), `internal/llm/client` (OpenAI-compatible client), `internal/llm/tools` (tool execution).
- **Status:** ✅ Resolved (2026-02-24) — Split into three focused sub-packages: `internal/llm` (shared types + Provider interface, ~170 lines), `internal/llm/client` (OpenAI-compatible streaming client, ~515 lines), `internal/llm/tools` (tool executor, ~690 lines).

### 12. macOS-only Keychain integration with no platform guard
- **Location:** `internal/anthropic/client.go:24-26`
- **Issue:** Shells out to macOS `security` CLI. Fails with unhelpful error on other platforms.
- **Remediation:** Add build tag or runtime `GOOS` check with clear error message.
- **Status:** ✅ Resolved (2026-02-24) — Added runtime `GOOS` guard with clear error message on non-macOS platforms.

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
- **Status:** ✅ Resolved (2026-02-24) — Extracted `renderScrollableModal` helper in `view.go`. Both modals call the shared helper.

### 17. Charm v2 libraries pinned to pre-release versions
- `bubbles/v2 v2.0.0-rc.1`, `bubbletea/v2 v2.0.0-rc.2`, `lipgloss/v2 v2.0.0-beta.3`
- Stable v2.0.0 releases are now available.
- **Status:** ✅ Resolved (2026-02-24) — Updated all three to stable `v2.0.0`. No breaking API changes.

### 18. Test coverage gaps in critical packages
- `cmd/` — 0%, `internal/anthropic/` — 0%, `internal/config/` — 0%
- `internal/gateway/` — 8.5%, `internal/llm/client` — 0%
- **Status:** ✅ Resolved (2026-02-24) — Overall coverage raised from 12.1% to 42.9% (target 40%). Key improvements: `llm/client` 0%→87.7%, `llm/tools` 6%→96.3%, `config` 0%→87.2%, `anthropic` 3.6%→42.9%, `gateway` 8.5%→28.4%, `tui` 15%→31%, `agents` 52%→72%, `job` 56%→86%.

### 19. `submitBlockerAnswers` closure captures pointer receiver
- **Location:** `internal/tui/blocker_modal.go` — `submitBlockerAnswers()` method
- **Issue:** The returned `tea.Cmd` closure captures `m` (pointer receiver) and accesses `m.jobs` asynchronously. Since `m` is a pointer receiver, this reads from the model after it may have been mutated by subsequent `Update()` calls. Latent data race risk.
- **Remediation:** Capture `m.jobs` into a local variable before the closure to snapshot the state.
- **Status:** ✅ Resolved (2026-02-24) — Job lookup moved before the closure; closure now captures only local variables (`jobDir`, `b`, `jobID`), not the pointer receiver.

### 20. `streamCompletion` and `streamCompletionWithTools` share ~80% duplicated code
- **Location:** `internal/llm/client/client.go` — two methods with nearly identical SSE parsing logic
- **Issue:** The only difference is tool definitions in the request and tool call accumulation. ~100 lines of duplicated SSE parsing, chunk handling, and error logic.
- **Remediation:** Extract a shared `doStream` helper that takes optional `[]llm.Tool` and a flag/callback for tool accumulation.
- **Status:** ✅ Resolved (2026-02-24) — Extracted `doStream` method (154 lines). Both public methods are now ≤15-line wrappers. Net reduction: 91 lines.

### 21. `GatewaySlot` and `AgentSpawner` don't belong in `internal/llm`
- **Location:** `internal/llm/types.go` — `GatewaySlot` struct and `AgentSpawner` interface
- **Issue:** These are gateway/orchestration concepts, not LLM communication types. They live in `llm` solely to break an import cycle (`gateway` → `llm` → `gateway`). Any new orchestration interface that `tools` needs will also end up here, gradually turning `types.go` into a grab-bag.
- **Remediation:** Consider a small `internal/iface` or `internal/orchestration` package for cross-cutting interfaces.
- **Status:** ✅ Resolved (2026-02-24) — Created `internal/orchestration/types.go`. Moved `GatewaySlot` and `AgentSpawner` out of `internal/llm`. No import cycles.

### 22. `internal/llm/client` has zero test coverage
- **Location:** `internal/llm/client/client.go` (~510 lines)
- **Issue:** SSE parsing, tool call accumulation across chunks, and HTTP request construction have no tests. The SSE parsing logic is particularly tricky — edge cases around `[DONE]` handling, streams ending without `[DONE]`, and tool call accumulation are easy to get wrong.
- **Remediation:** Add tests using `httptest.Server` to feed canned SSE responses through `streamCompletion` and `streamCompletionWithTools`.
- **Status:** ✅ Resolved (2026-02-24) — Added 26 tests covering streaming, tool calls, errors, context cancellation, non-streaming, and model fetching. Package coverage: 87.7%.

---

## Positive Observations

- **Clean build and vet** — zero warnings across the entire codebase
- **All tests pass** — existing test suite is green with no flaky tests
- **Clean `go mod tidy`** — no unused or phantom dependencies
- **Consistent error wrapping** — `fmt.Errorf("context: %w", err)` used correctly throughout
- **Good `context.Context` threading** — subprocess management properly threads context for cancellation
- **Strong test coverage** — 42.9% overall with critical packages well-covered: `llm/tools` 96.3%, `config` 87.2%, `llm/client` 87.7%, `job` 85.7%, `agents` 72.1%
- **Well-structured agent discovery** — `internal/agents/` has clean design with hot-reloading
- **Clean package dependency graph** — no circular dependencies
- **Thoughtful comments** — meaningful comments explaining *why* decisions were made

---

## Recommended Action Plan

| Priority | Effort | Item | Description | Status |
|---|---|---|---|---|
| **This week** | S | #1 | Add HTTP client timeout | ✅ Done |
| **This week** | S | #2 | Fix `--dangerously-skip-permissions` default | ✅ Done |
| **This week** | S | #3 | Update `x/crypto` and `x/net` | ✅ Done |
| **This week** | S | #7 | Run govulncheck | ✅ Done |
| **This week** | M | #4 | Fix errcheck findings | ✅ Done |
| **Next sprint** | M | #5 | Eliminate global state in tools.go | ✅ Done |
| **Next sprint** | M | #6 | Extract shared Claude stream types | ✅ Done |
| **Next sprint** | L | #9 | Replace parallel slices with struct | ✅ Done |
| **Next sprint** | M | #10 | Unify frontmatter parsing | ✅ Done |
| **Next sprint** | S | #12 | Add Keychain platform guard | ✅ Done |
| **Next sprint** | XL | #8 | Break up model.go | ✅ Done |
| **Next sprint** | M | #11 | Split `internal/llm` package | ✅ Done |
| **Backlog** | S | #13, #14, #15 | Dead code and lint fixes | ✅ Done |
| **Backlog** | S | #16 | Deduplicate modal rendering logic | ✅ Done |
| **Backlog** | M | #17 | Update Charm v2 to stable | ✅ Done |
| **Backlog** | L | #18 | Increase test coverage | ✅ Done (42.9%) |
| **Backlog** | S | #19 | Fix `submitBlockerAnswers` pointer capture | ✅ Done |
| **Backlog** | S | #20 | DRY up `streamCompletion` duplication | ✅ Done |
| **Backlog** | S | #21 | Move `GatewaySlot`/`AgentSpawner` out of `llm` | ✅ Done |
| **Backlog** | M | #22 | Add tests for `internal/llm/client` | ✅ Done |
