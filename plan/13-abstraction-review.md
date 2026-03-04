# Step 13: Abstraction Review — Additional Interfaces

## Summary

Three components should be abstracted beyond what's already planned:

1. **Executor** — shell command execution (local → Docker → SSH → noop)
2. **Hooks / Event Bus** — lifecycle events for cross-cutting concerns
3. **Filesystem** — file operations with pluggable sandboxing

---

## 1. Executor Interface

### Problem

`internal/shell/shell.go` hardcodes `/bin/sh -c` execution. This is fine for a local CLI tool, but when the agent runs as a Telegram/Lark bot in production:
- Users might send malicious commands
- The agent needs sandboxed execution (Docker, nsjail, etc.)
- Remote execution (SSH to dev server) becomes a real use case
- Testing needs a mock executor

### Interface

```go
// internal/executor/executor.go
package executor

type Result struct {
    Stdout   string
    Stderr   string
    ExitCode int
    TimedOut bool
    Command  string
}

// Executor runs commands in some environment.
type Executor interface {
    // Execute runs a command and returns the result.
    Execute(ctx context.Context, command string, timeout time.Duration) (*Result, error)

    // Name returns the executor type for logging (e.g., "local", "docker", "ssh").
    Name() string
}
```

### Built-in Implementations

| Implementation | Description | Use Case |
|---|---|---|
| `LocalExecutor` | `/bin/sh -c` on host | CLI development (current behavior) |
| `DockerExecutor` | `docker exec` in container | Production bots, sandboxed execution |
| `NoopExecutor` | Returns canned "execution disabled" | Read-only mode, testing |

Future (not in Phase 1-2):
- `SSHExecutor` — execute on remote host
- `NsjailExecutor` — lightweight Linux sandboxing

### Configuration

```bash
GOLEM_EXECUTOR=local          # "local", "docker", "noop"
GOLEM_DOCKER_IMAGE=ubuntu:24.04  # for docker executor
```

### Impact on Existing Plan

- **Step 8 (shell.go)**: Rename to `internal/executor/`. The `shell.go` file becomes `local.go` implementing `Executor`.
- **Step 9 (builtin/shell_tool.go)**: Accepts `Executor` interface instead of calling `shell.Execute()` directly.
- **Step 10 (agent.go)**: `AgentLoop` holds an `Executor`, passes it to shell tool.
- **Step 12 (main.go)**: Factory creates executor from config.

---

## 2. Hooks / Event Bus

### Problem

The agent loop currently needs to explicitly call:
- Memory hooks (`OnSessionStart`, `OnPromptSubmit`, `OnSessionEnd`)
- Logging at specific points
- Future: safety checks, analytics, rate limiting

This creates tight coupling. Every new cross-cutting concern requires modifying the agent loop.

### Interface

```go
// internal/hooks/hooks.go
package hooks

// Event represents a lifecycle event in the agent loop.
type Event struct {
    Type    EventType
    Payload map[string]interface{}
}

type EventType string
const (
    EventSessionStart   EventType = "session_start"
    EventSessionEnd     EventType = "session_end"
    EventBeforeLLMCall  EventType = "before_llm_call"
    EventAfterLLMCall   EventType = "after_llm_call"
    EventBeforeToolExec EventType = "before_tool_exec"
    EventAfterToolExec  EventType = "after_tool_exec"
    EventContextCompact EventType = "context_compacted"
    EventUserMessage    EventType = "user_message"
    EventAssistantReply EventType = "assistant_reply"
)

// Hook is called when a matching event fires.
// Hooks can modify the event payload (e.g., inject memory context)
// or return an error to abort the operation.
type Hook interface {
    // Name returns the hook name for logging.
    Name() string

    // Events returns which event types this hook cares about.
    Events() []EventType

    // Handle processes an event. Returns modified payload or error.
    Handle(ctx context.Context, event Event) (*Event, error)
}

// Bus manages hook registration and event dispatching.
type Bus struct {
    hooks map[EventType][]Hook
}

func NewBus() *Bus
func (b *Bus) Register(h Hook)
func (b *Bus) Emit(ctx context.Context, event Event) (*Event, error)
```

