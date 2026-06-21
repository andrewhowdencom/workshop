# Plan: Make `/compact` Always Available

## Objective

Decouple the `/compact` slash command's availability from `compaction.max-tokens`. Currently `compaction.max-tokens: 0` disables `/compact` entirely (the handler returns `"compaction is not enabled"`), overloading the per-invocation output budget as a kill switch. The fix makes `/compact` always reachable and reduces `compaction.max-tokens` to a pure budget where `0` means "use the framework default" (8192 in `ore/x/compaction`).

## Context

`/compact` is a slash command in the workshop project (`github.com/andrewhowdencom/workshop`) that forces an immediate LLM-summarization of the active thread. It was added in issue #64 (`add-slash-compact-command`) and migrated to ore's `compaction.Summarize` in #50 (`migrate-to-ore-compaction-with-llm-summarize`). ore v0.12 made compaction explicit-only — there is no automatic pre-turn trigger — so the only way to compact today is via the `/compact` slash command.

The current `internal/app/app.go` overloads `compaction.max-tokens` as both a per-call output budget and a kill switch:

- `compactCommand.Handler` (lines 605-613) returns `compaction is not enabled` if `c.prov == nil`.
- `buildManager` (lines 769-777) only populates `ccProv`/`ccSpec` when `cfg.compaction.MaxTokens > 0`. When `MaxTokens <= 0`, both stay at zero values and the handler hits the kill switch.

The `CompactionConfig` struct's own docstring (lines 102-108) explicitly documents the kill-switch semantics, but the user-facing descriptions (flag help, config example, README) are partially out of sync with the code. The `## Compaction` README section and the `## Compaction` config example describe an auto-trigger that no longer exists.

Related issues in the repo:

- #50 — original migration (still relevant: defines the current contract being refined)
- #55 — `compaction: default max-tokens (100k) is too high for many models` (separate concern; out of scope)
- #64 — `add-slash-compact-command` (introduced the command)

## Architectural Blueprint

No new components. This is a contract change for an existing slash command and a documentation alignment pass.

Three candidate approaches were considered:

| Path | Shape | Verdict |
|---|---|---|
| **A. In-place edit** | Remove the kill switch in `Handler` and the wiring guard in `buildManager`; always populate `ccProv`/`ccSpec`. | **Selected** — the `compileProviders` call plus the explicit `compaction.provider` name check at `app.go:758-760` already defend against the "no provider configured" failure mode. |
| B. Defensive guard | Keep a nil check, but `panic` at startup if the wiring can't produce a non-nil `prov`. | Overkill — startup-time validation already covers the impossible-in-practice case. |
| C. Lazy resolution | Drop the `prov`/`spec` fields on `compactCommand`; resolve per-call from a config snapshot. | Lifecycle complexity with no benefit. |

The handler's dead-code `c.prov == nil` branch is removed. The wiring always populates `ccProv` from the resolved compaction provider (which falls back to the default inference provider when `compaction.provider` is empty, per the existing logic at `app.go:754-757`) and `ccSpec` from the configured `MaxTokens`. When `MaxTokens <= 0`, `ccSpec.MaxOutputTokens` is `0`, which the `ore/x/compaction` package treats as "use framework default".

```
Before:  /compact → check(c.prov == nil) → error
         /compact → Summarize(prov, spec{0, 0})  [unreachable when MaxTokens=0]

After:   /compact → Summarize(prov, spec{MaxTokens})
         MaxTokens=0 → spec.MaxOutputTokens=0 → framework default 8192
```

## Requirements

1. The `/compact` slash command must succeed regardless of the value of `compaction.max-tokens`, as long as a provider is configured.
2. `compaction.max-tokens` must be treated as a pure per-invocation output budget for `compaction.Summarize`; `0` means "use framework default" (8192).
3. The handler must not return the `compaction is not enabled` error for any reachable user config.
4. All user-facing descriptions (flag help, config example, README) must match the new contract.
5. The existing `TestCompactSlashHandler_Disabled` test, which asserts the now-obsolete error, must be removed or replaced.
6. The misleadingly named `TestBuildManager_CompactionDisabled` must be renamed and its comment updated to reflect the new contract.

