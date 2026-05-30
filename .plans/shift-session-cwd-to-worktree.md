# Plan: Shift Session CWD to Worktree

## Objective

Make `workspace_create` shift the implicit session working directory to the newly created git worktree, so that all subsequent relative-path tool calls (`read_file`, `write_file`, `edit_file`, `search_files`, `bash`) resolve against the worktree directory. Make `git_commit` commit into the active worktree branch by default. On `workspace_destroy`, snap the session context back to the original repository root. Block nested worktree creation.

## Context

The codebase is a Go project (`github.com/andrewhowdencom/workshop`) built on the `ore` framework. Tool registration and handlers live in `internal/app/app.go`.

### Ore framework change (already merged)

The `ore` issue [#277](https://github.com/andrewhowdencom/ore/issues/277) has been merged. The `resolvePath` helper in `ore/x/tool/filesystem` no longer hardcodes rejection of absolute paths. All paths (relative and absolute) are now delegated to `FileSandbox.ResolvePath`, allowing sandbox implementations to decide whether to pass absolute paths through unchanged.

### Current workshop behavior

- `workspace_create` creates a git worktree under `.worktrees/<branch>` and stores its path in thread metadata key `workshop.worktree.path`.
- `workspace_destroy` removes the worktree and clears the metadata key.
- `git_commit` runs `git commit` with no explicit `cmd.Dir`, committing to the process CWD (the original repo root).
- Filesystem and bash tools are registered directly from the `ore` framework and use the unsafe sandbox, which does not implement `FileSandbox`, so all paths pass through unchanged against the process CWD.
- Workshop's `go.mod` currently pins `ore/x/tool/filesystem` at `v0.2.1` (before the merged fix).

## Architectural Blueprint

**Approach: Custom `FileSandbox` + `ExecSandbox` in workshop, backed by the merged ore fix.**

Create a `workshopSandbox` that implements `FileSandbox` (and optionally `ExecSandbox`) and registers it as the default sandbox in `buildManager`. The sandbox reads `workshop.worktree.path` from thread metadata:
- `ResolvePath(path)`: absolute paths pass through unchanged; relative paths are joined with the active worktree path.
- `WorkingDirectory()`: returns the active worktree path (or empty string if none).

This replaces the unsafe sandbox. Filesystem tools automatically resolve relative paths against the worktree. The bash tool uses `WorkingDirectory()` as its default working directory. `git_commit` reads `WorkingDirectory()` from the sandbox for `cmd.Dir`.

**Why not wrap tool functions?** Wrapping every ore tool at registration is verbose and fragile. The ore sandbox abstraction is the intended mechanism for this; the merged fix makes it usable for our use case.

**Why not process CWD mutation?** Mutating `os.Chdir` is global, racy, and breaks concurrent sessions (HTTP handler).

## Requirements

1. `workspace_create` sets the logical session CWD to the new worktree by storing its path in thread metadata.
2. Relative paths in `read_file`, `write_file`, `edit_file`, `search_files` resolve from the active worktree.
3. `bash` commands run in the active worktree by default (when `working_directory` is not explicitly provided).
4. `git_commit` creates the commit in the active worktree branch.
5. `workspace_destroy` reverts the session context to the original repository root (by clearing metadata).
6. Calling `workspace_create` while already inside a worktree context returns an error.
7. Absolute paths remain unaffected (pass through unchanged).
8. [inferred] If the process restarts and metadata is lost, the context reverts to the original directory.

## Task Breakdown

### Task 1: Update ore dependencies to fetch the merged fix
- **Goal**: Pull the merged ore fix into workshop by updating `go.mod` dependencies.
- **Dependencies**: None (the ore change is already merged).
- **Files Affected**: `go.mod`, `go.sum`
- **New Files**: None.
- **Validation**: `go test ./...` passes after `go mod tidy` (no code changes yet).
- **Details**:
  - The merged fix lives in `ore/x/tool/filesystem`. The current workshop `go.mod` pins this module at `v0.2.1`.
  - Run `go get github.com/andrewhowdencom/ore/x/tool/filesystem@latest` to pull the version containing the merged fix (this will resolve to a new tag like `v0.2.2` or a pseudo-version).
  - If additional ore modules have newer tags, update them too for consistency (e.g. `go get -u github.com/andrewhowdencom/ore/...`).
  - Run `go mod tidy` to clean up `go.sum`.
  - Verify `go test ./...` compiles and existing tests pass.

### Task 2: Create workshop sandbox and update handlers
- **Goal**: Implement `workshopSandbox`, replace the unsafe sandbox, make `git_commit` worktree-aware, and block nested `workspace_create`.
- **Dependencies**: Task 1 (ore fix must be available in `go.mod` so the sandbox compiles against the updated `resolvePath` behavior).
- **Files Affected**: `internal/app/app.go`
- **New Files**: None.
- **Validation**: `go test ./...` passes (existing tests still pass because the new sandbox is transparent when no worktree is active).
- **Details**:
  - Remove the `unsandbox` import alias (`github.com/andrewhowdencom/ore/x/tool/sandbox/unsafe`).
  - Define `workshopSandbox` implementing `tool.FileSandbox` (and optionally `tool.ExecSandbox`):
    ```go
    type workshopSandbox struct {
        name string
        thr  *session.Thread
    }

    func (s *workshopSandbox) Name() string { return s.name }

    func (s *workshopSandbox) ResolvePath(path string) (string, error) {
        if filepath.IsAbs(path) {
            return path, nil
        }
        if wtPath, ok := s.thr.GetMetadata("workshop.worktree.path"); ok && wtPath != "" {
            return filepath.Join(wtPath, path), nil
        }
        return path, nil
    }

    func (s *workshopSandbox) WorkingDirectory() string {
        if wtPath, ok := s.thr.GetMetadata("workshop.worktree.path"); ok && wtPath != "" {
            return wtPath
        }
        return ""
    }
    ```
  - Optionally implement `ExecSandbox` if we want all subprocess execution (including custom tools) to go through a single abstraction. This delegates to `exec.CommandContext` with `cmd.Dir` set to the worktree. If omitted, bash falls through to raw `exec.CommandContext` using `FileSandbox.WorkingDirectory()` for its default cwd, which is sufficient.
  - In `buildManager`, replace:
    ```go
    sbr.SetDefaultSandbox(unsandbox.New("default"))
    ```
    with:
    ```go
    sbr.SetDefaultSandbox(&workshopSandbox{name: "workshop", thr: thr})
    ```
  - Modify `makeGitCommitHandler` to accept `thr *session.Thread` and set `cmd.Dir` to the sandbox's working directory:
    ```go
    func makeGitCommitHandler(thr *session.Thread, pc ProviderConfig) tool.ToolFunc {
        return func(ctx context.Context, sb tool.Sandbox, args map[string]any) (any, error) {
            // ... existing validation and message building ...

            cmd := exec.CommandContext(ctx, "git", "commit", "-m", msg)
            if fsb, ok := sb.(tool.FileSandbox); ok {
                if dir := fsb.WorkingDirectory(); dir != "" {
                    cmd.Dir = dir
                }
            }
            // ... rest of execution ...
        }
    }
    ```
    Update the registration call in `buildManager` to pass `thr`:
    ```go
    mustRegisterRaw(registry, "git_commit", "...", gitCommitSchema, makeGitCommitHandler(thr, cfg.provider))
    ```
  - In `makeWorkspaceCreateHandler`, add a guard at the top:
    ```go
    if existingPath, ok := thr.GetMetadata("workshop.worktree.path"); ok && existingPath != "" {
        return nil, fmt.Errorf("already inside worktree %q; nested worktrees are not allowed", existingPath)
    }
    ```

### Task 3: Add tests for worktree-aware behavior
- **Goal**: Add comprehensive tests covering all acceptance criteria.
- **Dependencies**: Task 2.
- **Files Affected**: `internal/app/app_test.go`
- **New Files**: None.
- **Validation**: `go test -race ./...` passes.
- **Details**: Add the following tests, following existing patterns (use `session.NewMemoryStore()`, `t.TempDir()`, real git and file I/O where needed):
  - `TestWorkshopSandbox_ResolvePath_RelativeInWorktree` — verify relative paths are joined with worktree
  - `TestWorkshopSandbox_ResolvePath_AbsoluteUnchanged` — verify absolute paths pass through unchanged
  - `TestWorkshopSandbox_ResolvePath_NoWorktree` — verify relative paths pass through unchanged when no worktree is active
  - `TestWorkshopSandbox_WorkingDirectory_WithWorktree` — verify returns worktree path
  - `TestWorkshopSandbox_WorkingDirectory_WithoutWorktree` — verify returns empty string
  - `TestReadFile_ResolvesRelativePathInWorktree` — integration test: create `workshopSandbox` with active worktree metadata, call `filesystem.ReadFile` through sandbox with relative path, verify file is read from worktree
  - `TestReadFile_AbsolutePathUnchangedInWorktree` — integration test: active worktree metadata, call with absolute path to file outside worktree, verify it reads the outside file
  - `TestWriteFile_ResolvesRelativePathInWorktree` — integration test: verify `filesystem.WriteFile` creates file inside worktree when sandbox is active
  - `TestListDirectory_ResolvesRelativePathInWorktree` — integration test: verify `filesystem.ListDirectory` lists worktree contents
  - `TestSearchFiles_ResolvesRelativePathInWorktree` — integration test: verify `filesystem.SearchFiles` searches within worktree
  - `TestBash_DefaultsToWorktreeDirectory` — integration test: call `bash.Bash` with no `working_directory` through an active `workshopSandbox`, verify `pwd` output contains worktree path
  - `TestBash_ExplicitWorkingDirectoryRespected` — verify explicit `working_directory` in args is not overridden
  - `TestGitCommitHandler_WorktreeAware` — integration test with real git repo: create repo, create worktree, stage change in worktree, call `makeGitCommitHandler`, verify commit is on worktree branch
  - `TestWorkspaceCreateHandler_NestedRejection` — set `workshop.worktree.path` metadata, call `makeWorkspaceCreateHandler`, assert error contains "already inside worktree"
  - `TestWorkspaceDestroy_RevertsContext` — create worktree, destroy it, verify subsequent `ResolvePath` returns relative paths unchanged

## Dependency Graph

- Task 1 → Task 2 (must fetch ore fix before workshop sandbox compiles against updated behavior)
- Task 2 → Task 3 (tests exercise the new sandbox and handlers)

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| Submodule tag not yet published for `ore/x/tool/filesystem` | Medium | Medium | If no tag exists, use a pseudo-version (`go get ...@<commit-hash>`) or request a tag from the ore maintainer. |
| Other ore modules drift and introduce breaking changes when updated | Medium | Low | Update only `ore/x/tool/filesystem` first. If `go mod tidy` pulls transitive updates, verify with `go test ./...` before proceeding. |
| `workshopSandbox` captures `thr *session.Thread` pointer; parallel tool calls in same turn may race on metadata | Low | Low | Thread metadata access in ore is designed for concurrent tool execution. The `workspace_create` → file operation pattern is typically across turns, not within a single turn. |
| `git_commit` is the only custom tool with implicit CWD dependency not covered by sandbox abstractions | Low | Low | Explicitly wired via `FileSandbox.WorkingDirectory()`. Document in code comments. Future custom tools with similar implicit deps must follow the same pattern. |
| `ExecSandbox` not implemented; custom tools spawning subprocesses bypass sandbox | Low | Low | The only custom subprocess tool is `git_commit`, which is explicitly wired. If more are added, implement `ExecSandbox` on `workshopSandbox`. |

## Validation Criteria

- [ ] `go test -race ./...` passes after all tasks are complete.
- [ ] `workspace_create` rejects nested calls when `workshop.worktree.path` metadata is already set.
- [ ] `read_file` with a relative path resolves against the active worktree when `workshopSandbox` is the default sandbox.
- [ ] `write_file` with a relative path creates files inside the active worktree.
- [ ] `edit_file` with a relative path edits files inside the active worktree.
- [ ] `search_files` with a relative path searches inside the active worktree.
- [ ] `bash` without explicit `working_directory` executes commands in the active worktree.
- [ ] `git_commit` creates the commit in the active worktree branch.
- [ ] Absolute paths in all filesystem tools pass through unchanged regardless of worktree state.
- [ ] `workspace_destroy` clears metadata, and subsequent relative-path tool calls revert to resolving against the original repository root (process CWD).
