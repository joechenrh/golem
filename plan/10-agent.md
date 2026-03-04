# Step 10: Agent Loop

## Scope

The core ReAct loop — orchestrates LLM calls, tool execution, and response routing. The heart of the framework. Maps to crabclaw's `core/agent_loop.rs` + `core/model_runner.rs`.

## File

`internal/agent/agent.go`

## Key Points

### AgentLoop Struct

```go
type AgentLoop struct {
    llm             llm.Client
    tools           *tools.Registry
    tape            tape.Store
    contextStrategy context.ContextStrategy  // pluggable context management
    hooks           *hooks.Bus               // lifecycle event bus
    router          *router.Router
    config          *config.Config
    logger          *zap.Logger
}

func New(llm llm.Client, tools *tools.Registry, tape tape.Store,
         ctxStrategy context.ContextStrategy, hookBus *hooks.Bus,
         cfg *config.Config, logger *zap.Logger) *AgentLoop
```

The agent loop emits events via `hooks.Bus` at lifecycle points. Cross-cutting concerns (memory, logging, safety) register as hooks — the agent doesn't call them directly.

### Two Execution Modes

```go
// HandleInput processes a user message, returns the final response string.
// Used by non-streaming channels (Telegram, Lark).
func (a *AgentLoop) HandleInput(ctx context.Context, msg channel.IncomingMessage) (string, error)

// HandleInputStream processes a user message with streaming.
// Tokens are sent to tokenCh as they arrive. Used by CLI.
func (a *AgentLoop) HandleInputStream(ctx context.Context, msg channel.IncomingMessage, tokenCh chan<- string) error
```

### ReAct Loop Flow (HandleInput)

```
1. ROUTE USER INPUT
   │
   ├─ router.RouteUser(msg.Text)
   │   ├─ IsCommand? → handleInternalCommand() or shell.Execute() → return result
   │   └─ Not command → continue to LLM
   │
2. RECORD TO TAPE
   │
   ├─ tape.Append(message entry with role=user)
   │
3. EMIT USER_MESSAGE HOOK
   │
   ├─ hooks.Emit(EventUserMessage, {text, channel_id})
   │   └─ MemoryHook injects relevant memories into event payload
   │
4. BUILD CONTEXT (via pluggable ContextStrategy)
   │
   ├─ entries = tape.Entries()
   ├─ messages = contextStrategy.BuildContext(ctx, entries, maxTokens)
   ├─ systemPrompt = buildSystemPrompt() + memory hints from hook
   │
5. TOOL-CALLING LOOP (max config.MaxToolIter iterations)
   │
   ├─ hooks.Emit(EventBeforeLLMCall, {messages, tools})
   ├─ req = ChatRequest{Model, SystemPrompt, Messages, Tools: registry.ToolDefinitions()}
   ├─ resp = llm.Chat(ctx, req)
   ├─ hooks.Emit(EventAfterLLMCall, {response, usage})
   │
   ├─ If resp.ToolCalls is non-empty:
   │   ├─ For each toolCall:
   │   │   ├─ hooks.Emit(EventBeforeToolExec, {tool_name, args})
   │   │   │   └─ SafetyHook can return error to block dangerous commands
   │   │   ├─ result = tools.Execute(ctx, toolCall.Name, toolCall.Arguments)
   │   │   ├─ hooks.Emit(EventAfterToolExec, {tool_name, result})
   │   │   └─ Append tool call + result to messages
   │   ├─ Detect tool hints in response → registry.Expand()
   │   └─ Loop back to step 4
   │
   ├─ If resp.Content (final answer):
   │   ├─ router.RouteAssistant(resp.Content) → detect embedded commands
   │   │   └─ Execute any detected commands, append results
   │   ├─ tape.Append(message entry with role=assistant)
   │   └─ Return resp.Content
   │
   └─ If max iterations reached:
       └─ Return error message: "Tool calling limit reached"
```

### System Prompt Construction

```go
func (a *AgentLoop) buildSystemPrompt() string
```

Assembles:
1. **Identity**: "You are golem, a helpful coding assistant."
2. **Workspace context**: current directory, date/time
3. **Tool-calling contract**: "When you need to perform actions, use the available tools."
4. **Available tools summary**: short list from registry
5. **Custom prompt**: from `.agent/system-prompt.md` if it exists in workspace

### Internal Command Handling

```go
func (a *AgentLoop) handleInternalCommand(cmd, args string) string
```

| Command | Handler |
|---|---|
| `help` | Return formatted list of all commands |
| `quit` | Signal shutdown via context cancellation |
| `tape.info` | Return `tape.Info()` formatted |
| `tape.search` | Return `tape.Search(args)` formatted |
| `tools` | Return `registry.List()` |
| `skills` | Return skills subset of `registry.List()` |
| `model` | Show current model or switch model |
| `anchor` | Call `tape.AddAnchor(args)` |

### Streaming Mode (HandleInputStream)

Same flow as HandleInput, but:
- Step 4 uses `llm.ChatStream()` instead of `llm.Chat()`
- Content deltas are sent to `tokenCh` as they arrive
- Tool calls are accumulated from stream deltas before execution
- After tool execution, the loop continues with non-streaming (tool results aren't streamed)
- Final response is streamed token-by-token

### Error Handling

- LLM API errors → retry is handled in the LLM layer; agent receives final error
- Tool execution errors → error message is returned as tool result (LLM sees the error and can adapt)
- Max iteration exceeded → return a clear message, don't crash

### Concurrency

Each `HandleInput` / `HandleInputStream` call is independent. Multiple concurrent requests from different channels are handled in separate goroutines. The tape store has mutex protection.

## Design Decisions

- Tool results are always strings — even errors are formatted as strings for the LLM
- System prompt is rebuilt on every call (not cached) — allows dynamic workspace detection
- Max 15 tool iterations is a safety limit, not a performance optimization
- Streaming is only for the final text response; tool call/result phases are not streamed to the user
- Assistant comma-command detection is done post-response — the agent can embed commands in its output

## Done When

- Ask "What is 2+2?" → LLM responds with answer, tape has user + assistant entries
- Ask "Read the go.mod file" → LLM calls `read_file` tool, returns file content
- Type `,help` → shows command list without calling LLM
- Type `,tape.info` → shows tape statistics
- Ask a question requiring 3+ tool calls → all tools execute, final answer returned
- 16+ tool call loop → returns "Tool calling limit reached"
