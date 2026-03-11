package builtin

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/hooks"
)

func TestReportProgressTool(t *testing.T) {
	bus := hooks.NewBus(zap.NewNop())

	// Capture emitted events.
	var captured []hooks.Event
	bus.Register(&captureHook{fn: func(e hooks.Event) { captured = append(captured, e) }})

	tool := NewReportProgressTool(bus)

	t.Run("emits phase_update event", func(t *testing.T) {
		captured = nil
		result, err := tool.Execute(context.Background(), `{"summary":"analyzing code"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "Progress reported." {
			t.Errorf("unexpected result: %q", result)
		}
		if len(captured) != 1 {
			t.Fatalf("expected 1 event, got %d", len(captured))
		}
		if captured[0].Type != hooks.EventPhaseUpdate {
			t.Errorf("expected phase_update event, got %q", captured[0].Type)
		}
		summary, _ := captured[0].Payload["summary"].(string)
		if summary != "analyzing code" {
			t.Errorf("expected summary %q, got %q", "analyzing code", summary)
		}
	})

	t.Run("requires summary", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), `{}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "Error: 'summary' is required" {
			t.Errorf("unexpected result: %q", result)
		}
	})
}

// captureHook captures all events for testing.
type captureHook struct {
	fn func(hooks.Event)
}

func (h *captureHook) Name() string { return "capture" }
func (h *captureHook) Handle(_ context.Context, e hooks.Event) error {
	h.fn(e)
	return nil
}
