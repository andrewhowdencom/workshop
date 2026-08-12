# Plan: Bump Everything to Latest

## Objective
Bring every direct dependency in `go.mod` to its latest published version, fix any compile and test breakage introduced by API drift, and replace the stale `.plans/upgrade-ore-dependencies.md` (which describes a v0.2.x-era migration that has since shipped) with a plan that describes the actual ore v1.x migration. End state: `task validate` (lint + test + build) clean.

## Context
**The break**

Workshop fails to compile with two errors at `internal/app/app.go:1167`:

- `spec.CacheControl undefined (type models.Spec has no field or method CacheControl)`
- `undefined: models.CacheControl`

Investigation showed that `github.com/andrewhowdencom/ore/models.Spec` gained a `CacheControl *CacheControl` field in ore **v1.3.0** (verified at `/home/andrewhowdencom/go/pkg/mod/github.com/andrewhowdencom/ore@v1.3.0/models/spec.go:102`). The module is currently pinned to **v1.2.3**. The wire layer in `github.com/andrewhowdencom/ore/x/wire/anthropic@v0.2.3` already implements `applySpecCacheControl` (verified at `.../anthropic.go:243`) which reads `spec.CacheControl` and stamps Anthropic-style `cache_control` blocks. So once ore is at v1.3.0 and `x/wire/anthropic` is at v0.2.3, the call site at `app.go:1167` compiles unchanged.

**The commit trail**

The commit that introduced the breakage is `a761379 Wire cache-control through workshop`, which was authored against the ore v1.3.0 API but landed without bumping the dependency. The plan file (`.plans/upgrade-ore-dependencies.md`) was last touched under ore `v0.2.x` and references a "session/thread squashing" risk that the git history shows already shipped (`35252a4 Adopt ore v0.13.1 (session→junk rename)`).

**Project conventions** (from `go` skill): `Effective Go`, functional options, table-driven tests, `log/slog`, `go test -race`, `golangci-lint`. (`architecture` skill): hexagonal architecture, functional options for optional configuration. None of these are violated by a pure dep bump.

## Architectural Blueprint
Mechanical dependency upgrade with reactive source-code fixes. No architectural redesign. The app wiring and conduit patterns remain unchanged.

**Selected approach** (single PR/branch, not several): bump every direct dep in one shot, `go mod tidy`, then chase the resulting compile/test failures to green, then rewrite the plan file.

**Tree-of-Thought deliberation:**

- *Path A — surgical ore-only bump*: minimum-risk, fixes the known break. Rejected: user explicitly asked for "latest everything".
- *Path B — surgical now, batch later*: do A now, schedule non-ore bumps as a follow-up. Rejected: user wants everything in one go.
- *Path C — bump everything in one shot* (selected): single coherent change, easy to bisect via `git bisect` if a regression appears later, one PR to review.

## Responsibility Boundaries

| Package | Stated Responsibility (from godoc or file role) | Plan's Relationship |
|---|---|---|
| `github.com/andrewhowdencom/ore/models` | "Package models defines the ModelSpec value type… and the supporting ThinkingLevel type." | `Extends [consume the new Spec.CacheControl field added in ore v1.3.0]` |
| `github.com/andrewhowdencom/ore/x/wire/anthropic` | Anthropic wire adapter — translates `models.Spec` to Anthropic's wire format and applies `applySpecCacheControl` when `spec.CacheControl != nil`. | `Respects` |
| `internal/app/app.go` | Workshop's wire layer (private package; not externally documented) | `Respects` |
| `.plans/upgrade-ore-dependencies.md` | Plan document (not a Go package) | `Rewrites` (deletes; replaces with this plan) |

(Note: the full per-package table will be filled in by the builder as actual API drift surfaces during Tasks 1–3. The above are the packages verifiable at ideation time. Builder must add any new rows for packages where API drift actually changes the call site.)

## Requirements
1. Bump every direct dep in `go.mod` to its latest published version.
2. Run `go mod tidy` to normalize the module graph.
3. Resolve any compile errors caused by API drift in ore, cobra, viper, otel, charm, and other deps. (`app.go:1167` should compile unchanged once ore is on v1.3.0.)
4. Resolve any test failures caused by dep bumps.
5. Replace `.plans/upgrade-ore-dependencies.md` with a plan that describes the actual migration (not the stale v0.2.x story).
6. `task validate` (lint + test + build) passes.
7. **Out of scope:** new features, behavioral changes, API redesign, dropping Go version support.

## Task Breakdown

