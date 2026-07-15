# Plan: Add Declarative Sub-Agents

## Objective

Introduce a parallel declarative construction to roles for **sub-agents**: agent definitions that surface as tools the parent can call mid-turn. Sub-agent files live alongside roles (`<xdg>/workshop/subagents/*.md`), are parsed by a new `internal/subagent` package mirroring `internal/role`, and are wired into the parent agent's `tool.Registry` via `github.com/andrewhowdencom/ore/x/subagent.AsTool` inside `internal/app/app.go`'s `stepFactory`. v1 keeps the frontmatter minimal (description only; name from filename), the cognitive pattern fixed to `ReAct`, and the sub-agent's tool set equal to the workshop's full registry. Forward-compat hooks are reserved for future frontmatter growth (`pattern`, `model`, `tools:`).

## Context

Workshop today exposes a single role architecture: declarative agent definitions injected as a system-prompt transform into one top-level `*agent.Agent` (see `internal/role/role.go:21-25` and the `roleCommand` wiring in `internal/app/app.go:377-506`). The `internal/role` package is deliberately *leaf-ish* — it depends only on `tool.Sandbox` and `adrg/xdg` — so it can be consumed without cycle risk.

The parent agent has no way to delegate to *other* agents during its own turn; role switching is user-driven (`/role`) and externally mediated. The new construction closes that gap by reusing the sibling ore package `x/subagent` (`../ore/x/subagent/`), which exposes `AsTool(build func() (*agent.Agent, error), name, description string) (tool.Tool, tool.ToolFunc)` (`subagent.go:92-161`). `AsTool` wraps a factory closure that returns a *fresh* `*agent.Agent` per invocation; the child's output is validated against `ResultSchema` (`result.go:73-90`) and surfaced as a structured `Result{Status, Summary, Findings}`. State isolation is hard — fresh `ledger.Thread` per call, `agent.WithState` MUST be omitted from the factory.

The workshop's tool-registration surface (`internal/app/app.go:877-904`) uses `tool.NewRegistry()` per stream, `mustRegister(registry, tool.Tool, tool.ToolFunc)` for built-ins, and `mustRegisterRaw(...)` for ad-hoc tools. Both helpers live in `app.go:1237-1249`. The `stepFactory` closure (`app.go:838-923`) is the single integration point: it builds the registry, registers tools, and returns `[]loop.Option` that wire `loop.WithHandlers(xtool.NewHandler(registry, ...))` and `loop.WithInvokeOptions(invokeOpts...)`.

Project conventions (`Taskfile.yml`):

- `task build` → `go build ./cmd/workshop`
- `task test` → `go test -race ./...`
- `task lint` → `golangci-lint run ./...`
- `task validate` → `lint` + `test` + `build`

`go mod tidy` is the canonical way to refresh `go.sum` after a `go.mod` edit.

The `x/subagent` package exists in `../ore/x/subagent/` with its own `go.mod` declaring `module github.com/andrewhowdencom/ore/x/subagent` and requiring `github.com/andrewhowdencom/ore v1.2.0`. Workshop's `go.mod` currently resolves `github.com/andrewhowdencom/ore v1.2.1` (compatible). The package is **not** present in workshop's `go.sum` yet — Task 1 is to add it.

## Architectural Blueprint

The construction is a three-layer parallel to roles:

| Layer | Role (today) | Sub-agent (this plan) |
|---|---|---|
| Discovery | `<xdg>/workshop/roles/*.md` | `<xdg>/workshop/subagents/*.md` |
| Loader | `internal/role/role.go` (`LoadRole`, `ListRoleDefinitions`) | `internal/subagent/subagent.go` (`LoadSubagent`, `ListSubagentDefinitions`) |
| Wiring | `makeSystemPromptTransform` → system-prompt transform | `stepFactory` → `tool.Registry.Register` via `x/subagent.AsTool` |
| Invocation | `/role <name>` (slash command, external) | LLM tool-call (internal, mid-turn) |
| Lifecycle | Persistent across turns (resolver) | Fresh-per-call (factory) |

