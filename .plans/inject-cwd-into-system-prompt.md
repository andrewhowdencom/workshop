# Plan: Inject CWD into System Prompt

## Objective
Modify the workshop application so the system prompt communicates the current working directory to the AI, giving it spatial context for filesystem operations and enabling proactive codebase exploration.

## Context
The application (`internal/app/app.go`) generates a `defaultPrompt` string baked into the binary. This prompt describes the AI as a terminal-based coding assistant with filesystem and bash tools, but it never tells the AI *where* it is running. As a result the AI lacks spatial context — it does not know that `./` refers to the user's active project directory, and the existing guardrail "Before writing or editing files, verify the target path and confirm the change is intended" creates friction because the AI has no anchor for what "intended" means.

Key files discovered:
- `internal/app/app.go` — contains `defaultPrompt` and `makeCurrentPrompt(rdir, thr)` which builds the dynamic system prompt.
- `internal/app/app_test.go` — tests `makeCurrentPrompt` fallback and role-based prompts.
- `cmd/workshop/root.go` — CLI entry point for TUI; calls `app.RunTUI(...)`.
- `cmd/workshop/http.go` — CLI entry point for HTTP server; calls `app.RunHTTP(...)`.

## Architectural Blueprint
Use the existing `ore/x/systemprompt` multi-fragment support to inject the current working directory as a **separate content function**, keeping the `defaultPrompt` static and clean. Specifically:

1. Add `workingDir` to the internal `config` struct and expose `WithWorkingDir` option.
2. In `buildManager`, when constructing the `systemprompt.Transform`, add a second `WithContentFunc` that returns the CWD sentence (or empty string if unavailable).
3. Update the guardrails text to reference the CWD.

This avoids mutating the baked-in `defaultPrompt` or role prompts directly, and leverages the library's existing fragment-concatenation behavior.

No Tree-of-Thought deliberation was required: using `systemprompt.WithContentFunc` is strictly superior to string concatenation because it keeps the base prompt declarative and the runtime context additive.

## Requirements
1. The AI must know the current working directory when the application starts.
2. The working directory must appear in the system prompt for both default and role-based prompts.
3. The guardrail wording should be revised to reference the cwd (e.g., "Before writing outside `<cwd>`, verify the target path...").
4. The change must be testable and backward-compatible (role prompts without cwd still work, they just get enhanced).

## Task Breakdown

### Task 1: Add `WithWorkingDir` Option
- **Goal**: Extend the internal `config` struct and functional options in `app.go` to accept a working directory.
- **Dependencies**: None.
- **Files Affected**: `internal/app/app.go`
- **New Files**: None.
- **Interfaces**: New option `func WithWorkingDir(dir string) Option`.
- **Validation**: `go test ./internal/app/...` passes.
- **Details**: Add `workingDir string` to the `config` struct. Add `WithWorkingDir` option that sets it. No prompt changes yet.

### Task 2: Add CWD Content Function to System Prompt Transform
- **Goal**: Inject the working directory as a separate content fragment in the system prompt transform.
- **Dependencies**: Task 1.
- **Files Affected**: `internal/app/app.go`, `internal/app/app_test.go`
- **New Files**: None.
- **Interfaces**: `buildManager` gains a `workingDir`-aware second `systemprompt.WithContentFunc` call.
- **Validation**: `go test ./internal/app/...` passes with updated assertions.
- **Details**:
  - In `buildManager`, after the existing `systemprompt.WithContentFunc(currentPrompt)`, add a second `WithContentFunc` that returns the cwd context (e.g., `You are running in: <cwd>. This is the user's active project directory; explore it proactively.`).
  - Update the guardrail text to: `Before writing or editing files outside the current working directory, verify the target path and confirm the change is intended.`
  - Update tests to verify that `buildManager` succeeds and produces a prompt containing the cwd when configured.

### Task 3: Wire CWD from CLI Entry Points
- **Goal**: Capture `os.Getwd()` in `root.go` and `http.go` and pass it to the app options.
- **Dependencies**: Task 1.
- **Files Affected**: `cmd/workshop/root.go`, `cmd/workshop/http.go`
- **New Files**: None.
- **Interfaces**: None.
- **Validation**: `go test ./cmd/workshop/...` passes and `go build ./...` succeeds.
- **Details**: In both `runRoot` and `runHTTP`, call `os.Getwd()`, handle errors gracefully (on error, pass `""` and let the prompt omit the cwd sentence), and pass `app.WithWorkingDir(cwd)` to `app.RunTUI` / `app.RunHTTP`.

### Task 4: Create GitHub Issue
- **Goal**: Track this feature in the upstream issue tracker instead of versioning the plan via git commit.
- **Dependencies**: None (can run in parallel with Tasks 1–3, but logically after the plan is finalized).
- **Files Affected**: None.
- **New Files**: None.
- **Interfaces**: None.
- **Validation**: `gh issue view <number>` confirms the issue was created with title "Inject CWD into system prompt" and body summarizing the objective.
- **Details**: Run `gh issue create --title "Inject CWD into system prompt" --body "..."` in the repository root. The body should reference this plan file and summarize the problem, tasks, and acceptance criteria.

## Dependency Graph
- Task 1 → Task 2
- Task 1 → Task 3
- Task 2 || Task 3 (parallelizable after Task 1)
- Task 4 is independent

## Risks & Mitigations
| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| `os.Getwd()` fails in sandboxed/containerized environments | Medium | Low | Pass empty string and gracefully omit cwd from prompt; guardrail reverts to original behavior. |
| Role prompts become too long with appended cwd context | Low | Low | Keep cwd sentence concise (~30 words); measure prompt length in tests if needed. |
| Test flakiness due to varying cwd in CI | Medium | Medium | Tests should mock cwd via `WithWorkingDir` option rather than relying on actual `os.Getwd()`. |

## Validation Criteria
- [ ] `go test ./...` passes after all tasks.
- [ ] `go build ./...` produces a working binary.
- [ ] Unit tests assert that the generated prompt contains the injected working directory string.
- [ ] A GitHub issue exists tracking this feature request.
