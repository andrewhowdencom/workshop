# workshop

A terminal-based coding assistant built on [`ore`](https://github.com/andrewhowdencom/ore).

This project demonstrates how to build a fully fledged agentic application outside the `ore` examples directory. It wires together the TUI conduit, system prompt transforms, guardrails, filesystem tools, and a bash execution tool to create an interactive coding agent that can read, write, edit, search, and execute shell commands.

## Prerequisites

- [Go](https://go.dev/) 1.26+
- An OpenAI API key (or compatible API endpoint)
- [Task](https://taskfile.dev/) (optional, for development tasks)

## Commands

| Command | Description |
|---|---|
| `workshop` | Open the interactive TUI (default) |
| `workshop http` | Run the web UI HTTP server |
| `workshop config init` | Initialize a configuration file from current settings |
| `workshop version` | Print the build version |

## Usage

### TUI (default)

```bash
export WORKSHOP_PROVIDER_API_KEY=sk-...
export WORKSHOP_PROVIDER_MODEL=gpt-4o   # optional; defaults to gpt-4o
go run ./cmd/workshop
```

### Web UI (HTTP server)

```bash
go run ./cmd/workshop http
```

With custom port:

```bash
go run ./cmd/workshop http --http.addr :7654
```

The web chat UI is available at `http://localhost:8080/` (or the configured address).

### Resume an existing thread

```bash
go run ./cmd/workshop --thread <uuid>
```

### Persistent JSON store

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

For example, setting `WORKSHOP_LOG_LEVEL=debug` overrides `log-level: info` in the config file, unless `--log-level` is also supplied.

> **Security notice:** `config init` writes `provider.api-key` in plaintext. Ensure the generated file is stored securely and never committed to a public repository.

Example `config.yaml`:

```yaml
log-level: info
provider:
  kind: openai
  api-key: sk-...
  model: gpt-4o
  base-url: ""
store:
  dir: ""
http:
  addr: ":8080"
```

### Deprecated variables

The previous `ORE_*` and `STORE_DIR` environment variables are no longer supported. Use the `WORKSHOP_` prefix instead.

## Flags

| Flag | Environment Variable | Default | Description |
|---|---|---|---|
| `--provider.kind` | `WORKSHOP_PROVIDER_KIND` | `openai` | Provider kind (e.g. openai) |
| `--provider.api-key` | `WORKSHOP_PROVIDER_API_KEY` | — | API key for the provider (**required**) |
| `--provider.model` | `WORKSHOP_PROVIDER_MODEL` | `gpt-4o` | Model name |
| `--provider.base-url` | `WORKSHOP_PROVIDER_BASE_URL` | — | Custom API base URL |
| `--store.dir` | `WORKSHOP_STORE_DIR` | — | Directory for persistent JSON thread storage |
| `--thread` | `WORKSHOP_THREAD` | — | Existing thread UUID to resume |
| `--log-level` | `WORKSHOP_LOG_LEVEL` | `info` | Log level (`debug`, `info`, `warn`, `error`) |
| `--http.addr` | `WORKSHOP_HTTP_ADDR` | `:8080` | TCP address for the HTTP server (http command only) |

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
You are a senior code reviewer. Your job is to find bugs, security
issues, and performance problems. Be direct and actionable. Suggest
concrete fixes.
```

The frontmatter fields are:
- `name` — Display name for the role (optional; defaults to filename).
- `description` — Short summary shown in `list_roles` (optional).

Everything after the closing `---` becomes the system prompt body.

**Persistence**

Roles are stored per-thread in thread metadata. When you call
`switch_role`, the active role persists across session restarts. Resume a
thread with `--thread <uuid>` to continue with the previously selected
persona.

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