The parent's tool slot is the **delegation handle**, not a capability scope: the workshop's existing tools (filesystem, bash, workspace, git_commit, set_title, skills, …) are *shared* with the sub-agent at v1 by passing the same `(tool.Tool, tool.ToolFunc)` pairs into the sub-agent's factory closure. The sub-agent then constructs its own loop with the same provider, the default spec, `cognitive.React{}`, and `subagent.ResultSystemPrompt()` as a transform — producing JSON conforming to `ResultSchema` and returning it to the parent as a structured `Result`.

Tree-of-Thought deliberation on the **tool-set-inheritance** mechanism (the only real architectural choice):

- **Path A — Reuse parent's `tool.Registry` and `xtool.Handler` in the sub-agent's `*agent.Agent`.** Smallest footprint but couples parent and child handlers; any handler-level state is shared.
- **Path B — Build a fresh sub-agent `tool.Registry` per call, re-registering the parent's `(tool.Tool, tool.ToolFunc)` pairs and a fresh default `workshopSandbox`.** Slightly more allocation per call but strict isolation in registry identity; the parent's tool functions still capture the parent's `stream` via closure, so tool *behavior* is shared but registry *identity* is not.
- **Path C — Pass only the parent's `[]tool.Tool` schemas; sub-agent registers its own handler implementations.** Cleanest isolation but requires duplicating tool function implementations — adds maintenance burden and is rejected.

**Selected: Path B.** Rationale: the user's "domain specialist, full power" framing requires that sub-agent tool calls actually do work (filesystem reads, bash runs) against the same `stream` as the parent. Path B achieves this with one fresh registry per sub-agent call and zero duplication of tool logic. The overhead is one `tool.NewRegistry` + N `Register` calls per call — comparable to existing per-turn loop overhead.

Tree-of-Thought deliberation on **load timing**:

- **Path X — Load all sub-agent definitions once at `buildManager` time**; capture the slice in the `stepFactory` closure.
- **Path Y — Re-load sub-agent definitions inside `stepFactory` per stream**, paralleling how `skills.NewFSDiscoverer(...)` is invoked per stream.

**Selected: Path Y.** Rationale: parallel to existing skill discovery (already per-stream in `app.go:851-857`); picks up newly-added sub-agent files between sessions without restart; cost is one directory read per stream open.

## Requirements

1. A new package `internal/subagent` MUST expose `SubagentDefinition`, `Dir()`, `ExtractBody()`, `LoadSubagent()`, and `ListSubagentDefinitions()` mirroring `internal/role`. *(Explicit, agreed in ideation.)*
2. Sub-agent files MUST be discovered from `<xdg>/workshop/subagents/*.md`, where `xdg` is `adrg/xdg.DataHome`. *(Explicit, parallel to `internal/role/role.go:28-30`.)*
3. Sub-agent name MUST be derived from filename (basename without `.md`). *(Explicit.)*
4. Frontmatter MUST support a single `description` field at v1. *(Explicit.)*
5. The body MUST be free-form markdown and treated as the sub-agent's spec / domain identity. *(Explicit.)*
6. `x/subagent` MUST be added to `go.mod` and verifiable via `go build ./...` succeeding. *(Inferred — required for import.)*
7. For every loaded sub-agent, a `(tool.Tool, tool.ToolFunc)` pair MUST be registered into the parent agent's `tool.Registry` inside `stepFactory`, via `x/subagent.AsTool`. *(Explicit, the integration mechanism.)*
8. The sub-agent's factory MUST use `cognitive.React{}` as its pattern, the parent's `provider.Provider`, the parent's default `models.Spec`, and `subagent.ResultSystemPrompt()` as a transform. *(Explicit, agreed in ideation.)*
9. The sub-agent MUST inherit the workshop's full tool set at v1 (Path B in the blueprint). *(Explicit.)*
10. `task validate` (lint + test + build) MUST pass after each task. *(Inferred from project conventions.)*
11. The construction MUST leave room for future frontmatter fields (`pattern`, `model`, `tools:`) without breaking existing files. *(Explicit — `[inferred]` mechanism: tagged fields with `,omitempty` so absence is permissive; YAML schema MUST tolerate unknown keys.)*
12. Sub-agent discovery MUST be global (any role can call any sub-agent). *(Explicit.)*
13. Out of scope for v1: per-role opt-in lists, model override, custom pattern per sub-agent, custom tool policy per sub-agent, parallel fan-out, streaming deltas, span nesting under parent, dynamic tool-set changes within a call (matches `x/subagent` v1 contract). *(Explicit.)*

