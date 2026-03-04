# Step 8a: Hooks (Event Bus)

## Scope

Lifecycle event bus for cross-cutting concerns. The agent loop emits events at key points; hooks react without the agent knowing about them. Decouples logging, memory, and safety from the core loop.

## Files

- `internal/hooks/hooks.go` — Hook interface, Event type, Bus implementation
- `internal/hooks/logging.go` — LoggingHook (first concrete hook)

## Key Points

### Event Types

```go
type EventType string

const (
    EventUserMessage    EventType = "user_message"
    EventBeforeLLMCall  EventType = "before_llm_call"
    EventAfterLLMCall   EventType = "after_llm_call"
    EventBeforeToolExec EventType = "before_tool_exec"
    EventAfterToolExec  EventType = "after_tool_exec"
    EventError          EventType = "error"
)
```

### Event Struct

```go
type Event struct {
    Type    EventType
    Payload map[string]interface{}
}
```

Payload is intentionally untyped — hooks extract what they need. This avoids coupling the event definition to every possible consumer.

### Hook Interface (`hooks.go`)

```go
// Hook reacts to agent lifecycle events.
type Hook interface {
    // Name returns a unique identifier for this hook (for logging).
    Name() string

    // Handle processes an event. Returning an error from BeforeToolExec
    // blocks tool execution (used by safety hooks).
    Handle(ctx context.Context, event Event) error
}
```

### Bus (`hooks.go`)

```go
// Bus dispatches events to registered hooks.
type Bus struct {
    hooks []Hook
}

func NewBus() *Bus
func (b *Bus) Register(h Hook)

// Emit sends an event to all hooks. Returns the first error (if any).
// For "before_*" events, an error signals the caller to abort the action.
func (b *Bus) Emit(ctx context.Context, event Event) error
```

- Hooks are called in registration order.
- For `before_*` events, the first error stops further hooks and returns the error to the caller (e.g., safety hook blocks a dangerous command).
- For `after_*` events, errors are logged but do not affect the main flow.

### LoggingHook (`logging.go`)

```go
type LoggingHook struct {
    logger *zap.Logger
}

func NewLoggingHook(logger *zap.Logger) *LoggingHook
```

Logs each event at appropriate levels:
- `EventUserMessage` → info
- `EventBeforeLLMCall` → debug
- `EventAfterLLMCall` → info (includes token usage)
- `EventBeforeToolExec` → info (includes tool name)
- `EventAfterToolExec` → debug
- `EventError` → error

### Future Hooks (stubs, not implemented here)

- `SafetyHook` — blocks dangerous shell commands (e.g., `rm -rf /`)
- `MemoryHook` — injects relevant memories before LLM calls, stores new memories after

## Design Decisions

- `Bus.Emit` is synchronous — hooks run inline. Async hooks can spawn goroutines internally if needed.
- Payload is `map[string]interface{}` rather than typed structs to avoid a combinatorial explosion of event-specific types.
- `LoggingHook` requires `go.uber.org/zap` — this is the first step that adds zap as a dependency.

## Done When

- `Bus.Register(hook)` + `Bus.Emit(event)` dispatches to hook
- `LoggingHook` logs events at appropriate levels
- `before_tool_exec` error blocks further processing
- `Bus` with no hooks registered is a no-op (no panic)
