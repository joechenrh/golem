# Getting Started

## Prerequisites

- **Go 1.22+** — Golem uses modern Go idioms (`any`, range-over-int, `slices` package)
- An API key for at least one LLM provider (OpenAI, Anthropic, or compatible)

## Installation

```sh
git clone https://github.com/joechenrh/golem.git
cd golem
make build
```

The binary is built to `./golem`.

## Configuration

Copy the example environment file and set your credentials:

```sh
cp .env.example .env
```

At minimum, set:

```sh
GOLEM_MODEL=openai:gpt-4o
OPENAI_API_KEY=sk-...
```

See [Configuration](configuration.md) for all available options.

## Running

```sh
make run
```

This starts an interactive REPL. Type your request and the agent will reason, call tools, and respond.

## REPL Commands

All commands are prefixed with `:`:

| Command | Description |
|---|---|
| `:help` | Show available commands |
| `:quit` | Exit golem |
| `:usage` | Show token usage statistics |
| `:metrics` | Show operational metrics |
| `:tools` | List registered tools |
| `:skills` | List discovered skills |
| `:model [name]` | Show or change model |
| `:reset [label]` | Add context boundary (tape anchor) |
| `:tape.info` | Tape statistics |
| `:tape.search <q>` | Search conversation history |
| `:<command>` | Execute a shell command (e.g. `:ls -la`) |

## Built-in Tools

| Category | Tools |
|---|---|
| **File operations** | `read_file`, `write_file`, `edit_file`, `list_directory`, `search_files` |
| **Shell** | `shell_exec` |
| **Web** | `web_search`, `web_fetch`, `http_request` |
| **Lark/Feishu** | `lark_send`, `lark_list_chats`, `lark_read_doc`, `lark_write_doc` |
| **Agent** | `spawn_agent`, `check_tasks`, `persona_self`, `create_skill` |
| **Scheduler** | `schedule_add`, `schedule_list`, `schedule_remove` |

Tools use **progressive disclosure**: only a minimal schema is sent initially, expanding to the full parameter schema when referenced.

## Development

```sh
make check    # gofmt + go vet + go test
make fmt      # format code
make lint     # golangci-lint
make test     # go test ./...
make clean    # remove build artifacts
```

## Next Steps

- Read the [Architecture Overview](design/01-architecture.md) to understand how Golem works
- Learn about [Skills](guides/skills.md) to extend the agent with custom workflows
- Set up [External Plugins](guides/plugins.md) for additional tool integrations
