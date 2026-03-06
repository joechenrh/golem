# 08 — Hook / Event System

## 1. Overview

The `internal/hooks` package implements a lightweight event bus for agent lifecycle events. Hooks observe (and optionally block) actions as they flow through the agent loop. The design follows two rules:

* **`before_*` events are gates** — the first hook that returns an error aborts the action.
* **All other events are notifications** — errors are logged but never interrupt the main flow.

Source: `internal/hooks/`

## 2. Event Types

| `EventType` constant | String value | Fires when | Blocking? |
|-----------------------|---|---|---|
| `EventUserMessage` | `user_message` | User message received | No |
| `EventBeforeLLMCall` | `before_llm_call` | About to call the LLM | Yes |
| `EventAfterLLMCall` | `after_llm_call` | LLM response received | No |
| `EventBeforeToolExec` | `before_tool_exec` | About to execute a tool | Yes |
| `EventAfterToolExec` | `after_tool_exec` | Tool execution finished | No |
| `EventError` | `error` | Agent loop error | No |

Blocking is determined dynamically: `Emit` checks whether the event type string has the prefix `"before_"`.

## 3. Hook Interface

```go
type Hook interface {
    Name() string
    Handle(ctx context.Context, event Event) error
}
```

`Event` carries an untyped payload:

```go
type Event struct {
    Type    EventType
    Payload map[string]any
}
```

Payload keys are event-specific and loosely typed (e.g. `"tool_name"` as `string`, `"prompt_tokens"` as `int`). Each hook type-asserts the keys it cares about and ignores the rest.

## 4. Bus

`Register(h Hook)` appends to the hooks slice under a write lock. Hooks are called in registration order.

On `Emit`, the bus takes an `RLock`, snapshots the hooks slice with `slices.Clone`, then releases the lock so that concurrent registrations never block emission. It then iterates the snapshot, checking `ctx.Done()` before each hook call to respect cancellation. For `before_*` events, the first hook error stops iteration and propagates to the caller. For all other events, errors are logged at `Warn` level and iteration continues.

## 5. SafetyHook

Source: `internal/hooks/safety.go`

Only reacts to `EventBeforeToolExec`. Dispatches on `tool_name` to one of three check methods: `checkShell` for `shell_exec`, `checkWebFetch` for `web_fetch` and `http_request`, and `checkFileWrite` for `write_file` and `edit_file`.

### 5.1 Shell command blocking

The command string from the tool arguments is lower-cased, then matched against `dangerousPatterns` — a compiled slice of `*regexp.Regexp`. These patterns cover destructive filesystem operations (`rm -rf /`, `mkfs`, `dd of=/dev/`, fork bombs, `chmod 777 /`, `chown root /`), remote code execution vectors (`curl|sh`, `wget|sh`, `curl|python`, `eval $(curl`, `nc` to IP addresses), system commands (`shutdown`, `reboot`, `halt`, `poweroff`, `init 0/6`), and credential access (`cat /etc/shadow`, `cat .ssh/`, `cat .aws/`). Case-insensitive matching is achieved by lowering the command before regex evaluation, not via `(?i)` flags.

### 5.2 File path blocking

The path is lower-cased and tested against `sensitivePathPatterns`. These patterns protect environment files (`.env`, `.env.local`, `.env.production`), Git credentials (`.git/config`), SSH private keys and directories (`id_rsa`, `id_ed25519`, `.ssh/`, `authorized_keys`), generic credential files (`credentials`, `credentials.json`), and cloud/tool configuration directories (`.aws/`, `.kube/`, `.gnupg/`).

### 5.3 SSRF protection

For `web_fetch` and `http_request`, the URL is parsed and the hostname is resolved via `net.LookupIP`. Every resolved IP is checked against private CIDR ranges: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `127.0.0.0/8`, `169.254.0.0/16`, `0.0.0.0/8`, `::1/128`, `fc00::/7`, and `fe80::/10`. Cloud metadata hostnames (`metadata.google.internal`, `metadata`) are blocked by name before DNS resolution.

### 5.4 Malformed JSON handling

