# Plan: Kill `junk.*` and Migrate to Session-Native Architecture

## Objective

Workshop is migrating off the `junk.*` orchestrator onto the session-native architecture introduced by ore v1.x. The current TUI info bar is missing the static keys (`cwd`, `git_branch`, `thread_id`, `tui.pid`, `model`) because the post-bump TUI reads from `sess.metadata` while `defaultMeta` writes only to thread metadata. This is one symptom of a half-finished migration; the fix is to land the migration in full across eight staged commits on a single branch. The end state has zero `junk.*` references in production code, `internal/app/backend.go` deleted, `session.Registry` + `engine.Engine` as the orchestration primitives, and persistence flowing through `ledger.Repository` journal appends instead of `junk.Stream.Save` snapshots.

## Context

### The bug surface

The TUI's `WithStatusZones(statusZoneMapping)` declares (in `internal/app/app.go:267-281`):

```go
var statusZoneMapping = map[string]string{
    "phase":                   "lifecycle",
    "title":                   "lifecycle",
    "thread_id":               "context",
    "cwd":                     "context",
    "git_branch":              "context",
    "workshop.role":           "context",
    "workshop.thinking_level": "context",
    "tui.pid":                 "context",
    "model":                   "context",
    "sent":                    "lifecycle",
    "received":                "lifecycle",
    "total":                   "lifecycle",
    "thinking":                "lifecycle",
}
```

`defaultMeta` (`internal/app/app.go:1186-1203`) seeds five keys (`thread_id`, `cwd`, `git_branch`, `workshop.role`, `tui.pid`). `model` has never been seeded — it is referenced in the zone mapping since `690bb46 feat(tui): add zone grouping for status metadata rendering` but never added to the defaults map. `defaultSpec.Name` (built at `app.go:1022`) holds the model name (`cfg.defaultProviderConfig().Model`) and is in scope where `defaultMeta` is defined.

### Why the static keys don't appear in the info bar

`junk.WithDefaultMetadata(defaultMeta)` invokes `junk.Manager.applyDefaultMetadata(stream)` (`junk/manager.go:184-191`), which calls `stream.SetMetadata(k, v)` per key. This writes to `junk.Thread.Metadata` — the persistent store.

After `26ebace Bump all direct deps to latest; adapt to new tui/http API`, the TUI is constructed via `tui.New(tuiSess, ...)` (was `tui.New(mgr, ...)`). The TUI bootstraps its status from `sess.AllMetadata()` (`x/conduit/tui@v0.12.10/tui.go:313-318`):

```go
func statusFromSession(sess *session.Session) tea.Msg {
    if meta := sess.AllMetadata(); len(meta > 0) {
        return statusMsg{status: meta}
    }
    return nil
}
```

`session.New(...)` initializes `metadata: make(map[string]string)` (`session/session.go:48-53`). It does **not** copy thread metadata. So the keys `defaultMeta` writes are invisible to the bootstrap. The slash handlers' dual-write (e.g., `c.session.Thread().Meta().Set("workshop.role", name)` followed by `c.session.SetMetadata("workshop.role", name)` at `app.go:494-495`) accidentally papers over part of the gap — only `role` and `thinking_level` are dual-written, leaving the static keys stranded on the thread.

### The migration in flight

The recent commits show workshop already moving toward the session-native target:

- `26ebace Bump all direct deps to latest; adapt to new tui/http API` — `tui.New` and `httpc.New` switched from `*junk.Manager` to session-shaped APIs.
- `08adcbb Refactor slash handlers to session-only API` — slash handlers (`roleCommand`, `thinkingCommand`, `compactCommand`, `analyticsCommand`) migrated from `SetStream(*junk.Stream)` to `SetSession(*session.Session)`. `tuiEngineFactory` now binds handlers via `SetSession` on every `Build`.
- `f3659a0` / `28a867f Wire TUI Events() through an engine.Engine + per-turn agent` — the TUI now uses `engine.New(session.Registry, agent.Factory)` instead of `junk.Manager`'s internal worker.

