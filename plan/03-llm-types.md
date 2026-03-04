# Step 3: LLM Types

## Scope

Define all shared types used across LLM providers. This is the data contract between the agent loop and LLM clients.

## File

`internal/llm/types.go`

## Key Points

### Core Types

```go
type Role string
const (
    RoleSystem    Role = "system"
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
    RoleTool      Role = "tool"
)

type Message struct {
    Role       Role
    Content    string
    ToolCalls  []ToolCall   // assistant messages may contain tool calls
    ToolCallID string       // for tool result messages
    Name       string       // tool name for tool results
}

type ToolCall struct {
    ID        string
    Name      string
    Arguments string  // raw JSON string
}

type ToolDefinition struct {
    Name        string
    Description string
    Parameters  map[string]interface{}  // JSON Schema object
}
```

### Request/Response

```go
type ChatRequest struct {
    Model        string
    Messages     []Message
    Tools        []ToolDefinition
    MaxTokens    int
    Temperature  float64
    SystemPrompt string  // separate system prompt (Anthropic requires this)
}

type ChatResponse struct {
    Content      string
    ToolCalls    []ToolCall
    Usage        Usage
    FinishReason string  // "stop", "tool_calls", "length"
}

type Usage struct {
    PromptTokens     int
    CompletionTokens int
    TotalTokens      int
}
```

### Streaming Types

```go
type StreamEvent struct {
    Type    StreamEventType
    Content string          // for content deltas
    ToolCall *ToolCallDelta // for tool call deltas
    Error   error           // for error events
}

type StreamEventType string
const (
    StreamContentDelta  StreamEventType = "content_delta"
    StreamToolCallDelta StreamEventType = "tool_call_delta"
    StreamDone          StreamEventType = "done"
    StreamError         StreamEventType = "error"
)

type ToolCallDelta struct {
    ID        string
    Name      string
    Arguments string  // partial JSON fragment
}
```

### Provider Enum

```go
type Provider string
const (
    ProviderOpenAI    Provider = "openai"
    ProviderAnthropic Provider = "anthropic"
)
```

### Design Decisions

- `ToolCall.Arguments` is a raw JSON string, not `map[string]interface{}` — the tool registry parses it, not the LLM layer
- `ChatRequest.SystemPrompt` is separate from `Messages` because Anthropic requires system as a top-level field, not a message
- `StreamEvent` uses a discriminated type field rather than separate channel types — simpler to consume
- `ToolDefinition.Parameters` is `json.RawMessage` — preserves raw JSON bytes without forcing deserialization into `interface{}`, avoids double-encoding issues when marshaling for API calls
- All structs have `json:"snake_case"` struct tags with `omitempty` where fields are optional — this is critical for correct serialization to tape/persistence and for any future direct JSON usage
- `StreamEvent` intentionally omits JSON tags — it is an in-memory-only type (streamed via channels, never serialized)

## Done When

- All types compile
- `encoding/json` is the only external dependency (stdlib)
- `json.Marshal`/`json.Unmarshal` round-trips correctly for all types