If `json.Unmarshal` fails on the tool arguments string, every check method returns an error immediately (e.g. `"safety: cannot parse shell_exec args"`). Malformed arguments are blocked rather than silently passed through.

## 6. MetricsHook

Source: `internal/hooks/metrics.go`

Collects operational counters in memory using a hybrid concurrency strategy. Scalar counters — `llmCalls`, `llmErrors`, `totalPromptTok`, and `totalCompleteTok` — are stored as `atomic.Int64` values for lock-free updates. Map-based counters — `toolCalls` and `toolErrors`, both `map[string]int64` — are protected by a `sync.Mutex`. Latency samples are stored in a `[]int64` ring buffer capped at 100 entries, also mutex-guarded, alongside a `llmCallStart` timestamp used to measure per-call duration.

On `before_llm_call`, the hook records `llmCallStart` to the current time. On `after_llm_call`, it increments `llmCalls`, adds `prompt_tokens` and `completion_tokens` from the payload to the running totals, computes the elapsed time since `llmCallStart`, and appends the latency to the ring buffer (evicting the oldest entry when it reaches capacity). On `after_tool_exec`, it increments `toolCalls[tool_name]`; if the result string starts with `"Error:"` or `"Tool execution blocked"`, it also increments `toolErrors[tool_name]`. On `error`, it increments `llmErrors`.

`Snapshot()` returns a `MetricsSnapshot` struct with deep copies of all maps and the latency slice, safe for concurrent read by callers. `Summary()` produces a human-readable multi-line string with LLM call counts, token totals, latency stats (avg/min/max over the ring buffer), and per-tool call/error counts.

## 7. LoggingHook

Source: `internal/hooks/logging.go`

Emits structured log lines via `zap.Logger`, flattening all payload keys into `zap.Field` entries using `zap.Any`. The log level varies by event type: `user_message`, `after_llm_call`, and `before_tool_exec` log at `Info`; `before_llm_call` and `after_tool_exec` log at `Debug`; `error` logs at `Error`; and unknown event types fall back to `Debug` with an extra `type` field. The hook never returns an error and therefore never interferes with `before_*` gate semantics.

## 8. AuditHook

Source: `internal/hooks/audit.go`

Writes a structured JSONL audit trail by appending one JSON object per line to a file opened in append mode (`O_CREATE|O_WRONLY|O_APPEND`) at construction time. Each line follows this schema:

```json
{"ts":"2006-01-02T15:04:05Z","event":"before_tool_exec","payload":{...}}
```

The `ts` field is `time.Now().UTC()` formatted as RFC 3339, `event` is the event type string, and `payload` is the event payload map (omitted when nil or empty). Writes are serialized with a `sync.Mutex`. The hook exposes a `Close()` method to flush and close the underlying file handle. It records every event type unconditionally, making the audit file a complete timeline of agent activity.

## 9. Current Gaps

* **No hook removal / deregistration.** Once registered, a hook stays for the lifetime of the bus. There is no `Unregister` or priority/ordering API.
* **No per-event filtering at registration time.** Every hook receives every event and must filter internally. A subscription map (`EventType -> []Hook`) would reduce dispatch overhead.
* **MetricsHook latency tracking is single-threaded.** `llmCallStart` is a single `time.Time`; concurrent LLM calls would overwrite each other's start time, producing incorrect latency values.
* **AuditHook does not buffer writes.** Each event triggers a `file.Write` syscall. A `bufio.Writer` with periodic flush would reduce I/O pressure under high event rates.
* **AuditHook has no rotation or size cap.** The JSONL file grows unbounded; long-running agents will need external log rotation.
* **SafetyHook DNS resolution is synchronous.** `net.LookupIP` in the SSRF check blocks the calling goroutine. A timeout or async resolution would prevent slow DNS from stalling the agent loop.
* **No hook-level timeout.** A misbehaving hook can block `Emit` indefinitely. Wrapping each `Handle` call with a per-hook context deadline would add resilience.
* **SafetyHook regex list is static.** There is no way to extend blocked patterns via configuration; changes require a code edit and rebuild.
