# Plan: Wire Up Skills Toolkit

## Objective

Add progressive skill discovery to the `workshop` application by integrating the `ore/x/tool/skills` package. This exposes `list_skills`, `read_skill`, and `search_skills` to the LLM, backed by two filesystem discoverers: one at the repo-level `.agents/skills/` and one at the user-global `~/.agents/skills/`. The system prompt is updated to proactively mention these tools.

## Context

- `workshop` is a terminal-based coding assistant built on the `ore` framework. It wires tools in `internal/app/app.go` inside `buildManager`.
- `ore/x/tool/skills` implements the agentskills.io standard: a `Catalog` aggregates multiple `Discoverer`s, deduplicates by name (first-wins), and exposes three LLM-callable functions: `list_skills`, `read_skill`, `search_skills`.
- `ore/x/tool/skills` is a **separate Go module** (`github.com/andrewhowdencom/ore/x/tool/skills`). Workshop already uses `replace` directives for other `ore/x/tool/*` submodules, but **not** for `skills`. A new `replace` directive is required.
- `skills.NewFSDiscoverer` gracefully handles missing directories (WalkDir logs and skips), so neither `.agents/skills` nor `~/.agents/skills` need to exist at startup.
- The `defaultPrompt` constant in `internal/app/app.go` currently describes filesystem and bash tools; it needs to be extended to mention the skills toolkit.

## Architectural Blueprint

```
┌─────────────────────────────────────────────────────────────┐
│                    internal/app/app.go                        │
│                     buildManager()                            │
│                                                               │
│   ┌──────────────────┐      ┌──────────────────────────┐   │
│   │ tool.NewRegistry() │─────►│ registry.Register(...)   │   │
│   └──────────────────┘      │  filesystem tools        │   │
│                               │  bash tool             │   │
│   ┌──────────────────────┐    │  role tools            │   │
│   │ skills.NewToolkit(   │    │  skills toolkit  ◄─────┘   │
│   │   FSDiscoverer(cwd), │    └──────────────────────────┘   │
│   │   FSDiscoverer(home) │                                 │
│   │ )                    │                                 │
│   └──────────────────────┘                                 │
│            │                                                 │
│            ▼                                                 │
│   toolkit.Register(registry)                                 │
│                                                               │
│   loop.New(                                                  │
│     WithHandlers(registry.Handler()),                        │
│     WithInvokeOptions(openai.WithTools(registry.Tools()))    │
│   )                                                          │
└─────────────────────────────────────────────────────────────┘
```

No embedded skills are included (per requirement). The two filesystem discoverers are:
1. `.agents/skills` — repo-scoped skills, versioned with the project
2. `~/.agents/skills` — user-global personal skills that travel across repos

The catalog deduplicates by skill name with first-wins semantics, so a skill in `.agents/skills` shadows the same-named skill in `~/.agents/skills`.

## Requirements

1. Add `ore/x/tool/skills` as a module dependency with a local `replace` directive.
2. Import `ore/x/tool/skills` in `internal/app/app.go`.
3. In `buildManager`, construct a `skills.Toolkit` with two `FSDiscoverer`s:
   - `skills.NewFSDiscoverer(".agents/skills")`
   - `skills.NewFSDiscoverer(filepath.Join(homeDir, ".agents", "skills"))` where `homeDir` comes from `os.UserHomeDir()`; if that errors, omit the home discoverer.
4. Register the toolkit into the existing `tool.Registry` via `toolkit.Register(registry)`.
5. Update `defaultPrompt` to mention `list_skills`, `read_skill`, and `search_skills` and explain their purpose.

## Task Breakdown

### Task 1: Add Module Dependency for ore/x/tool/skills
- **Goal**: Make `github.com/andrewhowdencom/ore/x/tool/skills` importable by adding a `replace` directive and a `require` entry.
- **Dependencies**: None.
- **Files Affected**: `go.mod`
- **New Files**: None.
- **Interfaces**: None.
- **Validation**: `go mod tidy` completes without errors.
- **Details**: Add `replace github.com/andrewhowdencom/ore/x/tool/skills => ../ore/x/tool/skills` alongside the existing `ore/x/tool/*` replace directives. Add `github.com/andrewhowdencom/ore/x/tool/skills v0.0.0` to the `require` block. Run `go mod tidy` to resolve `go.sum`.

### Task 2: Wire Skills Toolkit and Update System Prompt
- **Goal**: Instantiate the skills toolkit with two filesystem discoverers, register it into the tool registry, and update the baked-in system prompt so the LLM knows to use skills.
- **Dependencies**: Task 1.
- **Files Affected**: `internal/app/app.go`
- **New Files**: None.
- **Interfaces**: Uses `skills.NewToolkit`, `skills.NewFSDiscoverer`, `Toolkit.Register`.
- **Validation**: `go build ./...` passes.
- **Details**:
  1. Add imports `"os"`, `"path/filepath"`, and `"github.com/andrewhowdencom/ore/x/tool/skills"`.
  2. In `buildManager`, after `registry := tool.NewRegistry()`, build a slice of `skills.Discoverer`:
     - Append `skills.NewFSDiscoverer(".agents/skills")`.
     - Call `os.UserHomeDir()`; if no error, append `skills.NewFSDiscoverer(filepath.Join(homeDir, ".agents", "skills"))`.
     - Create `skillsToolkit := skills.NewToolkit(discoverers...)`.
     - Call `skillsToolkit.Register(registry)`. Handle any registration error (log or return).
  3. Update the `defaultPrompt` constant to mention skills tools alongside filesystem and bash tools. Keep the tone and style consistent with the existing prompt.

## Dependency Graph

- Task 1 → Task 2

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| `ore/x/tool/skills` depends on a newer `ore` core API than the one workshop currently pins | High | Low | All modules are replaced to `../ore`, so they share the same local source. If ore has uncommitted changes, `go build` will surface them immediately. |
| `os.UserHomeDir()` returns an error (e.g., minimal container, missing HOME) | Low | Low | Skip the home discoverer on error; the local `.agents/skills` discoverer still works. |
| Skill name collision between `.agents/skills` and `~/.agents/skills` | Low | Medium | The ore catalog uses first-wins deduplication; local repo skills naturally shadow global ones, which is the desired behavior. |

## Validation Criteria

- [ ] `go mod tidy` completes successfully after adding the `replace` directive.
- [ ] `go build ./...` passes for the entire workshop repository.
- [ ] `internal/app/app.go` contains the `skills` import, a `skills.NewToolkit(...)` call with two `FSDiscoverer`s, and a `toolkit.Register(registry)` call.
- [ ] `defaultPrompt` mentions `list_skills`, `read_skill`, and `search_skills`.
- [ ] Running `go run ./cmd/workshop` (with a valid API key) starts without errors even when neither `.agents/skills` nor `~/.agents/skills` exists.
