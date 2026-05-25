# Plan: Refactor Workshop for ore v0.2.0 API

## Objective
Update the workshop application to compile against the current `ore` library HEAD, publish new versions of affected `ore` submodules, and eliminate all local `replace ../ore*` directives in favor of published module versions.

## Context
The `ore` library at `../ore` has undergone substantial refactoring since the workshop was last updated. Key API changes observed in the current ore HEAD (`bd78bda` and prior commits):

- `NewRegistry`, `Registry`, and `ToolFunc` moved from `ore/x/tool` into the root `ore/tool` package.
- `registry.Handler()` removed; use `ore/x/tool.NewHandler(registry)` instead.
- `ToolFunc` signature changed: now accepts `tool.Sandbox` as the second parameter.
- `Register` returns `error`, requiring error handling at every call site.

The workshop currently pins an older API via local `replace ../ore*` directives in `go.mod` and old pseudo-versions. The only two files that import `ore` packages are `internal/app/app.go` and `internal/app/app_test.go`. No other workshop packages (`cmd/workshop`, `internal/app/roles.go`, `internal/app/tool_schemas.go`, `internal/app/roles_test.go`) reference ore.

The `../ore` repository currently has root tag `v0.0.2` and submodule tags `*/v0.1.0`. All submodules (`x/tool`, `x/tool/bash`, `x/tool/filesystem`, `x/tool/skills`, `x/conduit/tui`, `x/conduit/http`, `x/provider/openai`) depend on `github.com/andrewhowdencom/ore v0.0.2`.

## Architectural Blueprint
The refactor is localized to the app wiring layer in `internal/app/app.go` and its tests in `internal/app/app_test.go`. No structural changes are needed in other workshop packages.

The execution strategy:
1. Publish new `ore` versions (root `v0.0.3`, submodules `v0.2.0`) from the `../ore` repository.
2. Refactor `internal/app/app.go` to use new import paths (`ore/tool` for core contracts, `ore/x/tool` aliased as `xtool` for `NewHandler`), new handler signatures accepting `tool.Sandbox`, and a `mustRegister` helper for error handling.
3. Update `internal/app/app_test.go` to pass `nil` sandbox to direct handler invocations.
4. Remove all `replace` directives from `go.mod` and resolve published versions via `go get` and `go mod tidy`.
5. Build and test to confirm everything compiles and passes with published dependencies.

## Requirements
1. Tag `github.com/andrewhowdencom/ore` root module with `v0.0.3`.
2. Update all affected submodule `go.mod` files in `../ore` to reference root `v0.0.3`, run `go mod tidy`, commit, and tag with `v0.2.0`.
3. Update `internal/app/app.go` imports to use `ore/tool` for core contracts and `ore/x/tool` aliased as `xtool` for `NewHandler`.
4. Add a `mustRegister` helper (and optionally `mustRegisterRaw` for custom tools without a `provider.Tool` struct) and update all `registry.Register` calls to handle errors.
5. Update custom role tool handler signatures (`makeListRolesHandler`, `makeGetCurrentRoleHandler`, `makeSwitchRoleHandler`) to accept `tool.Sandbox`.
6. Replace `registry.Handler()` with `xtool.NewHandler(registry)`.
7. Update `internal/app/app_test.go` to pass `nil` as the sandbox argument to all direct handler invocations.
8. Remove all `replace ../ore*` directives from `go.mod`.
9. Resolve published ore versions via `go get @latest` for each direct dependency.
10. Ensure `go build ./...` and `go test ./...` pass with no local replaces remaining.

## Task Breakdown

### Task 1: Publish ore Module Versions
- **Goal**: Tag the `ore` root module `v0.0.3`, update all affected submodule `go.mod` files to reference the new root version, tidy, commit, and tag submodules `v0.2.0`.
- **Dependencies**: None.
- **Files Affected** (in `../ore` repository): `go.mod`, `x/tool/go.mod`, `x/tool/bash/go.mod`, `x/tool/filesystem/go.mod`, `x/tool/skills/go.mod`, `x/conduit/tui/go.mod`, `x/conduit/http/go.mod`, `x/provider/openai/go.mod`.
- **New Files**: None.
- **Interfaces**: N/A.
- **Validation**: Run `git tag -l` in `../ore` to confirm `v0.0.3` and all `*/v0.2.0` tags exist. Verify submodule `go.mod` files reference `github.com/andrewhowdencom/ore v0.0.3`.
- **Details**:
  1. In the `../ore` directory, tag the root module: `git tag v0.0.3 && git push origin v0.0.3`.
  2. For each affected submodule (`x/tool`, `x/tool/bash`, `x/tool/filesystem`, `x/tool/skills`, `x/conduit/tui`, `x/conduit/http`, `x/provider/openai`):
     a. `cd` into the submodule directory.
     b. `go get github.com/andrewhowdencom/ore@v0.0.3` (and any other updated submodule dependencies if cross-submodule dependencies exist).
     c. `go mod tidy`.
     d. Commit the `go.mod` and `go.sum` changes with a descriptive message (e.g., `chore(release): bump root ore to v0.0.3`).
  3. Tag each submodule and push all tags:
     - `git tag x/tool/v0.2.0`
     - `git tag x/tool/bash/v0.2.0`
     - `git tag x/tool/filesystem/v0.2.0`
     - `git tag x/tool/skills/v0.2.0`
     - `git tag x/conduit/tui/v0.2.0`
     - `git tag x/conduit/http/v0.2.0`
     - `git tag x/provider/openai/v0.2.0`
     Then push all tags: `git push origin --tags`.
  4. If the Go module proxy has not cached the new tags immediately, use `GOPROXY=direct` during `go get`.