## Task Breakdown

### Task 1: Decouple `/compact` Availability from `compaction.max-tokens` in Code and Tests

- **Goal**: Remove the kill switch in `compactCommand.Handler` and the wiring guard in `buildManager`; update tests to match the new contract.
- **Dependencies**: None.
- **Files Affected**: `internal/app/app.go`, `internal/app/app_test.go`
- **New Files**: None.
- **Interfaces**: No public interface changes. The `CompactionConfig.MaxTokens` field retains its `int` type and `0`-means-default semantics; the change is in how the field is consumed.
- **Validation**:
  - `go build ./...` compiles cleanly.
  - `go test -race ./internal/app/...` passes.
  - `go test -race ./cmd/workshop/...` passes.
  - The replacement test (replacing `TestCompactSlashHandler_Disabled`) asserts that the handler succeeds when `prov` is non-nil and `spec.MaxOutputTokens` is `0` (i.e. `MaxTokens: 0` does not disable `/compact`).
- **Details**:
  - In `internal/app/app.go`, remove the `if c.prov == nil { return ..., "compaction is not enabled" }` block (lines 611-613).
  - Update the `Handler` docstring (lines 605-607) to remove the "If compaction is disabled, it returns an error" sentence; describe the new contract instead.
  - Update the `CompactionConfig.MaxTokens` field docstring (lines 102-108) to drop the kill-switch description; state that `0` means "use framework default" and that the field is a pure budget.
  - In `buildManager` (lines 763-777), remove the `if cfg.compaction.MaxTokens > 0` guard and unconditionally populate `ccProv = compactionProv` and `ccSpec = models.Spec{Name: cfg.providers[compactionName].Model, MaxOutputTokens: int64(cfg.compaction.MaxTokens)}`. Update the surrounding block comment (lines 763-768) to reflect the new wiring.
  - In `internal/app/app_test.go`, delete or rewrite `TestCompactSlashHandler_Disabled` (lines 601-627). The replacement test should construct a `compactCommand` with `prov: <non-nil testSummarizeProvider>` and `spec: models.Spec{MaxOutputTokens: 0}`, then assert that `Handler` does NOT return an error and that the stream's turn count is incremented as in `TestCompactSlashHandler_Enabled`.
  - In `internal/app/app_test.go`, rename `TestBuildManager_CompactionDisabled` (lines 2851-2875) to e.g. `TestBuildManager_CompactionZeroBudget` and update the comment block to describe the new contract: when `compaction.max-tokens` is `0`, `/compact` is still reachable and uses the framework default budget. The test body itself (which only checks that `buildManager` does not error) does not need to change.

### Task 2: Align User-Facing Descriptions with the New Contract

- **Goal**: Update the flag help text, config example, and README to describe `/compact` as always-available with a per-call budget.
- **Dependencies**: Task 1.
- **Files Affected**: `cmd/workshop/root.go`, `config.yaml.example`, `README.md`
- **New Files**: None.
- **Interfaces**: No interface changes; only user-facing strings.
- **Validation**:
  - `go build ./...` still compiles (the flag help text is a string literal).
  - `go test -race ./cmd/workshop/...` still passes.
  - Manual review: every `compaction.max-tokens` reference in the affected files describes the new contract; the phrase "0 = disabled" or "trigger compaction when tokens exceed" no longer appears.
