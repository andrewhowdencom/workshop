# Plan: TUI Delta Streaming with Debounced Markdown Rendering

## Objective

Make the ore TUI stream assistant responses fluidly by switching to `text_delta`/`reasoning_delta` events, accumulating incoming chunks, and debouncing glamour markdown re-renders at ~60fps (16ms). Currently the TUI receives accumulated `text`/`reasoning` artifacts and re-renders the full response through glamour on every single chunk, which is computationally expensive and produces broken formatting for incomplete markdown structures.

## Context

The ore framework already emits delta artifacts (`TextDelta`, `ReasoningDelta`) with the `IsDelta()` marker. The `loop` package's `FanOut` accumulates deltas into complete artifacts for subscribers who request the non-delta kinds. The TUI currently subscribes to accumulated kinds (`"text"`, `"reasoning"`) and therefore receives full re-accumulated text on every token.

The TUI lives in `../ore/x/conduit/tui/`. Key files:
- `tui.go` — session lifecycle, event subscription, goroutine wiring
- `model.go` — Bubble Tea model, `Update()`, `renderArtifact()`, viewport cache
- `view.go` — `buildContent()`, terminal output assembly
- `markdown.go` — glamour-based markdown renderer (`glamourMarkdownRenderer`)
- `model_test.go`, `view_test.go`, `tui_test.go` — existing test coverage

The current `artifactMsg` handler in `model.go` creates a new `renderedBlock` per artifact and immediately calls `syncViewport()`, which triggers `buildContent()` → `renderMarkdown()` → glamour full re-parse. The `renderedBlock.rendered` field caches glamour output, but with accumulated artifacts the `source` field grows on every event so the cache is always stale.

The ore TUI uses Bubble Tea v2 (powered by Ultraviolet), which provides cell-level differential terminal rendering. The bottleneck is not terminal bandwidth — it is glamour's per-call cost (`NewTermRenderer()` + goldmark AST build + HTML/CSS → ANSI pipeline).

## Architectural Blueprint

The change is localized to the `x/conduit/tui` package. No external API changes.

1. **Subscribe to deltas** in `tui.go` — replace `"text"`, `"reasoning"` with `"text_delta"`, `"reasoning_delta"`.
2. **Accumulate deltas inline** in `model.go` `Update()` — append delta `Content` to the last `currentTurn.blocks` entry when the kind matches, instead of appending a new block per artifact.
3. **Debounced render tick** — on each `artifactMsg` set `contentDirty = true` and schedule a `tea.Tick(16ms)` command if none is pending. The tick handler calls `renderMarkdown()` for all `text`/`reasoning` blocks in `currentTurn`, then `syncViewport()` + `viewport.GotoBottom()`.
4. **Final render on `TurnCompleteEvent`** — do a synchronous glamour pass on `currentTurn.blocks` before moving them to `turns`, and cancel any pending render tick.
5. **Keep `renderArtifact()` unchanged** for non-delta artifacts (`ToolCall`, `ToolResult`) and for historical re-rendering on resize / non-assistant turns.

## Requirements

