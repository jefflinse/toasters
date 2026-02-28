# Pre-Phase 4 — Wave 1: Safety & Cleanup

**Created:** 2026-02-27
**Status:** ✅ Complete (2026-02-27)
**Prerequisite for:** Wave 2, Phase 4 development
**Source:** `PRE_PHASE_4_ARCH_REVIEW.md` Section 11

---

## Purpose

Wave 1 eliminates critical security vulnerabilities, removes ~4,600 lines of dead code, and fixes concurrency/quality issues that create noise and risk. These are prerequisite fixes — do them all before starting Wave 2 or any Phase 4 feature work.

**Why this matters:** The dead code inflates coverage metrics, confuses navigation, and creates false import dependencies. The security fixes close real attack vectors. The concurrency fixes prevent potential hangs and races. Completing Wave 1 gives us a clean, honest codebase to build Phase 4 on.

---

## Progress Tracking

This file is the source of truth for Wave 1 execution. Update the status checkboxes and notes as each task is completed. When all tasks are done, update the Status at the top to "✅ Complete" with the date.

---

## Tasks

### Task 1.1: Fix `setup_workspace` Command Injection

- **Status:** ✅ Complete
- **Finding:** SEC-CRITICAL-1
- **Severity:** CRITICAL
- **Effort:** Small
- **Agent:** builder
- **Files:** `internal/operator/workspace_tools.go`

**Problem:**

The `setup_workspace` tool passes LLM-controlled `repo.URL` and `repo.Name` directly to `exec.CommandContext("git", "clone", repo.URL, name)`. Three attack vectors:

1. **Flag injection**: URL like `--upload-pack=malicious_command` interpreted as git flag
2. **`ext::` protocol**: `ext::sh -c 'command'` executes arbitrary shell commands
3. **Name injection**: Name like `--config=core.sshCommand=malicious` interpreted as git flag

**Fix:**

```go
// Validate URL scheme
u, err := url.Parse(repo.URL)
if err != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh" && u.Scheme != "git") {
    return "", fmt.Errorf("invalid git URL scheme")
}
// Reject flag injection
if strings.HasPrefix(repo.URL, "-") || strings.HasPrefix(name, "-") {
    return "", fmt.Errorf("invalid argument: must not start with '-'")
}
// Validate name: alphanumeric only
if repo.Name != "" && !regexp.MustCompile(`^[a-zA-Z0-9._-]+$`).MatchString(repo.Name) {
    return "", fmt.Errorf("invalid repo name")
}
// Use "--" to separate flags from positional arguments
cmd := exec.CommandContext(cloneCtx, "git", "clone", "--", repo.URL, name)
```

**Acceptance criteria:**
- [ ] URL scheme validated (only `https`, `http`, `ssh`, `git` allowed)
- [ ] Flag injection rejected (args starting with `-`)
- [ ] Repo name validated (alphanumeric + `.`, `_`, `-` only)
- [ ] `--` separator used before positional args
- [ ] Tests cover all three attack vectors
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes

**Verification:**
```bash
grep -n 'exec.CommandContext.*"git".*"clone"' internal/operator/workspace_tools.go
# Should show "--" separator and validation above the exec call
```

---

### Task 1.2: Expand `.gitignore`

- **Status:** ✅ Complete
- **Finding:** SEC-HIGH-2
- **Severity:** HIGH
- **Effort:** Trivial
- **Agent:** builder
- **Files:** `.gitignore`

**Problem:**

`.gitignore` only contains `toasters` (the binary). Missing patterns for sensitive and generated files.

**Fix:**

Add these patterns:
```
# Binary
toasters

# Database
*.db
*.db-shm
*.db-wal

# Logs
*.log

# Config (may contain API keys)
config.yaml

# Environment
.env

# Test coverage
coverage.out
coverage.html

# IDE
.idea/
.vscode/
*.swp
*.swo
*~
```

**Acceptance criteria:**
- [ ] All patterns above present in `.gitignore`
- [ ] No tracked files are accidentally ignored (run `git status` after)
- [ ] Verify no `.db`, `.log`, `.env`, or `config.yaml` files are currently tracked

**Verification:**
```bash
wc -l .gitignore
# Should be >15 lines (was 1)
```

---

### Task 1.3: Delete Legacy `llm` Package Family (DEAD-1)

- **Status:** ✅ Complete
- **Finding:** DEAD-1
- **Severity:** BLOCKING
- **Effort:** Medium
- **Agent:** builder
- **Files:** See deletion plan below

