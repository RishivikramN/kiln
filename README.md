# kiln

A terminal AI coding agent. Kiln runs in your terminal, connects to whichever LLM provider you have credentials for, and can read, write, and run code in your repository.

## Providers

Kiln auto-detects available providers at startup in priority order:

| Priority | Provider | Env var |
|----------|----------|---------|
| 1 | Anthropic Claude | `ANTHROPIC_API_KEY` |
| 2 | OpenAI | `OPENAI_API_KEY` |
| 3 | Google Gemini | `GEMINI_API_KEY` |
| 4 | Ollama | `OLLAMA_HOST` (default: `http://localhost:11434`) |

The highest-priority available provider is used by default. Switch at runtime with `/models`.

Claude and Gemini are implemented via direct HTTP streaming (no SDK). OpenAI and Ollama share the `openai-go` client since Ollama exposes an OpenAI-compatible API.

## Install

Requires Go 1.21+.

```sh
git clone <repo>
cd kiln
make build   # runs tests, then produces ./kiln
```

Or without make:

```sh
go test ./... && go build -o kiln .
```

## Usage

```sh
cd /your/repo
kiln
```

Kiln starts in the working directory where you launch it. Set permissions for that repo before asking it to touch files:

```
/permissions allow
```

Then just talk to it. It reads and writes files on its own — you don't direct it to use any tools.

## Slash commands

```
/models                     list all available models across all connected providers
/models <provider/model>    switch active model  (e.g. /models openai/gpt-4o)
/permissions                show per-repo permission state
/permissions allow          grant read+write for the current repo
/permissions read-only      grant read-only for the current repo
/permissions deny           revoke access
/system <text>              append a custom instruction to the system prompt
/system                     clear the custom instruction
/undo                       remove the last user+assistant exchange
/copy                       copy the last assistant response to the clipboard
/clear                      clear chat history (context window resets)
/exit                       quit
```

## Keyboard

| Key | Action |
|-----|--------|
| `↑` / `↓` | Browse input history |
| `PgUp` / `PgDn` | Scroll chat |
| `Ctrl+C` | Cancel in-flight request (first press); exit (second press / idle) |
| `Ctrl+L` | Force redraw |

## Permissions

Permissions are per-repo and persisted to `~/.kiln/permissions.json`.

| Mode | Tools available |
|------|----------------|
| `read-write` | `list_files`, `read_file`, `write_file`, `run_command` |
| `read-only` | `list_files`, `read_file` |
| `none` | — |

With no permissions set the agent can still answer questions but cannot touch the filesystem or run commands.

## Tools

The agent has four built-in tools. They are invisible to the user — the system prompt instructs the model to call them silently without narrating what it's doing.

- **`list_files`** — lists a directory relative to the repo root
- **`read_file`** — reads a file (truncated at 40 KB)
- **`write_file`** — writes a file, creating parent directories as needed; renders an inline diff in the chat
- **`run_command`** — runs a shell command in the repo root via `sh -c`, 30s timeout, captures stdout+stderr

All file paths are validated to prevent traversal outside the repo root.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `ANTHROPIC_API_KEY` | — | Anthropic API key |
| `OPENAI_API_KEY` | — | OpenAI API key |
| `GEMINI_API_KEY` | — | Google Gemini API key |
| `OLLAMA_HOST` | `http://localhost:11434` | Ollama server base URL |

No config file. Everything else is set at runtime via slash commands.

## Package layout

```
main.go                         provider init + TUI wiring
internal/
  provider/
    provider.go                 Provider interface, Message and Tool types, history role constants
    anthropic/claude.go         Claude provider — direct HTTP SSE
    openai/openai.go            OpenAI + Ollama provider — openai-go SDK
    gemini/gemini.go            Gemini provider — direct HTTP SSE
  tools/tools.go                built-in tool definitions and SafeJoin path guard
  diff/diff.go                  LCS diff algorithm, StorePending/TakePending side-channel
  render/render.go              markdown segment parser, code block and diff ANSI renderer
  permissions/permissions.go    per-repo permission store, persisted to ~/.kiln/
  tui/
    tui.go                      TUI struct, provider registration, Run loop
    chat.go                     runChat goroutine — history filtering, spinner, provider.Chat call
    display.go                  renderLocked, chatLines, ANSI layout
    input.go                    key handling, submit, input history
    commands.go                 slash command handlers
    prompt.go                   system prompt constant
    signal.go                   SIGWINCH resize watcher, chat context timeout
```

The TUI uses raw terminal mode with ANSI escape codes directly — no TUI framework. The only external dependency is `openai-go`.

## Context and history

Tool call context is preserved across conversation turns. Each provider serializes tool calls and results into the message history in its native format (plain text for Ollama, JSON-encoded native structs for OpenAI, Claude, and Gemini), so the model retains full context of what it did in previous turns.

Each chat turn has a 5-minute timeout. Ctrl+C cancels an in-flight request without exiting.
