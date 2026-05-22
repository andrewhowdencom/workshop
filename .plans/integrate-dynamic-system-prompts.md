# Plan: Integrate Dynamic System Prompts (Role Switching)

## Objective

Add tools to the `workshop` coding assistant that allow the LLM agent to introspect and switch its own system prompt (persona/role) at runtime. Role definitions are stored as YAML-frontmatter files in `XDG_DATA_DIR/workshop/roles/`. The implementation leverages upstream `ore` framework changes (dynamic `WithContentFunc` and thread-aware step factory) to persist the active role in `thread.Metadata` across session restarts.

## Context

- **Workshop** is a terminal-based coding assistant built on `ore` (`github.com/andrewhowdencom/ore`).
- `internal/app/app.go` currently uses the **removed** `systemprompt.WithContent` API and the old `func() (*loop.Step, error)` factory signature.
- `go.mod` lacks `replace` directives for new `ore` submodules (`x/tool` and `x/conduit`), causing build failures against latest `ore@main`.
- Upstream `ore` commits `ede7168` (`WithContentFunc`) and `3848d3a` (thread-aware step factory) are already present in the local `../ore` repository.
- `ore/thread.Thread` exposes `Metadata map[string]string` with mutex-safe `GetMetadata`/`SetMetadata` methods; metadata is persisted automatically by `session.Stream.Process` calling `store.Save(thread)` after each turn.
- Workshop already imports `github.com/adrg/xdg v0.5.3` for XDG directory resolution and `go.yaml.in/yaml/v3 v3.0.4` for YAML parsing.
- Existing `.plans/` in workshop use kebab-case naming (`add-xdg-config-file.md`, `setup-cobra-viper-cli-with-taskfile.md`).

## Architectural Blueprint

We adopt the **metadata-based dynamic prompt** approach (Option C from ideation):

1. The `stepFactory` receives `*thread.Thread` and closes over it.
2. `systemprompt.WithContentFunc` is given a closure that reads `thr.GetMetadata("workshop.role")` on every inference turn, loads the matching role file from disk, and returns the prompt text.
3. The `switch_role(name)` tool is a closure that validates the role file exists, then calls `thr.SetMetadata("workshop.role", name)`.
4. The `list_roles` and `get_current_role` tools expose discovery and introspection.
5. No custom artifact handler or fake `RoleSystem` turns are needed — metadata persistence is handled natively by the framework.

This avoids state-buffer bloat and aligns with the upstream design intention.

## Requirements

1. Fix `go.mod` to include missing `replace` directives for `ore/x/tool` and `ore/x/conduit`.
2. Migrate `internal/app/app.go` to the new upstream API (`WithContentFunc`, thread-aware factory).
3. Implement a YAML-frontmatter role file loader in `internal/app/roles.go`.
4. Register `list_roles`, `get_current_role`, and `switch_role` tools in the tool registry.
5. Wire dynamic prompt loading into the system prompt transform via thread metadata.
6. Update `README.md` with role file format and new tool documentation.
7. Validate end-to-end: automated tests pass, manual TUI test confirms role switching and thread resumption.

## Task Breakdown

### Task 1: Fix go.mod replace directives for new ore submodules
- **Goal**: Resolve module resolution errors so `workshop` compiles against latest `ore@main`.
- **Dependencies**: None
- **Files Affected**: `/home/andrewhowdencom/Development/workshop/go.mod`
- **New Files**: None
- **Interfaces**: None
- **Validation**: `go mod tidy && go build ./...` resolves without "no required module provides package" errors.
- **Details**: Add `replace github.com/andrewhowdencom/ore/x/tool => ../ore/x/tool` and `replace github.com/andrewhowdencom/ore/x/conduit => ../ore/x/conduit`. Run `go mod tidy` to pull in transitive requirements and update `go.sum`.

### Task 2: Migrate app.go to new upstream API
- **Goal**: Update workshop to use the new thread-aware step factory and `WithContentFunc` API.
- **Dependencies**: Task 1
- **Files Affected**: `/home/andrewhowdencom/Development/workshop/internal/app/app.go`
- **New Files**: None
- **Interfaces**:
  - `stepFactory` signature changes from `func() (*loop.Step, error)` to `func(thr *thread.Thread) (*loop.Step, error)`.
  - `session.NewManager(store, prov, stepFactory, ...)` call updated to match new signature.
  - `systemprompt.New(systemprompt.WithContent(...))` replaced with `systemprompt.New(systemprompt.WithContentFunc(currentPrompt))`.
- **Validation**: `go build ./...` compiles successfully.
- **Details**: Remove the static string passed to `WithContent`. Wrap the existing default prompt in a `WithContentFunc` closure as a temporary placeholder. The closure will be expanded in Task 6 to read from metadata.

### Task 3: Implement role file loader
- **Goal**: Parse YAML-frontmatter role definitions from `XDG_DATA_DIR/workshop/roles/`.
- **Dependencies**: Task 1
- **Files Affected**: None
- **New Files**: `/home/andrewhowdencom/Development/workshop/internal/app/roles.go`
- **Interfaces**:
  - `type RoleDefinition struct { Name string; Description string; Prompt string }`
  - `func loadRole(dir, name string) (*RoleDefinition, error)`
  - `func listRoleDefinitions(dir string) ([]RoleDefinition, error)`
  - `func roleDir() string` (uses `github.com/adrg/xdg`)
- **Validation**: Unit tests for frontmatter parsing (optional but encouraged); manual test that `loadRole` correctly splits frontmatter from body.
- **Details**: Read file from `<dir>/<name>.md`. If file starts with `---`, parse YAML between first and second `---` using `go.yaml.in/yaml/v3`. Everything after the closing `---` is the prompt body. Return error if file not found or malformed. `listRoleDefinitions` scans the directory for `*.md` files, loads each, and returns the slice. `roleDir()` returns `filepath.Join(xdg.DataHome, "workshop", "roles")`. Graceful fallback: missing directory → empty list.