## Task Breakdown

### Task 1: Add `x/subagent` Dependency to `go.mod`

- **Goal**: Make `github.com/andrewhowdencom/ore/x/subagent` importable from workshop code.
- **Dependencies**: None.
- **Files Affected**: `go.mod`, `go.sum`.
- **New Files**: None.
- **Interfaces**: None — purely a dependency manifest change.
- **Validation**:
  - `grep -nE "x/subagent" go.mod` returns the new require line.
  - `grep -nE "x/subagent" go.sum` shows the resolved version.
  - `go build ./...` succeeds.
  - `go mod tidy` produces no diff after the initial edit (i.e., the pinned version is reachable on the module proxy).
- **Details**: Verify the upstream tag exists at the resolution expected (e.g., `x/subagent/v0.1.0` or whatever the ore release script publishes). If a tag does not yet exist for `x/subagent`, fall back to a local `replace` directive pointing at `../ore/x/subagent` — document the choice in the commit message so future cleanup mirrors the precedent set by `.plans/drop-ore-replace-directives.md`. If a tag exists, the commit message MUST cite the version and note that no `replace` directive is being added. Use the same `Taskfile.yml` validation gate (`task validate`) before commit.

### Task 2: Create `internal/subagent` Loader Package

- **Goal**: Provide declarative sub-agent discovery and parsing parallel to `internal/role`.
- **Dependencies**: Task 1.
- **Files Affected**: None modified.
- **New Files**:
  - `internal/subagent/subagent.go`
  - `internal/subagent/subagent_test.go`
- **Interfaces**:
  ```go
  type SubagentDefinition struct {
      Name        string `yaml:"-"`        // derived from filename
      Description string `yaml:"description"`
      Prompt      string                  // body
  }
  func Dir() string
  func ExtractBody(content string) (body, frontmatter string)
  func LoadSubagent(dir, name string, sb tool.Sandbox) (*SubagentDefinition, error)
  func LoadBody(path string, sb tool.Sandbox) (string, error)
  func ListSubagentDefinitions(dir string, sb tool.Sandbox) ([]SubagentDefinition, error)
  ```
- **Validation**:
  - `go test -race ./internal/subagent/...` passes with all four exported functions covered.
  - Tests parallel the structure of `internal/role/role_test.go`: frontmatter present/absent, missing file, malformed YAML, listing empty dir, listing mixed valid/invalid files (malformed files skipped silently per the parallel `role.ListRoleDefinitions` semantics at `internal/role/role.go:142-144`).
  - Unknown YAML keys at v1 should match role's current behavior (`yaml.Unmarshal` is strict by default); document this limitation in a code comment and the package's `doc.go` so future work can introduce forward-compat parsing without ambiguity. Forward-compat is *not* in this task — it is tracked as a future concern in the Risks section.
  - `task lint` clean (golangci-lint over the new package).
  - `task build` succeeds (the package compiles even though nothing imports it yet — Go allows unused-internal packages as long as they compile).
- **Details**: Mirror the `internal/role` package's leaf-ish dependency surface: only `tool.Sandbox` and `adrg/xdg`. The `Dir()` function returns `filepath.Join(xdg.DataHome, "workshop", "subagents")`. `ExtractBody` and `LoadBody` are byte-for-byte analogues of the role versions — copy the algorithm. `ListSubagentDefinitions` MUST skip malformed files silently to match role behavior. The package's `doc.go` MUST include a paragraph stating that sub-agents are domain specialists invoked via the parent's tool registry, not restricted capability delegates, so future readers understand the construction's intent.

### Task 3: Wire Sub-Agent Registration into `stepFactory`