What remains: `junk.NewManager` is still the orchestrator (owns the worker, the ledger thread, the persistence pump, the slash-interceptor wiring). `internal/app/backend.go` is a 189-line adapter that bridges `*junk.Manager` onto the new session-shaped conduit APIs. `runTUIEngine` still calls `junk.Stream.Save()` after every turn for persistence. ~145 `junk.*` references remain across `internal/app/*.go` and `cmd/workshop/thread_test.go`.

### Project conventions (from `Taskfile.yml` and skills)

- `task build` → `go build ./cmd/workshop`
- `task test` → `go test -race ./...`
- `task lint` → `golangci-lint run ./...`
- `task validate` → lint + test + build (the pre-commit gate)
- Commit messages follow `Design:` / `Tradeoffs:` / `Justification:` sections with a `Co-authored-by:` trailer (`${MODEL}@${HARNESS}.agent`).

### Constraints / non-goals

- No upstream ore changes — the migration is workshop-side only.
- No behavioral changes to slash commands, persistence semantics, or model spec resolution.
- The session-native architecture the migration converges on is documented in `x/conduit/doc.go` (referenced from `backend.go:41-42`) and uses `engine.New(session.Registry, agent.Factory)`.

## Architectural Blueprint

The migration lands as **eight sequential commits on a single branch**. Each commits leaves the build green and is independently committable. Commits 1–3 are tightly coupled (info-bar fix + seeder + TUI session path); commits 4–5 are the persistence and slash-handler simplification; commits 6–7 are the stdio/HTTP migration and test rewrite; commit 8 is cleanup.

```
1: info bar fix (move defaultMeta to session seeder; include model)
 └─► 2: thread metadata dual-write for persistence-bearing keys
      └─► 3: delete junkBackend for TUI; RunTUI uses session.Registry directly
           ├─► 4: per-turn save hook (Repository.SaveTurn); drop stream.Save
           │    └─► 5: slash handlers + compact: drop stream field, session-only writes
           │         └─► 6: stdio + HTTP migrate to session-native; junkBackend deleted
           │              └─► 7: test fixtures: session-native
           │                   └─► 8: final sweep (no-op if 2-7 complete)
           └────────────► 6 (parallel design review)
```

**Why commits not PRs:** keeps the diff linear and reviewable in `git log`; avoids merge-order risk between PRs; the build remains green at every step.

### Tree-of-Thought deliberation on the per-turn save hook (Commit 4)

- **Path A — Snapshot save per turn via a new `Session.Save()` method.** Workshop calls a hypothetical `sess.Save()` after each `TurnCompleteEvent`. Rejected: requires an upstream API addition; we don't want to depend on ore accepting a workshop-driven change.
- **Path B — Journal append via `ledger.Repository` directly.** Workshop subscribes to `TurnCompleteEvent` on the session and calls `Repository.SaveTurn(ctx, threadID, &turn)` + `UpdateThreadTip(ctx, threadID, tip)` after each turn. The journal is append-only and atomic within a single process (POSIX `O_APPEND`); the existing `junk.Stream.Save` is replaced by a richer journal that preserves branching and control states.
- **Path C — Debounced snapshot save (e.g., every N turns or every M seconds).** Rejected: complicates failure semantics (crash mid-window loses the last batch); the journal model is already cheap.

**Selected: Path B.** Rationale: zero upstream coordination; the `ledger.Repository` journal primitives already exist and are richer than the snapshot; per-turn cost is one append per turn (one syscall). Failures are logged via `slog.Warn` matching the existing `runTUIEngine:404-406` pattern.

### Tree-of-Thought deliberation on test-rewrite strategy (Commit 7)

- **Path A — Keep `junk.NewMemoryStore` as a test-only shim** with the production code path-free. Rejected: leaves junk in the dependency graph; future "delete junk entirely" goal isn't fully met; the shim requires periodic upstream compatibility work.
- **Path B — Convert all fixtures to `session.NewInMemoryRegistry` + minimal `ledger.Repository` (e.g., `ledger.NewMemoryRepository`).** Mechanical but large: ~46 references in `app_test.go`, ~7 in `tui_engine_test.go`, ~30+ in `cmd/workshop/thread_test.go`.

