# Plan: Add Thinking-Level Primitive and `/thinking` Slash Command

## Objective

Replace the existing `thinking-budget: int64` (Anthropic) and `reasoning-effort: string` (OpenAI) configuration knobs with a single, cross-provider `thinking-level` enum (`off` | `minimal` | `low` | `medium` | `high` | `max`). Each adapter translates the level into its own wire format at request time. Workshop's TUI gains a `/thinking` slash command for per-thread, on-the-fly level changes, and the active level is shown persistently in the status bar. The result is a single, model-agnostic vocabulary for reasoning effort, with no need for users to know provider-specific token budgets or reasoning-effort string vocabularies.

## Context

- `ore/provider/provider.go` — the framework's `Provider` interface and shared `InvokeOption` shape. New shared types (`ThinkingLevel`) and helpers belong here because adapters and applications both depend on the provider package; per AGENTS.md, central types live in core packages.
- `ore/x/provider/anthropic/anthropic.go:88-108` — the current `WithThinkingBudget(tokens int64)` per-invocation option. The wire translation (`thinking.budget_tokens`) lives in `Invoke` at lines 314-318. Anthropic requires a minimum budget of 1024 tokens; a budget of 0 is a no-op.
- `ore/x/provider/anthropic/anthropic.go:309-321` — the `Invoke` request builder. `inv.maxTokens` and `inv.thinkingBudget` are read independently; the level-to-budget translation will reuse `inv.maxTokens` as the denominator for percentage-based mapping.
- `ore/x/provider/openai/openai.go:68-80` — the current `WithReasoningEffort(effort string)` per-invocation option. The wire translation (`params.ReasoningEffort`) lives in `Invoke` at line 475. OpenAI accepts `low | medium | high`; the absence of the field disables reasoning.
- `ore/session/stream.go:356-368` — `Stream.GetMetadata` / `SetMetadata`. The existing `Stream.ModelOption()` (lines 384-398) is the *exact* precedent for this plan: it reads a metadata key (`provider.model`) and returns a `provider.InvokeOption` for the per-invocation call. The new level plumbing mirrors this pattern with `workshop.thinking_level` and `provider.WithThinkingLevel`.
- `ore/session/stream.go:365-375` — `SetMetadata` automatically emits a `loop.PropertiesEvent` after writing. The TUI's status bar (see `ore/x/conduit/tui/model.go:162-170`) already consumes `PropertiesEvent` and re-renders, so the persistent status bar requirement is satisfied by writing metadata from the slash command — no new event types or TUI plumbing required.
- `ore/x/slash/slash.go` — the `slash` package. `Registry.Bind(name, description, handler)` registers a command; `Handler` is `func(ctx, emitter, cmd Command) (Result, error)`; `Result.Feedback` is an ephemeral `artifact.Text` shown in the UI but not persisted. The existing `/role` command in `workshop/internal/app/app.go:282-310` is the template — it mutates stream metadata and is registered at line 422.
- `workshop/internal/app/app.go:601-637` — `buildInvokeOptions` branches on `cfg.provider.Kind` to produce provider-specific options. The current `if cfg.provider.ThinkingBudget > 0 { ... WithThinkingBudget(...) }` and `if cfg.provider.ReasoningEffort != "" { ... WithReasoningEffort(...) }` branches are the ones being replaced.
- `workshop/internal/app/app.go:642-695` — `newProvider` mutates `pc.MaxTokens = defaultAnthropicMaxTokens` on the anthropic branch. Lines 681-685 emit a `slog.Warn` when `MaxTokens <= ThinkingBudget`; this warning becomes obsolete under the new design because the level-to-budget translation enforces the invariant by construction (thinking ≤ 80% of max_tokens by default).
- `workshop/internal/app/app.go:175-217` — `RunTUI`. Line 209 already calls `tui.WithStatusLabels(map[string]string{"workshop.role": "role"})`. The new level is added to this same mapping, and to the `WithStatusZones` mapping on line 196-209 (zone `"context"`).
- `workshop/cmd/workshop/root.go:43-49` — viper-bound flags. `provider.thinking-budget` (int64) and `provider.reasoning-effort` (string) are the existing flags; the new `provider.thinking-level` (string) replaces both.
- `workshop/cmd/workshop/config.go:46-58` — `buildConfigMap` writes the config YAML. Both `thinking-budget` and `reasoning-effort` keys are emitted; both go away.
- `workshop/config.yaml.example` — the example config has `thinking-budget: 0` and `reasoning-effort: ""` documented; both lines are removed and a single `thinking-level: ""` (or `off`/`medium`) replaces them.
- `workshop/internal/app/app_test.go:225-244` — existing tests that exercise the OpenAI `WithReasoningEffort` path. These need rewriting to use the level primitive. Tests at lines 161-204 for the `MaxTokens <= ThinkingBudget` warning go away because the warning does.
- `ore/x/provider/anthropic/anthropic_test.go:609-660` — `TestProviderInvokeOptions_*` tests for the existing options. The new option gets a parallel set of tests.
- `ore/x/provider/openai/openai_test.go` — analog tests for the openai adapter. Same treatment.
- `ore/x/provider/anthropic/doc.go` and `ore/x/provider/openai/doc.go` — package-level docs that document the per-invocation options. Both need updating to describe the new `WithThinkingLevel` option.
- `workshop/README.md` — describes the configuration knobs. The new field needs documentation alongside the others.

## Architectural Blueprint

