# 🤖 Golem

<p align="center">
  <img src="logo.png" width="500" alt="Golem Logo">
</p>


An AI agent framework with a ReAct loop, built in Go.

This project is a **Go clone** of [CrabClaw](https://github.com/jackwener/crabclaw), an OpenClaw-compatible agentic coding toolchain written in Rust. Huge thanks to [@jackwener](https://github.com/jackwener) and the CrabClaw project for the original design and inspiration.

## Features

- **Multi-channel**: CLI REPL with streaming, Lark/Feishu bot via WebSocket
- **Multiple LLM providers**: OpenAI, Anthropic, any OpenAI-compatible service
- **Tool system**: 12+ built-in tools with progressive disclosure to save tokens
- **Skill discovery**: Two-scope skill loading from `~/.golem/skills/` (global) and `~/.golem/agents/<name>/skills/` (per-agent)
- **Context management**: Tape-based conversation log with anchor/masking strategies
- **Sandboxed execution**: Filesystem and shell commands confined to workspace root
- **Persistent memory**: Optional cloud memory via mem9 (external plugin)

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

Built-in commands (prefix with `,`):

| Command | Description |
|---|---|
| `,help` | Show available commands |
| `,quit` | Exit golem |
| `,tools` | List registered tools |
| `,skills` | List discovered skills |
| `,model [name]` | Show or change model |
| `,anchor [label]` | Add context boundary |
| `,tape.info` | Tape statistics |
| `,tape.search <q>` | Search conversation history |

## Configuration

Set via environment variables or `.env` file:

| Variable | Description |
|---|---|
| `GOLEM_MODEL` | Provider and model (e.g. `openai:gpt-4o`, `anthropic:claude-sonnet-4-20250514`) |
| `<PROVIDER>_API_KEY` | API key for the provider |
| `<PROVIDER>_BASE_URL` | Custom endpoint (optional) |
| `GOLEM_MAX_TOOL_ITER` | Max tool calls per message (default: 15) |
| `GOLEM_SHELL_TIMEOUT` | Shell command timeout (default: 30s) |
| `LARK_APP_ID` / `LARK_APP_SECRET` | Lark bot credentials (optional) |

See [docs/architecture.md](docs/architecture.md) for the full configuration reference.

## Architecture

See [docs/architecture.md](docs/architecture.md) for a detailed description of the project structure, core interfaces, agent loop, tool system, and more.

## Development

```sh
make check    # gofmt + go vet + go test
make fmt      # format code
make lint     # golangci-lint
make test     # go test ./...
make clean    # remove build artifacts
```

## Roadmap

- [ ] **Parallel tool execution** — When the LLM returns multiple tool calls in a single response, execute them concurrently using an errgroup instead of sequentially (like Claude Code running independent file reads in parallel).
- [ ] **Enhanced CLI UI** — Improve the interactive REPL to show:
  - Tool execution inline with name, arguments, and a spinner/status while running
  - Visual separators (blank lines) between each agent reasoning block
  - Per-session token usage (input/output tokens and cumulative totals, shown after each turn or via `,usage`)
- [x] **Redact secrets before sending to LLM** — Detect and mask sensitive values (API keys, passwords, tokens) in config files and tool outputs before they are included in the LLM context, preventing accidental credential leakage.

## Acknowledgements

This project is inspired by and based on [CrabClaw](https://github.com/jackwener/crabclaw) by [@jackwener](https://github.com/jackwener). Thank you for the excellent original design.

## License

See [LICENSE](LICENSE) for details.
