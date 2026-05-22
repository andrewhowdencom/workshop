# Plan: Restructure Workshop with Cobra/Viper CLI and Taskfile

## Objective
Convert the flat root-level Go application into a standard-layout Go project using Cobra for CLI structure, Viper for configuration management, and Taskfile as the task runner. The root command opens the existing TUI, with a version subcommand and configurable log level.

## Context
The `workshop` project is a terminal-based coding assistant built on the `ore` framework. Currently, all logic lives in a single `main.go` at the repository root, using `flag` for CLI parsing and `os.Getenv` for configuration. The project has local `replace` directives for `ore` packages and depends on `charmbracelet/bubbletea` for the TUI. The codebase follows no standard Go layout (no `cmd/` or `internal/` directories). No tests or build tooling exist.

## Architectural Blueprint
**Selected approach**: Standard Go project layout with Cobra commands and Viper configuration binding.

- **Rejected alternative**: Keep flat layout and manually wire flags. Rejected because it doesn't scale and violates the Go skill's standard layout guidance.
- **Rejected alternative**: Use a third-party CLI framework other than Cobra. Rejected because Cobra is the de-facto standard in the Go ecosystem and explicitly aligns with the skills.

**Structure**:
- `cmd/workshop/main.go`: Minimal entry point (`main()` calls `Execute()`).
- `cmd/workshop/root.go`: Root Cobra command. Its `Run` opens the TUI. Defines persistent flags (`--log-level`) and local flags (`--thread`, `--api-key`, `--model`, `--base-url`, `--store-dir`). Wires Viper with `WORKSHOP_` env prefix.
- `cmd/workshop/version.go`: `version` subcommand using `runtime/debug.ReadBuildInfo`.
- `internal/app/app.go`: Extracted TUI wiring logic from the current `main.go`, exposed as `Run(ctx context.Context, opts ...Option) error`.
- `Taskfile.yml`: Standard tasks (`setup`, `build`, `test`, `lint`, `validate`, `generate`).

## Requirements
1. Restructure to standard Go layout (`cmd/`, `internal/`)
2. Add Cobra and Viper dependencies
3. Create `Taskfile.yml` with standard tasks per the taskfile skill
4. Root command opens the TUI when called without subcommands
5. `version` subcommand prints build info
6. `--log-level` persistent flag controls `slog` level
7. Viper binds flags to env vars with `WORKSHOP_` prefix
8. All existing TUI and ore functionality must be preserved
9. Old `main.go` at root must be removed once `cmd/workshop` is active

## Task Breakdown

### Task 1: Add Cobra and Viper Dependencies
- **Goal**: Add `spf13/cobra` and `spf13/viper` to the module and tidy dependencies.
- **Dependencies**: None
- **Files Affected**: `go.mod`, `go.sum`
- **New Files**: None
- **Interfaces**: None
- **Validation**: `go mod tidy` completes without errors; `go build` still succeeds for the current root `main.go`.
- **Details**: Run `go get github.com/spf13/cobra github.com/spf13/viper` in the project root, then `go mod tidy`. Verify the `go.mod` now includes these dependencies.

### Task 2: Create Taskfile.yml
- **Goal**: Add a `Taskfile.yml` with standard tasks.
- **Dependencies**: None (parallelizable with Task 1)
- **Files Affected**: None
- **New Files**: `Taskfile.yml`
- **Interfaces**: None
- **Validation**: `task --list` shows all defined tasks.
- **Details**: Create `Taskfile.yml` with tasks: `setup` (install tools), `build` (`go build ./cmd/workshop`), `test` (`go test ./...`), `lint` (`golangci-lint run`), `validate` (runs lint, test, build), `generate` (placeholder for code generation). Ensure the file uses valid Taskfile syntax.

### Task 3: Extract TUI Logic to `internal/app/app.go`
- **Goal**: Move the existing `run()` logic from `main.go` into a reusable internal package.
- **Dependencies**: Task 1 (module still valid)
- **Files Affected**: `main.go` (for reference; will be deleted later)
- **New Files**: `internal/app/app.go`
- **Interfaces**: 
  ```go
  package app
  
  type Option func(*config)
  
  func WithThreadID(id string) Option
  func WithAPIKey(key string) Option
  func WithModel(model string) Option
  func WithBaseURL(url string) Option
  func WithStoreDir(dir string) Option
  
  func Run(ctx context.Context, opts ...Option) error
  ```
- **Validation**: `go build ./internal/app` succeeds. The package compiles and its exported API is usable.
- **Details**: Copy the current `run()` body into `Run()` inside `internal/app/app.go`. Replace `flag` and `os.Getenv` usage with parameters populated via functional options. Keep all ore-related imports and logic identical. Do NOT delete the root `main.go` yet.