**Selected: Path B.** Rationale: completes the "kill junk" goal; `ledger.MemoryRepository` is the natural session-native test fixture; the assertions on slash-handler behavior mostly survive unchanged since the surface (`sess.GetMetadata`, `sess.SetMetadata`, `sess.Thread().Meta()`) is preserved across the migration.

## Responsibility Boundaries

| Package | Stated Responsibility | Plan's Relationship |
|---|---|---|
| `github.com/andrewhowdencom/ore/junk` | "Package junk holds session-orchestration primitives that are pending extraction into better-defined packages. **The intent is to drive the surface area of this package to zero over time.**" (`junk/doc.go`) | `Respects` — aligns with the package's stated goal of driving its surface to zero. **Deletes from workshop's call sites by Task 8.** |
| `github.com/andrewhowdencom/ore/session` | "Session is the per-conversation primitive. It owns the identity, the ledger thread, the conduit-mapping metadata, and the long-lived loop.Step used for subscriber fanout." (`session/session.go:9-19`) | `Respects` — becomes the canonical store for live metadata; constructed directly via `session.New` by Task 3. |
| `github.com/andrewhowdencom/ore/session.Registry` | "Registry indexes active `*Session` values by their ID. It is the process-wide lookup that the engine and conduits share. ... In the new architecture the registry is the single source of truth for active sessions." (`session/registry.go:17-31`) | `Respects` — replaces `junk.Manager`'s session map. |
| `github.com/andrewhowdencom/ore/engine` | "Engine is constructed with a `session.Registry` (so it can resolve session IDs to `*session.Session`) and an `agent.Factory` (so it can construct an agent from current session metadata at execution time). The two public operations are `Submit` and `Close`." (`engine/engine.go:14-30`) | `Respects` — already in use at `tui_engine.go:311`; the stdio and HTTP paths adopt it in Task 6. |
| `github.com/andrewhowdencom/ore/ledger` | Tree-backed ledger (`Thread`) plus `Repository` interface (`SaveTurn`, `UpdateThreadTip`, `UpdateTurnControl`, `UpdateTurnParent`, `HydrateThread`) — the journal persistence layer. `Thread` "is held by the Session that owns the Thread and used as a key into the journal repository." (`ledger/thread.go:1-15`) | `Respects` — replaces `junk.Stream.Save` snapshots with journal appends (Task 4). |
| `github.com/andrewhowdencom/ore/x/conduit/tui` | "TUI conduit that reads turns, metadata, and Subscribe events from a `*session.Session`." (`x/conduit/tui@v0.12.10/tui.go:220-228`) | `Respects` — no upstream changes. |
| `internal/app/backend.go` | "Backend adapter for the ore v1.3 session-based conduit API" (`backend.go:1`) | `Respects` during the migration; **deleted** by Task 6 once TUI and HTTP have native session paths. |
| `internal/app/app.go` | Workshop's wire layer | `Respects` — modified incrementally across all eight tasks. |
| `internal/app/tui_engine.go` | TUI engine pump and per-session agent factory | `Respects` — modified in Tasks 3, 4, 5. |
| `internal/app/app_test.go`, `internal/app/tui_engine_test.go`, `cmd/workshop/thread_test.go` | Test scaffolding using `junk.NewMemoryStore` / `junk.NewJSONStore` | `Respects` — fixtures rewritten in Task 7. |

(Builder must add rows for any package whose stated responsibility turns out to contradict the migration direction during implementation.)

## Requirements

