# 07 -- Channel System

## 1. Overview

The channel system abstracts message I/O so the agent core never depends on a
specific transport. Each channel adapts a different platform (terminal, Lark,
Telegram) into a uniform `Receive -> Process -> Send` loop. All channel
implementations live under `internal/channel/` and satisfy the `Channel`
interface defined in `internal/channel/channel.go`.

Three implementations exist today:

| Package | Transport | Status |
|---------|-----------|--------|
| `internal/channel/cli/` | Interactive terminal (stdin/stdout) | Complete |
| `internal/channel/lark/` | Lark/Feishu WebSocket | Complete |
| `internal/channel/telegram/` | Telegram Bot API | Stub only |

## 2. Channel Interface

Source: `internal/channel/channel.go`

The `Channel` interface defines six methods. `Name` returns a stable identifier for the channel type. `Start` runs the channel's receive loop, blocking until the context is cancelled or the transport disconnects, and pushes incoming messages onto the provided `inCh`. `Send` delivers a complete response, while `SendStream` delivers tokens incrementally when `SupportsStreaming` returns true; `app.go` chooses between them at dispatch time. `SendTyping` signals that the agent is working -- currently a no-op for all channels but reserved for future typing-indicator support.

The `IncomingMessage` struct carries a `ChannelID` that encodes both the channel type and the chat identity (e.g. `"lark:" + chatID`), along with `ChannelName`, `SenderID`, `SenderName`, `Text`, and a free-form `Metadata` map. It also includes a `Done` channel that is closed when processing completes, allowing a blocking `Start` loop to know when the agent has finished so it can accept the next message or update UI state such as the CLI "Thinking..." indicator.

The `SystemPrinter` interface is an optional extension for channels that support direct user-facing output. It declares `PrintSystem`, `PrintError`, and `PrintBanner` methods. Only `CLIChannel` implements it. `app.go` uses a type assertion to access `PrintError` when available; otherwise it falls back to logging.

## 3. CLI Channel

Source: `internal/channel/cli/`

The CLI channel implements a synchronous REPL for interactive terminal use.

### Input

`Start` reads lines from `os.Stdin` (overridable via `WithReader`) using a
`bufio.Scanner`. Empty lines are skipped. After sending each message to `inCh`,
it prints a dim "Thinking..." indicator and blocks on the `Done` channel before
showing the next prompt.

### Output

In non-streaming mode, `Send` clears the thinking indicator with `\r\033[K`, prints the full response, and sets `thinkingCleared = true`. In streaming mode, `SendStream` clears "Thinking..." on the first token using the same escape sequence, then prints each token inline. A trailing newline is appended after the stream closes.

### Banner

`PrintBanner` outputs version, model name, tool count, and tape path on
startup. `PrintSystem` and `PrintError` use ANSI color codes (`\033[2m` dim,
`\033[31m` red).

### Configuration

Functional options (`WithPrompt`, `WithReader`, `WithWriter`) allow test
harnesses to substitute stdin/stdout.

## 4. Lark Channel

Source: `internal/channel/lark/`

### Connection

Uses the Lark WebSocket SDK (`larkws.NewClient`) -- no public endpoint or
webhook URL is required. `Start` registers an `OnP2MessageReceiveV1` handler
on the event dispatcher, then launches two goroutines in an `errgroup`: the WebSocket client (`wsClient.Start`) which maintains the long-lived connection with automatic reconnect, and the eviction loop (`seenMsgsEvictionLoop`) which periodically cleans up the dedup map described below.

### Message Deduplication

Lark WebSocket uses at-least-once delivery. If the handler blocks (e.g.
waiting for an LLM response), the SDK may redeliver the same event. The
`seenMsgs` field (`sync.Map`) stores `messageID -> time.Time` pairs.
`LoadOrStore` in `onMessageReceive` detects and drops duplicates.

The eviction loop runs on a ticker (default 5 min), deleting entries older than `maxAge`. If the map still exceeds `maxSeenMsgs` (10,000) after age-based cleanup, the oldest entries are force-evicted until the count drops below the cap.

### Incoming Message Processing

`onMessageReceive` filters for `text`-type messages, extracts the text via
`extractTextContent` (parses the `{"text":"..."}` JSON wrapper), strips
`@bot` mention keys, and pushes an `IncomingMessage` to `inCh`. The handler
then blocks on `<-done`, serializing processing per WebSocket callback.

