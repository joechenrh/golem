# Golem

<p align="center">
  <img src="logo.png" width="500" alt="Golem Logo">
</p>

An AI agent framework with a ReAct loop, built in Go.

This project is a **Go clone** of [CrabClaw](https://github.com/jackwener/crabclaw), an OpenClaw-compatible agentic coding toolchain written in Rust. Huge thanks to [@jackwener](https://github.com/jackwener) and the CrabClaw project for the original design and inspiration.

## Features

- **Multi-channel**: CLI REPL with streaming, Lark/Feishu bot via WebSocket
- **Multiple LLM providers**: OpenAI, Anthropic, any OpenAI-compatible service
- **21 built-in tools** with progressive disclosure to save tokens
- **Parallel tool execution**: Multiple tool calls run concurrently via errgroup
- **Async subagent orchestration**: `spawn_agent` launches background tasks that run independently; main session stays responsive with `check_tasks` for status monitoring
- **Skill system**: Two-scope skill discovery from `~/.golem/skills/` (global) and `~/.golem/agents/<name>/skills/` (per-agent), plus runtime skill creation
- **Context management**: Tape-based conversation log with three strategies (anchor, masking, hybrid) and overhead budgeting for system prompt + tool schemas
- **Persona system**: Three-layer agent identity (SOUL.md, AGENTS.md, MEMORY.md) with shared USER.md
- **External hooks**: User-defined shell commands at lifecycle points (before/after LLM calls, reset, context dropped)
- **Persistent memory**: Optional cloud memory via [mem9](configs/mem9/) with smart recall and dropped-context saving
- **Task scheduler**: Cron-based background task execution with isolated sessions
- **Safety**: Filesystem/shell sandboxing, secret redaction, per-tool ACLs
- **Per-chat sessions**: Isolated sessions per channel with idle eviction, capacity management, and summarization on all exit paths

## Quick Start

```sh
# Build
make build

# Configure (copy and edit)
cp .env.example .env
# Set GOLEM_MODEL and API key, e.g.:
#   GOLEM_MODEL=openai:gpt-4o
#   OPENAI_API_KEY=sk-...

# Run
make run
```

## Usage

Golem starts an interactive REPL. Type your request and the agent will reason, call tools, and respond.

Built-in commands (prefix with `:`):

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

Remote channels (Lark) also support `/help`, `/new`, `/status`.

## Built-in Tools

| Category | Tools |
|---|---|
| **File operations** | `read_file`, `write_file`, `edit_file`, `list_directory`, `search_files` |
| **Shell** | `shell_exec` |
| **Web** | `web_search`, `web_fetch`, `http_request` |
| **Lark/Feishu** | `lark_send`, `lark_list_chats`, `lark_read_doc`, `lark_write_doc`, `chat_history` |
| **Agent** | `spawn_agent`, `check_tasks`, `persona_self`, `create_skill` |
| **Scheduler** | `schedule_add`, `schedule_list`, `schedule_remove` |

Tools use progressive disclosure: only a minimal schema is sent initially, expanding to the full parameter schema when the LLM calls a tool.

## Configuration

Set via environment variables or `.env` file. Agent-specific overrides go in `~/.golem/agents/<name>/config.env`.

| Variable | Description | Default |
|---|---|---|
| `GOLEM_MODEL` | Provider and model (e.g. `openai:gpt-4o`, `anthropic:claude-sonnet-4-20250514`) | — |
| `<PROVIDER>_API_KEY` | API key for the provider | — |
| `<PROVIDER>_BASE_URL` | Custom endpoint | provider default |
| `GOLEM_CONTEXT_STRATEGY` | Context strategy: `anchor`, `masking`, `hybrid` | `masking` |
| `GOLEM_MAX_TOOL_ITER` | Max tool calls per message | `15` |
| `GOLEM_MAX_OUTPUT_TOKENS` | Max LLM response tokens | `4096` |
| `GOLEM_SHELL_TIMEOUT` | Shell command timeout | `30s` |
| `GOLEM_WORKSPACE_DIR` | Agent workspace root | CWD or per-agent dir |
| `GOLEM_MAX_SESSIONS` | Max concurrent per-chat sessions | `100` |
| `GOLEM_SESSION_IDLE_TIME` | Idle session eviction timeout | `24h` |
| `GOLEM_TOOL_ALLOW` | Allowlist of tools (comma-separated) | all |
| `GOLEM_TOOL_DENY` | Blocklist of tools | none |
| `GOLEM_LOG_LEVEL` | Log verbosity: `debug`, `info`, `warn`, `error` | `info` |
| `LARK_APP_ID` / `LARK_APP_SECRET` | Lark bot credentials | — |

## Architecture

Design documents for all major subsystems live in the [`design/`](design/) directory:

| Doc | Topic |
|---|---|
| [01-architecture](design/01-architecture.md) | System overview, startup, shutdown, data flow |
| [02-agent-session](design/02-agent-session.md) | ReAct loop, tool calling |
| [03-llm-client](design/03-llm-client.md) | LLM provider abstraction |
| [04-tape](design/04-tape.md) | Conversation logging (JSONL) |
| [05-context-manager](design/05-context-manager.md) | Context strategies, overhead budgeting, HybridStrategy pipeline |
| [06-tools](design/06-tools.md) | Tool system, progressive disclosure, middleware |
| [07-channels](design/07-channels.md) | Channel interface, message dispatch |
| [08-hooks](design/08-hooks.md) | Internal hook bus, external hooks |
| [09-config-persona](design/09-config-persona.md) | Two-tier config, three-layer persona |
| [10-memory](design/10-memory.md) | Memory persistence |
| [11-scheduler](design/11-scheduler.md) | Cron-based task scheduling |
| [12-safety-sandbox](design/12-safety-sandbox.md) | Filesystem/shell confinement, redaction |
| [13-metrics](design/13-metrics.md) | Observability |

## Development

```sh
make check    # gofmt + go vet + go test
make fmt      # format code
make lint     # golangci-lint
make test     # go test ./...
make clean    # remove build artifacts
```

## Acknowledgements

This project is inspired by and based on [CrabClaw](https://github.com/jackwener/crabclaw) by [@jackwener](https://github.com/jackwener). Thank you for the excellent original design.

## License

See [LICENSE](LICENSE) for details.
