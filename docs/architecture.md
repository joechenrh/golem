# Golem Architecture

Golem is a Go-based AI agent framework implementing a ReAct (Reasoning + Acting) loop. It supports multiple communication channels, LLM providers, and a pluggable tool system.

## Directory Layout

```
golem/
├── cmd/golem/main.go              # Entry point, wiring
├── internal/
│   ├── agent/                     # ReAct loop orchestration
│   ├── channel/                   # Message I/O interface
│   │   ├── cli/                   # Interactive terminal REPL
│   │   ├── lark/                  # Lark/Feishu bot (WebSocket)
│   │   └── telegram/              # Telegram (stub)
│   ├── config/                    # Env + flag configuration
│   ├── ctxmgr/                    # Context window strategies
│   ├── executor/                  # Shell command execution
│   ├── fs/                        # Sandboxed filesystem
│   ├── hooks/                     # Lifecycle event bus
│   ├── llm/                       # LLM abstraction & providers
│   ├── memory/                    # Persistent memory (mnemos/TiDB)
│   ├── router/                    # User/assistant command routing
│   ├── tape/                      # Append-only conversation log
│   └── tools/                     # Tool registry & built-ins
│       └── builtin/               # File, shell, web, Lark, memory tools
├── .agent/skills/                 # Discoverable skill prompts
├── docs/                          # Documentation
├── plan/                          # Design documents
├── Makefile                       # Build targets
└── go.mod                         # Go 1.23, module deps
```

## Core Interfaces

### Channel (`internal/channel/channel.go`)

Abstracts message I/O for different platforms.

```go
type Channel interface {
    Name() string
    Start(ctx context.Context, inCh chan<- IncomingMessage) error
    Send(ctx context.Context, msg OutgoingMessage) error
    SendTyping(ctx context.Context, channelID string) error
    SupportsStreaming() bool
    SendStream(ctx context.Context, channelID string, tokenCh <-chan string) error
}
```

| Implementation | Transport | Streaming | Notes |
|---|---|---|---|
| `cli.CLIChannel` | stdin/stdout | Yes | Colored output, thinking indicator |
| `lark.LarkChannel` | WebSocket | No | Interactive cards with markdown |
| `telegram.TelegramChannel` | — | — | Stub, not implemented |

### Tool (`internal/tools/tool.go`)

An action the LLM can invoke.

```go
type Tool interface {
    Name() string
    Description() string
    FullDescription() string
    Parameters() json.RawMessage
    Execute(ctx context.Context, args string) (string, error)
}
```

### LLM Client (`internal/llm/client.go`)

Provider-agnostic LLM access.

```go
type Client interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
    Provider() Provider
}
```

Providers: **OpenAI**, **Anthropic**, any **OpenAI-compatible** service (registered dynamically via `RegisterProvider()`).

### Context Strategy (`internal/ctxmgr/strategy.go`)

Controls how conversation history is assembled for LLM calls.

```go
type ContextStrategy interface {
    BuildContext(ctx context.Context, entries []tape.TapeEntry, maxTokens int) ([]llm.Message, error)
    Name() string
}
```

| Strategy | Behavior |
|---|---|
| `AnchorStrategy` | All messages since last anchor; drops oldest on overflow |
| `MaskingStrategy` | Extends anchor; truncates large tool outputs near limit (default) |

### Executor (`internal/executor/executor.go`)

Runs shell commands.

```go
type Executor interface {
    Execute(ctx context.Context, command string, timeout time.Duration) (*Result, error)
    Name() string
}
```

`LocalExecutor` runs via `/bin/sh -c`; `NoopExecutor` is for testing.

### Filesystem (`internal/fs/fs.go`)

Sandboxed file operations with workspace-root enforcement. Rejects paths that escape the root (including via symlinks).

### Tape Store (`internal/tape/store.go`)

Append-only JSONL conversation log. Entries are typed (`message`, `anchor`, `event`). Anchors act as context boundaries for the context strategies. Thread-safe with mutex.

### Hook (`internal/hooks/hooks.go`)

Lifecycle events: `EventUserMessage`, `EventBeforeLLMCall`, `EventAfterLLMCall`, `EventBeforeToolExec`, `EventAfterToolExec`, `EventError`. `before_*` hooks can block actions by returning an error.

## Agent Loop (`internal/agent/agent.go`)

The `AgentLoop` orchestrates the ReAct cycle:

```
User Input
    │
    ▼
Router (check for , commands)
    │
    ▼
ReAct Loop (runReActLoop)
    ├─ Tape entries → Messages (via ContextStrategy)
    ├─ LLM call (with tool schemas)
    ├─ Tool calls? → Execute each → Record results → Loop
    └─ No tool calls? → Return final answer
    │
    ▼
Channel.Send / Channel.SendStream
```

Key methods:
- `HandleInput()` — non-streaming path
- `HandleInputStream()` — streaming path (tokens sent to channel in real time)
- `runReActLoop()` — core loop, max `GOLEM_MAX_TOOL_ITER` iterations (default 15)