- **Goal**: For every loaded sub-agent, register a `(tool.Tool, tool.ToolFunc)` pair in the parent's `tool.Registry`, backed by `x/subagent.AsTool`.
- **Dependencies**: Task 2.
- **Files Affected**: `internal/app/app.go`.
- **New Files**: None (logic lives inline in `app.go`; if helper functions exceed ~30 lines, extract to `internal/app/subagent.go`).
- **Interfaces** (new helpers, signatures are part of the contract):
  ```go
  // buildSubagentTool returns a (tool.Tool, tool.ToolFunc) pair for the given
  // sub-agent definition. The returned closure constructs a fresh *agent.Agent
  // per invocation, using the parent's provider, default spec, and tool set.
  func buildSubagentTool(
      sa SubagentDefinition,
      prov provider.Provider,
      spec models.Spec,
      parentTools []tool.Tool,
      parentToolFuncs map[string]tool.ToolFunc, // name -> func
      sandbox tool.Sandbox,
      tracer trace.Tracer,
  ) (tool.Tool, tool.ToolFunc)
  ```
- **Validation**:
  - `go build ./...` succeeds.
  - `task lint` clean.
  - `task test -race ./...` passes (existing `internal/app/app_test.go` 3128-line test suite must still pass; no test in this task is required — see Task 4 for the wiring-level test).
  - Manual smoke: with a test sub-agent file placed at `<xdg>/workshop/subagents/example.md`, starting the workshop TUI shows a tool named `example` in the registered tool list (verifiable via a `task` build with debug output, or by inspecting `registry.Tools()` length before/after).
- **Details**:
  - Inside `stepFactory` (after the existing `mustRegisterRaw` calls at `app.go:899-904`), load sub-agent definitions:
    ```go
    subs, err := subagent.ListSubagentDefinitions(subagent.Dir(), nil)
    if err != nil {
        return nil, fmt.Errorf("list subagents: %w", err)
    }
    ```
    Match `roleCommand`'s `nil` sandbox convention (`app.go:818`) — sub-agent file paths are absolute (XDG-derived), so no sandbox resolution is needed at v1.
  - For each `sa` in `subs`, build the (Tool, ToolFunc) pair via `buildSubagentTool(sa, prov, defaultSpec, registry.Tools(), parentFuncs, &workshopSandbox{...}, tracer)` and `mustRegister(registry, t, fn)`. The `parentFuncs` map is populated by the existing `mustRegister` / `mustRegisterRaw` calls — either refactor those to populate the map, or duplicate the registration list inside the helper. **Implementation choice: extract the existing tool-registration calls into a single helper that returns `([]tool.Tool, map[string]tool.ToolFunc)` so the sub-agent factory can reuse the same closures without drift.** This refactor is local to `stepFactory` and is part of this task.
  - Inside `buildSubagentTool`, the factory closure MUST:
    1. Call `subagent.ResultSystemPrompt()` to obtain the JSON-schema transform.
    2. Construct a fresh `*agent.Agent` with `agent.WithProvider(prov)`, `agent.WithSpec(spec)`, `agent.WithPattern(&cognitive.React{})`, and `agent.WithTransforms(sp)`. **MUST NOT** include `agent.WithState` (preserves the `x/subagent` state-isolation contract; see `subagent.go:71-76`).
    3. Pass `subagent.AsTool(factory, sa.Name, sa.Description)` to the caller.
  - The sub-agent's loop is wired inside the factory: build a fresh `tool.Registry`, register each `(tool.Tool, tool.ToolFunc)` from `parentTools` / `parentFuncs`, set the same default sandbox, wrap with `xtool.NewHandler(registry, xtool.WithTracer(tracer))`, and pass via `agent.WithHandlers(...)`. The provider-side `WithInvokeOptions` (anthropic.WithTools / openai.WithTools) MUST be applied per the parent's `buildInvokeOptions` pattern (`app.go:1075-1086`) so the sub-agent's `*agent.Agent` actually advertises the tools to the LLM.
  - Each task leaves the repo in a buildable state; this task in particular MUST NOT introduce a compile error if `subagent.Dir()` returns a non-existent path (`ListSubagentDefinitions` returns an empty slice in that case, parallel to `role.ListRoleDefinitions` at `internal/role/role.go:123-126`).

### Task 4: Integration Test for Sub-Agent Registration

