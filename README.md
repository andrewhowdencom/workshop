# workshop

A terminal-based coding assistant built on [`ore`](https://github.com/andrewhowdencom/ore).

This project demonstrates how to build a fully fledged agentic application outside the `ore` examples directory. It wires together the TUI conduit, system prompt transforms, guardrails, filesystem tools, and a bash execution tool to create an interactive coding agent that can read, write, edit, search, and execute shell commands.

## Prerequisites

- [Go](https://go.dev/) 1.26+
- An OpenAI API key (or compatible API endpoint)
- [Task](https://taskfile.dev/) (optional, for development tasks)

## Usage

```bash
export WORKSHOP_API_KEY=sk-...
export WORKSHOP_MODEL=gpt-4o   # optional; defaults to gpt-4o
go run ./cmd/workshop
```

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

## Commands

| Command | Description |
|---|---|
| `workshop` | Open the interactive TUI (default) |
| `workshop version` | Print the build version |

## Flags

| Flag | Environment Variable | Default | Description |
|---|---|---|---|
| `--api.key` | `WORKSHOP_API_KEY` | — | OpenAI-compatible API key (**required**) |
| `--model` | `WORKSHOP_MODEL` | `gpt-4o` | Model name |
| `--base.url` | `WORKSHOP_BASE_URL` | — | Custom API base URL |
| `--store.dir` | `WORKSHOP_STORE_DIR` | — | Directory for persistent JSON thread storage |
| `--thread` | `WORKSHOP_THREAD` | — | Existing thread UUID to resume |
| `--log-level` | `WORKSHOP_LOG_LEVEL` | `info` | Log level (`debug`, `info`, `warn`, `error`) |

> **Note:** Environment variables use the `WORKSHOP_` prefix. The previous `ORE_*` and `STORE_DIR` variables are no longer supported.

## Available tools

| Tool | Description |
|---|---|
| `read_file` | Read file contents with optional offset/limit |
| `write_file` | Write or overwrite a file |
| `edit_file` | Replace text in a file using exact old/new string matching |
| `list_directory` | List files in a directory |
| `search_files` | Search file contents with a query string |
| `bash` | Execute shell commands with optional working directory and timeout |

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