### Task 1: Bump every direct dep to latest and normalize the module graph
- **Goal**: `go.mod` direct deps all at latest; `go.sum` reconciled by `go mod tidy`.
- **Dependencies**: None.
- **Files Affected**: `go.mod`, `go.sum`.
- **New Files**: None.
- **Interfaces**: N/A (deps only).
- **Validation**: `go mod verify` passes; `go list -m all | grep '<module>'` shows the latest version of each previously-direct dep. (Build may still fail at this point — that's expected; Task 2 fixes it.)
- **Details**:
  1. Run `go list -m -u all` to enumerate outdated direct deps.
  2. For each direct dep, set the version to the latest published tag. Known ore versions to bump: `ore v1.2.3 → v1.3.0`, `x/wire/anthropic v0.2.2 → v0.2.3`, `x/provider/anthropic v0.2.5 → v0.2.6`, `x/conduit/http v0.8.2 → v0.9.0`, `x/conduit/tui v0.12.9 → v0.12.10`. Other direct deps: resolve via `go list -m -versions`.
  3. Run `go mod tidy`. Accept whatever indirect dep changes it makes (including any `add`/`remove` of indirect entries).
  4. Commit `go.mod` and `go.sum` together so the diff is reviewable.

### Task 2: Restore compile-clean state
- **Goal**: `go build ./...` succeeds against the bumped module graph.
- **Dependencies**: Task 1.
- **Files Affected**: Likely `internal/app/app.go`, possibly `cmd/workshop/*.go`, `internal/app/roles.go`, `internal/app/app_test.go`. Actual scope determined by the compiler.
- **New Files**: None.
- **Interfaces**: Adapted call sites for any ore / non-ore API drift.
- **Validation**: `go build ./...` clean; `go vet ./...` clean.
- **Details**:
  1. Run `go build ./...`.
  2. For each compile error, follow the diagnostic: rename symbols, update signatures, swap deprecated options.
  3. The known error at `app.go:1167` (`models.Spec.CacheControl` undefined) should now resolve because ore is at v1.3.0; no source change should be needed at that line. If it still fails, surface to the user — the wire/anthropic version may also need bumping.
  4. Re-run iteratively until clean.

### Task 3: Restore test-clean state
- **Goal**: `go test -race ./...` succeeds against the bumped module graph.
- **Dependencies**: Task 2.
- **Files Affected**: Tests may need expectation updates if upstream behavior changed (default values, error message formats, helper signatures).
- **New Files**: None.
- **Interfaces**: N/A.
- **Validation**: `go test -race ./...` passes with zero failures.
- **Details**:
  1. Run `go test -race ./...`.
  2. For each failure, classify: (a) test helper signature mismatch, (b) upstream behavior change, (c) test for an API that no longer exists.
  3. Fix accordingly. Do not weaken assertions to make tests pass; if behavior genuinely changed, update the expectation and add a brief comment explaining the cause.

### Task 4: Rewrite the stale plan file
- **Goal**: Replace `.plans/upgrade-ore-dependencies.md` with content that reflects the actual ore v1.x migration.
- **Dependencies**: Task 3.
- **Files Affected**: `.plans/upgrade-ore-dependencies.md` (overwritten in place); `.plans/bump-everything-to-latest.md` (created, this plan).
- **New Files**: `.plans/bump-everything-to-latest.md`.
- **Interfaces**: N/A.
- **Validation**: The rewritten file describes ore v1.x reality, references the actual cache_control migration cause, lists the actual versions landed (filled in during execution), and matches the original file's structure (Objective, Context, Blueprint, Requirements, Task Breakdown, Dependency Graph, Risks, Validation).
- **Details**:
  1. Preserve the structure of the original file.
  2. Update the version table to the actual versions landed (use `go list -m all` output).
  3. Drop the "session/thread squashing" risk — that shipped long ago.
  4. Update the "files that import ore packages" list to the current actual state.
  5. Rewrite the task list as a record of what was done (not what should be done).

### Task 5: Final validation pass
- **Goal**: `task validate` passes end-to-end.
- **Dependencies**: Task 4.
- **Files Affected**: Possibly source files if lint surfaces new issues; possibly go.mod if `go mod verify` flags something.
- **New Files**: None.
- **Interfaces**: N/A.
- **Validation**: `task validate` clean.
- **Details**:
  1. Run `task validate`.
  2. Address any lint issues (most likely stylistic; unlikely to need architectural changes).
  3. Run `go mod verify` one last time.
  4. Confirm `go.yaml.in/yaml/v3` artifact (carried over from prior plans) is either cleaned up or confirmed to be a legitimate indirect dep.

## Dependency Graph
- Task 1 → Task 2 (Task 2 cannot fix compile errors against the old module graph)
- Task 2 → Task 3 (Task 3 cannot run tests against code that doesn't compile)
- Task 3 → Task 4 (Task 4's content references the actual outcome of Tasks 1–3)
- Task 4 → Task 5 (Task 5 is final)

## Risks & Mitigations
| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| Non-ore bumps (cobra, viper, otel, charm, etc.) introduce unrelated API drift | Medium | Medium | Tasks 2 and 3 are reactive: follow compiler/test diagnostics. If a non-ore bump is the sole source of breakage, isolate by reverting that single bump and re-running. |
| `go mod tidy` adds/removes indirect deps unexpectedly | Low | Low | Accept the change; commit go.mod and go.sum together so the diff is reviewable. |
| Local Go toolchain is older than `go 1.26.2` from go.mod, surfacing unrelated build errors | Medium | Low | Builder checks `go version` before debugging; if mismatch is the root cause, surface to user. |
| Lint surfaces issues unrelated to the bump | Low | Medium | Address in Task 5; do not let lint churn block the migration. |
| Plan file rewrite produces a misleading "as-built" record | Low | Medium | Builder copies in actual `go list -m all` output and actual files changed, rather than rewriting generically. |
| `go.yaml.in/yaml/v3` malformed indirect persists | Low | Low | Task 5 includes `go mod verify`; if unjustified, remove from go.mod. |
| The cache_control line at `app.go:1167` still doesn't compile after Task 1, because the wire/anthropic version also needs bumping or the field is gated by another module | Medium | Low | The pre-bump investigation showed both `ore v1.3.0` and `x/wire/anthropic v0.2.3` are required; Task 1 bumps both. If still failing, surface. |

## Validation Criteria
- [ ] `go.mod` direct deps all at latest published versions.
- [ ] `go mod tidy` completes without error.
- [ ] `go build ./...` passes with zero errors.
- [ ] `go vet ./...` clean.
- [ ] `go test -race ./...` passes with zero failures.
- [ ] `task validate` clean (lint + test + build).
- [ ] `.plans/upgrade-ore-dependencies.md` rewritten to describe the actual ore v1.x migration (or removed, per filename decision).
- [ ] `.plans/bump-everything-to-latest.md` exists and matches the actual work done.
- [ ] No `go.yaml.in/yaml/v3` malformed artifact remains, or it's confirmed legitimate.