- **Goal**: Prove that a sub-agent file on disk results in a registered tool, and that the registered tool's handler is the `x/subagent.AsTool` closure (smoke-level — no LLM call).
- **Dependencies**: Task 3.
- **Files Affected**: `internal/app/app_test.go` (add new test), `internal/app/app.go` (no functional change; the test may require a small surface like `stepFactoryForTest` or a constructor split — see Details).
- **New Files**: None, or `internal/app/subagent_test.go` if preferred for isolation.
- **Interfaces**: None new (test-only).
- **Validation**:
  - `go test -race ./internal/app/...` passes with the new test added.
  - Test creates a temporary sub-agent file, calls `buildSubagentTool(...)` directly (or a refactored test seam), invokes the returned `tool.ToolFunc` with a stub prompt, and asserts:
    - Returned value is non-nil (or, if the underlying LLM is stubbed out, a structured `subagent.Result` is returned).
    - The tool's `Name` equals the sub-agent's filename basename.
    - The tool's `Description` equals the sub-agent's frontmatter `description`.
  - Test also asserts that with no sub-agent files present, the helper returns an empty list (parallel to `TestRoleCommand_NoArgEmptyDir` at `app_test.go:514`).
  - `task lint` and `task validate` clean.
- **Details**:
  - Follow the pattern of `internal/app/truncation_smoke_test.go` (uses a `captureEmitter` and `xtool.NewHandler(registry)` to test tool wiring without a real LLM). For the sub-agent test, the LLM-driven `a.Run(ctx, buf)` inside the `x/subagent.AsTool` closure cannot be exercised without a provider; the test MUST therefore either:
    - (a) Inject a stub provider that returns a canned `Result`-conforming JSON string, OR
    - (b) Test only the `(tool.Tool, tool.ToolFunc)` registration shape (name, description, schema) and the factory closure's first-invocation behavior up to the provider call.
  - Recommendation: **(b)**. The integration guarantee at v1 is "registered tools have the right metadata"; the actual LLM round-trip is exercised manually and in future E2E suites. Document this scope limitation in the test's doc comment.
  - If `stepFactory` cannot be invoked directly from a test (it depends on `cfg.tracer`, `stream`, etc.), expose a `stepFactoryForTest(cfg *config, stream *junk.Stream) ([]loop.Option, error)` that wraps the closure body — same pattern as `TestRoleSlashHandler` (which calls into `roleCommand` directly). Make the test seam minimal: the existing `buildManager` already encapsulates the full wiring.

## Dependency Graph

