# Plan: Propagate Anthropic Max-Tokens Default to Invoke Options

## Objective

Workshop's anthropic provider sends `max_tokens=1` to upstream providers
(causing the model to emit a single token and the turn to end) when the
user leaves `provider.max-tokens` unset. The 32000-token default is applied
in `newProvider`, but that function receives `ProviderConfig` **by value**,
so the defaulting mutation never reaches the caller's `cfg.provider`
struct. The subsequent read in `buildInvokeOptions` therefore sees `0`,
the `WithMaxTokens` option is never appended, and the upstream Anthropic
SDK falls back to its hardcoded `MaxTokens=1` initial value. The fix is
to change `newProvider`'s parameter to `*ProviderConfig` so the default
propagates, and to add regression tests that lock in the contract.

## Context

- `internal/app/app.go:642` — `newProvider(pc ProviderConfig, tracer trace.Tracer) (provider.Provider, error)`. The `case "anthropic":` arm (line ~660) contains the buggy mutation `pc.MaxTokens = defaultAnthropicMaxTokens` on a local copy. Doc comment at line 65 (`// Note: distinct from CompactionConfig.MaxTokens…`) is the user-facing description of the field.
- `internal/app/app.go:406` — the single production call site: `prov, err := newProvider(cfg.provider, tracer)`. `cfg` is a `*config`, so `&cfg.provider` is a `*ProviderConfig` into the live config struct.
- `internal/app/app.go:607-637` — `buildInvokeOptions(cfg *config, tools []tool.Tool)`. The anthropic branch reads `cfg.provider.MaxTokens` and guards on `if cfg.provider.MaxTokens > 0`. With the bug, this is always false in the default case, and the option is skipped.
- `internal/app/app.go:57-72` — `ProviderConfig` struct. `MaxTokens int64` is the field at issue. No struct changes required.
- `internal/app/app_test.go` — 11 call sites of `newProvider` (lines 47, 58, 69, 81, 92, 103, 123, 137, 164, 181, 200) all pass `pc` by value. The existing `TestNewProvider_Anthropic_AppliesDefaultMaxTokens` (line ~131) only asserts that `newProvider` does **not error** on zero MaxTokens; it does not assert the post-call state of `pc.MaxTokens`. This is the exact gap the bug exploits.
- `internal/app/app_test.go:211` — `optionTypes` helper, used by the existing `TestBuildInvokeOptions_*` tests. The new `TestBuildInvokeOptions_Anthropic_AppliesDefaultMaxTokens` will use this same helper for style consistency. The expected type-name string is `"anthropic.maxTokensOption"` (verified against the `ore` shim's `anthropic.maxTokensOption` struct at `../ore/x/provider/anthropic/anthropic.go:75`).
- `../ore/x/provider/anthropic/anthropic.go:300-307` — request builder uses `params.MaxTokens: 1` as the initial value, only overridden if `inv.maxTokensSet` is true. Doc comment at line 81 (`"The default (when this option is not supplied) is 1, the SDK's hard minimum, which will let the provider fail loudly…"`) confirms this is **intentional defensive behavior** by the shim. The shim is doing what it was designed to do; the bug is in workshop's wiring. (A follow-up design note: a `*int64`/`Optional` refactor of `ProviderConfig` would eliminate the entire class of "default doesn't propagate" bugs — out of scope for this plan.)
- `internal/app/truncation_smoke_test.go` — untracked, unrelated to this fix. Out of scope.
- `.worktrees/...` — independent git worktrees of the same code. Out of scope; do not modify.
- Project conventions (from `add-anthropic-provider.md` and `README.md`): kebab-case keys, `task validate` (lint + test + build) is the gate for each commit, defaults are layered (flag = 0, default applied in constructor), backwards compat preserved.

## Architectural Blueprint

Change `newProvider`'s parameter from `ProviderConfig` (value) to `*ProviderConfig` (pointer). The single production call site (`internal/app/app.go:406`) and the eleven test call sites in `internal/app/app_test.go` must be updated to pass `&cfg.provider` and `&pc` respectively. No other code changes are required: the existing mutation `pc.MaxTokens = defaultAnthropicMaxTokens` is preserved verbatim and now operates on the caller's struct via Go's auto-pointer-dereferencing.

This is the conservative fix (Option A from the ideation discussion). The more invasive alternative — moving the defaulting into `buildInvokeOptions` (Option B) — was considered and rejected for this plan because it would also require relocating the `MaxTokens <= ThinkingBudget` warning site (currently in `newProvider`), adding risk for the same observable outcome. Option A is the smaller, more localized change that preserves the plan's documented layering.

**Why the other `ProviderConfig` fields are not affected by the same bug class:** `ThinkingBudget`, `Temperature`, `ReasoningEffort`, `Kind`, `APIKey`, `Model`, and `BaseURL` are all **read-only** inside `newProvider` — the function inspects them to make constructor decisions but does not mutate any of them. Only `MaxTokens` is mutated, so only `MaxTokens` is the active bug. After the pointer change, all reads continue to work identically (Go auto-dereferences struct pointers for field access).

## Requirements

1. Change `newProvider`'s parameter type from `ProviderConfig` to `*ProviderConfig`.
2. Update the one production call site (`internal/app/app.go:406`) to pass `&cfg.provider`.
3. Update all eleven test call sites in `internal/app/app_test.go` to pass `&pc`.
4. Extend `TestNewProvider_Anthropic_AppliesDefaultMaxTokens` to additionally assert `pc.MaxTokens == defaultAnthropicMaxTokens` after the call. This is the direct regression test for the by-value mutation bug.
5. Add `TestBuildInvokeOptions_Anthropic_AppliesDefaultMaxTokens` that asserts `buildInvokeOptions` produces a `maxTokensOption` when starting from `MaxTokens=0`. This is the end-to-end test that the full pipeline (default → propagate → option list) is correct.
6. `task validate` must pass after the change.
7. No changes outside `internal/app/`. No changes to `.worktrees/`, `internal/app/truncation_smoke_test.go`, or any other file.

## Task Breakdown

### Task 1: Change `newProvider` to take `*ProviderConfig` and update call sites

- **Goal**: The `MaxTokens = defaultAnthropicMaxTokens` mutation in `newProvider`'s anthropic branch propagates to the caller's struct.
- **Dependencies**: None.
- **Files Affected**:
  - `internal/app/app.go` (function signature at line 642; one call site at line 406)
  - `internal/app/app_test.go` (11 call sites at lines 47, 58, 69, 81, 92, 103, 123, 137, 164, 181, 200)
- **New Files**: None.
- **Interfaces**:
  - `newProvider` signature changes from `func newProvider(pc ProviderConfig, tracer trace.Tracer) (provider.Provider, error)` to `func newProvider(pc *ProviderConfig, tracer trace.Tracer) (provider.Provider, error)`.
  - No other exported interface changes. `ProviderConfig` struct is unchanged.
- **Validation**:
  - `go build ./...` passes.
  - `go test ./internal/app/...` passes (full app package test suite, including the 8 existing anthropic `newProvider` tests and the 5 existing `buildInvokeOptions` tests).
  - `task validate` passes (lint + test + build per the project's gate).
- **Details**:
  1. In `internal/app/app.go:642`, change the signature: `func newProvider(pc *ProviderConfig, tracer trace.Tracer) (provider.Provider, error) {`.
  2. Add a one-line comment above the signature explaining why a pointer is required: `// newProvider takes a pointer because the anthropic branch mutates pc.MaxTokens // to apply the default; a value-pass would discard that mutation.`.
  3. In `internal/app/app.go:406`, change the call: `newProvider(&cfg.provider, tracer)`. (No other production call sites exist; verified by grep.)
  4. The body of `newProvider` does not need changes. Go auto-dereferences `pc` for field access (`pc.APIKey`, `pc.Model`, `pc.BaseURL`, `pc.Kind`, `pc.ThinkingBudget`, `pc.MaxTokens`), so all existing reads work unchanged. The existing `pc.MaxTokens = defaultAnthropicMaxTokens` line now writes through to the caller's struct.
  5. In `internal/app/app_test.go`, update every `newProvider(pc, nil)` to `newProvider(&pc, nil)`. Eleven call sites total. This is a mechanical change; each line is unique in the file (verified by reading lines 47-200).
  6. Do **not** modify the `optionTypes` helper, the test struct literals, or any other code in the test file. The only change is the prefix `&` on the first argument to `newProvider`.
  7. Do **not** modify anything in `.worktrees/`. Those are independent worktrees; if the plan is later backported, that's a separate change.
  8. After this task, the bug is fixed at runtime (the default propagates), but no test directly asserts the propagation. Task 2 closes that gap.

### Task 2: Add regression tests for the default propagation

- **Goal**: Lock in the fix so a future revert of Task 1's signature change is caught by the test suite.
- **Dependencies**: Task 1.
- **Files Affected**: `internal/app/app_test.go`.
- **New Files**: None.
- **Interfaces**: N/A.
- **Validation**:
  - `go test -run TestNewProvider_Anthropic_AppliesDefaultMaxTokens ./internal/app/...` passes.
  - `go test -run TestBuildInvokeOptions_Anthropic_AppliesDefaultMaxTokens ./internal/app/...` passes.
  - Both tests would FAIL if Task 1's signature change were reverted to value-pass (a mental walkthrough confirms: with value-pass, the `pc.MaxTokens = defaultAnthropicMaxTokens` line writes to a discarded local; the test reads `pc.MaxTokens` from the caller's still-zero struct; the assertion fails).
  - `task validate` passes.
- **Details**:
  1. **Extend** the existing `TestNewProvider_Anthropic_AppliesDefaultMaxTokens` (around `internal/app/app_test.go:131`). After the existing `if _, err := newProvider(pc, nil); err != nil { ... }` block, add an additional assertion:
     ```go
     if pc.MaxTokens != defaultAnthropicMaxTokens {
         t.Errorf("expected pc.MaxTokens to be mutated to default (%d) after newProvider; got %d (by-value pass would discard the default)",
             defaultAnthropicMaxTokens, pc.MaxTokens)
     }
     ```
     Change the existing call from `newProvider(pc, nil)` to `newProvider(&pc, nil)`. The pointer call is the contract under test; the existing value-pass call would still compile (Go allows this if the parameter were still a value) but would no longer exercise the fix.
  2. Update the test's doc comment to reflect the new assertion. Replace the existing comment with: `// TestNewProvider_Anthropic_AppliesDefaultMaxTokens verifies that the default MaxTokens is applied and propagates back to the caller. This is a regression test for the by-value pass bug — if the signature reverts to value-pass, the post-call assertion below fails because the local mutation is discarded.`
  3. **Add** a new test, `TestBuildInvokeOptions_Anthropic_AppliesDefaultMaxTokens`, that verifies the end-to-end pipeline. Place it adjacent to the other `TestBuildInvokeOptions_Anthropic_*` tests (after the `TestBuildInvokeOptions_Anthropic_OmitsReasoningEffort` test). Use the existing `optionTypes` helper (line 211) for consistency with the rest of the file:
     ```go
     // TestBuildInvokeOptions_Anthropic_AppliesDefaultMaxTokens is an
     // end-to-end regression test: when the user leaves provider.max-tokens
     // unset on the anthropic kind, buildInvokeOptions must include a
     // maxTokensOption. This proves the full pipeline (newProvider default
     // -> caller struct mutation -> buildInvokeOptions read -> option
     // appended) is correct, end to end. If the by-value bug in
     // newProvider regresses, this test fails because the option is
     // missing from the slice.
     func TestBuildInvokeOptions_Anthropic_AppliesDefaultMaxTokens(t *testing.T) {
         cfg := &config{
             provider: ProviderConfig{
                 Kind: "anthropic",
                 // MaxTokens intentionally left at 0 to trigger the
                 // defaulting path.
             },
         }
         got := optionTypes(buildInvokeOptions(cfg, nil))
         for _, ty := range got {
             if ty == "anthropic.maxTokensOption" {
                 return
             }
         }
         t.Errorf("expected anthropic.maxTokensOption in result, got %v", got)
     }
     ```
  4. No new imports are required. `strings` and `fmt` are already imported (lines 9, 11). `optionTypes` is already defined in the same file (line 211).
  5. The existing five `TestBuildInvokeOptions_Anthropic_*` tests should continue to pass unchanged. They use `MaxTokens: 16000` (non-zero), so they exercise the explicit-value path, not the defaulting path. This new test is the only one that exercises the defaulting path through `buildInvokeOptions`.

## Dependency Graph

- Task 1 → Task 2 (Task 2's first step changes the call site in `TestNewProvider_Anthropic_AppliesDefaultMaxTokens` from `pc` to `&pc`; this only makes sense — and only is testable as a regression — once Task 1 has changed the function signature to take a pointer)

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| Forgetting to update one of the 11 test call sites in `app_test.go` | Low (compile error catches it) | Medium | `go build ./...` and `go test ./...` will fail to compile. Run them before committing Task 1. |
| Future call site (e.g. a new test added later) uses the old value-pass convention | Low | Low | After the signature change, `newProvider(pc, ...)` is a compile error in Go. The compiler enforces the new convention. |
| The mutation pattern (write back to caller's struct) is surprising to future readers | Low | Medium | The one-line comment near the signature (added in Task 1, step 2) names the invariant. The regression test in Task 2 encodes the same intent in executable form. |
| The untracked `internal/app/truncation_smoke_test.go` could mask compile issues | Low | Low | This file is untracked, so `git diff` of this plan's commit will not touch it. The build will include it. Verify it still compiles via `go build ./...`. |
| Worktrees under `.worktrees/` diverge from main and are not updated | Low | High | Out of scope. Worktrees are isolated development environments; they will be re-rebased or re-merged as their owners see fit. Do not touch them in this plan. |
| The `*ProviderConfig` parameter convention is inconsistent with `newCompactor` (line ~678) which takes a value | Low | Low | `newCompactor` does not mutate its argument, so it can keep value-pass. The distinction is documented by the comment added in Task 1. If `newCompactor` ever needs to mutate, it should follow the same pattern. |
| `task validate` may flag a pre-existing lint issue (e.g. `errcheck`) | Low | Low | Out of scope. Pre-existing issues are not introduced by this plan. If the gate fails, address in a separate plan. |
| The plan grows to include the `*int64` ProviderConfig refactor (a "while we're here" temptation) | Medium | Medium | The refactor is a much larger conversation (touches config init, YAML schema, viper binding, every `if pc.X != 0` site, etc.) and is explicitly out of scope. The conservative fix is the right size for the bug. |
| Empirical confirmation: the user set `max_tokens=3200` (not 32000) when manually testing the workaround. Is there a secondary bug? | Low | Low | The 3200 is a user-chosen value, not a system default. The bug is about the *default*; once the user supplies any non-zero value, the explicit-value path works correctly. No secondary bug to investigate. |

## Validation Criteria

- [ ] `newProvider`'s parameter is `*ProviderConfig` (not `ProviderConfig`).
- [ ] `internal/app/app.go:406` calls `newProvider(&cfg.provider, tracer)`.
- [ ] All 11 call sites in `internal/app/app_test.go` pass `&pc` (or `&cfg.provider`) as the first argument.
- [ ] `TestNewProvider_Anthropic_AppliesDefaultMaxTokens` calls `newProvider(&pc, nil)` and asserts `pc.MaxTokens == defaultAnthropicMaxTokens` after the call.
- [ ] `TestBuildInvokeOptions_Anthropic_AppliesDefaultMaxTokens` exists, asserts that `buildInvokeOptions` produces an `"anthropic.maxTokensOption"` when starting from `MaxTokens=0`, and is placed adjacent to the other `TestBuildInvokeOptions_Anthropic_*` tests.
- [ ] `go build ./...` passes.
- [ ] `go test -race ./internal/app/...` passes (full app package test suite, including the new tests).
- [ ] `task validate` passes (lint + test + build).
- [ ] No changes outside `internal/app/`. Specifically: no changes to `.worktrees/`, `internal/app/truncation_smoke_test.go`, `go.mod`, `go.sum`, `README.md`, or `cmd/workshop/`.
- [ ] `git diff` of the implementation commit shows changes only in `internal/app/app.go` and `internal/app/app_test.go`.
