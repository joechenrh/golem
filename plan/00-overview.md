# golem — Go-Based Agentic AI Framework

## Context

A Go agentic AI framework inspired by [bubbuild/bub](https://github.com/bubbuild/bub) (Python) and [jackwener/crabclaw](https://github.com/jackwener/crabclaw) (Rust). Follows crabclaw's architecture — ReAct loop agent with append-only tape, progressive tool loading, multi-channel support. Adds **Lark** and **Telegram** channels, integrates **[mnemos](https://github.com/qiffang/mnemos)** for persistent memory.

- **Location**: `/Users/joechenrh/code/golem`
- **Module**: `github.com/joechenrh/golem`
- **LLM providers**: OpenAI + Anthropic
- **Scope**: Phase 1-2 complete (working agent with CLI, tool calling, router, file ops)

## Project Structure

```
golem/
├── cmd/golem/main.go
├── internal/
│   ├── agent/agent.go                           # ReAct loop orchestrator
│   ├── channel/
│   │   ├── channel.go                           # Channel interface
│   │   ├── cli/cli.go                           # CLI/REPL (streaming)
│   │   ├── telegram/telegram.go                 (stub)
│   │   └── lark/lark.go                         (stub)
│   ├── config/config.go                         # Hierarchical config
│   ├── context/
│   │   └── strategy.go                          # ContextStrategy interface
│   ├── executor/                                 # RENAMED from shell/
│   │   ├── executor.go                          # Executor interface
│   │   ├── local.go                             # LocalExecutor (/bin/sh -c)
│   │   └── noop.go                              # NoopExecutor
│   ├── fs/                                       # NEW
│   │   ├── fs.go                                # FS interface
│   │   └── local.go                             # LocalFS (os + sandbox)
│   ├── hooks/                                    # NEW
│   │   ├── hooks.go                             # Hook interface + Bus
│   │   └── logging.go                           # LoggingHook
│   ├── llm/
│   │   ├── client.go                            # Client interface + factory
│   │   ├── openai.go
│   │   ├── anthropic.go
│   │   ├── types.go
│   │   └── stream.go
│   ├── memory/
│   │   ├── memory.go                            # Memory interface
│   │   ├── mnemos.go                            (stub)
│   │   └── hook.go                              # MemoryHook (implements hooks.Hook)
│   ├── router/router.go                         # Comma command routing
│   ├── tape/
│   │   ├── store.go                             # Store interface + FileStore
│   │   └── entry.go                             # TapeEntry types
│   └── tools/
│       ├── tool.go                              # Tool interface
│       ├── registry.go                          # Registry + discovery
│       ├── skill.go                             # SKILL.md parser
│       ├── progressive.go                       # Progressive disclosure
│       └── builtin/
│           ├── file_ops.go                      # uses fs.FS
│           ├── shell_tool.go                    # uses executor.Executor
│           ├── web.go                           (stub)
│           ├── memory_tools.go                  (stub)
│           └── schedule.go                      (stub)
├── go.mod
├── .env.example
└── Makefile
```

## All Interfaces (9 total)

| Interface | Package | Implementations | Purpose |
|---|---|---|---|
| `Channel` | `channel` | CLI, Telegram*, Lark* | Message I/O adapter |
| `Client` | `llm` | OpenAI, Anthropic | LLM API abstraction |
| `Tool` | `tools` | Builtin tools, Skills | Agent capability |
| `Memory` | `memory` | mnemos REST*, direct* | Cross-session persistence |
| `Store` | `tape` | FileStore | Conversation history |
| `ContextStrategy` | `context` | Anchor, Masking, Hybrid* | Context window management |
| `Executor` | `executor` | Local, Noop, Docker* | Command execution environment |
| `Hook` | `hooks` | Logging, Memory*, Safety* | Lifecycle event handling |
| `FS` | `fs` | LocalFS, MemFS* | Filesystem operations |

*= stub or future implementation

## Dependency Graph

```
cmd/golem/main.go
  ├── internal/config
  ├── internal/hooks           ← event bus
  ├── internal/agent
  │   ├── internal/llm
  │   ├── internal/tools
  │   │   └── internal/tools/builtin
  │   │       ├── internal/executor
  │   │       └── internal/fs
  │   ├── internal/tape
  │   ├── internal/context
  │   ├── internal/hooks
  │   └── internal/router
  ├── internal/channel/cli
  ├── internal/channel/telegram
  └── internal/channel/lark
```

## Implementation Steps

| Step | File | Description |
|------|------|-------------|
| [01](01-project-init.md) | go.mod, Makefile, .env.example | Project init, dependencies, build system |
| [02](02-config.md) | internal/config/config.go | Hierarchical configuration |
| [03](03-llm-types.md) | internal/llm/types.go | Shared LLM types (Message, ToolCall, etc.) |
| [04](04-llm-clients.md) | internal/llm/*.go | OpenAI + Anthropic clients with streaming |
| [05](05-tape.md) | internal/tape/*.go | Append-only JSONL tape store |
| [05a](05a-context-management.md) | internal/context/strategy.go | Pluggable ContextStrategy interface |
| [06](06-tools.md) | internal/tools/*.go | Tool interface, registry, skills, progressive |
| [07](07-router.md) | internal/router/router.go | Input routing and comma command parsing |
| [08](08-shell.md) | internal/executor/*.go | Executor interface + LocalExecutor |
| [09](09-builtin-tools.md) | internal/tools/builtin/*.go | Shell tool (uses Executor) + file ops (uses FS) |
| [10](10-agent.md) | internal/agent/agent.go | Core ReAct loop (uses hooks.Bus) |
| [11](11-channels.md) | internal/channel/*.go | Channel interface + CLI + stubs |
| [12](12-main.md) | cmd/golem/main.go | Entry point, wiring, signal handling |
| [13](13-abstraction-review.md) | — | Abstraction review: Executor, Hooks, FS rationale |

## What NOT to Abstract (Rationale)

| Component | Why Keep Concrete |
|---|---|
| Router | Small, stable set of 8 commands. A command registry adds complexity with no benefit. |
| Token counter | Single heuristic function. An interface for tokenizers is premature. |
| Message renderer | Already handled by `Channel.Send()`. A `Renderer` interface duplicates Channel. |
| System prompt builder | Simple concatenation. Interface is over-engineering without fundamentally different prompt structures. |
| Config loader | Single implementation. No need to abstract env var reading. |

## Verification

1. `make build` succeeds
2. `./bin/golem` starts REPL → ask "What is 2+2?" → LLM response + tape grows
3. `./bin/golem --model anthropic:claude-sonnet-4-20250514` → Anthropic with streaming
4. Ask "read the go.mod file" → agent calls `read_file` tool
5. `,help` → internal commands listed
6. `,tape.info` → tape statistics
7. `,tools` → registered tools listed
8. `,quit` → clean exit