- **Details**:
  - In `cmd/workshop/root.go:37`, update the flag help text from `"... (0 = disabled; framework default otherwise is 8192)"` to `"... (0 = use framework default, 8192)"`.
  - In `config.yaml.example`, replace the comment block at lines 47-53. The new comment should describe `compaction.max-tokens` as a per-invocation output budget where `0` means "use the framework default" (8192). Drop the "Set `max-tokens: 0` to disable /compact entirely" line.
  - In `README.md`:
    - Line 280: Replace the inline YAML comment `# 0 = disabled; trigger compaction when tokens exceed this` with `# per-invocation output budget; 0 = use framework default (8192)`.
    - Lines 297-308: Rewrite the `## Compaction` section. Drop the "When triggered" automatic-trigger language. The new section should describe `/compact` as the only way to compact, explain that `compaction.max-tokens` is the per-call budget, and note that `0` means "use framework default" (8192).
    - Lines 330-333: Update the `/compact` description. Remove the "If compaction is disabled (`compaction.max-tokens: 0`), the command will return an error" sentence; the new wording is just "This immediately compacts the conversation history regardless of the current token count." Optionally retain a sentence that the per-call budget is configurable via `compaction.max-tokens`.
    - Lines 403 and 416: Update both flag-table rows to describe the new contract: e.g. `Per-invocation output budget for /compact (0 = use framework default, 8192)`.

## Dependency Graph

- Task 1 → Task 2 (Task 2's doc updates should reflect the final code semantics; no functional dependency, but a documentation-consistency one).

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| Removing the `c.prov == nil` check leaves the handler susceptible to nil-pointer panic if `buildManager` ever regresses | Low | Low | The new wiring unconditionally populates `ccProv` from a resolved provider; `compileProviders` and the `compaction.provider` name check at `app.go:758-760` ensure the wiring is always valid. A future regression would surface immediately, not silently. |
| Behavior change for users who relied on `max-tokens: 0` to disable `/compact` | Medium | Low | Intentional per the user's request; README and flag help are updated in the same pass. |
| The pre-existing inconsistency between the flag default (`100000`) and the framework default (`8192`) becomes more visible | Low | Low | Out of scope for this plan; flagged in "Non-goals" for follow-up. |
| The `testSummarizeProvider` may not exercise the `MaxOutputTokens: 0` path in a way the test can assert | Low | Low | The replacement test only needs to assert that the handler does not return the now-obsolete error and that compaction still produces the expected turn count. |
| The framework's "0 means default" behavior is not formally tested here | Low | Low | Trusted per the existing comment at `app.go:767-768`; the `ore/x/compaction` package is the source of truth. |

## Non-goals

- Changing the default value of `--compaction.max-tokens` from `100000` to the framework default (`8192`). This is a separate concern (see issue #55).
- Adding a new config field (e.g. `compaction.enabled`) as a replacement kill switch. The user explicitly chose not to keep the ability to disable compaction via config.
- Modifying the `compaction.NewTransform()` pre-turn projection in `buildManager` (line 878). It is a separate concern and is unaffected by this change.
- Modifying the underlying `ore/x/compaction` package.

## Validation Criteria

- [ ] `go build ./...` succeeds.
- [ ] `go test -race ./...` passes with zero failures.
- [ ] `golangci-lint run ./...` is clean.
- [ ] `task validate` (the standard pre-commit gate defined in `Taskfile.yml`) passes.
- [ ] The handler in `internal/app/app.go` does not return the string `"compaction is not enabled"` for any reachable config.
- [ ] `compaction.max-tokens: 0` results in a `models.Spec.MaxOutputTokens: 0` being passed to `compaction.Summarize`.
- [ ] The flag help text for `--compaction.max-tokens` no longer contains the phrase `"0 = disabled"`.
- [ ] The `## Compaction` README section no longer describes an automatic pre-turn trigger.
- [ ] Both flag-table rows in `README.md` (lines 403 and 416) describe the new contract.
- [ ] The replacement test in `app_test.go` (replacing `TestCompactSlashHandler_Disabled`) passes and asserts the new contract.
- [ ] `TestBuildManager_CompactionDisabled` is renamed to `TestBuildManager_CompactionZeroBudget` (or similar) and its comment is updated.