**Problem:**

The codebase has two complete, parallel provider systems. The legacy one (`internal/llm/client`, `internal/llm/types.go`, `internal/llm/provider.go`, `internal/anthropic/client.go`) is ~4,600 lines of dead code. Nothing in the production code path uses `llm.Provider` — the entire runtime uses `provider.Provider`.

The only actively-used code in the legacy packages is `anthropic.ReadKeychainAccessToken()` (~200 lines of keychain/OAuth helpers), called by `provider/anthropic.go`.

**Execution plan (3 steps):**

#### Step 1: Extract keychain helpers (~200 lines to keep)

Create `internal/anthropic/keychain.go` containing:
- `ReadKeychainAccessToken() (string, error)` (exported, used by `provider/anthropic.go`)
- `readKeychainCredentials() (*keychainCredentials, error)` (unexported)
- `readKeychainBlob(service string) ([]byte, error)` (unexported)
- `writeKeychainBlob(blob []byte) error` (unexported)
- `refreshAccessToken(creds) (*tokenResponse, error)` (unexported)
- `formatAPIError(resp) error` (unexported)
- Types: `keychainCredentials`, `keychainBlob`, `keychainOauth`, `tokenResponse`
- Constants: `keychainService`, `keychainAccount`, `oauthTokenURL`

#### Step 2: Delete dead files

| Action | File | Lines |
|--------|------|-------|
| DELETE | `internal/llm/types.go` | 110 |
| DELETE | `internal/llm/provider.go` | 23 |
| DELETE | `internal/llm/doc.go` | 4 |
| DELETE | `internal/llm/client/client.go` | 408 |
| DELETE | `internal/llm/client/client_test.go` | 1,455 |
| DELETE | `internal/llm/client/doc.go` | 3 |
| GUT | `internal/anthropic/client.go` → delete everything except what moved to `keychain.go` | ~560 dead |
| PRUNE | `internal/anthropic/client_test.go` → keep only tests for keychain functions | ~1,600 dead |

After deletion, the `internal/llm/client/` directory should be completely removed. The `internal/llm/` directory should contain only the `tools/` subdirectory.

#### Step 3: Verify

```bash
go build ./...                    # must pass
go test ./...                     # must pass
grep -r "internal/llm/client" .   # must return nothing
grep -r "internal/llm\"" .        # must return only llm/tools imports
```

**Acceptance criteria:**
- [ ] `internal/anthropic/keychain.go` created with all keychain helpers
- [ ] `internal/llm/client/` directory deleted entirely
- [ ] `internal/llm/types.go`, `internal/llm/provider.go`, `internal/llm/doc.go` deleted
- [ ] `internal/anthropic/client.go` gutted (only keychain code remains, or file deleted if all moved)
- [ ] `internal/anthropic/client_test.go` pruned to keychain tests only
- [ ] `provider/anthropic.go` still imports and uses `ReadKeychainAccessToken()` correctly
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes
- [ ] No imports of `internal/llm/client` anywhere
- [ ] No imports of `internal/llm"` except from `llm/tools`

**Notes for the builder:**
- Read `internal/anthropic/client.go` carefully to identify exactly which functions are used by `provider/anthropic.go`. The keychain functions are the only live code.
- The `internal/llm/tools/` package is NOT dead — it's the operator tool dispatcher. It stays. It just happens to live under `internal/llm/` for historical reasons (addressed in Wave 2 as DEAD-3).
- After this task, `internal/llm/` will contain only `tools/` — an orphaned package path that Wave 2 will relocate.

---

### Task 1.4: Extract Shared SSRF Protection (STRUCT-1 partial)

- **Status:** ✅ Complete
- **Finding:** STRUCT-1 (partial — full consolidation is Wave 2)
- **Severity:** HIGH
- **Effort:** Small
- **Agent:** builder
- **Files:** `internal/runtime/tools.go`, `internal/llm/tools/handler_web.go` (or `tools.go`), new `internal/httputil/` package

**Problem:**

SSRF protection (private network detection) is duplicated in two places:
1. `internal/runtime/tools.go` — agent tool `web_fetch`
2. `internal/llm/tools/` — operator tool `fetchWebpage`

Both have their own copy of `privateNetworks` (CIDR list) and `isPrivateIP()`. This is security-critical code that must be maintained in sync.

**Fix:**

