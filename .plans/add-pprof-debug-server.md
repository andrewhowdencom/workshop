# Plan: Add pprof Debug Server

## Objective

Add a `--pprof` persistent flag (and `--pprof.addr`) to the `workshop` CLI that, when enabled, starts a separate `localhost` HTTP listener with standard Go `net/http/pprof` handlers under `/debug/pprof/`. This applies to both the root TUI/stdio command and the `http` subcommand, runs for the application's full lifetime, and gracefully logs-and-skips if the configured port is already in use.

## Context

The `workshop` project is a terminal-based coding assistant built on the `ore` framework. It uses Cobra for CLI handling and Viper for configuration management (env vars, config file, flags).

**Key files:**
- `cmd/workshop/root.go`: Defines the root command, persistent flags (e.g., `--log-level`, `--provider.*`, `--store.dir`), and the `runRoot()` function which dispatches to `app.RunTUI()` (interactive) or `app.RunStdio()` (non-interactive).
- `cmd/workshop/http.go`: Defines the `http` subcommand and `runHTTP()` which calls `app.RunHTTP()`.
- `internal/app/app.go`: Core application package providing `RunTUI()`, `RunStdio()`, and `RunHTTP()` with a functional `Option` pattern.
- `cmd/workshop/root_test.go`: Tests for flag/viper bindings and config loading.
- `README.md`: User-facing documentation for commands and usage.

No pprof dependencies are needed (`net/http/pprof` is standard library).

## Architectural Blueprint

**Selected approach**: Keep pprof logic in the `cmd/workshop` package as a standalone sidecar helper, avoiding bloat in the `internal/app` package which is focused on wiring ore conduits.

- **Rejected alternative**: Add pprof as an `app.Option` in `internal/app`. Rejected because pprof is a debugging sidecar orthogonal to the core application lifecycle; adding it to `internal/app` would couple a tangential concern to the TUI/HTTP conduit wiring.
- **Rejected alternative**: Multiplex pprof onto the existing `http` conduit's server. Rejected because the user explicitly requested a separate listener for isolation.

**Structure**:
- `cmd/workshop/pprof.go`: New file containing `maybeStartPProf(ctx context.Context, enabled bool, addr string)` which spins up an `http.Server` in a goroutine and a shutdown goroutine tied to the context.
- `cmd/workshop/root.go`: Add `--pprof` and `--pprof.addr` persistent flags, bind to Viper. Call `maybeStartPProf` in `runRoot` before `app.RunTUI`/`app.RunStdio`.
- `cmd/workshop/http.go`: Call `maybeStartPProf` in `runHTTP` before `app.RunHTTP`.
- `cmd/workshop/pprof_test.go`: Tests for server startup, request serving, port-conflict fallback, and context-cancellation shutdown.

## Requirements

1. Add `--pprof` persistent boolean flag (default `false`) to the root command.
2. Add `--pprof.addr` persistent string flag (default `localhost:8715`) to the root command.
3. Bind both flags to Viper so they are overridable via env vars (`WORKSHOP_PPROF`, `WORKSHOP_PPROF_ADDR`) and config file.
4. When `--pprof` is enabled, start a background HTTP server on the configured address serving standard `net/http/pprof` routes.
5. The pprof server must run for the full lifetime of the application (both TUI/stdio mode and HTTP server mode).
6. If the configured port is already in use, log a warning and continue without blocking the main application.
7. On application shutdown (context cancellation), gracefully shut down the pprof server.
8. Add tests for the pprof helper covering startup, serving requests, port conflicts, and shutdown.
9. Document the new flags in `README.md`.

## Task Breakdown

### Task 1: Add pprof Persistent Flags
- **Goal**: Add `--pprof` and `--pprof.addr` persistent flags to the root Cobra command and bind them to Viper.
- **Dependencies**: None
- **Files Affected**: `cmd/workshop/root.go`
- **New Files**: None
- **Interfaces**: None
- **Validation**:
  - `go build ./cmd/workshop` succeeds.
  - `go run ./cmd/workshop --help` shows both `--pprof` and `--pprof.addr` flags.
  - `go test ./cmd/workshop -run TestSetupViper` passes (or new test confirms env binding works).
- **Details**: In the `init()` function of `cmd/workshop/root.go`, add:
  ```go
  rootCmd.PersistentFlags().Bool("pprof", false, "Enable the pprof debug server")
  rootCmd.PersistentFlags().String("pprof.addr", "localhost:8715", "TCP address for the pprof server")
  ```
  Bind them with `viper.BindPFlags` alongside the existing flags. The existing `setupViper` function already configures the `WORKSHOP_` env prefix and `.` → `_` replacer, so env vars `WORKSHOP_PPROF` and `WORKSHOP_PPROF_ADDR` will work automatically.

### Task 2: Create pprof Server Helper
- **Goal**: Implement a reusable pprof server launcher that starts in a goroutine and shuts down on context cancellation.
- **Dependencies**: None (parallelizable with Task 1)
- **Files Affected**: None
- **New Files**: `cmd/workshop/pprof.go`
- **Interfaces**:
  ```go
  func maybeStartPProf(ctx context.Context, enabled bool, addr string)
  ```
- **Validation**:
  - `go build ./cmd/workshop` succeeds.
  - `go vet ./cmd/workshop` clean.