### Built-in Hooks

| Hook | Events | Behavior |
|---|---|---|
| `MemoryHook` | `session_start`, `user_message`, `session_end` | Load/inject/save memories via mnemos |
| `LoggingHook` | all | Log event types and key payload fields |
| `SafetyHook` | `before_tool_exec` | Block dangerous commands (optional) |

### How the Agent Loop Uses It

```go
// Before:
memories, _ := a.memoryHooks.OnSessionStart(ctx, spaceID)

// After:
event, _ := a.hooks.Emit(ctx, hooks.Event{
    Type: hooks.EventSessionStart,
    Payload: map[string]interface{}{"space_id": spaceID},
})
// Memory hook auto-injects relevant memories into event.Payload["context"]
```

The agent loop only knows about `hooks.Bus` — it doesn't know about memory, logging, or safety directly.

### Configuration

Hooks are registered in `main.go` during wiring:

```go
bus := hooks.NewBus()
bus.Register(hooks.NewLoggingHook(logger))
if cfg.MnemosURL != "" {
    bus.Register(hooks.NewMemoryHook(memClient))
}
if cfg.SafetyMode {
    bus.Register(hooks.NewSafetyHook(cfg))
}
```

### Impact on Existing Plan

- **Step 10 (agent.go)**: `AgentLoop` holds a `hooks.Bus`. Emits events at key lifecycle points. No direct memory calls.
- **Step 12 (main.go)**: Creates and configures `hooks.Bus`, registers hooks.
- **memory/hooks.go**: Becomes a `Hook` implementation instead of standalone struct.

---

## 3. Filesystem Interface

### Problem

`internal/tools/builtin/file_ops.go` calls `os.ReadFile()`, `os.WriteFile()`, etc. directly, with a `resolveSafePath()` wrapper for sandboxing. Issues:
- Sandbox logic is mixed with tool logic
- Can't test file tools without touching real filesystem
- Docker executor would need a different filesystem view (container FS)
- Future: remote file operations (SSH, cloud storage)

### Interface

```go
// internal/fs/fs.go
package fs

// FS provides filesystem operations within a boundary.
type FS interface {
    // ReadFile reads a file's content. Path is relative to the workspace.
    ReadFile(path string) ([]byte, error)

    // WriteFile writes content to a file. Creates parent dirs as needed.
    WriteFile(path string, content []byte) error

    // ListDir lists directory contents.
    ListDir(path string) ([]DirEntry, error)

    // Search searches file contents for a pattern.
    Search(path, pattern string, opts SearchOpts) ([]SearchResult, error)

    // Stat returns file info.
    Stat(path string) (FileInfo, error)

    // WorkspaceRoot returns the absolute workspace root path.
    WorkspaceRoot() string
}

type DirEntry struct {
    Name  string
    IsDir bool
    Size  int64
}

type SearchResult struct {
    Path    string
    Line    int
    Content string
}

type SearchOpts struct {
    FileGlob      string
    MaxResults    int
    CaseSensitive bool
}
```

### Built-in Implementations

| Implementation | Description | Use Case |
|---|---|---|
| `LocalFS` | `os` package + workspace sandbox enforcement | Default — current behavior |
| `MemFS` | In-memory filesystem | Testing |

Future (not in Phase 1-2):
- `DockerFS` — reads/writes via `docker cp` or bind mounts
- `RemoteFS` — SSH-based file operations

### Impact on Existing Plan

- **Step 9 (builtin/file_ops.go)**: All file tools receive `FS` interface instead of calling `os` directly. No `resolveSafePath()` — the `LocalFS` implementation handles sandboxing internally.
- **Step 10 (agent.go)**: `AgentLoop` or tool registry holds the `FS` instance.
- **Step 12 (main.go)**: Creates `fs.NewLocalFS(workDir)` and passes it to tools.

---

## Updated Dependency Graph

