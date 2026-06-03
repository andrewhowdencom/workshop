# Plan: Wire OpenTelemetry Tracing

## Objective
Replace the hardcoded noop tracer in `internal/app/app.go` with a configurable OpenTelemetry tracing pipeline that exports spans via OTLP/HTTP to a user-configured endpoint. Wire the tracer through all ore framework components (cognitive, loop, provider, conduits, tool handler) so spans flow end-to-end. When no endpoint is configured, the application falls back to a noop tracer.

## Context

### Repository Topology
- **Language**: Go 1.26.2, module `github.com/andrewhowdencom/workshop`
- **CLI framework**: cobra + viper (config in `config.yaml`, env vars with `WORKSHOP_` prefix)
- **App framework**: `github.com/andrewhowdencom/ore` (replaced locally at `../ore`)
- **Build tool**: Taskfile (`task build`, `task test`, `task lint`, `task validate`)
- **Entry point**: `cmd/workshop/main.go` → cobra root command (`cmd/workshop/root.go`, `cmd/workshop/http.go`, etc.)
- **Core app logic**: `internal/app/app.go` wires ore conduits (TUI, HTTP, stdio), provider, loop, guardrails, tools, and session manager

### Current State
- The build currently passes because `cognitive.NewTurnProcessor(noop.NewTracerProvider().Tracer(""))` was added to fix an earlier compilation error after `ore` introduced required tracing.
- The noop tracer is **hardcoded** — there is no way to configure a real OTLP endpoint.
- No other ore components (loop, provider, conduits, tool handler) have tracers wired.
- OpenTelemetry dependencies exist only as **indirect** in `go.mod` (`go.opentelemetry.io/otel v1.44.0`, `go.opentelemetry.io/otel/trace v1.44.0`).
- `config.yaml` has no tracing section.

### ore Framework Tracing Support (Verified)
All of the following `WithTracer` options and `NewTurnProcessor` signature were verified in the local `../ore` source:

| Component | API |
|---|---|
| `cognitive.NewTurnProcessor(tracer)` | **Required** `trace.Tracer` argument |
| `loop.WithTracer(tracer)` | Optional `loop.Option` |
| `openai.WithTracer(tracer)` | Optional `openai.Option` |
| `tui.WithTracer(tracer)` | Optional `tui.Option` |
| `httpc.WithTracer(tracer)` | Optional `httpc.Option` |
| `stdioc.WithTracer(tracer)` | Optional `stdioc.Option` |
| `xtool.WithTracer(tracer)` | Optional `xtool.HandlerOption` |

### Configuration Pattern (Observed)
- `cmd/workshop/root.go` registers cobra persistent flags, binds them to viper, then reads values via `viper.GetString` / `viper.GetBool` / etc.
- `cmd/workshop/config.go` `buildConfigMap()` produces the YAML structure written by `workshop config init`.
- `internal/app/app.go` exposes functional options (`WithStoreDir`, `WithProvider`, etc.) and a private `config` struct.

## Architectural Blueprint

### Selected Architecture
**Path A: CLI-layer tracer initialization → `app.Option` → `internal/app` wiring**

This is the only viable path because:
1. The codebase already reads all configuration in `cmd/workshop` and passes it into `internal/app` via `app.Option` functional options.
2. `internal/app` is a pure application logic package with no access to viper.
3. Creating a tracer requires viper configuration (`tracing.endpoint`), so initialization must happen in the CLI layer.

**Rejected paths:**
- *Path B (initialize tracer inside `internal/app`)*: Would break the clean separation between CLI/config and application logic. `internal/app` would need to read environment variables directly, which contradicts the existing functional-options pattern.
- *Path C (lazy global tracer)*: Anti-pattern, hard to test, and contradicts the dependency-injection style already used for providers and stores.

