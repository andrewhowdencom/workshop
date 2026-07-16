# Authoring Checklist

Run this checklist before declaring a sub-agent file complete. Each step has a *why* so the model can reason about edge cases.

## 1. Pick a Name

- Filename (sans `.md`) is lowercase-hyphenated.
- Filename does **not** match any built-in tool (`read_file`, `write_file`, `edit_file`, `list_directory`, `search_files`, `bash`, `workspace_create`, `workspace_destroy`, `git_commit`, `set_title`, `read_skill`).
- Filename is namespaced (`my-team.<name>`) when an unnamespaced form risks collision.

*Why:* the workshop session fails to start with a descriptive error if the filename collides, and the failure surfaces only at session open. Catching it here saves a debug cycle.

## 2. Write the Frontmatter

- `description` is one declarative sentence: what the sub-agent does and when to use it.
- No extraneous fields.

*Why:* `description` is the only signal the parent model uses to decide whether to call this sub-agent. A vague or generic description under-triggers. Extraneous fields fail YAML parsing at v1.

## 3. Draft the Body

The body becomes the sub-agent's system prompt. Cover:

- **Domain**: one paragraph describing the specialist's area.
- **Outputs**: explicit JSON shape for `findings`. The parent can branch on this.
- **Constraints**: read-only mode, single-file scope, length budgets, output schema.
- **Status choice**: when to emit `success` / `partial` / `failed`.

*Why:* without an explicit `findings` shape, the sub-agent will emit mode-2 failures (invalid JSON) or mode-3 failures (valid JSON, wrong shape). The parent wastes a tool call either way.

## 4. Verify the Body Stays Within v1 Capabilities

- Body does not require a tool outside the inherited set: `read_file`, `write_file`, `edit_file`, `list_directory`, `search_files`, `bash`, `workspace_create`, `workspace_destroy`, `git_commit`, `set_title`, `read_skill`.
- Body does not require a custom cognitive pattern (only `ReAct` is available at v1).
- Body does not require a custom model or temperature override.
- Body does not require streaming, parallel fan-out, or persistent state.

*Why:* the framework's `x/subagent` package seeds a fresh `ledger.Thread` per call and uses `ReAct` exclusively. A body that assumes parent history, custom patterns, or streaming will misbehave silently.

## 5. Test the File Parses

Run the loader test against your file. The simplest verification:

```bash
go test -race ./internal/subagent/... -run TestLoadSubagent
```

For a full smoke test that exercises the workshop wiring:

```bash
task validate
```

This runs `go test -race ./...`, `golangci-lint run ./...`, and `go build ./cmd/workshop`. The `task validate` gate also runs the loader-level collision check.

## 6. Smoke-Test the Sub-Agent Live

Drop the file into `$XDG_DATA_HOME/workshop/subagents/` and launch workshop. Ask the parent agent to delegate to your sub-agent by name. Verify:

- The parent correctly identifies the description and emits a tool call with `prompt`.
- The sub-agent runs and returns a JSON object matching the result schema.
- The `status` value matches what the body instructed for this scenario.
- The `findings` object has the shape you declared in the body.

If any step fails, the body is the most likely culprit — re-read it and check the v1 capability matrix in SKILL.md.

## 7. Run the Review Path Gates

Walk the seven gates in `../SKILL.md` (frontmatter, trigger quality, length & shape, tone & rationale, scope tightness, name collision, references). Output enumerated findings only — do not pass/fail the file yourself.

## 8. Commit and Reload

If the file lives in this repo (rather than `~/.local/share/workshop/subagents/`), commit it on a feature branch and merge via PR. The workshop session picks up new files between sessions automatically (per-stream discovery).

## Common Pitfalls

| Symptom | Likely cause | Fix |
|---|---|---|
| `subagent "<name>" collides with already-registered tool` | Filename matches a built-in tool. | Rename with a namespace prefix (`my-team.<name>`). |
| Sub-agent returns text the parent can't parse | Body didn't say "emit JSON only". | Add explicit "Return a JSON object with status/summary/findings and no other prose." |
| Sub-agent gets the parent's history (and acts on it) | Body references parent context. | Sub-agents start from a fresh `ledger.Thread`; remove any prompt fragments that assume history. |
| Sub-agent picks the wrong tool | Body is too broad. | Narrow the domain; split into multiple sub-agents. |
| Description under-triggers (parent never calls) | Description is generic ("a helpful assistant"). | Sharpen to: "Migrates Go code from io/ioutil to os/io packages, returning the modified files." |