- **Details**: Create `cmd/workshop/pprof.go`:
  - Import `_ "net/http/pprof"` to register handlers on `http.DefaultServeMux`.
  - `maybeStartPProf` should return immediately if `enabled` is `false`.
  - Create an `http.Server` with the given `addr` (nil handler uses `DefaultServeMux` which now has pprof routes).
  - Start `srv.ListenAndServe()` in a goroutine. If it errors (e.g., port in use), log a warning via `slog.Warn` and return—do not block or fail the caller.
  - In a second goroutine, wait on `<-ctx.Done()` then call `srv.Shutdown(context.Background())` (or a short timeout context) to release the port on exit.

### Task 3: Wire pprof into Application Entry Points
- **Goal**: Call `maybeStartPProf` from both `runRoot` and `runHTTP` before starting the main application.
- **Dependencies**: Task 1, Task 2
- **Files Affected**: `cmd/workshop/root.go`, `cmd/workshop/http.go`
- **New Files**: None
- **Interfaces**: None
- **Validation**:
  - `go build ./cmd/workshop` succeeds.
  - `go test ./cmd/workshop` passes.
  - Manual smoke test: `go run ./cmd/workshop --pprof &` and verify `curl http://localhost:8715/debug/pprof/` returns the pprof index.
- **Details**: 
  - In `runRoot`, after building the context and before calling `app.RunTUI` or `app.RunStdio`, add:
    ```go
    maybeStartPProf(ctx, viper.GetBool("pprof"), viper.GetString("pprof.addr"))
    ```
  - In `runHTTP`, after building the context and before calling `app.RunHTTP`, add the same call.
  - Ensure the call happens before the potentially blocking TUI/HTTP server starts, so pprof is available immediately.

### Task 4: Add pprof Tests
- **Goal**: Add unit tests for the pprof helper covering startup, request serving, port conflict handling, and shutdown.
- **Dependencies**: Task 2, Task 3
- **Files Affected**: None
- **New Files**: `cmd/workshop/pprof_test.go`
- **Interfaces**: None
- **Validation**:
  - `go test ./cmd/workshop -run TestPProf` passes.
  - All tests in `./cmd/workshop` pass (`go test ./cmd/workshop/...`).
- **Details**: Create `cmd/workshop/pprof_test.go`:
  - `TestPProf_Disabled`: Call `maybeStartPProf` with `enabled=false` and verify no server starts (e.g., no goroutine leaks, port not bound).
  - `TestPProf_StartAndServe`: Start on `127.0.0.1:0` (random available port), extract the actual address, and `http.Get` `/debug/pprof/` expecting HTTP 200.
  - `TestPProf_PortInUse`: Start a dummy listener on a fixed port, then start pprof on the same port with `enabled=true`, and verify the main goroutine is not blocked (e.g., using a done channel or checking that a subsequent operation succeeds).
  - `TestPProf_ShutdownOnCancel`: Start pprof, then cancel the parent context, and verify the server stops accepting connections.
  - Use `net.Listen("tcp", "127.0.0.1:0")` to get an available port for conflict tests, and close it before starting pprof.

### Task 5: Document pprof Feature in README
- **Goal**: Add user-facing documentation for the new `--pprof` and `--pprof.addr` flags.
- **Dependencies**: Task 3
- **Files Affected**: `README.md`
- **New Files**: None
- **Interfaces**: None
- **Validation**: README renders correctly (no broken markdown).
- **Details**: Add a new subsection under `## Usage` (or under a new `## Debugging` section) in `README.md`:
  - Show `--pprof` example for both TUI and HTTP modes.
  - Show `--pprof.addr` example for a custom port.
  - Mention environment variable equivalents (`WORKSHOP_PPROF=true`, `WORKSHOP_PPROF_ADDR=...`).
  - Briefly explain that `/debug/pprof/` is available when enabled.

## Dependency Graph

- Task 1 || Task 2 (parallel)
- Task 1 → Task 3
- Task 2 → Task 3
- Task 3 → Task 4
- Task 3 → Task 5 (parallel with Task 4)

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| Viper key collision (`pprof` bool vs `pprof.addr` nested key) in YAML config | Medium | Medium | Document that config-file users should set the boolean via env/flag, or test Viper's actual behavior with dotted keys in YAML. If collision occurs, implement a manual key binding (e.g., bind `--pprof` to `pprof.enabled` internally while keeping the flag name). |
| Port-conflict test is flaky in CI (port already taken by another test/process) | Low | Low | Use `127.0.0.1:0` for the pprof server in tests to bind an ephemeral port; use a separate `net.Listen` for the dummy "in-use" port, then close and immediately reuse it. |
| pprof goroutine leak if context is never cancelled in tests | Low | Low | Always cancel the test context in `t.Cleanup` and use `t.Parallel` cautiously. |

## Validation Criteria

- [ ] `go run ./cmd/workshop --help` lists `--pprof` and `--pprof.addr` flags.
- [ ] `WORKSHOP_PPROF=true go run ./cmd/workshop` starts the TUI and makes `curl http://localhost:8715/debug/pprof/` return the pprof index page.
- [ ] `go run ./cmd/workshop http --pprof` starts the HTTP server and makes pprof available on the same default address.
- [ ] `go run ./cmd/workshop --pprof --pprof.addr localhost:9999` makes pprof available on port 9999.
- [ ] Starting two instances with `--pprof` (same port) results in the second logging a warning and continuing to run its main application.
- [ ] `go test ./cmd/workshop -run TestPProf` passes.
- [ ] `go test ./cmd/workshop/...` passes.
- [ ] README.md contains pprof usage documentation.
