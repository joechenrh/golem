# How Golem Communicates with OpenAI

This document explains how the golem agent builds requests for the OpenAI Chat Completions API, sends them over HTTP, and decodes responses in both non-streaming and streaming modes.

## Architecture Overview

```
User Input
    |
    v
AgentLoop (internal/agent/agent.go)
    |
    |-- builds ChatRequest (unified type)
    v
llm.Client interface (internal/llm/client.go)
    |
    |-- Chat()       -> non-streaming HTTP POST
    |-- ChatStream() -> streaming HTTP POST (SSE)
    v
openaiClient (internal/llm/openai.go)
    |
    |-- buildRequest()      -> converts to wire format
    |-- convertResponse()   -> converts back to unified format
    |-- readStream()        -> parses SSE events
    v
OpenAI API (https://api.openai.com/v1/chat/completions)
```

## 1. Unified Types (`internal/llm/types.go`)

Golem uses provider-agnostic types internally. These get converted to OpenAI wire format before sending.

### Message

```go
type Message struct {
    Role       Role       // "system", "user", "assistant", "tool"
    Content    string     // text content
    ToolCalls  []ToolCall // present when assistant requests tool execution
    ToolCallID string     // present when role == "tool" (result of a tool call)
    Name       string     // tool name (for tool result messages)
}
```

### ToolCall

```go
type ToolCall struct {
    ID        string // unique ID assigned by OpenAI (e.g. "call_abc123")
    Name      string // tool function name (e.g. "shell_exec")
    Arguments string // raw JSON string of arguments (e.g. `{"command":"ls"}`)
}
```

### ChatRequest

```go
type ChatRequest struct {
    Model        string           // e.g. "gpt-4o"
    Messages     []Message        // conversation history
    Tools        []ToolDefinition // available tools with JSON Schema parameters
    SystemPrompt string           // handled separately for Anthropic compatibility
}
```

### ChatResponse

```go
type ChatResponse struct {
    Content      string     // text response
    ToolCalls    []ToolCall // tool calls the model wants to execute
    Usage        Usage      // token counts
    FinishReason string     // "stop" or "tool_calls"
}
```

## 2. Building the Request (`openaiClient.buildRequest`)

The `buildRequest` method in `internal/llm/openai.go:236` converts the unified `ChatRequest` into OpenAI's wire format.

### System Prompt

OpenAI accepts the system prompt as a regular message with `role: "system"`. It is prepended to the messages array:

```go
if req.SystemPrompt != "" {
    msgs = append(msgs, openaiMessage{
        Role:    "system",
        Content: req.SystemPrompt,
    })
}
```

The system prompt is built by `AgentLoop.buildSystemPrompt()` and includes:
- Agent identity ("You are golem, a helpful coding assistant.")
- Working directory and current time
- Instructions on tool usage
- Custom workspace prompt from `.agent/system-prompt.md` (if present)

### Messages

Each unified `Message` maps to an `openaiMessage`. Tool calls on assistant messages get wrapped in the nested `function` structure OpenAI expects:

```json
{
  "role": "assistant",
  "content": "",
  "tool_calls": [
    {
      "id": "call_abc123",
      "type": "function",
      "function": {
        "name": "shell_exec",
        "arguments": "{\"command\":\"ls -la\"}"
      }
    }
  ]
}
```

Tool result messages use `role: "tool"` with `tool_call_id` referencing the original call:

```json
{
  "role": "tool",
  "tool_call_id": "call_abc123",
  "name": "shell_exec",
  "content": "total 42\ndrwxr-xr-x ..."
}
```

### Tools

Each `ToolDefinition` becomes an OpenAI tool with `type: "function"`:

```json
{
  "type": "function",
  "function": {
    "name": "read_file",
    "description": "Read the contents of a file",
    "parameters": {
      "type": "object",
      "properties": {
        "path": {"type": "string", "description": "File path"}
      },
      "required": ["path"]
    }
  }
}
```

### Final Wire Format

The complete request sent to `POST /v1/chat/completions`:

```json
{
  "model": "gpt-4o",
  "messages": [
    {"role": "system", "content": "You are golem..."},
    {"role": "user", "content": "List files in the current directory"},
    {"role": "assistant", "content": "", "tool_calls": [...]},
    {"role": "tool", "tool_call_id": "call_xxx", "content": "..."}
  ],
  "tools": [...],
  "stream": false
}
```

## 3. Non-Streaming Flow (`openaiClient.Chat`)

Located at `internal/llm/openai.go:125`.

1. **Build request**: `buildRequest(req, false)` — `stream` is set to `false`.
2. **Serialize**: `json.Marshal` the wire request.
3. **Send with retry**: `doWithRetry` sends the HTTP POST with up to 3 attempts, exponential backoff with jitter, and `Retry-After` header support for 429s.
4. **Headers**: `Content-Type: application/json` and `Authorization: Bearer <api_key>`.
5. **Decode response**: JSON-decode into `openaiChatResponse`.
6. **Convert**: `convertResponse` extracts the first choice and maps back to unified `ChatResponse`. Tool call arguments are normalized (empty string → `"{}"`).

### Response Structure

