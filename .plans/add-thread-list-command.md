# Plan: Add `workshop thread list` Subcommand

## Objective
Add a `workshop thread list` subcommand that lists persistent threads from the JSON store as a human-readable table, filtering to threads updated within the last N days (default 30), sorted by most-recent activity first. The command must not require an API key and should warn and exit non-zero when the store is in-memory.

## Context
- The CLI uses Cobra with subcommands defined in `cmd/workshop/*.go` (e.g. `version.go`, `config.go`, `http.go`).
- Each command is registered to `rootCmd` in its own `init()` function.
- `rootCmd` defines `PersistentPreRunE: configureLogging` which is inherited by all subcommands.
- Thread persistence is handled by `github.com/andrewhowdencom/ore/thread.JSONStore`, created when `--store.dir` is set.
- The `thread.Store` interface includes `List() ([]*thread.Thread, error)`.
- A `thread.Thread` has `ID`, `CreatedAt`, `UpdatedAt`, `Metadata map[string]string`.
- The active role is stored in metadata under key `workshop.role`.
- The project uses `go test -race ./...` for validation and `golangci-lint run ./...` for linting.

## Architectural Blueprint
Add a new `cmd/workshop/thread.go` file containing a `thread` parent command and a `list` child command. The `list` command reads `store.dir` from viper, initializes a `thread.JSONStore`, calls `List()`, filters by `UpdatedAt`, sorts by `UpdatedAt` descending, and prints a tabwriter-formatted table. The command does not require a provider or API key. To support testing, the core logic is extracted into a separate function following the pattern used in `config.go` (`runConfigInitWithPath`).

## Requirements
1. Add `workshop thread list` subcommand.
2. Add `--days` flag (default 30) to control the lookback period.
3. Display full UUIDs, created timestamp, updated timestamp, and role metadata.
4. Sort by `UpdatedAt` descending (most recent first).
5. Filter threads where `UpdatedAt` is within the lookback period.
6. Do not require provider configuration or API key.
7. If `store.dir` is empty (in-memory store), print a warning to stderr and exit with an error.
8. Leave the repository in a buildable, lint-clean, test-passing state after each task.

## Task Breakdown

### Task 1: Add Thread List Command Scaffold
- **Goal**: Add the `thread` parent and `list` child Cobra commands without implementation logic.
- **Dependencies**: None.
- **Files Affected**: `cmd/workshop/root.go` (add `threadCmd` via `rootCmd.AddCommand` in existing init pattern).
- **New Files**: `cmd/workshop/thread.go`.
- **Interfaces**: No new interfaces.
- **Validation**: `go build ./cmd/workshop` succeeds.
- **Details**:
  - Create `threadCmd` with `Use: "thread"` and add `threadListCmd` with `Use: "list"`.
  - Add `--days` int flag to `threadListCmd` with default `30`.
  - Register `threadCmd` under `rootCmd` in `init()`.
  - The `PersistentPreRunE: configureLogging` inherited from `rootCmd` is sufficient; do not override it.
  - Stub the `RunE` to return `nil` for now.

### Task 2: Implement List Logic, Filtering, Sorting, and Output
- **Goal**: Implement the full listing behavior with persistent store support.
- **Dependencies**: Task 1.
- **Files Affected**: `cmd/workshop/thread.go`.
- **New Files**: None.
- **Interfaces**: No new interfaces; uses existing `thread.Store` and `thread.JSONStore`.
- **Validation**: `go test -race ./...` passes, `golangci-lint run ./...` clean, `go build ./cmd/workshop` succeeds.
- **Details**:
  - In the `RunE` handler, read `store.dir` from viper.
  - If `store.dir` is empty, print a warning to stderr and return a non-nil error so the command exits non-zero.
  - Create a `thread.JSONStore` from the directory.
  - Call `store.List()`.
  - Compute the cutoff time as `time.Now().AddDate(0, 0, -days)`.
  - Filter threads where `UpdatedAt` is after the cutoff.
  - Sort the filtered slice by `UpdatedAt` descending (`sort.Slice`).
  - Extract the role from each thread via `thr.GetMetadata("workshop.role")`; default to empty string if missing.
  - Format output using `text/tabwriter` with columns: `ID`, `CREATED`, `UPDATED`, `ROLE`.
  - Use a concise date format (e.g. `2006-01-02 15:04`) in local time.
  - Extract a testable helper function `runThreadListWithStore` or similar, following the `config.go` pattern, so the core logic can be exercised with an injected store directory or `thread.Store`.

### Task 3: Add Tests for Thread List Command
- **Goal**: Validate filtering, sorting, formatting, and the in-memory-store warning path.
- **Dependencies**: Task 2.
- **Files Affected**: `cmd/workshop/thread.go` (if minor refactors are needed for testability).
- **New Files**: `cmd/workshop/thread_test.go`.
- **Interfaces**: No new interfaces.
- **Validation**: `go test -race ./cmd/workshop/...` passes.
- **Details**:
  - Test the in-memory store path: when `store.dir` is empty, verify the command returns an error.
  - Test the full happy path by creating a temporary directory, using `thread.NewJSONStore`, creating a few threads with `store.Create()`, setting metadata via `thr.SetMetadata("workshop.role", "...")`, saving them with `store.Save()`, and then verifying the list output includes the expected threads in the correct order.
  - Test that threads older than the `--days` cutoff are excluded.
  - Use the pipe-capture pattern from `version_test.go` to capture stdout and assert table content.
  - Test sort order explicitly.

## Dependency Graph
- Task 1 → Task 2
- Task 2 → Task 3

## Risks & Mitigations
| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| `thread.JSONStore` loads full thread state (including all messages) just to list metadata, causing poor performance for large thread histories. | Low | Low | Acceptable for local CLI use; note as future optimization if needed. |
| Tests creating real JSON store files may leave artifacts on disk if not cleaned up. | Low | Low | Always use `t.TempDir()` for store directories; Go automatically cleans up. |
| The `--days` flag name might conflict with future flags on a parent `thread` command. | Low | Low | Flag is scoped to `threadListCmd`, not the parent; safe. |

## Validation Criteria
- [ ] `workshop thread list --store.dir /path/to/store` prints a human-readable table of threads updated in the last 30 days.
- [ ] `workshop thread list --days 7 --store.dir /path/to/store` filters to the last 7 days.
- [ ] Running without `--store.dir` prints a warning and exits with a non-zero status.
- [ ] `go test -race ./...` passes after all tasks.
- [ ] `golangci-lint run ./...` is clean after all tasks.
- [ ] Full UUIDs are displayed so they can be copied to `--thread <id>`.