### Task 4: Register list_roles tool
- **Goal**: Allow the agent to discover available personas.
- **Dependencies**: Task 2, Task 3
- **Files Affected**: `/home/andrewhowdencom/Development/workshop/internal/app/app.go`
- **New Files**: None
- **Interfaces**:
  - Tool name: `list_roles`
  - JSON Schema: `{}` (no arguments)
  - Returns: serializable slice of objects with `name` and `description` fields.
- **Validation**: Manual TUI test: agent can call `list_roles` and receives accurate list.
- **Details**: Register in the tool registry inside `stepFactory`. Closure over `roleDir()`. If directory does not exist or is empty, return empty array, not error.

### Task 5: Register get_current_role and switch_role tools
- **Goal**: Allow the agent to introspect its current persona and switch to another.
- **Dependencies**: Task 2, Task 3
- **Files Affected**: `/home/andrewhowdencom/Development/workshop/internal/app/app.go`
- **New Files**: None
- **Interfaces**:
  - Tool name: `get_current_role` — schema `{}`, returns `{ "role": string, "description": string, "prompt_preview": string }`.
  - Tool name: `switch_role` — schema `{"name": {"type": "string", "description": "Name of the role to activate"}}`, returns confirmation string.
- **Validation**: Manual TUI test: `switch_role` validates name, updates metadata, returns success. `get_current_role` reflects the change.
- **Details**: Both tools are closures over `*thread.Thread` (from the factory argument) and `roleDir()`.
  - `switch_role`: validate `args["name"]` is a string; call `loadRole(roleDir, name)` to verify existence; call `thr.SetMetadata("workshop.role", name)`; return `fmt.Sprintf("Switched to role: %s", name)`. If role not found, return error.
  - `get_current_role`: read `thr.GetMetadata("workshop.role")`. If unset, role is `"default"`. Load role file to return name, description, and first 200 chars of prompt as preview.

### Task 6: Wire dynamic system prompt via thread metadata
- **Goal**: Connect the metadata-driven role to the system prompt transform.
- **Dependencies**: Task 2, Task 3, Task 5
- **Files Affected**: `/home/andrewhowdencom/Development/workshop/internal/app/app.go`
- **New Files**: None
- **Interfaces**:
  - `currentPrompt := func() string { ... }` passed to `systemprompt.WithContentFunc(currentPrompt)`.
- **Validation**: Manual TUI test: start workshop, verify default prompt, call `switch_role("reviewer")`, verify next assistant response uses reviewer persona. Quit and resume with `--thread <id>`, verify persona persists.
- **Details**: Inside `stepFactory(thr)`, define `currentPrompt` closure that:
  1. Calls `thr.GetMetadata("workshop.role")`.
  2. If found and non-empty, calls `loadRole(roleDir, roleName)`.
  3. If successful, returns `role.Prompt`.
  4. Otherwise, returns the baked-in default prompt.
  This closure is passed to `systemprompt.WithContentFunc`. Because `thr` is a pointer and `GetMetadata` is mutex-safe, this is thread-safe and evaluates fresh on every inference turn.

### Task 7: Update README documentation
- **Goal**: Document the new role system for users.
- **Dependencies**: Task 4, Task 5, Task 6
- **Files Affected**: `/home/andrewhowdencom/Development/workshop/README.md`
- **New Files**: None
- **Interfaces**: None
- **Validation**: README renders correctly; new tools appear in Available tools table.
- **Details**: Add a "Roles" section describing the `XDG_DATA_DIR/workshop/roles/` directory, YAML frontmatter format, and example `reviewer.md`. Add `list_roles`, `get_current_role`, `switch_role` to the tools table. Explain that roles persist per-thread.

### Task 8: End-to-end validation and commit
- **Goal**: Ensure the entire feature works together and the repository is healthy.
- **Dependencies**: Task 1–7
- **Files Affected**: None (validation only)
- **New Files**: None
- **Interfaces**: None
- **Validation**:
  - `go test ./...` passes.
  - `go build ./...` succeeds.
  - Manual TUI test cycle: create role file → switch → verify → resume → verify.
- **Details**: Run all automated checks. Perform manual validation. Commit all changes with a conventional commit message.

## Dependency Graph

- Task 1 → Task 2
- Task 1 → Task 3
- (Task 2, Task 3) → Task 4
- (Task 2, Task 3) → Task 5
- (Task 2, Task 3, Task 5) → Task 6
- (Task 4, Task 5, Task 6) → Task 7
- Task 7 → Task 8

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| Missing `replace` directives cause module resolution errors | High | High | Task 1 explicitly adds them; validated by `go build`. |
| `WithContentFunc` closure called with nil thread | Medium | Low | Factory always receives `thr` from `session.Manager.Create/Attach`; upstream guarantees non-nil. |
| Role file deleted after switch | Medium | Low | `WithContentFunc` falls back to default prompt silently if `loadRole` returns error. |
| Same-turn prompt confusion | Low | Medium | Documented in README: switch applies to next turn, not current generation. |
| Old ore examples broken by upstream API change | Low | High | Already handled by upstream commit `3848d3a` which updated all call sites. |

## Validation Criteria

- [ ] `go build ./...` succeeds with zero errors.
- [ ] `go test ./...` passes.
- [ ] `list_roles` returns correct JSON array of available roles.
- [ ] `switch_role("reviewer")` updates metadata and returns success message.
- [ ] After `switch_role`, the next assistant turn uses the new prompt content.
- [ ] Resuming a thread with `--thread <id>` restores the previously active role.
- [ ] README documents the role system and new tools.
