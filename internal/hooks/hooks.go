package hooks

import (
	"context"
	"slices"
	"strings"
	"sync"

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

	// Progress tracking events.
	EventIterationStart EventType = "iteration_start"
	EventPhaseUpdate    EventType = "phase_update"
	EventTaskLaunched   EventType = "task_launched"
	EventTaskCompleted  EventType = "task_completed"
	EventTurnStart      EventType = "turn_start"
	EventTurnDone       EventType = "turn_done"
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
	mu     sync.RWMutex
	hooks  []Hook
	logger *zap.Logger
}

// NewBus creates an empty event bus.
func NewBus(logger *zap.Logger) *Bus {
	return &Bus{logger: logger}
}

// Register adds a hook to the bus. Hooks are called in registration order.
func (b *Bus) Register(h Hook) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hooks = append(b.hooks, h)
}

// BuildDefaultBus creates a Bus pre-loaded with the standard hooks:
// LoggingHook, SafetyHook, and optionally MetricsHook and AuditHook.
// This is the single source of truth for hook wiring so that all session
// creation paths (default, per-chat, restored, scheduled) are consistent.
func BuildDefaultBus(logger *zap.Logger, metricsHook *MetricsHook, auditPath string) (*Bus, *MetricsHook) {
	bus := NewBus(logger)
	bus.Register(NewLoggingHook(logger))
	bus.Register(NewSafetyHook())

	if metricsHook == nil {
		metricsHook = NewMetricsHook()
	}
	bus.Register(metricsHook)

	if auditPath != "" {
		if auditHook, err := NewAuditHook(auditPath); err != nil {
			logger.Warn("failed to create audit hook", zap.Error(err))
		} else {
			bus.Register(auditHook)
		}
	}
	return bus, metricsHook
}

// Emit sends an event to all hooks.
// For "before_*" events, the first error stops further hooks and returns the error.
// For other events, errors are logged but do not affect the main flow.
func (b *Bus) Emit(ctx context.Context, event Event) error {
	b.mu.RLock()
	hooks := slices.Clone(b.hooks)
	b.mu.RUnlock()

	isBefore := strings.HasPrefix(string(event.Type), "before_")

	for _, h := range hooks {
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