A single `provider.ThinkingLevel` enum is the framework-level primitive. Each adapter exports a `WithThinkingLevel(ThinkingLevel)` per-invocation option. Inside the adapter's `Invoke`, the level is translated into the wire format: percentage of `max_tokens` for Anthropic, mapped to OpenAI's qualitative enum for OpenAI, no-op for non-reasoning providers. The translation table lives in the adapter because the adapter is the only place that knows its wire format.

Workshop's `buildInvokeOptions` reads the level from `ProviderConfig.ThinkingLevel` (with a config-file default of `"off"` for backward compatibility with today's behavior) and forwards it via the appropriate adapter option. The level is also written to / read from stream metadata as `workshop.thinking_level` by the slash command, so `/thinking <level>` overrides the file default for the current thread. The status bar reads the same metadata key on every `PropertiesEvent` and re-renders.

Per-project convention (AGENTS.md), there is no backwards compatibility: `thinking-budget` and `reasoning-effort` are deleted outright. Users with either knob in their config will see a parse error and have to update; the workshop is `v0.x`, the change is documented in the example config, and the new field is materially more useful than the old one.

The default value for the new `thinking-level` field is `"off"` (preserves today's behavior for users who do not set it). Future work — explicitly out of scope for this plan — can introduce a per-model reasoning-capability registry that would let the default be `"medium"` for known reasoning-capable models. For this plan, the user opts in via the config file or `/thinking` at runtime.

## Requirements

1. Add `provider.ThinkingLevel` enum to `ore/provider/provider.go` (or a new `ore/provider/thinking.go`). Six levels: `ThinkingLevelOff`, `ThinkingLevelMinimal`, `ThinkingLevelLow`, `ThinkingLevelMedium`, `ThinkingLevelHigh`, `ThinkingLevelMax`. Include a `ParseThinkingLevel(string) (ThinkingLevel, error)` helper and a `Valid()` method on the type.
2. Replace `anthropic.WithThinkingBudget(int64)` with `anthropic.WithThinkingLevel(provider.ThinkingLevel)`. The translation to `thinking.budget_tokens` lives in the anthropic adapter's `Invoke`, using the table:
   - `off` → no `thinking` field on the request
   - `minimal`, `low`, `medium`, `high`, `max` → percentage of `max_tokens` (see Translation table below)
3. Replace `openai.WithReasoningEffort(string)` with `openai.WithThinkingLevel(provider.ThinkingLevel)`. The translation to `params.ReasoningEffort` lives in the openai adapter's `Invoke`, using the table:
   - `off` → no `reasoning_effort` field on the request
   - `minimal` → `"low"`
   - `low` → `"low"`
   - `medium` → `"medium"`
   - `high` → `"high"`
   - `max` → `"high"` (clamped; OpenAI does not support a value higher than `high`)
4. Drop `ProviderConfig.ThinkingBudget` and `ProviderConfig.ReasoningEffort`; add `ProviderConfig.ThinkingLevel string`. Update viper binding, config init, and `config.yaml.example` accordingly. The default value of the field is `"off"`.
5. Update `buildInvokeOptions` to read the level and forward it via the appropriate adapter option. Remove the `if cfg.provider.ThinkingBudget > 0` and `if cfg.provider.ReasoningEffort != ""` branches. Remove the `MaxTokens <= ThinkingBudget` warning in `newProvider` (no longer applicable because the translation enforces the invariant).
6. Add a `/thinking` slash command to workshop. Handler validates the level, reads the current value from stream metadata, sets the new value via `stream.SetMetadata("workshop.thinking_level", level)`, and emits a `Result.Feedback` confirmation. `/thinking` with no args reports the current level and lists available levels. Unknown levels produce a feedback message and do not change state.
7. Add the `workshop.thinking_level` key to the TUI's `WithStatusLabels` and `WithStatusZones` mappings in `RunTUI`, alongside the existing `workshop.role` entry. The label is `"thinking"` and the zone is `"context"`.
8. Update package-level docs (`ore/x/provider/anthropic/doc.go`, `ore/x/provider/openai/doc.go`) to describe the new `WithThinkingLevel` option and the per-level translation behavior. Update `workshop/README.md` to document `provider.thinking-level` and the `/thinking` command. Update `config.yaml.example` to show the new field.
9. Update or remove the tests that depend on the deleted fields. Specifically:
   - Remove `TestNewProvider_Anthropic_WarnsOnMaxTokensLeqThinkingBudget`, `TestNewProvider_Anthropic_SilentWhenMaxTokensExceedsThinkingBudget`, `TestNewProvider_Anthropic_SilentWhenThinkingBudgetZero` (the warning is gone).
   - Rewrite `TestBuildInvokeOptions_OpenAI_IncludesToolsAndReasoningEffort` to use `ThinkingLevel`.
   - Rewrite `TestBuildInvokeOptions_OpenAI_OmitsReasoningEffortWhenEmpty` to use `ThinkingLevel`.
   - Add new tests in both adapters for the level translation, and in workshop for the slash command.
10. `task validate` must pass after each task. The build must not break at any point.
11. The plan touches both the `ore` repo and the `workshop` repo. Tasks 1-3 are in `ore`; tasks 4-8 are in `workshop`. Each task is independently committable; tasks in `ore` land first, then tasks in `workshop`.

### Translation table (anthropic)

The percentage of `max_tokens` allocated to the thinking budget, with a floor of 1024 (Anthropic's minimum) and a ceiling of `max_tokens - 1024` (guarantee at least 1024 tokens for the visible response). For a 32k `max_tokens`:

| Level | % of max_tokens | Budget @ 32k |
|---|---|---|
| `off` | — | (no `thinking` field) |
| `minimal` | 2% | 640 → floor to 1024 |
| `low` | 8% | 2560 |
| `medium` | 25% | 8192 |
| `high` | 50% | 16000 |
| `max` | 80% | 25600 |

The numbers are deliberately not Pi's. Pi's defaults (1024, 2048, 8192, 16384) are absolute, and the percentage interpretation is a workshop-side design choice. These percentages produce budgets that round-trip through Anthropic's 1024-floor cleanly and that leave the visible response a useful share of the total output budget.

## Task Breakdown

### Task 1: Add `ThinkingLevel` type to `ore/provider/`

- **Goal**: Define the cross-provider `ThinkingLevel` enum, its constants, and its validation helpers as a framework-level primitive.
- **Dependencies**: None.
- **Files Affected**:
  - `ore/provider/provider.go` (or a new `ore/provider/thinking.go` in the same package)
- **New Files**: Optional — `ore/provider/thinking.go` if the file is split for readability. Recommended: add to the existing `provider.go` since the type is small.
- **Interfaces**:
  ```go
  // ThinkingLevel is a portable, qualitative description of how much
  // reasoning effort a model should spend on a turn. Adapters
  // translate the level into their provider's wire format at
  // request time.
  type ThinkingLevel string

  const (
      ThinkingLevelOff     ThinkingLevel = "off"
      ThinkingLevelMinimal ThinkingLevel = "minimal"
      ThinkingLevelLow     ThinkingLevel = "low"
      ThinkingLevelMedium  ThinkingLevel = "medium"
      ThinkingLevelHigh    ThinkingLevel = "high"
      ThinkingLevelMax     ThinkingLevel = "max"
  )

  // Valid reports whether the level is one of the defined constants.
  func (l ThinkingLevel) Valid() bool

  // ParseThinkingLevel parses a string into a ThinkingLevel. The
  // empty string is treated as a parse error — callers should
  // substitute their own default before calling.
  func ParseThinkingLevel(s string) (ThinkingLevel, error)
  ```
- **Validation**:
  - `go build ./...` passes in the `ore` repo.
  - `go test -race ./provider/...` passes.
  - New test file `ore/provider/thinking_test.go` (or appended to `provider_test.go`) covers: valid constants round-trip, `Valid()` returns true for all six, `Valid()` returns false for empty/unknown strings, `ParseThinkingLevel` accepts valid strings, `ParseThinkingLevel` rejects empty and unknown strings.
- **Details**:
  1. Add the type, constants, and helpers to `ore/provider/provider.go` (or new file — author's choice; the existing `provider.go` is small enough to absorb the addition).
  2. The `Valid()` method is a single switch on the level value.
  3. `ParseThinkingLevel` constructs a `ThinkingLevel` from the input and calls `Valid()`; on `false`, returns an error wrapping a list of valid values.
  4. Tests are table-driven per the project convention. No new dependencies required.
  5. This task produces no observable behavior change in the framework — the type is unused outside tests. The build must still pass and the existing test suite must still pass.

### Task 2: Replace `WithThinkingBudget` with `WithThinkingLevel` in the anthropic adapter

- **Goal**: The anthropic adapter accepts a `ThinkingLevel` per-invocation option and translates it to a `thinking.budget_tokens` wire value using the percentage table. The old `WithThinkingBudget(int64)` is removed.
- **Dependencies**: Task 1.
- **Files Affected**:
  - `ore/x/provider/anthropic/anthropic.go`
  - `ore/x/provider/anthropic/anthropic_test.go`
  - `ore/x/provider/anthropic/doc.go`
- **New Files**: None.
- **Interfaces**:
  ```go
  // thinkingLevelOption is a per-invocation option that sets the
  // thinking effort level for the Anthropic Messages API. The level
  // is translated to a thinking.budget_tokens value at request time
  // as a percentage of max_tokens, with a 1024-token floor and a
  // (max_tokens - 1024) ceiling. The empty level or "off" disables
  // extended thinking entirely.
  type thinkingLevelOption struct{ level provider.ThinkingLevel }
  func (thinkingLevelOption) IsInvokeOption() {}

  // WithThinkingLevel returns an InvokeOption that sets the thinking
  // effort level for a single provider invocation. The level is
  // translated to a token budget at request time.
  func WithThinkingLevel(l provider.ThinkingLevel) provider.InvokeOption
  ```
  The `WithThinkingBudget` function is removed.
- **Validation**:
  - `go build ./...` passes in the `ore` repo.
  - `go test -race ./x/provider/anthropic/...` passes.
  - The full test suite (including existing SSE-stream tests that depend on a `thinking` block) continues to pass — the new option must produce a `thinking` block on the wire when a non-`off` level is set.
  - New tests verify: `WithThinkingLevel("off")` produces no `thinking` field; `WithThinkingLevel("medium")` produces a `thinking` field with `budget_tokens = 25% of max_tokens` (or 1024, whichever is greater); the 1024 floor is enforced when the percentage would be smaller; the `(max_tokens - 1024)` ceiling is enforced when the percentage would be larger; an unknown level produces an error (or is treated as `off` — design decision; recommend `off` for forward compatibility with future levels).
- **Details**:
  1. In `ore/x/provider/anthropic/anthropic.go`:
     a. Replace the `thinkingBudgetOption` struct, the `WithThinkingBudget` function, and the `thinkingBudget` / `thinkingBudgetSet` fields on `invokeOptions` with the level-based equivalents.
     b. The `applyInvokeOptions` function folds `thinkingLevelOption` into `inv.thinkingLevel` and `inv.thinkingLevelSet`.
     c. The `Invoke` function's request builder (around line 314) replaces the `if inv.thinkingBudgetSet && inv.thinkingBudget > 0` block with a helper call:
        ```go
        if budget, ok := translateThinkingLevel(inv.thinkingLevel, inv.maxTokens); ok {
            params.Thinking = anthropic.ThinkingConfigParamOfEnabled(budget)
        }
        ```
     d. Add the translation helper at the package level:
        ```go
        // translateThinkingLevel returns the thinking budget in tokens
        // for the given level and max_tokens. The second return is
        // false when the level is "off" or unset, in which case the
        // request should omit the thinking field.
        func translateThinkingLevel(l provider.ThinkingLevel, maxTokens int64) (int64, bool)
        ```
        with the table from the Requirements section. The helper enforces the 1024 floor and the `(maxTokens - 1024)` ceiling.
  2. In `ore/x/provider/anthropic/anthropic_test.go`:
     a. Remove the existing `TestProviderInvokeOptions_ThinkingBudgetZeroIsSet` (line ~648) and replace with `TestProviderInvokeOptions_ThinkingLevelOffIsUnset` and `TestProviderInvokeOptions_ThinkingLevelFoldsCorrectly` (table-driven across the six levels).
     b. Update the existing `TestProviderInvokeOptions_FoldsAllKnownOptions` (line ~626) to use the new option.
     c. Add a new test `TestTranslateThinkingLevel` that exhaustively covers the percentage mapping, the 1024 floor, and the ceiling.
     d. The streaming tests (`TestProviderInvoke_StreamsThinking`, etc.) do not need to change — they exercise the wire-protocol level (the `thinking` block in the SSE stream), which is independent of how the request is built.
  3. In `ore/x/provider/anthropic/doc.go`:
     a. Update the "Extended thinking" section to describe `WithThinkingLevel(provider.ThinkingLevel)` instead of `WithThinkingBudget(int64)`.
     b. Document the percentage mapping and the floor/ceiling behavior.
     c. Note that the empty level or `"off"` disables extended thinking.
  4. The adapter is internal to the `ore` repo; the workshop's call site is updated in a later task. This task must leave the build green in `ore` even if the workshop is temporarily broken; the workshop's `anthropic.WithThinkingBudget` call (in `workshop/internal/app/app.go:626`) will be updated in Task 4.

### Task 3: Replace `WithReasoningEffort` with `WithThinkingLevel` in the openai adapter

- **Goal**: The openai adapter accepts a `ThinkingLevel` per-invocation option and translates it to OpenAI's `reasoning_effort` field. The old `WithReasoningEffort(string)` is removed.
- **Dependencies**: Task 1.
- **Files Affected**:
  - `ore/x/provider/openai/openai.go`
  - `ore/x/provider/openai/openai_test.go`
  - `ore/x/provider/openai/doc.go`
- **New Files**: None.
- **Interfaces**:
  ```go
  // thinkingLevelOption is a per-invocation option that sets the
  // thinking effort level for OpenAI-compatible providers. The level
  // is translated to OpenAI's reasoning_effort field at request
  // time. The "off" level omits the field entirely.
  type thinkingLevelOption struct{ level provider.ThinkingLevel }
  func (thinkingLevelOption) IsInvokeOption() {}

  // WithThinkingLevel returns an InvokeOption that sets the thinking
  // effort level for a single provider invocation. The level is
  // mapped to OpenAI's reasoning_effort vocabulary (low | medium |
  // high); levels outside that vocabulary are clamped (minimal ->
  // low; max -> high).
  func WithThinkingLevel(l provider.ThinkingLevel) provider.InvokeOption
  ```
  The `WithReasoningEffort` function is removed.
- **Validation**:
  - `go build ./...` passes in the `ore` repo.
  - `go test -race ./x/provider/openai/...` passes.
  - New tests verify: `WithThinkingLevel("off")` produces no `reasoning_effort` field; each of the five non-off levels produces the expected string per the translation table; unknown levels produce no field (or an error — see below).
- **Details**:
  1. In `ore/x/provider/openai/openai.go`:
     a. Replace the `reasoningEffortOption` struct, the `WithReasoningEffort` function, and the `reasoningEffort` field on the local variables (around line 414) with the level-based equivalents.
     b. Add a translation helper:
        ```go
        // translateThinkingLevel returns OpenAI's reasoning_effort
        // string for the given level, or the empty string if the
        // request should omit the field.
        func translateThinkingLevel(l provider.ThinkingLevel) string
        ```
        with the table from the Requirements section. Unknown levels return the empty string (treated as "off") for forward compatibility.
     c. The `Invoke` function's request builder (around line 475) replaces the `if reasoningEffort != ""` block with:
        ```go
        if effort := translateThinkingLevel(inv.thinkingLevel); effort != "" {
            params.ReasoningEffort = openai.ReasoningEffort(effort)
        }
        ```
  2. In `ore/x/provider/openai/openai_test.go`:
     a. Add `TestProviderInvokeOptions_ThinkingLevelFoldsCorrectly` (table-driven across the six levels).
     b. Add `TestTranslateThinkingLevel` that exhaustively covers the mapping, including the `minimal → low` and `max → high` clamps.
  3. In `ore/x/provider/openai/doc.go`:
     a. Update the "Reasoning" section to describe the level option and the clamps.
  4. The adapter is internal to the `ore` repo; the workshop's call site (in `workshop/internal/app/app.go:635`) is updated in Task 4.

### Task 4: Update workshop's `ProviderConfig` and config schema

- **Goal**: Workshop's `ProviderConfig` exposes a single `ThinkingLevel string` field. The old `ThinkingBudget` and `ReasoningEffort` fields are removed. The viper flag, the config init, the `newProvider` function, and the example config all reflect the new shape. The `MaxTokens <= ThinkingBudget` warning is removed (no longer applicable).
- **Dependencies**: Tasks 2, 3.
- **Files Affected**:
  - `workshop/internal/app/app.go` (`ProviderConfig` struct at lines 60-79; `newProvider` at lines 642-695)
  - `workshop/cmd/workshop/root.go` (flag at line 46)
  - `workshop/cmd/workshop/config.go` (`buildConfigMap` at lines 46-58)
  - `workshop/config.yaml.example` (the commented-out example at lines 13-23)
- **New Files**: None.
- **Interfaces**:
  ```go
  type ProviderConfig struct {
      Kind            string
      APIKey          string
      Model           string
      BaseURL         string
      Temperature     float64
      // ThinkingLevel is the qualitative reasoning effort. "off"
      // disables extended thinking; "minimal", "low", "medium",
      // "high", "max" are translated to provider-specific
      // parameters at request time. Default: "off".
      ThinkingLevel string
      MaxTokens     int64
  }
  ```
  The `ThinkingBudget` and `ReasoningEffort` fields are removed. The new field has no default in the struct; the default `"off"` is applied in `buildInvokeOptions` (or in the new `WithThinkingLevel` option's call site) when the field is empty.
- **Validation**:
  - `go build ./...` passes in the workshop repo.
  - `go test -race ./...` passes.
  - The tests at `workshop/internal/app/app_test.go:161-204` (the `MaxTokens <= ThinkingBudget` warning tests) are removed as part of this task.
  - A new test verifies that the new flag is bound and the new field is read.
- **Details**:
  1. In `workshop/internal/app/app.go:60-79`: drop the `ReasoningEffort` and `ThinkingBudget` fields; add the `ThinkingLevel` field with a doc comment.
  2. In `workshop/internal/app/app.go:642-695`: remove the `slog.Warn` block at lines 681-685. Remove the `if pc.ThinkingBudget > 0 && ...` guard. The function no longer needs the `ThinkingBudget` field. The signature is unchanged.
  3. In `workshop/cmd/workshop/root.go:46`: replace
     ```go
     rootCmd.PersistentFlags().Int64("provider.thinking-budget", 0, "Extended-thinking token budget (anthropic only; 0 = disabled)")
     ```
     with
     ```go
     rootCmd.PersistentFlags().String("provider.thinking-level", "off", "Thinking effort level (off, minimal, low, medium, high, max). Default: off.")
     ```
     Also remove the `provider.reasoning-effort` flag (line 47).
  4. In `workshop/cmd/workshop/root.go` `makeProviderConfig` (around line 100-110): replace the `ReasoningEffort` and `ThinkingBudget` field assignments with a `ThinkingLevel` assignment.
  5. In `workshop/cmd/workshop/config.go:46-58`: update the `buildConfigMap` to write `thinking-level` (and remove `thinking-budget` and `reasoning-effort`).
  6. In `workshop/config.yaml.example:13-23`: remove `reasoning-effort` from the openai block; remove `thinking-budget` from the anthropic block; the new field's value is omitted by default (which means `"off"`, preserving today's behavior).
  7. In `workshop/internal/app/app_test.go:161-204`: remove the three `TestNewProvider_Anthropic_*ThinkingBudget*` tests. The warning is gone; these tests are obsolete.
  8. This task leaves the build green. `buildInvokeOptions` still references the old fields and is fixed in Task 5.

### Task 5: Update `buildInvokeOptions` to use the new level

- **Goal**: `buildInvokeOptions` reads the level from `ProviderConfig.ThinkingLevel` and forwards it via the new adapter options. The default of `"off"` is applied when the field is empty.
- **Dependencies**: Task 4.
- **Files Affected**:
  - `workshop/internal/app/app.go` (`buildInvokeOptions` at lines 601-637)
  - `workshop/internal/app/app_test.go` (the `TestBuildInvokeOptions_*` tests)
- **New Files**: None.
- **Interfaces**: N/A (the function signature is unchanged).
- **Validation**:
  - `go build ./...` passes in the workshop repo.
  - `go test -race ./...` passes.
  - New tests cover: anthropic + `ThinkingLevel: "medium"` produces a `thinkingLevelOption`; anthropic + `ThinkingLevel: "off"` (or empty) does not; openai + `ThinkingLevel: "medium"` produces a `thinkingLevelOption`; openai + `ThinkingLevel: "off"` (or empty) does not; an unknown level is treated as `"off"`.
- **Details**:
  1. In `workshop/internal/app/app.go:601-637`:
     a. Replace the anthropic branch's `if cfg.provider.ThinkingBudget > 0 { ... WithThinkingBudget(...) }` block with:
        ```go
        if level := defaultLevel(cfg.provider.ThinkingLevel); level != provider.ThinkingLevelOff {
            opts = append(opts, anthropic.WithThinkingLevel(level))
        }
        ```
        where `defaultLevel` is a small package-local helper that returns `provider.ThinkingLevelOff` for an empty or unknown string, otherwise parses the string.
     b. Replace the openai branch's `if cfg.provider.ReasoningEffort != "" { ... WithReasoningEffort(...) }` block with the same pattern, using `openai.WithThinkingLevel`.
     c. Update the doc comment to reflect the new shape.
  2. In `workshop/internal/app/app_test.go`:
     a. Rewrite `TestBuildInvokeOptions_OpenAI_IncludesToolsAndReasoningEffort` (line ~225) to use `ThinkingLevel: "medium"` and assert the new option type (`"openai.thinkingLevelOption"` or whatever Go's `%T` produces).
     b. Rewrite `TestBuildInvokeOptions_OpenAI_OmitsReasoningEffortWhenEmpty` (line ~244) to use `ThinkingLevel: ""` and assert the option is absent.
     c. Add `TestBuildInvokeOptions_Anthropic_IncludesThinkingLevel` and `TestBuildInvokeOptions_Anthropic_OmitsThinkingLevelWhenOff` (parallel to the openai tests).
     d. Add `TestDefaultLevel` for the helper.
  3. The default-level helper is small enough to inline in `buildInvokeOptions`, but extracting it as a package-level function makes it testable in isolation. The author can choose.

### Task 6: Add the `/thinking` slash command

- **Goal**: Workshop exposes a `/thinking` slash command that reads the current level from stream metadata, validates the user-supplied level, and writes the new level back via `stream.SetMetadata`. With no argument, it reports the current level and the available levels.
- **Dependencies**: Task 5 (so that the metadata is actually read by `buildInvokeOptions` — otherwise the slash command is a no-op for the user).
- **Files Affected**:
  - `workshop/internal/app/app.go` (new `thinkingCommand` type and handler; new `slashReg.Bind` call)
  - `workshop/internal/app/app_test.go` (new tests for the handler)
- **New Files**: None.
- **Interfaces**:
  ```go
  // thinkingCommand handles the /thinking slash command for
  // changing the thread's thinking level without triggering an LLM
  // turn. The level is stored in stream metadata under
  // "workshop.thinking_level" so it persists across turns and across
  // thread resume.
  type thinkingCommand struct {
      mu     sync.Mutex
      stream *session.Stream
  }

  // Handler validates the level name and updates the stream
  // metadata. With no argument, returns the current level and the
  // list of valid levels. An invalid level returns a feedback
  // message and leaves state unchanged.
  func (c *thinkingCommand) Handler(ctx context.Context, _ loop.Emitter, cmd slash.Command) (slash.Result, error)

  // SetStream updates the shared stream reference.
  func (c *thinkingCommand) SetStream(s *session.Stream)
  ```
  The slash registry gains a single `Bind` call:
  ```go
  slashReg.Bind("thinking", "Set the thinking level for this thread", tc.Handler)
  ```
  The metadata key is `"workshop.thinking_level"`, parallel to `"workshop.role"`.
- **Validation**:
  - `go build ./...` passes in the workshop repo.
  - `go test -race ./...` passes.
  - New tests cover: `/thinking` with no arg returns current level + valid levels; `/thinking medium` sets the metadata and returns confirmation feedback; `/thinking foo` returns an "unknown level" feedback and does not mutate state; `/thinking` with no active stream returns an error message; the metadata is read by `buildInvokeOptions` (end-to-end test).
  - The existing `/help` listing must include `/thinking` automatically (the slash package auto-generates `/help` from the bound commands).
- **Details**:
  1. In `workshop/internal/app/app.go`, add a new type `thinkingCommand` with the same shape as `roleCommand` (lines 282-310): a mutex, a stream pointer, a `Handler` method, and a `SetStream` method.
  2. The `Handler` reads the current level from `c.stream.GetMetadata("workshop.thinking_level")`. If unset, the current level is `"medium"` (the runtime default — note: this is intentionally different from the *config-file* default of `"off"` so that the user can verify the level is "medium" via `/thinking` even when the config didn't set it. The runtime default is set in `buildInvokeOptions`; see Task 5).
  3. If `cmd.Input` is empty, return `Result.Feedback` listing the current level and the available levels. If the input is a valid level, call `c.stream.SetMetadata("workshop.thinking_level", parsedLevel)` and return `Result.Feedback` confirming the change. If the input is invalid, return `Result.Feedback` with the error and leave state unchanged.
  4. In `buildManager` (around line 414-426), construct the `thinkingCommand`, bind it, and call `tc.SetStream(stream)` in the `stepFactory` (parallel to `rc.SetStream(stream)` and `cc.SetStream(stream)` at line 430).
  5. The slash command does not need to interact with `buildInvokeOptions` directly. `SetMetadata` emits a `PropertiesEvent` automatically (`ore/session/stream.go:365-375`), and `buildInvokeOptions` is called on every turn from the `stepFactory`, which has access to the stream. The metadata is read at request time.
  6. In `workshop/internal/app/app_test.go`, add:
     a. `TestThinkingCommand_NoArgReportsCurrent` — handler returns the current level.
     b. `TestThinkingCommand_ValidLevelSetsMetadata` — handler writes the new level.
     c. `TestThinkingCommand_InvalidLevelNoOp` — handler returns error feedback and does not mutate.
     d. `TestThinkingCommand_NoStreamError` — handler returns an error when no stream is bound.
     e. `TestBuildInvokeOptions_ReadsThinkingLevelFromMetadata` — end-to-end test that `buildInvokeOptions` produces the right option type when the level is in metadata. This is the test that proves the slash command is wired into the request path.

### Task 7: Add the status bar indicator

- **Goal**: The TUI status bar shows the current thinking level alongside the existing model and role indicators. The indicator updates in real time as the user changes the level via `/thinking`.
- **Dependencies**: None (the wiring is mechanical; the TUI's `PropertiesEvent` consumer re-renders on every metadata change).
- **Files Affected**:
  - `workshop/internal/app/app.go` (the `WithStatusLabels` and `WithStatusZones` calls in `RunTUI` at lines 196-209)
- **New Files**: None.
- **Interfaces**: N/A.
- **Validation**:
  - `go build ./...` passes.
  - `go test -race ./...` passes.
  - A test verifies that the `WithStatusLabels` mapping in `RunTUI` includes `"workshop.thinking_level": "thinking"`. (This is a simple read-only assertion on the constructed TUI; see `ore/x/conduit/tui/model_test.go:1932` for the precedent on testing TUI updates from events.)
  - Manual visual check: run workshop, type `/thinking high`, confirm the status bar shows the new level. (Out of scope for automated tests; recorded in the commit message.)
- **Details**:
  1. In `workshop/internal/app/app.go`, locate the `tui.WithStatusLabels` call (line 209) and the `tui.WithStatusZones` call (line 196-208) inside `RunTUI`.
  2. Add `"workshop.thinking_level": "thinking"` to the labels map.
  3. Add `"workshop.thinking_level": "context"` to the zones map (same zone as `workshop.role`).
  4. The TUI's `model.status` is populated from `PropertiesEvent` events (see `ore/x/conduit/tui/model.go:280-298`); the new key flows through automatically once it is in the labels and zones maps.
  5. The visual position of the indicator is determined by the zone priority and the formatter; both already exist. No further TUI changes are required.

### Task 8: Update package-level docs and README

- **Goal**: Documentation describes the new `WithThinkingLevel` option, the per-level translation behavior, and the `/thinking` slash command. The example config shows the new field.
- **Dependencies**: Tasks 1-7.
- **Files Affected**:
  - `ore/x/provider/anthropic/doc.go`
  - `ore/x/provider/openai/doc.go`
  - `workshop/README.md`
  - `workshop/config.yaml.example`
- **New Files**: None.
- **Interfaces**: N/A.
- **Validation**:
  - The docs are internally consistent with the code.
  - `task validate` passes.
- **Details**:
  1. **`ore/x/provider/anthropic/doc.go`**: update the "Extended thinking" section. Replace references to `WithThinkingBudget(tokens int64)` with `WithThinkingLevel(provider.ThinkingLevel)`. Document the percentage mapping (2% / 8% / 25% / 50% / 80%) and the 1024 floor / (max_tokens - 1024) ceiling. Note that `"off"` or the empty level disables thinking.
  2. **`ore/x/provider/openai/doc.go`**: update the "Reasoning" section. Replace `WithReasoningEffort(string)` with `WithThinkingLevel(provider.ThinkingLevel)`. Document the mapping (`off` → no field, `minimal` → `low`, `low` → `low`, `medium` → `medium`, `high` → `high`, `max` → `high`).
  3. **`workshop/README.md`**: add a "Thinking level" subsection under the configuration section. Describe the levels, the default (`off`), and how the level interacts with each provider. Add a "Slash commands" subsection if not present, listing `/thinking` alongside `/role`, `/compact`, and `/name`. Note that the level is per-thread and persists across turns.
  4. **`workshop/config.yaml.example`**: the comments around the level field should say "Default: off" and link to the README section. The commented-out example for anthropic should show `thinking-level: medium` as a usage example.

## Dependency Graph

- Task 1 → Task 2 (anthropic adapter imports the new type)
- Task 1 → Task 3 (openai adapter imports the new type)
- Task 2 || Task 3 (the two adapter changes are independent and can be done in either order, in parallel, or by the same author)
- Task 2 → Task 4 (workshop's `anthropic.WithThinkingBudget` reference would fail to compile without Task 2; Task 4's build target assumes both adapter changes are landed)
- Task 3 → Task 4 (same for openai)
- Task 4 → Task 5 (`buildInvokeOptions` uses the new `ProviderConfig.ThinkingLevel` field, which Task 4 introduces)
- Task 5 → Task 6 (the slash command's effect is observable only when `buildInvokeOptions` reads metadata; without Task 5, `/thinking` would set metadata that nothing reads)
- Task 7 || Task 6 (the status bar wiring is independent of the slash command; both can land in either order, and the indicator works for the config-file default even before the slash command exists)
- (Task 6, Task 7) → Task 8 (docs reflect the final state)

Critical path: **Task 1 → (Task 2 || Task 3) → Task 4 → Task 5 → Task 6 → Task 8**. Task 7 can be parallelized with the critical path. In a single-author setting, the natural execution order is 1, 2, 3, 4, 5, 6, 7, 8 — each task builds on the previous and the plan ships as a sequence of atomic commits.

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| Removing `WithThinkingBudget` breaks downstream callers (anything importing the anthropic adapter outside the workshop) | Medium | Low | A `grep -rn "WithThinkingBudget" /home/andrewhowdencom/Development` shows only the workshop and the ore test file. Both are updated by Tasks 2 and 4. External consumers of the `ore` module would need to migrate, but the module is `v0.x` and the migration is mechanical. |
| Removing `WithReasoningEffort` breaks downstream callers | Medium | Low | Same as above. Grep confirms only the workshop callsite. |
| The level percentages produce budgets the model rejects (e.g. too small or too large) | High | Low | The 1024 floor and `(max_tokens - 1024)` ceiling handle the common rejection cases. The percentages are deliberately conservative (max 80%) so the visible response always has room. If a particular model rejects a particular budget, the fix is to adjust the percentage table in `translateThinkingLevel` — a one-line change in the adapter, not a config change. |
| The user sets a level that doesn't make sense for the model (e.g. `max` on a model that doesn't support thinking) | Low | Medium | The translation is silent — the level is passed to the wire and the model ignores what it doesn't support. The user sees no thinking, which matches the current behavior. A future improvement could be a per-model allowlist (out of scope). |
| The slash command mutates state outside the user's intent (e.g. typo sets the level to `off` and the user doesn't notice) | Low | Low | The handler validates the level before writing; an unknown level produces feedback and does not mutate. A successful set produces a confirmation feedback message. The status bar (Task 7) makes the change visible at a glance. |
| The status bar is too crowded with both `role` and `thinking` indicators | Low | Medium | The existing zone layout groups role in the `context` zone. The thinking level is added to the same zone. If the bar overflows, the existing zone-priority logic handles truncation. A polish task (out of scope) could add a separator or rename one indicator. |
| `task validate` flags a pre-existing lint issue unrelated to this plan | Low | Low | Out of scope. The plan's commits do not introduce new lint issues; if the gate fails on a pre-existing issue, address in a separate plan. |
| The `SetMetadata` → `PropertiesEvent` → TUI render path has a race condition that causes the status bar to lag | Low | Low | The TUI is Bubble Tea; updates are serialized on the event loop. The PropertiesEvent is emitted from `SetMetadata` synchronously, and the TUI processes it on the next tick. Worst case: a one-frame lag, which is invisible to the user. |
| A user has the old `thinking-budget` or `reasoning-effort` keys in their config and the new workshop rejects their config | Medium | Medium | Per the project's no-backwards-compat policy, this is expected. The example config documents the new shape. Users with old configs will see a YAML parse warning on `task validate` (workshop does not currently error on unknown keys, so the run will proceed with the new field's default). Document this in the commit message and the README. |
| The plan grows to include per-model reasoning-capability detection (a "while we're here" temptation) | Medium | Medium | Explicitly out of scope. The default `"off"` is preserved; the user opts in via the config or `/thinking`. The capability-detection work is its own design conversation. |
| The new `thinkingLevelOption` is silently ignored by an existing consumer of the anthropic adapter that doesn't yet know about the level | Low | Low | The framework contract is that adapters ignore unknown options (`ore/provider/provider.go:11-15`). The new option is recognized by both adapters in this plan; the workshop call site is updated in Task 4. |
| `ParseThinkingLevel` rejects a level that should be accepted (e.g. uppercase `"MEDIUM"`) | Low | Low | Decision: reject. Levels are case-sensitive lowercase. The status bar and `/help` listing show the canonical form; users who try uppercase get a clear error. The choice is documented in the `ParseThinkingLevel` doc comment. |

## Validation Criteria

- [ ] `ore/provider/provider.go` (or a new file in the same package) defines `ThinkingLevel` with the six constants, a `Valid()` method, and a `ParseThinkingLevel(string)` helper.
- [ ] `ore/provider/thinking_test.go` (or appended `provider_test.go`) covers valid round-trip, `Valid()` for all six, `ParseThinkingLevel` accept/reject, and unknown level error.
- [ ] `ore/x/provider/anthropic/anthropic.go` exports `WithThinkingLevel(provider.ThinkingLevel)` and no longer exports `WithThinkingBudget`. The translation helper enforces the 1024 floor and the `(max_tokens - 1024)` ceiling.
- [ ] `ore/x/provider/openai/openai.go` exports `WithThinkingLevel(provider.ThinkingLevel)` and no longer exports `WithReasoningEffort`. The translation helper clamps `minimal → low` and `max → high`.
- [ ] `workshop/internal/app/app.go` `ProviderConfig` exposes `ThinkingLevel` and no longer exposes `ThinkingBudget` or `ReasoningEffort`. The `MaxTokens <= ThinkingBudget` warning is removed.
- [ ] `workshop/cmd/workshop/root.go` binds `--provider.thinking-level` (string, default `"off"`) and no longer binds `--provider.thinking-budget` or `--provider.reasoning-effort`.
- [ ] `workshop/cmd/workshop/config.go` `buildConfigMap` writes `thinking-level` and no longer writes the old keys.
- [ ] `workshop/config.yaml.example` shows the new field and no longer shows the old ones.
- [ ] `workshop/internal/app/app.go` `buildInvokeOptions` reads the level from `ProviderConfig.ThinkingLevel` and forwards it via `anthropic.WithThinkingLevel` or `openai.WithThinkingLevel` based on provider kind. The default `"off"` is applied when the field is empty.
- [ ] `workshop/internal/app/app.go` defines `thinkingCommand` and binds it as `/thinking` on the slash registry. The handler validates the level, writes metadata under `"workshop.thinking_level"`, and emits a feedback message. With no argument, the handler reports the current level and the available levels.
- [ ] `workshop/internal/app/app.go` `RunTUI` adds `"workshop.thinking_level": "thinking"` to the `WithStatusLabels` mapping and `"workshop.thinking_level": "context"` to the `WithStatusZones` mapping.
- [ ] `ore/x/provider/anthropic/doc.go` documents the new `WithThinkingLevel` option, the percentage mapping, the 1024 floor, and the `(max_tokens - 1024)` ceiling.
- [ ] `ore/x/provider/openai/doc.go` documents the new `WithThinkingLevel` option and the clamps.
- [ ] `workshop/README.md` documents `provider.thinking-level` and the `/thinking` slash command.
- [ ] All existing tests pass after each task. Specifically:
  - `go test -race ./provider/...` in `ore` passes after Task 1.
  - `go test -race ./x/provider/...` in `ore` passes after Tasks 2 and 3.
  - `go test -race ./...` in `workshop` passes after Tasks 4-7.
- [ ] `task validate` passes after each task in the workshop repo.
- [ ] A grep for `WithThinkingBudget` and `WithReasoningEffort` returns no production callers (only the slash package's own docs may reference them in a deprecation note if any).
- [ ] The plan's commits are atomic per task. Each commit leaves the build green.
- [ ] No changes outside `ore/provider/`, `ore/x/provider/{anthropic,openai}/`, `workshop/internal/app/`, `workshop/cmd/workshop/`, `workshop/config.yaml.example`, and `workshop/README.md`. No changes to `.worktrees/`, `workshop/.worktrees/`, the slash package itself, or any other module.
