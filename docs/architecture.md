# Golem Architecture

Golem is a Go-based AI agent framework implementing a ReAct (Reasoning + Acting) loop. It supports multiple communication channels, LLM providers, pluggable tools, and per-chat session isolation for concurrent multi-user operation.

## Directory Layout

```
golem/
├── cmd/golem/main.go                 # Entry point: flags, logger, signal handling
├── internal/
│   ├── agent/                        # ReAct loop orchestration
│   │   ├── agent.go                  # Session: ReAct loop, LLM calls, tool dispatch
│   │   └── session.go                # SessionManager: per-chat isolation
│   ├── app/                          # Wiring layer (separate from main for testability)
│   │   └── app.go                    # AgentInstance, BuildAgent, Run, message dispatch
│   ├── channel/                      # Message I/O abstraction
│   │   ├── channel.go                # Channel + SystemPrinter interfaces
│   │   ├── cli/cli.go                # Interactive terminal REPL
│   │   ├── lark/lark.go              # Lark/Feishu bot (WebSocket + card streaming)
│   │   └── telegram/telegram.go      # Telegram bot (long-polling)
│   ├── config/                       # Two-tier configuration
│   │   └── config.go                 # Global + per-agent config, env/flag loading
│   ├── ctxmgr/                       # Context window management
│   │   └── strategy.go               # AnchorStrategy, MaskingStrategy, token estimation
│   ├── executor/                     # Shell command execution
│   │   ├── executor.go               # Executor interface + Result type
│   │   ├── local.go                  # LocalExecutor (/bin/sh -c)
│   │   └── noop.go                   # NoopExecutor (testing)
│   ├── fs/                           # Sandboxed filesystem
│   │   ├── fs.go                     # FS interface + SandboxError
│   │   └── local.go                  # LocalFS with workspace-root enforcement
│   ├── hooks/                        # Lifecycle event bus
│   │   ├── hooks.go                  # Bus, Hook interface, EventTypes
│   │   ├── logging.go                # LoggingHook (structured event logging)
│   │   ├── safety.go                 # SafetyHook (shell/SSRF/file-write blocking)
│   │   └── metrics.go                # MetricsHook (tokens, latency, tool stats)
│   ├── llm/                          # LLM provider abstraction
│   │   ├── types.go                  # Message, ToolCall, ChatRequest/Response, Usage
│   │   ├── client.go                 # Client interface, provider registry, rate limiter
│   │   ├── openai.go                 # OpenAI provider (SSE streaming)
│   │   ├── anthropic.go              # Anthropic provider (SSE streaming)
│   │   ├── stream.go                 # Shared streaming helpers
│   │   └── retry.go                  # Retry wrapper with backoff
│   ├── memory/                       # Persistent vector memory
│   │   └── client.go                 # mnemos direct-mode client (TiDB Serverless HTTP)
│   ├── middleware/                    # Tool execution middleware
│   │   ├── middleware.go             # Middleware type definition
│   │   ├── cache.go                  # CacheMiddleware (TTL-based, read-only tools)
│   │   └── redact.go                 # Redact middleware (masks secrets in results)
│   ├── redact/                       # Secret detection patterns
│   │   └── redact.go                 # Redactor with regex-based secret masking
│   ├── router/                       # Command routing
│   │   └── router.go                 # RouteUser, RouteAssistant, ParseArgs
│   ├── tape/                         # Append-only conversation log
│   │   ├── store.go                  # FileStore (JSONL, persistent file handle)
│   │   ├── entry.go                  # TapeEntry, BuildMessages, PayloadMap
│   │   └── discover.go               # Session tape discovery, chat ID extraction
│   └── tools/                        # Tool system
│       ├── tool.go                   # Tool interface
│       ├── registry.go               # Registry with progressive disclosure + middleware
│       ├── progressive.go            # ExpandHints (auto-expand from text references)
│       ├── skill.go                  # SKILL.md discovery and parsing
│       └── builtin/                  # Built-in tool implementations
│           ├── file_ops.go           # read_file, write_file, edit_file, list_directory, search_files
│           ├── shell_tool.go         # shell_exec
│           ├── web_tool.go           # web_search, web_fetch
│           ├── lark_tool.go          # lark_send, lark_list_chats
│           ├── lark_doc_tool.go      # lark_read_doc, lark_write_doc
│           ├── memory_tool.go        # memory_store, memory_recall
│           ├── spawn_tool.go         # spawn_agent (sub-agent delegation)
│           └── stubs.go              # Stub definitions
├── .agent/skills/                    # Discoverable skill prompts (SKILL.md)
├── docs/                             # Documentation
├── plan/                             # Design documents
├── Makefile
└── go.mod                            # Go 1.23
```