1. After Commit 1, the TUI's status bar MUST display `cwd`, `git_branch`, `thread_id`, `tui.pid`, and `model` from session creation, without any slash command invocation. *(Explicit — the original bug.)*
2. After Commit 1, `model` MUST read from `defaultSpec.Name` (the configured provider's model), not from any session-time metadata. *(Inferred — only one source exists at construction.)*
3. After Commit 3, `internal/app/tui_engine.go`'s session lookup MUST go through `session.Registry.Get(...)`, not `junk.Manager.Get(...)`. *(Explicit.)*
4. After Commit 4, the per-turn save MUST use `ledger.Repository.SaveTurn` / `UpdateThreadTip`, not `junk.Stream.Save`. *(Explicit.)*
5. After Commit 5, no slash handler (`roleCommand`, `thinkingCommand`, `compactCommand`, `analyticsCommand`) MAY hold a `*junk.Stream` field. *(Explicit — the field's purpose disappears with junk.)*
6. After Commit 6, `internal/app/backend.go` MUST be deleted and no `junk.NewManager` call site MUST remain in production code. *(Explicit.)*
7. After Commit 7, `go test -race ./...` MUST pass with no `junk.*` import in any test file (other than `cmd/workshop/thread_test.go`'s legacy listing test, which the builder may either rewrite or leave as a final cleanup). *(Inferred — the goal is "no junk left"; if any remain, the builder flags it.)*
8. Every commit MUST leave `task validate` clean. *(Inferred from the `git` skill.)*
9. Commit messages MUST follow the `Design:` / `Tradeoffs:` / `Justification:` format with a `Co-authored-by:` trailer. *(Explicit — convention.)*
10. **Out of scope:** upstream ore changes; new features; behavioral changes to slash commands, persistence semantics, or model spec resolution; renames outside the migration path.

## Task Breakdown

### Task 1: Seed session metadata at session creation (info bar fix)

- **Goal**: Close the info bar bug — `cwd`, `git_branch`, `thread_id`, `tui.pid`, and `model` appear in the TUI status bar from session creation, before any user interaction.
- **Dependencies**: None.
- **Files Affected**: `internal/app/app.go`, `internal/app/backend.go`.
- **New Files**: None (or `internal/app/seeder.go` if the helper exceeds ~30 lines).
- **Interfaces**: A new helper, called once at the seam where junk stream → session:
  ```go
  // seedSession writes the static metadata into the session's metadata
  // map (so the TUI's statusFromSession picks it up via sess.AllMetadata())
  // and emits PropertiesEvents for each key. The session is the canonical
  // store for live metadata after this call; the thread metadata is
  // updated separately if persistence is required (Commit 2).
  func seedSession(sess *session.Session, model string) error
  ```
- **Validation**:
  - `go build ./...` succeeds.
  - `go test -race ./...` passes (existing tests unchanged; this commit only adds).
  - `task lint` clean.
  - New unit test: a `*session.Session` is constructed, `seedSession(sess, "test-model")` is called, then `sess.AllMetadata()` MUST contain exactly the five keys with the expected content. (`model` is `"test-model"`; `thread_id` is `sess.ID()`; `cwd`, `git_branch`, `tui.pid` are populated from the workshop's `cfg`.)
  - Manual smoke: with `workshop tui` running, the info bar shows the five keys immediately.
- **Details**:
  - Read `cfg.cwd`, `cfg.gitBranch` (already computed at `app.go:1175-1184`); `cfg.tui.pid` from `os.Getpid()`.
  - Read `defaultSpec.Name` for the model. `defaultSpec` is in scope at `app.go:1022`.
  - Move the seeder's definition next to (or inline in) `buildManager` so it has access to `cfg` and `defaultSpec` without parameter sprawl.
  - Call the seeder from `junkBackend.CreateSession` and `junkBackend.GetSession` (the two seam methods). Both currently call `sessionFromStream(stream)`; the seeder follows the conversion.
  - Commit message MUST cite the bug as the trigger and note that `model` was previously missing from `defaultMeta` (since `690bb46`).

### Task 2: Also persist static keys to thread metadata

- **Goal**: Static keys that should survive across TUI restarts are written to thread metadata, in addition to the session seed from Commit 1.
- **Dependencies**: Task 1.
- **Files Affected**: `internal/app/app.go`, `internal/app/backend.go`.
- **New Files**: None.
- **Interfaces**: `seedSession` gains a second write to `sess.Thread().Meta().Set(...)` for the same five keys (so the thread's persistent metadata has them too).
- **Validation**:
  - `go test -race ./...` passes.
  - New test asserts `sess.Thread().Meta().Get("cwd")` (and the other four) return the seeded values after `seedSession` runs.
  - The thread metadata MUST match the session metadata after seeding.
- **Details**:
  - The thread write is unconditional for `cwd`, `git_branch`, `thread_id`, `tui.pid`, `model` — they're computed at startup, so persistence is cheap and they don't churn.
  - This commit leaves `junk.WithDefaultMetadata(defaultMeta)` in place but its output becomes secondary — the session seed is authoritative for live metadata; the thread metadata is the persistent copy.
  - Commit 6 removes `junk.WithDefaultMetadata` entirely when stdio and HTTP migrate.

### Task 3: `RunTUI` creates sessions directly via `session.Registry`

- **Goal**: The TUI session path no longer goes through `junkBackend`. `RunTUI` constructs the session directly via `session.New(id, thread, opts...)`, registers it in `session.NewInMemoryRegistry()`, and passes both to `runTUIEngine`.
- **Dependencies**: Task 2.
- **Files Affected**: `internal/app/app.go`, `internal/app/tui_engine.go`, `internal/app/tui_engine_test.go`.
- **New Files**: None.
- **Interfaces**:
  - `runTUIEngine` signature changes from `(ctx, *session.Session, *tui.TUI, *tuiEngineFactory, *junk.Stream)` to `(ctx, *session.Session, *tui.TUI, *tuiEngineFactory)`. The `*junk.Stream` parameter is removed because the TUI no longer holds a stream handle.
  - `tuiEngineFactory.Build(sess *session.Session)` no longer calls `mgr.Get(sess.ID())` (the thread is already in `sess.Thread()`).
- **Validation**:
  - `go build ./...` succeeds.
  - `go test -race ./internal/app/...` passes. The TUI engine tests in `tui_engine_test.go` must be updated to construct sessions directly (not via the junkBackend fixture at `tui_engine_test.go:74-92`).
  - `task validate` clean.
  - Manual smoke: `workshop tui` launches; slash commands work (`/role`, `/thinking`); the info bar still shows the five static keys.
- **Details**:
  - The thread is constructed via `ledger.NewThread()` (with `HydrateThread` if `cfg.threadID` is set, via the `ledger.Repository` interface).
  - The `junkBackend`'s TUI half (`CreateSession` / `GetSession`) is removed. The `junkBackend` file remains in this commit because HTTP still uses it.
  - `tuiEngineFactory.Build(sess)` looks up the thread via `sess.Thread()` (was: `mgr.Get(sess.ID())`).
  - The compact handler's `SetStream` call (currently at `tui_engine.go:299-301`) is removed in this commit (it's redundant once `sess.Thread()` is the canonical thread reference); Commit 5 cleans up the `stream` field on the handlers.

### Task 4: Per-turn save hook via `ledger.Repository`

- **Goal**: Persistence is driven by a per-session save hook that listens for `TurnCompleteEvent` and calls `Repository.SaveTurn` / `UpdateThreadTip`. `junk.Stream.Save()` is no longer called.
- **Dependencies**: Task 3.
- **Files Affected**: `internal/app/tui_engine.go`, `internal/app/app.go`.
- **New Files**: `internal/app/persist.go` (~50 LOC; contains `SessionPersister` and the `NewSessionPersister(repo ledger.Repository, log *slog.Logger)` constructor).
- **Interfaces**:
  ```go
  // SessionPersister subscribes to a session's TurnCompleteEvent and
  // appends a journal entry per turn via the supplied ledger.Repository.
  // It replaces the post-turn junk.Stream.Save() call.
  type SessionPersister struct {
      repo ledger.Repository
      log  *slog.Logger
  }

  func NewSessionPersister(repo ledger.Repository, log *slog.Logger) *SessionPersister
  func (p *SessionPersister) Attach(sess *session.Session) error // subscribes; returns immediately
  func (p *SessionPersister) Close() error
  ```
- **Validation**:
  - `go build ./...` succeeds.
  - `go test -race ./...` passes.
  - New unit test: stub a `ledger.Repository` with a counter; emit two `TurnCompleteEvent`s on a `*session.Session`; assert `SaveTurn` was called twice and `UpdateThreadTip` once per turn.
  - Manual smoke: `workshop tui`, send two turns, kill the process, restart with the same `--thread` ID; the thread resumes with both turns.
- **Details**:
  - `runTUIEngine` instantiates one `SessionPersister` per session, calls `Attach(sess)` after the engine registers the session, and `Close()` on shutdown.
  - The `Repository` is constructed via `ledger.NewFileRepository(threadsDir)` where `threadsDir` is the XDG data home path (the same path `junk.NewJSONStore` uses today, per `cmd/workshop/thread.go`).
  - Failure to save is logged via `slog.Warn` (matching `runTUIEngine:404-406`); persistence failures do not abort the session.
  - The `*junk.Stream` parameter on `runTUIEngine` is removed in this commit (it was only used for `Save`).
  - This is the design-decision commit; the builder should land it in a single commits so the reviewer sees the proposal in one place.

### Task 5: Slash handlers and compact drop the `stream` field

- **Goal**: `roleCommand`, `thinkingCommand`, `compactCommand`, `analyticsCommand` no longer hold a `*junk.Stream`. The dual-write reduces to `sess.Thread().Meta().Set(...)` + `sess.SetMetadata(...)`.
- **Dependencies**: Task 4.
- **Files Affected**: `internal/app/app.go`, `internal/app/app_test.go`.
- **New Files**: None.
- **Interfaces**: The `stream *junk.Stream` field is removed from each handler struct. `compactCommand.SetStream(s *junk.Stream)` is deleted. `tuiEngineFactory.Build` no longer calls `SetStream`.
- **Validation**:
  - `go build ./...` succeeds.
  - `go test -race ./...` passes.
  - Slash-handler tests (currently using `newRoleCommandStream` / `newRoleCommandSession` at `app_test.go:452-477`) get their fixtures adjusted to construct sessions directly without a junk stream handle. The assertions on slash-handler behavior survive unchanged.
  - Manual smoke: `/role`, `/thinking`, `/compact`, `/analytics`, `/help`, `/name` all work in the TUI.
- **Details**:
  - The `if c.stream != nil { c.stream.SetMetadata(...) }` block at `app.go:902-904` is removed; the session + thread writes remain.
  - `compactCommand`'s boundary write (the `compaction.MetaKeyBoundaryInfo` key at `app.go:900-901`) is now session + thread only.
  - The `tuiEngineFactory` simplifies (no `stream` lookup).

### Task 6: Stdio and HTTP migrate to session-native; `junkBackend` deleted

- **Goal**: `RunStdio` and `RunHTTP` use `session.New` + `session.NewInMemoryRegistry` + `engine.New` directly. `internal/app/backend.go` is deleted. No `junk.NewManager` call site remains in production code.
- **Dependencies**: Task 5.
- **Files Affected**: `internal/app/app.go`, `internal/app/backend.go` (deleted), `cmd/workshop/*.go` if any.
- **New Files**: None.
- **Interfaces**: `RunStdio` and `RunHTTP` gain the same construction shape as `RunTUI` (a `buildManager`-style helper that returns `(*session.Registry, *tuiEngineFactory-or-equivalent, error)`, or whatever shape the stdio and HTTP conduits accept).
- **Validation**:
  - `go build ./...` succeeds.
  - `go test -race ./...` passes.
  - `grep -rn "junk\." internal/app/*.go` returns zero matches (excluding comments).
  - `grep -rn "junk\." internal/app/*.go cmd/workshop/*.go` returns zero matches in production code.
  - Manual smoke: `workshop tui`, `workshop http`, `workshop stdio` all start and accept input.
- **Details**:
  - The stdio conduit (`x/conduit/stdio` at the current ore version) still accepts `*junk.Manager` per the legacy pattern — if so, this commit either keeps a thin junk-shaped adapter scoped to stdio (deferring its removal to upstream), or migrates stdio to the same session-shaped API the HTTP conduit uses. The builder picks based on what's available upstream; if neither is clean, the builder surfaces the choice.
  - `backend.go` deletion removes the file entirely.
  - The `junk.WithDefaultMetadata(defaultMeta)` call at `app.go:1216` is removed (its work moved to Commit 2's `seedSession`).

### Task 7: Test fixtures converted to session-native

- **Goal**: All `junk.NewMemoryStore` / `junk.NewJSONStore` / `junk.NewManager` references in test files are replaced with session-native fixtures (`ledger.NewMemoryRepository` + `session.NewInMemoryRegistry` + `session.New(...)`).
- **Dependencies**: Task 6.
- **Files Affected**: `internal/app/app_test.go`, `internal/app/tui_engine_test.go`, `cmd/workshop/thread_test.go`.
- **New Files**: None (helpers may be extracted if duplication emerges).
- **Interfaces**: New test helpers replace `newRoleCommandStream`, `newRoleCommandSession`, `newRoleCommandStreamWithRoles` (and similar). The helpers take a `*testing.T` and return `(*session.Session, ledger.Repository)` (or a wider tuple as needed).
- **Validation**:
  - `go test -race ./...` passes.
  - `grep -rn "junk\." internal/app/*_test.go cmd/workshop/*_test.go` returns zero matches (or only the `cmd/workshop/thread_test.go` legacy listing test, which the builder documents as a follow-up).
  - The slash-handler assertions in `app_test.go` (line ranges 446-1260, 2964-3210, etc.) all pass with the new fixtures.
  - `task validate` clean.
- **Details**:
  - The replacement is mechanical for most fixtures: replace `junk.NewMemoryStore()` + `junk.NewManager(store, prov, stepFactory, processor, junk.WithDefaultMetadata(defaultMeta))` with `ledger.NewMemoryRepository()` + `session.NewInMemoryRegistry()` + `session.New(threadID, ledger.NewThread(), ...)`.
  - The `*junk.Stream`-based assertions in `app_test.go:1314`, `1378`, `1478`, `1799`, `1893`, `1945`, `2317` rewrite to use `*ledger.Thread` and `*session.Session`.
  - `cmd/workshop/thread_test.go`'s `seedThreadAt` (line 32) and `junk.NewJSONStore` calls (16+ instances) rewrite to `ledger.NewMemoryRepository` and journal the events manually. The thread-listing semantics (`ListThreadIDs`) need a parallel in the new repo; if absent, the builder files this as a follow-up.

### Task 8: Final sweep — remove any leftover `junk.*` references

- **Goal**: Zero `junk.*` references in production code. Any orphan imports removed. Any dead helpers deleted.
- **Dependencies**: Task 7.
- **Files Affected**: Whatever remains. Likely a no-op if Commits 2–7 were thorough.
- **New Files**: None.
- **Interfaces**: N/A.
- **Validation**:
  - `grep -rn "junk\." internal/app/*.go cmd/workshop/*.go` returns zero matches (production code).
  - `grep -rn "junk\." internal/app/*_test.go cmd/workshop/*_test.go` returns zero matches (or only documented follow-ups).
  - `go mod tidy` produces no diff.
  - `task validate` clean.
- **Details**:
  - If `cmd/workshop/thread_test.go` still has `junk.*` references from Commit 7's "documented follow-up" carve-out, this commit handles them.
  - The `go.mod` may have `require github.com/andrewhowdencom/ore v1.x.x` lines that no longer need `junk` in the indirect graph; `go mod tidy` resolves.

## Dependency Graph

```
Task 1 → Task 2 → Task 3 → Task 4 → Task 5 → Task 6 → Task 7 → Task 8
```

Strictly sequential. Each commit depends on the previous (the build must be green at each step).

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| Persistence redesign (Commit 4) changes the on-disk format. Existing thread journals written by `junk.Stream.Save` are not directly readable by `ledger.Repository`. | High — workshop can't resume existing threads post-migration. | Certain if the formats differ. | **Builder must verify the format compatibility in Task 4 before implementing.** If incompatible, either (a) write a one-shot migration tool that converts `junk.Thread` JSON snapshots to `ledger.Repository` journal entries, or (b) accept a one-time loss of pre-migration threads and document it. If (a), the migration tool is a separate commits BEFORE Commit 4 lands; if (b), the README is updated. |
| `session.Metadata` and `thread.Metadata` drift at runtime. A slash handler writes only one store and the other lags. | Medium — info bar goes stale or persistence breaks for the lagging key. | Low — the existing dual-write pattern is well-understood and the new architecture doesn't change its shape, only its surface. | Commit 5 explicitly keeps the dual-write for the slash handlers; Commit 7 tests assert the two stores stay in sync after every slash invocation. |
| `x/conduit/stdio` still consumes `*junk.Manager` (the legacy pattern), so Commit 6 can't fully migrate stdio. | Medium — stdio path retains a junk dependency. | Medium — needs verification at Task 6 design time. | Builder surfaces the choice: either (a) keep a junk-shaped adapter scoped to stdio only, or (b) file an upstream request against ore to add a session-shaped stdio constructor. |
| Test rewrite (Commit 7) is large (~400-500 LOC across 3 test files). A subtle assertion difference in a fixture helper could silently weaken test coverage. | Medium — coverage gaps surface later. | Medium — large rewrites invite drift. | Commit 7 is structured as a single commits with the full rewrite; `go test -race ./...` MUST pass; the builder runs `task validate` before commit. The builder documents any test that's deliberately weakened in the commit message. |
| `go.mod` indirect graph includes `junk` after production migration (because some test or transitive dep still references it). | Low — code compiles but `junk` remains in `go.sum`. | Low — `go mod tidy` resolves it if no code imports it. | Commit 8 runs `go mod tidy` and verifies the result. |
| The `model` key in `statusZoneMapping` (added `690bb46`) was never seeded before this plan; users who've trained on the info bar don't expect it. | Low — additive, not a regression. | Certain. | Mention in the Commit 1 message that the model is now visible in the info bar; document in README if appropriate. |
| `tuiEngineFactory.Build`'s removal of the `mgr.Get(sess.ID())` lookup may surface a session/thread mismatch (e.g., if the session was constructed with one thread and the engine was given a different one). | High — inference runs against the wrong thread. | Low — `session.New` takes the thread and `sess.Thread()` returns the same one. | Builder adds a sanity check in Commit 3: assert `factory.Build(sess)` is only called for sessions constructed by the same `RunTUI` path. |
| The HTTP conduit migration in Commit 6 changes the public `RunHTTP` signature surface (whatever `httpc.New` consumes changes from `*junk.Manager` to a session-shaped one). | Medium — breaks external HTTP integration. | Medium — workshop's HTTP UI is in-repo, no external consumers, but the API is `app.RunHTTP(...)`. | Builder verifies the signature change is internal; the `app.RunHTTP` function signature does not change (it returns `error`). |

## Validation Criteria

- [ ] After Task 1: `workshop tui` shows `cwd`, `git_branch`, `thread_id`, `tui.pid`, `model` in the info bar from session creation.
- [ ] After Task 1: `go test -race ./internal/app/...` includes a new unit test for `seedSession` covering all five keys.
- [ ] After Task 4: A two-turn TUI session, followed by a process kill and a `workshop tui --thread <id>` restart, resumes with both turns intact.
- [ ] After Task 5: `/role`, `/thinking`, `/compact`, `/analytics`, `/help`, `/name` all work in the TUI.
- [ ] After Task 6: `grep -rn "junk\." internal/app/*.go cmd/workshop/*.go` returns zero matches.
- [ ] After Task 6: `workshop tui`, `workshop http`, `workshop stdio` all start and accept input.
- [ ] After Task 7: `go test -race ./...` passes with zero `junk.*` references in any test file (or only documented follow-ups).
- [ ] After Task 8: `go mod tidy` produces no diff; `task validate` is clean.
- [ ] Every commit's message follows the `Design:` / `Tradeoffs:` / `Justification:` format with a `Co-authored-by:` trailer (`${MODEL}@${HARNESS}.agent`).
- [ ] Every commit leaves `task validate` clean.
- [ ] Each commit is independently revertable; `git revert <commit>` produces a green build at the previous state.