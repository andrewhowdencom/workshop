# Plan: Add VCS Status Verifier Cache

## Objective

Add infrastructure to track VCS (git) changes after a ReAct cycle completes and make the changed-files list available to verifiers via a shared, per-attempt cache. This enables self-selecting verifiers that run only when relevant files changed, and provides the foundation for targeted verification pipelines.

## Context

### Repository Topology
- **Language**: Go 1.26.2, module `github.com/andrewhowdencom/workshop`
- **Build tool**: Taskfile (`task build`, `task test`, `task lint`, `task validate`)
- **App framework**: `github.com/andrewhowdencom/ore` (replaced locally at `../ore`)
- **Core app logic**: `internal/app/app.go` wires ore conduits, provider, loop, guardrails, tools, and session manager

### Current State
- Workshop's `buildManager` uses `cognitive.NewTurnProcessor(cognitive.ReActFactory, tracer)` — **no `WithVerification` is wired up**
- `ore/x/verifier` exists in `go.mod` as `// indirect` (`v0.1.1`) but is **not directly imported** in any workshop source file
- The `ore` cognitive package provides `cognitive.WithVerification(inner Pattern, step loop.TurnSubmitter, opts ...VerificationOption)` in `ore/cognitive/verify.go`
- `verifier.RunAll` in `ore/x/verifier/aggregate.go` runs verifiers **in parallel goroutines**
- `verifier.Verifier` interface requires `Verify(ctx context.Context, st state.State) (VerificationResult, error)`
- `state.State` interface provides `Turns() []Turn` and `Append(role Role, artifacts ...Artifact)`

### ore Framework Verification Support (Verified)
| Component | API |
|---|---|
| `cognitive.WithVerification(inner, step, opts...)` | Wraps a `Pattern`, runs verifiers after `inner.Run` |
| `cognitive.WithVerifiers(v ...verifier.Verifier)` | `VerificationOption` that sets the verifier list |
| `cognitive.ReActFactory(step, prov, tracer)` | Creates `ReAct` pattern; `step` is `loop.TurnExecutor` |
| `verifier.RunAll(ctx, verifiers, st)` | Runs all verifiers in parallel, returns `[]VerificationResult` |
| `verifier.VerificationResult` | `{Name, Status, Report}` |
| `verifier.VerificationPass` / `VerificationFail` / `VerificationError` | Status constants |

### Key Architectural Detail
`WithVerification` retries the inner pattern up to `maxRetries` times if any verifier returns `VerificationFail`. Each retry adds a system turn with the verification report, which increases `len(st.Turns())`. This means the state fingerprint changes between attempts, providing a natural cache invalidation signal.

## Architectural Blueprint

### Selected Architecture
**Path A: Shared mutex-based cache keyed by state fingerprint → verifiers capture cache at creation time**

This is the simplest path that requires **no framework changes** and correctly handles the `WithVerification` retry behavior:
1. `internal/vcs/status.go` defines `StatusCache` with a `sync.Mutex` and a `lastFingerprint` field.
2. `ChangedFiles(st state.State)` computes a fingerprint from `len(st.Turns())`, checks the cache, and runs `git status` only when the fingerprint changes.
3. `internal/app/verifier.go` defines `VCSStatusVerifier` (cache populator) and example self-selecting verifiers. Each verifier captures a `*vcs.StatusCache` pointer when created by the factory function.
4. `buildManager` creates a single `StatusCache` instance per message, passes it to all verifiers, and wires `cognitive.WithVerification` around `ReActFactory`.

**Rejected paths:**
- *Path B (`sync.Once` cache)*: `sync.Once` cannot be reset, so the cache would be stale on `WithVerification` retries. The state fingerprint approach handles this cleanly without framework changes.
- *Path C (custom `Pattern` wrapper instead of `WithVerification`)*: Would require reimplementing the retry logic from `WithVerification`. The existing wrapper is sufficient; we only need a cache that invalidates on state change.
- *Path D (pre-verifier hook in `ore` framework)*: Would require modifying `ore/cognitive/verify.go` to add a hook between `inner.Run` and `verifier.RunAll`. This is unnecessary because the `VCSStatusVerifier` (first in the verifier list) achieves the same effect.