1. Change `tui.go` subscription from `"text"`, `"reasoning"` to `"text_delta"`, `"reasoning_delta"`.
2. Add `renderTickMsg` type and `renderScheduled bool` field to `model`.
3. In `model.go` `artifactMsg` handler: detect `TextDelta`/`ReasoningDelta`, append to the last block's `source` when kind matches; for non-deltas, delegate to `renderArtifact()` as before. Set `contentDirty = true` and schedule a `tea.Tick(16ms)` if `!renderScheduled`.
4. Add `renderTickMsg` handler: if `renderScheduled`, iterate `currentTurn.blocks`, run `renderMarkdown()` on `text`/`reasoning` blocks, call `syncViewport()` + `GotoBottom()`, clear flags. If `contentDirty` became true again during the tick window (not possible in Bubble Tea's single-goroutine `Update()` loop, but guard anyway), schedule another tick.
5. In `turnMsg` (assistant branch): set `renderScheduled = false` to cancel pending tick, do a final `renderMarkdown()` pass on `currentTurn.blocks`, then proceed with existing append/reset logic.
6. Update `model_test.go` streaming tests to use `artifact.TextDelta{Content: ...}` and `artifact.ReasoningDelta{Content: ...}` instead of `artifact.Text{Content: ...}` / `artifact.Reasoning{Content: ...}` for the incremental accumulation path.
7. Ensure `go test ./...` in `../ore/x/conduit/tui` passes after changes.

## Task Breakdown

### Task 1: Subscribe to Delta Events
- **Goal**: Change the TUI's `stream.Subscribe()` call to receive delta artifact kinds.
- **Dependencies**: None.
- **Files Affected**: `../ore/x/conduit/tui/tui.go`.
- **New Files**: None.
- **Interfaces**: None.
- **Validation**: `go build ./...` in `../ore/x/conduit/tui` compiles.
- **Details**: Replace `"text"`, `"reasoning"` with `"text_delta"`, `"reasoning_delta"` in the `stream.Subscribe()` call in `Start()`. Keep `"tool_call"`, `"tool_result"`, `"turn_complete"`, `"error"`, `"process_complete"`, `"status"`.

### Task 2: Add Delta Accumulation and Debounced Render Tick
- **Goal**: Accumulate delta chunks into existing blocks and debounce viewport updates at ~60fps.
- **Dependencies**: Task 1.
- **Files Affected**: `../ore/x/conduit/tui/model.go`.
- **New Files**: None.
- **Interfaces**:
  - New message type: `type renderTickMsg struct{}`
  - New model field: `renderScheduled bool`
  - Inline delta handling in `Update() artifactMsg` case.
- **Validation**: `go test ./...` in `../ore/x/conduit/tui` passes.
- **Details**:
  1. Add `renderTickMsg` type alongside existing message types.
  2. Add `renderScheduled bool` to `model` struct.
  3. In `artifactMsg` handler:
     - If `msg.artifact` is `TextDelta`: find the last block in `currentTurn.blocks` with `kind == "text"`. If found, append `Content` to `block.source` and clear `block.rendered`. If not found, create a new `renderedBlock{kind: "text", source: Content}`.
     - Same for `ReasoningDelta` with `kind == "reasoning"`.
     - For non-delta artifacts (`ToolCall`, `ToolResult`, `Text`, `Reasoning`), delegate to existing `renderArtifact()` logic.
     - Set `contentDirty = true`.
     - If `!renderScheduled`, set `renderScheduled = true` and return `tea.Tick(16*time.Millisecond, func(t time.Time) tea.Msg { return renderTickMsg{} })`.
  4. Add `renderTickMsg` handler:
     - If `!renderScheduled`, return early (tick was cancelled by turn completion).
     - For each block in `currentTurn.blocks` where `kind == "text"` or `kind == "reasoning"` and `source != ""`: call `m.renderMarkdown(block.source, m.viewport.Width())` and store result in `block.rendered`.
     - Call `m.syncViewport()` and `m.viewport.GotoBottom()`.
     - Set `contentDirty = false` and `renderScheduled = false`.
     - If `contentDirty` is somehow true again, schedule another tick (return a new `tea.Tick`).
  5. In `turnMsg` assistant branch, before appending `currentTurn` to `turns`:
     - Set `renderScheduled = false`.
     - Do a final `renderMarkdown()` pass on `currentTurn.blocks` (same loop as render tick).
     - Then proceed with existing append/reset logic.

### Task 3: Update Tests for Delta Streaming
- **Goal**: Convert existing streaming accumulation tests to use delta artifacts.
- **Dependencies**: Task 2.
- **Files Affected**: `../ore/x/conduit/tui/model_test.go`.
- **New Files**: None.
- **Interfaces**: None.
- **Validation**: `go test ./...` in `../ore/x/conduit/tui` passes.
- **Details**:
  1. Find tests that send `artifactMsg{artifact: artifact.Text{Content: ...}}` before `turnMsg` to simulate streaming.
  2. Replace with `artifact.TextDelta{Content: ...}`.
  3. For tests that send multiple artifacts in sequence, send multiple `TextDelta` messages and verify they accumulate into a single block.
  4. Verify that `turnMsg` finalizes the block and the `rendered` field is populated after completion.
  5. Add at least one test verifying the debounce behavior: send two `TextDelta` messages rapidly and confirm the block source accumulates both before any render tick is processed (simulate by not sending `renderTickMsg` manually in the test, or by inspecting state before the tick).

### Task 4: Verify End-to-End Behavior
- **Goal**: Confirm the TUI streams smoothly without visible lag or flicker.
- **Dependencies**: Task 3.
- **Files Affected**: None (runtime validation).
- **New Files**: None.
- **Interfaces**: None.
- **Validation**: Run a live TUI session against an LLM provider and observe:
  - Text appears incrementally as tokens arrive.
  - No visible "stutter" or frozen UI during long responses.
  - Markdown formatting is correct after the turn completes.
  - `Ctrl+O` expansion still works for tool calls and reasoning blocks.
- **Details**:
  1. In the `../ore` repo, run `go test ./x/conduit/tui/...` and confirm all tests pass.
  2. Build a small test program or use an existing ore example that exercises the TUI conduit.
  3. Send a prompt that produces a long markdown response (e.g., a multi-paragraph explanation with code blocks).
  4. Observe streaming behavior subjectively. If lag is still perceptible, document findings for a future iteration (e.g., replacing glamour with a lighter incremental renderer).

## Dependency Graph

- Task 1 → Task 2 (Task 2 needs the delta subscription to be active)
- Task 2 → Task 3 (tests exercise the new delta handling)
- Task 3 → Task 4 (runtime validation follows green tests)

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| Bubble Tea `Tick` scheduling conflicts with existing command returns | Medium | Low | Ensure `artifactMsg` returns `tea.Tick` as the command, not alongside other commands. If other `Update()` branches also return commands, sequence them carefully. |
| Incomplete markdown during streaming looks worse at 16ms intervals than at per-token intervals | Low | Medium | The fallback `cellbuf.Wrap` raw-text path in `buildContent()` provides reasonable wrapping. If formatting during streaming is unacceptable, this plan can be extended with a lighter inline markdown parser in a follow-up. |
| `renderScheduled` cancellation on `turnMsg` races with a pending tick message | Low | Low | Bubble Tea's `Update()` loop is single-goroutine; state mutations are atomic with respect to message processing. The pending tick message will arrive after `turnMsg` processing and will see `renderScheduled == false`. |
| Tests rely on `renderArtifact()` creating blocks with pre-populated `rendered`; delta path leaves `rendered` empty until tick | Medium | Low | Update tests to either manually send `renderTickMsg` or inspect `source` instead of `rendered` for intermediate states. |

## Validation Criteria

- [ ] `go test ./...` passes in `../ore/x/conduit/tui`.
- [ ] `go build ./...` passes in `../ore/x/conduit/tui`.
- [ ] TUI subscription includes `"text_delta"` and `"reasoning_delta"` but not `"text"` or `"reasoning"`.
- [ ] Multiple `TextDelta` messages sent to the model accumulate into a single `renderedBlock` with concatenated `source`.
- [ ] `renderTickMsg` triggers `renderMarkdown()` for the accumulated block and updates `block.rendered`.
- [ ] `TurnCompleteEvent` finalizes the turn with correctly formatted `rendered` output.
- [ ] No `renderScheduled` tick remains pending after turn completion.
