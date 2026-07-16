---
name: subagent-authoring
description: Guidelines for authoring workshop sub-agents — YAML-frontmatter .md files
  in $XDG_DATA_HOME/workshop/subagents/ that the parent agent invokes mid-turn as tools.
---

# Sub-agent Authoring

A **sub-agent** is a specialist the parent agent invokes mid-turn by emitting a tool call. Each invocation runs a fresh `*agent.Agent` against an isolated conversation thread and returns a structured `{status, summary, findings}` JSON object. Sub-agents are the **invokable** counterpart to **roles** (which are loaded as the parent agent's system prompt).

> **Roles** define the *active* agent (loaded as the system prompt).
> **Sub-agents** define *invokable* agents (loaded as tools). Use a role for the persona the assistant takes; use a sub-agent for a specialist the assistant can delegate to.

## Inlined Expertise Doctrine

This skill follows the **writing-skills** doctrine (the framework-shipped skill shipped with `ore/x/tool/skills` — load it via `read_skill(name="writing-skills")`): **SKILL.md is NOT an index**. It carries the most-frequent SOPs for authoring sub-agents directly. The two reference files (`references/result-schema.md`, `references/authoring-checklist.md`) cover deep dives and pre-flight steps that don't belong inline.

## File Location

Sub-agents are auto-discovered from a single directory at session start. The path follows XDG Base Directory on Linux/macOS and the local AppData convention on Windows.

| OS | Path |
|---|---|
| Linux / macOS | `$XDG_DATA_HOME/workshop/subagents/` (fallback `~/.local/share/workshop/subagents/`) |
| Windows | `%LOCALAPPDATA%\workshop\subagents\` |

The directory is not auto-created. Place `.md` files there directly.

## File Format

Each sub-agent is one `.md` file. The filename (without `.md`) becomes the tool name the parent sees in its registry. YAML frontmatter is delimited by `---` lines at the very start of the file; everything after the closing `---` is the body (free-form markdown).

```markdown
---
description: A research-focused sub-agent that finds and cites references.
---
You are a research sub-agent. When invoked with a question, find relevant
references in the codebase or via the `bash` tool, and return a JSON
object matching the workshop sub-agent result schema.
```

A YAML frontmatter is optional — files without one parse fine and the sub-agent will simply have no description. **Description is the only field the model reads to decide whether to call you**, so omitting it is a finding in review.

## Frontmatter Fields (v1)

| Field | Required? | Effect |
|---|---|---|
| `description` | Recommended | Tool description shown to the model. The model uses this to decide whether to call you. Empty string if absent. |
| `name` | Ignored | The filename is the source of truth. A `name:` in the frontmatter is silently discarded — this is tested and deliberate, to keep the on-disk ↔ registered mapping deterministic. |

Unknown fields (e.g., a future `pattern:`, `model:`, `tools:`) currently fail YAML parsing because the loader uses strict `yaml.Unmarshal`. This matches role behavior at v1. **Do not** add unsupported fields until they ship; open a GitHub issue first.

## Body Content

The body becomes the sub-agent's **spec** — the system prompt it runs against. It SHOULD:

- Define the specialist's domain in one paragraph.
- State what artifacts the sub-agent must produce (a summary, a list of findings, a patch).
- List constraints the sub-agent must respect (read-only mode, single-file scope, length budgets, JSON-schema-conformant output).
- Avoid referencing the parent's history — sub-agents start from a fresh `ledger.Thread` and never see it.

The body is free-form markdown. Headers, lists, fenced code blocks, and links all work. The body is **not** itself a `writing-skills` doc, so the ~150-line length budget does not strictly apply, but anything over ~150 lines should be split across `references/` files that the body links to.

## Naming Convention

The filename (sans `.md`) becomes the tool name. This name:

- MUST be lowercase-hyphenated (e.g., `code-reviewer`, `researcher`, `my-team.migrator`).
- MUST NOT collide with any built-in tool. Built-ins at v1 are: `read_file`, `write_file`, `edit_file`, `list_directory`, `search_files`, `bash`, `workspace_create`, `workspace_destroy`, `git_commit`, `set_title`, `read_skill`. A colliding name **fails the workshop session at startup** with the error `subagent "<name>" collides with already-registered tool`.
- SHOULD be namespaced to reduce collision risk: prefer `my-team.code-reviewer` over `code-reviewer`. The dot separator is permitted and helps when multiple authors share `~/.local/share/workshop/subagents/`.

The same name also collides if a previous sub-agent loaded first; the session fails the same way. Run `task validate` (or relaunch workshop) after adding a file to surface collisions fast.

## Invocation Contract

The parent calls a sub-agent by emitting a tool call with this JSON shape:

```json
{"prompt": "Free-form instruction for the sub-agent."}
```

The sub-agent runs a fresh `*agent.Agent` against an isolated `ledger.Thread`, performs whatever work the body specifies (within the capability bounds below), and returns a structured result. **The full result schema is in [references/result-schema.md](./references/result-schema.md)** — read it before designing a sub-agent whose output the parent will branch on.

## Capabilities at v1

Sub-agents inherit these capabilities from the parent agent at v1. They are **fixed**; future frontmatter fields (`pattern:`, `model:`, `tools:`) will make them configurable but are not implemented yet.

| Capability | Source at v1 | Out of scope (future work) |
|---|---|---|
| Cognitive pattern | `ReAct` (the parent's pattern; passed via `agent.WithPattern(&cognitive.ReAct{})` in `internal/app/subagent.go`). | Custom pattern per sub-agent. |
| Provider | The parent's provider and default model spec. | Per-sub-agent model override. |
| Tools | The parent's full tool registry: `read_file`, `write_file`, `edit_file`, `list_directory`, `search_files`, `bash`, `workspace_create`, `workspace_destroy`, `git_commit`, `set_title`, `read_skill`. Sub-agent tool calls resolve against the parent's active worktree. | Custom tool policy per sub-agent. |
| State | None. `agent.WithState` is deliberately omitted; the framework's `x/subagent` package seeds a fresh `ledger.Thread` per call. | None — state isolation is a hard contract. |
| Tracing | The parent's OTel tracer; sub-agent spans are nested under the parent's span. | Span nesting controls. |
| Streaming | None — the sub-agent returns the full structured result once complete. | Streaming deltas; parallel fan-out. |

A sub-agent that depends on a tool outside this list, or that requires a different pattern/model, **cannot** be implemented at v1. Open an issue.

## Definition of Done

A sub-agent file is **complete** when:

1. The file parses via `internal/subagent.LoadSubagent` without error (no malformed YAML).
2. The filename does not collide with any built-in or already-registered tool name. The session fails loudly if it does — that's the test.
3. `task validate` succeeds (`go test -race ./internal/subagent/...` covers the loader).
4. The body produces output that parses against the [result schema](./references/result-schema.md). Smoke-test by running workshop with the file in place and asking the parent to invoke it.
5. The description is a single declarative sentence stating what the sub-agent does and when to use it.

## Review Path

When asked to review, audit, check, or improve an existing sub-agent file, run the gates below against it. Output enumerated findings only; do not render pass/fail verdicts.

### 1. Frontmatter
- `description` is a single declarative sentence stating what the sub-agent does and when to use it.
- No extraneous fields. v1 supports `description` only; future `pattern:` / `model:` / `tools:` must not appear until they ship.
- `name:` in YAML is silently ignored. A finding is *informational*, not blocking — note it but don't reject.

### 2. Trigger Quality
- Would the description fire on the right user prompts? Test against hypothetical parent invocations: "ask the researcher to find X", "delegate a migration to the migrator".
- Could the description under-trigger? If the description is generic ("a helpful assistant"), surface it as a finding — narrow scope.

### 3. Length and Shape
- Body under ~150 lines if it must be readable at a glance; longer bodies split across `references/` linked from the body.
- No required title heading; the body is a spec, not a `writing-skills` doc.

### 4. Tone and Rationale
- Imperative voice throughout.
- Every MUST/NEVER is paired with a *why* so the model can reason about edge cases rather than blindly obey. Bare "NEVER do X" without explanation is a finding.

### 5. Scope Tightness
- The sub-agent addresses a single concern. A "generalist" sub-agent that handles research, refactoring, and migration is a finding — split into three files.
- The body's outputs should be parseable against [result-schema.md](./references/result-schema.md). If the body instructs the sub-agent to produce free-form prose, the parent cannot branch on outcomes — surface this as a finding.

### 6. Name Collision
- Filename basename does not match any built-in tool (`read_file`, `write_file`, `edit_file`, `list_directory`, `search_files`, `bash`, `workspace_create`, `workspace_destroy`, `git_commit`, `set_title`, `read_skill`).
- Filename is lowercase-hyphenated.
- Filename is namespaced (`my-team.<name>`) when an unnamespaced form risks collision with another team or user's sub-agents.

### 7. References
- If the body links to `references/foo.md`, that file exists at `<subagent-dir>/references/foo.md`.
- Reference files are one-level deep (no nested chains).
- Reference files use relative paths from the skill root.

## References

- [Result Schema](./references/result-schema.md) — full `{status, summary, findings}` contract, status enum semantics, failure modes.
- [Authoring Checklist](./references/authoring-checklist.md) — pre-flight steps to run before declaring a sub-agent complete.