Create `internal/httputil/` package with:
- `var PrivateNetworks []*net.IPNet` — the canonical CIDR list
- `func IsPrivateIP(ip net.IP) bool` — checks against PrivateNetworks
- `func NewSafeClient(timeout time.Duration) *http.Client` — HTTP client with SSRF protection (dial hook that rejects private IPs), timeouts, and redirect limits
- Optionally: `func SafeGet(ctx context.Context, url string) (*http.Response, error)` — convenience wrapper

Then update both call sites to use the shared package.

**Acceptance criteria:**
- [ ] `internal/httputil/` package created with SSRF protection
- [ ] `internal/runtime/tools.go` uses `httputil.IsPrivateIP` or `httputil.NewSafeClient`
- [ ] `internal/llm/tools/` uses `httputil.IsPrivateIP` or `httputil.NewSafeClient`
- [ ] No duplicate `privateNetworks` or `isPrivateIP` definitions remain
- [ ] Tests for `httputil` package (private IP detection, safe client behavior)
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes

**Verification:**
```bash
grep -rn "privateNetworks" internal/
# Should only appear in internal/httputil/
```

---

### Task 1.5: Add File Size Limits to `editFile` and `writeFile`

- **Status:** ✅ Complete
- **Finding:** SEC-MEDIUM-1, SEC-MEDIUM-2
- **Severity:** MEDIUM
- **Effort:** Small
- **Agent:** builder
- **Files:** `internal/runtime/tools.go`

**Problem:**

- `editFile` calls `os.ReadFile(resolved)` with no size limit. An LLM directed to edit a multi-GB file causes OOM.
- `writeFile` has no size limit on `params.Content`. Partially mitigated by LLM token limits but not enforced.

**Fix:**

For `editFile` (around line 463-498):
```go
// Before os.ReadFile, add:
info, err := os.Stat(resolved)
if err != nil {
    return "", fmt.Errorf("stat file: %w", err)
}
const maxEditFileSize = 10 * 1024 * 1024 // 10 MB
if info.Size() > maxEditFileSize {
    return "", fmt.Errorf("file too large to edit: %d bytes (max %d)", info.Size(), maxEditFileSize)
}
```

For `writeFile` (around line 436-461):
```go
const maxWriteContentSize = 50 * 1024 * 1024 // 50 MB
if len(params.Content) > maxWriteContentSize {
    return "", fmt.Errorf("content too large to write: %d bytes (max %d)", len(params.Content), maxWriteContentSize)
}
```

**Acceptance criteria:**
- [ ] `editFile` rejects files larger than 10 MB with a clear error message
- [ ] `writeFile` rejects content larger than 50 MB with a clear error message
- [ ] Tests verify both limits
- [ ] Existing tests still pass
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes

---

### Task 1.6: Add Timeout to `Runtime.Shutdown()`

- **Status:** ✅ Complete
- **Finding:** CONC-4
- **Severity:** MEDIUM
- **Effort:** Small
- **Agent:** builder
- **Files:** `internal/runtime/runtime.go`

**Problem:**

`Runtime.Shutdown()` (around line 254-277) polls `len(r.sessions)` with `time.Sleep(10 * time.Millisecond)` in a loop. No timeout — a hung session blocks exit forever.

**Fix:**

Replace the busy-wait with a `sync.WaitGroup` and timeout:

```go
func (r *Runtime) Shutdown() {
    r.mu.Lock()
    for _, s := range r.sessions {
        s.Cancel()
    }
    r.mu.Unlock()

    done := make(chan struct{})
    go func() {
        r.wg.Wait()
        close(done)
    }()

    select {
    case <-done:
        slog.Info("runtime shutdown complete")
    case <-time.After(10 * time.Second):
        slog.Warn("runtime shutdown timed out, some sessions may still be running")
    }
}
```

This requires adding a `sync.WaitGroup` field to `Runtime` and calling `wg.Add(1)` when sessions start and `wg.Done()` when they end. Check if a WaitGroup already exists — if so, use it.

**Acceptance criteria:**
- [ ] `Shutdown()` uses `sync.WaitGroup` (or equivalent) instead of busy-wait
- [ ] 10-second timeout prevents indefinite hang
- [ ] Timeout logs a warning via `slog.Warn`
- [ ] Normal shutdown (all sessions complete) still works correctly
- [ ] Tests verify timeout behavior
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes

**Verification:**
```bash
grep -n "time.Sleep" internal/runtime/runtime.go
# Should no longer appear in Shutdown()
```

---

### Task 1.7: Fix `fetchWebpage` Missing Context