### Component Diagram
```
buildManager (internal/app/app.go)
├─ creates StatusCache ───────► internal/vcs/status.go
│                               ├─ ChangedFiles(st) → []string, error
│                               ├─ mutex + fingerprint cache
│                               └─ runs git status --porcelain -z
│
├─ factory func (per message)   internal/app/verifier.go
│   ├─ creates VCSStatusVerifier   ├─ Verify → populates cache, returns Pass
│   ├─ creates GoTestVerifier      ├─ checks cache for .go files
│   └─ creates LintVerifier        └─ skips if no relevant files
│
└─ cognitive.WithVerification(ReAct, step,
       cognitive.WithVerifiers(vcs, goTest, lint))
           │
           ▼
       verifier.RunAll (parallel)
           ├─ VCSStatusVerifier → git status (once)
           ├─ GoTestVerifier → reads cache, self-selects
           └─ LintVerifier → reads cache, self-selects
```

### Key Design Decisions
1. **`sync.Mutex` instead of `sync.Once`**: `sync.Once` cannot be reset, which would leave the cache stale on `WithVerification` retries. A mutex with a `lastFingerprint` field allows cheap re-validation on every call and re-population when the state changes.
2. **Fingerprint from `len(st.Turns())`**: Each retry adds a system turn (verification report), so the turn count strictly increases. This is a cheap, stable, and automatically-invalidating fingerprint.
3. **`VCSStatusVerifier` as first verifier**: `verifier.RunAll` runs verifiers in parallel, so there is no guaranteed ordering. However, the mutex ensures that even if all verifiers call `ChangedFiles` simultaneously, only one `git status` executes. The `VCSStatusVerifier` is still placed first as a semantic signal that it warms the cache.
4. **`-z` flag for `git status --porcelain`**: The `-z` flag uses NUL-terminated paths, which correctly handles filenames with spaces and special characters. This is more robust than line-based parsing.
5. **Graceful non-repo handling**: If `git status` fails (e.g., not a git repo, git not installed), the cache returns an empty `[]string` and a `nil` error. Verifiers treat this as "no files changed" and skip. This prevents the verification pipeline from crashing when run outside a git repo.
6. **TUI footer out of scope**: Per user request, TUI status updates are deferred. The cache is designed to be usable for TUI updates later (e.g., an `OnEmit` callback can call `ChangedFiles` independently).

## Requirements

1. Run `git status` at most once per `verifier.RunAll` invocation, even when multiple verifiers are registered.
2. Parse changed file paths from `git status --porcelain -z` output.
3. Make changed file paths available to verifiers for self-selection (run/skip based on file patterns).
4. Cache auto-invalidates when the conversation state changes (e.g., `WithVerification` retries).
5. Graceful handling when not in a git repository or when `git` is unavailable — return empty list, do not error.
6. [inferred] TUI footer updates are out of scope for this plan but the cache should be usable by future TUI integration.

## Task Breakdown

### Task 1: Create VCS Status Cache Package
- **Goal:** Create `internal/vcs/status.go` with a thread-safe cache that memoizes `git status` per state fingerprint.
- **Dependencies:** None
- **Files Affected:** None
- **New Files:** `internal/vcs/status.go`, `internal/vcs/status_test.go`
- **Interfaces:**
  - `func NewStatusCache(dir string) *StatusCache`
  - `func (c *StatusCache) ChangedFiles(st state.State) ([]string, error)`
- **Validation:** `go test ./internal/vcs` passes, `go vet ./internal/vcs` clean.
- **Details:**
  - `StatusCache` holds a `sync.Mutex`, `lastFingerprint string`, `files []string`, and `err error`.
  - `ChangedFiles` computes `fingerprint := strconv.Itoa(len(st.Turns()))`, checks if it matches `lastFingerprint`, and returns the cached `files`/`err` if so.
  - If the fingerprint differs, acquires the lock, double-checks the fingerprint, runs `git status --porcelain -z`, parses the NUL-terminated output, and stores the result.
  - `dir` parameter sets `cmd.Dir` on the `git` command. If empty, uses the current working directory.
  - If `git status` exits with an error (e.g., not a git repo), returns `[]string{}` and `nil` error. Only actual execution failures (e.g., `exec.LookPath` fails) return a non-nil error.
  - Tests must cover: cache hit, cache miss, fingerprint invalidation, concurrent access, non-git repo, empty repo.