OpenAI returns:

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": "Here are the files...",
        "tool_calls": [...]
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 150,
    "completion_tokens": 50,
    "total_tokens": 200
  }
}
```

`convertResponse` (`internal/llm/openai.go:290`) takes only `choices[0]` and maps it to the unified `ChatResponse`.

## 4. Streaming Flow (`openaiClient.ChatStream`)

Located at `internal/llm/openai.go:159`.

### Initiating the Stream

1. **Build request**: `buildRequest(req, true)` — `stream` is set to `true`.
2. **Send**: Single HTTP POST (no retry — the stream HTTP client has no timeout, relying on context cancellation).
3. **Return channel**: A `chan StreamEvent` (buffered, capacity 8) is returned immediately. A goroutine (`readStream`) processes the SSE body in the background.

### SSE Parsing (`internal/llm/stream.go`)

The `sseReader` is a pull-based parser that reads the HTTP response body line by line:

- Lines starting with `data:` contribute to the event's data field
- Lines starting with `event:` set the event type
- Lines starting with `:` are comments (ignored)
- Empty lines mark event boundaries

Each SSE event looks like:

```
data: {"choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"shell_exec","arguments":""}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\""}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"ls\"}"}}]},"finish_reason":null}]}

data: [DONE]
```

### Stream Event Processing (`readStream`)

Located at `internal/llm/openai.go:189`. For each SSE event:

1. If `data` is `"[DONE]"`, emit `StreamDone` and return.
2. JSON-unmarshal into `openaiStreamChunk`.
3. For each choice delta:
   - If `delta.content` is non-empty → emit `StreamContentDelta` with the text fragment.
   - If `delta.tool_calls` is present → emit `StreamToolCallDelta` with `ID`, `Name`, and `Arguments` fragment.

### Stream Event Types

```go
StreamContentDelta   // a text fragment of the response
StreamToolCallDelta  // a fragment of a tool call (ID, name, or argument piece)
StreamDone           // end of stream
StreamError          // an error occurred
```

## 5. Agent Loop: Assembling Stream into Response

The `AgentLoop.doStreamingCall` method (`internal/agent/agent.go:213`) consumes the stream channel and reassembles a complete `ChatResponse`:

### Content Assembly

Content deltas are concatenated into a `strings.Builder` and simultaneously forwarded to the UI via `tokenCh`:

```go
case llm.StreamContentDelta:
    contentBuf.WriteString(ev.Content)
    tokenCh <- ev.Content  // real-time display to user
```

### Tool Call Assembly

Tool calls arrive as fragments across multiple SSE events. The first delta carries the `ID` and `Name` (and potentially the beginning of `Arguments`). Subsequent deltas carry additional `Arguments` fragments:

```go
case llm.StreamToolCallDelta:
    tc := ev.ToolCall
    if tc.ID != "" {
        // First delta: create entry with ID, Name, and initial Arguments
        toolCallMap[tc.ID] = &llm.ToolCall{
            ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
        }
        toolCallOrder = append(toolCallOrder, tc.ID)
    } else if len(toolCallOrder) > 0 {
        // Subsequent delta: append arguments to most recent tool call
        lastID := toolCallOrder[len(toolCallOrder)-1]
        existing.Arguments += tc.Arguments
    }
```

After the stream ends, arguments are normalized (`""` → `"{}"`), and `FinishReason` is inferred from whether tool calls are present.

## 6. The ReAct Loop

The `runReActLoop` (`internal/agent/agent.go:106`) orchestrates multi-turn tool-calling:

```
                      ┌──────────────────────────┐
                      │  Build context from tape  │
                      │  (conversation history)   │
                      └──────────┬───────────────┘
                                 │
                                 v
                      ┌──────────────────────────┐
                      │  Send ChatRequest to LLM  │
                      │  (streaming or not)        │
                      └──────────┬───────────────┘
                                 │
                        ┌────────┴────────┐
                        │                 │
                   Has tool calls?    No tool calls
                        │                 │
                        v                 v
               ┌────────────────┐   ┌──────────┐
               │ Execute tools  │   │  Return   │
               │ Record results │   │  content  │
               │ to tape        │   │  (done)   │
               └───────┬────────┘   └──────────┘
                       │
                       └──────> loop back (up to MaxToolIter)
```

Each iteration:

1. **Read tape**: Get all conversation entries from the tape store.
2. **Build context**: Apply context strategy (masking) to fit within token limits.
3. **Build system prompt**: Includes workspace info and custom prompts.
4. **Call LLM**: Streaming (`doStreamingCall`) or non-streaming (`Chat`), depending on the channel.
5. **If tool calls**: Record the assistant message + tool calls to tape, execute each tool, record results, and loop.
6. **If no tool calls**: Record the final text response and return.

The loop exits after `MaxToolIter` (default: 15) iterations to prevent infinite loops.

## 7. Error Handling and Retry

The retry logic in `internal/llm/retry.go`:

- **Max attempts**: 3 (1 initial + 2 retries)
- **Retryable conditions**: HTTP 429 (rate limit) and 5xx (server errors), plus network errors
- **Non-retryable**: 4xx errors (except 429)
- **Backoff**: Exponential with base 1s (`1s → 2s → 4s`), capped at 30s
- **Jitter**: Random factor in `[0.5, 1.5)` to prevent thundering herd
- **Retry-After**: Honored on 429 responses if the header value exceeds the calculated backoff

## 8. Custom Provider Support

Any OpenAI-compatible API can be used by setting environment variables:

```bash
export DEEPSEEK_API_KEY="sk-xxx"
export DEEPSEEK_BASE_URL="https://api.deepseek.com/v1"
export GOLEM_MODEL="deepseek:deepseek-chat"
```

The provider is auto-registered in `main.go` using `llm.RegisterProvider()` with the `NewOpenAICompatibleClient` factory, which creates the same `openaiClient` with a different base URL.