Before dispatch, `sentChats` is cleared (`l.sentChats.Clear()`) to reset the
per-cycle duplicate-send guard.

### Card-Based Message Rendering

All outgoing messages are sent as Lark interactive cards, not plain text.
`buildCard` constructs the JSON structure:

```go
{"elements": [{"tag": "markdown", "content": "<sanitized text>"}]}
```

Cards are sent via `Im.V1.Message.Create` and patched in-place via
`Im.V1.Message.Patch` during streaming.

### Markdown Sanitization

`sanitizeLarkMarkdown` converts standard markdown into Lark's supported
subset. Content inside fenced code blocks is preserved untouched. Outside
code fences:

- `# Heading` -> `**Heading**` (bold)
- `> blockquote` -> `*blockquote*` (italic)
- `` `inline code` `` -> plain text (backticks stripped)

### Streaming

`SendStream` implements progressive card updates. Tokens accumulate in a `strings.Builder` while a ticker fires every 800 ms (`streamUpdateInterval`). On the first tick with content, `sendCardReturnID` creates the initial card and captures the `messageID`. Subsequent ticks call `patchCard` to update the card in place, appending a typing cursor (`" ▍"`) during streaming that is removed on the final update.

### Duplicate Send Guard

`sentChats` (`sync.Map`) prevents the same chat from receiving two replies in
a single processing cycle. Both `Send` and `SendStream` record the chat ID;
`Send` skips if the chat was already written to (e.g. by a tool call via
`SendToChat`).

### Lark Document Operations

Two methods support reading and writing Feishu documents. `ReadDocContent` calls `Docx.V1.Document.RawContent` to fetch plain-text content by document ID. `WriteDocContent` is a multi-step process: it converts markdown to Feishu blocks via `Document.Convert` with `ContentTypeMarkdown`, fetches the root block to count existing children, batch-deletes all existing children, then creates new blocks in batches of `maxBlocksPerRequest` (50), respecting the Feishu API limit per call.

### Auxiliary

`ListChats` returns `[]ChatInfo` (ChatID, Name, Description) for groups the bot has joined. `zapLarkLogger` adapts `zap.Logger` to the Lark SDK's logger interface.

## 5. Telegram Channel

Source: `internal/channel/telegram/`

Currently a stub. `Start` returns an error and all other methods are no-ops.
`SupportsStreaming` returns false. The struct and constructor exist to reserve
the namespace and allow config validation without a runtime dependency on a
Telegram SDK.

## 6. Message Flow

For the full message flow trace, see 01-architecture.md section 6.

The dispatch loop in `app.go` fans out incoming messages by `ChannelID`: CLI messages are processed inline, while remote messages are dispatched to per-chat goroutine queues (cap 16). For each message, the dispatcher looks up the appropriate `Channel` by name and the appropriate `Session` (the default session for CLI, `SessionManager.GetOrCreate` for remote channels). If the channel supports streaming, a `tokenCh` is created and `SendStream` runs concurrently with the session's `HandleInputStream`; otherwise the session produces a complete response and `Send` delivers it.

## 7. Current Gaps

1. **Telegram is unimplemented.** The stub exists but `Start` returns an error.
   A real implementation would need polling or webhook-based message receipt,
   ACL filtering, and markdown rendering for Telegram's Bot API.

2. **No `SendTyping` usage.** The interface method exists on all channels but
   every implementation is a no-op. It could be wired into `processMessage`
   before the LLM call to show typing indicators on platforms that support them.

3. **Lark only handles text messages.** `onMessageReceive` filters for
   `*msg.MessageType == "text"` and silently drops images, files, and rich
   content. Supporting additional message types would require content-type
   dispatch and possibly multimodal LLM input.

4. **No reconnect signaling.** If the Lark WebSocket drops permanently,
   `Start` returns and the entire agent shuts down (via `gcancel()`). There is
   no backoff-and-retry wrapper at the `app.go` level.

5. **Single-block cards.** `buildCard` emits a single markdown element. Very
   long responses may hit Lark's per-element character limit. The
   `maxBlocksPerRequest` batching exists for doc writes but is not applied to
   card rendering.

6. **No outbound-only channel.** The scheduler can send messages to channels
   but there is no lightweight "send-only" channel adapter; it reuses the full
   `Channel` interface even when `Start` is never called.
