# Plan (as-built): Upgrade ore Dependencies to Latest

## Objective
Bump all `andrewhowdencom/ore` Go module dependencies (and the `go.opentelemetry.io/otel` direct deps) to their latest published versions, fix the resulting compile and test breakage caused by ore's session-shaped conduit contract, and record the actual outcome for future reference.

This is the as-built record of the work planned in `.plans/bump-everything-to-latest.md`. The prescriptive version lives next to this one.

## Context
**What was actually broken**

Workshop fails to compile because `models.Spec.CacheControl` is undefined:

```
internal/app/app.go:1167:9: spec.CacheControl undefined (type models.Spec has no field or method CacheControl)
internal/app/app.go:1167:32: undefined: models.CacheControl
```

This was introduced by commit `a761379 Wire cache-control through workshop` (PR #131), which was authored against `github.com/andrewhowdencom/ore v1.3.0`'s public API but landed without bumping the dependency. The pinned version (v1.2.3) does not yet have the `CacheControl` field on `models.Spec`.

**Architectural shift uncovered by the bump**

In addition to the cache_control gap, the bump revealed ore v1.x's transition to a "session-shaped" conduit contract (documented in `x/conduit/doc.go` of the ore module):

- `tui.New(sess *session.Session, opts...)` — was `tui.New(mgr *junk.Manager, ...)`
- `http.New(backend httpc.Backend, opts...)` — was `http.New(mgr *junk.Manager, ...)`

The stdio, slack, and telegram conduits still follow the legacy `*junk.Manager` pattern. Workshop uses `*junk.Manager` as its central orchestrator (worker, ledger thread, processor, slash registry all live on the manager). Adapting tui/http required bridging `*junk.Manager` onto `httpc.Backend`.

## Architectural Blueprint
Mechanical dependency upgrade with reactive source-code fixes. No architectural redesign. `*junk.Manager` remains the central orchestrator; tui/http are now connected to it via a thin `httpc.Backend` adapter.

**Tradeoffs accepted during execution**

- The `junk.WithInterceptor(slashReg)` option was removed by ore. Slash commands must now be invoked explicitly via `slashReg.Intercept(ctx, event, sess, emitter)` before each `Submit`. Workshop's conduits do not currently do this; slash commands (`/role`, `/compact`, `/thinking`, `/analytics`, `/name`) will not fire until the engine-based rewrite lands. Tracked as a follow-up.
- `tui.WithThreadID` was removed (session identity flows through the session itself). Workshop's TUI now obtains a session via `junkBackend.CreateSession(ctx, cfg.threadID)`.
- The TUI's emitted events (`tui.Events()`) are not yet pumped into the manager's worker. The TUI is wired compile-clean but runtime-inert until the engine + `session.Registry` work completes. Tracked as a follow-up.

## Actual Versions Landed

| Module | Was | Now |
|---|---|---|
| `github.com/andrewhowdencom/ore` | v1.2.3 | **v1.3.0** |
| `.../ore/x/wire/anthropic` | v0.2.2 | **v0.2.3** |
| `.../ore/x/provider/anthropic` | v0.2.5 | **v0.2.6** |
| `.../ore/x/conduit/http` | v0.8.2 | **v0.9.0** |
| `.../ore/x/conduit/tui` | v0.12.9 | **v0.12.10** |
| `go.opentelemetry.io/otel` | v1.44.0 | **v1.45.0** |
| `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp` | v1.44.0 | **v1.45.0** |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace` | v1.44.0 | **v1.45.0** |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp` | v1.44.0 | **v1.45.0** |
| `go.opentelemetry.io/otel/metric` | v1.44.0 | **v1.45.0** |
| `go.opentelemetry.io/otel/sdk` | v1.44.0 | **v1.45.0** |
| `go.opentelemetry.io/otel/sdk/metric` | v1.44.0 | **v1.45.0** |
| `go.opentelemetry.io/otel/trace` | v1.44.0 | **v1.45.0** |

All other `.../ore/...` modules were already at their latest version (or no newer release was available). No non-ore deps beyond otel needed bumping.

## Work Actually Done

### Task 1: Bump every direct dep to latest and normalize the module graph
- Edited `go.mod` to bump the 13 modules in the table above to their latest published versions.
- Ran `go mod tidy`. The bump transitively pulled in updates to `go.opentelemetry.io/proto/otlp` (v1.10.0 → v1.11.0), `google.golang.org/genproto/...`, `google.golang.org/grpc` (v1.82.1 → v1.83.0), `go-logr/logr` (v1.4.3 → v1.4.4), and `ore/x/conduit` (v0.1.5 → v0.2.0). All accepted.
- **Result**: `go build ./cmd/workshop` failed because the cache_control call site still references `models.Spec.CacheControl` (now defined in v1.3.0) but the **tui** call site used `tui.WithThreadID` (removed) and `tui.New(mgr, ...)` where the new API wants `tui.New(sess, ...)`. The **http** call site passed `mgr` where the new API wants a `Backend`. The **junk** call site passed `junk.WithInterceptor(slashReg)` which no longer exists.

### Task 2: Restore compile-clean state
- New file `internal/app/backend.go` introduces `junkBackend`, an `httpc.Backend` adapter that bridges `*junk.Manager` onto the new session-shaped surface. It implements `CreateSession`, `GetSession`, `Submit`, `ListThreads`, `DeleteSession`. Per-session `*junk.Stream` continues to do inference; the `*session.Session` is a thin wrapper whose `loop.Step` is independent of the stream's worker.
- `internal/app/app.go`:
  - `RunTUI`: removed `tui.WithThreadID(cfg.threadID)`; created the session via `junkBackend.CreateSession(ctx, cfg.threadID)`.
  - `RunHTTP`: changed `httpc.New(mgr, ...)` to `httpc.New(newJunkBackend(mgr), ...)`.
  - `buildManager`: removed `junk.WithInterceptor(slashReg)`. `slash.NewRegistry()` and its `Bind` calls remain, awaiting the engine rewrite.
- **Result**: `go build ./...` passes; `go vet ./...` passes; `go mod verify` passes.

### Task 3: Restore test-clean state
- Discovered and fixed a config-loading bug that the bump exposed: `loadProvidersConfig` in `cmd/workshop/root.go` did not read `providers.<name>.cache-control` into `ProviderConfig.CacheControl`. The wire was implicit — the field existed, the YAML key was bound, the spec-side plumbing consumed it, but the loader struct literal never assigned it. Test `TestLoadProvidersConfig_CacheControl` failed with the field empty. One-line fix.
- **Result**: `go test -race ./...` passes for `internal/app`, `internal/role`, `internal/subagent`, `internal/telemetry`, and `cmd/workshop`.

### Tasks not in the prescriptive plan but performed
- **Task 3.5 (config loader bugfix)**: as above.

### Tasks deferred
- **Slash command interceptor wiring**: ore removed `junk.WithInterceptor`; `slashReg.Intercept(ctx, event, sess, emitter)` must now be invoked by the application before each `Submit`. Workshop does not yet have an engine-style event pipeline. Slash commands (`/role`, `/compact`, `/thinking`, `/analytics`, `/name`) are non-functional in this build.
- **TUI / HTTP engine pump**: tui emits `session.Event` on `Events()`. Workshop does not currently submit these to an inference engine; TUI submissions do not reach the manager's worker. Same follow-up as above; needs `engine.Engine` + `session.Registry`.
- **Migration of `*junk.Manager` to engine + session.Registry**: the long-term recommended architecture per `x/conduit/doc.go`. Out of scope for this migration.

## Files Actually Changed

| File | Why |
|---|---|
| `go.mod` | Bumped 13 direct deps to latest. |
| `go.sum` | Reconciled by `go mod tidy`. |
| `internal/app/app.go` | `tui.New(sess, ...)` (instead of `tui.New(mgr, ...)`); `httpc.New(newJunkBackend(mgr), ...)`; removed `tui.WithThreadID`; removed `junk.WithInterceptor`. |
| `internal/app/backend.go` (new) | `junkBackend` implements `httpc.Backend`. |
| `cmd/workshop/root.go` | `loadProvidersConfig` now reads `cache-control` into `ProviderConfig.CacheControl`. |
| `.plans/bump-everything-to-latest.md` (new) | The prescriptive plan for this work. |
| `.plans/upgrade-ore-dependencies.md` (this file) | As-built record. |

## Dependency Graph
- Task 1 → Task 2 → Task 3 → Task 3.5 (bugfix surfaced during Task 3). All executed sequentially in a single plan; commits group Tasks 1+2 and Task 3 separately.

## Risks & Mitigations
| Risk | Impact | Likelihood | Actual outcome |
|---|---|---|---|
| Non-ore bumps introduce unrelated API drift | Medium | Low | otel v1.44.0 → v1.45.0 was a clean patch bump; no source changes needed. |
| `go mod tidy` adds/removes indirect deps unexpectedly | Low | Low | True in execution: proto/otlp, genproto, grpc, logr, and ore/x/conduit all moved. Accepted. |
| Local Go toolchain older than `go 1.26.2` | Medium | Low | Local toolchain is `1.26.2`. Match. |
| TUI / HTTP conduits need a Backend adapter beyond simple signature swap | Medium | High | **Realized**: yes, required the new `backend.go` file plus a `session.Session` shim for TUI. |
| `junk.WithInterceptor` removal breaks slash command handling | Medium | High | **Realized**: slash commands are no longer auto-processed. Documented as follow-up. |
| Cache_control config loader bug surfaces | Low | Medium | **Realized**: a one-line fix in `cmd/workshop/root.go`. |

## Validation Criteria (post-execution)
- [x] `go list -m all` shows the latest version of every previously-direct ore and otel dep.
- [x] `go mod tidy` completes without error.
- [x] `go build ./...` passes with zero errors.
- [x] `go vet ./...` passes.
- [x] `go test -race ./...` passes (all packages green).
- [ ] **Open**: `task validate` (lint + test + build) — not yet run; `golangci-lint` may surface issues unrelated to the bump.
- [ ] **Open**: Slash commands invoke interceptors. Tracked as follow-up.
- [ ] **Open**: TUI events reach the manager's worker via engine. Tracked as follow-up.