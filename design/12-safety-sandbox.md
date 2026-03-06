# 12 — Safety, Sandboxing, and Middleware

## 1. Overview

Golem uses a defense-in-depth strategy with four independent layers that limit what an autonomous agent can do: the filesystem sandbox (`internal/fs`) guards against path traversal and symlink escape; the command executor (`internal/executor`) prevents unbounded execution and runaway processes; the safety hook (`internal/hooks`) blocks destructive commands, SSRF, and credential writes; and the middleware chain (`internal/middleware`) handles PII redaction and redundant-call caching. Each layer operates independently — a failure in one does not compromise the others.

## 2. Filesystem Sandbox

**Source: `internal/fs`**

`fs.FS` defines a minimal filesystem abstraction (read, write, stat, readdir, mkdir, abs) consumed by every file-oriented tool. `LocalFS` is the sole production implementation, rooted at a workspace directory resolved to an absolute, symlink-free path at construction time via `filepath.EvalSymlinks`.

Every operation passes through `resolve(path)`, which converts the path to an absolute, cleaned form, then checks it is within the sandbox root. The check requires the path to either equal the root exactly or start with `root + filepath.Separator`, preventing the classic prefix confusion where `/tmp/sandbox` appears to be inside `/tmp/sand`. After the prefix check, `resolve` follows symlinks with `filepath.EvalSymlinks` and re-checks the result, so a symlink inside the sandbox cannot point outside it. For new paths that do not yet exist on disk (e.g., `WriteFile` to a new file), `resolveNewPath` resolves the existing parent directory and verifies the result instead.

Violations return a typed `*SandboxError` containing the offending path and the root, which tools can surface to the LLM.

`BuildAgent` in `app.go` creates the `LocalFS` rooted at `os.Getwd()` and passes it to every file tool (`read_file`, `write_file`, `edit_file`, `list_directory`, `search_files`).

## 3. Command Executor

**Source: `internal/executor`**

The `Executor` interface exposes a single `Execute(ctx, command, timeout)` method returning a `Result` with stdout, stderr, exit code, timeout flag, and the original command string. `FormatResult` renders it into a human-readable block for LLM consumption.

### LocalExecutor

Runs commands via `/bin/sh -c` in a fixed working directory. Every execution gets a `context.WithTimeout`; when the deadline fires, the process is killed and `Result.TimedOut` is set. The timeout comes from `config.ShellTimeout` (default 30 s, configurable via `GOLEM_SHELL_TIMEOUT`). Both stdout and stderr are capped at 50 KB (`maxOutputBytes`) to prevent a runaway `cat` or `find` from consuming unbounded memory. Non-zero exits and signals are captured via `syscall.WaitStatus` rather than surfaced as Go errors, so the agent sees the failure in-band.

### NoopExecutor

Returns a static "Command execution is disabled" message with exit code 1. Selected by setting `GOLEM_EXECUTOR=noop`. Used for read-only deployments or testing where shell access should be completely disabled.

`BuildAgent` selects the executor based on `cfg.Executor` and passes it to `builtin.NewShellTool` along with `cfg.ShellTimeout`.

## 4. Safety Hook

The SafetyHook is covered in detail in `08-hooks.md`. In brief, it gates `before_tool_exec` events and blocks dangerous shell commands (e.g., `rm -rf /`, fork bombs, `curl | sh`), SSRF attempts against private IPs and cloud metadata endpoints, and writes to sensitive file paths like `.env`, SSH keys, and credential stores. If the hook returns an error the tool call is skipped and the error is returned to the LLM as the tool result.

## 5. PII Redaction

**Source: `internal/redact`**

`Redactor` holds an ordered list of regex patterns. `Redact(s)` applies them sequentially, replacing matches with `[REDACTED:<category>]`.

The default pattern set, ordered from most specific to most generic:

| Category | Example match |
|---|---|
| `private_key` | `-----BEGIN RSA PRIVATE KEY-----` |
| `anthropic_key` | `sk-ant-api03-...` |
| `api_key` | `sk-...` (20+ chars) |
| `aws_key` | `AKIA0123456789ABCDEF` |
| `bearer_token` | `Bearer eyJhbGci...` |
| `url_credentials` | `://user:pass@host` |
| `env_secret` | `API_KEY=supersecret` (preserves key name, redacts value only) |

