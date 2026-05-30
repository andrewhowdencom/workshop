# Plan: Upgrade ore Dependencies to Latest

## Objective
Upgrade all `andrewhowdencom/ore` Go module dependencies to their latest published versions, then fix any compilation and test failures caused by breaking API changes — specifically adapting to any session/thread concept consolidation ("squashing") introduced in newer ore releases.

## Context
This codebase (`github.com/andrewhowdencom/workshop`) is the sole consumer of the `andrewhowdencom/ore` library. The current dependency versions were established by a prior plan (`refactor-workshop-for-ore-v0.2.0-api.md`) that refactored the app for ore v0.2.0-era APIs. Now a new wave of ore releases is available.

**Current vs. latest ore versions:**
| Module | Current | Latest | Bump? |
|---|---|---|---|
| `github.com/andrewhowdencom/ore` | v0.2.0 | v0.2.1 | Yes |
| `github.com/andrewhowdencom/ore/x/conduit/http` | v0.3.0 | v0.3.1 | Yes |
| `github.com/andrewhowdencom/ore/x/conduit/stdio` | v0.1.2 | v0.1.3 | Yes |
| `github.com/andrewhowdencom/ore/x/conduit/tui` | v0.3.0 | v0.3.1 | Yes |
| `github.com/andrewhowdencom/ore/x/provider/openai` | v0.2.1 | v0.3.0 | Yes |
| `github.com/andrewhowdencom/ore/x/tool` | v0.3.0 | v0.3.0 | No |
| `github.com/andrewhowdencom/ore/x/tool/bash` | v0.2.0 | v0.2.0 | No |
| `github.com/andrewhowdencom/ore/x/tool/filesystem` | v0.2.1 | v0.2.1 | No |
| `github.com/andrewhowdencom/ore/x/tool/skills` | v0.3.0 | v0.3.0 | No |
| `github.com/andrewhowdencom/ore/x/tool/sandbox/unsafe` | v0.1.0 | v0.1.0 | No |
| `github.com/andrewhowdencom/ore/x/conduit` (indirect) | v0.1.2 | v0.1.2 | No |

**Files that directly import ore packages** (and will be affected by API drift):
- `go.mod` — version pins
- `internal/app/app.go` — imports `ore/cognitive`, `ore/loop`, `ore/provider`, `ore/session`, `ore/thread`, `ore/tool`, `ore/x/conduit/http`, `ore/x/conduit/stdio`, `ore/x/conduit/tui`, `ore/x/guardrails`, `ore/x/provider/openai`, `ore/x/systemprompt`, `ore/x/systemprompt/source`, `ore/x/tool`, `ore/x/tool/bash`, `ore/x/tool/filesystem`, `ore/x/tool/skills`, `ore/x/tool/sandbox/unsafe`
- `internal/app/app_test.go` — imports `ore/artifact`, `ore/state`, `ore/thread`, `ore/x/systemprompt`, `ore/x/systemprompt/source`, `ore/x/tool/skills`
- `cmd/workshop/thread.go` — imports `ore/thread`
- `cmd/workshop/thread_test.go` — imports `ore/thread`
- `internal/app/roles.go` — imports `ore/tool`

**Key concern**: The user noted that newer ore versions may include "session / thread squashing" — a potential merge or refactor of the `session` and `thread` packages. Currently, `app.go` imports both packages separately and uses `session.NewManager` and `thread.NewJSONStore` / `thread.Thread`. Any consolidation would require refactoring these call sites.

**Secondary concern**: `ore/x/provider/openai` has a minor version bump (v0.2.1 → v0.3.0) which may include breaking changes to provider options or initialization signatures.

**go.mod artifact**: The file currently contains a malformed-looking indirect dependency `go.yaml.in/yaml/v3 v3.0.4`. This is likely an artifact from prior module resolution and may or may not be cleaned up by `go mod tidy`.

## Architectural Blueprint
This is a mechanical dependency upgrade with reactive fixes. There is no architectural redesign — the existing app wiring and conduit patterns remain the same. The only structural change will be adapting to any ore API renames or package merges discovered during the build phase.

Execution strategy:
1. Batch-bump all ore dependency versions in `go.mod` to latest.
2. Run `go mod tidy` to normalize the module graph.
3. Build the project (`go build ./...`) to surface all compilation errors from API drift.
4. Fix all compilation errors, prioritizing the session/thread squashing if present.
5. Run the full test suite (`go test ./...`) to surface test failures.
6. Fix all test failures.

## Requirements
1. Bump all out-of-date `andrewhowdencom/ore` dependencies to their latest published versions.
2. Run `go mod tidy` to clean up the module graph.
3. Fix all compilation errors arising from the upgrade.
4. If the ore library has merged `session` and `thread` packages, refactor all call sites to use the new consolidated API.
5. Fix any test failures caused by API changes or behavioral differences.
6. Ensure `go build ./...` and `go test ./...` both pass.

## Task Breakdown

### Task 1: Bump ore Dependency Versions
- **Goal**: Update `go.mod` to pin the latest ore versions, then run `go mod tidy` to normalize transitive dependencies.
- **Dependencies**: None.
- **Files Affected**: `go.mod`, `go.sum`.
- **New Files**: None.
- **Interfaces**: N/A.
- **Validation**: `go mod tidy` completes without error. This leaves the module graph in a valid state even if the code itself does not yet compile.
- **Details**:
  1. Edit `go.mod` to bump the following direct ore dependencies:
     - `github.com/andrewhowdencom/ore` → `v0.2.1`
     - `github.com/andrewhowdencom/ore/x/conduit/http` → `v0.3.1`
     - `github.com/andrewhowdencom/ore/x/conduit/stdio` → `v0.1.3`
     - `github.com/andrewhowdencom/ore/x/conduit/tui` → `v0.3.1`
     - `github.com/andrewhowdencom/ore/x/provider/openai` → `v0.3.0`
  2. Run `go mod tidy`.
  3. If `go mod tidy` reports issues with the malformed `go.yaml.in/yaml/v3` entry, investigate and clean it up manually if needed.