### Task 2: Refactor Workshop App Wiring
- **Goal**: Update `internal/app/app.go` and `internal/app/app_test.go` to use the new ore v0.2.0 API.
- **Dependencies**: Task 1 (published ore versions must be available before removing replaces; however, code changes can be validated against local `../ore` which already has the new API).
- **Files Affected**: `internal/app/app.go`, `internal/app/app_test.go`.
- **New Files**: None.
- **Interfaces**:
  - New import: `github.com/andrewhowdencom/ore/tool` (for `NewRegistry`, `Registry`, `ToolFunc`, `Sandbox`).
  - Aliased import: `xtool "github.com/andrewhowdencom/ore/x/tool"` (for `NewHandler`).
  - New helper: `func mustRegister(registry tool.Registry, t provider.Tool, fn tool.ToolFunc)`.
  - Optional helper: `func mustRegisterRaw(registry tool.Registry, name, description string, schema map[string]any, fn tool.ToolFunc)` for custom role tools that lack a `provider.Tool` struct.
  - Updated handler signatures:
    - `makeListRolesHandler(rdir string) tool.ToolFunc` → closure accepts `_ tool.Sandbox`.
    - `makeGetCurrentRoleHandler(rdir string, thr *thread.Thread) tool.ToolFunc` → closure accepts `_ tool.Sandbox`.
    - `makeSwitchRoleHandler(rdir string, thr *thread.Thread) tool.ToolFunc` → closure accepts `_ tool.Sandbox`.
- **Validation**: `go build ./...` passes (local replaces still active).
- **Details**:
  1. In `internal/app/app.go`:
     a. Add `github.com/andrewhowdencom/ore/tool` import.
     b. Change `"github.com/andrewhowdencom/ore/x/tool"` to `xtool "github.com/andrewhowdencom/ore/x/tool"`.
     c. Change `registry := tool.NewRegistry()` to use `tool.NewRegistry()` from `ore/tool`.
     d. Add `mustRegister` helper (and optionally `mustRegisterRaw`):
        ```go
        func mustRegister(registry tool.Registry, t provider.Tool, fn tool.ToolFunc) {
            if err := registry.Register(t.Name, t.Description, t.Schema, fn); err != nil {
                panic(fmt.Sprintf("register %s: %v", t.Name, err))
            }
        }
        ```
     e. Replace all `registry.Register(...)` calls with `mustRegister`:
        - `mustRegister(registry, filesystem.ReadFileTool, filesystem.ReadFile)`
        - `mustRegister(registry, filesystem.WriteFileTool, filesystem.WriteFile)`
        - `mustRegister(registry, filesystem.EditFileTool, filesystem.EditFile)`
        - `mustRegister(registry, filesystem.ListDirectoryTool, filesystem.ListDirectory)`
        - `mustRegister(registry, filesystem.SearchFilesTool, filesystem.SearchFiles)`
        - `mustRegister(registry, bash.BashTool, bash.Bash)`
        - For role tools, either construct `provider.Tool{}` inline or use `mustRegisterRaw`:
          ```go
          mustRegisterRaw(registry, "list_roles", "List all available role definitions.", listRolesSchema, makeListRolesHandler(rdir))
          ```
     f. Replace `loop.WithHandlers(registry.Handler())` with `loop.WithHandlers(xtool.NewHandler(registry))`.
     g. Keep explicit error handling for `skillsToolkit.Register(registry)` as-is.
     h. Update `makeListRolesHandler`, `makeGetCurrentRoleHandler`, `makeSwitchRoleHandler` to return closures that accept `_ tool.Sandbox` as the second parameter:
        ```go
        func makeListRolesHandler(rdir string) tool.ToolFunc {
            return func(ctx context.Context, _ tool.Sandbox, args map[string]any) (any, error) {
                // existing body
            }
        }
        ```
  2. In `internal/app/app_test.go`:
     a. Update all direct handler invocations to pass `nil` as the sandbox argument:
        - `handler(context.Background(), nil, map[string]any{})`
        - `handler(context.Background(), nil, map[string]any{"name": "planner"})`
        - etc.

