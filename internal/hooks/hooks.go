package hooks

import (
	"context"
	"strings"

	"go.uber.org/zap"
)

// EventType identifies a lifecycle event in the agent loop.
type EventType string

const (
	EventUserMessage    EventType = "user_message"
	EventBeforeLLMCall  EventType = "before_llm_call"
	EventAfterLLMCall   EventType = "after_llm_call"
	EventBeforeToolExec EventType = "before_tool_exec"
	EventAfterToolExec  EventType = "after_tool_exec"
	EventError          EventType = "error"
)

// Event represents a lifecycle event with an untyped payload.
type Event struct {
	Type    EventType
	Payload map[string]any
}

// Hook reacts to agent lifecycle events.
type Hook interface {
	// Name returns a unique identifier for this hook (for logging).
	Name() string

	// Handle processes an event. Returning an error from before_* events
	// blocks the action (e.g., safety hook blocks a dangerous command).
	Handle(ctx context.Context, event Event) error
}

// Bus dispatches events to registered hooks.
type Bus struct {
	hooks  []Hook
	logger *zap.Logger
}

// NewBus creates an empty event bus.
func NewBus(logger *zap.Logger) *Bus {
	return &Bus{logger: logger}
}

// Register adds a hook to the bus. Hooks are called in registration order.
func (b *Bus) Register(h Hook) {
	b.hooks = append(b.hooks, h)
}

// Emit sends an event to all hooks.
// For "before_*" events, the first error stops further hooks and returns the error.
// For other events, errors are logged but do not affect the main flow.
func (b *Bus) Emit(ctx context.Context, event Event) error {
	isBefore := strings.HasPrefix(string(event.Type), "before_")

	for _, h := range b.hooks {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := h.Handle(ctx, event); err != nil {
			if isBefore {
				return err
			}
			b.logger.Warn("hook error",
				zap.String("hook", h.Name()),
				zap.String("event", string(event.Type)),
				zap.Error(err),
			)
		}
	}
	return nil
}
