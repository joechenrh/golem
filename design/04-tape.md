# Design 04 — Tape (Conversation Log)

## 1. Overview

The tape is an append-only JSONL conversation log that serves two purposes:

1. **Persistence** — every user message, assistant response, tool call, system event, anchor, and summary is written to a `.jsonl` file on disk so sessions survive restarts.
2. **Context source** — on each LLM call the tape entries are fed through `BuildMessages` (via a `ContextStrategy`) to assemble the message history the model actually sees.

Source files:

| File | Role |
|------|------|
| `internal/tape/entry.go` | `TapeEntry` struct, entry kinds, `BuildMessages` |
| `internal/tape/store.go` | `Store` interface, `FileStore` (cache + disk) |
| `internal/tape/discover.go` | `Discover`, `ParseChatID` for session restore |
| `internal/ctxmgr/strategy.go` | `AnchorStrategy`, `MaskingStrategy` — consume `BuildMessages` output |

## 2. Entry Types

Each tape entry carries an `ID` (UUID, auto-assigned on append), a `Kind`, a `Timestamp`, and a `Payload` stored as raw JSON. Using raw JSON for the payload allows different entry kinds to carry different shapes — an `llm.Message` for messages, a `{"label": "..."}` map for anchors, and so on — while surviving JSON round-trips without losing type fidelity. Embedded structures like `[]ToolCall` inside an assistant message are preserved exactly.

| Kind | Constant | Purpose |
|------|----------|---------|
| `"message"` | `KindMessage` | User, assistant, or tool messages. Payload is an `llm.Message` (role, content, tool_calls, tool_call_id). |
| `"anchor"` | `KindAnchor` | Context boundary marker. Everything before the most recent anchor is excluded from LLM context. |
| `"summary"` | `KindSummary` | Auto-generated conversation summary. Injected as synthetic context on restore. |
| `"event"` | `KindEvent` | System events (session start, config changes). Not sent to the LLM. |

## 3. FileStore

Source: `internal/tape/store.go`

`FileStore` combines a mutex-guarded in-memory slice with a persistent file handle, keeping the two in sync.

```
  ┌──────────────┐         ┌─────────────────┐
  │  []TapeEntry  │  <───>  │  .jsonl on disk  │
  │  (in-memory)  │         │  (append-only)   │
  └──────────────┘         └─────────────────┘
```

On creation the file is opened with `O_CREATE|O_APPEND|O_WRONLY`, its size is recorded, and `loadFromDisk` populates the in-memory cache. When appending, the entry is marshaled to JSON and written to disk first, then added to the in-memory slice; this disk-first ordering means a crash after write but before cache update loses nothing, because the entry will be reloaded on next start. Reads return a clone of the cache to prevent caller mutation. Search performs a brute-force case-insensitive substring match on raw payload bytes.

## 4. On Restart

### What the LLM sees

The raw entries are not sent directly. `BuildMessages` filters and transforms them. It scans all entries for the most recent anchor and the most recent summary. If a summary is found, two synthetic messages are prepended: a user message containing the summary text and an assistant acknowledgment. Then all `KindMessage` entries after the last anchor are collected and deserialized; for user messages with a `sender_id` field, a `[sender:xxx]` prefix is prepended for group-chat speaker identification.

### Example

Suppose the tape contains:

```
entry 0:  KindMessage   user      "What is Go?"
entry 1:  KindMessage   assistant "Go is a language..."
entry 2:  KindSummary              {"summary": "Discussed Go basics"}
entry 3:  KindAnchor               {"label": "context-reset"}
entry 4:  KindMessage   user      "How do goroutines work?"
entry 5:  KindMessage   assistant "Goroutines are lightweight..."
```

`BuildMessages` produces:

```
msg 0:  user       "[Previous conversation summary]\nDiscussed Go basics"
msg 1:  assistant  "Understood, I have the context from our previous conversation."
msg 2:  user       "How do goroutines work?"
msg 3:  assistant  "Goroutines are lightweight..."
```

Entries 0-1 are before the anchor so they are excluded. The summary (entry 2, also before the anchor) is still found and injected. Entry 3 (the anchor itself) is skipped. Only entries 4-5 become real messages.

### Context strategies

`BuildMessages` output is consumed by a `ContextStrategy` (in `ctxmgr/strategy.go`). **AnchorStrategy** passes the output through `trimToFit`, which drops oldest messages if total tokens exceed the model's context window, always dropping tool call / tool result pairs together to avoid orphaning. **MaskingStrategy** does the same but first truncates large tool-result messages — keeping head and tail with a `[...truncated N chars...]` marker — when estimated tokens exceed 50% of the context window.

## 5. Rotation

Rotation is triggered at 50 MB (`MaxTapeFileSize`), checked at the end of every `Append`.

Source: `internal/tape/store.go`

