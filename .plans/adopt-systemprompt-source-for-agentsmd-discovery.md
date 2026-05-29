# Plan: Adopt x/systemprompt/source for AGENTS.md Discovery

## Objective

Integrate the upstream `ore/x/systemprompt/source` package into workshop so that repository-level `AGENTS.md` and `CLAUDE.md` instruction files are automatically discovered and injected into the system prompt. This replaces the current purely manual prompt content acquisition with ergonomic factory functions that walk parent directories nearest-first, matching the behavior of Pi and other agentic tools.

## Context

Workshop currently wires prompt content manually in `internal/app/app.go` within the `buildManager` function. Two closures are passed to `systemprompt.WithContentFunc`:

- `makeCurrentPrompt(rdir, thr)` — dynamically reads the active role prompt from thread metadata, falling back to `defaultPrompt`.
- `makeWorkingDirContent(cfg.workingDir)` — emits a contextual sentence describing the current working directory (e.g., "You are running in: /project/subdir").

There is **no** automatic discovery of `AGENTS.md` or `CLAUDE.md` files anywhere in the codebase today.

The upstream `ore` framework has added `x/systemprompt/source` (branch `226`, commit `2e98869`) which provides:

- `source.AgentsMD(startDir) func() string` — walks parent directories from `startDir` toward the root, discovering `AGENTS.md` and `CLAUDE.md` at each level, concatenated nearest-first with `\n\n` separators. Returns empty string if none are found.
- `source.File(path) func() string` — reads a file on every call, returning empty string if missing.

Both closures are compatible with `systemprompt.WithContentFunc`.

**Relevant files discovered:**
- `internal/app/app.go` — core app wiring, `buildManager`, system prompt transform construction
- `internal/app/app_test.go` — tests for system prompt behavior, buildManager smoke tests
- `cmd/workshop/root.go` — TUI/stdio entrypoint; calls `os.Getwd()` and passes via `app.WithWorkingDir(cwd)`
- `cmd/workshop/http.go` — HTTP entrypoint; same `os.Getwd()` pattern
- `go.mod` — core `ore` dependency updated to `v0.2.0` which includes `x/systemprompt/source`

## Architectural Blueprint

**Selected approach (Path A): Additive integration.**

Keep the existing `makeCurrentPrompt` (role-based dynamic prompt) and `makeWorkingDirContent` (working directory path context) closures. Add `source.AgentsMD(cfg.workingDir)` as an **additional** `WithContentFunc` in the `systemprompt.New` call inside `buildManager`.

**Rationale for Path A over alternatives:**
- **Path B (replace working dir content with AgentsMD)** would lose the explicit "You are running in: X" sentence, which is complementary context (directory path vs. instruction file content).
- **Path C (simplify working dir content)** is unnecessary — `makeWorkingDirContent` is already a minimal one-line closure.
- `source.AgentsMD` is non-breaking: it returns an empty string when no files are found, so existing behavior is preserved for repositories without instruction files.

The `cfg.workingDir` field is already populated from `os.Getwd()` in both `cmd/workshop/root.go` and `cmd/workshop/http.go`, so no CLI changes are required.

`source.File` is noted as potentially useful for future role-to-file mappings but is not needed for the current role-based dynamic prompt system (`makeCurrentPrompt` handles per-thread role switching).

## Requirements

1. Workshop imports `github.com/andrewhowdencom/ore/x/systemprompt/source`.
2. `source.AgentsMD` is wired into the system prompt transform in `buildManager`, using `cfg.workingDir` as the start directory.
3. Manual `AGENTS.md`/`CLAUDE.md` reading closures are removed or simplified [inferred: currently none exist; criterion is vacuously satisfied by adding automatic discovery].
4. `makeWorkingDirContent` is preserved to retain working-directory path context.
5. `go test ./...` passes after the change.

## Task Breakdown

### Task 1: Update ore Dependency to Version Including x/systemprompt/source
- **Goal**: Upgrade the core `github.com/andrewhowdencom/ore` dependency to a version that contains `x/systemprompt/source`.
- **Dependencies**: None.
- **Files Affected**: `go.mod`, `go.sum`
- **New Files**: None.
- **Interfaces**: None.
- **Validation**:
  - `go mod download` succeeds.
  - `go build ./...` compiles without errors.
