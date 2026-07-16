# Sub-agent Result Schema

The `x/subagent` framework enforces a JSON result schema on every sub-agent invocation. The schema is baked into the sub-agent's system prompt at construction time via `osubagent.ResultSystemPrompt()`; the parent sees the parsed JSON object as the tool result.

## Top-Level Shape

```json
{
  "status":   "success" | "partial" | "failed",
  "summary":  "<string>",
  "findings": <object | null>
}
```

| Field | Type | Always present? | Notes |
|---|---|---|---|
| `status` | string enum | yes | One of `success`, `partial`, `failed`. |
| `summary` | string | yes | Plain-text summary. For `failed`, contains the raw payload when schema validation failed. |
| `findings` | object \| null | yes | Domain-specific structured findings; `null` is valid when the sub-agent produced no findings (e.g., a clean negative result). |

## `status` Enum

The three values partition the outcome space and let the parent branch without losing diagnostic information.

### `success`

The sub-agent fully completed the task. `summary` describes what was done; `findings` (if non-null) carries structured data the parent will use.

```json
{
  "status":  "success",
  "summary": "Found 3 usages of `os.ReadFile` in `internal/foo` and migrated all to `fs.ReadFile`.",
  "findings": {
    "files_modified": ["internal/foo/a.go", "internal/foo/b.go", "internal/foo/c.go"]
  }
}
```

### `partial`

Progress was made but the task was not finished. The sub-agent is honest about incompleteness rather than fabricating completion. The parent may retry, escalate, or branch on this state.

```json
{
  "status":  "partial",
  "summary": "Migrated 2 of 3 target files. The third file uses a non-standard pattern that needs a manual decision.",
  "findings": {
    "files_modified":  ["internal/foo/a.go", "internal/foo/b.go"],
    "files_remaining": [{"path": "internal/foo/c.go", "reason": "uses os.ReadFile in init() before fs is available"}]
  }
}
```

### `failed`

The sub-agent could not accomplish the task **OR** the child's output failed schema validation (bad enum value, missing required field, wrong type). The framework preserves the raw payload in `summary` so the parent can diagnose.

```json
{
  "status":  "failed",
  "summary": "Schema validation failed: missing required field 'summary'.",
  "findings": null
}
```

```json
{
  "status":  "failed",
  "summary": "Encountered unresolvable ambiguity in the prompt; could not decide which package to migrate.",
  "findings": null
}
```

The parent should always branch on `status`. A common shape:

```go
res, err := saFn(ctx, sandbox, args)
switch r := res.(type) {
case subagent.Result:
    switch r.Status {
    case subagent.StatusSuccess:
        // done
    case subagent.StatusPartial:
        // retry or escalate
    case subagent.StatusFailed:
        // log r.Summary, retry or escalate
    }
case nil:
    // err is set; tool failed (parse error, agent error, etc.)
}
```

This is illustrative; the actual workshop call site uses the framework's tool dispatch.

## Failure Modes

Three distinct failure modes surface differently:

1. **Tool-call error** (e.g., empty `prompt` arg). `tool.ToolFunc` returns `nil, err`. The parent sees a tool error, not a `Result`. The `subagent-authoring` skill's `ToolFunc` rejects empty `prompt` with the error `prompt is required`.

2. **Parse error** (child emitted text that is not valid JSON). `tool.ToolFunc` returns `nil, err`. Parent sees a tool error.

3. **Schema-invalid JSON** (valid JSON, but wrong enum / missing required / wrong type). `tool.ToolFunc` returns a `Result` with `Status == StatusFailed` and `Summary` containing the raw payload. The parent can branch.

Sub-agents whose body instructs the LLM to emit non-JSON output will hit mode 2 or 3 and waste the parent's tool call. **Always instruct the sub-agent to emit only JSON matching the schema**, with no surrounding prose.

## Authoring Implications

- The body should **explicitly** tell the sub-agent to emit JSON conforming to this schema. A body that says "summarize your findings" without specifying JSON shape will produce mode-2 failures.
- A body that asks for multiple structured outputs (e.g., "list each file and the change made") should pre-declare the `findings` object shape: "findings is `{files: [{path, change}]}`".
- Status choice is up to the sub-agent. Bodies should describe when to emit each value. A body that says "always return success" hides failures from the parent and is a finding in review.

## Out of Scope

The framework's v1 `x/subagent` does not support streaming deltas, parallel fan-out, or span nesting under the parent. Sub-agents must complete their work and emit a single structured result. Do not design sub-agents that need streaming — the contract cannot honor it.