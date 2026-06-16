# Plan: Refactor `workshop` config to named providers

## Objective

Reshape `workshop`'s `config.yaml` (and its viper / env / Go config wiring) so that providers are defined once as a named map and the rest of the config references them by name. The minimum scope is the inference provider and the compaction provider; the layout must be additive for future consumers (e.g. strategy choice, per-invocation output budget) without re-doing this refactor. This is a **hard break** with the existing flat `provider.*` viper keys and `WORKSHOP_PROVIDER_*` env vars; no deprecation shim is preserved.

## Context

Findings from Phases 1-2 of the planner methodology.

### Current state

- `workshop` is a single Go module (`github.com/andrewhowdencom/workshop`, go 1.26.2). Relevant code lives in two packages:
  - `cmd/workshop/` — CLI entrypoint. `root.go` (TUI/stdio invocation, viper wiring, `makeProviderConfig()`), `http.go` (HTTP conduit invocation, same viper wiring), `config.go` (`buildConfigMap` for `workshop config init`).
  - `internal/app/` — application wiring. `app.go` holds `ProviderConfig`, the `config` struct, `newProvider`, `newCompactor`, `buildInvokeOptions`, `buildManager`, `coAuthoredByTrailer`, the system prompt "model" sentence, and the slash command handlers.
- The config has a single `provider:` block with fields: `kind`, `api-key`, `model`, `base-url`, `temperature`, `thinking-level`, `max-tokens`. All read flat from viper, collapsed into `app.ProviderConfig` by `makeProviderConfig()`, passed to `app.WithProvider()`, then to a single `prov` instance built once in `buildManager`.
- `newCompactor(cfg.compaction, prov)` reuses the same `prov` for both inference and compaction — no way to split them today.
- `compaction.max-tokens` (existing) controls the **trigger threshold** (when to compact), distinct from the new `SummarizeStrategy.MaxTokens` (per-invocation output budget, currently hard-coded to 8192 in `ore` and **not** in `config.yaml`).
- Env-var mapping: `WORKSHOP_PROVIDER_<FIELD>` via `viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))` (`cmd/workshop/root.go:63`). Verified in `TestSetupViper` (`cmd/workshop/root_test.go:56`).
- `cfg.provider.MaxTokens` is mutated by `newProvider` (Anthropic default) and re-read by `buildInvokeOptions`. The by-pointer signature is load-bearing and has a regression test (`TestNewProvider_Anthropic_AppliesDefaultMaxTokens`).

### The seam on the `ore` side is already ready

PR #444 landed `SummarizeStrategy` taking a `provider.Provider` plus a `provider.WithMaxTokens(...)` option, with no imports of concrete adapters. The compaction package is fully provider-agnostic. What's missing is on the `workshop` side: the config layer can't express "use a different model for compaction."

### Coupling inventory (must be untangled in this refactor)

| Consumer | Reads | Currently |
|---|---|---|
| `buildInvokeOptions` (per-turn) | `cfg.provider.{Kind, Temperature, ThinkingLevel, MaxTokens}` | Inference provider's fields |
| `coAuthoredByTrailer` / `makeGitCommitHandler` | `cfg.provider.{Model, Kind}` | Inference provider's fields |
| System prompt "model" sentence | `cfg.provider.{Model, Kind}` | Inference provider's fields |
| `newProvider` (called once in `buildManager`) | `cfg.provider` | Compiled to a single `provider.Provider` |
| `newCompactor` (called once in `buildManager`) | `cfg.compaction.MaxTokens` + the same `prov` | Same provider as inference |
| `processor` closure (per-turn) | `compactor` (closure capture) | Triggers compaction, uses compactor's own provider |

The `processor` closure captures the inference `prov` and uses the separately-built `compactor`. After the refactor, the compactor's provider is a *different* `provider.Provider` instance from the inference one, but the closure is unchanged. The git_commit tool, the system prompt "model" sentence, and `buildInvokeOptions` all move to read from the **default (inference) named provider**.

### Decided design points (from ideation)

1. `provider:` is a *required* string name reference into the `providers:` map. Single source of truth — no shorthand sugar.
2. Env vars use the per-named-provider pattern: `WORKSHOP_PROVIDER_<UPPER_NAME>_<FIELD>` (e.g. `WORKSHOP_PROVIDER_HAIKU_API_KEY`).
3. Validate every defined named provider at startup (every name's required fields are checked regardless of whether it's referenced).
4. `compaction.provider` is optional; omitted ⇒ resolve to the default inference provider. `compaction.max-tokens == 0` remains the compaction-disable switch.