### Task 2: Create VCS Status Populator Verifier
- **Goal:** Create a verifier that populates the cache and returns `VerificationPass` (data collection, not a gate).
- **Dependencies:** Task 1
- **Files Affected:** None
- **New Files:** `internal/app/verifier.go`, `internal/app/verifier_test.go`
- **Interfaces:**
  - `type VCSStatusVerifier struct { Cache *vcs.StatusCache }`
  - `func (v *VCSStatusVerifier) Verify(ctx context.Context, st state.State) (verifier.VerificationResult, error)`
- **Validation:** `go test ./internal/app` passes, `go vet ./internal/app` clean.
- **Details:**
  - `Verify` calls `v.cache.ChangedFiles(st)` and returns a `verifier.VerificationResult` with `Status: verifier.VerificationPass`, `Name: "vcs-status"`, and a short `Report` summarizing the number of changed files (e.g., "3 modified, 1 untracked").
  - This verifier never returns `Fail` or `Error` — it is purely a data-collection step.
  - Must be placed first in the verifier list so the cache is warm before other verifiers need it. (The mutex ensures correctness even if ordering is not guaranteed by `RunAll`.)
  - Tests must verify that `Verify` returns `Pass` and that the cache is populated after the call.

### Task 3: Create Example Self-Selecting Verifiers
- **Goal:** Create at least one example verifier that uses the cache to skip when no relevant files changed, demonstrating the pattern for future verifiers.
- **Dependencies:** Task 1, Task 2
- **Files Affected:** None
- **New Files:** `internal/app/verifier.go` (same file as Task 2, or `internal/app/verifier_examples.go`), `internal/app/verifier_examples_test.go` (or combined with `verifier_test.go`)
- **Interfaces:**
  - `type FilePatternVerifier struct { Name string; Cache *vcs.StatusCache; Pattern *regexp.Regexp; Inner verifier.Verifier }`
  - `func (v *FilePatternVerifier) Verify(ctx context.Context, st state.State) (verifier.VerificationResult, error)`
- **Validation:** `go test ./internal/app` passes, `go vet ./internal/app` clean.
- **Details:**
  - `FilePatternVerifier` checks if any changed files (from `cache.ChangedFiles(st)`) match `Pattern`. If no match, returns `VerificationPass` with `Report: "No relevant files changed — skipping"`. If match, delegates to `Inner.Verify(ctx, st)`.
  - Include at least one concrete example: `GoTestVerifier` that matches `\.go$` and delegates to `verifier.ExecVerifier{Name: "go-test", Command: "go", Args: []string{"test", "./..."}}`.
  - The example verifiers should be registered in `buildManager` (Task 4) so they are part of the integration.
  - Tests must verify: skip when no matching files, delegate when matching files exist, correct report content.

### Task 4: Wire Up WithVerification in buildManager
- **Goal:** Modify `buildManager` to use `cognitive.WithVerification` instead of plain `cognitive.ReActFactory`, passing VCS-aware verifiers with a shared cache.
- **Dependencies:** Task 1, Task 2, Task 3
- **Files Affected:** `internal/app/app.go`
- **New Files:** None
- **Interfaces:**
  - Modify the `processor` function in `buildManager`
  - Change the factory from `cognitive.ReActFactory` to a custom factory that creates `ReAct` and wraps it with `cognitive.WithVerification`
  - Add `import "github.com/andrewhowdencom/ore/x/verifier"` to `internal/app/app.go`
