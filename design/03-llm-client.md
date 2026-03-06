# Design 03 — LLM Client Abstraction

## 1. Overview

The `internal/llm` package provides a provider-agnostic interface for calling large language models. The central abstraction is the `Client` interface:

```go
type Client interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
    Provider() Provider
}
```

Two providers are registered at init time: **OpenAI** and **Anthropic**. A third-party provider can be added via `RegisterProvider` without modifying the package. The caller builds a `ChatRequest` with a provider-neutral message list and tool definitions; the concrete client translates that into the provider's wire format, executes the HTTP call, and translates the response back into the unified `ChatResponse` or `StreamEvent` stream.

An optional `RateLimitedClient` wrapper applies a token-bucket rate limiter (`golang.org/x/time/rate`) in front of any `Client`.

## 2. Type System

Source: `internal/llm/types.go`

`Message` carries a `Role` (`system | user | assistant | tool`), text `Content`, an optional slice of `ToolCall` (when the model invokes tools), and a `ToolCallID` plus `Name` (when the message is a tool result). `ToolCall` stores the call `ID`, tool `Name`, and `Arguments` as a raw JSON string. `ToolDefinition` describes a tool the model may invoke: name, description, and a `json.RawMessage` JSON Schema for its parameters. `ChatRequest` bundles model name, message history, tool definitions, max tokens, temperature, system prompt, and an optional `ResponseFormat` (`"text"` or `"json_object"`). Temperature is a `*float64` pointer: nil means "use the provider's default," while a non-nil pointer sends the exact value, including zero. Both provider implementations forward the pointer as-is to their wire structs so that `omitempty` correctly omits the field only when the caller leaves it nil. `ChatResponse` contains the model's text content, any tool calls, token `Usage`, and a normalized `FinishReason` (`"stop"`, `"tool_calls"`, `"length"`). `NormalizeArgs` replaces empty or whitespace-only tool-call argument strings with `"{}"` so callers never receive an unparseable empty string.

`StreamEventType` discriminates four event kinds delivered over a buffered channel (size 8):

| Type | Payload fields |
|---|---|
| `StreamContentDelta` | `Content` (text fragment) |
| `StreamToolCallDelta` | `ToolCall` (`*ToolCallDelta` — partial ID, name, or argument fragment) |
| `StreamDone` | `Usage` (final token counts) |
| `StreamError` | `Error` |

## 3. Provider Detection

`ParseModelProvider` splits a model string on the first colon: `"anthropic:claude-sonnet-4-20250514"` yields `(ProviderAnthropic, "claude-sonnet-4-20250514")`, while a bare `"gpt-4o"` defaults to `ProviderOpenAI`.

`NewClient` looks up the provider in the `providers` map (protected by an RWMutex), applies any `ClientOption` overrides (currently just `WithBaseURL`), and calls the registered factory. The default base URLs are `https://api.openai.com/v1` for OpenAI and `https://api.anthropic.com` for Anthropic. `NewOpenAICompatibleClient` is exported so external code can register providers that expose an OpenAI-compatible API using the existing wire-format logic.

## 4. OpenAI Wire Format

Source: `internal/llm/openai.go`

Endpoint: `POST {baseURL}/chat/completions`. Authentication is via the `Authorization: Bearer {apiKey}` header.

The request builder prepends the `SystemPrompt` as a system-role message at the front of the messages array, maps each `Message` one-to-one to an OpenAI message (tool calls become objects with `Type: "function"`), wraps each `ToolDefinition` in an `openaiTool` envelope with `Type: "function"`, forwards `Temperature` as a pointer (omitted when nil), and passes `ResponseFormat` through directly.

Response conversion uses only `Choices[0]`. Content, finish reason, and tool calls are extracted directly, with tool call arguments run through `NormalizeArgs`.

