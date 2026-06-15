# Plan: Add Anthropic Provider Support

## Objective
Wire the new `x/provider/anthropic` package from `ore` PR #439 into the
`workshop` app as a first-class provider. Users will be able to run workshop
against Anthropic native (Claude) or OpenRouter's `/api/v1/messages` mirror by
setting `WORKSHOP_PROVIDER_KIND=anthropic`, with first-class configuration
surface (flags, env vars, config file) for `MaxTokens` and `ThinkingBudget`.

## Context
`workshop` is the consumer of `ore`. Today it only knows the
`ore/x/provider/openai` provider — `internal/app/app.go:newProvider` switches
on `cfg.provider.Kind` and the `stepFactory` builds per-invocation options
hard-coded to `openai.WithTools/Temperature/ReasoningEffort`. The CLI
(`cmd/workshop/root.go`) declares the `provider.*` flags; `makeProviderConfig`
maps viper values into `app.ProviderConfig`; `cmd/workshop/config.go:buildConfigMap`
emits the YAML on `config init`.

`ore` PR #439 lands:
- A new `x/provider/anthropic` module
  (`github.com/andrewhowdencom/ore/x/provider/anthropic`) wrapping
  `github.com/anthropics/anthropic-sdk-go v1.50.1`.
- Artifact additions: `artifact.ReasoningSignature{Provider, SubKind, Data}`
  and `Usage.ThinkingTokens` (for thinking round-trip).
- A new `go.work` entry and corresponding `x/provider/anthropic/go.mod`.

The new provider's public surface (verified from the PR diff):
- Constructor: `anthropic.New(opts ...Option) (*Provider, error)`
- Construction options: `WithAPIKey`, `WithModel`, `WithBaseURL`,
  `WithHTTPClient`, `WithTracer`.
- Per-invocation options: `WithTools`, `WithTemperature`, `WithMaxTokens`
  (required by Anthropic), `WithThinkingBudget` (soft cap on extended thinking).
- Auth header is selected at construction from the base URL substring
  (`openrouter` → `Authorization: Bearer`; otherwise → `x-api-key`).

Workshop's `replace` directives in `go.mod` all point at a local `../ore`
checkout, so once #439 is merged locally the new module is available via a
matching `replace`.

Files relevant to this plan (all paths verified during discovery):
- `go.mod` — module + replace directives
- `go.sum` — auto-managed by `go mod tidy`
- `internal/app/app.go` — `ProviderConfig` (line 57), `stepFactory` (line 413),
  `invokeOpts` (line 472-477), `newProvider` (line 586-606), warning site
  in `newProvider`, `coAuthoredByTrailer` (line 792, already
  provider-agnostic)
- `internal/app/app_test.go` — `TestNewProvider_*` (line 42-72)
- `cmd/workshop/root.go` — flag declarations in `init()` (line 30+),
  `makeProviderConfig` (line 110)
- `cmd/workshop/config.go` — `buildConfigMap` (line 60+)
- `cmd/workshop/config_test.go` — `TestRunConfigInitWithPath_WritesCorrectYAML`
- `README.md` — flag/env table (line ~251), prerequisites, YAML example,
  usage section

Project conventions observed:
- Backwards-compat: empty `Kind` maps to openai (default `provider.kind: openai`).
- Config precedence: flag > env > config file > built-in default.
- New config keys use kebab-case (`base-url`, `reasoning-effort`); Go field
  names use PascalCase.
- `task validate` runs lint, test, build — that's the gate for each commit.

## Architectural Blueprint
Extend the existing provider switch in `newProvider` to recognize
`Kind == "anthropic"`. The provider uses Anthropic's required `max_tokens`
field on every request, so default it to 32000 (overridable). Surface
`ThinkingBudget` as a separate field; the user opts in to extended thinking
by setting it > 0. Warn at construction when `MaxTokens <= ThinkingBudget`,
since that configuration would consume the entire output budget on thinking
and produce no visible response.

Extract a `buildInvokeOptions(cfg ProviderConfig, tools []tool.Tool) []provider.InvokeOption`
helper that branches on `cfg.provider.Kind`. This keeps the per-invocation
plumbing in one place and makes it easy to add a third provider later. The
helper applies:

- `WithTools(tools)` — both kinds
- `WithTemperature(t)` — both kinds, only when `t != 0`
- `WithMaxTokens(n)` — anthropic only, only when `n > 0`
- `WithThinkingBudget(n)` — anthropic only, only when `n > 0`
- `WithReasoningEffort(e)` — openai only, only when `e != ""`

