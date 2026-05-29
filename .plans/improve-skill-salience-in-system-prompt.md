# Plan: Improve Skill Salience and Activation in System Prompt

## Objective

Integrate the `ore/x/tool/skills` `SystemPromptFragment()` into the system prompt composition and replace the passive skills mention in `defaultPrompt` with a conditional behavioral directive. This makes skill names and descriptions visible to the LLM on every turn and triggers skill activation based on pattern matching rather than proactive exploration.

## Context

- **File**: `internal/app/app.go` contains `buildManager()` which wires all application components.
- **Current state**: `defaultPrompt` mentions skills passively: `"You also have access to skills tools (list_skills, read_skill, search_skills) that let you discover and load specialized instructions for specific tasks."` The LLM has no idea what skills exist and no incentive to call `list_skills`.
- **Current state**: `buildManager` creates a `skills.Toolkit` with filesystem discoverers (`.agents/skills` and `~/.agents/skills`) and registers `list_skills`, `read_skill`, and `search_skills` as tools. However, `skillsToolkit.SystemPromptFragment()` is never called, so no skill listing reaches the system prompt.
- **Library capability**: `skills.Catalog.SystemPromptFragment()` already:
  - Returns a formatted bullet list of all discovered skills (name + description)
  - Sorts skills by name for deterministic, reproducible output
  - Returns an empty string when no skills are discovered or an error occurs
  - Includes a basic directive: `"Use read_skill(name=<skill>) to load detailed instructions when needed"`
- **Library capability**: `systemprompt.Transform` supports `WithContextContentFunc` which accepts `func(context.Context) string`. Empty fragments are omitted from the prompt. `ctxContentFuncs` are evaluated after all regular `contentFuncs`.
- **Test coverage**: `internal/app/app_test.go` already tests `buildManager`, system prompt transforms, and `defaultPrompt` content. New tests must be added to validate the skills fragment behavior.

## Architectural Blueprint

The fix is a two-part change in `internal/app/app.go`:

1. **Strengthen the prompt directive**: Replace the passive skills sentence in `defaultPrompt` with a conditional behavioral rule that tells the LLM *when* and *how* to load skills.

2. **Inject the skills fragment**: Pass `skillsToolkit.SystemPromptFragment()` to `systemprompt.New()` via `WithContextContentFunc`. This appends a dynamically-generated, deterministic skill listing after the base prompt and working directory content on every turn.

The `SystemPromptFragment()` requires a `context.Context` for catalog discovery/refresh, so `WithContextContentFunc` is the correct option (not `WithContentFunc`). Because the fragment returns `""` when no skills exist, the system prompt transform gracefully omits it with no additional code.

```
┌─────────────────────────────────────────────────────────────────────────┐
│                     systemprompt.New(...)                                 │
│                                                                           │
│   WithContentFunc(currentPrompt) ──────► "You are a terminal-based..."   │
│   WithContentFunc(workingDirContent) ──► "You are running in: ..."       │
│   WithContextContentFunc(skillsFragment) ──► "You have access to..."    │
│                                              "- git: Guidelines..."        │
│                                              "- testing: Testing..."    │
└─────────────────────────────────────────────────────────────────────────┘
```

## Requirements

1. Replace the passive skills mention in `defaultPrompt` with a conditional behavioral directive.
2. Wire `skillsToolkit.SystemPromptFragment()` into the system prompt transform via `systemprompt.WithContextContentFunc`.
3. Ensure the application starts and operates correctly when no skills are discovered (empty fragment omission).
4. Skill listing must remain deterministic (already guaranteed by `Catalog.List` sorting).
5. Add tests verifying the behavioral directive, skills fragment inclusion, and empty-fragment omission.

## Task Breakdown

### Task 1: Replace Passive Skills Mention with Behavioral Directive
- **Goal**: Update `defaultPrompt` to replace the passive sentence about skills tools with a conditional rule that tells the LLM when to call `read_skill`.
- **Dependencies**: None.
- **Files Affected**: `internal/app/app.go`
- **New Files**: None.
- **Interfaces**: None.
- **Validation**: `go test ./internal/app/...` passes (including new and existing tests).
- **Details**:
  1. In `internal/app/app.go`, locate the `defaultPrompt` constant.
  2. Replace the sentence:
     ```go
     "You also have access to skills tools (list_skills, read_skill, search_skills) that let you discover and load specialized instructions for specific tasks. " +
     ```
     with:
     ```go
     "When your task matches a skill description below, call read_skill to load its detailed instructions before proceeding. " +
     ```
  3. Ensure the rest of the `defaultPrompt` constant is unchanged.
  4. Add `TestDefaultPrompt_ContainsBehavioralDirective` in `internal/app/app_test.go` that verifies `defaultPrompt` contains `"When your task matches a skill description below"`.