---

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

type SystemPrinter interface {
    PrintSystem(text string)
    PrintError(text string)
    PrintBanner(model string, toolCount int, tapePath string)
}
```

`IncomingMessage` carries `ChannelID`, `ChannelName`, `SenderID`, `SenderName`, `Text`, `Metadata`, and a `Done` channel closed when processing completes.

| Implementation | Transport | Streaming | Notes |
|---|---|---|---|
| `cli.CLIChannel` | stdin/stdout | Yes | Colored output, thinking indicator |
| `lark.LarkChannel` | WebSocket | Yes | Progressive card patching (800ms interval, ▍ cursor) |
| `telegram.TelegramChannel` | Long-polling | No | Basic implementation |

### Tool (`internal/tools/tool.go`)

An action the LLM can invoke.

```go
type Tool interface {
    Name() string
    Description() string        // Short (compact mode)
    FullDescription() string    // Full (expanded mode)
    Parameters() json.RawMessage // JSON Schema
    Execute(ctx context.Context, args string) (string, error)
}
```

### LLM Client (`internal/llm/client.go`)

Provider-agnostic LLM access with streaming support.

```go
type Client interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
    Provider() Provider
}
```

Built-in providers: **OpenAI**, **Anthropic**. Custom providers are auto-registered as OpenAI-compatible via `RegisterProvider()`. All clients can be wrapped with `RateLimitedClient` for token-bucket rate limiting.

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
| `MaskingStrategy` | Extends anchor; truncates large tool outputs when tokens exceed 50% threshold (default) |

Token estimation is CJK-aware: Latin text ~4 chars/token, CJK ~1 char/token. The `trimToFit` function preserves tool call/result pairs when dropping old messages.

### Hook (`internal/hooks/hooks.go`)

Lifecycle event bus with blocking semantics for safety.

```go
type Hook interface {
    Name() string
    Handle(ctx context.Context, event Event) error
}
```

Events: `user_message`, `before_llm_call`, `after_llm_call`, `before_tool_exec`, `after_tool_exec`, `error`.

**Blocking behavior**: `before_*` hook errors halt the action and propagate the error (e.g., SafetyHook blocks dangerous shell commands). `after_*` hook errors are logged but don't affect the flow.

| Hook | Purpose |
|---|---|
| `LoggingHook` | Structured logging of all lifecycle events |
| `SafetyHook` | Blocks dangerous shell commands, SSRF to private IPs, sensitive file writes |
| `MetricsHook` | Tracks LLM calls/errors, token usage, per-tool call counts, latency ring buffer |

### Middleware (`internal/middleware/middleware.go`)

Wraps tool execution with cross-cutting behavior.

```go
type Middleware func(ctx context.Context, toolName string, args string,
    next func(context.Context, string) (string, error)) (string, error)
```

Middlewares are registered on the `Registry` and compose into a chain (registration order). Each middleware can inspect/modify arguments and results, or short-circuit execution.

| Middleware | Behavior |
|---|---|
| `CacheMiddleware` | SHA-256 keyed cache with TTL; only caches specified read-only tools |
| `Redact` | Masks secrets (API keys, passwords, URLs with credentials) in tool output |

### Executor (`internal/executor/executor.go`)

Runs shell commands with timeout and output capture.

```go
type Executor interface {
    Execute(ctx context.Context, command string, timeout time.Duration) (*Result, error)
    Name() string
}
```

`LocalExecutor` runs via `/bin/sh -c` with combined stdout/stderr capture. `NoopExecutor` is for testing.

### Filesystem (`internal/fs/fs.go`)

Sandboxed file operations with workspace-root enforcement.

```go
type FS interface {
    ReadFile(path string) ([]byte, error)
    WriteFile(path string, data []byte, perm os.FileMode) error
    Stat(path string) (os.FileInfo, error)
    ReadDir(path string) ([]os.DirEntry, error)
    MkdirAll(path string, perm os.FileMode) error
    Abs(path string) (string, error)
}
```

`LocalFS` resolves symlinks and rejects paths that escape the workspace root, returning `SandboxError`.

### Tape Store (`internal/tape/store.go`)

Append-only JSONL conversation log with in-memory cache.

```go
type Store interface {
    Append(entry TapeEntry) error
    Entries() ([]TapeEntry, error)
    Search(query string) ([]TapeEntry, error)
    EntriesSince(anchorID string) ([]TapeEntry, error)
    LastAnchor() (*TapeEntry, error)
    AddAnchor(label string) error
    Info() TapeInfo
    Close() error
}
```

`TapeEntry` has three kinds: `message` (user/assistant/tool), `anchor` (context boundary), `event` (system events). `FileStore` keeps the file handle open for the session lifetime and maintains an in-memory slice for fast reads.

---

## Agent Loop (`internal/agent/agent.go`)

The `Session` is the core orchestrator implementing the ReAct cycle.

### Data Flow

```
User Input
    │
    ▼
