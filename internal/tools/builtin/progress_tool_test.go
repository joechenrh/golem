package builtin

import (
	"context"
	"encoding/json"
	"strings"
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

	t.Run("invalid JSON returns parse error", func(t *testing.T) {
		result, err := tool.Execute(context.Background(), `{bad json}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(result, "Error: invalid arguments:") {
			t.Errorf("expected parse error, got: %q", result)
		}
	})

	t.Run("metadata", func(t *testing.T) {
		if tool.Name() != "report_progress" {
			t.Errorf("unexpected name: %q", tool.Name())
		}
		if tool.Description() == "" {
			t.Error("expected non-empty description")
		}
		if tool.FullDescription() == "" {
			t.Error("expected non-empty full description")
		}
	})

	t.Run("parameters schema is valid JSON", func(t *testing.T) {
		var schema map[string]any
		if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
			t.Fatalf("invalid parameters JSON: %v", err)
		}
		if schema["type"] != "object" {
			t.Errorf("expected type=object, got %v", schema["type"])
		}
		required, _ := schema["required"].([]any)
		if len(required) != 1 || required[0] != "summary" {
			t.Errorf("expected required=[summary], got %v", required)
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