## Architectural Blueprint

### Selected architecture

```
config.yaml
├─ provider: <default-name>                 # required: name of inference provider
├─ providers:
│   └─ <name>:
│       ├─ kind: openai | anthropic
│       ├─ api-key: ...
│       ├─ model: ...
│       ├─ base-url: ...
│       ├─ temperature: 0
│       ├─ thinking-level: off
│       └─ max-tokens: 0
└─ compaction:
    ├─ provider: <name>                     # optional; defaults to <default-name>
    └─ max-tokens: 100000                   # trigger threshold; 0 = disabled
```

### Pipeline (inside `buildManager`)

1. **Validate** — iterate over every key of `providers:`. For each, require non-empty `api-key` and `model`, and validate `kind ∈ {"", "openai", "anthropic"}`. Then check that `provider: <default>` and `compaction.provider: <compaction>` (if set) reference defined names. Fail loud on any of the above.
2. **Compile** — for each defined name, call `newProvider(name, pc, tracer)` to get a `provider.Provider`. Store in `map[string]provider.Provider`. (The existing `newProvider` is renamed; the Anthropic default-MaxTokens mutation continues to apply to the copy held in `cfg.providers[name]`.)
3. **Resolve** — `inferenceProv = compiled[defaultName]`; `compactionName = compactionCfg.Provider` (or `defaultName` if empty); `compactionProv = compiled[compactionName]`.
4. **Build compactor** — `newCompactor(cfg.compaction, compactionProv)` (existing signature unchanged; it gets a different provider now).
5. **Wire the rest** — `session.NewManager(store, inferenceProv, ...)` and the `processor` closure continue to use `inferenceProv` (no closure change needed).

### Tree-of-Thought: how the refactor decomposes

I considered three decompositions:

| Option | Pro | Con |
|---|---|---|
| Single mega-PR | Simple | Huge blast radius, hard to review, hard to bisect |
| **Sequential sub-refactors by package boundary** (selected) | Each step is reviewable, leaves the repo buildable, follows the pattern in sibling `.plans/` files | Three commits instead of one |
| Behavior-preserving rename first, real change later | Lowest risk per commit | Throws away an iteration; doesn't actually solve the problem |

Selected: **sequential sub-refactors by package boundary**. The refactor naturally decomposes along the existing package boundaries and the natural data dependency:
1. Refactor the loader + app-wiring (atomic; the new struct shape has to flow through both packages).
2. Add the `compaction.provider` name reference (additive; the compactor is built with a *different* provider).
3. Update user-facing documentation (example file, README, `buildConfigMap`).

### Tree-of-Thought: env-var binding

I considered two options:

- **Option X**: drop env-var support for per-provider keys, tell users to use the config file.
- **Option Y**: programmatically bind env vars per-name using `viper.BindEnv(viperKey, envKey)`, iterating over the defined names after `loadViperConfig` returns.

