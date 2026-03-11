package agent

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/hooks"
)

func TestProgressPipeline_Integration(t *testing.T) {
	bus := hooks.NewBus(zap.NewNop())
	acc := NewEventAccumulator(50)
	bus.Register(acc)

	ch := &mockChannel{}
	reporter := NewProgressReporter(ch, "test-chat", 0, zap.NewNop())
	bus.Register(reporter)

	ctx := context.Background()

	// Simulate a ReAct loop.
	bus.Emit(ctx, hooks.Event{Type: hooks.EventTurnStart, Payload: map[string]any{"user_message": "fix the bug"}})

	snap := acc.Snapshot()
	if snap.State.IdleSince != nil {
		t.Error("should not be idle after turn_start")
	}

	bus.Emit(ctx, hooks.Event{Type: hooks.EventIterationStart, Payload: map[string]any{"iteration": 0, "max_iter": 15}})

	bus.Emit(ctx, hooks.Event{Type: hooks.EventBeforeToolExec, Payload: map[string]any{"tool_name": "read_file"}})
	snap = acc.Snapshot()
	if snap.State.ActiveTool != "read_file" {
		t.Errorf("expected active tool read_file, got %q", snap.State.ActiveTool)
	}

	bus.Emit(ctx, hooks.Event{Type: hooks.EventAfterToolExec, Payload: map[string]any{"tool_name": "read_file", "duration_ms": int64(50)}})
	snap = acc.Snapshot()
	if snap.State.ActiveTool != "" {
		t.Errorf("expected no active tool, got %q", snap.State.ActiveTool)
	}

	// Phase update — should trigger reporter.
	bus.Emit(ctx, hooks.Event{Type: hooks.EventPhaseUpdate, Payload: map[string]any{"summary": "found the root cause"}})
	snap = acc.Snapshot()
	if snap.State.Phase != "found the root cause" {
		t.Errorf("expected phase %q, got %q", "found the root cause", snap.State.Phase)
	}

	msgs := ch.sentMessages()
	if len(msgs) != 1 || msgs[0] != "📋 found the root cause" {
		t.Errorf("expected 1 reporter message, got %v", msgs)
	}

	// Turn done.
	bus.Emit(ctx, hooks.Event{Type: hooks.EventTurnDone, Payload: map[string]any{"tokens_used": 500}})
	snap = acc.Snapshot()
	if snap.State.IdleSince == nil {
		t.Error("expected idle after turn_done")
	}
}
