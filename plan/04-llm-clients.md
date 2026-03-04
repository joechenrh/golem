# Step 4: LLM Clients

## Scope

Unified LLM client interface with OpenAI and Anthropic implementations, both streaming and non-streaming. Provider factory with routing by model name prefix.

## Files

- `internal/llm/client.go` — interface + factory
- `internal/llm/openai.go` — OpenAI Chat Completions API
- `internal/llm/anthropic.go` — Anthropic Messages API
- `internal/llm/stream.go` — SSE line parser shared by both providers

## Key Points

### Client Interface (`client.go`)

```go
type Client interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
    Provider() Provider
}

// NewClient creates a Client based on the provider string.
// model format: "openai:gpt-4o" or "anthropic:claude-sonnet-4-20250514"
func NewClient(provider Provider, apiKey string) (Client, error)
```

### Provider Routing

`llm.ParseModelProvider()` splits `"openai:gpt-4o"` → `(ProviderOpenAI, "gpt-4o")`. If no prefix, default to OpenAI. Note: `Config.ModelProvider()` was removed — use `llm.ParseModelProvider(cfg.Model)` to avoid logic duplication.

### OpenAI Implementation (`openai.go`)

**Endpoint**: `POST https://api.openai.com/v1/chat/completions`

**Key mapping**:
- `ChatRequest.SystemPrompt` → prepend as `{"role": "system", "content": ...}` message
- `ChatRequest.Tools` → `tools` array with `{"type": "function", "function": {...}}`
- Response `choices[0].message.tool_calls` → `ChatResponse.ToolCalls`
- `FinishReason: "tool_calls"` indicates tool calls present

**Streaming**:
- `stream: true` in request
- Parse SSE `data: {...}` lines via `stream.go`
- `choices[0].delta.content` → `StreamContentDelta`
- `choices[0].delta.tool_calls` → `StreamToolCallDelta` (accumulate fragments)
- `data: [DONE]` → `StreamDone`

### Anthropic Implementation (`anthropic.go`)

**Endpoint**: `POST https://api.anthropic.com/v1/messages`

**Key mapping**:
- `ChatRequest.SystemPrompt` → top-level `system` field (NOT a message)
- `ChatRequest.Messages` → filter out system role messages
- `ChatRequest.Tools` → `tools` array with `{"name": ..., "input_schema": ...}`
- Response `content` blocks with `type: "tool_use"` → `ChatResponse.ToolCalls`
- Tool results sent as `{"role": "user", "content": [{"type": "tool_result", ...}]}`

**Anthropic-specific quirks**:
- Must set `anthropic-version: 2023-06-01` header
- `x-api-key` header (not `Authorization: Bearer`)
- System prompt is a separate top-level field
- Tool call arguments are a JSON object (not a string) — must marshal to string for unified type
- Tool results go in a user message with `tool_result` content blocks
- Max tokens is required (default: 4096)

**Streaming**:
- `stream: true` in request
- SSE events: `content_block_start`, `content_block_delta`, `message_delta`, `message_stop`
- `content_block_delta` with `type: "text_delta"` → `StreamContentDelta`
- `content_block_delta` with `type: "input_json_delta"` → `StreamToolCallDelta`

### SSE Parser (`stream.go`)

```go
// ParseSSE reads from an io.Reader line by line, yielding data payloads.
// Handles "data: " prefix stripping, empty line event boundaries, [DONE] sentinel.
func ParseSSE(r io.Reader) <-chan string
```

Shared by both providers — they both use SSE format but with different JSON payloads.

### Retry Logic

Both clients implement retry with exponential backoff:
- Retryable: HTTP 429 (rate limit), 5xx (server error), network errors
- Non-retryable: 400, 401, 403, 404
- Max 3 retries, backoff: 1s → 2s → 4s with jitter
- On 429: respect `Retry-After` header if present

### Error Types

```go
type APIError struct {
    StatusCode int
    Message    string
    Provider   Provider
    Retryable  bool
}
```

## Implementation Notes (from code review)

### Wire-format separation

**Do NOT marshal the unified types (from `types.go`) directly to/from provider APIs.** OpenAI and Anthropic have very different JSON shapes:

- Anthropic `content` is an array of blocks, not a string
- Anthropic tool call arguments are a JSON object, OpenAI uses a JSON string
- Anthropic tool results go in a `user` message with `tool_result` content blocks
- Anthropic system prompt is a top-level field, not a message

**Solution**: Define private wire-format structs in each provider file (e.g., `openaiChatRequest`, `anthropicMessageRequest`) with their own JSON tags, and convert to/from the unified types. This keeps the provider API details out of the shared type definitions.

### JSON tags

All unified types in `types.go` already have `json:` struct tags (added during review). Wire-format structs must also have proper tags matching each provider's API spec.

### HTTP client timeout

Both client stubs already set `http.Client{Timeout: 120 * time.Second}`. For streaming requests, the timeout applies to the entire response, not per-chunk — consider using a context deadline instead for streaming, or setting Timeout to 0 for streaming and relying solely on context cancellation.

### `APIError.Provider` field

The field is named `Provider` (not `Prov`). Ensure error construction uses this field name.

### `ToolDefinition.Parameters`

This is `json.RawMessage` (not `map[string]interface{}`). When building wire-format request bodies, use it directly — no need to marshal/unmarshal through `interface{}`.

## Design Decisions

- Both providers use `net/http` directly — no third-party HTTP client needed
- Streaming returns a `<-chan StreamEvent` — consumer reads until channel closes
- Tool call argument format difference (OpenAI=string, Anthropic=object) is normalized in the provider layer
- Base URLs are hardcoded but overridable via env (`OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`) for proxies

## Done When

- `NewClient(ProviderOpenAI, key)` → can call `Chat()` and get response
- `NewClient(ProviderAnthropic, key)` → can call `Chat()` and get response
- `ChatStream()` yields token-by-token events
- Tool calls in response are correctly parsed into `[]ToolCall`
- Retry handles 429 gracefully