### Task 4: Create `cmd/workshop/main.go` and `cmd/workshop/root.go`
- **Goal**: Create the Cobra root command that opens the TUI and defines all flags.
- **Dependencies**: Task 3 (`internal/app` must exist)
- **Files Affected**: None
- **New Files**: `cmd/workshop/main.go`, `cmd/workshop/root.go`
- **Interfaces**: 
  ```go
  // cmd/workshop/root.go
  var rootCmd = &cobra.Command{
      Use:   "workshop",
      Short: "A terminal-based coding assistant",
      RunE:  runRoot,
  }
  
  func Execute() error
  ```
- **Validation**: `go build ./cmd/workshop` succeeds. `go run ./cmd/workshop --help` shows flags.
- **Details**: 
  - `main.go` should be minimal: call `cobra.CheckErr(rootCmd.Execute())` or similar.
  - `root.go` should define `rootCmd` with `RunE` calling `app.Run(ctx, opts...)`.
  - Add persistent flag `--log-level` with default `info`.
  - Add local flags: `--thread`, `--api.key`, `--model`, `--base.url`, `--store.dir`.
  - In a `PersistentPreRunE`, configure `slog` based on `--log-level`.
  - Wire Viper: `viper.BindPFlags(cmd.Flags())`, `viper.SetEnvPrefix("WORKSHOP")`, `viper.AutomaticEnv()`.
  - Read flag values via `viper.GetString()` and pass them to `app.Run()`.

### Task 5: Create `cmd/workshop/version.go`
- **Goal**: Add the `version` subcommand.
- **Dependencies**: Task 4 (cmd package exists)
- **Files Affected**: None
- **New Files**: `cmd/workshop/version.go`
- **Interfaces**: None
- **Validation**: `go run ./cmd/workshop version` prints version info like `v0.0.0-...` from `runtime/debug.ReadBuildInfo`.
- **Details**: Implement `versionCmd` using `runtime/debug.ReadBuildInfo`. Print the main module version. If no build info is available (e.g., `go run`), print a fallback like `dev`. Add the command to `rootCmd`.

### Task 6: Wire Viper Configuration and Delete Old `main.go`
- **Goal**: Complete the Viper integration and remove the obsolete root `main.go`.
- **Dependencies**: Task 4, Task 5
- **Files Affected**: `main.go` (delete), `cmd/workshop/root.go` (modify)
- **New Files**: None
- **Interfaces**: None
- **Validation**: `go build ./...` passes. `go run ./cmd/workshop --help` shows all flags. `WORKSHOP_LOG_LEVEL=debug go run ./cmd/workshop` opens the TUI with debug logging.
- **Details**: 
  - Ensure all flags are correctly bound to Viper and env vars.
  - Handle missing required configuration gracefully (e.g., `--api.key` / `WORKSHOP_API_KEY` is required; return a clear error if absent).
  - Delete the root `main.go` file.
  - Verify `go test ./...`, `go build ./...`, and `go run ./cmd/workshop` all work.
  - If there are `replace` directive issues, ensure they remain intact.

### Task 7: Update README.md
- **Goal**: Document the new CLI structure and usage.
- **Dependencies**: Task 6
- **Files Affected**: `README.md`
- **New Files**: None
- **Interfaces**: None
- **Validation**: README accurately describes `go run ./cmd/workshop`, flags, env vars, and `workshop version`.
- **Details**: Update the Usage, Resume thread, and Persistent store sections to reflect the new Cobra/Viper commands and flags. Document the `WORKSHOP_` env var prefix. Mention `task` for development tasks.

## Dependency Graph
- Task 1 || Task 2 (parallelizable)
- Task 1 → Task 3
- Task 3 → Task 4
- Task 4 → Task 5
- Task 4, Task 5 → Task 6
- Task 6 → Task 7

## Risks & Mitigations
| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| `ore` replace directives cause build failures after restructuring | High | Medium | Verify `go mod tidy` does not alter `replace` lines. Test `go build ./...` after every task. |
| Root `main.go` deletion breaks CI/CD that calls `go run .` | Medium | Medium | Update README to reflect `go run ./cmd/workshop`. Add a note in Task 7. |
| Viper env prefix `WORKSHOP_` conflicts with existing `ORE_*` env vars in user workflows | Medium | High | Accept both prefixes during transition, or document the change clearly in README. |
| Cobra/Viper versions incompatible with Go 1.26.2 | Low | Low | Check compatibility during Task 1; pin versions if necessary. |

## Validation Criteria
- [ ] `go mod tidy` passes and `go.mod` contains `cobra` and `viper`
- [ ] `task --list` shows setup, build, test, lint, validate, generate
- [ ] `go build ./cmd/workshop` produces a binary
- [ ] `go run ./cmd/workshop --help` shows `--log-level`, `--thread`, `--api.key`, `--model`, `--base.url`, `--store.dir`
- [ ] `go run ./cmd/workshop version` prints build version
- [ ] `go run ./cmd/workshop` opens the TUI (or at least starts without a CLI parsing error)
- [ ] `WORKSHOP_LOG_LEVEL=debug go run ./cmd/workshop` enables debug logging
- [ ] `go test ./...` passes (or finds no tests, which is valid)
- [ ] Old root `main.go` is deleted
- [ ] README.md reflects new usage