Single-kind, two-host strategy: the new package auto-selects auth header from
the base URL. There is no separate `kind: openrouter` — users set
`WORKSHOP_PROVIDER_BASE_URL=https://openrouter.ai/api/v1` and the provider
does the right thing.

`coAuthoredByTrailer` and the system prompt's "Provider backend" line are
already provider-agnostic (verified — the `TestCoAuthoredByTrailer` table
already includes `Kind: "anthropic"`). No change there.

## Requirements
1. Land `ore` PR #439 in `ore` (or have it available in the local `../ore`
   checkout) before starting work.
2. Add `github.com/andrewhowdencom/ore/x/provider/anthropic` to `go.mod` as a
   direct dependency with a matching local `replace` directive.
3. Extend `app.ProviderConfig` with `MaxTokens int64` and `ThinkingBudget int64`.
4. Extend `newProvider` to construct an `anthropic.Provider` for
   `Kind == "anthropic"`, with a `slog.Warn` when `MaxTokens <= ThinkingBudget`.
5. Extract `buildInvokeOptions(cfg, tools)` from `stepFactory` and make it
   kind-aware.
6. Add `--provider.max-tokens` and `--provider.thinking-budget` persistent
   flags (and matching env / config keys) to `cmd/workshop/root.go`.
7. Extend `makeProviderConfig` and `buildConfigMap` to round-trip the new
   fields.
8. Default `MaxTokens=32000` for the anthropic kind when unset; default
   `ThinkingBudget=0` (i.e. thinking disabled by default). [inferred]
9. Gate `WithReasoningEffort` to the openai kind only.
10. Add unit tests: new `newProvider` branch (mirror existing
    `TestNewProvider_*` patterns), `buildInvokeOptions` dispatch, config
    init YAML output, and a smoke test that constructs the anthropic
    provider.
11. Update `README.md`: prerequisites (mention Anthropic), flag/env table
    rows for the new flags, YAML example with new keys, two usage examples
    (Anthropic native, OpenRouter).
12. `task validate` must pass after the change.

## Task Breakdown

### Task 1: Add the anthropic module to go.mod
- **Goal**: Make the new `ore/x/provider/anthropic` package importable in
  workshop.
- **Dependencies**: `ore` PR #439 merged into the local `../ore` checkout.
- **Files Affected**: `go.mod`, `go.sum`.
- **New Files**: None.
- **Interfaces**: No Go code changes.
- **Validation**:
  - `go mod tidy` completes cleanly.
  - `grep -F 'github.com/andrewhowdencom/ore/x/provider/anthropic' go.mod`
    shows a `require` line and a `replace` line.
  - `go list -m github.com/andrewhowdencom/ore/x/provider/anthropic`
    resolves without error.
  - `go build ./...` still passes (no consumers yet, but a no-op smoke
    check).