- **Validation:** `go test ./internal/app` passes, `go build ./cmd/workshop` succeeds, `go mod tidy` is clean.
- **Details:**
  - The factory function in `cognitive.NewTurnProcessor` receives `step loop.TurnExecutor`, which embeds `loop.TurnSubmitter`. This `step` can be passed to both `cognitive.ReActFactory(step, prov, tracer)` and `cognitive.WithVerification(react, step, ...)`.
  - Create a single `vcs.NewStatusCache(cwd)` instance (using `cwd` from `buildManager`) inside the factory function. The factory is called once per user message, so the cache is per-message (per-attempt for the first verification, and the fingerprint handles retries).
  - Create verifiers with the shared cache:
    ```go
    cache := vcs.NewStatusCache(cwd)
    verifiers := []verifier.Verifier{
        &VCSStatusVerifier{Cache: cache},
        &GoTestVerifier{Cache: cache}, // FilePatternVerifier with .go pattern
    }
    ```
  - Return `cognitive.WithVerification(react, step, cognitive.WithVerifiers(verifiers...))` from the factory.
  - Remove or keep the existing `cognitive.NewTurnProcessor(cognitive.ReActFactory, tracer)` pattern — replace with the new factory.
  - After modifying `app.go`, run `go mod tidy` to promote `ore/x/verifier` from `// indirect` to direct.

### Task 5: Add Integration Tests
- **Goal:** Verify the full flow: ReAct → verification → cache behavior (single execution, self-selection, invalidation).
- **Dependencies:** Task 4
- **Files Affected:** `internal/app/app_test.go`
- **New Files:** None
- **Interfaces:** N/A
- **Validation:** `go test ./internal/app` passes.
- **Details:**
  - Add tests that use `buildManager` with a mock provider and a test session to trigger the full `processor` pipeline.
  - Verify that `git status` is called at most once per `verifier.RunAll` invocation, even with multiple verifiers. This can be tested by mocking `exec.Command` or by using a mock `StatusCache`.
  - Verify that verifiers correctly self-select (skip when no relevant files, run when relevant files exist). Use a temporary git repository with controlled file changes.
  - Verify that cache invalidates when the state changes (e.g., between `WithVerification` retries). This can be tested by creating a mock `state.State` that returns different `Turns()` lengths on consecutive calls.
  - Ensure tests do not depend on the real git repository state of the workshop project itself. Use `testing.T.TempDir()` and `git init` to create isolated test repos.

## Dependency Graph
- Task 1 → Task 2
- Task 1 → Task 3
- Task 2 → Task 3
- Task 3 → Task 4
- Task 4 → Task 5

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| `git status` fails or hangs | Medium | Low | Cache returns empty list gracefully. Add a 5-second timeout to the `git` command via `context.WithTimeout`. |
| `WithVerification` retry changes state but cache doesn't invalidate | Medium | Low | State fingerprint (`len(st.Turns())`) strictly increases on each retry (system turn added). Cache is re-populated automatically. |
| Concurrent verifiers race on cache population | Medium | Low | `sync.Mutex` in `StatusCache` ensures only one `git status` runs per fingerprint. Other verifiers wait and return the cached result. |
| `ore/x/verifier` import not available in workshop | Low | High | Already present as `// indirect` in `go.mod`. Importing it in `app.go` will promote it to direct. Run `go mod tidy`. |
| Example verifiers (e.g., `go test`) fail in non-Go repos | Low | Medium | `FilePatternVerifier` self-selects based on changed files. If no `.go` files changed, it skips. This is the intended behavior. |

## Validation Criteria
- [ ] `go test ./...` passes (including new `internal/vcs` and `internal/app` tests).
- [ ] `go build ./cmd/workshop` succeeds.
- [ ] `go vet ./...` is clean.
- [ ] `go mod tidy` is clean (no unused imports, `ore/x/verifier` promoted to direct).
- [ ] `git status` is called at most once per `verifier.RunAll` invocation (verified by test or mock).
- [ ] Verifiers can access changed file paths and correctly self-select (skip/run) based on them.
- [ ] Cache correctly invalidates when the state fingerprint changes (verified by unit test).
- [ ] Non-git-repo scenarios return empty list without error (verified by test).