For streaming, SSE parsing is handled by the shared `sseReader` in `stream.go`. The stream loop reads SSE events until `data: [DONE]` or EOF. Each chunk may contain a content delta and/or indexed tool call deltas. Usage arrives on the final chunk (via OpenAI's `stream_options`) and is emitted with the `StreamDone` event. The streaming HTTP client uses `Timeout: 0`, relying on context cancellation instead of a fixed deadline.

## 5. Anthropic Wire Format

Source: `internal/llm/anthropic.go`

Endpoint: `POST {baseURL}/v1/messages`. Authentication headers are `x-api-key`, `anthropic-version: 2023-06-01`, and `Content-Type: application/json`.

The request builder places the `SystemPrompt` in Anthropic's top-level `system` string field rather than as a message. If `ResponseFormat.Type` is `"json_object"`, a JSON-mode instruction is appended to that system string since Anthropic has no native `response_format`. `MaxTokens` defaults to 4096 if the caller sends 0 (Anthropic requires it). Tool definitions map `Parameters` to `input_schema`.

Message conversion is the most complex translation because Anthropic requires strictly alternating user/assistant turns and uses content blocks rather than top-level fields for tool interactions. System messages are filtered out (handled via the top-level field). Assistant messages with tool calls become an assistant message whose content is an array of content blocks — an optional text block followed by one `tool_use` block per call, with the `Arguments` string cast directly to `json.RawMessage` for the `Input` field. Tool result messages become `tool_result` content blocks inside a user message, and consecutive tool results are merged into the same user message to maintain the alternating-turn invariant.

Response conversion normalizes `stop_reason`: `"end_turn"` becomes `"stop"`, `"tool_use"` becomes `"tool_calls"`, and `"max_tokens"` becomes `"length"`. Text content blocks are concatenated into `ChatResponse.Content`; `tool_use` blocks become `ToolCall` entries with arguments passed through `NormalizeArgs`.

For streaming, Anthropic uses named SSE event types rather than a single `data:` stream. `message_start` captures initial input tokens. `content_block_start` with a `tool_use` block emits a `StreamToolCallDelta` carrying the tool's ID and name. `content_block_delta` routes `text_delta` to `StreamContentDelta` and `input_json_delta` to `StreamToolCallDelta` with partial arguments. `message_delta` captures output tokens and computes totals. `message_stop` emits the final `StreamDone` event. The `ping` and `content_block_stop` events are ignored.

## 6. Streaming Protocol

Both providers follow the same consumer-facing protocol. `ChatStream` returns a `<-chan StreamEvent` (buffered, size 8) and spawns a goroutine that reads the HTTP response body. Content deltas arrive as `StreamContentDelta` events whose `.Content` fragments the consumer concatenates. Tool call deltas arrive as `StreamToolCallDelta` events: the first delta for a tool carries `ID` and `Name`, while subsequent deltas carry `Arguments` fragments that must be concatenated to reconstruct the full JSON. A single `StreamDone` event signals completion and carries final `Usage` stats. If anything goes wrong, a `StreamError` event is emitted and the channel is closed. The channel is always closed when the goroutine exits via `defer close(ch)`.

Context cancellation is handled by a shared `sendEvent` helper that selects between the channel send and `ctx.Done()`, so a cancelled context unblocks the producer goroutine immediately rather than leaking it. The streaming HTTP client sets `Timeout: 0`; lifetime is governed entirely by the context. Errors from SSE parsing or JSON unmarshalling are sent as `StreamError` events, then the goroutine returns and closes the channel. `ChatStream` does not use `doWithRetry` — only `Chat` (non-streaming) wraps its HTTP call in the retry loop. If the initial streaming HTTP request returns a non-200 status, the error is returned synchronously before the channel is created.

## 7. Retry Logic

Source: `internal/llm/retry.go`

Both `openaiClient.Chat` and `anthropicClient.Chat` wrap their HTTP calls in `doWithRetry`, which allows up to 3 attempts (1 initial + 2 retries) with a base backoff of 1 second and a maximum backoff cap of 30 seconds. Network errors and HTTP 429 (rate limit) or 5xx (server error) responses trigger a retry; other 4xx responses are returned as-is for the caller to parse into an `APIError`. The backoff delay doubles each attempt, is capped at 30 seconds, and respects the `Retry-After` header (integer seconds) on 429 responses when that value exceeds the computed delay. Jitter multiplies the delay by a random factor in [0.5, 1.5) using `math/rand/v2`. The sleep itself respects context cancellation via a select on `ctx.Done()`.

`APIError` carries `StatusCode`, `Message`, `Provider`, and a `Retryable` bool. Both providers set `Retryable = true` for 429 and >= 500 status codes.

## 8. Current Gaps

1. **No streaming retry.** `ChatStream` makes a single HTTP attempt. A transient 429 or 503 on the initial request will fail immediately. The non-streaming `Chat` path retries, but streaming does not.

2. **No request-level timeout for streaming.** The streaming HTTP client sets `Timeout: 0`. If the caller's context has no deadline, a stalled connection will block indefinitely.

3. **No token counting or context-window management.** The client reports usage after the fact but has no mechanism to pre-compute token counts or truncate messages to fit a model's context window.

4. **No structured error classification beyond `APIError`.** Authentication failures (401/403), invalid request (400), and content-policy rejections all surface as the same `APIError` with different status codes. Callers must inspect `StatusCode` manually.

5. **No image or multi-modal content support.** `Message.Content` is a plain string. Neither provider mapping handles image blocks, PDF inputs, or other non-text content types.

6. **No response caching or request deduplication.** Identical requests always hit the network.

7. **OpenAI `stream_options` not explicitly requested.** The code expects usage in the final streaming chunk, but the request does not set `stream_options: {include_usage: true}`. This works only if the server sends usage by default.

8. **Anthropic `tool_result` merge is fragile.** `convertMessages` merges consecutive tool results into the previous user message only if its `Content` is already `[]anthropicContentBlock`. If a plain-string user message precedes the tool results (unlikely in practice but possible), the merge path is skipped and the alternating-turn constraint may be violated.