### Component Diagram
```
cmd/workshop/root.go          internal/telemetry/telemetry.go
├─ reads viper config ───────►├─ NewTracer(endpoint)
│                              │   ├─ empty → noop tracer
│                              │   └─ set   → OTLP/HTTP exporter
│                              │        → batch processor
│                              │        → TracerProvider
│                              │        → trace.Tracer
│                              └─ shutdown func
│
▼
app.WithTracer(tracer)        internal/app/app.go
├─ config.tracer = tracer    ├─ buildManager(cfg)
│                              │   ├─ newProvider(cfg.provider, tracer)
│                              │   │   └─ openai.WithTracer(tracer)
│                              │   └─ stepFactory
│                              │       ├─ loop.WithTracer(tracer)
│                              │       ├─ loop.WithHandlers(
│                              │       │      xtool.NewHandler(..., xtool.WithTracer(tracer)), ...)
│                              │       └─ cognitive.NewTurnProcessor(tracer)
│                              ├─ RunTUI → tui.New(..., tui.WithTracer(tracer))
│                              ├─ RunHTTP → httpc.New(..., httpc.WithTracer(tracer))
│                              └─ RunStdio → stdioc.New(..., stdioc.WithTracer(tracer))
│
▼
OTLP/HTTP endpoint (config.yaml)
```

### Key Design Decisions
1. **`internal/telemetry` package**: Encapsulates OTel SDK boilerplate (exporter, processor, provider, resource). Keeps `cmd/workshop` clean and makes the tracer setup independently testable.
2. **Nil-safe fallback**: `buildManager` checks `cfg.tracer == nil` and falls back to a noop tracer. This preserves backward compatibility for unit tests that construct `&config{}` directly without `WithTracer`.
3. **Graceful shutdown**: The `internal/telemetry.NewTracer` function returns a `shutdown func(context.Context) error`. Each `run*` function in `cmd/workshop` defers this shutdown with a fresh 5-second timeout context, ensuring spans are flushed before the process exits.
4. **Single config key**: Only `tracing.endpoint` (a full URL like `http://localhost:4318`) is exposed. TLS vs. insecure is determined by the URL scheme (`http://` vs. `https://`), keeping the surface area minimal.
5. **Service name**: Hardcoded to `"workshop"` in the OTel resource. This is sufficient for a single-service application.

## Requirements

1. [inferred] Add OTel SDK and OTLP/HTTP exporter dependencies to `go.mod`.
2. Add tracing configuration to `config.yaml` (`tracing.endpoint`).
3. Create `internal/telemetry` package that initializes a `trace.Tracer` from an endpoint URL, falling back to noop when the endpoint is empty.
4. Add `app.WithTracer(trace.Tracer) Option` and store it in `internal/app`'s `config` struct.
5. Wire the tracer through `buildManager` (cognitive, loop, xtool handler) and `newProvider` (openai provider).
6. Wire the tracer through all three conduits: TUI, HTTP, stdio.
7. Initialize the tracer in `cmd/workshop/root.go` and `cmd/workshop/http.go`, pass it via `app.WithTracer`, and defer shutdown.
8. Add tracing keys to `cmd/workshop/config.go` `buildConfigMap()` so `workshop config init` includes them.
9. Update tests to remain passing after signature changes.
10. Update `config.yaml` with an example tracing section.

## Task Breakdown

### Task 1: Add OTel SDK Dependencies
- **Goal**: Add `go.opentelemetry.io/otel/sdk`, `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp`, `go.opentelemetry.io/otel/sdk/resource`, and `go.opentelemetry.io/otel/semconv/v1.26.0` to `go.mod` and run `go mod tidy`.
- **Dependencies**: None.
- **Files Affected**: `go.mod`, `go.sum`.
- **New Files**: None.
- **Interfaces**: No new interfaces; resolves indirect OTel deps to direct.
- **Validation**:
  - `go mod tidy` exits 0.
  - `go build ./...` passes.
  - `go test ./...` passes.
- **Details**: Use `go get` for the new packages. The existing `go.opentelemetry.io/otel` and `go.opentelemetry.io/otel/trace` should become direct dependencies. Commit after `go mod tidy` succeeds.

### Task 2: Create `internal/telemetry` Package
- **Goal**: Implement a self-contained telemetry initialization package that produces a `trace.Tracer` and a shutdown function.
- **Dependencies**: Task 1.
- **Files Affected**: None (new package).
- **New Files**:
  - `internal/telemetry/telemetry.go`
  - `internal/telemetry/telemetry_test.go`
- **Interfaces**:
  ```go
  package telemetry

  import (
      "context"
      "go.opentelemetry.io/otel/trace"
  )

  // NewTracer creates a tracer. If endpoint is empty, returns a noop tracer
  // and a no-op shutdown function. Otherwise, creates an OTLP/HTTP exporter,
  // a batch span processor, a TracerProvider with resource attributes, and a
  // tracer scoped to "github.com/andrewhowdencom/workshop".
  func NewTracer(endpoint string) (trace.Tracer, func(context.Context) error, error)
  ```
