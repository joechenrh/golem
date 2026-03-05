# Golem TODO

Remaining issues from codebase review. Ordered roughly by impact.

## High Priority

### #4 — looksLikePlan false positives
`looksLikePlan()` uses simple substring matching ("I'll", "let me") which can
trigger on legitimate final answers. Consider requiring multiple signals
(e.g., plan keywords + presence of tool names) or using a lightweight classifier.
File: `internal/agent/agent.go`

### #5 — Session nil deref on config race
`SessionManager.GetOrCreate` creates sessions with shared `*config.Config`.
If config is mutated concurrently (e.g., hot-reload), sessions could see
inconsistent state. Deep-copy config on session creation or make config immutable.
File: `internal/agent/session.go`

### #8 — Registry thread safety
`tools.Registry` uses a plain map without synchronization. If `Expand()` or
`RegisterAll()` is called concurrently with `Get()`, it races. Add `sync.RWMutex`
or switch to `sync.Map`.
File: `internal/tools/registry.go`

### #9 — Anthropic streaming retry
Anthropic SSE connections can drop mid-stream. Currently no retry logic exists —
a dropped connection returns a partial response. Add reconnect with
`Last-Event-ID` or at minimum detect truncation and retry the full call.
File: `internal/llm/anthropic.go`

## Medium Priority

### #7 — fsync after tape write
`FileStore.Append` writes to a persistent file handle but never calls `Sync()`.
A crash can lose the last few entries. Add periodic or per-write fsync, or at
least fsync on `Close()`.
File: `internal/tape/store.go`

### #10 — expandHome edge cases
`expandHome()` only handles the `~/` prefix. Paths like `~user/dir` are silently
passed through unchanged, which may surprise users. Either support `~user` via
`os/user.Lookup` or return an error.
File: `internal/fs/local.go`

### #14 — Parameterized SQL in tape search
If tape search ever adds query parameters from user input, the current string
concatenation approach would be vulnerable. Pre-emptively use parameterized
queries if migrating tape to SQLite.
File: `internal/tape/store.go`

### #15 — Lark log redaction
Lark webhook payloads logged at debug level may contain user messages with
sensitive content. Add a redaction pass before logging, or lower the log level.
File: `internal/channel/lark/lark.go`

### #20 — Tool definitions in context budget
Each tool definition consumes prompt tokens. With many tools expanded, the
context window fills faster. Track tool-definition token overhead and factor
it into the context strategy's budget calculations.
File: `internal/ctxmgr/strategy.go`, `internal/tools/registry.go`

### #21 — Graceful context overflow
When conversation history exceeds the context window, the anchor strategy
silently drops old messages. Add a user-visible indicator (e.g., "[earlier
messages truncated]") so the agent and user know context was lost.
File: `internal/ctxmgr/strategy.go`

## Lower Priority

### #22 — Human-in-the-loop confirmation
No mechanism for the agent to ask for user confirmation before executing
dangerous actions (e.g., deleting files, running destructive commands).
Add a `confirm` tool or hook that pauses execution and waits for user approval.

### #23 — Multimodal support
No image/file attachment handling. Lark messages with images are treated as
text-only. Add support for extracting image URLs and passing them to
vision-capable models.
File: `internal/channel/lark/lark.go`, `internal/llm/types.go`

### #27 — Concurrent CLI input handling
The CLI channel reads from stdin synchronously. If the agent is processing,
new user input is blocked. Consider buffering input or showing a "processing"
indicator.
File: `internal/channel/cli/cli.go`

### #29 — Telegram channel stub
`internal/channel/telegram/` exists but is unimplemented. Either implement it
or remove the stub to avoid confusion.
File: `internal/channel/telegram/`