The procedure closes the current file handle, renames the file to `<path>.bak` (overwriting any previous backup), and opens a fresh file at the original path. If the new file cannot be opened, the `.bak` is renamed back to avoid data loss. The disk byte counter resets to zero. The in-memory entries are then trimmed: if an anchor exists, the anchor and everything after it are kept; otherwise the most recent 100 entries are retained.

The kept entries are **not** re-written to the new file. After rotation the new `.jsonl` starts empty and only contains entries appended from that point forward. The in-memory cache retains the trimmed history so the current process continues to work, but if the process restarts before any new entries are appended, those kept entries are lost — they exist only in the `.bak` file.

## 6. Anchors

An anchor (`KindAnchor`) is a context boundary marker that tells the system the LLM only needs to see messages after this point.

Source: `internal/tape/store.go`, `internal/tape/entry.go`

`AddAnchor` appends a `TapeEntry` with `Kind: KindAnchor` and a `{"label": "..."}` payload. `BuildMessages` scans for the last anchor and excludes all `KindMessage` entries at or before that index. `EntriesSince` returns all entries after a given anchor ID for callers that need raw post-anchor entries, while `LastAnchor` returns the most recent anchor by scanning backwards. During rotation, `entriesFromLastAnchor` decides what to keep in memory, preserving the anchor itself as the first kept entry. `Info` reports `AnchorCount` and `EntriesSinceAnchor` for diagnostics.

Anchors are added explicitly (e.g., by a tool or command). There is currently no automatic anchor insertion based on message count or token budget.

## 7. Summaries

A `KindSummary` entry contains a `{"summary": "..."}` payload generated by calling the LLM with a summarization prompt.

Source: `internal/agent/session.go`

`Session.Summarize` reads all tape entries and calls `BuildMessages` to get the current message history, takes the last 50 messages to keep the summarization call small, and appends a user prompt asking for 3-5 bullet points covering key points, decisions, and outcomes in the user's language. The messages are sent to the LLM with `MaxTokens: 1024`, and the result is appended as a `KindSummary` entry to the tape.

`BuildMessages` scans the entire entry list backwards for the most recent summary. If found, it prepends a synthetic user/assistant exchange before the real post-anchor messages:

```
user:      [Previous conversation summary]\n<summary text>
assistant: Understood, I have the context from our previous conversation.
```

The summary is visible to the LLM regardless of anchor position — even if the summary entry is before the last anchor, it is still found and injected.

## 8. Session Discovery

### Filename format

```
session-<agentName>-<chatID>-<YYYYMMDD>-<HHMMSS>.jsonl
```

Example: `session-golem-oc_abc123-20260305-143022.jsonl`

### Discover and ParseChatID

Source: `internal/tape/discover.go`

`Discover(dir, prefix)` lists all files in `dir` that are not directories, match the given prefix (e.g., `"session-golem-"`), and end with `.jsonl`. Results are sorted by name, which is naturally chronological due to the timestamp suffix. `ParseChatID` extracts the chat ID from a filename by stripping the prefix and `.jsonl` suffix, then removing the `YYYYMMDD-HHMMSS` timestamp from the end by locating the second-to-last dash.

### Session restore flow

Source: `internal/agent/manager.go`

`SessionManager.LoadExisting` calls `Discover` to find all tape files matching the agent's prefix, groups them by chat ID (keeping only the most recent tape per chat since `Discover` returns sorted results), skips empty files, and calls `createSessionFromTape` which opens the tape via `NewFileStore` — triggering `loadFromDisk` and restoring the full in-memory history. The session is then registered in `sm.sessions[chatID]`.

## 9. Current Gaps

1. **No fsync** — `Append` calls `file.Write` but never `file.Sync()`. On crash, the OS write buffer may not have been flushed, potentially losing the most recent entries.

2. **No auto-anchor** — anchors are only added explicitly. There is no automatic insertion when message count or token usage crosses a threshold, meaning context can grow unboundedly until rotation kicks in at 50 MB.

3. **Rotation does not persist kept entries** — after rotation, the new `.jsonl` file starts empty. The trimmed entries from `entriesFromLastAnchor` live only in memory. If the process restarts before new entries are written, those kept entries are lost. Only the `.bak` file retains them.

4. **Single backup** — rotation renames to `.bak`, overwriting any previous backup. A long-running session that rotates multiple times loses all but the most recent backup.

5. **Scanner line limit** — `loadFromDisk` uses a 1 MB buffer per line. Any single JSONL entry exceeding 1 MB (e.g., a very large tool output) would be silently skipped.

6. **Summary search is global** — `BuildMessages` scans the entire entry list for the most recent summary, not just entries after the last anchor. This is intentional (summaries should survive anchors), but it means a stale summary from a very old conversation segment could be injected if no newer summary exists.

7. **No compaction** — there is no mechanism to rewrite a tape file with only the relevant entries (post-anchor + summary). The file grows monotonically until rotation.