### Task 2: Wire Skills Fragment into System Prompt Transform
- **Goal**: Pass `skillsToolkit.SystemPromptFragment()` to `systemprompt.New()` via `WithContextContentFunc` so the skills listing appears in every system prompt turn.
- **Dependencies**: Task 1.
- **Files Affected**: `internal/app/app.go`, `internal/app/app_test.go`
- **New Files**: None.
- **Interfaces**: Uses `skills.Toolkit.SystemPromptFragment() func(context.Context) string` and `systemprompt.WithContextContentFunc`.
- **Validation**: `go test ./...` passes, `go build ./cmd/workshop` succeeds.
- **Details**:
  1. In `internal/app/app.go`, in `buildManager`, locate the `systemprompt.New()` call:
     ```go
     sp, err := systemprompt.New(
         systemprompt.WithContentFunc(currentPrompt),
         systemprompt.WithContentFunc(makeWorkingDirContent(cfg.workingDir)),
     )
     ```
  2. Add a third option:
     ```go
     sp, err := systemprompt.New(
         systemprompt.WithContentFunc(currentPrompt),
         systemprompt.WithContentFunc(makeWorkingDirContent(cfg.workingDir)),
         systemprompt.WithContextContentFunc(skillsToolkit.SystemPromptFragment()),
     )
     ```
     The `skillsToolkit` variable is already defined earlier in `buildManager`.
  3. In `internal/app/app_test.go`, add a `mockSkillDiscoverer` type that implements `skills.Discoverer`:
     ```go
     type mockSkillDiscoverer struct {
         meta []skills.SkillMeta
         read map[string]string
     }

     func (m *mockSkillDiscoverer) Discover(ctx context.Context) ([]skills.SkillMeta, error) {
         return m.meta, nil
     }

     func (m *mockSkillDiscoverer) Read(ctx context.Context, name string) (string, error) {
         return m.read[name], nil
     }
     ```
  4. Add `TestSystemPrompt_WithSkillsFragment` that:
     - Creates a `mockSkillDiscoverer` with at least two `skills.SkillMeta` entries
     - Creates `tk := skills.NewToolkit(mock)`
     - Creates a `systemprompt.Transform` with `WithContentFunc(func() string { return "Base prompt." })` and `WithContextContentFunc(tk.SystemPromptFragment())`
     - Transforms an empty `state.Buffer{}`
     - Verifies the resulting system prompt artifact contains the skill names and the fragment header
  5. Add `TestSystemPrompt_WithoutSkillsFragment` that:
     - Creates a `mockSkillDiscoverer` with an empty `meta` slice
     - Creates a toolkit and system prompt transform as above
     - Verifies the resulting system prompt does NOT contain the fragment header `"You have access to the following specialized skills"`
     - Verifies the base prompt is still present
  6. Verify existing `TestBuildManager_Smoke` and `TestBuildManager_WithWorkingDir` still pass (they operate in environments with no skills files, so the fragment is empty and omitted).

## Dependency Graph

- Task 1 → Task 2

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| `skillsToolkit.SystemPromptFragment()` performs I/O on every Transform call | Low | Medium | The `Catalog` caches metadata; `SystemPromptFragment` calls `List()` which hits the cache after the first discovery. No repeated filesystem walks. |
| Mock discoverer in tests diverges from real `skills.Discoverer` interface | Low | Low | The `Discoverer` interface is stable (two methods). Test compilation will catch any future changes. |
| `defaultPrompt` change inadvertently breaks existing tests that assert exact prompt content | Medium | Low | Existing tests (`TestMakeCurrentPrompt_Fallback`, `TestSystemPrompt_WithCWD`) reference `defaultPrompt` as a variable, not as a hardcoded string, so they adapt automatically. New tests validate the behavioral directive explicitly. |
| Context cancellation during `SystemPromptFragment` evaluation | Low | Low | The fragment is evaluated inside `systemprompt.Transform` which receives the turn context. Cancellation would simply omit the fragment; `Catalog.List` handles errors gracefully by returning empty. |

## Validation Criteria

- [ ] `go test ./...` passes with no failures.
- [ ] `go build ./cmd/workshop` produces a binary successfully.
- [ ] `defaultPrompt` contains the behavioral directive `"When your task matches a skill description below, call read_skill to load its detailed instructions before proceeding."`.
- [ ] `defaultPrompt` no longer contains the passive skills sentence `"You also have access to skills tools (list_skills, read_skill, search_skills) that let you discover and load specialized instructions for specific tasks."`.
- [ ] `buildManager` passes `skillsToolkit.SystemPromptFragment()` to `systemprompt.New` via `WithContextContentFunc`.
- [ ] When skills are discovered, the system prompt artifact contains a deterministic, sorted listing of skill names and descriptions.
- [ ] When no skills are discovered, the system prompt does not contain the skills fragment header, and the application operates normally.
