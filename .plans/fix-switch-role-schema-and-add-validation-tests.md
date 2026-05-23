# Plan: Fix switch_role Tool Schema and Add Validation Tests

## Objective

Fix the malformed JSON Schema for the `switch_role` tool in `internal/app/app.go` that causes Fireworks API to return a 400 Bad Request. Audit and correct `list_roles` and `get_current_role` schemas for the same structural issue. Extract the schema definitions into testable constants, and add unit tests that verify schema structure as well as a smoke test for `buildManager`. This prevents runtime API failures from schema bugs and provides a local safety net until upstream `ore#162` adds registry-level validation.

## Context

**Issue #8** (downstream `workshop`) reports that the Fireworks API rejects the `switch_role` tool schema:

```
400 Bad Request: JSON Schema not supported: could not understand the instance `{'name': {'description': 'Name of the role to activate', 'type': 'string'}}`.
```

In `internal/app/app.go`, the `buildManager` function registers `switch_role` with:

```go
map[string]any{
    "name": map[string]any{
        "type":        "string",
        "description": "Name of the role to activate",
    },
}
```

This is missing the required `type: "object"` root and `properties` wrapper that OpenAI-compatible function calling requires. The correct schema must be:

```json
{
    "type": "object",
    "properties": {
        "name": {
            "type": "string",
            "description": "Name of the role to activate"
        }
    },
    "required": ["name"]
}
```

**Issue #162** (upstream `ore`) will add `tool.Registry` validation at registration time. Until that lands, the `workshop` project needs its own tests to catch schema mistakes locally rather than at runtime against a live API.

**Existing test coverage** (`internal/app/app_test.go`) tests `newProvider` and individual tool handlers in isolation, but `buildManager` has zero test coverage, so schema bugs are not caught by `go test`.

## Architectural Blueprint

The fix is localized to the `internal/app` package and consists of three layers:

1. **Schema Correction**: Update the three role-management tool schemas in `buildManager` to comply with OpenAI JSON Schema requirements.
2. **Testability Refactor**: Extract the schema maps into package-level variables so they can be asserted independently without invoking the full `buildManager` machinery.
3. **Validation Tests**: Add unit tests that marshal each schema to JSON, verify root `type: "object"`, inspect `properties` and `required` fields, and smoke-test `buildManager` with a dummy provider config.

This approach keeps the production change minimal while establishing a regression test suite. Once `ore#162` lands, these tests will continue to serve as a downstream contract test even though the registry will also validate at runtime.

## Requirements

1. Correct `switch_role` schema to include `type: "object"`, `properties` wrapper, and `required: ["name"]`.
2. Correct `list_roles` and `get_current_role` schemas to explicitly declare `type: "object"` (even when empty, for strict provider compatibility).
3. Extract role tool schemas into testable package-level variables.
4. Add unit tests that verify each role tool schema has valid JSON Schema structure.
5. Add a smoke test for `buildManager` that exercises tool registration with a dummy provider config.
6. Ensure all existing tests continue to pass.

## Task Breakdown

### Task 1: Fix and Extract Role Tool Schemas
- **Goal**: Correct malformed schemas and extract them into testable constants.
- **Dependencies**: None.
- **Files Affected**:
  - `internal/app/app.go`
- **New Files**:
  - `internal/app/tool_schemas.go`
- **Interfaces**: New exported (or package-level) variables: `listRolesSchema`, `getCurrentRoleSchema`, `switchRoleSchema` of type `map[string]any`.
- **Validation**:
  - `go test ./internal/app/...` passes (no new tests yet, but no regressions).
  - `go build ./...` succeeds.
- **Details**:
  1. Create `internal/app/tool_schemas.go` containing:
     ```go
     var listRolesSchema = map[string]any{
         "type": "object",
     }
     var getCurrentRoleSchema = map[string]any{
         "type": "object",
     }
     var switchRoleSchema = map[string]any{
         "type": "object",
         "properties": map[string]any{
             "name": map[string]any{
                 "type":        "string",
                 "description": "Name of the role to activate",
             },
         },
         "required": []string{"name"},
     }
     ```
  2. Update `buildManager` in `internal/app/app.go` to reference `listRolesSchema`, `getCurrentRoleSchema`, and `switchRoleSchema` instead of inline maps.
  3. Verify no other tools in `buildManager` have malformed schemas (filesystem and bash tools are imported from `ore` and assumed correct).

### Task 2: Add Schema Validation and buildManager Smoke Tests
- **Goal**: Add tests that assert schema structure and that `buildManager` initializes without error.
- **Dependencies**: Task 1.
- **Files Affected**:
  - `internal/app/app_test.go`
- **New Files**: None.
- **Interfaces**: New test functions: `TestRoleToolSchemas` and `TestBuildManager_Smoke`.
- **Validation**:
  - `go test ./internal/app/...` passes, including the new tests.
  - `go test ./...` passes.
- **Details**:
  1. Add `TestRoleToolSchemas` that:
     - Marshals each schema (`listRolesSchema`, `getCurrentRoleSchema`, `switchRoleSchema`) to JSON and back to `map[string]any`.
     - Asserts the root `type` key equals `"object"`.
     - For `switchRoleSchema`, asserts `properties` exists, `properties.name.type` equals `"string"`, and `required` contains `"name"`.
     - For `listRolesSchema` and `getCurrentRoleSchema`, asserts `properties` is either absent or an empty map.
  2. Add `TestBuildManager_Smoke` that:
     - Calls `buildManager(&config{provider: ProviderConfig{Kind: "openai", APIKey: "sk-test-dummy", Model: "test-model"}})`.
     - Asserts no error is returned.
     - Asserts the returned `*session.Manager` is non-nil.
  3. Run the full test suite to confirm no regressions.

## Dependency Graph

- Task 1 → Task 2 (tests depend on extracted schema variables)

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| `buildManager` smoke test fails because `openai.New` validates API key format or makes a network call during construction | Medium | Low | Use a syntactically valid dummy key (`sk-test-dummy`). If `openai.New` is stricter, mock the provider creation or skip the smoke test and test schemas only. |
| `list_roles` / `get_current_role` empty schemas (`{"type":"object"}`) are rejected by some OpenAI-compatible providers that expect `{}` | Low | Low | The explicit `type: "object"` is valid JSON Schema and accepted by OpenAI. If a provider is stricter, adjust after testing. |
| Upstream `ore#162` changes `tool.Registry.Register` signature or behavior, breaking our tests | Low | Low | The plan only adds downstream tests; once `ore#162` lands, we can remove redundant local validation or keep it as a contract test. |

## Validation Criteria

- [ ] `switch_role` schema serializes to JSON with `type: "object"`, `properties.name`, and `required: ["name"]`.
- [ ] `list_roles` and `get_current_role` schemas serialize to JSON with `type: "object"`.
- [ ] `go test ./internal/app/...` passes, including the new schema and smoke tests.
- [ ] `go build ./...` succeeds with no errors.
- [ ] `go test ./...` passes with no regressions in existing tests.