- **Details**: The `x/systemprompt/source` package is available in `ore` v0.2.0.
  Update the core `ore` module to `v0.2.0`, then run `go mod tidy` to refresh
  `go.sum`. Submodules may also need updating if they reference incompatible
  core versions — use `go get -u ./...` and verify with `go build ./...`.

### Task 2: Import and Wire source.AgentsMD into System Prompt Transform
- **Goal**: Add the `source` import and wire `source.AgentsMD(cfg.workingDir)` into the system prompt transform in `buildManager`.
- **Dependencies**: Task 1.
- **Files Affected**: `internal/app/app.go`
- **New Files**: None.
- **Interfaces**: None.
- **Validation**:
  - `go test ./...` passes.
  - `go vet ./...` is clean.
- **Details**: In `internal/app/app.go`, inside `buildManager`, add:
  1. An import alias: `github.com/andrewhowdencom/ore/x/systemprompt/source`
  2. A third `WithContentFunc` call in `systemprompt.New`:
     ```go
     sp, err := systemprompt.New(
         systemprompt.WithContentFunc(currentPrompt),
         systemprompt.WithContentFunc(makeWorkingDirContent(cfg.workingDir)),
         systemprompt.WithContentFunc(source.AgentsMD(cfg.workingDir)),
     )
     ```
  Do **not** remove `makeCurrentPrompt` or `makeWorkingDirContent`; they serve distinct purposes (role selection and directory path context).

### Task 3: Add Tests for AgentsMD Discovery in System Prompt
- **Goal**: Extend test coverage to verify that `source.AgentsMD`-driven content appears in the system prompt when `AGENTS.md`/`CLAUDE.md` files exist in the working directory tree.
- **Dependencies**: Task 2.
- **Files Affected**: `internal/app/app_test.go`
- **New Files**: None.
- **Interfaces**: None.
- **Validation**:
  - `go test ./...` passes.
- **Details**: Add one or more test cases that:
  1. Create a temporary directory hierarchy with `AGENTS.md` and/or `CLAUDE.md` files.
  2. Pass the temporary directory as `cfg.workingDir`.
  3. Call `buildManager` (or directly test `source.AgentsMD` behavior if unit-testing the source closure).
  4. Verify the resulting system prompt contains the concatenated file contents in nearest-first order.
  Also verify that when no instruction files exist, the prompt does not contain unexpected content and the system prompt transform still succeeds.

## Dependency Graph

- Task 1 → Task 2 (Task 2 depends on the updated ore dependency)
- Task 2 → Task 3 (Task 3 tests the new wiring)

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| Submodule drift from core ore update | **Medium** — other `ore/x/*` submodules may reference an incompatible core version | **Medium** | After updating core `ore`, run `go mod tidy` and `go get -u ./...` to ensure all `ore` submodules resolve consistently. Verify with `go build ./...`. |
| Tests fail due to unexpected source.AgentsMD content | **Low** — existing tests use direct `systemprompt.New` construction, not `buildManager` | **Low** | `TestSystemPrompt_WithCWD` and `TestSystemPrompt_WithoutCWD` manually construct the transform; they are unaffected by `buildManager` changes. Smoke tests (`TestBuildManager_*`) only verify manager creation, not prompt content. |
| Working directory is empty (`os.Getwd()` fails) | **Low** — `source.AgentsMD("")` will walk from current directory; `makeWorkingDirContent("")` already handles this by returning empty string | **Low** | `source.AgentsMD` handles empty startDir gracefully (will use `.` effectively). No code change needed. |

## Validation Criteria

- [x] `go.mod` references a version of `github.com/andrewhowdencom/ore` that includes `x/systemprompt/source`.
- [x] `internal/app/app.go` imports `github.com/andrewhowdencom/ore/x/systemprompt/source`.
- [x] `buildManager` passes `source.AgentsMD(cfg.workingDir)` as a `systemprompt.WithContentFunc` alongside existing closures.
- [x] `makeCurrentPrompt` and `makeWorkingDirContent` are preserved (not removed).
- [x] `go test ./...` passes.
- [x] `go vet ./...` is clean.
- [x] `go build ./...` succeeds for all commands (`workshop`, `workshop http`).
