package hooks

import (
	"context"

	"go.uber.org/zap"
)

// LoggingHook logs agent lifecycle events at appropriate levels.
type LoggingHook struct {
	logger *zap.Logger
}

// NewLoggingHook creates a hook that logs events using the provided logger.
func NewLoggingHook(logger *zap.Logger) *LoggingHook {
	return &LoggingHook{logger: logger}
}

func (h *LoggingHook) Name() string { return "logging" }

func (h *LoggingHook) Handle(_ context.Context, event Event) error {
	fields := payloadFields(event.Payload)

	switch event.Type {
	case EventUserMessage:
		h.logger.Info("user message", fields...)
	case EventBeforeLLMCall:
		h.logger.Debug("before LLM call", fields...)
	case EventAfterLLMCall:
		h.logger.Info("after LLM call", fields...)
	case EventBeforeToolExec:
		h.logger.Info("before tool exec", fields...)
	case EventAfterToolExec:
		h.logger.Debug("after tool exec", fields...)
	case EventError:
		h.logger.Error("agent error", fields...)
	default:
		h.logger.Debug("unknown event", append(fields, zap.String("type", string(event.Type)))...)
	}

	return nil
}

func payloadFields(payload map[string]interface{}) []zap.Field {
	fields := make([]zap.Field, 0, len(payload))
	for k, v := range payload {
		fields = append(fields, zap.Any(k, v))
	}
	return fields
}