### Task 2: Fix All Compilation Errors
- **Goal**: Make the entire project compile after the ore upgrade, adapting to any breaking API changes including session/thread squashing.
- **Dependencies**: Task 1.
- **Files Affected**: `internal/app/app.go`, `internal/app/app_test.go`, `cmd/workshop/thread.go`, `cmd/workshop/thread_test.go`, `internal/app/roles.go` (potentially, if `ore/tool` signatures changed).
- **New Files**: None.
- **Interfaces**: Adapted ore API call sites. Exact changes will be discovered during build; likely candidates:
  - `session.NewManager(...)` → potentially moved to `thread.NewManager(...)` or renamed.
  - `session.Manager` type → potentially relocated.
  - `thread.NewJSONStore(...)` / `thread.NewMemoryStore(...)` / `thread.Store` / `thread.Thread` → potentially consolidated with session types.
  - `openai.New(...)` / `openai.WithAPIKey(...)` / `openai.WithModel(...)` / `openai.WithBaseURL(...)` / `openai.WithTemperature(...)` / `openai.WithReasoningEffort(...)` / `openai.WithTools(...)` → potentially renamed or restructured.
  - `tui.New(...)` / `tui.WithThreadID(...)` → potentially changed.
  - `httpc.New(...)` / `httpc.WithUI(...)` / `httpc.WithAddr(...)` → potentially changed.
  - `stdioc.New(...)` / `stdioc.WithThreadID(...)` → potentially changed.
- **Validation**: `go build ./...` passes with zero compilation errors.
- **Details**:
  1. Run `go build ./...` to surface all compilation errors.
  2. If the error output shows that `session` and `thread` packages have been squashed (e.g., "undefined: session.NewManager", "cannot find package github.com/andrewhowdencom/ore/session"), refactor all affected files:
     - Remove the obsolete import.
     - Update all type and function references to use the new consolidated package.
     - Adjust `app.go` `buildManager` function to use the new API.
     - Adjust `cmd/workshop/thread.go` and `cmd/workshop/thread_test.go` if `thread.Store` or `thread.Thread` types moved.
  3. Fix any other compilation errors from the `openai` provider bump or conduit bumps. Follow compiler diagnostics — rename symbols, update signatures, swap deprecated options.
  4. Re-run `go build ./...` iteratively until it passes.

### Task 3: Fix All Test Failures
- **Goal**: Make the full test suite pass after the compilation fixes.
- **Dependencies**: Task 2.
- **Files Affected**: `internal/app/app_test.go`, `cmd/workshop/thread_test.go`, `cmd/workshop/root_test.go`, `cmd/workshop/config_test.go`, `cmd/workshop/version_test.go`, `internal/app/roles_test.go` (tests may need updates if helper signatures or mock types changed).
- **New Files**: None.
- **Interfaces**: N/A.
- **Validation**: `go test ./...` passes with zero failures.
- **Details**:
  1. Run `go test ./...` to surface all test failures.
  2. For each failing test, determine whether the failure is due to:
     - A test helper signature mismatch (e.g., mock sandbox argument, thread creation pattern).
     - A behavioral change in the upgraded ore library (e.g., different default values, changed error messages).
     - A test that was testing an API that no longer exists.
  3. Fix each failure by adapting the test to the new API or updating expectations.
  4. Re-run `go test ./...` iteratively until all tests pass.

## Dependency Graph
- Task 1 → Task 2 (cannot fix compilation against versions that aren't in go.mod)
- Task 2 → Task 3 (cannot run tests against code that does not compile)

## Risks & Mitigations
| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| Session/thread squashing causes widespread refactoring across `app.go`, `thread.go`, and all tests | High | Medium | The builder agent will follow compiler errors systematically. If the change is too large, split into a sub-task for each affected package. |
| `ore/x/provider/openai` v0.3.0 introduces breaking changes to provider options or model configuration | Medium | Medium | Follow compiler errors; update `ProviderConfig` struct and `newProvider` function in `app.go` accordingly. |
| `go mod tidy` does not clean up the malformed `go.yaml.in/yaml/v3` indirect dependency | Low | Medium | Manually remove the line from `go.mod` if `go mod tidy` preserves it, then re-run `go mod tidy`. |
| Go module proxy delay for newly published ore versions | Low | Low | Not applicable — the versions (v0.2.1, v0.3.0, v0.3.1, v0.1.3) are already published and discoverable by `go list`. |

## Validation Criteria
- [ ] `go.mod` pins the five ore dependencies at their latest versions (v0.2.1, v0.3.1, v0.1.3, v0.3.1, v0.3.0).
- [ ] `go mod tidy` completes successfully and `go.sum` is updated.
- [ ] `go build ./...` passes with zero errors.
- [ ] `go test ./...` passes with zero failures.
- [ ] No `go.yaml.in/yaml/v3` malformed dependency remains in `go.mod` (or it is confirmed to be a legitimate indirect dependency).
- [ ] All ore imports in the codebase resolve to the upgraded versions (verify with `go list -m all | grep 'andrewhowdencom/ore'`).