Router.RouteUser() ── :command? ──→ handleCommand() → response
    │                                  ├─ Internal (:help, :usage, :metrics, ...)
    │                                  └─ Shell (:git status, :ls, ...)
    ▼
runReActLoop() [max MaxToolIter iterations]
    │
    ├─ Build context: tape.Entries() → ContextStrategy.BuildContext()
    ├─ LLM call: Chat() or ChatStream()
    │   ├─ Hook: before_llm_call
    │   ├─ LLM provider call (with tool schemas + ResponseFormat)
    │   ├─ Hook: after_llm_call (usage stats)
    │   └─ Accumulate turn/session token usage
    │
    ├─ Tool calls present?
    │   ├─ processToolCalls() [parallel via errgroup]
    │   │   ├─ Auto-expand tool schemas (progressive disclosure)
    │   │   ├─ For each tool call (concurrently):
    │   │   │   ├─ Hook: before_tool_exec (SafetyHook can block)
    │   │   │   ├─ Registry.Execute() → middleware chain → Tool.Execute()
    │   │   │   └─ Hook: after_tool_exec
    │   │   ├─ Append results to tape in original order
    │   │   └─ Self-correction: if tool fails ≥3 times, inject reflection prompt
    │   └─ Continue loop
    │
    ├─ Empty response? → Retry (skip, don't return blank to user)
    │
    ├─ Looks like a plan? → Auto-nudge (up to 2×/turn)
    │   ├─ Append assistant plan + nudge message to tape
    │   └─ Continue loop
    │
    └─ Final answer
        ├─ RouteAssistant() → extract embedded :commands from response
        ├─ Execute any detected commands
        ├─ Append to tape
        └─ Return content
```

### Key Behaviors

**Parallel Tool Execution**: When the LLM returns multiple tool calls, they execute concurrently via `errgroup`. Results are collected in a slice indexed by position, then appended to the tape in the original order for deterministic replay.

**Auto-Nudge**: `looksLikePlan()` detects when the LLM describes what it *will* do ("I'll read the file...") instead of actually calling tools. The loop injects a nudge message ("Don't just describe what you'll do — use the available tools now") and re-enters the loop. Limited to 2 nudges per turn.

**Empty Response Retry**: If the LLM returns an empty response with no tool calls, the loop retries instead of showing a blank answer.

**Self-Correction**: Per-turn failure tracking counts errors per tool name. When a tool fails ≥3 times (`maxToolFailures`), a reflection prompt is injected: "Tool X has failed N times this turn. Reconsider your approach."

**Token Tracking**: Session-level and turn-level `Usage` accumulators track prompt/completion tokens. Available via `:usage` command. Streaming responses capture usage from the `StreamDone` event.

**Structured Output**: `ResponseFormat` field on `ChatRequest` supports `json_object` mode. For Anthropic (which lacks native JSON mode), a JSON-only instruction is injected into the system prompt.

### Streaming

```
HandleInputStream(msg, tokenCh)
    │
    ▼
executeLLMCall(stream=true, tokenCh)
    │
    ▼
doStreamingCall() reads <-chan StreamEvent:
    ├─ ContentDelta → write token to tokenCh
    ├─ ToolCallDelta → accumulate partial tool call
    └─ StreamDone → finalize response, capture Usage
    │
    ▼
Channel.SendStream() consumes tokenCh:
    ├─ CLI: Print tokens as they arrive
    └─ Lark: Patch interactive card every 800ms with accumulated text + ▍ cursor
```

---

## Session Management (`internal/agent/session.go`)

Remote channels (Lark, Telegram) need per-chat isolation so concurrent conversations don't interfere.

```
SessionManager
    ├─ sessions: map[chatID] → *Session
    │
    ├─ GetOrCreate(chatID) → *Session
    │     ├─ Return existing session (update lastAccess)
    │     ├─ Evict oldest if at MaxSessions cap
    │     └─ Create new: fresh tape, tool registry, context
    │
    ├─ LoadExisting() → restore sessions from tape files on disk
    │     └─ Pattern: session-<agent>-<chatID>-<timestamp>.jsonl
    │
    ├─ StartEvictionLoop(interval, maxIdle)
    │     └─ Every 10min: cancel + remove sessions idle > 24h
    │
    └─ Shutdown() → cancel all session contexts
```

Each `Session` is self-contained with:
- Tape file (conversation history, tracked by `TapePath`)
- Tool registry (independent progressive disclosure state)
- Token tracking (turn/session usage, tool failure counts)
- Lifecycle fields (`ctx`, `cancel`, `lastAccess`) managed by `SessionManager`
- For the default CLI session, lifecycle fields are unused — callers pass their own context

---

## Wiring Layer (`internal/app/app.go`)

The `app` package separates component assembly from `main` for testability.

### AgentInstance

Bundles all runtime components: a default `Session` (CLI), a `SessionManager` (remote channels), channels, config, logger. The `Run()` method starts channels, processes messages, and blocks.

### Message Dispatch

```
processMessages()
    │
    ├─ CLI message → processMessage() inline (single-threaded)
    │
    └─ Remote message → per-chatID worker queue
        ├─ First message creates the queue + goroutine
        ├─ Messages within a chat are serialized
        └─ Different chats run in parallel
```

### BuildAgent Flow

1. Load two-tier config (global + agent)
2. Create LLM client (provider detection, rate limiting)
3. Create tape store (session JSONL file)
4. Create executor and sandboxed filesystem
5. Create context strategy
6. Create hook bus (logging, safety, metrics)
7. Build tool registry: file tools, shell, web, Lark, memory, skills
8. Register middleware: cache → redact
9. Create default `Session` (for CLI)
10. Create `SessionManager` (for remote channels)
11. Return `AgentInstance`

### Background Agent Discovery

```
main() creates the "default" CLI agent, then:
    ├─ config.DiscoverAgents() → list ~/.golem/agents/ subdirs
    ├─ For each agent with remote channels (Lark/Telegram):
    │   ├─ BuildAgent(agentName) with agent-specific config
    │   └─ Run in background goroutine
    └─ Dedup: claimedLarkApps map prevents duplicate WebSocket connections
```

---

## Configuration (`internal/config/config.go`)

### Two-Tier Loading

| Tier | File | Scope |
|---|---|---|
| Global | `~/.golem/config.env` | LLM keys, model, skills dir, rate limits, web search backend |
| Agent | `~/.golem/agents/<name>/config.env` | Behavior (max iterations, timeout, context strategy), channels (Lark, Telegram), storage, logging |

Precedence: **CLI flags** > **shell environment** > **config.env** > **defaults**.

### Key Variables

| Variable | Default | Description |
|---|---|---|
| `GOLEM_MODEL` | — | Provider:model (e.g., `openai:gpt-4o`, `anthropic:claude-sonnet-4-20250514`) |
| `<PROVIDER>_API_KEY` | — | API key for the provider |
| `<PROVIDER>_BASE_URL` | — | Custom endpoint (OpenAI-compatible) |
| `GOLEM_MAX_TOOL_ITER` | 15 | Max tool calls per user message |
| `GOLEM_MAX_OUTPUT_TOKENS` | 4096 | Max tokens per LLM response |
| `GOLEM_SHELL_TIMEOUT` | 30s | Shell command timeout |
| `GOLEM_CONTEXT_STRATEGY` | masking | `anchor` or `masking` |
| `GOLEM_EXECUTOR` | local | `local` or `noop` |
| `GOLEM_TAPE_DIR` | ~/.golem/tapes | Conversation log directory |
| `GOLEM_SKILLS_DIR` | .agent/skills | Skill discovery directory |
| `GOLEM_LOG_LEVEL` | info | Log level |
| `GOLEM_MAX_SESSIONS` | 100 | Max concurrent per-chat sessions |
| `GOLEM_SESSION_IDLE_TIME` | 24h | Evict sessions idle longer than this |
| `GOLEM_LLM_RATE_LIMIT` | — | Requests per second (0 = unlimited) |
| `GOLEM_WEB_SEARCH_BACKEND` | bing | `bing` or `stub` |
| `LARK_APP_ID` | — | Lark bot app ID |
| `LARK_APP_SECRET` | — | Lark bot app secret |
| `LARK_VERIFY_TOKEN` | — | Lark event verification token |
| `TELEGRAM_TOKEN` | — | Telegram bot token |
| `MNEMO_DB_HOST` | — | TiDB host for mnemos memory |

---

## Tool Registry (`internal/tools/registry.go`)

### Progressive Disclosure

Tools start in **compact mode**: short description, empty parameter schema (`{"type":"object","properties":{}}`). This saves tokens by not sending full schemas for tools the LLM may never use.

Expansion triggers:
1. **LLM calls the tool** → auto-expanded for the next iteration
2. **LLM mentions the tool** → `ExpandHints()` detects word-boundary matches in LLM text (e.g., "I'll use read_file" or "I'll read file") and expands proactively

After expansion, the full description and complete JSON Schema are sent in subsequent LLM calls.

### Middleware Chain

```
Registry.Execute(toolName, args)
    │
    ▼
CacheMiddleware: Check SHA-256(toolName+args) cache
    │  ├─ Hit (not expired) → return cached result
    │  └─ Miss → continue
    ▼
Redact Middleware: (pass-through on the way in)
    │
    ▼
Tool.Execute(ctx, args)
    │
    ▼
Redact Middleware: Mask secrets in result
    │
    ▼
CacheMiddleware: Store result with TTL
    │
    ▼
Return result
```

### Built-in Tools

| Tool | Purpose |
|---|---|
| `read_file` | Read file contents (offset/limit for large files) |
| `write_file` | Create or overwrite files |
| `edit_file` | Edit specific line ranges |
| `list_directory` | Recursive listing, respects `.gitignore` |
| `search_files` | Full-text regex search across workspace |
| `shell_exec` | Shell commands with configurable timeout |
| `web_search` | Bing web search (configurable backend) |
| `web_fetch` | Fetch and parse HTML from URLs |
| `lark_send` | Send message to Lark group chat |
| `lark_list_chats` | List bot's Lark group chats |
| `lark_read_doc` | Read Lark document content |
| `lark_write_doc` | Write/update Lark documents |
| `memory_store` | Save to persistent vector memory (mnemos) |
| `memory_recall` | Vector-similarity memory search |
| `spawn_agent` | Delegate task to independent sub-agent |

### Sub-Agent (`spawn_agent`)

Delegates a task to a fresh `Session` with its own context. The sub-agent has standard tools but **no spawn capability** (prevents recursive spawning). The parent can pass context via the `context` parameter, which is prepended to the prompt.

### Skills

Discovered from `.agent/skills/*/SKILL.md` files. Each skill has YAML frontmatter (name, description) and a markdown body that's injected as a prompt when called. Skills participate in progressive disclosure like regular tools.

---

## Tape System (`internal/tape/`)

### Structure

Each tape is a JSONL file with immutable, append-only entries:

```jsonl
{"id":"...","kind":"message","payload":{"role":"user","content":"Hello"},"timestamp":"..."}
{"id":"...","kind":"message","payload":{"role":"assistant","content":"Hi!","tool_calls":[...]},"timestamp":"..."}
{"id":"...","kind":"message","payload":{"role":"tool","content":"...","name":"read_file","tool_call_id":"tc1"},"timestamp":"..."}
{"id":"...","kind":"anchor","payload":{"label":"manual"},"timestamp":"..."}
```

### Entry Kinds

| Kind | Purpose |
|---|---|
| `message` | User, assistant, or tool messages (the conversation) |
| `anchor` | Context boundary — `:reset` creates one; strategies use it as a cutoff |
| `event` | System events (session start, config change) |

### Context Assembly

`BuildMessages(entries)`:
1. Find the last anchor
2. Include only `message` entries after the anchor
3. For user messages with `sender_id`, prepend `[sender:ID]` for group chat disambiguation
4. Return `[]llm.Message` ready for the LLM

### Session Tape Discovery

`Discover(tapeDir)` finds tape files matching `session-<agent>-<chatID>-<timestamp>.jsonl` and groups them by chat ID. `SessionManager.LoadExisting()` restores previous sessions from these files on startup.

---

## Safety (`internal/hooks/safety.go`)

The `SafetyHook` fires on `before_tool_exec` events and blocks dangerous operations:

**Shell Command Safety** — 14 regex patterns matching:
- Destructive operations: `rm -rf /`, `mkfs`, `dd of=/dev/`, fork bombs
- Remote code execution: `curl | sh`, `wget | sh`, `eval $(curl ...)`
- Privilege escalation: `chmod 777 /`, `chown root /`
- Credential theft: `cat /etc/shadow`, `cat .ssh/`
- System control: `shutdown`, `reboot`, `halt`

**SSRF Protection** — Blocks `web_fetch` requests to:
- Private/reserved CIDRs: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `127.0.0.0/8`, `169.254.0.0/16`, `0.0.0.0/8`
- IPv6 private ranges: `::1/128`, `fc00::/7`, `fe80::/10`
- Cloud metadata endpoints: `metadata.google.internal`

**Sensitive File Write Protection** — Blocks writes to 10 patterns:
`.env`, `.git/config`, SSH keys, `.ssh/`, `.aws/`, `.kube/`, `.gnupg/`, `credentials.json`, `authorized_keys`

---

## Memory (`internal/memory/client.go`)

Persistent vector memory via mnemos direct-mode client, backed by TiDB Cloud Serverless. Supports:
- **Store**: Save memories with content, key, source, tags
- **Recall**: Hybrid search combining vector similarity (cosine distance on auto-embedded content) and full-text search via Reciprocal Rank Fusion (RRF)
- **Auto-embedding**: Uses TiDB's built-in embedding models (e.g., `tidbcloud_free/amazon/titan-embed-text-v2`)

Exposed as `memory_store` and `memory_recall` tools.

---

## User Commands

Prefixed with `:` in the CLI. Also detected in assistant output (lines starting with `:`, skipping code fences).

| Command | Description |
|---|---|
| `:help` | Show available commands |
| `:quit` | Exit golem |
| `:usage` | Show token usage (session + turn) |
| `:metrics` | Show operational metrics (calls, latency, per-tool stats) |
| `:tape.info` | Tape statistics (entries, anchors, file path) |
| `:tape.search <q>` | Search conversation history |
| `:tools` | List registered tools |
| `:skills` | List discovered skills |
| `:model [name]` | Show or change model |
| `:reset` | Add anchor (context boundary) |
| `:<cmd>` | Execute shell command (fallback for unrecognized commands) |

---

## Initialization Flow (`cmd/golem/main.go`)

1. Parse CLI flags (`--model`, `--log-level`, `--work-dir`)
2. Load global config (`~/.golem/config.env`)
3. Create logger (JSONL to tape directory)
4. `app.BuildAgent("default")` — create the primary agent instance:
   - LLM client with rate limiting
   - Tape store, executor, filesystem, context strategy
   - Hook bus (logging + safety + metrics)
   - Tool registry with middleware chain
   - CLI channel + optional Lark/Telegram channels
   - SessionManager for remote channels
5. Discover and build background agents (`~/.golem/agents/*/`)
6. Start all agents concurrently via errgroup
7. Signal handling: SIGINT/SIGTERM trigger graceful shutdown

---

## Limits

| Resource | Limit |
|---|---|
| Shell output | 50 KB |
| File read | 50,000 chars |
| Directory listing | 200 entries |
| File search results | 50 matches |
| Web search results | 5 default, 20 max |
| Tool iterations per message | 15 (configurable) |
| Max output tokens | 4,096 (configurable) |
| Token estimation | ~4 chars/token (Latin), ~1 char/token (CJK) |
| Max concurrent sessions | 100 (configurable) |
| Session idle timeout | 24h (configurable) |
| LLM latency ring buffer | 100 entries |
| Tool cache TTL | 60s |

---

## Dependencies

| Module | Purpose |
|---|---|
| `github.com/larksuite/oapi-sdk-go/v3` | Lark/Feishu SDK |
| `go.uber.org/zap` | Structured logging |
| `github.com/google/uuid` | UUID generation |
| `github.com/joho/godotenv` | `.env` file loading |
| `golang.org/x/net` | HTML parsing |
| `golang.org/x/sync` | errgroup for parallel execution |
| `golang.org/x/time` | Rate limiter for LLM calls |

---

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

Integration tests use `//go:build integration` tag and a mock OpenAI server:

```sh
go test -tags integration ./internal/agent/
```

Covers: simple Q&A, single/multi-step tool calls, tool call limit, colon command bypass, streaming, tape recording, shell execution, empty response retry, nudge behavior, self-correction on repeated tool failure, and parallel tool calls.
