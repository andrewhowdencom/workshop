# Plan: Add XDG Config File Support

## Objective
Add a persistent YAML configuration file stored in the XDG Base Directory (`$XDG_CONFIG_HOME/workshop/config.yaml`), loaded via `github.com/adrg/xdg` and consumed through `spf13/viper`. Introduce a `workshop config init` subcommand that writes the currently resolved configuration (including env vars) into this file.

## Context
The `workshop` project is a terminal-based coding assistant built on the `ore` framework. It already uses `cobra` for CLI structure and `viper` for configuration binding. Configuration is currently sourced from command-line flags and environment variables with the `WORKSHOP_` prefix. There is no config file support yet, and the `github.com/adrg/xdg` dependency is not present.

Key existing files:
- `cmd/workshop/root.go`: Defines the root command, all flags (`--log-level`, `--thread`, `--api.key`, `--model`, `--base.url`, `--store.dir`), viper env binding, and the `PersistentPreRunE` logging setup.
- `cmd/workshop/version.go`: Defines the `version` subcommand.
- `cmd/workshop/main.go`: Minimal entry point.
- `internal/app/app.go`: Core TUI application logic, accepts functional options for configuration.
- `go.mod`: Uses `spf13/cobra` and `spf13/viper`; no `adrg/xdg`.
- `Taskfile.yml`: Standard development tasks already present.
- `README.md`: Documents current flag and env var usage.

## Architectural Blueprint

**Selected approach**: Extend the existing viper/cobra configuration to include an optional YAML config file located via XDG standards, and add a `config init` subcommand that serializes the current effective viper state to that file.

**Evaluated alternatives**:
- **Alt A**: Manually read/write YAML without viper, using `adrg/xdg` for paths. Rejected because it duplicates viper's precedence and unmarshaling logic that is already wired into the project.
- **Alt B**: Store config in the application data directory (`xdg.DataHome`) rather than config home. Rejected because the XDG spec reserves `DataHome` for application-specific data files; static user-editable configuration belongs in `ConfigHome`.
- **Alt C**: Support multiple config formats (TOML, JSON). Rejected because YAML is the de facto standard for human-editable configs in this ecosystem, and the skill examples use YAML.

**Structure**:
- At startup, viper searches `$XDG_CONFIG_HOME/workshop/` for `config.yaml`.
- Missing config files are silently ignored (the app works fine with only flags/env).
- The `config` command group contains an `init` subcommand (aliased `initialize`) that writes the current resolved configuration to the XDG config path.
- Viper precedence remains: **flag → env → file → default**.
- The `thread` flag remains purely per-invocation; it is never written by `config init`.

## Requirements
1. Load an optional `config.yaml` from the XDG config directory at startup.
2. Config file keys cover: `log-level`, `api.key`, `model`, `base.url`, `store.dir`.
3. `thread` is not persisted to the config file.
4. Add `github.com/adrg/xdg` as a direct dependency.
5. Add a `config` Cobra command group.
6. Add `config init` (alias `initialize`) that writes current resolved values to the XDG config file.
7. Precedence: flag overrides env, env overrides file, file overrides default.
8. Update `README.md` to document the config file location and the new command.
9. All existing flag and env behavior must remain intact.

## Task Breakdown

### Task 1: Add `adrg/xdg` Dependency
- **Goal**: Add `github.com/adrg/xdg` to the module and tidy dependencies.
- **Dependencies**: None
- **Files Affected**: `go.mod`, `go.sum`
- **New Files**: None
- **Interfaces**: None
- **Validation**: `go mod tidy` completes without errors. `go build ./...` passes. `go.mod` contains `github.com/adrg/xdg`.
- **Details**: Run `go get github.com/adrg/xdg` in the project root, then `go mod tidy`. Verify the `go.mod` now includes the dependency and all `replace` directives remain intact.

### Task 2: Integrate XDG Config File Loading into Root Command
- **Goal**: Wire viper to read an optional `config.yaml` from the XDG config directory at application startup.
- **Dependencies**: Task 1
- **Files Affected**: `cmd/workshop/root.go`
- **New Files**: None
- **Interfaces**: None
- **Validation**: `go build ./...` passes. Running the app without a config file still works and behaves identically to before.
- **Details**:
  - Import `github.com/adrg/xdg` and `path/filepath`.
  - In `init()`, after setting up viper env prefix and replacer, compute the config directory: `filepath.Join(xdg.ConfigHome, "workshop")`.
  - Call `viper.AddConfigPath(configDir)`, `viper.SetConfigName("config")`, `viper.SetConfigType("yaml")`.
  - Call `viper.ReadInConfig()`. If the error is `viper.ConfigFileNotFoundError`, ignore it silently. For any other error, print a warning to `os.Stderr` and continue — do not fail startup because of a malformed config file.
  - Do NOT change existing flag/env binding logic.