Order matters: the `anthropic_key` pattern fires before the broader `api_key` pattern so Anthropic keys get the more specific label.

## 6. Middleware

**Source: `internal/middleware`**

A middleware wraps tool execution. It receives the tool name, raw JSON args, and a `next` function to call the next layer. It can short-circuit (skip `next`), modify args before calling `next`, or transform the result after.

`Registry.Use(mw)` appends middlewares. `Registry.Execute` builds the chain by iterating in reverse so that the first-registered middleware is the outermost wrapper:

```go
exec := t.Execute
for i := len(r.middlewares) - 1; i >= 0; i-- {
    mw := r.middlewares[i]
    next := exec
    exec = func(ctx context.Context, args string) (string, error) {
        return mw(ctx, name, args, next)
    }
}
return exec(ctx, args)
```

Given registration order `[cache, redact]`, the call path is `cache -> redact -> tool.Execute`. A cache hit returns the already-redacted result without invoking the redact middleware or the tool. A cache miss proceeds to redact, which calls the tool and then masks secrets in the output before the result is stored in the cache.

### CacheMiddleware

Caches results of read-only tools to avoid redundant calls within a conversation turn (e.g., the LLM reading the same file twice). Only tools in an explicit allowlist are cached (currently `read_file`, `list_directory`, `search_files`, `web_search`, `web_fetch`). The cache key is a SHA-256 of `toolName + "\x00" + args`, truncated to 128 bits and hex-encoded. TTL is 60 seconds. `Invalidate()` clears all entries and is intended to be called on mutations, though the current wiring does not hook this up automatically. The cache is protected by a `sync.Mutex` held only for map reads/writes, not during tool execution.

### RedactMiddleware

A thin wrapper that calls `next`, then passes the result through `redact.Redactor.Redact()`. Errors bypass redaction and are returned as-is. This ensures secrets in tool output never reach the conversation tape or the LLM.

## 7. Wiring

**Source: `internal/app/app.go`; see also `06-tools.md` for the full middleware chain detail.**

`BuildToolRegistry` assembles the middleware stack by registering cache first, then redact. The agent session emits `EventBeforeToolExec` through the hook bus before every tool invocation, giving the `SafetyHook` a chance to block the call. If not blocked, `Registry.Execute` runs the middleware chain: the cache middleware checks for a hit, then the redact middleware calls through to the actual tool implementation, which in turn uses `LocalExecutor` and/or `LocalFS` to enforce timeouts and sandbox boundaries. The result flows back up — redacted, then cached, then returned to the session.

The `LocalFS` and `Executor` are injected at tool construction time, not at the middleware level — they are structural constraints, not wrapping behaviors.

## 8. Current Gaps

1. **No automatic cache invalidation on writes.** `CacheMiddleware.Invalidate()` exists but is never called. A `write_file` or `shell_exec` that modifies files will not invalidate cached `read_file` results until the 60 s TTL expires.

2. **Executor has no allowlist/denylist.** The `SafetyHook` uses regex pattern matching, which is inherently bypassable (e.g., encoding tricks, aliasing). There is no allowlist mode or seccomp/AppArmor integration.

3. **No network-level sandbox.** The `SafetyHook` SSRF check resolves hostnames at check time, but DNS rebinding (where a hostname resolves to a public IP at check time and a private IP at request time) is not mitigated.

4. **Redaction is output-only.** Tool *arguments* (which may contain secrets pasted by the user) are not redacted before being written to the tape or sent to the LLM.

5. **Sandbox root equals cwd.** The filesystem sandbox is always rooted at the process working directory. There is no way to configure a narrower sandbox (e.g., restricting to a `src/` subdirectory) or to grant read-only access to paths outside the root.

6. **No resource limits on executor.** `LocalExecutor` enforces a timeout but does not cap CPU, memory, or disk I/O. A `shell_exec` call like `yes > /dev/null` will saturate a core until the timeout fires.

7. **Sub-agent sessions skip the safety hook.** Sub-agents created by `spawn_agent` register a `LoggingHook` but not a `SafetyHook`, so spawned agents bypass the destructive-command and SSRF checks.