- **Validation**:
  - `go test ./internal/telemetry/...` passes with table-driven tests covering:
    - Empty endpoint → noop tracer, nil error, no-op shutdown.
    - Invalid endpoint URL → returns error.
  - `go build ./...` passes.
  - `go test ./...` passes.
- **Details**:
  - The TracerProvider resource should set `service.name` to `"workshop"`.
  - Use `sdktrace.WithBatcher` for the span processor.
  - If `endpoint == ""`, return `noop.NewTracerProvider().Tracer("")` and `func(context.Context) error { return nil }`.
  - Keep the package small; no metrics or logs.

### Task 3: Add `WithTracer` Option and Wire Through Core Components
- **Goal**: Add `WithTracer` to `internal/app`, pass the tracer through `buildManager` and `newProvider`, and wire it into cognitive, loop, tool handler, and openai provider.
- **Dependencies**: Task 2.
- **Files Affected**:
  - `internal/app/app.go`
  - `internal/app/app_test.go`
- **New Files**: None.
- **Interfaces**:
  - `func WithTracer(tracer trace.Tracer) Option`
  - `newProvider` signature change: `func newProvider(pc ProviderConfig, tracer trace.Tracer) (provider.Provider, error)`
- **Validation**:
  - `go build ./...` passes.
  - `go test ./internal/app/...` passes.
- **Details**:
  1. Add `tracer trace.Tracer` field to the unexported `config` struct.
  2. Add `WithTracer` functional option.
  3. In `buildManager`, if `cfg.tracer == nil`, create a noop tracer locally (preserves backward compatibility for tests that construct `&config{}` without `WithTracer`). Otherwise use `cfg.tracer`.
  4. Pass the resolved tracer to `cognitive.NewTurnProcessor(tracer)`.
  5. Change `newProvider` signature to accept `tracer trace.Tracer` as the second argument. Wire `openai.WithTracer(tracer)` only when `tracer != nil` (belt-and-suspenders; ore already nil-checks, but explicit is safer).
  6. In the `stepFactory`, add `loop.WithTracer(tracer)` to the returned options.
  7. In the `stepFactory`, change `xtool.NewHandler(registry)` to `xtool.NewHandler(registry, xtool.WithTracer(tracer))`.
  8. Update the three `newProvider` test calls in `app_test.go` to pass `nil` as the second argument (they test validation logic, not tracing).

### Task 4: Wire Tracer Through Conduits
- **Goal**: Pass the tracer from `cfg.tracer` into the TUI, HTTP, and stdio conduit constructors.
- **Dependencies**: Task 3.
- **Files Affected**: `internal/app/app.go`.
- **New Files**: None.
- **Interfaces**: No new interfaces; uses existing `WithTracer` options from ore.
- **Validation**:
  - `go build ./...` passes.
  - `go test ./internal/app/...` passes.
- **Details**:
  1. In `RunTUI`, add `tui.WithTracer(cfg.tracer)` to the `tui.New` call.
  2. In `RunHTTP`, add `httpc.WithTracer(cfg.tracer)` to the `httpc.New` call.
  3. In `RunStdio`, add `stdioc.WithTracer(cfg.tracer)` to the `stdioc.New` call.
  4. No-op if `cfg.tracer` is nil; ore conduits handle nil tracers internally.

### Task 5: Add CLI Configuration, Initialization, and Shutdown
- **Goal**: Read `tracing.endpoint` from viper, initialize the tracer in `runRoot` and `runHTTP`, pass it to `app.WithTracer`, and ensure graceful shutdown.
- **Dependencies**: Task 3 (needs `WithTracer` to exist).
- **Files Affected**:
  - `cmd/workshop/root.go`
  - `cmd/workshop/http.go`
  - `cmd/workshop/config.go`
- **New Files**: None.
- **Interfaces**: No new exported interfaces.
- **Validation**:
  - `go build ./...` passes.
  - `go test ./cmd/workshop/...` passes.
