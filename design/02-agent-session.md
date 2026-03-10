# Design 02: Agent Session System & Command Routing

## 1. Overview

`Session` is the core runtime unit. It owns a single ReAct loop: receive user input, build LLM context, call the model, execute any requested tools, repeat until a final answer or the iteration cap is hit. Every conversation -- whether a local CLI session or a remote Lark chat -- gets its own `Session` instance.

`SessionManager` provides per-chat isolation for remote channels. It maps channel IDs (e.g. `lark:oc_xxx`) to `Session` instances, handling creation, eviction, and restore from persisted tape files.

The **command router** intercepts colon-prefixed input (`:help`, `:tape.info`, `:ls -la`) before it reaches the LLM, dispatching to built-in handlers or the shell.

Source: `internal/agent/session.go`, `internal/agent/manager.go`, `internal/router/router.go`

## 2. ReAct Loop

Two entry points exist: `HandleInput()` for non-streaming channels such as Lark, and `HandleInputStream()` for the CLI which pipes tokens to a channel for real-time display. Both route input through `router.RouteUser()` first; if the input is a command, the command handler runs and the LLM is never called. Otherwise `runReActLoop()` takes over.

The loop resets per-turn state (usage counters, tool failure counts, nudge counter) and iterates up to `MaxToolIter` times. At the top of each iteration, `injectCompletedTasks()` drains any finished background tasks from the `TaskTracker` and appends their results as ephemeral user messages so the LLM can see them. Each iteration then calls `executeLLMCall()`, which converts tape entries into an LLM message list via the context strategy, builds the system prompt (persona or flat), collects tool definitions with progressive disclosure, and makes either a streaming or non-streaming API call. Usage statistics are accumulated into both turn-level and session-level counters, and lifecycle hooks fire before and after the call. On the first successful LLM response, the pending user message is persisted to tape -- this deferred write prevents a dangling user entry when the API call fails. The loop then branches: if tool calls are present, they are processed and the loop continues; if the response is empty with no tool calls, a warning is logged and the loop retries; if the response looks like a plan and fewer than two nudges have been sent, the plan text and a nudge prompt are appended to tape and the loop continues; otherwise the response is treated as the final answer. If the loop exhausts `MaxToolIter`, a fallback message is returned.

### Streaming

`doStreamingCall()` consumes an event channel returned by `llm.ChatStream()` and assembles the full response from deltas. Text fragments (`StreamContentDelta`) are forwarded to a token channel for real-time display. Tool call arguments (`StreamToolCallDelta`) are accumulated incrementally into a map keyed by call ID and normalized via `llm.NormalizeArgs()` before being added to the response. The `StreamDone` event captures final usage, and any `StreamError` is returned immediately.

### Tool execution

Source: `internal/agent/session.go` (`processToolCalls`)

The assistant message (with tool calls) is first recorded to tape. Progressive disclosure then marks each called tool for full schema in future iterations via `tools.Expand()`, and `tools.ExpandHints()` expands tools mentioned in the assistant's text. All tool calls execute **in parallel** via `errgroup`, and results are appended to tape **in original order** for deterministic replay. Failures are tracked for self-correction (see section 4).

### Assistant response post-processing

`router.RouteAssistant()` scans the final answer for embedded colon commands at line starts, skipping lines inside code fences. Detected commands are executed and their output appended; command lines are stripped from the clean text.

## 3. Auto-Nudge (Classifier-Based)

When the LLM returns text but no tool calls, a two-phase decision flow determines whether to nudge, escalate, or accept the response. The system relies on an LLM classifier rather than heuristic phrase matching, avoiding false positives on legitimate clarifying questions (e.g. "可以。你想查哪方面的新闻？" was previously caught by ack detection matching "可以").

### Phase 1: Classifier (primary)

When configured via `GOLEM_CLASSIFIER_MODEL`, a lightweight LLM classifies all non-empty text-only responses in sessions that have used tools before. The classifier returns one of:

- **"nudge"**: The agent is describing a plan instead of acting. Inject a generic nudge.
- **"accept"**: The response is a valid final answer (including clarification questions). Accept it.
- **"stuck"**: The agent is lost. Inject a task-specific reminder with the classifier's summary.

Up to `maxNudges` (2) classifier nudges are allowed per turn. When no classifier is configured, all text-only responses are accepted as-is.

### Phase 2: Stuck Escalation

If the classifier nudged at least once and the LLM still responds with text-only (no tool calls), the system injects a **task-specific reminder** using either the classifier's task summary or the last user message as context. This gets one additional attempt. Stuck escalation is skipped when the classifier explicitly accepted the current response.

### Escalation Flow

```
Iteration N: LLM responds with text (no tool calls)
  → Classifier configured? → classifier decides (nudge/accept/stuck)
  → No classifier? → accept as final answer
Iteration N+1: still text-only (after classifier nudge)
  → Stuck escalation → task reminder
Iteration N+2: still text-only
  → Accept as final answer
```

Source: `internal/agent/nudge.go`, `internal/agent/session.go`

## 4. Self-Correction

Per-tool failure counts are tracked in a map reset each turn. When a tool result starts with `"Error:"`, its counter increments. At 3 failures for the same tool, a user-role message is injected telling the LLM to reconsider its approach and try a different tool or method. This steers the model away from a broken tool without hard-blocking it.

## 5. Summarization

`Summarize()` reads all tape entries and converts them to messages. If fewer than 2 messages exist, it skips. Otherwise it caps at the last 50 messages, appends a summarization prompt asking for 3-5 bullet points in the user's language, and makes a non-streaming LLM call with `MaxTokens: 1024` and no tools. The result is appended as a `KindSummary` tape entry.