System prompt is built dynamically (working directory, timestamp) and loads `.agent/system-prompt.md` if present.

## Tool Registry (`internal/tools/registry.go`)

### Progressive Disclosure

Tools start in **compact mode** (short description, empty parameter schema) to save tokens. When the LLM calls or mentions a tool, it's **auto-expanded** (full description + full JSON Schema) for the next iteration. This reduces prompt size without sacrificing capability.

### Built-in Tools

| Tool | Package | Purpose |
|---|---|---|
| `read_file` | `builtin` | Read file contents (offset/limit for large files) |
| `write_file` | `builtin` | Create or overwrite files |
| `edit_file` | `builtin` | Edit specific line ranges |
| `list_directory` | `builtin` | Recursive listing, respects `.gitignore` |
| `search_files` | `builtin` | Full-text search across workspace |
| `shell_exec` | `builtin` | Shell commands with timeout |
| `web_search` | `builtin` | Bing web search (configurable backend) |
| `web_fetch` | `builtin` | Fetch and parse HTML from URLs |
| `lark_send` | `builtin` | Send message to Lark group chat |
| `lark_list_chats` | `builtin` | List bot's Lark group chats |
| `memory_store` | `builtin` | Save to persistent memory (mnemos) |
| `memory_recall` | `builtin` | Vector-similarity memory search |

### Skills

Discovered from `.agent/skills/*/SKILL.md` files. Each skill has YAML frontmatter (name, description) and a markdown body that's injected as a prompt — no executable code.

## Configuration (`internal/config/config.go`)

Precedence: CLI flags > environment variables (`.env` auto-loaded) > defaults.

| Variable | Default | Description |
|---|---|---|
| `GOLEM_MODEL` | — | Provider:model (e.g. `openai:gpt-4o`) |
| `<PROVIDER>_API_KEY` | — | API key for the provider |
| `<PROVIDER>_BASE_URL` | — | Custom endpoint for the provider |
| `GOLEM_MAX_TOOL_ITER` | 15 | Max tool calls per user message |
| `GOLEM_SHELL_TIMEOUT` | 30s | Shell command timeout |
| `GOLEM_CONTEXT_STRATEGY` | masking | `anchor` or `masking` |
| `GOLEM_EXECUTOR` | local | `local` or `noop` |
| `GOLEM_TAPE_DIR` | ~/.golem/tapes | Conversation log directory |
| `GOLEM_SKILLS_DIR` | .agent/skills | Skill discovery directory |
| `GOLEM_LOG_LEVEL` | info | Log level |
| `LARK_APP_ID` | — | Lark bot app ID |
| `LARK_APP_SECRET` | — | Lark bot app secret |
| `LARK_VERIFY_TOKEN` | — | Lark event verification token |
| `GOLEM_WEB_SEARCH_BACKEND` | bing | `bing` or `stub` |
| `MNEMO_DB_HOST` | — | TiDB host for mnemos memory |

## User Commands

Prefixed with `,` in the CLI:

| Command | Description |
|---|---|
| `,help` | Show available commands |
| `,quit` | Exit golem |
| `,tape.info` | Tape statistics |
| `,tape.search <q>` | Search conversation history |
| `,tools` | List registered tools |
| `,skills` | List discovered skills |
| `,model [name]` | Show or change model |
| `,anchor [label]` | Add context boundary |
| `,<cmd>` | Execute shell command |

## Initialization Flow (`cmd/golem/main.go`)

1. Parse CLI flags
2. Load config (env + flags)
3. Create logger (JSONL to tape directory)
4. Create LLM client from provider string
5. Create tape store (session JSONL file)
6. Create executor and sandboxed filesystem
7. Create context strategy
8. Create hook bus (with logging hook)
9. Create CLI channel
10. Create Lark channel (if configured)
11. Build tool registry: file tools, shell, web, Lark, memory, skills
12. Create `AgentLoop`
13. Start message processing goroutine
14. Start Lark channel in background (if configured)
15. Start CLI REPL (blocks)

## Limits

| Resource | Limit |
|---|---|
| Shell output | 50 KB |
| File read | 50,000 chars |
| Directory listing | 200 entries |
| File search results | 50 matches |
| Web search results | 5 default, 20 max |
| Tool iterations | 15 per message |
| Token estimation | ~4 chars/token |

## Dependencies

| Module | Purpose |
|---|---|
| `github.com/larksuite/oapi-sdk-go/v3` | Lark/Feishu SDK |
| `go.uber.org/zap` | Structured logging |
| `github.com/google/uuid` | UUID generation |
| `github.com/joho/godotenv` | `.env` file loading |
| `golang.org/x/net` | HTML parsing |

## Build & Test

```sh
make build          # → bin/golem
make run            # build + execute
make test           # go test ./...
make check          # gofmt + go vet + go test
make fmt            # gofmt -w .
make lint           # golangci-lint
make clean          # rm -rf bin/
```