- Task 1 → Task 2 → Task 3 → Task 4 (strictly sequential; each depends on the previous)
- Task 3 || Task 4 (Task 4 cannot start before Task 3 lands, but is parallel to "refactor of existing tool-registration calls into a shared helper" if the implementer chooses to extract that helper first as a sub-step — treating that refactor as a sub-step inside Task 3 keeps the graph linear)

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| `x/subagent` is not yet tagged for upstream release; workshop's `go.mod` cannot resolve it from the public proxy. | Medium — would block Task 1. | Medium — verified absent from `go.sum` at planning time. | Task 1 explicitly checks for an upstream tag. Fallback: add a `replace github.com/andrewhowdencom/ore/x/subagent => ../ore/x/subagent` directive and document the cleanup path (parallels the precedent in `.plans/drop-ore-replace-directives.md`). |
| Tool-set inheritance (Path B) requires duplicating parent tool registrations into a fresh sub-agent registry per call. Some parent tool funcs close over `stream`; the closure must be captured correctly so sub-agent tool calls see the *parent's* stream, not a stale one. | High — incorrect closure capture would break filesystem reads, bash execution, and git_commit attribution in sub-agent calls. | Low — the existing `mustRegister`/`mustRegisterRaw` calls already close over `stream` in `app.go:899-901`. | Task 3 explicitly extracts the existing tool registrations into a shared helper that returns `([]tool.Tool, map[string]tool.ToolFunc)`. The sub-agent factory reuses the same `tool.ToolFunc` values, preserving closure semantics. Code review MUST verify that the same `workshopSandbox{stream: stream}` instance is set as default on the sub-agent's registry. |
| YAML frontmatter is strict at v1 (matches role's current behavior). A user adding a future `model:` field to a sub-agent file will hit a parse error. | Low — feature unblocking, not breakage. | High — the user's stated trajectory ("in future, model overrides") will hit this. | Documented in the package's `doc.go` and Risks table. Forward-compat parsing (e.g., `yaml.Node` decoding or `map[string]any` plucking) is a follow-up task explicitly scoped out of v1. |
| The sub-agent's factory captures `prov`, `defaultSpec`, `tracer`, and tool closures from `buildManager`'s scope. A long-lived `*agent.Agent` inside the closure (not the case — the factory returns a *fresh* agent per call) would leak resources. | Medium if violated; N/A at v1. | N/A — `x/subagent.AsTool` contract (line 92) requires fresh agents; `Agent.Close` is idempotent (line 102-103 of `subagent.go`). | The closure follows the contract literally. Code review MUST verify no `agent.New(...)` is hoisted out of the factory. |
| `subagent.ResultSystemPrompt()` returns `(loop.Transform, error)`. Callers must handle the error. | Low — function-level error handling. | Certain | Task 3 explicitly checks and returns the error from the factory (mirrors `subagent.ResultSystemPrompt()` documentation at `result.go:92-99`). |
| Per-stream sub-agent directory read (`stepFactory` body) adds a small filesystem hit per session open. | Low | Certain | Parallel pattern to skills discovery (`app.go:851-857`); cost is one `os.ReadDir` per stream open. Negligible. |
| A user-defined sub-agent name (filename) collides with an existing workshop tool name (e.g., `bash`, `read_file`). | Medium — silent overwrite is the registry's default behavior; a malicious or accidental sub-agent file could shadow a built-in tool. | Medium — names are user-controlled filenames. | Task 3 MUST detect collisions between loaded sub-agent names and the parent's pre-registration tool set. If a collision exists, fail the `stepFactory` call with a descriptive error (`fmt.Errorf("subagent %q collides with built-in tool", sa.Name)`). Document the convention that sub-agent names should be namespaced (`my-team.code-reviewer` not `reviewer`). |

## Validation Criteria

- [ ] **Criterion 1**: `task build` succeeds at every task boundary. Verified after each of Tasks 1, 2, 3, 4.
- [ ] **Criterion 2**: `task test -race ./...` passes after each task. Verified after each of Tasks 1, 2, 3, 4.
- [ ] **Criterion 3**: `task lint` (golangci-lint) is clean after each task. Verified after each of Tasks 1, 2, 3, 4.
- [ ] **Criterion 4**: A sub-agent file placed at `<xdg>/workshop/subagents/example.md` with a `description: ...` frontmatter and free-form body results in a tool named `example` appearing in the parent's `registry.Tools()` list when the workshop starts. Verified manually via a debug-print or integration test (Task 4).
- [ ] **Criterion 5**: The `x/subagent` import appears in `internal/app/app.go` and the dependency is resolved in `go.sum`. Verified after Task 1 and confirmed again after Task 3.
- [ ] **Criterion 6**: The `internal/subagent` package compiles in isolation (`go build ./internal/subagent/`) even when nothing imports it. Verified after Task 2.
- [ ] **Criterion 7**: No modification to `internal/role/*` (the parallel package). Verified by `git diff` showing zero changes to `internal/role/role.go` and `internal/role/role_test.go`.
- [ ] **Criterion 8**: The `cognitive.React{}` pattern is the only pattern used in sub-agent factories. Verified by `grep -n "WithPattern" internal/app/app.go` showing only the existing single `WithPattern` line and the new sub-agent factory line, both passing `&cognitive.React{}` (or a shared `reactPattern := &cognitive.React{}` symbol — designer's choice).
- [ ] **Criterion 9**: A malformed sub-agent file (bad YAML) is skipped silently during `ListSubagentDefinitions`, parallel to role behavior. Verified by Task 2's test suite.
- [ ] **Criterion 10**: A sub-agent file with a name colliding with an existing built-in tool name causes `stepFactory` to return a descriptive error rather than silently overwriting. Verified by Task 4's test suite (or an explicit unit test added to Task 3).