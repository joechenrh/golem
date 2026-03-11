package agent

import (
	"context"
	"maps"
	"sync"
	"time"

	"github.com/joechenrh/golem/internal/hooks"
)

// EventAccumulator implements hooks.Hook and accumulates lifecycle events
// for progress tracking. Register it on a hooks.Bus to capture tool
// execution, iteration, and phase events. Call Snapshot() from any
// goroutine to read the current state without disrupting the ReAct loop.
type EventAccumulator struct {
	mu        sync.RWMutex
	events    []accEvent
	maxEvents int
	current   SessionState
}

// accEvent is an internal event record stored in the rolling window.
type accEvent struct {
	Type      hooks.EventType
	Timestamp time.Time
	Payload   map[string]any
}

// SessionState tracks the live progress of a session.
type SessionState struct {
	Iteration    int
	MaxIter      int
	ActiveTool   string
	ToolStarted  time.Time
	Phase        string
	RunningTasks int
	IdleSince    *time.Time
}

// StatusSnapshot is a point-in-time copy of the accumulator state,
// safe to read after Snapshot() returns.
type StatusSnapshot struct {
	State        SessionState
	RecentEvents []accEvent
}

// NewEventAccumulator creates an accumulator with the given rolling window size.
func NewEventAccumulator(maxEvents int) *EventAccumulator {
	return &EventAccumulator{
		events:    make([]accEvent, 0, maxEvents),
		maxEvents: maxEvents,
	}
}

// Name implements hooks.Hook.
func (a *EventAccumulator) Name() string { return "accumulator" }

// Handle implements hooks.Hook. Called by hooks.Bus.Emit for every event.
func (a *EventAccumulator) Handle(_ context.Context, event hooks.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Reset on turn start before recording the event.
	if event.Type == hooks.EventTurnStart {
		a.events = make([]accEvent, 0, a.maxEvents)
		a.current = SessionState{}
	}

	// Shallow-copy payload to avoid aliasing the caller's map.
	payload := make(map[string]any, len(event.Payload))
	maps.Copy(payload, event.Payload)

	entry := accEvent{
		Type:      event.Type,
		Timestamp: time.Now(),
		Payload:   payload,
	}

	// Copy-down eviction keeps the backing array at its pre-allocated capacity.
	if len(a.events) >= a.maxEvents {
		copy(a.events, a.events[1:])
		a.events[len(a.events)-1] = entry
	} else {
		a.events = append(a.events, entry)
	}

	switch event.Type {
	case hooks.EventBeforeToolExec:
		a.current.ActiveTool = payloadStr(event.Payload, "tool_name")
		a.current.ToolStarted = time.Now()
		a.current.IdleSince = nil
	case hooks.EventAfterToolExec:
		a.current.ActiveTool = ""
	case hooks.EventIterationStart:
		a.current.Iteration = payloadInt(event.Payload, "iteration")
		a.current.MaxIter = payloadInt(event.Payload, "max_iter")
		a.current.IdleSince = nil
	case hooks.EventPhaseUpdate:
		a.current.Phase = payloadStr(event.Payload, "summary")
	case hooks.EventTaskLaunched:
		a.current.RunningTasks++
	case hooks.EventTaskCompleted:
		a.current.RunningTasks = max(0, a.current.RunningTasks-1)
	case hooks.EventTurnDone:
		now := time.Now()
		a.current.IdleSince = &now
		a.current.ActiveTool = ""
	}

	return nil
}

// Snapshot returns a point-in-time copy of the current state and last 5 events.
// Safe to call concurrently from the StatusHandler goroutine.
func (a *EventAccumulator) Snapshot() StatusSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()

	n := min(5, len(a.events))
	recent := make([]accEvent, n)
	copy(recent, a.events[len(a.events)-n:])

	return StatusSnapshot{
		State:        a.current,
		RecentEvents: recent,
	}
}

// payloadStr extracts a string value from the event payload.
func payloadStr(p map[string]any, key string) string {
	v, _ := p[key].(string)
	return v
}

// payloadInt extracts an int value from the event payload.
// Handles both int and float64 (from JSON unmarshaling).
func payloadInt(p map[string]any, key string) int {
	switch v := p[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}