```
cmd/golem/main.go
  ├── internal/config
  ├── internal/hooks         ← NEW: event bus
  ├── internal/agent
  │   ├── internal/llm
  │   ├── internal/tools
  │   │   ├── internal/tools/builtin
  │   │   │   ├── internal/memory
  │   │   │   ├── internal/executor  ← RENAMED from shell
  │   │   │   └── internal/fs       ← NEW: filesystem
  │   │   └── internal/executor
  │   ├── internal/tape
  │   ├── internal/context
  │   ├── internal/hooks
  │   └── internal/router
  ├── internal/channel/cli
  ├── internal/channel/telegram
  └── internal/channel/lark
```

---

## What NOT to Abstract (Rationale)

| Component | Why Keep Concrete |
|---|---|
| **Router** | Small, stable set of 8 commands. A command registry adds complexity with no real benefit. |
| **Token counter** | Single function, heuristic is fine. An interface for different tokenizers is premature. |
| **Message renderer** | Already handled by `Channel.Send()`. Adding a `Renderer` interface duplicates the Channel abstraction. |
| **System prompt builder** | Simple string concatenation. An interface would be over-engineering unless we need fundamentally different prompt structures (not just content). |
| **Config loader** | Single implementation is fine. No need to abstract environment variable reading. |

---

## Updated Project Structure (with new abstractions)

```
golem/
├── cmd/golem/main.go
├── internal/
│   ├── agent/agent.go
│   ├── channel/
│   │   ├── channel.go              # Channel interface
│   │   ├── cli/cli.go
│   │   ├── telegram/telegram.go    (stub)
│   │   └── lark/lark.go            (stub)
│   ├── config/config.go
│   ├── context/
│   │   └── strategy.go             # ContextStrategy interface
│   ├── executor/                    # RENAMED from shell/
│   │   ├── executor.go             # Executor interface
│   │   ├── local.go                # LocalExecutor (/bin/sh -c)
│   │   └── noop.go                 # NoopExecutor (disabled)
│   ├── fs/                          # NEW
│   │   ├── fs.go                   # FS interface
│   │   └── local.go                # LocalFS (os + sandbox)
│   ├── hooks/                       # NEW
│   │   ├── hooks.go                # Hook interface + Bus
│   │   ├── logging.go              # LoggingHook
│   │   └── safety.go               # SafetyHook (stub)
│   ├── llm/
│   │   ├── client.go               # Client interface
│   │   ├── openai.go
│   │   ├── anthropic.go
│   │   ├── types.go
│   │   └── stream.go
│   ├── memory/
│   │   ├── memory.go               # Memory interface
│   │   ├── mnemos.go               (stub)
│   │   └── hook.go                 # MemoryHook (implements hooks.Hook)
│   ├── router/router.go
│   ├── tape/
│   │   ├── store.go                # Store interface
│   │   └── entry.go
│   └── tools/
│       ├── tool.go                 # Tool interface
│       ├── registry.go
│       ├── skill.go
│       ├── progressive.go
│       └── builtin/
│           ├── file_ops.go         # uses fs.FS
│           ├── shell_tool.go       # uses executor.Executor
│           ├── web.go              (stub)
│           ├── memory_tools.go     (stub)
│           └── schedule.go         (stub)
├── go.mod
├── .env.example
└── Makefile
```

## Complete Interface Summary

| Interface | Package | Implementations | Purpose |
|---|---|---|---|
| `Channel` | `channel` | CLI, Telegram*, Lark* | Message I/O adapter |
| `Client` | `llm` | OpenAI, Anthropic | LLM API abstraction |
| `Tool` | `tools` | Builtin tools, Skills | Agent capability |
| `Memory` | `memory` | mnemos REST*, mnemos direct* | Cross-session persistence |
| `Store` | `tape` | FileStore | Conversation history |
| `ContextStrategy` | `context` | Anchor, Masking, Hybrid* | Context window management |
| `Executor` | `executor` | Local, Noop, Docker* | Command execution environment |
| `Hook` | `hooks` | Logging, Memory*, Safety* | Lifecycle event handling |
| `FS` | `fs` | LocalFS, MemFS* | Filesystem operations |

*= stub or future implementation