- **Details**:
  1. In `cmd/workshop/root.go` `init()`:
     - Add `rootCmd.PersistentFlags().String("tracing.endpoint", "", "OpenTelemetry OTLP/HTTP endpoint URL (e.g. http://localhost:4318)")`.
     - Bind the flag to viper.
  2. In `cmd/workshop/root.go` `runRoot`:
     - Call `telemetry.NewTracer(viper.GetString("tracing.endpoint"))`.
     - If error, return it.
     - Append `app.WithTracer(tracer)` to the `opts` slice passed to `app.RunTUI` or `app.RunStdio`.
     - Defer a shutdown call with a 5-second timeout context (independent of the signal context, which may be cancelled):
       ```go
       defer func() {
           shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
           defer cancel()
           if err := shutdown(shutdownCtx); err != nil {
               slog.Warn("tracer shutdown failed", "error", err)
           }
       }()
       ```
  3. In `cmd/workshop/http.go` `runHTTP`:
     - Repeat the same tracer initialization, `app.WithTracer`, and deferred shutdown pattern.
  4. In `cmd/workshop/config.go` `buildConfigMap`:
     - Add `"tracing": map[string]interface{}{"endpoint": viper.GetString("tracing.endpoint")}` to the returned map.

### Task 6: Update Documentation and Example Config
- **Goal**: Add a tracing section to `config.yaml` and update the README if it has a configuration reference.
- **Dependencies**: Task 5.
- **Files Affected**:
  - `config.yaml`
  - `README.md`
- **New Files**: None.
- **Validation**: Manual review — `config.yaml` is valid YAML and the README accurately describes the new option.
- **Details**:
  1. In `config.yaml`, add an example (commented out by default):
     ```yaml
     tracing:
       endpoint: "http://localhost:4318"
     ```
  2. In `README.md`, locate the configuration reference section (if any) and add a row or paragraph for `tracing.endpoint`.
  3. If the README has an environment variable table, add `WORKSHOP_TRACING_ENDPOINT`.

## Dependency Graph

```
Task 1 (deps)
    │
    ▼
Task 2 (telemetry package)
    │
    ▼
Task 3 (core app wiring)
    │
    ├──────────────┐
    ▼              ▼
Task 4 (conduits)    Task 5 (CLI config + init)
    │                  │
    └────────┬─────────┘
             ▼
        Task 6 (docs)
```

- Task 1 → Task 2
- Task 2 → Task 3
- Task 3 → Task 4
- Task 3 → Task 5
- Task 4 → Task 6
- Task 5 → Task 6

Tasks 4 and 5 are **parallelizable** after Task 3 because they touch disjoint files (`internal/app/app.go` for Task 4, `cmd/workshop/*.go` for Task 5). However, since Task 4 is a small edit in a file already modified by Task 3, sequential execution is simpler and avoids merge conflicts.

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| OTel SDK version mismatch with existing `go.opentelemetry.io/otel v1.44.0` indirect dep | Medium (build/test failure) | Low | `go get` the latest compatible SDK version; if conflict, pin to the same minor version family and run `go mod tidy`. |
| `newProvider` signature change breaks more tests than identified | Low | Medium | The grep found 3 direct calls; if more exist, compiler will catch them. Fix as part of Task 3 validation. |
| Local `../ore` replacement is out of sync with published version | Low | Low | Build already passes against local `../ore`; the plan assumes this state is stable. If `../ore` changes mid-implementation, re-run `go build ./...` to detect drift. |
| OTel exporter connection errors on startup (e.g., unreachable endpoint) | Medium (app fails to start) | Low | `telemetry.NewTracer` should return an error only for malformed endpoint URLs, not for connection failures. Connection errors are handled asynchronously by the OTel SDK. Verify this in implementation. |
| `stdioc.WithTracer` signature differs from other conduits | Low | Low | Verified in ore source that `stdioc.WithTracer(tracer trace.Tracer) Option` exists. If it doesn't at implementation time, skip stdio wiring and note it in the commit message. |

## Validation Criteria

- [ ] `go build ./...` passes after every task.
- [ ] `go test ./...` passes after every task.
- [ ] `task validate` (lint + test + build) passes after Task 6.
- [ ] `workshop config init` produces a YAML file containing a `tracing.endpoint` key.
- [ ] When `tracing.endpoint` is empty, the app starts successfully and uses a noop tracer (no network calls).
- [ ] When `tracing.endpoint` is set to a valid OTLP/HTTP collector (e.g., `http://localhost:4318`), spans are exported and visible in the collector's backend (manual smoke test).
- [ ] The tracer shutdown function is called on SIGINT/SIGTERM, flushing pending spans before exit.
