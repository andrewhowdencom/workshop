# workshop

A terminal-based coding assistant built on [`ore`](https://github.com/andrewhowdencom/ore).

This project demonstrates how to build a fully fledged agentic application outside the `ore` examples directory. It wires together the TUI conduit, system prompt transforms, guardrails, filesystem tools, and a bash execution tool to create an interactive coding agent that can read, write, edit, search, and execute shell commands.

## Prerequisites

- [Go](https://go.dev/) 1.26+
- An API key for [OpenAI](https://platform.openai.com/), [Anthropic](https://console.anthropic.com/), or a compatible endpoint (e.g. [OpenRouter](https://openrouter.ai/))
- [Task](https://taskfile.dev/) (optional, for development tasks)

## Commands

| Command | Description |
|---|---|
| `workshop` | Open the interactive TUI (default) |
| `workshop http` | Run the web UI HTTP server |
| `workshop config init` | Initialize a configuration file from current settings |
| `workshop version` | Print the build version |
| `workshop thread export <id>` | Export a thread to stdout or a file (--format text, json, html; --output file) |

Role files (e.g. `ideation.md`, `build.md`) are loaded from
`$XDG_DATA_HOME/workshop/roles/` (fallback: `~/.local/share/workshop/roles/`).

## Usage

### TUI (default)

```bash
export WORKSHOP_PROVIDER_API_KEY=sk-...
export WORKSHOP_PROVIDER_MODEL=gpt-4o   # optional; defaults to gpt-4o
go run ./cmd/workshop
```

### Anthropic (native)

```bash
export WORKSHOP_PROVIDER_KIND=anthropic
export WORKSHOP_PROVIDER_API_KEY=sk-ant-...
export WORKSHOP_PROVIDER_MODEL=claude-sonnet-4-5   # any Anthropic model name
go run ./cmd/workshop
```

The Anthropic provider requires a per-request `max_tokens`. When unset,
workshop applies a default of 32000 (overridable via
`--provider.max-tokens`). To enable Anthropic's extended thinking, set
`--provider.thinking-level` to one of `minimal`, `low`, `medium`, `high`,
or `max`; the level is translated at request time to a percentage of
`max_tokens` for `thinking.budget_tokens` (with a 1024-token floor and
a `(max_tokens - 1024)` ceiling so the visible response always has
room). The level is also settable at runtime via the `/thinking` slash
command; see "Slash commands" below.

### OpenRouter (via the Anthropic Messages mirror)

OpenRouter exposes an Anthropic-compatible endpoint at
`https://openrouter.ai/api/v1`. Workshop selects the right auth header
automatically when the base URL contains `openrouter`, so no separate
provider kind is needed:

```bash
export WORKSHOP_PROVIDER_KIND=anthropic
export WORKSHOP_PROVIDER_API_KEY=sk-or-...                  # OpenRouter key
export WORKSHOP_PROVIDER_BASE_URL=https://openrouter.ai/api/v1
export WORKSHOP_PROVIDER_MODEL=anthropic/claude-sonnet-4-5 # OpenRouter model id
go run ./cmd/workshop
```

The `Authorization: Bearer <key>` header is sent in place of
Anthropic's `x-api-key`; everything else behaves identically to the
native flow.

### Web UI (HTTP server)

```bash
go run ./cmd/workshop http
```

With custom port:

```bash
go run ./cmd/workshop http --http.addr :7654
```

### Start with a role

```bash
# Via CLI flag
go run ./cmd/workshop --role ideation

# Via environment variable
WORKSHOP_ROLE=reviewer go run ./cmd/workshop
```

The web chat UI is available at `http://localhost:8080/` (or the configured address).

### Resume an existing thread

```bash
go run ./cmd/workshop --thread <uuid>
```

### `workshop thread export`

```bash
go run ./cmd/workshop thread export <uuid>
```

Print plain text to stdout (default):

```bash
go run ./cmd/workshop thread export <uuid> --format html
```

With output to a file:

```bash
go run ./cmd/workshop thread export <uuid> --format json --output thread.json
```

Supported formats are `text` (default), `json`, and `html`.

> **Note:** Exporting very large threads may generate substantial output.
> The HTML format includes minimal styling; you may wish to redirect it to a file and open it in a browser.

See also: [Persistent JSON store](#persistent-json-store) for the default storage location of thread data.

### Persistent JSON store

Thread history is persisted by default to `$XDG_DATA_HOME/workshop/threads/`
(fallback: `~/.local/share/workshop/threads/`). To use a custom storage
location, set `--store.dir` or `WORKSHOP_STORE_DIR`:

```bash
WORKSHOP_STORE_DIR=/tmp/ore-store go run ./cmd/workshop
```

### Adjust log level

```bash
go run ./cmd/workshop --log-level debug
```

### Configuration file

Workshop supports an optional YAML configuration file stored in the XDG config directory.

**Quick start**

1. Generate the file from your current environment:

   ```bash
   go run ./cmd/workshop config init
   ```

2. Open the generated file and replace the placeholder API key:

   ```bash
   # Linux / macOS
   $EDITOR ~/.config/workshop/config.yaml

   # Windows
   notepad %APPDATA%\workshop\config.yaml
   ```

3. Secure the file:

   ```bash
   chmod 600 ~/.config/workshop/config.yaml
   ```

4. Verify by running with a different log level:

   ```bash
   go run ./cmd/workshop --log-level debug
   ```

**Default path**

- Linux / macOS: `$XDG_CONFIG_HOME/workshop/config.yaml` (fallback: `~/.config/workshop/config.yaml`)
- Windows: `%APPDATA%\workshop\config.yaml`

**Precedence**

| Source | Priority | Example |
|---|---|---|
| Flag | 1 (highest) | `--provider.model=gpt-4o` |
| Environment | 2 | `WORKSHOP_PROVIDER_MODEL=gpt-4o` |
| Config file | 3 | `provider.model: gpt-4o` |
| Default | 4 | Built-in defaults |

For example, setting `WORKSHOP_LOG_LEVEL=debug` overrides `log-level: info` in the config file, unless `--log-level` is also supplied. The same precedence applies to `role`:

| Source | Example |
|---|---|
| Flag | `--role=ideation` |
| Environment | `WORKSHOP_ROLE=reviewer` |
| Config file | `role: planner` |

> **Security notice:** `config init` writes `provider.api-key` in plaintext. Ensure the generated file is stored securely and never committed to a public repository.

Example `config.yaml`:

```yaml
log-level: info
provider:
  kind: openai
  api-key: sk-...
  model: gpt-4o
  base-url: ""
  temperature: 0          # 0 = provider default; range 0–2 for OpenAI
  reasoning-effort: ""    # DEPRECATED; use thinking-level instead
  max-tokens: 0           # hard cap on output tokens; 0 = workshop default (anthropic only)
  thinking-level: "off"   # off, minimal, low, medium, high, max (shared by every backend)
store:
  dir: ""              # empty = use $XDG_DATA_HOME/workshop/threads
http:
  addr: ":8080"
compaction:
  max-tokens: 100000    # 0 = disabled; trigger compaction when tokens exceed this
telemetry:
  traces:
    endpoint: ""         # OTLP/HTTP collector URL for traces (e.g. http://localhost:4318); empty = disabled
  metrics:
    endpoint: ""         # OTLP/HTTP collector URL for metrics (e.g. http://localhost:4318); empty = disabled
```

### Deprecated variables

The previous `ORE_*` and `STORE_DIR` environment variables are no longer supported. Use the `WORKSHOP_` prefix instead.

## Compaction

When conversation history grows beyond the provider's context window, workshop
can automatically compact older turns into a single summary turn before each
inference. This keeps recent context intact while retaining key facts from
earlier in the conversation.

Compaction is triggered by token usage reported by the provider
(`--compaction.max-tokens`). When triggered, turns that exceed the token budget
are summarized via the same LLM provider, and the result is injected as a
synthetic system turn. The newest turns that fit within the budget are kept
verbatim. Set `--compaction.max-tokens 0` to disable.

You can also force compaction at any time by typing `/compact` in the TUI or
stdio interface. This immediately compacts the conversation history regardless
of the current token count. If compaction is disabled (`--compaction.max-tokens 0`),
the command will return an error.

### Slash commands

Workshop exposes a small set of in-conversation slash commands that mutate
thread state without triggering an LLM turn. They are entered as the first
text of a user message and processed by the slash interceptor before the
provider is invoked. The auto-generated `/help` lists every bound command.

- `/role <name>` — switch the active role (see Roles).
- `/compact` — force compaction of the conversation history (see Compaction).
- `/thinking` — report the current thinking level and the available levels.
- `/thinking <level>` — set the thinking level for this thread, where
  `<level>` is `off`, `minimal`, `low`, `medium`, `high`, or `max`. The
  change is persisted in stream metadata, so it survives across turns and
  across thread resume, and is reflected in the TUI's status bar
  immediately. The level is per-thread; different threads can run at
  different levels simultaneously.

When running in TUI mode, the conversation history is automatically reloaded
after compaction so the display reflects the newly compacted state.

Only `SummarizeStrategy` is active; it internally handles preserving the last N turns, so no separate truncation strategy is needed.

## Debugging

### pprof

Workshop can expose Go's `net/http/pprof` debug endpoints on a separate
HTTP listener. Enable it with the `--pprof` flag (or `WORKSHOP_PPROF`
environment variable):

```bash
# TUI / stdio mode
go run ./cmd/workshop --pprof

# HTTP server mode
go run ./cmd/workshop http --pprof
```

The default address is `localhost:0` (a random unused port). Use `--pprof.addr` (or
`WORKSHOP_PPROF_ADDR`) to set a fixed address:

```bash
go run ./cmd/workshop --pprof --pprof.addr localhost:9999
```

When enabled, the profile index is available at
`http://<addr>/debug/pprof/`.

## Flags

| Flag | Environment Variable | Default | Description |
|---|---|---|---|
| `--provider.kind` | `WORKSHOP_PROVIDER_KIND` | `openai` | Provider kind (e.g. openai) |
| `--provider.api-key` | `WORKSHOP_PROVIDER_API_KEY` | — | API key for the provider (**required**) |
| `--provider.model` | `WORKSHOP_PROVIDER_MODEL` | `gpt-4o` | Model name |
| `--provider.base-url` | `WORKSHOP_PROVIDER_BASE_URL` | — | Custom API base URL |
| `--provider.temperature` | `WORKSHOP_PROVIDER_TEMPERATURE` | `0` | Sampling temperature (0 = default) |
| `--provider.thinking-level` | `WORKSHOP_PROVIDER_THINKING_LEVEL` | `off` | Thinking effort level: `off`, `minimal`, `low`, `medium`, `high`, or `max`. Shared by every backend; each adapter translates to its own wire format. |
| `--provider.max-tokens` | `WORKSHOP_PROVIDER_MAX_TOKENS` | `0` | Hard cap on output tokens per request (anthropic only; 0 = workshop default of 32000) |
| `--store.dir` | `WORKSHOP_STORE_DIR` | `$XDG_DATA_HOME/workshop/threads` | Directory for persistent JSON thread storage |
| `--format` | — | `text` | Export format (text, json, html) (thread export command only) |
| `--output` | — | — | Output file path (default: stdout) (thread export command only) |
| `--thread` | `WORKSHOP_THREAD` | — | Existing thread UUID to resume |
| `--log-level` | `WORKSHOP_LOG_LEVEL` | `info` | Log level (`debug`, `info`, `warn`, `error`) |
| `--http.addr` | `WORKSHOP_HTTP_ADDR` | `:8080` | TCP address for the HTTP server (http command only) |
| `--pprof` | `WORKSHOP_PPROF` | `false` | Enable the pprof debug server |
| `--pprof.addr` | `WORKSHOP_PPROF_ADDR` | `localhost:0` | TCP address for the pprof server |
| `--compaction.max-tokens` | `WORKSHOP_COMPACTION_MAX_TOKENS` | `100000` | Trigger compaction when total tokens exceed this threshold (0 = disabled) |
| `--telemetry.traces.endpoint` | `WORKSHOP_TELEMETRY_TRACES_ENDPOINT` | — | OpenTelemetry OTLP/HTTP endpoint URL for traces (e.g. `http://localhost:4318`); empty = disabled |
| `--telemetry.metrics.endpoint` | `WORKSHOP_TELEMETRY_METRICS_ENDPOINT` | — | OpenTelemetry OTLP/HTTP endpoint URL for metrics (e.g. `http://localhost:4318`); empty = disabled |

> **Note:** Environment variables use the `WORKSHOP_` prefix. Configuration file keys mirror the flag names (e.g., `provider.api-key`, `log-level`).

`--thread` is a per-invocation flag. It is never persisted to the config file and must be supplied on each run that resumes an existing thread.

## Roles

Workshop supports dynamic system prompts via role definitions stored as
YAML-frontmatter markdown files in the XDG data directory.

**Role directory**

- Linux / macOS: `$XDG_DATA_HOME/workshop/roles/`
  (fallback: `~/.local/share/workshop/roles/`)
- Windows: `%LOCALAPPDATA%\workshop\roles\`

**File format**

Each role is a `.md` file with YAML frontmatter delimited by `---`:

```markdown
---
name: reviewer
description: A critical code reviewer focused on bugs and performance
---
You are a senior code reviewer. Identify bugs, security issues, and
performance problems. Provide direct, actionable fixes with concrete
code suggestions.
```

> The example above is illustrative. Create your own role files in the
> XDG data directory to customize the assistant's behavior.

The frontmatter fields are:
- `name` — Display name for the role (optional; defaults to filename).
- `description` — Short summary shown in `list_roles` (optional).

Everything after the closing `---` becomes the system prompt body.

> **Note:** Role file loading is sandbox-aware. When a custom `FileSandbox` is
> configured, role paths are resolved through the sandbox before reading.

**Persistence**

Roles are stored per-thread in thread metadata. When you call
`switch_role`, the active role persists across session restarts. Resume a
thread with `--thread <uuid>` to continue with the previously selected
persona.

**Runtime context**

The system prompt automatically includes the current working directory so
the AI knows which project directory it is operating in. This helps the
assistant resolve relative paths correctly and proactively explore the
codebase.

### Project-level instructions

Workshop automatically discovers `AGENTS.md` and `CLAUDE.md` instruction
files in the working directory and its ancestors, injecting their contents
into the system prompt on every turn. Files are discovered nearest-first
(child directories before parent directories) and concatenated with blank
lines between them. If no files are found, no extra content is injected.

This lets you commit repository-wide guidance alongside your code. Create
an `AGENTS.md` or `CLAUDE.md` in your project root:

```markdown
# Project Conventions

- Use table-driven tests with sub-tests.
- Prefer `fmt.Errorf("...: %w", err)` for error wrapping.
- Run `go test -race ./...` before committing.
```

Or place one in a subdirectory for package-specific rules:

```bash
# API package conventions
cat > api/CLAUDE.md << 'EOF'
- All handlers must implement the HandlerFunc type.
- Use structured logging via slog.
EOF
```

## Available tools

| Tool | Description |
|---|---|
| `read_file` | Read file contents with optional offset/limit |
| `write_file` | Write or overwrite a file |
| `edit_file` | Replace text in a file using exact old/new string matching |
| `list_directory` | List files in a directory |
| `search_files` | Search file contents with a query string |
| `bash` | Execute shell commands with optional working directory and timeout |
| `list_roles` | List available role definitions |
| `get_current_role` | Show the currently active role for this thread |
| `switch_role` | Switch to a different role by name |
| `workspace_create` | Create a new git worktree for isolated development |
| `workspace_destroy` | Remove the git worktree created in this session |
| `git_commit` | Commit staged changes with automatic co-author attribution |

## Security notice

Workshop uses an **unsafe sandbox** by default, which means the `bash` tool can
execute arbitrary shell commands on the host without isolation. This is
convenient for local development but must not be used where untrusted code
could be executed. Replace the default sandbox with a secure implementation
(e.g., container-based) before running in any production or multi-tenant
environment.

## Built with

- [`ore`](https://github.com/andrewhowdencom/ore) — A Go-native framework for building agentic applications
- [`cobra`](https://github.com/spf13/cobra) — CLI framework
- [`viper`](https://github.com/spf13/viper) — Configuration management

## Development

Run all validation checks (lint, test, build) before committing:

```bash
task validate
```

Available tasks:

```bash
task --list
```

## Development setup

The `go.mod` in this repository uses temporary `replace` directives pointing to the local `ore` repository:

```go
replace github.com/andrewhowdencom/ore => ../ore
replace github.com/andrewhowdencom/ore/x/conduit/tui => ../ore/x/conduit/tui
// ... etc
```

These are needed because `ore` and its sub-modules are not yet published with versioned tags. Once `ore` publishes releases, these `replace` directives can be removed and normal `go get` will work.