**Selected: Option Y.** Dropping env-var support would be a silent regression and is hostile to secret management. The implementation is mechanical: discover names from the loaded config, then `BindEnv` each `(providers.<name>.<field>, WORKSHOP_PROVIDER_<UPPER_NAME>_<FIELD>)`. Cobra flag bindings on those same keys override env (per viper's precedence: explicit Set > flag > env > config > default).

### Tree-of-Thought: per-named-provider CLI flags

The current `rootCmd` has a pflag for every `provider.*` field (`--provider.api-key`, `--provider.model`, etc.). With named providers, pflags don't naturally fit — you can't bind `viper.BindPFlag("providers.<dynamic>.api-key", ...)` because the name is dynamic.

Decision: **drop the per-named-provider pflags entirely**. Keep the single `provider` pflag for the default name (it's a single value). Users who want to set per-named-provider fields use the config file or env vars. This is a real loss of CLI ergonomics for users who like to pass everything on the command line, but it's a natural consequence of the refactor and is documented in the README. Flagging as a follow-up to consider: a per-named-provider flag scheme like `--providers.haiku.api-key` could be added later by generating pflag names from the config file at startup, but is out of scope here.

## Requirements

1. `config.yaml` MUST use the new shape: `provider: <name>` (string, required) + `providers: <name>: { ... }` (map).
2. Every defined named provider MUST be validated at startup; missing `api-key` or `model`, or unknown `kind`, is a startup error.
3. `provider: <default>` MUST reference a name in `providers:`; otherwise startup errors.
4. `compaction.provider` MUST (when set) reference a name in `providers:`; otherwise startup errors. Omitted ⇒ falls back to `provider: <default>`. `[inferred]`
5. Env-var binding MUST follow the per-named-provider pattern (`WORKSHOP_PROVIDER_<UPPER_NAME>_<FIELD>`). The old `WORKSHOP_PROVIDER_<FIELD>` env vars are silently ignored (hard break).
6. The flat `provider.*` viper keys are no longer read. An old config file with the flat shape will be silently ignored (no error), but the new required `provider:` key will be missing, which errors. `[inferred]`
7. The compactor's provider is the resolved `compaction.provider` (or the default, if unset), not necessarily the inference provider. The `git_commit` co-author trailer, the system prompt "model" sentence, and `buildInvokeOptions` continue to use the **inference** provider.
8. `compaction.max-tokens == 0` continues to disable compaction entirely (independent of `compaction.provider`). `[inferred]`
9. The new `compaction:` block must invite future extensions (`compaction.strategy:` for strategy choice, `compaction.strategy.max-tokens` for per-invocation output budget) without re-doing the layout. The flat-sibling design (`provider: <name>` as a sibling of `max-tokens`, with a future `strategy:` sub-block) satisfies this.
10. All existing tests for the provider config (`TestNewProvider_*`, `TestBuildInvokeOptions_*`, `TestMakeProviderConfig`, `TestSetupViper`) MUST be rewritten to the new shape and continue to pass.
11. `workshop config init` MUST emit the new shape via `buildConfigMap()`.
12. `config.yaml.example` and the README MUST be updated to document the new shape and env-var pattern.

## Task Breakdown

### Task 1: Refactor `cmd/workshop` and `internal/app` to the named-providers shape (no compaction.provider yet)

- **Goal**: Replace the flat `provider.*` viper keys with `provider: <name>` (string) + `providers: <name>: { ... }` (map), and refactor the app's config and wiring to match. Compaction still reuses the inference provider (no new behavior, just the structural change).
- **Dependencies**: None.
- **Files Affected**:
  - `cmd/workshop/root.go` — `setupViper` (drop `provider.*` env mapping; add a per-name env binding pass), `init()` (remove per-`provider.*` pflags; add the single `--provider` pflag for the default name; add the per-name env binding call), `runRoot` (use the new loader).
  - `cmd/workshop/http.go` — `runHTTP` (use the new loader).
  - `cmd/workshop/root_test.go` — rewrite `TestMakeProviderConfig` to `TestLoadProvidersConfig`; rewrite `TestSetupViper` to test the per-name env pattern; add new tests for: missing `provider:`, `provider:` references undefined name, env-var precedence over config file, flag override over env.
  - `internal/app/app.go` — change `WithProvider(pc)` to `WithProvider(name string, pc ProviderConfig)` and add `WithDefaultProviderName(name string)`; change `config.provider ProviderConfig` to `config.providers map[string]ProviderConfig` + `config.defaultProviderName string`; refactor `newProvider` to take `(name string, pc *ProviderConfig, tracer trace.Tracer)`; refactor `buildManager` to validate all named providers, compile each, resolve the default, and pass the compiled `provider.Provider` for inference; refactor `buildInvokeOptions` to read from `cfg.providers[cfg.defaultProviderName]`; refactor `coAuthoredByTrailer`, `makeGitCommitHandler`, and the system prompt "model" sentence to use the default provider.
  - `internal/app/app_test.go` — rewrite `TestNewProvider_*` to pass a name; rewrite `TestBuildInvokeOptions_*` to set up `cfg.providers` and `cfg.defaultProviderName`; add tests for the new validation logic in `buildManager` (or whichever function holds it).
- **New Files**: None. All changes are modifications to existing files. (The implementer may optionally factor the validation+compile logic into `internal/app/providers.go`; if so, that file is added in this task.)
- **Interfaces**:
  - `app.WithProvider(name string, pc ProviderConfig) Option` — name is the entry's key in the `providers:` map.
  - `app.WithDefaultProviderName(name string) Option` — sets the inference provider name.
  - `app.CompactionConfig` — same shape as today (just `MaxTokens`); `Provider` field is added in Task 2.
  - `app.newProvider(name string, pc *ProviderConfig, tracer trace.Tracer) (provider.Provider, error)` — the `name` is used in error messages and (optionally) for tracing/logging.
  - New unexported helper: `func (c *config) defaultProviderConfig() ProviderConfig` — returns `c.providers[c.defaultProviderName]` for the inference consumer; panics if `c.defaultProviderName` is empty (caller's responsibility to validate).
- **Validation**:
  - `go build ./...` passes.
  - `go test -race ./...` passes from the root.
  - `golangci-lint run ./...` is clean.
  - Manual smoke test: `workshop --provider haiku` against a valid config file with a `haiku` named provider starts the TUI without error.
  - Manual smoke test: an old config file with flat `provider.*` keys is silently ignored (the app errors on the missing `provider:` name key).
  - Manual smoke test: an old `WORKSHOP_PROVIDER_API_KEY` env var is silently ignored.
- **Details**:
  - The new `loadProvidersConfig()` (in `cmd/workshop/root.go` or a new `providers.go`) reads the named-provider map, the default name, and (for now) ignores `compaction.provider`. It returns a struct suitable for `app.WithProvider` / `app.WithDefaultProviderName` / `app.WithCompaction`.
  - The per-name env-var binding pass iterates over `v.GetStringMap("providers")` (after `loadViperConfig`), uppercases each key, and calls `v.BindEnv("providers.<name>.<field>", "WORKSHOP_PROVIDER_<UPPER_NAME>_<FIELD>")` for each known field. The known fields are: `kind`, `api-key`, `model`, `base-url`, `temperature`, `thinking-level`, `max-tokens`.
  - The `init()` order in `root.go` becomes: (1) pflag definitions, (2) `setupViper`, (3) `loadViperConfig`, (4) `bindNamedProviderEnvVars(viper.GetViper())`, (5) `viper.BindPFlags(...)`. This ordering is load-bearing: per-name env binding must happen *after* the config file is loaded (so the names are known) and *before* the flag bindings (so the flag takes precedence per viper's standard priority).
  - The `newProvider` function signature change from `*ProviderConfig` to `(name string, *ProviderConfig, ...)` is the only public function change in `internal/app`. The Anthropic default-MaxTokens mutation continues to apply to the `*ProviderConfig` pointer (the existing by-pointer pattern is preserved).
  - The `config` struct gains `providers map[string]ProviderConfig` and `defaultProviderName string`, and loses `provider ProviderConfig`. Every site that read `cfg.provider.X` is updated to `cfg.defaultProviderConfig().X` (or, where performance matters, the implementer can hoist the lookup to a local variable). There are 6 such sites in `app.go` (the `cfg.provider.{Model,Kind,Temperature,ThinkingLevel,MaxTokens}` reads in `buildInvokeOptions`, `coAuthoredByTrailer`, the system prompt transform, and `makeGitCommitHandler`).
  - Tests: existing `TestNewProvider_Anthropic_AppliesDefaultMaxTokens` continues to work (the by-pointer mutation is preserved). Existing `TestBuildInvokeOptions_*` tests are rewritten to set `cfg.providers[cfg.defaultProviderName]` instead of `cfg.provider`.

### Task 2: Wire the `compaction.provider` name reference

- **Goal**: Add the `compaction.provider` name reference. Build a *second* `provider.Provider` for compaction in `buildManager`, distinct from the inference provider. Pass it to `newCompactor`. Omitted ⇒ falls back to the default name.
- **Dependencies**: Task 1.
- **Files Affected**:
  - `internal/app/app.go` — add `Provider string` to `CompactionConfig`; refactor `buildManager` to (a) validate that `compaction.Provider` (if set) references a defined name, (b) resolve the name to the default if empty, (c) pass the resolved compiled provider to `newCompactor` instead of the inference provider. Add a new unexported helper `func (c *config) compactionProvider() provider.Provider` that returns the resolved provider; newCompactor's signature stays the same but the second argument now comes from this helper.
  - `internal/app/app_test.go` — add tests: `compaction.provider` unset → compactor uses the default inference provider; `compaction.provider` set to a defined name → compactor uses that name; `compaction.provider` set to a name not in `providers:` → startup error; `compaction.max-tokens == 0` → compactor is `nil` (existing behavior preserved).
- **New Files**: None.
- **Interfaces**:
  - `app.CompactionConfig` gains `Provider string` — the name of the named provider to use for compaction; empty means "use the default inference provider."
- **Validation**:
  - `go build ./...` passes.
  - `go test -race ./...` passes from the root.
  - New tests for the four cases above pass.
  - Manual smoke test: a config with `compaction.provider: haiku` (a different name from `provider: sonnet`) starts the TUI and `/compact` works without error.
  - Manual smoke test: a config with `compaction.provider: nonexistent` fails at startup with a clear error.
- **Details**:
  - The compactor's `SummarizeStrategy.Provider` field is already a `provider.Provider` (since PR #444). The refactor here is purely in the workshop-side wiring: pass a *different* compiled provider to `newCompactor` instead of the inference one.
  - The validation order in `buildManager` is: (1) validate every defined named provider, (2) validate `provider:` references a defined name, (3) validate `compaction.provider` (if set) references a defined name, (4) compile all, (5) resolve the default and the compaction names, (6) build the compactor.
  - The "fall back to default" behavior is implemented by resolving the name in the same step as the rest, not by special-casing in `newCompactor`. This keeps the resolution logic in one place.
  - Error messages must include the offending name (e.g., `compaction.provider "nonexistent" is not defined in providers:`). Per the user's "fail loud" rule.

### Task 3: Update user-facing documentation and `workshop config init`

- **Goal**: Rewrite `config.yaml.example`, the README sections that document the config and env vars, and `buildConfigMap()` to emit the new shape. This task is purely user-facing; no Go behavior changes.
- **Dependencies**: Task 2 (so the documentation matches the actual behavior).
- **Files Affected**:
  - `config.yaml.example` — full rewrite to the new shape. Includes: the two provider kinds (openai, anthropic) as comments, an example of two named providers (`haiku` and `sonnet`), an example of `compaction.provider` pointing to the cheap one, the `compaction.max-tokens` trigger threshold, the telemetry section (unchanged).
  - `README.md` — update the Usage section (TUI, Anthropic, OpenRouter subsections) to use the new env-var pattern; update the example `config.yaml` block in the Configuration section; update the Security notice to refer to `providers.<name>.api-key`; update the Compaction section to mention `compaction.provider`; update the precedence table to show the per-name env vars.
  - `cmd/workshop/config.go` — rewrite `buildConfigMap()` to emit the new shape (a `provider: <name>` string at the top level; a `providers:` map at the top level with per-name fields; a `compaction:` block with `provider` and `max-tokens`).
  - `cmd/workshop/config_test.go` — rewrite assertions on the new shape (the existing `TestRunConfigInitWithPath_WritesCorrectYAML` test asserts the new structure; remove assertions on the old flat `provider.*` keys; add an assertion for `compaction.provider`).
- **New Files**: None.
- **Interfaces**:
  - `cmd/workshop.buildConfigMap()` returns `map[string]interface{}` in the new shape.
- **Validation**:
  - `go test -race ./...` passes (the `config_test.go` assertions are part of the test suite).
  - Manual smoke test: `workshop config init` produces a YAML file that, when re-loaded by `loadViperConfig`, produces the same `ProvidersConfig` as the live viper state.
  - Visual review: `config.yaml.example` and the README's example config are consistent with each other and with `buildConfigMap()`'s output.
- **Details**:
  - The security notice in the README must be updated to refer to the *plural* `providers.<name>.api-key` keys (the `config init` output will contain every defined named provider's api-key, even those that aren't referenced).
  - The README's env-var example for OpenRouter is rewritten from `WORKSHOP_PROVIDER_BASE_URL=...` to `WORKSHOP_PROVIDER_<NAME>_BASE_URL=...`, with a note that `<NAME>` is the name from the `providers:` map.
  - The Compaction section is updated to add a short paragraph: "To use a different model for compaction than for inference, set `compaction.provider: <name>` to reference a different entry in `providers:`. If unset, compaction reuses the inference provider."

## Dependency Graph

- Task 1 → Task 2 (Task 2 depends on Task 1's named-providers infrastructure)
- Task 2 → Task 3 (Task 3 documents the behavior Task 2 implements)
- Tasks 1, 2, 3 are otherwise sequential.

No parallelizable work identified. Each task's blast radius is small enough that parallelizing wouldn't speed anything up.

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| Viper's `GetStringMap("providers")` returns a `map[string]interface{}` whose nested values are themselves `map[string]interface{}` — type assertions are fragile | Medium | Medium | The new loader is the *only* place that touches this map; it has a dedicated test suite covering all the type-assertion paths. The loader is small (~50 lines) and easy to audit. |
| Per-name env binding must run *after* the config file loads (names must be known) and *before* flag binding (flag precedence) | High | High | This ordering is documented in Task 1's "Details" and asserted by a test that sets a config file, an env var, and a flag, and verifies the flag wins. |
| Old configs / env vars silently fail (no error message saying "your old config doesn't work") | Medium | High | The startup error on the missing `provider:` key is the failure mode; the error message includes the hint. The README's CHANGELOG-style note calls this out as a hard break. |
| The `processor` closure captures the *inference* `prov` from the outer scope. After Task 2, the compactor has a *different* provider, but the closure is unchanged. A future maintainer might "fix" this by passing the compactor's provider into the closure | Medium | Low | The closure is not changed; a comment in `buildManager` notes "the compactor uses its own provider; this closure uses the inference provider for the main loop." |
| `cfg.provider.MaxTokens` is mutated by `newProvider` (Anthropic default) and re-read by `buildInvokeOptions`. With named providers, the same per-name mutation must apply. The implementer might break this when refactoring | Medium | Medium | The by-pointer signature is preserved; `TestNewProvider_Anthropic_AppliesDefaultMaxTokens` continues to assert the mutation. The test is updated to also assert that the lookup `cfg.providers[cfg.defaultProviderName].MaxTokens` returns the mutated value. |
| The new `compaction.provider` could be set to the same name as the inference provider (no-op) or to a different name. The behavior is correct in both cases, but a future maintainer might assume "always different" | Low | Low | A test asserts both cases (same name and different name) and verifies the right provider is used. |
| `pflag` ergonomics regress — users who like to set per-named-provider fields on the command line lose that ability | Low | Medium | Documented in the README under a "Per-named-provider configuration" subsection; flagged as a follow-up to consider a generated-pflag scheme. The env-var and config-file paths are unaffected. |
| Env-var binding iteration over `v.GetStringMap("providers")` runs even when the config file is absent | Low | High | The map is empty; `BindEnv` is a no-op for an empty set; the app errors on the missing `provider:` key as expected. A test asserts this. |
| The `git_commit` co-author trailer and the system prompt "model" sentence might accidentally be wired to the compaction provider instead of the inference provider | Medium | Low | Task 1's test suite includes assertions that these still read from the default provider (the inference one), even when `compaction.provider` is set to a different name. Task 2's tests verify the same. |

## Validation Criteria

- [ ] `go build ./...` passes from the root.
- [ ] `go test -race ./...` passes from the root.
- [ ] `golangci-lint run ./...` is clean.
- [ ] A config file in the new shape loads, validates, and starts the TUI.
- [ ] A config file with `compaction.provider: <different-name>` starts the TUI and uses a different provider for compaction than for inference.
- [ ] A config file with `compaction.provider: <undefined-name>` fails at startup with a clear error message naming the undefined provider.
- [ ] A config file with a defined named provider missing `api-key` fails at startup with a clear error message naming the provider.
- [ ] An env var `WORKSHOP_PROVIDER_<NAME>_API_KEY` is read and applied to the named provider.
- [ ] A flag override (`--provider <name>`) takes precedence over both the config file and the env var.
- [ ] An old flat `provider.api-key` config key is silently ignored (the app errors on the missing `provider:` name, not on the flat key).
- [ ] An old `WORKSHOP_PROVIDER_API_KEY` env var is silently ignored.
- [ ] `compaction.max-tokens == 0` still disables compaction entirely (the compactor is `nil`).
- [ ] The `git_commit` co-author trailer uses the *inference* provider's model name, not the compaction provider's.
- [ ] The system prompt "model" sentence uses the *inference* provider's model name.
- [ ] `workshop config init` produces a YAML file in the new shape.
- [ ] `config.yaml.example` and the README's example config are consistent with `buildConfigMap()`'s output.
- [ ] The README's env-var examples use the per-named-provider pattern.
- [ ] The `compaction:` block design leaves room for a future `compaction.strategy:` sub-block and `compaction.strategy.max-tokens` field without re-doing the layout.
