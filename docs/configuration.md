# Configuration

Golem is configured via environment variables or a `.env` file. Agent-specific overrides go in `~/.golem/agents/<name>/config.env`.

## Environment Variables

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
| `LARK_APP_ID` | Lark bot app ID | — |
| `LARK_APP_SECRET` | Lark bot app secret | — |

## Model Format

The `GOLEM_MODEL` variable uses the format `provider:model-name`:

```sh
# OpenAI
GOLEM_MODEL=openai:gpt-4o

# Anthropic
GOLEM_MODEL=anthropic:claude-sonnet-4-20250514

# Custom OpenAI-compatible endpoint
GOLEM_MODEL=openai:my-model
OPENAI_BASE_URL=https://my-service.example.com/v1
```

## Context Strategies

Golem supports three context management strategies:

- **`anchor`** — Manual context boundaries via `:reset` command. Older entries before the last anchor are dropped when context is tight.
- **`masking`** — Automatic masking of older entries based on relevance scoring. Most hands-off approach.
- **`hybrid`** — Combines both strategies. Uses anchors as hard boundaries and masking within segments.

See [Context Manager](design/05-context-manager.md) for implementation details.

## Per-Agent Configuration

Each agent can have its own configuration overlay:

```
~/.golem/agents/<name>/
├── config.env          # Environment overrides
├── SOUL.md             # Agent personality/identity
├── MEMORY.md           # Persistent memory
└── skills/             # Agent-specific skills
    └── my-skill/
        └── SKILL.md
```

See [Config & Persona](design/09-config-persona.md) for the full configuration system.

## Tool Access Control

Restrict which tools are available using allowlist/blocklist:

```sh
# Only allow file operations and shell
GOLEM_TOOL_ALLOW=read_file,write_file,edit_file,shell_exec

# Block web access
GOLEM_TOOL_DENY=web_search,web_fetch,http_request
```

See [Safety & Sandbox](design/12-safety-sandbox.md) for more security options.
