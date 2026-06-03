# Plan: Fix Compaction by Removing Redundant KeepLastN Strategy

## Objective

Remove the redundant `compaction.KeepLastN` strategy from the compactor configuration in `internal/app/app.go`. The `ore/x/compaction.Compactor` only supports a single strategy, and `WithStrategy` overwrites rather than appends. Currently, `KeepLastN` silently overwrites `SummarizeStrategy`, causing compaction to truncate conversation history instead of LLM-summarizing it. `SummarizeStrategy` already handles `PreserveLastN` internally, so `KeepLastN` is unnecessary.

## Context

The workshop project (`github.com/andrewhowdencom/workshop`) is a terminal-based coding assistant built on the `ore` framework. Compaction was introduced in a prior migration (`migrate-to-ore-compaction-with-llm-summarize.md`) to automatically compact conversation history when token usage exceeds a threshold.

In `internal/app/app.go`, `buildManager()` configures the compactor:

```go
compactor = compaction.New(
    compaction.WithTrigger(compaction.TokenUsageTrigger{MaxTokens: cfg.compaction.MaxTokens}),
    compaction.WithStrategy(compaction.SummarizeStrategy{
        Provider:      prov,
        PreserveLastN: cfg.compaction.PreserveLastN,
    }),
    compaction.WithStrategy(compaction.KeepLastN{N: cfg.compaction.PreserveLastN}),  // OVERWRITES above
)
```

The `ore/x/compaction.Compactor` struct (`../ore/x/compaction/compaction.go`) holds only one `strategy Strategy` field. `WithStrategy` sets it directly, so the second call clobbers the first. The intended LLM summarization never runs; only hard truncation runs instead.

A parallel GitHub issue was raised in `ore` to address the framework-level limitation: https://github.com/andrewhowdencom/ore/issues/333

This plan focuses on the **workshop-side fix**: removing the redundant `KeepLastN` line so `SummarizeStrategy` is the sole strategy, which already preserves the last N turns verbatim and summarizes older ones via the LLM provider.

## Architectural Blueprint

No architectural change. This is a surgical bug fix within the existing `buildManager()` function. The compactor will use `SummarizeStrategy` alone, which is the intended behavior from the original compaction migration.

```
Before: [TokenUsageTrigger] → SummarizeStrategy (intended, overwritten)
                         → KeepLastN (actual, wins)

After:  [TokenUsageTrigger] → SummarizeStrategy (sole strategy, preserves last N internally)
```

## Requirements

1. Remove the `compaction.WithStrategy(compaction.KeepLastN{...})` line from the compactor configuration in `internal/app/app.go`.
2. Confirm `SummarizeStrategy` is now the only strategy configured.
3. Verify existing tests pass after the change.
4. Ensure `go build ./...` compiles cleanly.

## Task Breakdown

### Task 1: Remove Redundant KeepLastN Strategy
- **Goal**: Remove the `compaction.WithStrategy(compaction.KeepLastN{...})` line from `buildManager()` so `SummarizeStrategy` is the sole compaction strategy.
- **Dependencies**: None.
- **Files Affected**: `internal/app/app.go`
- **New Files**: None.
- **Interfaces**: No interface changes.
- **Validation**: `go build ./...` compiles cleanly; `go test ./...` passes.
- **Details**: In `internal/app/app.go`, within the `if cfg.compaction.MaxTokens > 0` block inside `buildManager()`, delete the second `compaction.WithStrategy` call that passes `compaction.KeepLastN`. Leave the first `compaction.WithStrategy(compaction.SummarizeStrategy{...})` intact. The resulting compactor configuration should contain only the trigger and the summarize strategy.

## Dependency Graph

- Task 1 (sole task — no dependencies or parallelism needed)

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| `SummarizeStrategy` fails and aborts the inference turn | High | Low | Accepted per user decision: "if summarisation fails, inference also fails Just let it fail." |
| Extra LLM call per compaction increases cost/latency | Medium | Low | Accepted per user decision: "an extra LLM call on compaction is fine." |

## Validation Criteria

- [ ] `internal/app/app.go` compactor configuration contains only one `WithStrategy` call, passing `SummarizeStrategy`.
- [ ] `go test ./...` passes with zero failures.
- [ ] `go build ./cmd/workshop` succeeds.
- [ ] No references to `KeepLastN` remain in the workshop codebase (excluding `go.mod`/`go.sum` indirect references).