- **Status:** ✅ Complete
- **Finding:** QUAL-1
- **Severity:** MEDIUM
- **Effort:** Trivial
- **Agent:** builder
- **Files:** `internal/llm/tools/handler_web.go` (or `tools.go` — check which file contains `fetchWebpage`)

**Problem:**

`fetchWebpage` uses `http.NewRequest` instead of `http.NewRequestWithContext`. The handler receives a `context.Context` but doesn't propagate it to the HTTP request. This means cancellation doesn't work.

**Fix:**

Change:
```go
req, err := http.NewRequest("GET", url, nil)
```
To:
```go
req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
```

**Note:** If Task 1.4 (SSRF extraction) is done first, this fix may be absorbed into the shared `httputil.SafeGet()` function. Check whether the `fetchWebpage` handler already uses the shared client after Task 1.4. If so, mark this task as "absorbed by Task 1.4" and skip it.

**Acceptance criteria:**
- [ ] `http.NewRequestWithContext` used (or absorbed into shared SSRF client)
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes

**Verification:**
```bash
grep -n "http.NewRequest(" internal/llm/tools/
# Should show only NewRequestWithContext (no bare NewRequest)
```

---

### Task 1.8: Add Mutex to Token Refresh

- **Status:** ✅ Complete
- **Finding:** SEC-MEDIUM-3
- **Severity:** MEDIUM
- **Effort:** Small
- **Agent:** builder
- **Files:** `internal/anthropic/client.go` (or `keychain.go` after Task 1.3)

**Problem:**

Multiple goroutines can simultaneously refresh an expired Keychain access token, causing a race on Keychain writes and potential refresh token rotation issues (if the OAuth server rotates refresh tokens, the second goroutine's refresh will fail because the first already consumed the old refresh token).

**Fix:**

Add a `sync.Mutex` (or `sync.Once`-style pattern) around the token refresh path. The mutex should be at the package level or on a singleton, since `ReadKeychainAccessToken()` is a package-level function.

```go
var refreshMu sync.Mutex

func ReadKeychainAccessToken() (string, error) {
    refreshMu.Lock()
    defer refreshMu.Unlock()
    // ... existing logic (read creds, check expiry, refresh if needed)
}
```

**Note:** If Task 1.3 (DEAD-1) is done first, this code will be in `internal/anthropic/keychain.go`. Apply the fix there.

**Acceptance criteria:**
- [ ] Token refresh is serialized via mutex
- [ ] Concurrent callers block rather than racing
- [ ] Tests verify no data race (run with `-race` flag)
- [ ] `go build ./...` passes
- [ ] `go test ./... -race` passes

---

## Execution Order

Tasks can be executed in this order. Some can be parallelized:

```
Parallel group 1 (independent):
  Task 1.1 (setup_workspace injection)
  Task 1.2 (.gitignore)
  Task 1.5 (file size limits)
  Task 1.6 (shutdown timeout)

Sequential (depends on nothing, but do before 1.7 and 1.8):
  Task 1.3 (DEAD-1: delete legacy llm package)

Depends on 1.3:
  Task 1.8 (token refresh mutex — file location depends on 1.3 extraction)

Parallel group 2 (after 1.3):
  Task 1.4 (SSRF extraction)
  Task 1.7 (fetchWebpage context — may be absorbed by 1.4)
```

## Verification (All Tasks Complete)

After all Wave 1 tasks are done, run:

```bash
# Full build
go build ./...

# Full test suite
go test ./... -count=1

# Race detector
go test ./... -race -count=1

# Lint
golangci-lint run

# Verify dead code removed
ls internal/llm/client/ 2>/dev/null && echo "FAIL: llm/client still exists" || echo "OK: llm/client removed"
grep -r "internal/llm/client" . && echo "FAIL: llm/client still imported" || echo "OK: no llm/client imports"

# Verify SSRF consolidated
grep -rn "privateNetworks" internal/ | grep -v httputil && echo "FAIL: duplicate SSRF" || echo "OK: SSRF consolidated"

# Verify .gitignore expanded
test $(wc -l < .gitignore) -gt 10 && echo "OK: .gitignore expanded" || echo "FAIL: .gitignore still minimal"
```

All commands must pass with zero errors and zero lint findings.

---

## Post-Wave 1

After Wave 1 is complete:
1. Update this file's status to "✅ Complete" with the date
2. Update `PRE_PHASE_4_ARCH_REVIEW.md` Section 10 findings registry with completion status
3. Update `CLAUDE.md` Tech Debt section to reference Wave 1 completion
4. Proceed to Wave 2 (`PRE_PHASE_4_WAVE_2.md`)