### Task 3: Remove Local Replaces and Update Dependencies
- **Goal**: Eliminate all `replace ../ore*` directives from `go.mod` and resolve newly published ore versions.
- **Dependencies**: Task 1 (published versions must be fetchable), Task 2 (code must already use new API).
- **Files Affected**: `go.mod`, `go.sum`.
- **New Files**: None.
- **Interfaces**: N/A.
- **Validation**: `go build ./...` passes with no local replaces.
- **Details**:
  1. Delete all `replace` directives pointing to `../ore*` from `go.mod`. There are currently 8 replace directives:
     - `replace github.com/andrewhowdencom/ore => ../ore`
     - `replace github.com/andrewhowdencom/ore/x/conduit/tui => ../ore/x/conduit/tui`
     - `replace github.com/andrewhowdencom/ore/x/provider/openai => ../ore/x/provider/openai`
     - `replace github.com/andrewhowdencom/ore/x/tool/filesystem => ../ore/x/tool/filesystem`
     - `replace github.com/andrewhowdencom/ore/x/conduit/http => ../ore/x/conduit/http`
     - `replace github.com/andrewhowdencom/ore/x/tool => ../ore/x/tool`
     - `replace github.com/andrewhowdencom/ore/x/tool/skills => ../ore/x/tool/skills`
     - `replace github.com/andrewhowdencom/ore/x/conduit => ../ore/x/conduit`
  2. Run `go get` for each direct ore dependency at `@latest`:
     - `go get github.com/andrewhowdencom/ore@latest`
     - `go get github.com/andrewhowdencom/ore/x/tool/bash@latest`
     - `go get github.com/andrewhowdencom/ore/x/tool/filesystem@latest`
     - `go get github.com/andrewhowdencom/ore/x/tool/skills@latest`
     - `go get github.com/andrewhowdencom/ore/x/conduit/tui@latest`
     - `go get github.com/andrewhowdencom/ore/x/conduit/http@latest`
     - `go get github.com/andrewhowdencom/ore/x/provider/openai@latest`
  3. Run `go mod tidy` to normalize indirect dependencies and fix the `go.yaml.in/yaml/v3` typo if it persists.
  4. Verify `go build ./...` succeeds.

### Task 4: Validate Build and Tests
- **Goal**: Run the full test suite and verify the module graph is clean.
- **Dependencies**: Task 2, Task 3.
- **Files Affected**: None (read-only validation).
- **New Files**: None.
- **Interfaces**: N/A.
- **Validation**: `go test ./...` passes; zero replace directives for ore remain.
- **Details**:
  1. Run `go test ./...` and confirm all tests pass.
  2. Run `grep -c 'replace.*\.\./ore' go.mod` and confirm it returns 0.
  3. Run `go list -m all | grep 'andrewhowdencom/ore'` and confirm all ore dependencies resolve to published versions (e.g., `v0.0.3`, `v0.2.0`) and not local paths.

## Dependency Graph
- Task 1 → Task 2 (Task 2 code changes can be validated locally, but Task 1 must complete before Task 3)
- Task 1 → Task 3
- Task 2 → Task 3 (code must be refactored before switching to published versions)
- Task 3 → Task 4

## Risks & Mitigations
| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| Go module proxy delay after tagging | High | Medium | Use `GOPROXY=direct` during `go get` if the proxy has not cached new tags. |
| Submodule dependency ordering in ore repo | Medium | Medium | Update root first, then submodules. If submodules depend on each other, update in topological order. |
| `go.yaml.in/yaml/v3` typo persists after `go mod tidy` | Low | Low | Manually fix the import in `internal/app/roles.go` to `gopkg.in/yaml.v3` and re-run `go mod tidy`. |
| Existing `v0.0.3` or `*/v0.2.0` tags already exist in ore repo | Low | Low | Check `git tag -l` before creating tags. If tags exist, use the next available patch/minor version and update this plan accordingly. |

## Validation Criteria
- [ ] `github.com/andrewhowdencom/ore` root module tagged `v0.0.3` and pushed.
- [ ] All affected ore submodules tagged `v0.2.0` and pushed.
- [ ] `internal/app/app.go` compiles with new ore API: imports `ore/tool`, uses `xtool.NewHandler(registry)`, has `mustRegister` helper, custom handlers accept `tool.Sandbox`.
- [ ] `internal/app/app_test.go` passes with updated handler invocations passing `nil` sandbox.
- [ ] `go.mod` contains zero `replace` directives pointing to `../ore`.
- [ ] `go build ./...` passes.
- [ ] `go test ./...` passes.
- [ ] All ore dependencies in `go list -m all` resolve to published module versions (`v0.0.3` or `v0.2.0`).
