# Step 8: Executor (renamed from Shell)

## Scope

Pluggable command execution abstraction. Default implementation runs commands locally via `/bin/sh -c`. Maps to crabclaw's `core/shell.rs` but abstracted as an interface.

## Files

- `internal/executor/executor.go` — Executor interface + Result type
- `internal/executor/local.go` — LocalExecutor implementation
- `internal/executor/noop.go` — NoopExecutor (disabled mode)

## Key Points

### Interface (`executor.go`)

```go
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
    Execute(ctx context.Context, command string, timeout time.Duration) (*Result, error)
    Name() string  // "local", "docker", "noop"
}

// FormatResult returns a human-readable string for LLM consumption.
func FormatResult(result *Result) string
```

### LocalExecutor (`local.go`)

```go
type LocalExecutor struct {
    WorkDir string  // working directory for commands
}

func NewLocal(workDir string) *LocalExecutor
```

Implementation:
1. `exec.CommandContext(ctx, "/bin/sh", "-c", command)`
2. Set `cmd.Dir` to `WorkDir`
3. `context.WithTimeout` for deadline enforcement
4. Capture stdout/stderr separately via `bytes.Buffer`
5. On timeout: process killed via context cancellation
6. Output truncated to 50KB max

### NoopExecutor (`noop.go`)

```go
type NoopExecutor struct{}

func (n *NoopExecutor) Execute(ctx context.Context, cmd string, timeout time.Duration) (*Result, error) {
    return &Result{
        Stdout:   "Command execution is disabled in this mode.",
        ExitCode: 1,
        Command:  cmd,
    }, nil
}
```

Used for read-only deployments or testing.

### Configuration

```bash
GOLEM_EXECUTOR=local   # "local" or "noop"
```

### Why Abstract

- **Production bots** (Telegram/Lark) may need sandboxed execution (Docker) for safety
- **Testing** needs a mock executor
- **Future**: `DockerExecutor`, `SSHExecutor` can be added without changing tool code

## Done When

- `NewLocal(dir).Execute(ctx, "echo hello", 10s)` → `{Stdout: "hello\n", ExitCode: 0}`
- `NewLocal(dir).Execute(ctx, "sleep 100", 1s)` → `{TimedOut: true}`
- `NoopExecutor.Execute(...)` → returns disabled message
- `FormatResult()` produces readable output for LLM