### Task 3: Add `config init` Subcommand
- **Goal**: Create the `config` command group and an `init` subcommand that writes the current resolved configuration to the XDG config file.
- **Dependencies**: Task 2
- **Files Affected**: None
- **New Files**: `cmd/workshop/config.go`
- **Interfaces**:
  ```go
  var configCmd = &cobra.Command{ Use: "config", Short: "Manage workshop configuration" }
  var configInitCmd = &cobra.Command{ Use: "init", Aliases: []string{"initialize"}, Short: "Initialize a configuration file", RunE: runConfigInit }
  func runConfigInit(cmd *cobra.Command, args []string) error
  ```
- **Validation**: `go build ./...` passes. `go run ./cmd/workshop --help` shows the `config` command. `go run ./cmd/workshop config init` creates a YAML file at the XDG config path. `go run ./cmd/workshop config initialize` produces the same result.
- **Details**:
  - Create `config.go` in `cmd/workshop`.
  - Define `configCmd` and `configInitCmd`. Register `configInitCmd` under `configCmd`, and `configCmd` under `rootCmd`.
  - In `runConfigInit`:
    1. Resolve the target path with `xdg.ConfigFile("workshop/config.yaml")`. This function creates parent directories automatically.
    2. Build a map of settings to persist. Include only keys that make sense for a config file: `log-level`, `api` → `key`, `model`, `base` → `url`, `store` → `dir`. Do NOT include `thread`.
    3. Read the current effective values from the global viper instance (which already reflects flags, env vars, and defaults).
    4. Marshal the map to YAML (using the existing `go.yaml.in/yaml/v3` transitive dependency).
    5. Write the bytes to the resolved path, overwriting if the file already exists.
    6. Print a confirmation message to stdout indicating the file path.

### Task 4: Update README for Config File Support
- **Goal**: Document the new config file location, contents, and the `config init` command.
- **Dependencies**: Task 3
- **Files Affected**: `README.md`
- **New Files**: None
- **Interfaces**: None
- **Validation**: README accurately describes how to create the config file, where it lives, and the precedence hierarchy.
- **Details**:
  - Add a "Configuration file" section describing the default path (`$XDG_CONFIG_HOME/workshop/config.yaml`).
  - Document `workshop config init` as the way to generate the file from current env/flag values.
  - Update the Flags table to note that every flag/env also has a config file equivalent.
  - Document the precedence order: flag > env > file > default.
  - Optionally show a sample `config.yaml`.

### Task 5: End-to-End Validation
- **Goal**: Verify the complete config file lifecycle works correctly.
- **Dependencies**: Task 3, Task 4
- **Files Affected**: None
- **New Files**: None
- **Interfaces**: None
- **Validation**: `task validate` passes. Manual test sequence succeeds.
- **Details**:
  1. Run `go test ./...` and confirm no regressions.
  2. Run `go build ./...`.
  3. Run `task validate` (lint + test + build).
  4. **Manual test**:
     - Set `WORKSHOP_MODEL=gpt-4o-mini`.
     - Run `go run ./cmd/workshop config init`.
     - Inspect the generated file; it must contain `model: gpt-4o-mini`.
     - Unset `WORKSHOP_MODEL`.
     - Run `go run ./cmd/workshop` (without `--model`); the app must start without requiring `--model` because the config file supplies it.
     - Run `go run ./cmd/workshop --model o3` and confirm the flag overrides the file value.
  5. Confirm `thread` is never written to the config file and still works as a flag.

## Dependency Graph
- Task 1 → Task 2 → Task 3 → Task 4 || Task 5
- Task 4 and Task 5 are parallelizable (documentation and validation are independent), though validation logically should include the documented behavior.

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| Config file YAML structure doesn't match viper's key expectations, causing values to be ignored | Medium | Medium | Use a flat or clearly nested key structure that mirrors the flag names (`api.key`, `base.url`, etc.). Test with `viper.GetString` after `ReadInConfig`. |
| `config init` writes `api.key` to disk in plaintext | Low | High | Accepted per user direction. Document the security implication in README. |
| Existing `WORKSHOP_LOG_LEVEL=debug` + `config init` persists debug logging permanently | Low | Medium | Document that `config init` captures the current effective state, including env vars. Users can edit the file afterward. |
| `adrg/xdg` returns unexpected paths on minimal/containerized environments | Low | Low | `adrg/xdg` falls back to `$HOME/.config` when `XDG_CONFIG_HOME` is unset, which is well-tested behavior. |
| Adding viper config read in `init()` breaks startup if config file is malformed | High | Low | Explicitly catch and warn on non-`ConfigFileNotFoundError` read errors; do not fail startup. |

## Validation Criteria
- [ ] `go.mod` contains `github.com/adrg/xdg` and `go mod tidy` is clean.
- [ ] `go build ./...` passes.
- [ ] `go test ./...` passes.
- [ ] `task validate` passes.
- [ ] Running without a config file behaves identically to before (flags + env only).
- [ ] `go run ./cmd/workshop config init` creates a YAML file at the XDG config path.
- [ ] The generated YAML file contains the current effective values for `log-level`, `api.key`, `model`, `base.url`, and `store.dir`.
- [ ] `thread` is absent from the generated YAML file.
- [ ] After creating the file, running the app without flags/env picks up values from the config file.
- [ ] A flag still overrides a value set in the config file.
- [ ] An env var still overrides a value set in the config file.
- [ ] README documents the config file path, the `config init` command, and the precedence order.
