package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/joechenrh/golem/internal/hooks"
)

func TestEventAccumulator_Handle(t *testing.T) {
	tests := []struct {
		name   string
		events []hooks.Event
		check  func(t *testing.T, snap StatusSnapshot)
	}{
		{
			name: "turn_start resets state",
			events: []hooks.Event{
				{Type: hooks.EventIterationStart, Payload: map[string]any{"iteration": 3, "max_iter": 15}},
				{Type: hooks.EventTurnStart, Payload: map[string]any{"user_message": "hello"}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.Iteration != 0 {
					t.Errorf("expected iteration 0 after reset, got %d", snap.State.Iteration)
				}
				// turn_start itself is in events (reset clears before appending)
				if len(snap.RecentEvents) != 1 {
					t.Errorf("expected 1 event after reset, got %d", len(snap.RecentEvents))
				}
			},
		},
		{
			name: "iteration_start updates state",
			events: []hooks.Event{
				{Type: hooks.EventIterationStart, Payload: map[string]any{"iteration": 5, "max_iter": 15}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.Iteration != 5 {
					t.Errorf("expected iteration 5, got %d", snap.State.Iteration)
				}
				if snap.State.MaxIter != 15 {
					t.Errorf("expected max_iter 15, got %d", snap.State.MaxIter)
				}
			},
		},
		{
			name: "tool lifecycle updates active tool",
			events: []hooks.Event{
				{Type: hooks.EventBeforeToolExec, Payload: map[string]any{"tool_name": "shell_exec"}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.ActiveTool != "shell_exec" {
					t.Errorf("expected active tool shell_exec, got %q", snap.State.ActiveTool)
				}
			},
		},
		{
			name: "after_tool_exec clears active tool",
			events: []hooks.Event{
				{Type: hooks.EventBeforeToolExec, Payload: map[string]any{"tool_name": "shell_exec"}},
				{Type: hooks.EventAfterToolExec, Payload: map[string]any{"tool_name": "shell_exec", "duration_ms": int64(100)}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.ActiveTool != "" {
					t.Errorf("expected no active tool, got %q", snap.State.ActiveTool)
				}
			},
		},
		{
			name: "phase_update sets phase",
			events: []hooks.Event{
				{Type: hooks.EventPhaseUpdate, Payload: map[string]any{"summary": "analyzing codebase"}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.Phase != "analyzing codebase" {
					t.Errorf("expected phase %q, got %q", "analyzing codebase", snap.State.Phase)
				}
			},
		},
		{
			name: "task_launched increments running tasks",
			events: []hooks.Event{
				{Type: hooks.EventTaskLaunched, Payload: map[string]any{"task_id": 1, "description": "test"}},
				{Type: hooks.EventTaskLaunched, Payload: map[string]any{"task_id": 2, "description": "test2"}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.RunningTasks != 2 {
					t.Errorf("expected 2 running tasks, got %d", snap.State.RunningTasks)
				}
			},
		},
		{
			name: "task_completed decrements running tasks",
			events: []hooks.Event{
				{Type: hooks.EventTaskLaunched, Payload: map[string]any{"task_id": 1, "description": "test"}},
				{Type: hooks.EventTaskCompleted, Payload: map[string]any{"task_id": 1, "result": "done"}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.RunningTasks != 0 {
					t.Errorf("expected 0 running tasks, got %d", snap.State.RunningTasks)
				}
			},
		},
		{
			name: "turn_done sets idle",
			events: []hooks.Event{
				{Type: hooks.EventTurnDone, Payload: map[string]any{"tokens_used": 100}},
			},
			check: func(t *testing.T, snap StatusSnapshot) {
				if snap.State.IdleSince == nil {
					t.Error("expected IdleSince to be set")
				}
				if snap.State.ActiveTool != "" {
					t.Errorf("expected no active tool after turn_done, got %q", snap.State.ActiveTool)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := NewEventAccumulator(50)
			for _, evt := range tt.events {
				acc.Handle(context.Background(), evt)
			}
			snap := acc.Snapshot()
			tt.check(t, snap)
		})
	}
}

func TestEventAccumulator_RollingWindow(t *testing.T) {
	acc := NewEventAccumulator(3) // tiny window
	for i := range 5 {
		acc.Handle(context.Background(), hooks.Event{
			Type:    hooks.EventIterationStart,
			Payload: map[string]any{"iteration": i, "max_iter": 15},
		})
	}
	snap := acc.Snapshot()
	// Window size 3, so only last 3 events kept. Snapshot returns min(5, len) = 3.
	if len(snap.RecentEvents) != 3 {
		t.Errorf("expected 3 recent events, got %d", len(snap.RecentEvents))
	}
}

func TestEventAccumulator_SnapshotLastN(t *testing.T) {
	acc := NewEventAccumulator(50)
	for i := range 10 {
		acc.Handle(context.Background(), hooks.Event{
			Type:    hooks.EventIterationStart,
			Payload: map[string]any{"iteration": i, "max_iter": 15},
		})
	}
	snap := acc.Snapshot()
	// Snapshot returns last 5 regardless of window size.
	if len(snap.RecentEvents) != 5 {
		t.Errorf("expected 5 recent events, got %d", len(snap.RecentEvents))
	}
}

func TestEventAccumulator_ConcurrentAccess(t *testing.T) {
	acc := NewEventAccumulator(50)
	var wg sync.WaitGroup

	// Writer goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 100 {
			acc.Handle(context.Background(), hooks.Event{
				Type:    hooks.EventIterationStart,
				Payload: map[string]any{"iteration": i, "max_iter": 100},
			})
		}
	}()

	// Reader goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			_ = acc.Snapshot()
		}
	}()

	wg.Wait()
	// If we get here without a race detector panic, concurrent access is safe.
}

func TestEventAccumulator_TaskCompletedNeverNegative(t *testing.T) {
	acc := NewEventAccumulator(50)
	// Complete without a prior launch — should not go negative.
	acc.Handle(context.Background(), hooks.Event{
		Type:    hooks.EventTaskCompleted,
		Payload: map[string]any{"task_id": 1},
	})
	snap := acc.Snapshot()
	if snap.State.RunningTasks != 0 {
		t.Errorf("expected 0 running tasks, got %d", snap.State.RunningTasks)
	}
}