- **Details**:
  1. Confirm the local `../ore/x/provider/anthropic` directory exists and
     is non-empty (i.e. PR #439 is in the local tree).
  2. Add to `go.mod` under the `require` block:
     ```
     github.com/andrewhowdencom/ore/x/provider/anthropic v0.x.x
     ```
     Use the version `go get` pins; if no upstream tag exists yet, the
     pseudo-version from the local `replace` is acceptable.
  3. Add a matching `replace` line, mirroring the existing entries:
     ```
     replace github.com/andrewhowdencom/ore/x/provider/anthropic => ../ore/x/provider/anthropic
     ```
  4. Run `go mod tidy` to refresh `go.sum`.
  5. Do **not** add an import anywhere yet — that's Task 2. This task is
     about wiring the module, not consuming it.

### Task 2: Extend ProviderConfig and add the anthropic branch to newProvider
- **Goal**: Construct an `anthropic.Provider` when
  `cfg.ProviderConfig.Kind == "anthropic"`; surface the misconfig warning.
- **Dependencies**: Task 1.
- **Files Affected**: `internal/app/app.go` (add anthropic import, extend
  `ProviderConfig`, add `case "anthropic":` to `newProvider`, add
  `slog.Warn` for the `MaxTokens <= ThinkingBudget` condition).
- **New Files**: None.
- **Interfaces**:
  - `ProviderConfig` gains two fields:
    ```go
    MaxTokens     int64 // hard cap on output tokens; required for anthropic
    ThinkingBudget int64 // soft cap on extended-thinking tokens; 0 = disabled
    ```
  - `newProvider` signature unchanged.
- **Validation**:
  - `go build ./...` passes.
  - New unit tests in `internal/app/app_test.go`:
    - `TestNewProvider_Anthropic_MissingAPIKey`: `Kind: "anthropic", Model: "claude-sonnet-4-5"` → error contains "api_key".
    - `TestNewProvider_Anthropic_MissingModel`: `Kind: "anthropic", APIKey: "sk-ant-..."` → error contains "model".
    - `TestNewProvider_Anthropic_Constructs`: valid config → non-nil provider. Smoke test; do not exercise the network.
    - `TestNewProvider_Anthropic_OpenRouterBaseURL`: pass `BaseURL: "https://openrouter.ai/api/v1"`, valid config → non-nil. The auth dispatch itself is verified by the new package's own tests.
    - `TestNewProvider_Anthropic_WarnsOnMaxTokensLeqThinkingBudget`: construct with `MaxTokens: 1000, ThinkingBudget: 2000`. Capture log output via `slog.SetDefault` with a buffer handler; assert the warning is emitted. (If injecting a logger is too invasive, a code comment near the warning is acceptable; prefer the test.)
- **Details**:
  1. Add `"github.com/andrewhowdencom/ore/x/provider/anthropic"` to the
     imports in `internal/app/app.go`.
  2. Extend `ProviderConfig` (currently at line 57) with the two new
     fields. Document each in a comment that disambiguates from
     `CompactionConfig.MaxTokens` (which is a token budget for compaction,
     not a per-request output cap).
  3. In `newProvider` (line 586), add a `case "anthropic":` arm following
     the openai arm. Mirror its structure: validate `APIKey` and `Model`,
     then build `[]anthropic.Option` and call `anthropic.New(opts...)`.
     Apply `WithBaseURL` when non-empty, `WithTracer` when non-nil.
  4. Inside the new case, **before** calling `anthropic.New`, emit the
     warning:
     ```go
     if pc.MaxTokens > 0 && pc.ThinkingBudget > 0 && pc.MaxTokens <= pc.ThinkingBudget {
         slog.Warn("provider.max-tokens is <= provider.thinking-budget; the model may exhaust its output budget on thinking and produce no visible response",
             "max_tokens", pc.MaxTokens, "thinking_budget", pc.ThinkingBudget)
     }
     ```
     Use the same `slog` package the rest of the app uses. Do not error
     out — this is a warning, not a hard failure.
  5. Apply the default: if `Kind == "anthropic"` and `MaxTokens == 0`,
     set `MaxTokens = 32000` before passing to the constructor. Do this
     in `newProvider`, not in `buildInvokeOptions` (which sees the
     per-invocation options, not the construction-time config).
  6. Tests mirror the existing `TestNewProvider_*` patterns in
     `internal/app/app_test.go` (line 42-72). Use the table style if
     it shortens the test.

### Task 3: Extract buildInvokeOptions and make it kind-aware
- **Goal**: Per-invocation options are built in one place, with the right
  options per provider kind.
- **Dependencies**: Task 2.
- **Files Affected**: `internal/app/app.go` (add `buildInvokeOptions`
  function, refactor `stepFactory` to call it), `internal/app/app_test.go`
  (add unit tests for the helper).
- **New Files**: None.
- **Interfaces**:
  - New function:
    ```go
    func buildInvokeOptions(cfg *config, tools []tool.Tool) []provider.InvokeOption
    ```
    Returns the slice of `provider.InvokeOption` that `stepFactory` will
    pass to `loop.WithInvokeOptions`.
- **Validation**:
  - `go build ./...` passes.
  - All existing tests still pass (behavioral parity for the openai path).
  - New unit tests for `buildInvokeOptions`:
    - `TestBuildInvokeOptions_OpenAI_IncludesToolsAndReasoningEffort`:
      `Kind: "openai", Tools: [...]`, `ReasoningEffort: "medium"` →
      output contains exactly the right options (count, or use reflection
      on option types if accessible).
    - `TestBuildInvokeOptions_OpenAI_OmitsReasoningEffortWhenEmpty`:
      `ReasoningEffort: ""` → no reasoning-effort option in output.
    - `TestBuildInvokeOptions_Anthropic_IncludesMaxTokens`:
      `Kind: "anthropic", MaxTokens: 16000, ThinkingBudget: 8000` →
      contains a `maxTokensOption` with the right value and a
      `thinkingBudgetOption` with the right value.
    - `TestBuildInvokeOptions_Anthropic_OmitsThinkingBudgetWhenZero`:
      `ThinkingBudget: 0` → no thinking-budget option.
    - `TestBuildInvokeOptions_Anthropic_OmitsTemperatureWhenZero`:
      `Temperature: 0` → no temperature option (same behavior as the
      existing openai path).
    - `TestBuildInvokeOptions_Anthropic_OmitsReasoningEffort`:
      `Kind: "anthropic", ReasoningEffort: "high"` → output does **not**
      include a reasoning-effort option. This locks in the
      kind-gating decision.
- **Details**:
  1. Add the new function above `newProvider` (around line 585). The
     function takes `*config` and `[]tool.Tool` and returns
     `[]provider.InvokeOption`.
  2. Inside, switch on `cfg.provider.Kind`:
     - `case "anthropic"`:
       - Append `anthropic.WithTools(tools)`.
       - If `cfg.provider.Temperature != 0`, append `anthropic.WithTemperature(...)`.
       - If `cfg.provider.MaxTokens > 0`, append `anthropic.WithMaxTokens(...)`.
         (The default is applied in `newProvider`, so by the time we
         reach the helper on the anthropic path, `MaxTokens` is always
         > 0. We still guard defensively in case the defaulting policy
         changes.)
       - If `cfg.provider.ThinkingBudget > 0`, append `anthropic.WithThinkingBudget(...)`.
     - `case "", "openai"` (default):
       - Mirror the existing behavior at line 472-477: `openai.WithTools`,
         `openai.WithTemperature` (when non-zero), `openai.WithReasoningEffort`
         (when non-empty).
  3. In `stepFactory` (line 413), replace the openai-specific `invokeOpts`
     construction (line 472-477) with a single call:
     ```go
     invokeOpts := buildInvokeOptions(cfg, registry.Tools())
     ```
  4. The `openai` import is still used by `newProvider`, so do not remove
     it from the import list.
  5. Tests for the helper: the option types returned by the openai
     package are unexported (e.g. `temperatureOption`, `reasoningEffortOption`),
     so test by stringifying via `fmt.Sprintf("%T", opt)` or by
     asserting the count of options for a given config shape. The
     anthropic types (`temperatureOption`, `maxTokensOption`,
     `thinkingBudgetOption`) are similarly unexported, so the same
     stringification trick applies. If type-asserting is too brittle,
     test by count and by config shape — e.g. "for this config, the
     result has exactly N options" — and rely on the smoke test in
     Task 2 plus the package's own tests for correctness of the option
     values.

### Task 4: Add CLI flags, env bindings, and config init fields
- **Goal**: `provider.max-tokens` and `provider.thinking-budget` are
  first-class config knobs.
- **Dependencies**: Task 2 (so `makeProviderConfig` can read the new
  fields). Does not require Task 3 — the config plumbing and the
  invoke-options refactor are independent.
- **Files Affected**:
  - `cmd/workshop/root.go` — add two `PersistentFlags()` in `init()`;
    extend `makeProviderConfig`.
  - `cmd/workshop/config.go` — extend `buildConfigMap`.
  - `cmd/workshop/config_test.go` — extend
    `TestRunConfigInitWithPath_WritesCorrectYAML` to assert the new
    keys land in the YAML.
- **New Files**: None.
- **Interfaces**:
  - New flags (in `init()`):
    ```
    --provider.max-tokens        (int64, default 0)
    --provider.thinking-budget   (int64, default 0)
    ```
  - `makeProviderConfig` populates the two new `ProviderConfig` fields
    via `viper.GetInt64("provider.max-tokens")` and
    `viper.GetInt64("provider.thinking-budget")`.
  - `buildConfigMap` writes `max-tokens` and `thinking-budget` into the
    `provider` sub-map. Use `int64` values; `yaml.Marshal` renders
    them as integers.
- **Validation**:
  - `go build ./...` passes.
  - `TestRunConfigInitWithPath_WritesCorrectYAML` extended to assert
    `max-tokens` and `thinking-budget` are present in the YAML. Use
    the existing pattern of `setViperValue` (note: it's currently typed
    for `string`; for `int64` values, use `viper.Set` directly with a
    `t.Cleanup` to restore).
  - `go run ./cmd/workshop --help` lists the two new flags.
  - `WORKSHOP_PROVIDER_MAX_TOKENS=16000 WORKSHOP_PROVIDER_THINKING_BUDGET=8000 go run ./cmd/workshop` parses without error (does not need a valid API key to verify flag parsing — pre-empt the API-key check with a dummy `WORKSHOP_PROVIDER_API_KEY=sk-test`).
- **Details**:
  1. In `root.go:init()`, add the two new persistent flags immediately
     after the existing `provider.reasoning-effort` line. Match the
     existing flag-help style:
     ```
     rootCmd.PersistentFlags().Int64("provider.max-tokens", 0, "Maximum output tokens per request (anthropic only; 0 = use provider default)")
     rootCmd.PersistentFlags().Int64("provider.thinking-budget", 0, "Extended-thinking token budget (anthropic only; 0 = disabled)")
     ```
  2. In `makeProviderConfig` (line 110), add the two new field
     initializers.
  3. In `config.go:buildConfigMap` (line 60+), add the two new keys to
     the `provider` sub-map.
  4. In `config_test.go:TestRunConfigInitWithPath_WritesCorrectYAML`,
     add `setViperInt64Value` (or use `viper.Set` directly with a
     cleanup) and assert the new YAML keys. Mirror the existing
     `temperature` / `reasoning-effort` assertions.
  5. Viper's `SetEnvKeyReplacer` already converts dots to underscores,
     so the env var names are `WORKSHOP_PROVIDER_MAX_TOKENS` and
     `WORKSHOP_PROVIDER_THINKING_BUDGET`. Document this in the
     README in Task 5.

### Task 5: Update README and verify end-to-end
- **Goal**: The new provider is documented; the whole change passes
  validation.
- **Dependencies**: Tasks 1-4.
- **Files Affected**: `README.md`.
- **New Files**: None.
- **Interfaces**: N/A.
- **Validation**:
  - `task validate` passes (`lint`, `test`, `build`).
  - Manual smoke (documented in the plan but not gated on it):
    `WORKSHOP_PROVIDER_KIND=anthropic WORKSHOP_PROVIDER_API_KEY=sk-ant-... WORKSHOP_PROVIDER_MODEL=claude-sonnet-4-5 go run ./cmd/workshop` opens the TUI without error. If a real Anthropic key is not available, a `sk-ant-test` key is enough to verify the wiring (it will fail at first request, but flag parsing, provider construction, and TUI boot all succeed).
- **Details**:
  1. **Prerequisites** (around line 16): change "An OpenAI API key (or
     compatible API endpoint)" to mention Anthropic and OpenRouter
     alongside. e.g. "An API key for OpenAI, Anthropic, or a compatible
     API endpoint (e.g. OpenRouter)".
  2. **YAML example** (around line 168-186): add `max-tokens` and
     `thinking-budget` to the `provider:` block, with comments noting
     they apply to the anthropic kind.
  3. **Flag/env table** (around line 251): add two rows for
     `--provider.max-tokens` and `--provider.thinking-budget`.
  4. **Usage examples**: add a new section after the existing TUI
     usage showing two variants:
     - Anthropic native
     - OpenRouter via base URL
  5. Do **not** change the default model (`gpt-4o` stays). Do **not**
     change the default kind (`openai` stays). The new provider is
     opt-in via `WORKSHOP_PROVIDER_KIND=anthropic`.
  6. Run `task validate` to confirm the full test + lint + build
     pipeline passes.