When building context, `tape.BuildMessages()` scans backwards for the most recent `KindSummary` entry. If found, it prepends a synthetic user/assistant exchange containing the summary text and an acknowledgment, giving restored sessions condensed history before the post-anchor messages.

Summarization is called before eviction in `EvictIdle()`. The eviction loop calls `Summarize()` for each idle session before cancelling its context and deleting it.

## 6. SessionManager

Source: `internal/agent/manager.go`

The manager holds a mutex-protected map from channel ID to `Session`, a `SessionFactory` containing shared resources (LLM client, config, logger, and a `ToolFactory` that creates a fresh tool registry per session), and a base context that parents all session contexts.

**GetOrCreate** locks the map and returns an existing session if one exists (updating `lastAccess`). If the map is at capacity, it evicts the oldest session to make room. To create a new session, it allocates a tape file (`session-<agent>-<safeID>-<timestamp>.jsonl`), a fresh context strategy, a fresh hooks bus (with logging and safety hooks), and a fresh tool registry, then wires everything into a new `Session` with a cancellable context derived from the base.

**Eviction** follows three paths. `evictOldestLocked()` fires when `GetOrCreate` hits capacity -- it linearly scans for the session with the oldest `lastAccess`, cancels its context, and deletes it without summarization. `EvictIdle(maxAge)` is called periodically by `StartEvictionLoop` (a background goroutine that ticks at a configured interval); it iterates all sessions, and for any whose `lastAccess` is before the cutoff, it summarizes first, then cancels and deletes. `Shutdown()` runs at application exit and cancels all sessions without summarization. All cleanup paths call `Session.Close()` before cancelling the context, ensuring background subagent goroutines are cancelled and waited on via the `TaskTracker`.

**LoadExisting** discovers persisted tape files via `tape.Discover()`, groups them by chat ID (keeping only the most recent tape per chat by lexicographic sort on timestamp), and for each non-empty tape calls `createSessionFromTape()` to open the existing file rather than creating a new one. It sets `lastAccess` to the tape file's modification time.

## 7. Command Router

Source: `internal/router/router.go`

### User input routing

Input must start with `:` to be treated as a command. The router first checks against the internal commands set (dotted names like `tape.info` are supported). Anything starting with `:` that is not internal is treated as a shell command, with the full text after `:` becoming the command string.

Internal commands:

| Command | Description |
|---|---|
| `:help` | List available commands |
| `:quit` | Exit the session |
| `:usage` | Show session + turn token counts |
| `:metrics` | Show operational metrics (if hook registered) |
| `:tape.info` | Tape file path, entry/anchor counts |
| `:tape.search <q>` | Search tape history |
| `:tools` | List all registered tools |
| `:skills` | List discovered skills only |
| `:model [name]` | Show current model (switching not yet supported) |
| `:reset [label]` | Insert a context boundary anchor |

Shell commands (e.g. `:ls -la`, `:git status`) are dispatched to the `shell_exec` tool via `tools.Execute()`.

### Assistant output routing

`RouteAssistant()` scans assistant output line by line for colon commands at line starts, skipping lines inside code fences. Each detected command is parsed through `RouteUser()` and returned as a `DetectedCommand` with its line number. The caller executes each and appends results to the response.

### Argument parsing

`splitArgs` splits on whitespace with quote handling (single and double quotes, backslash escapes inside quotes). `ParseArgs` categorizes the tokens:

| Token form | Destination |
|---|---|
| `--key=value` | `Flags["key"] = "value"` |
| `--key value` | `Flags["key"] = "value"` (next token consumed) |
| `--flag` (no value, next token is also a flag) | `BoolFlags["flag"] = true` |
| anything else | `Positional` slice |

## 8. Token Tracking

The session maintains two `llm.Usage` accumulators: `sessionUsage`, which accumulates over the entire session lifetime and is never reset, and `turnUsage`, which is reset at the start of each `runReActLoop()` invocation. Both are updated after each LLM response with `PromptTokens`, `CompletionTokens`, and `TotalTokens`. Usage is surfaced via the `:usage` command and emitted in `EventAfterLLMCall` hook payloads.

## 9. Current Gaps

1. **No summarization on capacity eviction or shutdown.** `evictOldestLocked()` and `Shutdown()` cancel sessions without calling `Summarize()`. Only `EvictIdle()` summarizes. A session evicted to make room for a new one loses its context permanently.

2. **Model switching is stubbed out.** `:model <name>` reports "not yet supported". Switching would require creating a new `llm.Client`, which the session does not own the factory for.

3. **No per-session concurrency guard.** Nothing prevents two goroutines from calling `HandleInput()` on the same `Session` simultaneously. If a Lark chat receives two messages in rapid succession, both could enter `runReActLoop()` concurrently, racing on `turnUsage`, `toolFailures`, and tape writes.

4. **`evictOldestLocked` is O(n).** It linearly scans all sessions to find the oldest. Fine at the current default cap of 100 sessions, but would need a heap or LRU structure if the cap grows significantly.

5. **Streaming usage may be zero.** `doStreamingCall()` only captures `Usage` from the `StreamDone` event. If the provider does not emit usage in the streaming path, `turnUsage` and `sessionUsage` will under-count.

6. **LoadExisting does not enforce MaxSessions.** It restores all discovered tapes without checking the cap. On restart with many persisted tapes, the session count could exceed the configured limit until the next eviction cycle runs.

7. **`RouteAssistant` fence tracking is naive.** It toggles `inFence` on any line starting with `` ``` ``. Nested or indented fences, or fences with language tags on separate lines, could cause mis-detection. In practice this is unlikely since LLM output rarely nests fences.