## Dependency Graph
- Task 1 → Task 2 (cannot import the package before it's in go.mod)
- Task 2 → Task 3 (Task 3 references the new anthropic options, which
  are introduced in Task 2's import)
- Task 2 → Task 4 (Task 4 reads the new `ProviderConfig` fields)
- Task 3 || Task 4 (independent; can be done in either order, or in
  parallel branches)
- Task 3, Task 4 → Task 5 (Task 5 documents the final state and runs
  the full validation suite)

## Risks & Mitigations
| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| `ore` PR #439 not yet merged when Task 1 starts | High (blocks Task 1) | Medium | Confirm with the user before starting that the PR is in the local `../ore` checkout. If not, this plan is blocked until it is. |
| New `x/provider/anthropic` module has no upstream tag, so `go mod tidy` picks a pseudo-version | Low | High | Expected; the `replace` directive pins the source to `../ore/x/provider/anthropic` regardless of the version. Document the pseudo-version in the commit message so reviewers know it's intentional. |
| Persisted threads from before the merge lack `ReasoningSignature` artifacts; resuming against a thinking-capable Anthropic model silently loses prior thinking | Low | High | Documented in the plan context. The round-trip works for new turns; loss is limited to thinking content that pre-dates the schema. A migration tool is out of scope. |
| `MaxTokens <= ThinkingBudget` warning is easy to forget to test | Low | Medium | Task 2 includes an explicit test for it (either via captured `slog` output or via a unit assertion that the warning code path is reached). |
| Builder sets the new `MaxTokens` default inconsistently (in `newProvider` vs. `buildInvokeOptions` vs. flag default) | Medium | Medium | The plan specifies the layering: flag default = 0, `newProvider` applies the 32000 default for the anthropic kind when 0, `buildInvokeOptions` always uses whatever `ProviderConfig` says. Task 5's `task validate` is the gate. |
| The `MaxTokens` field name on `ProviderConfig` collides conceptually with `CompactionConfig.MaxTokens` | Low | High | Different types, different config keys (`provider.max-tokens` vs. `compaction.max-tokens`); document each in a Go field comment. README YAML example shows the namespacing. |
| Tests for `buildInvokeOptions` are brittle because the per-provider option types are unexported | Medium | High | Task 3 specifies the fallback (assert by count and config shape; rely on the new package's own tests for option-value correctness). |
| Default model flag (`gpt-4o`) becomes misleading once anthropic is first-class | Low | Low | Out of scope; default model stays `gpt-4o` for backwards compat. The README's new usage examples show how to set the model explicitly. |
| `coAuthoredByTrailer` or the system prompt's "Provider backend" line misbehave for the new kind | Low | Low | Verified during discovery: `TestCoAuthoredByTrailer` already includes `Kind: "anthropic"`; the system prompt's "Provider backend" line concatenates `cfg.provider.Kind` verbatim. No change needed. |
| `task validate` is currently red on pre-existing `errcheck` issues (per the PR description for `ore`'s examples) | Low | Low | Out of tree — those issues are in `ore`'s `examples/`, not in `workshop`. Workshop's own lint baseline should be unaffected. Confirm in Task 5. |

## Validation Criteria
- [ ] `go.mod` contains a `require` line and a matching `replace` line for
      `github.com/andrewhowdencom/ore/x/provider/anthropic`.
- [ ] `go mod tidy` completes without error.
- [ ] `go build ./...` passes.
- [ ] `go test -race ./...` passes (full suite, not just touched files).
- [ ] `golangci-lint run ./...` is clean.
- [ ] `app.ProviderConfig` has `MaxTokens int64` and `ThinkingBudget int64`,
      with field comments disambiguating them from `CompactionConfig.MaxTokens`.
- [ ] `newProvider` constructs an `anthropic.Provider` for
      `Kind == "anthropic"` and emits a `slog.Warn` when
      `MaxTokens > 0 && ThinkingBudget > 0 && MaxTokens <= ThinkingBudget`.
- [ ] `buildInvokeOptions` is a function in `internal/app/app.go` and is
      called by `stepFactory`. It produces different option sets for
      `Kind == "openai"` vs. `Kind == "anthropic"`.
- [ ] `WithReasoningEffort` is not applied when `Kind == "anthropic"`.
- [ ] `--provider.max-tokens` and `--provider.thinking-budget` are
      declared as persistent flags, are bound to viper, and round-trip
      through `makeProviderConfig` and `buildConfigMap`.
- [ ] `WORKSHOP_PROVIDER_MAX_TOKENS` and
      `WORKSHOP_PROVIDER_THINKING_BUDGET` are recognized env vars.
- [ ] `TestRunConfigInitWithPath_WritesCorrectYAML` asserts the new YAML
      keys.
- [ ] `README.md` lists the new flags in the flag/env table, mentions
      Anthropic and OpenRouter in the prerequisites, includes
      `max-tokens` and `thinking-budget` in the YAML example, and has
      at least one usage example for the anthropic kind.
- [ ] Default `MaxTokens=32000` is applied for the anthropic kind when
      the field is zero; default `ThinkingBudget=0` is honored (no
      thinking by default).
- [ ] `task validate` passes from a clean checkout.
